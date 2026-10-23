package gql

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// maxHops bounds unbounded quantifiers (->*, ->+, *, *m..), matching
// the engine's variable-length edge limit.
const maxHops = 64

// Translate turns a GQL (cypher=false) or openCypher (cypher=true)
// query into a KQL query over graphRef, a KQL expression yielding the
// graph (a stored function such as G, graph('Model'), or a
// make-graph pipeline in parentheses).
func Translate(src, graphRef string, cypher bool) (string, error) {
	graphRef = strings.TrimSpace(graphRef)
	if graphRef == "" {
		return "", fmt.Errorf("gql: no graph reference (set query_graph_reference)")
	}
	q, err := parseQuery(src, cypher)
	if err != nil {
		return "", err
	}
	t := &tr{ref: graphRef}
	var branches []string
	for _, part := range q.parts {
		s, err := t.linear(part)
		if err != nil {
			return "", err
		}
		branches = append(branches, s)
	}
	if len(branches) == 1 {
		return branches[0], nil
	}
	out := branches[0]
	for i, b := range branches[1:] {
		out = "union (" + out + "), (" + b + ")"
		if !q.unionAll[i] {
			out += "\n| distinct *"
		}
	}
	return out, nil
}

type bindKind int

const (
	kValue bindKind = iota
	kNode
	kEdge
	kPath
	kInline // an output alias referenced before its column exists
)

type binding struct {
	kind   bindKind
	col    string            // value column, or the entity's bag column
	id     string            // node id column
	props  map[string]string // property -> typed column (graph-projected)
	labels string            // labels column
	nodes  string            // path: nodes column
	edges  string            // path: edges column
	varLen bool              // edge: variable length (its value is a list)
	text   string            // kInline: the expression
	user   bool              // named in the query (not generated)
}

type state struct {
	pipe  []string
	scope map[string]*binding
	order []string // user variables, in binding order (RETURN *)
}

func (st *state) bind(name string, b *binding) {
	if _, ok := st.scope[name]; !ok && b.user {
		st.order = append(st.order, name)
	}
	st.scope[name] = b
}

type tr struct {
	ref string
	n   int
}

func (t *tr) gen(prefix string) string {
	t.n++
	return fmt.Sprintf("__%s%d", prefix, t.n)
}

// q quotes a column name for KQL. Generated names are plain
// identifiers; user names are always bracket-quoted, which keeps GQL
// names that are KQL keywords (count, by, on, ...) usable.
func q(name string) string {
	if strings.HasPrefix(name, "__") {
		return name
	}
	return "['" + strings.ReplaceAll(strings.ReplaceAll(name, `\`, `\\`), "'", `\'`) + "']"
}

func kqlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (t *tr) linear(cs []clause) (string, error) {
	st := &state{scope: map[string]*binding{}}
	var segs [][]clause
	cur := []clause{}
	for _, c := range cs {
		if _, ok := c.(*nextClause); ok {
			segs = append(segs, cur)
			cur = []clause{}
			continue
		}
		cur = append(cur, c)
	}
	segs = append(segs, cur)

	returned := false
	for si, seg := range segs {
		seg = mergeMatches(seg)
		last := si == len(segs)-1
		var rest []clause // every clause after the current one, all segments
		for _, s := range segs[si+1:] {
			rest = append(rest, s...)
		}
		returned = false
		for i := 0; i < len(seg); i++ {
			after := append(append([]clause{}, seg[i+1:]...), rest...)
			var err error
			switch c := seg[i].(type) {
			case *matchClause:
				err = t.doMatch(st, c, after)
			case *filterClause:
				err = t.need(st)
				if err == nil {
					var s string
					if s, err = t.emit(st, c.cond); err == nil {
						st.pipe = append(st.pipe, "where "+s)
					}
				}
			case *letClause:
				for k, name := range c.names {
					s, e := t.emit(st, c.exprs[k])
					if e != nil {
						return "", e
					}
					st.pipe = append(st.pipe, "extend "+q(name)+" = "+s)
					st.bind(name, &binding{kind: kValue, col: name, user: true})
				}
			case *forClause:
				err = t.doFor(st, c)
			case *orderClause, *offsetClause, *limitClause:
				err = t.doPaging(st, c, nil)
			case *returnClause:
				j := i + 1
				var paging []clause
				for j < len(seg) {
					switch seg[j].(type) {
					case *orderClause, *offsetClause, *limitClause:
						paging = append(paging, seg[j])
						j++
						continue
					}
					break
				}
				err = t.doReturn(st, c, paging, last)
				i = j - 1
				returned = true
			default:
				err = fmt.Errorf("gql: unsupported clause %T", c)
			}
			if err != nil {
				return "", err
			}
		}
	}
	if !returned {
		return "", fmt.Errorf("gql: the query must end with RETURN")
	}
	return strings.Join(st.pipe, "\n| "), nil
}

func (t *tr) need(st *state) error {
	if st.pipe == nil {
		return fmt.Errorf("gql: a query must start with MATCH")
	}
	return nil
}

// mergeMatches folds consecutive plain MATCH clauses into one, so
// they share a single graph-match (shared variables join for free).
func mergeMatches(seg []clause) []clause {
	var out []clause
	for _, c := range seg {
		m, ok := c.(*matchClause)
		if ok && !m.optional && len(out) > 0 {
			if p, ok := out[len(out)-1].(*matchClause); ok && !p.optional && p.mode == m.mode &&
				!hasShortest(p) && !hasShortest(m) {
				merged := &matchClause{mode: p.mode, paths: append(append([]*pathPat{}, p.paths...), m.paths...)}
				merged.where = andExpr(p.where, m.where)
				out[len(out)-1] = merged
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func hasShortest(m *matchClause) bool {
	for _, p := range m.paths {
		if p.shortest != "" {
			return true
		}
	}
	return false
}

func andExpr(a, b expr) expr {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return &binary{exprBase{"(" + a.src() + ") AND (" + b.src() + ")"}, "and", false, a, b}
}

func conjuncts(x expr, out []expr) []expr {
	if b, ok := x.(*binary); ok && b.op == "and" {
		return conjuncts(b.r, conjuncts(b.l, out))
	}
	if x != nil {
		out = append(out, x)
	}
	return out
}

// --- MATCH ---------------------------------------------------------

type gmVar struct {
	name   string
	kind   bindKind
	varLen bool
	user   bool
	path   *pathPat
}

// uses collects what later clauses read from each pattern variable.
type uses struct {
	props  map[string]map[string]bool
	labels map[string]bool
	bag    map[string]bool
	path   map[string]bool
	all    bool // RETURN *
}

func (t *tr) doMatch(st *state, mc *matchClause, after []clause) error {
	vars := map[string]*gmVar{}
	var order []string
	addVar := func(name string, kind bindKind, user, varLen bool) {
		if _, ok := vars[name]; !ok {
			vars[name] = &gmVar{name: name, kind: kind, user: user, varLen: varLen}
			order = append(order, name)
		}
	}
	var patterns []string
	var conds []string     // graph-match where
	var postIn []expr      // after the graph-match, its variables only
	var postOut []expr     // after joining with the input
	type elemCond struct { // element-level filters, emitted after names are known
		name   string
		varLen bool
		x      expr
	}
	var elemConds []elemCond
	shortest := ""
	for _, pp := range mc.paths {
		if pp.shortest != "" {
			shortest = pp.shortest
		}
		var sb strings.Builder
		for i, n := range pp.nodes {
			if n.name == "" {
				n.name = t.gen("n")
				addVar(n.name, kNode, false, false)
			} else {
				addVar(n.name, kNode, true, false)
			}
			if i > 0 {
				e := pp.edges[i-1]
				if e.name == "" {
					e.name = t.gen("e")
					addVar(e.name, kEdge, false, e.varLen)
				} else {
					addVar(e.name, kEdge, true, e.varLen)
				}
				inner := e.name
				if e.varLen {
					hi := e.max
					if hi < 0 {
						hi = maxHops
					}
					if hi < e.min {
						return fmt.Errorf("gql: invalid quantifier {%d,%d}", e.min, e.max)
					}
					inner += fmt.Sprintf("*%d..%d", e.min, hi)
				}
				switch e.dir {
				case dirForward:
					sb.WriteString("-[" + inner + "]->")
				case dirBackward:
					sb.WriteString("<-[" + inner + "]-")
				default:
					sb.WriteString("-[" + inner + "]-")
				}
				if e.label != nil {
					elemConds = append(elemConds, elemCond{e.name, e.varLen, labelCondFor(e.name, e.label)})
				}
				for k, key := range e.keys {
					elemConds = append(elemConds, elemCond{e.name, e.varLen, eqProp(e.name, key, e.vals[k])})
				}
				if e.where != nil {
					elemConds = append(elemConds, elemCond{e.name, e.varLen, e.where})
				}
			}
			sb.WriteString("(" + n.name + ")")
			if n.label != nil {
				elemConds = append(elemConds, elemCond{n.name, false, labelCondFor(n.name, n.label)})
			}
			for k, key := range n.keys {
				elemConds = append(elemConds, elemCond{n.name, false, eqProp(n.name, key, n.vals[k])})
			}
			if n.where != nil {
				elemConds = append(elemConds, elemCond{n.name, false, n.where})
			}
		}
		patterns = append(patterns, sb.String())
		if pp.name != "" {
			vars[pp.name] = &gmVar{name: pp.name, kind: kPath, user: true, path: pp}
			order = append(order, pp.name)
		}
	}

	gm := &gmEmitter{t: t, vars: vars}
	for _, ec := range elemConds {
		var s string
		var err error
		if ec.varLen {
			gm.allVar = ec.name
			s, err = gm.emit(ec.x)
			gm.allVar = ""
			if err == nil {
				s = "all(" + ec.name + ", " + s + ")"
			}
		} else {
			s, err = gm.emit(ec.x)
		}
		if err != nil {
			if ec.varLen {
				return fmt.Errorf("gql: unsupported condition on variable-length edge %s: %v", ec.name, err)
			}
			postIn = append(postIn, ec.x)
			continue
		}
		conds = append(conds, s)
	}
	for _, c := range conjuncts(mc.where, nil) {
		if s, err := gm.emit(c); err == nil {
			conds = append(conds, s)
			continue
		}
		if refsSubset(c, vars) {
			postIn = append(postIn, c)
		} else {
			postOut = append(postOut, c)
		}
	}

	// What later clauses need from each variable.
	u := uses{props: map[string]map[string]bool{}, labels: map[string]bool{}, bag: map[string]bool{}, path: map[string]bool{}}
	for _, x := range append(append([]expr{}, postIn...), postOut...) {
		scanUses(x, vars, &u)
	}
	for _, c := range after {
		scanClause(c, vars, &u)
	}

	// Projection.
	var proj []string
	binds := map[string]*binding{}
	add := func(x string) string {
		c := t.gen("g")
		proj = append(proj, c+" = "+x)
		return c
	}
	for _, name := range order {
		v := vars[name]
		b := &binding{kind: v.kind, user: v.user, varLen: v.varLen, props: map[string]string{}}
		binds[name] = b
		if v.kind == kPath {
			if u.path[name] || (u.all && v.user) {
				b.nodes, b.edges = add(pathNodes(v.path)), add(pathEdges(v.path))
			}
			continue
		}
		if v.kind == kNode && v.user {
			b.id = add("node_id(" + name + ")")
		}
		props := make([]string, 0, len(u.props[name]))
		for p := range u.props[name] {
			props = append(props, p)
		}
		sort.Strings(props)
		for _, p := range props {
			b.props[p] = add(name + "." + gmProp(p))
		}
		if u.labels[name] {
			b.labels = add("labels(" + name + ")")
		}
		if u.bag[name] || (u.all && v.user) {
			if v.varLen {
				b.col = add("__gql_path_edges(" + name + ")")
			} else {
				b.col = add(name)
			}
		}
	}
	if len(proj) == 0 {
		add("1")
	}

	var op strings.Builder
	op.WriteString(t.ref + "\n| ")
	if shortest != "" {
		op.WriteString("graph-shortest-paths output=" + shortest + " gql_labels=true ")
	} else {
		op.WriteString("graph-match ")
		if mc.mode != "" {
			op.WriteString("cycles=" + mc.mode + " ")
		}
		op.WriteString("gql_labels=true ")
	}
	op.WriteString(strings.Join(patterns, ", "))
	if len(conds) > 0 {
		op.WriteString("\n  where " + strings.Join(conds, "\n    and "))
	}
	op.WriteString("\n  project " + strings.Join(proj, ", "))
	gmText := op.String()

	// Variables shared with the input are joined on node id.
	var keys [][2]string
	if st.pipe != nil {
		for _, name := range order {
			if old, ok := st.scope[name]; ok {
				nb := binds[name]
				if old.kind != kNode || old.id == "" || nb.kind != kNode || nb.id == "" {
					return fmt.Errorf("gql: variable %s is already bound and can only be re-matched as a node", name)
				}
				keys = append(keys, [2]string{old.id, nb.id})
			}
		}
	}

	// Conditions on this pattern's variables apply before the join.
	inner := &state{scope: map[string]*binding{}}
	for name, b := range binds {
		inner.scope[name] = b
	}
	var innerWhere []string
	for _, c := range postIn {
		s, err := t.emit(inner, c)
		if err != nil {
			return err
		}
		innerWhere = append(innerWhere, "where "+s)
	}

	if st.pipe == nil {
		st.pipe = append([]string{gmText}, innerWhere...)
	} else {
		right := gmText
		for _, w := range innerWhere {
			right += "\n  | " + w
		}
		kind := "inner"
		if mc.optional {
			kind = "leftouter"
		}
		if len(keys) == 0 {
			x := t.gen("x")
			st.pipe = append(st.pipe, "extend "+x+" = 1",
				"join kind="+kind+" ("+right+"\n  | extend "+x+" = 1) on "+x,
				"project-away "+x+", "+x+"1")
		} else {
			var on, away []string
			for _, k := range keys {
				on = append(on, "$left."+k[0]+" == $right."+k[1])
				away = append(away, k[1])
			}
			st.pipe = append(st.pipe, "join kind="+kind+" ("+right+") on "+strings.Join(on, ", "),
				"project-away "+strings.Join(away, ", "))
		}
	}
	for _, name := range order {
		nb := binds[name]
		if old, ok := st.scope[name]; ok {
			// Re-matched: keep the input's id and bag, add new columns.
			for p, c := range nb.props {
				if _, have := old.props[p]; !have {
					if old.props == nil {
						old.props = map[string]string{}
					}
					old.props[p] = c
				}
			}
			if old.labels == "" {
				old.labels = nb.labels
			}
			if old.col == "" {
				old.col = nb.col
			}
			continue
		}
		st.bind(name, nb)
	}
	for _, c := range postOut {
		s, err := t.emit(st, c)
		if err != nil {
			return err
		}
		st.pipe = append(st.pipe, "where "+s)
	}
	return nil
}

func gmProp(p string) string {
	if isPlainIdent(p) {
		return p
	}
	return "['" + strings.ReplaceAll(p, "'", `\'`) + "']"
}

func isPlainIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func pathNodes(pp *pathPat) string {
	if len(pp.edges) == 0 {
		return "pack_array(" + pp.nodes[0].name + ")"
	}
	parts := []string{"__gql_path_nodes(" + pp.edges[0].name + ")"}
	for _, e := range pp.edges[1:] {
		parts = append(parts, "array_slice(__gql_path_nodes("+e.name+"), 1, -1)")
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "array_concat(" + strings.Join(parts, ", ") + ")"
}

func pathEdges(pp *pathPat) string {
	if len(pp.edges) == 0 {
		return "dynamic([])"
	}
	var parts []string
	for _, e := range pp.edges {
		parts = append(parts, "__gql_path_edges("+e.name+")")
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "array_concat(" + strings.Join(parts, ", ") + ")"
}

// labelCondFor is a label expression on a named element as a GQL
// expression over labels(name).
func labelCondFor(name string, l labelExpr) expr {
	return &labelTest{exprBase{"labels"}, name, l}
}

// labelTest is an internal expression: element name's labels satisfy l
// (name "" inside all()).
type labelTest struct {
	exprBase
	name string
	l    labelExpr
}

func eqProp(name, key string, v expr) expr {
	return &binary{exprBase{name + "." + key}, "=", false, &propRef{exprBase{name + "." + key}, &varRef{exprBase{name}, name}, key}, v}
}

func refsSubset(x expr, vars map[string]*gmVar) bool {
	ok := true
	walk(x, func(e expr) bool {
		if v, isVar := e.(*varRef); isVar {
			if _, in := vars[v.name]; !in {
				ok = false
			}
		}
		return true
	})
	return ok
}

// walk visits x and its subexpressions; f returns false to skip
// children.
func walk(x expr, f func(expr) bool) {
	if x == nil || !f(x) {
		return
	}
	switch e := x.(type) {
	case *listLit:
		for _, i := range e.items {
			walk(i, f)
		}
	case *mapLit:
		for _, v := range e.vals {
			walk(v, f)
		}
	case *propRef:
		walk(e.x, f)
	case *indexRef:
		walk(e.x, f)
		walk(e.i, f)
	case *unary:
		walk(e.x, f)
	case *binary:
		walk(e.l, f)
		walk(e.r, f)
	case *isNull:
		walk(e.x, f)
	case *call:
		for _, a := range e.args {
			walk(a, f)
		}
	case *caseExpr:
		walk(e.operand, f)
		for k := range e.whens {
			walk(e.whens[k], f)
			walk(e.thens[k], f)
		}
		walk(e.els, f)
	case *castExpr:
		walk(e.x, f)
	case *labelTest:
		if e.name != "" {
			walk(&varRef{exprBase{e.name}, e.name}, f)
		}
	}
}

var pathFuncs = map[string]bool{"nodes": true, "edges": true, "relationships": true, "path_length": true, "length": true}

func scanUses(x expr, vars map[string]*gmVar, u *uses) {
	walk(x, func(e expr) bool {
		switch e := e.(type) {
		case *propRef:
			if v, ok := e.x.(*varRef); ok {
				if gv, in := vars[v.name]; in && gv.kind != kPath {
					if u.props[v.name] == nil {
						u.props[v.name] = map[string]bool{}
					}
					u.props[v.name][e.name] = true
					return false
				}
			}
		case *labelTest:
			if e.name != "" {
				u.labels[e.name] = true
			}
			return false
		case *call:
			if len(e.args) == 1 {
				if v, ok := e.args[0].(*varRef); ok {
					if gv, in := vars[v.name]; in {
						switch {
						case gv.kind == kPath:
							u.path[v.name] = true
							return false
						case e.name == "labels" || e.name == "type":
							u.labels[v.name] = true
							return false
						case (e.name == "element_id" || e.name == "id" || e.name == "count") && gv.kind == kNode:
							return false // the id column is always projected
						}
					}
				}
			}
		case *varRef:
			if gv, in := vars[e.name]; in {
				if gv.kind == kPath {
					u.path[e.name] = true
				} else {
					u.bag[e.name] = true
				}
			}
		}
		return true
	})
}

func scanClause(c clause, vars map[string]*gmVar, u *uses) {
	switch c := c.(type) {
	case *matchClause:
		if c.where != nil {
			scanUses(c.where, vars, u)
		}
		for _, p := range c.paths {
			for _, n := range p.nodes {
				for _, v := range n.vals {
					scanUses(v, vars, u)
				}
				if n.where != nil {
					scanUses(n.where, vars, u)
				}
			}
		}
	case *filterClause:
		scanUses(c.cond, vars, u)
	case *letClause:
		for _, x := range c.exprs {
			scanUses(x, vars, u)
		}
	case *forClause:
		scanUses(c.list, vars, u)
	case *orderClause:
		for _, it := range c.items {
			scanUses(it.x, vars, u)
		}
	case *returnClause:
		if c.star {
			u.all = true
		}
		for _, it := range c.items {
			scanUses(it.x, vars, u)
		}
	}
}

// --- FOR, paging, RETURN --------------------------------------------

func (t *tr) doFor(st *state, c *forClause) error {
	if err := t.need(st); err != nil {
		return err
	}
	s, err := t.emit(st, c.list)
	if err != nil {
		return err
	}
	st.pipe = append(st.pipe, "extend "+q(c.name)+" = "+s)
	mv := "mv-expand "
	if c.idx != "" {
		if !isPlainIdent(c.idx) {
			return fmt.Errorf("gql: FOR index name %q must be a plain identifier", c.idx)
		}
		mv += "with_itemindex=" + c.idx + " "
	}
	mv += q(c.name)
	if typ := listElemType(c.list); typ != "" {
		mv += " to typeof(" + typ + ")"
	}
	st.pipe = append(st.pipe, mv)
	st.bind(c.name, &binding{kind: kValue, col: c.name, user: true})
	if c.idx != "" {
		if c.ordinality {
			st.pipe = append(st.pipe, "extend "+q(c.idx)+" = "+q(c.idx)+" + 1")
		}
		st.bind(c.idx, &binding{kind: kValue, col: c.idx, user: true})
	}
	return nil
}

func listElemType(x expr) string {
	l, ok := x.(*listLit)
	if !ok || len(l.items) == 0 {
		return ""
	}
	typ := ""
	for _, it := range l.items {
		li, ok := it.(*lit)
		if !ok {
			return ""
		}
		var k string
		switch li.kind {
		case litInt:
			k = "long"
		case litFloat:
			k = "real"
		case litString:
			k = "string"
		case litBool:
			k = "bool"
		default:
			return ""
		}
		if typ != "" && typ != k {
			if (typ == "long" && k == "real") || (typ == "real" && k == "long") {
				typ = "real"
				continue
			}
			return ""
		}
		typ = k
	}
	return typ
}

// doPaging emits ORDER BY / OFFSET / LIMIT. resolve, when set, maps an
// ORDER BY expression to an output column.
func (t *tr) doPaging(st *state, c clause, resolve func(expr) (string, bool)) error {
	if err := t.need(st); err != nil {
		return err
	}
	switch c := c.(type) {
	case *orderClause:
		var keys []string
		for _, it := range c.items {
			s := ""
			if resolve != nil {
				s, _ = resolve(it.x)
			}
			if s == "" {
				var err error
				if s, err = t.emit(st, it.x); err != nil {
					return err
				}
			}
			k := s + " asc"
			if it.desc {
				k = s + " desc"
			}
			if it.nulls != "" {
				k += " nulls " + it.nulls
			}
			keys = append(keys, k)
		}
		st.pipe = append(st.pipe, "sort by "+strings.Join(keys, ", "))
	case *offsetClause:
		s, err := t.emit(st, c.n)
		if err != nil {
			return err
		}
		rn := t.gen("rn")
		st.pipe = append(st.pipe, "serialize "+rn+" = row_number()", "where "+rn+" > "+s, "project-away "+rn)
	case *limitClause:
		s, err := t.emit(st, c.n)
		if err != nil {
			return err
		}
		st.pipe = append(st.pipe, "take "+s)
	}
	return nil
}

var aggFuncs = map[string]bool{
	"count": true, "sum": true, "avg": true, "min": true, "max": true,
	"collect_list": true, "collect": true, "array_agg": true,
	"stdev": true, "stddev": true, "stddev_samp": true, "stddev_pop": true, "stdevp": true,
}

func hasAgg(x expr) bool {
	found := false
	walk(x, func(e expr) bool {
		if c, ok := e.(*call); ok && aggFuncs[c.name] {
			found = true
			return false
		}
		return !found
	})
	return found
}

func defaultName(x expr) string {
	if v, ok := x.(*varRef); ok {
		return v.name
	}
	return x.src()
}

func (t *tr) doReturn(st *state, rc *returnClause, paging []clause, last bool) error {
	if err := t.need(st); err != nil {
		return err
	}
	items := rc.items
	if rc.star {
		var star []returnItem
		for _, name := range st.order {
			star = append(star, returnItem{x: &varRef{exprBase{name}, name}})
		}
		if len(star) == 0 && len(items) == 0 {
			return fmt.Errorf("gql: RETURN * with no variables in scope")
		}
		items = append(star, items...)
	}
	names := make([]string, len(items))
	seen := map[string]bool{}
	for i, it := range items {
		n := it.alias
		if n == "" {
			n = defaultName(it.x)
		}
		for base, k := n, 1; seen[n]; k++ {
			n = fmt.Sprintf("%s%d", base, k)
		}
		seen[n] = true
		names[i] = n
	}

	newScope := &state{scope: map[string]*binding{}}
	agg := false
	for _, it := range items {
		if hasAgg(it.x) {
			agg = true
		}
	}
	// Entities that continue past NEXT carry their id / path columns.
	carry := func(i int, sink *[]string) *binding {
		nb := &binding{kind: kValue, col: names[i], user: true}
		v, ok := items[i].x.(*varRef)
		if !ok {
			return nb
		}
		b := st.scope[v.name]
		if b == nil {
			return nb
		}
		switch b.kind {
		case kNode, kEdge:
			nb.kind = b.kind
			nb.varLen = b.varLen
			if !last && b.id != "" {
				nb.id = t.gen("c")
				*sink = append(*sink, nb.id+" = "+b.id)
			}
		case kPath:
			nb.kind = kPath
			if !last {
				nb.nodes, nb.edges = t.gen("c"), t.gen("c")
				*sink = append(*sink, nb.nodes+" = "+b.nodes, nb.edges+" = "+b.edges)
			}
		}
		return nb
	}

	// resolveOut maps an ORDER BY expression to an output column: an
	// alias, or the same text as a returned expression.
	resolveOut := func(x expr) (string, bool) {
		if v, ok := x.(*varRef); ok {
			for i, n := range names {
				if n == v.name {
					return q(names[i]), true
				}
			}
		}
		for i, it := range items {
			if it.x.src() == x.src() {
				return q(names[i]), true
			}
		}
		return "", false
	}

	var proj, hidden []string
	if !agg && !rc.distinct {
		for i, it := range items {
			s, err := t.emit(st, it.x)
			if err != nil {
				return err
			}
			proj = append(proj, q(names[i])+" = "+s)
			nb := carry(i, &hidden)
			nb.col = names[i]
			newScope.bind(names[i], nb)
		}
		// ORDER BY keys that are not outputs are computed alongside.
		var orderCols []string
		resolve := func(x expr) (string, bool) {
			if s, ok := resolveOut(x); ok {
				return s, true
			}
			ov := &state{scope: map[string]*binding{}}
			for k, b := range st.scope {
				ov.scope[k] = b
			}
			for i, it := range items {
				if _, shadow := ov.scope[names[i]]; !shadow {
					if s, err := t.emit(st, it.x); err == nil {
						ov.scope[names[i]] = &binding{kind: kInline, text: s}
					}
				}
			}
			s, err := t.emit(ov, x)
			if err != nil {
				return "", false
			}
			c := t.gen("o")
			hidden = append(hidden, c+" = "+s)
			orderCols = append(orderCols, c)
			return c, true
		}
		// Resolve order keys first so their columns join the project.
		var keyed []clause
		for _, p := range paging {
			if oc, ok := p.(*orderClause); ok {
				nc := &orderClause{}
				for _, it := range oc.items {
					s, ok := resolve(it.x)
					if !ok {
						return fmt.Errorf("gql: cannot order by %s", it.x.src())
					}
					nc.items = append(nc.items, orderItem{x: &rawExpr{exprBase{s}}, desc: it.desc, nulls: it.nulls})
				}
				keyed = append(keyed, nc)
				continue
			}
			keyed = append(keyed, p)
		}
		st.pipe = append(st.pipe, "project "+strings.Join(append(proj, hidden...), ", "))
		st.scope, st.order = newScope.scope, newScope.order
		for _, p := range keyed {
			if err := t.doPaging(st, p, nil); err != nil {
				return err
			}
		}
		if len(orderCols) > 0 {
			st.pipe = append(st.pipe, "project-away "+strings.Join(orderCols, ", "))
		}
		return nil
	}

	// Aggregation (or DISTINCT): group by the non-aggregate items.
	var groups, aggs, final []string
	for i, it := range items {
		if hasAgg(it.x) {
			s, err := t.emitAgg(st, it.x, &aggs)
			if err != nil {
				return err
			}
			final = append(final, q(names[i])+" = "+s)
			newScope.bind(names[i], &binding{kind: kValue, col: names[i], user: true})
			continue
		}
		s, err := t.emit(st, it.x)
		if err != nil {
			return err
		}
		groups = append(groups, q(names[i])+" = "+s)
		final = append(final, q(names[i]))
		nb := carry(i, &hidden)
		nb.col = names[i]
		newScope.bind(names[i], nb)
	}
	groups = append(groups, hidden...)
	for _, h := range hidden {
		final = append(final, strings.SplitN(h, " = ", 2)[0])
	}
	if len(aggs) == 0 {
		st.pipe = append(st.pipe, "summarize by "+strings.Join(groups, ", "))
	} else {
		s := "summarize " + strings.Join(aggs, ", ")
		if len(groups) > 0 {
			s += " by " + strings.Join(groups, ", ")
		}
		st.pipe = append(st.pipe, s)
	}
	st.pipe = append(st.pipe, "project "+strings.Join(final, ", "))
	st.scope, st.order = newScope.scope, newScope.order
	for _, p := range paging {
		if err := t.doPaging(st, p, resolveOut); err != nil {
			return err
		}
	}
	return nil
}

// rawExpr is already-emitted KQL.
type rawExpr struct{ exprBase }

// --- expressions -----------------------------------------------------

var errNotGM = errors.New("not a graph-match predicate")

// gmEmitter emits graph-match where predicates: pattern variables are
// referenced directly (v.Prop, labels(v)); inside all(e, ...) the
// edge's properties are bare.
type gmEmitter struct {
	t      *tr
	vars   map[string]*gmVar
	allVar string
}

func (g *gmEmitter) emit(x expr) (string, error) {
	return g.t.emitX(x, func(e expr) (string, bool, error) {
		switch e := e.(type) {
		case *varRef:
			v, ok := g.vars[e.name]
			if !ok || v.kind == kPath || e.name == g.allVar {
				return "", true, errNotGM
			}
			return e.name, true, nil
		case *propRef:
			v, ok := e.x.(*varRef)
			if !ok {
				return "", false, nil
			}
			gv, in := g.vars[v.name]
			if !in || gv.kind == kPath {
				return "", true, errNotGM
			}
			if v.name == g.allVar {
				return gmProp(e.name), true, nil
			}
			if gv.varLen {
				return "", true, errNotGM
			}
			return v.name + "." + gmProp(e.name), true, nil
		case *labelTest:
			name := e.name
			arg := "labels(" + name + ")"
			if name == "" || name == g.allVar {
				arg = "labels()"
			}
			return labelKQL(e.l, arg), true, nil
		case *call:
			if aggFuncs[e.name] || pathFuncs[e.name] {
				return "", true, errNotGM
			}
			if (e.name == "labels" || e.name == "type") && len(e.args) == 1 {
				v, ok := e.args[0].(*varRef)
				if !ok {
					return "", true, errNotGM
				}
				arg := "labels(" + v.name + ")"
				if v.name == g.allVar {
					arg = "labels()"
				} else if gv, in := g.vars[v.name]; !in || gv.kind == kPath || gv.varLen {
					return "", true, errNotGM
				}
				if e.name == "type" {
					return "tostring(" + arg + "[0])", true, nil
				}
				return arg, true, nil
			}
			if (e.name == "element_id" || e.name == "id") && len(e.args) == 1 {
				v, ok := e.args[0].(*varRef)
				if gv, in := g.vars[nameOf(v, ok)]; ok && in && gv.kind == kNode {
					return "node_id(" + v.name + ")", true, nil
				}
				return "", true, errNotGM
			}
		case *param:
			return "", true, errNotGM
		}
		return "", false, nil
	})
}

func nameOf(v *varRef, ok bool) string {
	if !ok {
		return ""
	}
	return v.name
}

func labelKQL(l labelExpr, arg string) string {
	switch l := l.(type) {
	case labelName:
		return "set_has_element(" + arg + ", " + kqlString(l.name) + ")"
	case labelOr:
		return "(" + labelKQL(l.l, arg) + " or " + labelKQL(l.r, arg) + ")"
	case labelAnd:
		return "(" + labelKQL(l.l, arg) + " and " + labelKQL(l.r, arg) + ")"
	case labelNot:
		return "not(" + labelKQL(l.x, arg) + ")"
	case labelAny:
		return "(array_length(" + arg + ") > 0)"
	}
	return "true"
}

// emit renders x over the current table.
func (t *tr) emit(st *state, x expr) (string, error) {
	return t.emitX(x, func(e expr) (string, bool, error) { return t.tableHook(st, e, nil) })
}

// emitAgg renders x with its aggregate calls lifted into aggs
// (summarize items); x's result refers to them by name.
func (t *tr) emitAgg(st *state, x expr, aggs *[]string) (string, error) {
	return t.emitX(x, func(e expr) (string, bool, error) { return t.tableHook(st, e, aggs) })
}

func (t *tr) tableHook(st *state, e expr, aggs *[]string) (string, bool, error) {
	switch e := e.(type) {
	case *rawExpr:
		return e.text, true, nil
	case *varRef:
		b, ok := st.scope[e.name]
		if !ok {
			return "", true, fmt.Errorf("gql: unknown variable %s", e.name)
		}
		switch b.kind {
		case kInline:
			return "(" + b.text + ")", true, nil
		case kPath:
			if b.nodes == "" {
				return "__gql_interleave(dynamic([]), dynamic([]))", true, nil
			}
			return "__gql_interleave(" + b.nodes + ", " + b.edges + ")", true, nil
		}
		if b.col == "" {
			return "", true, fmt.Errorf("gql: variable %s cannot be used as a value here", e.name)
		}
		return q(b.col), true, nil
	case *propRef:
		v, ok := e.x.(*varRef)
		if !ok {
			return "", false, nil
		}
		b, ok := st.scope[v.name]
		if !ok {
			return "", true, fmt.Errorf("gql: unknown variable %s", v.name)
		}
		if c, ok := b.props[e.name]; ok {
			return c, true, nil
		}
		if b.col != "" {
			return q(b.col) + "[" + kqlString(e.name) + "]", true, nil
		}
		return "", true, fmt.Errorf("gql: %s has no property %s here", v.name, e.name)
	case *labelTest:
		b := st.scope[e.name]
		if b == nil || b.labels == "" {
			return "", true, fmt.Errorf("gql: labels of %s are not available here", e.name)
		}
		return labelKQL(e.l, b.labels), true, nil
	case *call:
		if aggFuncs[e.name] {
			if aggs == nil {
				return "", true, fmt.Errorf("gql: aggregate %s is only allowed in RETURN / WITH", e.name)
			}
			s, err := t.aggKQL(st, e)
			if err != nil {
				return "", true, err
			}
			c := t.gen("a")
			*aggs = append(*aggs, c+" = "+s)
			return c, true, nil
		}
		if len(e.args) != 1 {
			return "", false, nil
		}
		v, ok := e.args[0].(*varRef)
		if !ok {
			return "", false, nil
		}
		b, ok := st.scope[v.name]
		if !ok {
			return "", false, nil
		}
		switch {
		case b.kind == kPath && (e.name == "nodes" || e.name == "edges" || e.name == "relationships"):
			if b.nodes == "" {
				return "", true, fmt.Errorf("gql: path %s is not available here", v.name)
			}
			if e.name == "nodes" {
				return b.nodes, true, nil
			}
			return b.edges, true, nil
		case b.kind == kPath && (e.name == "path_length" || e.name == "length"):
			if b.edges == "" {
				return "", true, fmt.Errorf("gql: path %s is not available here", v.name)
			}
			return "array_length(" + b.edges + ")", true, nil
		case (b.kind == kNode || b.kind == kEdge) && (e.name == "labels" || e.name == "type"):
			lab := b.labels
			if lab == "" {
				if b.col == "" {
					return "", true, fmt.Errorf("gql: labels of %s are not available here", v.name)
				}
				lab = "__gql_labels(" + q(b.col) + ")"
			}
			if e.name == "type" {
				return "tostring(" + lab + "[0])", true, nil
			}
			return lab, true, nil
		case (b.kind == kNode || b.kind == kEdge) && (e.name == "element_id" || e.name == "id"):
			if b.id != "" {
				return "tostring(" + b.id + ")", true, nil
			}
			if b.col != "" {
				return "tostring(hash(tostring(" + q(b.col) + ")))", true, nil
			}
			return "", true, fmt.Errorf("gql: %s(%s) is not available here", e.name, v.name)
		case (b.kind == kNode || b.kind == kEdge) && e.name == "properties":
			if b.col == "" {
				return "", true, fmt.Errorf("gql: %s is not available here", v.name)
			}
			return q(b.col), true, nil
		}
	}
	return "", false, nil
}

func (t *tr) aggKQL(st *state, c *call) (string, error) {
	if c.star {
		if c.name != "count" {
			return "", fmt.Errorf("gql: %s(*) is not valid", c.name)
		}
		return "count()", nil
	}
	if len(c.args) != 1 {
		return "", fmt.Errorf("gql: %s takes one argument", c.name)
	}
	for _, a := range c.args {
		if hasAgg(a) {
			return "", fmt.Errorf("gql: nested aggregate in %s", c.src())
		}
	}
	arg := c.args[0]
	// count(n) of an entity counts its non-null bindings.
	if v, ok := arg.(*varRef); ok {
		if b := st.scope[v.name]; b != nil && (b.kind == kNode || b.kind == kEdge || b.kind == kPath) && c.name == "count" {
			col := b.id
			if col == "" {
				col = b.col
			}
			if col == "" {
				col = b.edges
			}
			if col == "" {
				return "count()", nil
			}
			if c.distinct {
				return "count_distinct(tostring(" + q(col) + "))", nil
			}
			return "countif(isnotnull(" + q(col) + "))", nil
		}
	}
	s, err := t.emit(st, arg)
	if err != nil {
		return "", err
	}
	switch c.name {
	case "count":
		if c.distinct {
			return "count_distinct(" + s + ")", nil
		}
		return "countif(isnotnull(" + s + "))", nil
	case "sum", "avg", "min", "max":
		return c.name + "(" + s + ")", nil
	case "collect_list", "collect", "array_agg":
		if c.distinct {
			return "make_set(" + s + ")", nil
		}
		return "make_list(" + s + ")", nil
	case "stdev", "stddev", "stddev_samp":
		return "stdev(" + s + ")", nil
	case "stddev_pop", "stdevp":
		return "stdevp(" + s + ")", nil
	}
	return "", fmt.Errorf("gql: unsupported aggregate %s", c.name)
}

// emitX renders an expression; hook handles context-dependent nodes
// (variables, properties, labels) and reports handled=false to fall
// through to the generic rendering.
func (t *tr) emitX(x expr, hook func(expr) (string, bool, error)) (string, error) {
	if s, handled, err := hook(x); handled || err != nil {
		return s, err
	}
	rec := func(e expr) (string, error) { return t.emitX(e, hook) }
	switch e := x.(type) {
	case *lit:
		switch e.kind {
		case litInt:
			return e.val, nil
		case litFloat:
			if !strings.ContainsAny(e.val, ".eE") {
				return e.val + ".0", nil
			}
			return "real(" + e.val + ")", nil
		case litString:
			return kqlString(e.val), nil
		case litBool:
			return e.val, nil
		}
		return "dynamic(null)", nil
	case *listLit:
		if js, ok := constJSON(e); ok {
			return "dynamic(" + js + ")", nil
		}
		var parts []string
		for _, it := range e.items {
			s, err := rec(it)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return "pack_array(" + strings.Join(parts, ", ") + ")", nil
	case *mapLit:
		if len(e.keys) == 0 {
			return "dynamic({})", nil
		}
		var parts []string
		for k, key := range e.keys {
			s, err := rec(e.vals[k])
			if err != nil {
				return "", err
			}
			parts = append(parts, kqlString(key), s)
		}
		return "bag_pack(" + strings.Join(parts, ", ") + ")", nil
	case *param:
		return "", fmt.Errorf("gql: query parameters ($%s) are not supported", e.name)
	case *varRef:
		return "", fmt.Errorf("gql: unknown variable %s", e.name)
	case *propRef:
		s, err := rec(e.x)
		if err != nil {
			return "", err
		}
		return s + "[" + kqlString(e.name) + "]", nil
	case *indexRef:
		s, err := rec(e.x)
		if err != nil {
			return "", err
		}
		i, err := rec(e.i)
		if err != nil {
			return "", err
		}
		return s + "[" + i + "]", nil
	case *unary:
		s, err := rec(e.x)
		if err != nil {
			return "", err
		}
		if e.op == "not" {
			return "not(" + s + ")", nil
		}
		return "(" + e.op + s + ")", nil
	case *isNull:
		s, err := rec(e.x)
		if err != nil {
			return "", err
		}
		if e.not {
			return "isnotnull(" + s + ")", nil
		}
		return "isnull(" + s + ")", nil
	case *binary:
		return t.emitBinary(e, rec)
	case *caseExpr:
		var parts []string
		var op string
		if e.operand != nil {
			s, err := rec(e.operand)
			if err != nil {
				return "", err
			}
			op = s
		}
		for k := range e.whens {
			w, err := rec(e.whens[k])
			if err != nil {
				return "", err
			}
			if op != "" {
				w = "(" + op + " == " + w + ")"
			}
			th, err := rec(e.thens[k])
			if err != nil {
				return "", err
			}
			parts = append(parts, w, th)
		}
		els := "dynamic(null)"
		if e.els != nil {
			s, err := rec(e.els)
			if err != nil {
				return "", err
			}
			els = s
		} else {
			els = typedNull(e.thens)
		}
		return "case(" + strings.Join(parts, ", ") + ", " + els + ")", nil
	case *castExpr:
		s, err := rec(e.x)
		if err != nil {
			return "", err
		}
		f, ok := castFuncs[e.to]
		if !ok {
			return "", fmt.Errorf("gql: unsupported CAST type %s", e.to)
		}
		return f + "(" + s + ")", nil
	case *call:
		return t.emitCall(e, rec)
	case *labelTest:
		return "", fmt.Errorf("gql: label test outside a pattern")
	case *rawExpr:
		return e.text, nil
	}
	return "", fmt.Errorf("gql: unsupported expression %s", x.src())
}

func typedNull(thens []expr) string {
	for _, th := range thens {
		if l, ok := th.(*lit); ok {
			switch l.kind {
			case litString:
				return "tostring(dynamic(null))"
			case litInt:
				return "long(null)"
			case litFloat:
				return "real(null)"
			case litBool:
				return "bool(null)"
			}
		}
	}
	return "dynamic(null)"
}

var castFuncs = map[string]string{
	"STRING": "tostring", "VARCHAR": "tostring", "CHAR": "tostring", "CHARACTER VARYING": "tostring",
	"INT": "tolong", "INTEGER": "tolong", "INT64": "tolong", "BIGINT": "tolong", "LONG": "tolong",
	"INTEGER64": "tolong", "SIGNED INTEGER": "tolong", "INT32": "toint", "SMALLINT": "toint", "INTEGER32": "toint",
	"FLOAT": "todouble", "DOUBLE": "todouble", "REAL": "todouble", "FLOAT64": "todouble", "DOUBLE PRECISION": "todouble",
	"FLOAT32": "todouble", "DECIMAL": "todouble",
	"BOOL": "tobool", "BOOLEAN": "tobool",
	"DATETIME": "todatetime", "ZONED DATETIME": "todatetime", "TIMESTAMP": "todatetime", "LOCAL DATETIME": "todatetime",
	"DURATION": "totimespan",
}

func constJSON(x expr) (string, bool) {
	var conv func(expr) (interface{}, bool)
	conv = func(e expr) (interface{}, bool) {
		switch e := e.(type) {
		case *lit:
			switch e.kind {
			case litInt:
				n, err := strconv.ParseInt(e.val, 10, 64)
				return n, err == nil
			case litFloat:
				f, err := strconv.ParseFloat(e.val, 64)
				return f, err == nil
			case litString:
				return e.val, true
			case litBool:
				return e.val == "true", true
			}
			return nil, true
		case *listLit:
			out := []interface{}{}
			for _, it := range e.items {
				v, ok := conv(it)
				if !ok {
					return nil, false
				}
				out = append(out, v)
			}
			return out, true
		}
		return nil, false
	}
	v, ok := conv(x)
	if !ok {
		return "", false
	}
	b, err := json.Marshal(v)
	return string(b), err == nil
}

func isStringLit(x expr) bool {
	l, ok := x.(*lit)
	return ok && l.kind == litString
}

func (t *tr) emitBinary(e *binary, rec func(expr) (string, error)) (string, error) {
	l, err := rec(e.l)
	if err != nil {
		return "", err
	}
	if e.op == "in" {
		if list, ok := e.r.(*listLit); ok && len(list.items) > 0 {
			var parts []string
			for _, it := range list.items {
				s, err := rec(it)
				if err != nil {
					return "", err
				}
				parts = append(parts, s)
			}
			op := " in "
			if e.not {
				op = " !in "
			}
			return "(" + l + op + "(" + strings.Join(parts, ", ") + "))", nil
		}
		r, err := rec(e.r)
		if err != nil {
			return "", err
		}
		s := "set_has_element(" + r + ", " + l + ")"
		if e.not {
			s = "not(" + s + ")"
		}
		return s, nil
	}
	r, err := rec(e.r)
	if err != nil {
		return "", err
	}
	neg := func(s string) string {
		if e.not {
			return "not(" + s + ")"
		}
		return s
	}
	switch e.op {
	case "and", "or":
		return "(" + l + " " + e.op + " " + r + ")", nil
	case "xor":
		return "((" + l + " or " + r + ") and not(" + l + " and " + r + "))", nil
	case "=":
		return "(" + l + " == " + r + ")", nil
	case "<>":
		return "(" + l + " != " + r + ")", nil
	case "<", "<=", ">", ">=", "-", "*", "/", "%":
		return "(" + l + " " + e.op + " " + r + ")", nil
	case "+":
		if isStringLit(e.l) || isStringLit(e.r) {
			return "strcat(" + l + ", " + r + ")", nil
		}
		return "(" + l + " + " + r + ")", nil
	case "||":
		return "strcat(" + l + ", " + r + ")", nil
	case "^":
		return "pow(" + l + ", " + r + ")", nil
	case "starts":
		return neg("(" + l + " startswith_cs " + r + ")"), nil
	case "ends":
		return neg("(" + l + " endswith_cs " + r + ")"), nil
	case "contains":
		return neg("(" + l + " contains_cs " + r + ")"), nil
	case "=~":
		if lit, ok := e.r.(*lit); ok && lit.kind == litString {
			return "(" + l + " matches regex " + kqlString("^(?:"+lit.val+")$") + ")", nil
		}
		return "(" + l + " matches regex strcat(\"^(?:\", " + r + ", \")$\"))", nil
	}
	return "", fmt.Errorf("gql: unsupported operator %s", e.op)
}

// funcMap renames functions whose arguments carry over unchanged.
var funcMap = map[string]string{
	"upper": "toupper", "toupper": "toupper", "lower": "tolower", "tolower": "tolower",
	"char_length": "strlen", "character_length": "strlen",
	"string_join": "strcat_array", "replace": "replace_string",
	"tostring": "tostring", "tointeger": "tolong", "tofloat": "todouble", "toboolean": "tobool",
	"ceil": "ceiling", "ln": "log", "power": "pow",
	"keys": "bag_keys", "parse_json_string": "parse_json", "to_json_string": "tostring",
	"zoned_datetime": "todatetime", "local_datetime": "todatetime", "datetime": "todatetime",
	"size": "__gql_size", "ltrim": "__gql_ltrim", "rtrim": "__gql_rtrim",
}

func (t *tr) emitCall(c *call, rec func(expr) (string, error)) (string, error) {
	if c.star {
		return "", fmt.Errorf("gql: %s(*) is only valid as count(*) in RETURN", c.name)
	}
	args := make([]string, len(c.args))
	for i, a := range c.args {
		s, err := rec(a)
		if err != nil {
			return "", err
		}
		args[i] = s
	}
	n := len(args)
	switch c.name {
	case "trim", "btrim":
		if n == 1 {
			return `trim(@"\s+", ` + args[0] + ")", nil
		}
	case "__gql_ltrim", "ltrim":
		if n == 1 {
			return `trim_start(@"\s+", ` + args[0] + ")", nil
		}
	case "rtrim":
		if n == 1 {
			return `trim_end(@"\s+", ` + args[0] + ")", nil
		}
	case "left":
		if n == 2 {
			return "substring(" + args[0] + ", 0, " + args[1] + ")", nil
		}
	case "right":
		if n == 2 {
			return "substring(" + args[0] + ", max_of(strlen(" + args[0] + ") - " + args[1] + ", 0))", nil
		}
	case "mod":
		if n == 2 {
			return "(" + args[0] + " % " + args[1] + ")", nil
		}
	case "head":
		if n == 1 {
			return args[0] + "[0]", nil
		}
	case "last":
		if n == 1 {
			return args[0] + "[-1]", nil
		}
	case "tail":
		if n == 1 {
			return "array_slice(" + args[0] + ", 1, -1)", nil
		}
	case "e":
		if n == 0 {
			return "exp(1.0)", nil
		}
	case "length":
		if n == 1 {
			return "__gql_size(" + args[0] + ")", nil
		}
	case "zoned_datetime", "local_datetime", "current_timestamp", "current_datetime", "now":
		if n == 0 {
			return "now()", nil
		}
	case "duration_between":
		if n == 2 {
			return "(" + args[1] + " - " + args[0] + ")", nil
		}
	case "duration":
		if m, ok := c.args[0].(*mapLit); ok && n == 1 {
			units := map[string]string{"days": "1d", "hours": "1h", "minutes": "1m", "seconds": "1s",
				"milliseconds": "1ms", "microseconds": "1microsecond", "nanoseconds": "1tick / 100"}
			var parts []string
			for k, key := range m.keys {
				u, ok := units[strings.ToLower(key)]
				if !ok {
					return "", fmt.Errorf("gql: unsupported duration unit %s", key)
				}
				v, err := rec(m.vals[k])
				if err != nil {
					return "", err
				}
				parts = append(parts, "("+v+") * "+u)
			}
			if len(parts) == 0 {
				return "0s", nil
			}
			return "(" + strings.Join(parts, " + ") + ")", nil
		}
		if n == 1 {
			return "totimespan(" + args[0] + ")", nil
		}
	case "coalesce":
		return "coalesce(" + strings.Join(args, ", ") + ")", nil
	case "range":
		if n == 2 {
			return "range(" + args[0] + ", " + args[1] + ", 1)", nil
		}
	case "rand":
		return "rand()", nil
	case "nodes", "edges", "relationships", "path_length", "element_id", "id", "labels", "type", "properties":
		return "", fmt.Errorf("gql: %s() expects a pattern variable", c.name)
	case "exists":
		return "", fmt.Errorf("gql: EXISTS is not supported")
	}
	name := c.name
	if m, ok := funcMap[name]; ok {
		name = m
	}
	return name + "(" + strings.Join(args, ", ") + ")", nil
}
