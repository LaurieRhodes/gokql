package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// Graph is the in-memory result of make-graph: a directed graph whose edges
// carry full edge-table rows and whose nodes optionally carry node-table rows.
// It flows between pipeline operators via pipeState (see applyPipeline) and
// never appears as a *types.Table — the engine's tabular invariant holds for
// every non-graph operator.
type Graph struct {
	EdgeSchema types.Schema
	NodeSchema types.Schema // zero-value unless a node table was supplied

	Edges []GraphEdge

	// Node bookkeeping. Keys are formatted node id values (graphKey).
	NodeIDs   []types.Value        // deterministic node order (first-seen)
	NodeRows  map[string]types.Row // node key → node-table row (nil = edge-only node)
	Adjacency map[string][]int     // node key → outgoing edge indices into Edges

	SourceIdx, TargetIdx int // column indices in EdgeSchema
	NodeIdIdx            int // node id column index in NodeSchema (if hasNodeTable)
	hasNodeTable         bool
}

// GraphEdge is one directed edge with its full source row for property access.
type GraphEdge struct {
	Source, Target types.Value
	Row            types.Row
}

// derivedNodeIdType is the declared type of the synthetic NodeId column
// when nodes are derived from edge endpoints (no node table). If the
// source and target columns disagree, ids of both types coexist, so the
// column widens to string and derivedNodeIdValue formats accordingly.
func (g *Graph) derivedNodeIdType() types.KQLType {
	st := g.EdgeSchema.Columns[g.SourceIdx].Type
	tt := g.EdgeSchema.Columns[g.TargetIdx].Type
	if st == tt {
		return st
	}
	return types.TypeString
}

// derivedNodeIdValue converts a node id for storage under the derived
// NodeId column: identity normally, formatted string when mixed endpoint
// types forced the column to widen.
func (g *Graph) derivedNodeIdValue(id types.Value) types.Value {
	if g.EdgeSchema.Columns[g.SourceIdx].Type == g.EdgeSchema.Columns[g.TargetIdx].Type {
		return id
	}
	if id == nil {
		return nil
	}
	return fmt.Sprintf("%v", id)
}

// graphKey formats a node id value as a map key. Type-tagged via
// typedKey (join.go) so string "1" and long 1 are distinct nodes.
func graphKey(v types.Value) string {
	return typedKey(v)
}

// pipeState carries the intermediate result between pipeline operators.
// Exactly one of table/graph is non-nil: tabular operators consume and
// produce tables, make-graph produces a graph, and graph-* operators
// consume it.
type pipeState struct {
	table *types.Table
	graph *Graph
}

// applyPipeline threads the input table through the operator pipeline,
// handling the graph-typed intermediate state introduced by make-graph.
// All top-level pipelines route through here; applyOperator remains the
// pure table→table dispatch for every non-graph operator. Adjacent
// `sort by ... | take N` pairs fuse into a bounded top-N heap
// (applyTopN) instead of a full sort.
func (e *Engine) applyPipeline(input *types.Table, ops []parser.Operator) (*types.Table, error) {
	st := pipeState{table: input}
	for i := 0; i < len(ops); i++ {
		if st.graph == nil {
			if ob, isOrder := ops[i].(*parser.OrderByOp); isOrder && i+1 < len(ops) {
				if tk, isTake := ops[i+1].(*parser.TakeOp); isTake {
					t, err := e.applyTopN(st.table, ob, tk.Count)
					if err != nil {
						return nil, err
					}
					st = pipeState{table: t}
					i++ // consumed the take as well
					continue
				}
			}
		}
		var err error
		st, err = e.applyOperatorState(st, ops[i])
		if err != nil {
			return nil, err
		}
	}
	if st.graph != nil {
		return nil, fmt.Errorf("query result is a graph; add graph-to-table (or graph-match) to produce tabular output")
	}
	return st.table, nil
}

func (e *Engine) applyOperatorState(st pipeState, op parser.Operator) (pipeState, error) {
	switch o := op.(type) {
	case *parser.MakeGraphOp:
		if st.graph != nil {
			return st, fmt.Errorf("make-graph: input is already a graph")
		}
		g, err := e.applyMakeGraph(st.table, o)
		if err != nil {
			return st, err
		}
		return pipeState{graph: g}, nil

	case *parser.GraphToTableOp:
		if st.graph == nil {
			return st, fmt.Errorf("graph-to-table: input is not a graph (make-graph must precede it)")
		}
		t, err := applyGraphToTable(st.graph, o)
		if err != nil {
			return st, err
		}
		return pipeState{table: t}, nil

	case *parser.GraphMatchOp:
		if st.graph == nil {
			return st, fmt.Errorf("graph-match: input is not a graph (make-graph must precede it)")
		}
		t, err := e.applyGraphMatch(st.graph, o)
		if err != nil {
			return st, err
		}
		return pipeState{table: t}, nil

	default:
		if st.graph != nil {
			return st, fmt.Errorf("%T: operator requires tabular input but received a graph (add graph-to-table first)", op)
		}
		t, err := e.applyOperator(st.table, op)
		if err != nil {
			return st, err
		}
		return pipeState{table: t}, nil
	}
}

// applyMakeGraph builds a Graph from the edge table. Edges with a null
// source or target are dropped. When a node table is named, its rows are
// attached as node properties and isolated nodes (present in the node table
// but absent from any edge) join the node set; name resolution against let
// bindings and the catalog happens inside executeQuery, as with lookup.
func (e *Engine) applyMakeGraph(edges *types.Table, op *parser.MakeGraphOp) (*Graph, error) {
	srcIdx := edges.Schema.ColumnIndex(op.SourceColumn)
	if srcIdx < 0 {
		return nil, fmt.Errorf("make-graph: source column %q not found", op.SourceColumn)
	}
	tgtIdx := edges.Schema.ColumnIndex(op.TargetColumn)
	if tgtIdx < 0 {
		return nil, fmt.Errorf("make-graph: target column %q not found", op.TargetColumn)
	}

	g := &Graph{
		EdgeSchema: edges.Schema,
		NodeRows:   make(map[string]types.Row),
		Adjacency:  make(map[string][]int),
		SourceIdx:  srcIdx,
		TargetIdx:  tgtIdx,
	}

	// Load the node table FIRST (if any) so its id-column type is known
	// before any node key is computed. This is what lets keyOf below
	// coerce consistently across both edge endpoints and node-table
	// ids, rather than the node table racing edges that already used
	// uncoerced keys — found live during the backlog pass: a node
	// table's NodeId typed differently from the edge endpoints (e.g.
	// NodeId: string vs Edges Src/Dst: long) previously matched nobody
	// — typedKey's cross-type distinctness (91f9794, correct for join
	// semantics) meant long 1 and string "1" silently became two
	// different graph nodes instead of one, with no error.
	var nodes *types.Table
	if op.NodesTable != "" {
		var err error
		nodes, err = e.executeQuery(&parser.Query{Source: op.NodesTable})
		if err != nil {
			return nil, fmt.Errorf("make-graph nodes: %w", err)
		}
		idIdx := nodes.Schema.ColumnIndex(op.NodeIdColumn)
		if idIdx < 0 {
			return nil, fmt.Errorf("make-graph: node id column %q not found in %s", op.NodeIdColumn, op.NodesTable)
		}
		g.NodeSchema = nodes.Schema
		g.NodeIdIdx = idIdx
		g.hasNodeTable = true
	}

	// coerceIdentity is true when the node table's id type disagrees
	// with the edge endpoint type(s), mirroring derivedNodeIdType/
	// Value's existing widen-to-string behavior for mismatched edge
	// endpoints alone — the same policy, extended to also cover the
	// node-table case rather than leaving it silently uncoerced.
	// Coercing the IDENTITY VALUE itself at extraction (rather than
	// only the key computation) is deliberate: graph-to-table and
	// graph-match's traversal code both call graphKey(id) directly in
	// many places, and propagating a coercion flag through every one
	// of those call sites would be invasive and easy to miss one of.
	// Coercing the value once, here, means every existing graphKey(id)
	// call downstream — completely unchanged — naturally computes a
	// consistent key, because the values it's given already agree.
	coerceIdentity := false
	if g.hasNodeTable {
		nodeIDType := nodes.Schema.Columns[g.NodeIdIdx].Type
		if nodeIDType != g.derivedNodeIdType() {
			coerceIdentity = true
		}
	}
	identity := func(id types.Value) types.Value {
		if coerceIdentity && id != nil {
			return fmt.Sprintf("%v", id)
		}
		return id
	}

	addNode := func(id types.Value) string {
		key := graphKey(id)
		if _, seen := g.NodeRows[key]; !seen {
			g.NodeRows[key] = nil
			g.NodeIDs = append(g.NodeIDs, id)
		}
		return key
	}

	for _, row := range edges.Rows {
		src := identity(row[srcIdx])
		tgt := identity(row[tgtIdx])
		if src == nil || tgt == nil {
			continue
		}
		srcKey := addNode(src)
		addNode(tgt)
		g.Edges = append(g.Edges, GraphEdge{Source: src, Target: tgt, Row: row})
		g.Adjacency[srcKey] = append(g.Adjacency[srcKey], len(g.Edges)-1)
	}

	if g.hasNodeTable {
		for _, row := range nodes.Rows {
			id := identity(row[g.NodeIdIdx])
			if id == nil {
				continue
			}
			key := addNode(id)
			g.NodeRows[key] = row
		}
	}

	return g, nil
}

// applyGraphToTable materializes the graph as rows.
//   - edges: original edge rows under the edge schema
//   - nodes with a node table: node-table rows in first-seen node order;
//     edge-only nodes emit their id with nulls for property columns
//   - nodes without a node table: a single NodeId column typed from the
//     edge source column
func applyGraphToTable(g *Graph, op *parser.GraphToTableOp) (*types.Table, error) {
	switch op.Output {
	case "edges":
		result := types.NewTable("", g.EdgeSchema)
		for _, edge := range g.Edges {
			result.AddRow(edge.Row)
		}
		return result, nil

	case "nodes":
		if g.hasNodeTable {
			result := types.NewTable("", g.NodeSchema)
			for _, id := range g.NodeIDs {
				row := g.NodeRows[graphKey(id)]
				if row == nil {
					row = make(types.Row, len(g.NodeSchema.Columns))
					row[g.NodeIdIdx] = id
				}
				result.AddRow(row)
			}
			return result, nil
		}
		idType := g.derivedNodeIdType()
		schema := types.Schema{Columns: []types.Column{{Name: "NodeId", Type: idType}}}
		result := types.NewTable("", schema)
		for _, id := range g.NodeIDs {
			result.AddRow(types.Row{g.derivedNodeIdValue(id)})
		}
		return result, nil

	default:
		return nil, fmt.Errorf("graph-to-table: unknown output %q", op.Output)
	}
}

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

// buildMatchPredicates splits the where clause into position-scheduled
// conjuncts. finalPos is len(op.Edges) — the position of the last node.
func buildMatchPredicates(op *parser.GraphMatchOp) []matchPredicate {
	if op.Where == nil {
		return nil
	}
	varPos := make(map[string]int)
	for i, name := range op.Nodes {
		if name != "" {
			varPos[name] = i
		}
	}
	for i, edge := range op.Edges {
		if edge.Name != "" {
			varPos[edge.Name] = i + 1
		}
	}
	finalPos := len(op.Edges)

	conjuncts := splitConjuncts(op.Where, nil)
	preds := make([]matchPredicate, 0, len(conjuncts))
	for _, c := range conjuncts {
		names := make(map[string]bool)
		pos := 0
		if !collectBaseNames(c, names) {
			pos = finalPos // unknown expr shape: evaluate at emit time
		} else {
			for n := range names {
				p, ok := varPos[n]
				if !ok {
					// A literal dotted reference ("a.Kind") maps to the
					// variable named by its first segment.
					if dot := strings.IndexByte(n, '.'); dot > 0 {
						p, ok = varPos[n[:dot]]
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

// matchLayout maps pattern variables onto the synthetic match schema.
// Each named node variable contributes the node columns as "var.Col"
// (or a single "var.NodeId" when nodes were derived from edges); each
// named edge variable contributes the edge columns as "var.Col".
// Anonymous elements contribute no columns.
type matchLayout struct {
	schema      types.Schema
	nodeOffsets []int // per pattern node: first column index, -1 if anonymous
	edgeOffsets []int // per pattern edge: first column index, -1 if anonymous
	nodeWidth   int
	edgeWidth   int
}

func buildMatchLayout(g *Graph, op *parser.GraphMatchOp) (*matchLayout, error) {
	lay := &matchLayout{
		nodeOffsets: make([]int, len(op.Nodes)),
		edgeOffsets: make([]int, len(op.Edges)),
		edgeWidth:   len(g.EdgeSchema.Columns),
	}
	if g.hasNodeTable {
		lay.nodeWidth = len(g.NodeSchema.Columns)
	} else {
		lay.nodeWidth = 1
	}

	seen := make(map[string]bool)
	claim := func(name string) error {
		if name == "" {
			return nil
		}
		if seen[name] {
			return fmt.Errorf("graph-match: duplicate pattern variable %q", name)
		}
		seen[name] = true
		return nil
	}

	addNodeVar := func(i int, name string) {
		lay.nodeOffsets[i] = len(lay.schema.Columns)
		if g.hasNodeTable {
			for _, col := range g.NodeSchema.Columns {
				lay.schema.Columns = append(lay.schema.Columns,
					types.Column{Name: name + "." + col.Name, Type: col.Type})
			}
		} else {
			lay.schema.Columns = append(lay.schema.Columns,
				types.Column{Name: name + ".NodeId", Type: g.derivedNodeIdType()})
		}
	}
	addEdgeVar := func(i int, edge parser.GraphMatchEdge) {
		lay.edgeOffsets[i] = len(lay.schema.Columns)
		if edge.MinHops == 1 && edge.MaxHops == 1 {
			for _, col := range g.EdgeSchema.Columns {
				lay.schema.Columns = append(lay.schema.Columns,
					types.Column{Name: edge.Name + "." + col.Name, Type: col.Type})
			}
			return
		}
		// Variable-length edge: a single dynamic column holding the JSON
		// array of traversed edges, so array_length(e) and e[0].Col work
		// through the standard dynamic machinery.
		lay.schema.Columns = append(lay.schema.Columns,
			types.Column{Name: edge.Name, Type: types.TypeDynamic})
	}

	for i, name := range op.Nodes {
		if err := claim(name); err != nil {
			return nil, err
		}
		if name == "" {
			lay.nodeOffsets[i] = -1
			continue
		}
		addNodeVar(i, name)
	}
	for i, edge := range op.Edges {
		if err := claim(edge.Name); err != nil {
			return nil, err
		}
		if edge.Name == "" {
			lay.edgeOffsets[i] = -1
			continue
		}
		addEdgeVar(i, edge)
	}
	return lay, nil
}

// writeNode fills a node variable's columns in the row buffer.
func (lay *matchLayout) writeNode(g *Graph, row types.Row, patternIdx int, id types.Value) {
	off := lay.nodeOffsets[patternIdx]
	if off < 0 {
		return
	}
	if !g.hasNodeTable {
		row[off] = g.derivedNodeIdValue(id)
		return
	}
	nodeRow := g.NodeRows[graphKey(id)]
	for c := 0; c < lay.nodeWidth; c++ {
		if nodeRow != nil {
			row[off+c] = nodeRow[c]
		} else if c == g.NodeIdIdx {
			row[off+c] = id // edge-only node: id with null properties
		} else {
			row[off+c] = nil
		}
	}
}

// writeEdge fills an edge variable's columns in the row buffer.
func (lay *matchLayout) writeEdge(g *Graph, row types.Row, patternIdx int, edgeIdx int) {
	off := lay.edgeOffsets[patternIdx]
	if off < 0 {
		return
	}
	edgeRow := g.Edges[edgeIdx].Row
	for c := 0; c < lay.edgeWidth; c++ {
		row[off+c] = edgeRow[c]
	}
}

// writeEdgeList fills a variable-length edge variable's column with the
// JSON array of traversed edges (one object per hop, edge columns as keys).
// Dynamic edge columns are stored as JSON strings on the row; embedding
// them as raw JSON (rather than re-encoded strings) keeps nested access
// like e[0].Tags.sev working through the standard dynamic machinery.
func (lay *matchLayout) writeEdgeList(g *Graph, row types.Row, patternIdx int, edgeIdxs []int) error {
	off := lay.edgeOffsets[patternIdx]
	if off < 0 {
		return nil
	}
	list := make([]map[string]interface{}, len(edgeIdxs))
	for i, ei := range edgeIdxs {
		m := make(map[string]interface{}, lay.edgeWidth)
		for c, col := range g.EdgeSchema.Columns {
			v := g.Edges[ei].Row[c]
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
	row[off] = string(b)
	return nil
}

// applyGraphMatch enumerates paths matching the pattern, filters them with
// the where clause, and projects the result. Traversal is depth-first over
// pattern positions. The where clause is split into AND-conjuncts, each
// evaluated at the earliest position where its pattern variables are all
// bound (buildMatchPredicates), pruning dead branches during traversal
// instead of after full enumeration. A physical edge is used at most once
// within a single matched path, which bounds variable-length expansion
// alongside MaxHops; total output is capped at maxGraphMatches.
func (e *Engine) applyGraphMatch(g *Graph, op *parser.GraphMatchOp) (*types.Table, error) {
	lay, err := buildMatchLayout(g, op)
	if err != nil {
		return nil, err
	}
	preds := buildMatchPredicates(op)

	if op.Edges[0].Distinct {
		if len(op.Edges) != 1 {
			return nil, fmt.Errorf("graph-match: 'distinct' is only valid on a single-edge pattern " +
				"(start)-[e*min..max distinct]-(end) — node-identity dedup does not compose with " +
				"continuing the rest of a longer pattern")
		}
		return e.applyGraphMatchDistinctBFS(g, op, lay, preds)
	}
	for _, spec := range op.Edges[1:] {
		if spec.Distinct {
			return nil, fmt.Errorf("graph-match: 'distinct' is only valid on a single-edge pattern")
		}
	}

	matches := types.NewTable("", lay.schema)
	row := make(types.Row, len(lay.schema.Columns)) // reused buffer
	usedEdges := make([]bool, len(g.Edges))

	// Reverse adjacency, built once and only when some edge traverses
	// backward or undirected.
	var radj map[string][]int
	for _, spec := range op.Edges {
		if spec.Direction != parser.EdgeForward {
			radj = make(map[string][]int, len(g.Adjacency))
			for ei, edge := range g.Edges {
				k := graphKey(edge.Target)
				radj[k] = append(radj[k], ei)
			}
			break
		}
	}

	// neighbors yields the candidate (edge index, next node id) pairs
	// from key under the given traversal direction. Undirected yields
	// the forward set then the backward set; usedEdges already prevents
	// one edge matching twice within a single binding.
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

	// checkPreds evaluates every conjunct scheduled at pos against the
	// currently bound row prefix. false = prune this branch.
	checkPreds := func(pos int) (bool, error) {
		for _, p := range preds {
			if p.pos != pos {
				continue
			}
			keep, evalErr := evalExpr(p.expr, &lay.schema, row)
			if evalErr != nil {
				return false, fmt.Errorf("graph-match where: %w", evalErr)
			}
			if b, ok := keep.(bool); !ok || !b {
				return false, nil
			}
		}
		return true, nil
	}

	emit := func() error {
		if len(matches.Rows) >= maxGraphMatches {
			return fmt.Errorf("graph-match: result exceeds %d rows; narrow the pattern or where clause", maxGraphMatches)
		}
		out := make(types.Row, len(row))
		copy(out, row)
		matches.AddRow(out)
		return nil
	}

	// Reverse-reachability pruning (backlog P3 item 16): only attempted
	// for the single-edge-spec pattern it's scoped to, and only trusted
	// once the extracted target literal is verified to correspond to a
	// REAL node in the graph — a false match here (e.g. from a type
	// mismatch between the literal and the node's actual identity
	// representation) would prune correct paths, not just miss a
	// speedup, so this fails closed (no pruning) on any uncertainty.
	var reachDist map[string]int
	var reachMaxHops int
	if len(op.Edges) == 1 {
		if _, endVal, ok := detectFixedEndpoints(op, preds); ok {
			targetKey := graphKey(endVal)
			if _, isRealNode := g.NodeRows[targetKey]; isRealNode {
				reachMaxHops = op.Edges[0].MaxHops
				reachDist = reachabilityToTarget(g, targetKey, op.Edges[0].Direction, reachMaxHops)
			}
		}
	}

	var walk func(hop int, curKey string, curId types.Value) error
	walk = func(hop int, curKey string, curId types.Value) error {
		if hop == len(op.Edges) {
			return emit()
		}
		spec := op.Edges[hop]

		if spec.MinHops == 1 && spec.MaxHops == 1 {
			return neighbors(curKey, spec.Direction, func(ei int, next types.Value) error {
				if usedEdges[ei] {
					return nil
				}
				usedEdges[ei] = true
				lay.writeEdge(g, row, hop, ei)
				lay.writeNode(g, row, hop+1, next)
				keep, walkErr := checkPreds(hop + 1)
				if walkErr == nil && keep {
					walkErr = walk(hop+1, graphKey(next), next)
				}
				usedEdges[ei] = false
				return walkErr
			})
		}

		// Variable-length: expand depth-first up to MaxHops, handing off
		// to the rest of the pattern at every depth >= MinHops. A failed
		// predicate check prunes only the hand-off — deeper expansion
		// continues, since a longer path may still satisfy it.
		collected := make([]int, 0, spec.MaxHops)
		var expand func(depth int, key string, id types.Value) error
		expand = func(depth int, key string, id types.Value) error {
			if depth >= spec.MinHops {
				if listErr := lay.writeEdgeList(g, row, hop, collected); listErr != nil {
					return listErr
				}
				lay.writeNode(g, row, hop+1, id)
				keep, checkErr := checkPreds(hop + 1)
				if checkErr != nil {
					return checkErr
				}
				if keep {
					if walkErr := walk(hop+1, key, id); walkErr != nil {
						return walkErr
					}
				}
			}
			if depth == spec.MaxHops {
				return nil
			}
			return neighbors(key, spec.Direction, func(ei int, next types.Value) error {
				if usedEdges[ei] {
					return nil
				}
				if reachDist != nil {
					nk := graphKey(next)
					d, reachable := reachDist[nk]
					if !reachable || depth+1+d > reachMaxHops {
						// Provably cannot reach the fixed target within
						// the remaining hop budget from here — cut this
						// branch. Every path this discards would have
						// failed the terminal equality predicate anyway;
						// nothing in the true answer set is lost.
						return nil
					}
				}
				usedEdges[ei] = true
				collected = append(collected, ei)
				expandErr := expand(depth+1, graphKey(next), next)
				collected = collected[:len(collected)-1]
				usedEdges[ei] = false
				return expandErr
			})
		}
		return expand(0, curKey, curId)
	}

	for _, id := range g.NodeIDs {
		lay.writeNode(g, row, 0, id)
		keep, err := checkPreds(0)
		if err != nil {
			return nil, err
		}
		if !keep {
			continue
		}
		if err := walk(0, graphKey(id), id); err != nil {
			return nil, err
		}
	}

	return e.applyProject(matches, op.Project)
}
