package engine

// graph_match.go — the graph-match operator: pattern planning,
// traversal, cycle control and the map/all/any/inner_nodes path
// functions.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// --- graph-match ---

// maxGraphMatches caps the number of rows graph-match may emit. Pattern
// enumeration is combinatorial; a runaway pattern should fail loudly
// rather than exhaust memory.
const maxGraphMatches = 1_000_000

// matchPredicate is one AND-conjunct of the where clause, scheduled for
// evaluation at the earliest pattern position where every pattern
// variable it references is bound. pos p means "after node p is bound"
// (node var i binds at pos i; edge var i binds at pos i+1, together with
// its target node). Conjuncts referencing no pattern variables run at
// pos 0. Evaluating conjuncts during traversal prunes dead branches
// before they expand; because `and` is eager (no short-circuit) and a
// match requires every conjunct strictly true, splitting is semantics-
// preserving, including error and null behavior.
type matchPredicate struct {
	expr parser.Expr
	pos  int
}

// splitConjuncts flattens top-level `and` chains into a conjunct list.
func splitConjuncts(expr parser.Expr, out []parser.Expr) []parser.Expr {
	if be, ok := expr.(*parser.BinaryExpr); ok && be.Op == parser.OpAnd {
		out = splitConjuncts(be.Left, out)
		return splitConjuncts(be.Right, out)
	}
	return append(out, expr)
}

// collectBaseNames gathers the base identifiers an expression references
// (ColumnRef names and AccessExpr base names). Returns complete=false if
// an unknown expression type is encountered, in which case the caller
// must schedule the conjunct at the final position (safe fallback).
func collectBaseNames(expr parser.Expr, names map[string]bool) (complete bool) {
	switch e := expr.(type) {
	case nil:
		return true
	case *parser.Literal:
		return true
	case *parser.ColumnRef:
		names[e.Name] = true
		return true
	case *parser.AccessExpr:
		for _, k := range e.Path {
			if k.Expr != nil && !collectBaseNames(k.Expr, names) {
				return false
			}
		}
		return collectBaseNames(e.Object, names)
	case *parser.BinaryExpr:
		return collectBaseNames(e.Left, names) && collectBaseNames(e.Right, names)
	case *parser.UnaryExpr:
		return collectBaseNames(e.Expr, names)
	case *parser.FuncCall:
		ok := true
		for _, arg := range e.Args {
			ok = collectBaseNames(arg, names) && ok
		}
		return ok
	case *parser.BetweenExpr:
		return collectBaseNames(e.Expr, names) &&
			collectBaseNames(e.Low, names) && collectBaseNames(e.High, names)
	case *parser.InExpr:
		ok := collectBaseNames(e.Column, names)
		for _, v := range e.Values {
			ok = collectBaseNames(v, names) && ok
		}
		return ok
	case *parser.HasAnyAllExpr:
		ok := collectBaseNames(e.Column, names)
		for _, v := range e.Values {
			ok = collectBaseNames(v, names) && ok
		}
		return ok
	default:
		return false
	}
}

// --- pattern plan ---

// gmVar is one node variable of the pattern. Named variables are shared
// by every occurrence (within and across sequences); each anonymous
// "()" is its own variable.
type gmVar struct {
	name      string // "" = anonymous
	offset    int    // first column in the match schema (-1 = anonymous)
	pos       int    // plan position at which it becomes bound
	labelsOff int    // hidden labels column (-1 = not used)
	nodeOff   int    // hidden node reference column (-1 = not used)
}

// gmElem is one edge element of the pattern, between node variables
// left and right in written order.
type gmElem struct {
	spec        parser.GraphMatchEdge
	left, right int
	offset      int // first column in the match schema (-1 = anonymous)
	pathOff     int // hidden graph-path column (-1 = anonymous)
	pos         int // plan position at which it becomes bound
	labelsOff   int // hidden labels column (-1 = not used)
}

// gmStep traverses one edge element from an already bound variable.
// reversed: traversal runs right→left, so the direction flips and the
// collected path is reversed back into written order before writing.
type gmStep struct {
	elem     int
	from, to int
	dir      parser.EdgeDirection
	reversed bool
	toBound  bool // to was bound by an earlier step (or is from itself)
}

// gmPlan is a compiled graph-match pattern. Position 0 binds vars[0]
// (the first node of the first sequence); position k (k >= 1) is after
// steps[k-1].
type gmPlan struct {
	vars   []gmVar
	elems  []gmElem
	steps  []gmStep
	schema types.Schema
	varPos map[string]int
}

// graphPathPrefix names the hidden per-edge-variable column holding a
// *graphPath, which map/all/any/inner_nodes read. The NUL keeps it
// unreachable from user-written names.
const graphPathPrefix = "\x00graph-path:"

// graphLabelsPrefix names the hidden labels column of a variable read
// by labels(v); graphElemLabelsColumn holds the current element's
// labels inside map/all/any. All hidden graph columns share
// graphHiddenPrefix.
const (
	graphHiddenPrefix     = "\x00graph-"
	graphLabelsPrefix     = "\x00graph-labels:"
	graphElemLabelsColumn = "\x00graph-elem-labels"
)

func graphPathColumn(name string) string   { return graphPathPrefix + name }
func graphLabelsColumn(name string) string { return graphLabelsPrefix + name }

// graphNodePrefix names the hidden column holding a *graphNodeRef for a
// node variable read by node_id/node_degree_in/node_degree_out;
// graphElemNodeColumn holds the current inner node inside map/all/any.
const (
	graphNodePrefix     = "\x00graph-node:"
	graphElemNodeColumn = "\x00graph-elem-node"
)

func graphNodeColumn(name string) string { return graphNodePrefix + name }

// graphNodeRef identifies one node of a graph for the node functions.
type graphNodeRef struct {
	g  *Graph
	id types.Value
}

// graphPath is the traversal behind an edge variable: the node it
// starts from in written (left-to-right) order and its edges in order.
type graphPath struct {
	g     *Graph
	start types.Value
	edges []int
}

// nodes returns the path's node sequence, start first.
func (p *graphPath) nodes() []types.Value {
	out := make([]types.Value, 0, len(p.edges)+1)
	cur := p.start
	out = append(out, cur)
	for _, ei := range p.edges {
		e := p.g.Edges[ei]
		if graphKey(e.Source) == graphKey(cur) {
			cur = e.Target
		} else {
			cur = e.Source
		}
		out = append(out, cur)
	}
	return out
}

// walkFuncCalls calls fn for every function call in expr.
// complete=false means an expression shape was not understood; callers
// must then assume the worst.
func walkFuncCalls(expr parser.Expr, fn func(*parser.FuncCall)) (complete bool) {
	switch e := expr.(type) {
	case nil, *parser.Literal, *parser.ColumnRef:
		return true
	case *parser.FuncCall:
		fn(e)
		ok := true
		for _, a := range e.Args {
			ok = walkFuncCalls(a, fn) && ok
		}
		return ok
	case *parser.AccessExpr:
		for _, k := range e.Path {
			if k.Expr != nil && !walkFuncCalls(k.Expr, fn) {
				return false
			}
		}
		return walkFuncCalls(e.Object, fn)
	case *parser.BinaryExpr:
		return walkFuncCalls(e.Left, fn) && walkFuncCalls(e.Right, fn)
	case *parser.UnaryExpr:
		return walkFuncCalls(e.Expr, fn)
	case *parser.BetweenExpr:
		return walkFuncCalls(e.Expr, fn) && walkFuncCalls(e.Low, fn) && walkFuncCalls(e.High, fn)
	case *parser.InExpr:
		ok := walkFuncCalls(e.Column, fn)
		for _, v := range e.Values {
			ok = walkFuncCalls(v, fn) && ok
		}
		return ok
	case *parser.HasAnyAllExpr:
		ok := walkFuncCalls(e.Column, fn)
		for _, v := range e.Values {
			ok = walkFuncCalls(v, fn) && ok
		}
		return ok
	default:
		return false
	}
}

// graphUses is what a graph-match's expressions need from the walk:
// the edge variables map/all/any iterate (paths), the variables
// labels(v) reads, and whether labels() appears without an argument.
type graphUses struct {
	paths, labels, nodes map[string]bool
	all                  bool // some expression was not understood: assume everything
}

// graphNodeFuncs take a node variable (or, inside map/all/any over
// inner_nodes, no argument) and read the node's identity or degree.
var graphNodeFuncs = map[string]bool{"node_id": true, "node_degree_in": true, "node_degree_out": true}

func collectGraphUses(op *parser.GraphMatchOp) graphUses {
	u := graphUses{paths: map[string]bool{}, labels: map[string]bool{}, nodes: map[string]bool{}}
	fn := func(fc *parser.FuncCall) {
		switch {
		case (fc.Name == "map" || fc.Name == "all" || fc.Name == "any") && len(fc.Args) == 2:
			src := fc.Args[0]
			if c, ok := src.(*parser.FuncCall); ok && c.Name == "inner_nodes" && len(c.Args) == 1 {
				src = c.Args[0]
			}
			if r, ok := src.(*parser.ColumnRef); ok {
				u.paths[r.Name] = true
			}
		case fc.Name == "labels" && len(fc.Args) == 1:
			if r, ok := fc.Args[0].(*parser.ColumnRef); ok {
				u.labels[r.Name] = true
			}
		case graphNodeFuncs[fc.Name] && len(fc.Args) == 1:
			if r, ok := fc.Args[0].(*parser.ColumnRef); ok {
				u.nodes[r.Name] = true
			}
		}
	}
	if !walkFuncCalls(op.Where, fn) {
		u.all = true
	}
	if op.Project != nil {
		for _, it := range op.Project.Items {
			if !walkFuncCalls(it.Expr, fn) {
				u.all = true
			}
		}
	}
	return u
}

func (e *Engine) buildGraphMatchPlan(g *Graph, op *parser.GraphMatchOp) (*gmPlan, error) {
	if len(op.Sequences) == 0 {
		return nil, fmt.Errorf("graph-match: missing pattern")
	}
	// resolve evaluates scalar hop bounds (-[e*lo..hi]->).
	resolve := func(spec parser.GraphMatchEdge) (parser.GraphMatchEdge, error) {
		if spec.MinExpr == nil {
			return spec, nil
		}
		bound := func(x parser.Expr) (int, error) {
			v, err := evalExpr(e, x, &types.Schema{}, types.Row{})
			if err != nil {
				return 0, fmt.Errorf("graph-match hop range: %w", err)
			}
			switch n := v.(type) {
			case int64:
				return int(n), nil
			case int32:
				return int(n), nil
			case float64:
				if n == float64(int(n)) {
					return int(n), nil
				}
			}
			return 0, fmt.Errorf("graph-match hop range: bounds must be integer constants, got %v", v)
		}
		lo, err := bound(spec.MinExpr)
		if err != nil {
			return spec, err
		}
		hi, err := bound(spec.MaxExpr)
		if err != nil {
			return spec, err
		}
		err = parser.CheckHopRange(lo, hi, fmt.Sprintf("*%d..%d", lo, hi), &spec)
		return spec, err
	}
	plan := &gmPlan{varPos: make(map[string]int)}
	named := make(map[string]int) // node var name → var index
	edgeNames := make(map[string]bool)

	varFor := func(name string) int {
		if name != "" {
			if v, ok := named[name]; ok {
				return v
			}
			named[name] = len(plan.vars)
		}
		plan.vars = append(plan.vars, gmVar{name: name, offset: -1, pos: -1, labelsOff: -1, nodeOff: -1})
		return len(plan.vars) - 1
	}
	for _, seq := range op.Sequences {
		if len(seq.Nodes) != len(seq.Edges)+1 {
			return nil, fmt.Errorf("graph-match: malformed pattern sequence")
		}
		prev := varFor(seq.Nodes[0])
		for i, spec := range seq.Edges {
			spec, err := resolve(spec)
			if err != nil {
				return nil, err
			}
			next := varFor(seq.Nodes[i+1])
			if spec.Name != "" {
				if edgeNames[spec.Name] {
					return nil, fmt.Errorf("graph-match: duplicate pattern variable %q", spec.Name)
				}
				edgeNames[spec.Name] = true
			}
			plan.elems = append(plan.elems, gmElem{spec: spec, left: prev, right: next, offset: -1, pathOff: -1, labelsOff: -1})
			prev = next
		}
	}
	for name := range edgeNames {
		if _, clash := named[name]; clash {
			return nil, fmt.Errorf("graph-match: duplicate pattern variable %q", name)
		}
	}

	// Order the traversal: from the first node, repeatedly take the
	// first unprocessed edge element touching a bound variable.
	bound := make([]bool, len(plan.vars))
	bound[0] = true
	plan.vars[0].pos = 0
	done := make([]bool, len(plan.elems))
	for len(plan.steps) < len(plan.elems) {
		picked := -1
		for i, el := range plan.elems {
			if !done[i] && (bound[el.left] || bound[el.right]) {
				picked = i
				break
			}
		}
		if picked < 0 {
			return nil, errGraphDisconnected
		}
		el := &plan.elems[picked]
		st := gmStep{elem: picked, from: el.left, to: el.right, dir: el.spec.Direction}
		if !bound[el.left] {
			st.from, st.to, st.reversed = el.right, el.left, true
			switch st.dir {
			case parser.EdgeForward:
				st.dir = parser.EdgeBackward
			case parser.EdgeBackward:
				st.dir = parser.EdgeForward
			}
		}
		pos := len(plan.steps) + 1
		st.toBound = bound[st.to]
		if !st.toBound {
			bound[st.to] = true
			plan.vars[st.to].pos = pos
		}
		el.pos = pos
		done[picked] = true
		plan.steps = append(plan.steps, st)
	}
	for _, b := range bound {
		if !b {
			return nil, errGraphDisconnected
		}
	}

	// Match schema: node variables, then edge variables, then the
	// hidden graph-path columns.
	nodeSchema := g.nodeElemSchema()
	for i := range plan.vars {
		v := &plan.vars[i]
		if v.name == "" {
			continue
		}
		v.offset = len(plan.schema.Columns)
		plan.varPos[v.name] = v.pos
		for _, col := range nodeSchema.Columns {
			plan.schema.Columns = append(plan.schema.Columns,
				types.Column{Name: v.name + "." + col.Name, Type: col.Type})
		}
	}
	for i := range plan.elems {
		el := &plan.elems[i]
		if el.spec.Name == "" {
			continue
		}
		el.offset = len(plan.schema.Columns)
		plan.varPos[el.spec.Name] = el.pos
		if !el.spec.VarLength {
			for _, col := range g.EdgeSchema.Columns {
				plan.schema.Columns = append(plan.schema.Columns,
					types.Column{Name: el.spec.Name + "." + col.Name, Type: col.Type})
			}
			continue
		}
		// Variable-length edge: a dynamic column holding the JSON array
		// of traversed edges, so array_length(e) and e[0].Col work
		// through the standard dynamic machinery.
		plan.schema.Columns = append(plan.schema.Columns,
			types.Column{Name: el.spec.Name, Type: types.TypeDynamic})
	}
	// Hidden graph paths and labels only for the variables that
	// map/all/any and labels() use.
	uses := collectGraphUses(op)
	for i := range plan.elems {
		el := &plan.elems[i]
		if el.spec.Name == "" || !(uses.all || uses.paths[el.spec.Name]) {
			continue
		}
		el.pathOff = len(plan.schema.Columns)
		plan.schema.Columns = append(plan.schema.Columns,
			types.Column{Name: graphPathColumn(el.spec.Name), Type: types.TypeDynamic})
	}
	for i := range plan.vars {
		v := &plan.vars[i]
		if v.name != "" && (uses.all || uses.nodes[v.name]) {
			v.nodeOff = len(plan.schema.Columns)
			plan.schema.Columns = append(plan.schema.Columns,
				types.Column{Name: graphNodeColumn(v.name), Type: types.TypeDynamic})
		}
		if v.name != "" && (uses.all || uses.labels[v.name]) {
			v.labelsOff = len(plan.schema.Columns)
			plan.schema.Columns = append(plan.schema.Columns,
				types.Column{Name: graphLabelsColumn(v.name), Type: types.TypeDynamic})
		}
	}
	for i := range plan.elems {
		el := &plan.elems[i]
		if el.spec.Name != "" && (uses.all || uses.labels[el.spec.Name]) {
			el.labelsOff = len(plan.schema.Columns)
			plan.schema.Columns = append(plan.schema.Columns,
				types.Column{Name: graphLabelsColumn(el.spec.Name), Type: types.TypeDynamic})
		}
	}
	return plan, nil
}

var errGraphDisconnected = fmt.Errorf("graph-match: pattern sequences must be connected through shared node variables (only a single connected component is supported)")

// buildMatchPredicates splits the where clause into position-scheduled
// conjuncts: each runs at the latest binding position among the pattern
// variables it references.
func (plan *gmPlan) buildMatchPredicates(where parser.Expr) []matchPredicate {
	if where == nil {
		return nil
	}
	finalPos := len(plan.steps)
	conjuncts := splitConjuncts(where, nil)
	preds := make([]matchPredicate, 0, len(conjuncts))
	for _, c := range conjuncts {
		names := make(map[string]bool)
		pos := 0
		if !collectBaseNames(c, names) {
			pos = finalPos // unknown expr shape: evaluate at emit time
		} else {
			for n := range names {
				p, ok := plan.varPos[n]
				if !ok {
					// A literal dotted reference ("a.Kind") maps to the
					// variable named by its first segment.
					if dot := strings.IndexByte(n, '.'); dot > 0 {
						p, ok = plan.varPos[n[:dot]]
					}
				}
				if ok && p > pos {
					pos = p
				}
			}
		}
		preds = append(preds, matchPredicate{expr: c, pos: pos})
	}
	return preds
}

// writeNode fills node variable v's columns in the row buffer.
func (plan *gmPlan) writeNode(g *Graph, row types.Row, v int, id types.Value) {
	off := plan.vars[v].offset
	if off < 0 {
		return
	}
	copy(row[off:], g.nodeElemRow(id))
	if lo := plan.vars[v].labelsOff; lo >= 0 {
		row[lo] = g.nodeLabelsJSON(id)
	}
	if no := plan.vars[v].nodeOff; no >= 0 {
		row[no] = &graphNodeRef{g: g, id: id}
	}
}

// writePath fills edge element el's columns: the edge's columns for a
// fixed edge, the JSON edge array for a variable-length one, plus the
// hidden graph path. edges are in written order, starting at start.
func (plan *gmPlan) writePath(g *Graph, row types.Row, el int, start types.Value, edges []int) error {
	elem := &plan.elems[el]
	if elem.offset < 0 {
		return nil
	}
	if elem.labelsOff >= 0 {
		if elem.spec.VarLength {
			parts := make([]string, len(edges))
			for i, ei := range edges {
				parts[i] = g.edgeLabelsJSON(ei)
			}
			row[elem.labelsOff] = "[" + strings.Join(parts, ",") + "]"
		} else {
			row[elem.labelsOff] = g.edgeLabelsJSON(edges[0])
		}
	}
	if elem.pathOff >= 0 {
		cp := make([]int, len(edges))
		copy(cp, edges)
		row[elem.pathOff] = &graphPath{g: g, start: start, edges: cp}
	}
	if !elem.spec.VarLength {
		copy(row[elem.offset:], g.Edges[edges[0]].Row)
		return nil
	}
	list := make([]map[string]interface{}, len(edges))
	for i, ei := range edges {
		m := make(map[string]interface{}, len(g.EdgeSchema.Columns))
		for c, col := range g.EdgeSchema.Columns {
			v := g.Edges[ei].Row[c]
			// Dynamic edge columns are stored as JSON strings; embed
			// them raw so e[0].Tags.sev keeps working.
			if col.Type == types.TypeDynamic {
				if s, ok := v.(string); ok && json.Valid([]byte(s)) {
					m[col.Name] = json.RawMessage(s)
					continue
				}
			}
			m[col.Name] = v
		}
		list[i] = m
	}
	b, err := json.Marshal(list)
	if err != nil {
		return fmt.Errorf("graph-match: marshal edge list: %w", err)
	}
	row[elem.offset] = string(b)
	return nil
}

// graphMatchCollectCap sizes the shared edge-collection buffer.
const graphMatchCollectCap = 64

// applyGraphMatch enumerates bindings of the pattern, filters them with
// the where clause and projects the result. Traversal is depth-first
// over the plan's steps. The where clause is split into AND-conjuncts,
// each evaluated at the earliest position where its pattern variables
// are all bound, pruning dead branches during traversal instead of
// after full enumeration. Cycle control: unique_edges (default) uses a
// graph edge at most once per match; none also visits each graph node
// at most once (a repeated node variable is the same visit); all only
// bounds the hop ranges. Total output is capped at maxGraphMatches.
func (e *Engine) applyGraphMatch(g *Graph, op *parser.GraphMatchOp) (*types.Table, error) {
	plan, err := e.buildGraphMatchPlan(g, op)
	if err != nil {
		return nil, err
	}
	preds := plan.buildMatchPredicates(op.Where)

	if op.Shortest != "" {
		return e.applyGraphShortestPaths(g, op, plan, preds)
	}

	for i, el := range plan.elems {
		if !el.spec.Distinct {
			continue
		}
		if len(plan.elems) != 1 || el.left == el.right || i != 0 {
			return nil, fmt.Errorf("graph-match: 'distinct' is only valid on a single-edge pattern " +
				"(start)-[e*min..max distinct]-(end) — node-identity dedup does not compose with " +
				"continuing the rest of a longer pattern")
		}
		return e.applyGraphMatchDistinctBFS(g, op, plan, preds)
	}

	matches := types.NewTable("", plan.schema)
	err = e.walkGraphMatch(g, plan, preds, op.Cycles, func(row types.Row, _ []string, _ int) error {
		if len(matches.Rows) >= maxGraphMatches {
			return fmt.Errorf("graph-match: result exceeds %d rows; narrow the pattern or where clause", maxGraphMatches)
		}
		out := make(types.Row, len(row))
		copy(out, row)
		matches.AddRow(out)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return e.applyProject(matches, plan.projectWithGraphNames(op.Project))
}

// projectWithGraphNames renames unnamed project items the way
// Kusto.Language does for pattern properties (GraphResultName).
func (plan *gmPlan) projectWithGraphNames(p *parser.ProjectOp) *parser.ProjectOp {
	isVar := func(n string) bool { _, ok := plan.varPos[n]; return ok }
	out := &parser.ProjectOp{Items: append([]parser.ProjectItem(nil), p.Items...)}
	for i, it := range out.Items {
		if it.Unnamed && it.Expr != nil {
			if n, ok := parser.GraphResultName(it.Expr, isVar); ok {
				out.Items[i].Name = n
			}
		}
	}
	return out
}

// walkGraphMatch enumerates the bindings of plan that satisfy preds,
// calling emit with the (reused) row buffer, the node keys bound to
// each pattern variable and the match's total hop count.
func (e *Engine) walkGraphMatch(g *Graph, plan *gmPlan, preds []matchPredicate, cycles string,
	emitFn func(row types.Row, bindKey []string, hops int) error) error {
	uniqueEdges := cycles != "all"
	noCycles := cycles == "none"

	row := make(types.Row, len(plan.schema.Columns)) // reused buffer
	usedEdges := make([]bool, len(g.Edges))
	bindKey := make([]string, len(plan.vars))
	bindId := make([]types.Value, len(plan.vars))
	var visits map[string]int
	if noCycles {
		visits = make(map[string]int)
	}

	var radj map[string][]int
	for _, st := range plan.steps {
		if st.dir != parser.EdgeForward {
			radj = g.reverseAdjacency()
			break
		}
	}
	neighbors := func(key string, dir parser.EdgeDirection, fn func(ei int, next types.Value) error) error {
		if dir != parser.EdgeBackward {
			for _, ei := range g.Adjacency[key] {
				if err := fn(ei, g.Edges[ei].Target); err != nil {
					return err
				}
			}
		}
		if dir != parser.EdgeForward {
			for _, ei := range radj[key] {
				if err := fn(ei, g.Edges[ei].Source); err != nil {
					return err
				}
			}
		}
		return nil
	}

	checkPreds := func(pos int) (bool, error) {
		for _, p := range preds {
			if p.pos != pos {
				continue
			}
			keep, evalErr := evalExpr(e, p.expr, &plan.schema, row)
			if evalErr != nil {
				return false, fmt.Errorf("graph-match where: %w", evalErr)
			}
			if b, ok := keep.(bool); !ok || !b {
				return false, nil
			}
		}
		return true, nil
	}

	// Reverse-reachability pruning (graph_reach.go): only for a single
	// edge between two distinct variables whose end is pinned to a
	// literal that is verified to be a REAL node — a false match would
	// prune correct paths, so this fails closed on any uncertainty.
	var reachDist map[string]int
	var reachMaxHops int
	if len(plan.steps) == 1 && !plan.steps[0].toBound {
		if _, endVal, ok := detectFixedEndpoints(preds, 1); ok {
			targetKey := graphKey(endVal)
			if _, isRealNode := g.NodeRows[targetKey]; isRealNode {
				reachMaxHops = plan.elems[0].spec.MaxHops
				reachDist = reachabilityToTarget(g, targetKey, plan.steps[0].dir, reachMaxHops)
			}
		}
	}

	collected := make([]int, 0, graphMatchCollectCap)
	var walk func(stepIdx int) error
	walk = func(stepIdx int) error {
		if stepIdx == len(plan.steps) {
			return emitFn(row, bindKey, len(collected))
		}
		st := plan.steps[stepIdx]
		spec := plan.elems[st.elem].spec
		pos := stepIdx + 1
		base := len(collected)

		// handoff binds the arrival node and the edge element, checks
		// the predicates due at pos and continues with the next step.
		handoff := func(key string, id types.Value) error {
			if st.toBound && key != bindKey[st.to] {
				return nil
			}
			path := collected[base:]
			start := bindId[st.from]
			if st.reversed {
				rev := make([]int, len(path))
				for i, ei := range path {
					rev[len(path)-1-i] = ei
				}
				path = rev
				start = id
			}
			if !st.toBound {
				bindKey[st.to], bindId[st.to] = key, id
				plan.writeNode(g, row, st.to, id)
			}
			if err := plan.writePath(g, row, st.elem, start, path); err != nil {
				return err
			}
			keep, err := checkPreds(pos)
			if err != nil || !keep {
				return err
			}
			return walk(stepIdx + 1)
		}

		var expand func(depth int, key string, id types.Value) error
		expand = func(depth int, key string, id types.Value) error {
			if depth >= spec.MinHops {
				if err := handoff(key, id); err != nil {
					return err
				}
			}
			if depth == spec.MaxHops {
				return nil
			}
			return neighbors(key, st.dir, func(ei int, next types.Value) error {
				if uniqueEdges && usedEdges[ei] {
					return nil
				}
				nk := graphKey(next)
				if reachDist != nil {
					d, reachable := reachDist[nk]
					if !reachable || depth+1+d > reachMaxHops {
						return nil // provably cannot reach the pinned target in budget
					}
				}
				terminalOnly := false
				if noCycles && visits[nk] > 0 {
					// Already visited: only acceptable as the arrival at
					// an already bound endpoint, never passed through.
					if !st.toBound || nk != bindKey[st.to] || depth+1 < spec.MinHops {
						return nil
					}
					terminalOnly = true
				}
				usedEdges[ei] = true
				collected = append(collected, ei)
				var err error
				if terminalOnly {
					err = handoff(nk, next)
				} else {
					if noCycles {
						visits[nk]++
					}
					err = expand(depth+1, nk, next)
					if noCycles {
						visits[nk]--
					}
				}
				collected = collected[:len(collected)-1]
				usedEdges[ei] = false
				return err
			})
		}
		return expand(0, bindKey[st.from], bindId[st.from])
	}

	for _, id := range g.NodeIDs {
		key := graphKey(id)
		bindKey[0], bindId[0] = key, id
		plan.writeNode(g, row, 0, id)
		keep, err := checkPreds(0)
		if err != nil {
			return err
		}
		if !keep {
			continue
		}
		if noCycles {
			visits[key]++
		}
		err = walk(0)
		if noCycles {
			visits[key]--
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// applyGraphShortestPaths is graph-shortest-paths: per (first node,
// last node) pair, the matches with the fewest hops — one of them for
// output=any, every one for output=all. Enumeration does not revisit
// nodes (a shortest path never needs to), which bounds the search.
func (e *Engine) applyGraphShortestPaths(g *Graph, op *parser.GraphMatchOp, plan *gmPlan, preds []matchPredicate) (*types.Table, error) {
	if len(op.Sequences) != 1 {
		return nil, fmt.Errorf("graph-shortest-paths: the pattern can't contain multiple sequences")
	}
	hasVar := false
	for _, el := range plan.elems {
		hasVar = hasVar || el.spec.VarLength
	}
	if !hasVar {
		return nil, fmt.Errorf("graph-shortest-paths: the pattern must include a variable length edge")
	}
	seq := op.Sequences[0]
	first := 0
	last := len(plan.vars) - 1
	if n := seq.Nodes[len(seq.Nodes)-1]; n != "" {
		for i, v := range plan.vars {
			if v.name == n {
				last = i
			}
		}
	}

	type best struct {
		hops int
		rows []types.Row
	}
	byPair := make(map[string]*best)
	var order []string
	all := op.Shortest == "all"
	total := 0
	cycles := op.Cycles
	if cycles == "" {
		cycles = "none"
	}
	err := e.walkGraphMatch(g, plan, preds, cycles, func(row types.Row, bindKey []string, hops int) error {
		pair := bindKey[first] + "\x00" + bindKey[last]
		b := byPair[pair]
		if b == nil {
			b = &best{hops: hops}
			byPair[pair] = b
			order = append(order, pair)
		}
		switch {
		case hops > b.hops, hops == b.hops && !all && len(b.rows) > 0:
			return nil
		case hops < b.hops:
			total -= len(b.rows)
			b.hops, b.rows = hops, b.rows[:0]
		}
		if total >= maxGraphMatches {
			return fmt.Errorf("graph-shortest-paths: result exceeds %d rows; narrow the pattern or where clause", maxGraphMatches)
		}
		out := make(types.Row, len(row))
		copy(out, row)
		b.rows = append(b.rows, out)
		total++
		return nil
	})
	if err != nil {
		return nil, err
	}
	matches := types.NewTable("", plan.schema)
	for _, pair := range order {
		for _, r := range byPair[pair].rows {
			matches.AddRow(r)
		}
	}
	return e.applyProject(matches, plan.projectWithGraphNames(op.Project))
}

// --- graph path functions: map, all, any, inner_nodes ---

type elemSchemaKey struct {
	outer *types.Schema
	nodes bool
}

// elemSchema is the evaluation schema for a per-element expression:
// the element's properties first, so bare names resolve to them, then
// the enclosing row's columns.
func (g *Graph) elemSchema(outer *types.Schema, nodes bool) (*types.Schema, int) {
	var cols []types.Column
	if nodes {
		cols = g.nodeElemSchema().Columns
	} else {
		cols = g.EdgeSchema.Columns
	}
	width := len(cols) + 2 // + the element's labels and node reference
	g.elemMu.Lock()
	defer g.elemMu.Unlock()
	key := elemSchemaKey{outer, nodes}
	if s, ok := g.elemSchemas[key]; ok {
		return s, width
	}
	s := &types.Schema{}
	s.Columns = append(s.Columns, cols...)
	s.Columns = append(s.Columns, types.Column{Name: graphElemLabelsColumn, Type: types.TypeDynamic},
		types.Column{Name: graphElemNodeColumn, Type: types.TypeDynamic})
	s.Columns = append(s.Columns, outer.Columns...)
	if g.elemSchemas == nil {
		g.elemSchemas = make(map[elemSchemaKey]*types.Schema)
	}
	g.elemSchemas[key] = s
	return s, width
}

// evalGraphPathFunc evaluates map(), all() and any() over the edges of
// an edge variable or, through inner_nodes(e), over the nodes strictly
// inside its path. The second argument is evaluated once per element
// with the element's properties in scope by bare name. Zero elements:
// all() is true, any() false, map() an empty array. handled is false
// when the call is not over a graph-match variable (e.g. the any()
// aggregation reaching scalar evaluation), so dispatch continues.
func evalGraphPathFunc(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "labels":
		return evalGraphLabels(fc, schema, row)
	case "node_id", "node_degree_in", "node_degree_out":
		return evalGraphNodeFunc(fc, schema, row)
	case "inner_nodes":
		if schemaHasGraphPaths(schema) {
			return nil, true, fmt.Errorf("inner_nodes() can only be used as the first argument of all(), any() or map()")
		}
		return nil, false, nil
	case "map", "all", "any":
	default:
		return nil, false, nil
	}
	if len(fc.Args) != 2 {
		return nil, false, nil
	}
	src := fc.Args[0]
	innerNodes := false
	if call, ok := src.(*parser.FuncCall); ok && call.Name == "inner_nodes" {
		if len(call.Args) != 1 {
			return nil, true, fmt.Errorf("inner_nodes() expects one edge variable")
		}
		innerNodes = true
		src = call.Args[0]
	}
	ref, ok := src.(*parser.ColumnRef)
	if !ok {
		if innerNodes {
			return nil, true, fmt.Errorf("inner_nodes() expects an edge variable")
		}
		return nil, false, nil
	}
	idx := schema.ColumnIndex(graphPathColumn(ref.Name))
	if idx < 0 {
		if innerNodes {
			return nil, true, fmt.Errorf("inner_nodes(): %q is not an edge variable of the pattern", ref.Name)
		}
		return nil, false, nil
	}
	path, _ := row[idx].(*graphPath)
	if path == nil {
		return nil, true, nil
	}
	g := path.g

	elemSchema, width := g.elemSchema(schema, innerNodes)
	var elems []types.Row
	var elemLabels []string
	var elemNodes []types.Value
	if innerNodes {
		nodes := path.nodes()
		for i := 1; i < len(nodes)-1; i++ {
			elems = append(elems, g.nodeElemRow(nodes[i]))
			elemLabels = append(elemLabels, g.nodeLabelsJSON(nodes[i]))
			elemNodes = append(elemNodes, &graphNodeRef{g: g, id: nodes[i]})
		}
	} else {
		for _, ei := range path.edges {
			elems = append(elems, g.Edges[ei].Row)
			elemLabels = append(elemLabels, g.edgeLabelsJSON(ei))
			elemNodes = append(elemNodes, nil)
		}
	}

	buf := make(types.Row, width+len(row))
	copy(buf[width:], row)
	var mapped []interface{}
	var mapType types.KQLType
	if fc.Name == "map" {
		mapped = make([]interface{}, 0, len(elems))
		mapType = inferExprType(eng, fc.Args[1], elemSchema)
	}
	for i, er := range elems {
		copy(buf, er)
		buf[width-2] = elemLabels[i]
		buf[width-1] = elemNodes[i]
		v, err := evalExpr(eng, fc.Args[1], elemSchema, buf)
		if err != nil {
			return nil, true, err
		}
		switch fc.Name {
		case "all":
			if b, ok := v.(bool); !ok || !b {
				return false, true, nil
			}
		case "any":
			if b, ok := v.(bool); ok && b {
				return true, true, nil
			}
		default:
			if s, ok := v.(string); ok && mapType == types.TypeDynamic && json.Valid([]byte(s)) {
				mapped = append(mapped, json.RawMessage(s))
			} else {
				mapped = append(mapped, valueForJSONArray(v, mapType))
			}
		}
	}
	switch fc.Name {
	case "all":
		return true, true, nil
	case "any":
		return false, true, nil
	}
	b, err := json.Marshal(mapped)
	if err != nil {
		return nil, true, err
	}
	return string(b), true, nil
}

// graphPathFuncType is the result type of map/all/any over a graph-match
// variable (ok=false when the call is not one).
func graphPathFuncType(fc *parser.FuncCall, schema *types.Schema) (types.KQLType, bool) {
	if fc.Name == "labels" && len(fc.Args) <= 1 && schemaHasGraphPaths(schema) {
		return types.TypeDynamic, true
	}
	if graphNodeFuncs[fc.Name] && len(fc.Args) <= 1 && schemaHasGraphPaths(schema) {
		if fc.Name == "node_id" {
			return types.TypeString, true
		}
		return types.TypeLong, true
	}
	if (fc.Name != "map" && fc.Name != "all" && fc.Name != "any") || len(fc.Args) != 2 || !schemaHasGraphPaths(schema) {
		return types.TypeDynamic, false
	}
	if fc.Name == "map" {
		return types.TypeDynamic, true
	}
	return types.TypeBool, true
}

func schemaHasGraphPaths(schema *types.Schema) bool {
	if schema == nil {
		return false
	}
	for i := len(schema.Columns) - 1; i >= 0; i-- {
		if strings.HasPrefix(schema.Columns[i].Name, graphHiddenPrefix) {
			return true
		}
	}
	return false
}

// evalGraphLabels is labels(v) for a graph-match variable, or labels()
// for the current element inside map/all/any: a dynamic array of the
// element's labels (empty for graphs built with make-graph).
func evalGraphLabels(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if !schemaHasGraphPaths(schema) {
		return nil, false, nil
	}
	switch len(fc.Args) {
	case 0:
		if idx := schema.ColumnIndex(graphElemLabelsColumn); idx >= 0 {
			return row[idx], true, nil
		}
		return nil, true, fmt.Errorf("labels() without an argument is only valid inside all(), any() or map()")
	case 1:
		ref, ok := fc.Args[0].(*parser.ColumnRef)
		if !ok {
			return nil, true, fmt.Errorf("labels() expects a node or edge variable")
		}
		if idx := schema.ColumnIndex(graphLabelsColumn(ref.Name)); idx >= 0 {
			return row[idx], true, nil
		}
		return nil, true, fmt.Errorf("labels(): %q is not a node or edge variable of the pattern", ref.Name)
	}
	return nil, true, fmt.Errorf("labels() expects at most one argument")
}

// evalGraphNodeFunc is node_id(v), node_degree_in(v) and
// node_degree_out(v) for a node variable, or the argument-less forms
// for the current inner node inside map/all/any. node_id is the node's
// id as a string; the degrees count incoming and outgoing edges.
func evalGraphNodeFunc(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if !schemaHasGraphPaths(schema) {
		return nil, false, nil
	}
	var ref *graphNodeRef
	switch len(fc.Args) {
	case 0:
		idx := schema.ColumnIndex(graphElemNodeColumn)
		if idx < 0 {
			return nil, true, fmt.Errorf("%s() without an argument is only valid inside all(), any() or map() over inner_nodes()", fc.Name)
		}
		ref, _ = row[idx].(*graphNodeRef)
		if ref == nil {
			return nil, true, fmt.Errorf("%s() without an argument needs inner_nodes(): edges have no %s", fc.Name, fc.Name)
		}
	case 1:
		r, ok := fc.Args[0].(*parser.ColumnRef)
		if !ok {
			return nil, true, fmt.Errorf("%s() expects a node variable", fc.Name)
		}
		idx := schema.ColumnIndex(graphNodeColumn(r.Name))
		if idx < 0 {
			return nil, true, fmt.Errorf("%s(): %q is not a node variable of the pattern", fc.Name, r.Name)
		}
		ref, _ = row[idx].(*graphNodeRef)
		if ref == nil {
			return nil, true, nil
		}
	default:
		return nil, true, fmt.Errorf("%s() expects at most one argument", fc.Name)
	}
	key := graphKey(ref.id)
	switch fc.Name {
	case "node_id":
		return fmt.Sprintf("%v", ref.id), true, nil
	case "node_degree_in":
		return int64(len(ref.g.reverseAdjacency()[key])), true, nil
	default:
		return int64(len(ref.g.Adjacency[key])), true, nil
	}
}
