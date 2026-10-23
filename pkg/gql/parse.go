package gql

import (
	"fmt"
	"strconv"
	"strings"
)

type parser struct {
	src    string
	toks   []token
	p      int
	cypher bool
}

func (ps *parser) peek() token { return ps.toks[ps.p] }
func (ps *parser) peekN(n int) token {
	if ps.p+n < len(ps.toks) {
		return ps.toks[ps.p+n]
	}
	return ps.toks[len(ps.toks)-1]
}
func (ps *parser) next() token {
	t := ps.toks[ps.p]
	if t.kind != tEOF {
		ps.p++
	}
	return t
}

func (ps *parser) errf(format string, a ...interface{}) error {
	t := ps.peek()
	near := "end of query"
	if t.kind != tEOF {
		near = fmt.Sprintf("%q", ps.src[t.pos:min(len(ps.src), t.pos+30)])
	}
	return fmt.Errorf("gql: "+format+" (near %s)", append(a, near)...)
}

func (ps *parser) acceptKw(k string) bool {
	if ps.peek().kw(k) {
		ps.p++
		return true
	}
	return false
}

func (ps *parser) acceptKws(ks ...string) bool {
	for i, k := range ks {
		if !ps.peekN(i).kw(k) {
			return false
		}
	}
	ps.p += len(ks)
	return true
}

func (ps *parser) expectKw(k string) error {
	if !ps.acceptKw(k) {
		return ps.errf("expected %s", strings.ToUpper(k))
	}
	return nil
}

func (ps *parser) accept(p string) bool {
	if ps.peek().is(p) {
		ps.p++
		return true
	}
	return false
}

func (ps *parser) expect(p string) error {
	if !ps.accept(p) {
		return ps.errf("expected %q", p)
	}
	return nil
}

// ident accepts a name: a plain identifier or a `quoted` one.
func (ps *parser) ident() (string, bool) {
	t := ps.peek()
	if t.kind == tIdent || t.kind == tQuotedIdent {
		ps.p++
		return t.text, true
	}
	return "", false
}

// clauseKeywords end an expression list or a clause.
var clauseKeywords = map[string]bool{
	"match": true, "optional": true, "where": true, "return": true, "with": true,
	"order": true, "limit": true, "offset": true, "skip": true, "next": true,
	"filter": true, "let": true, "for": true, "unwind": true, "union": true,
}

func (ps *parser) atClauseKeyword() bool {
	t := ps.peek()
	return t.kind == tEOF || (t.kind == tIdent && clauseKeywords[strings.ToLower(t.text)])
}

func parseQuery(src string, cypher bool) (*query, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	ps := &parser{src: src, toks: toks, cypher: cypher}
	q := &query{}
	for {
		part, err := ps.linear()
		if err != nil {
			return nil, err
		}
		q.parts = append(q.parts, part)
		if !ps.acceptKw("union") {
			break
		}
		all := ps.acceptKw("all")
		if !all {
			ps.acceptKw("distinct")
		}
		q.unionAll = append(q.unionAll, all)
	}
	ps.accept(";")
	if ps.peek().kind != tEOF {
		return nil, ps.errf("unexpected text")
	}
	return q, nil
}

func (ps *parser) linear() ([]clause, error) {
	var cs []clause
	for {
		t := ps.peek()
		if t.kind == tEOF || t.kw("union") || t.is(";") {
			break
		}
		c, err := ps.clause()
		if err != nil {
			return nil, err
		}
		cs = append(cs, c)
		if r, ok := c.(*returnClause); ok && r.with {
			cs = append(cs, &nextClause{})
		}
	}
	if len(cs) == 0 {
		return nil, ps.errf("empty query")
	}
	// A leading UNWIND / FOR over a constant list commutes with the
	// first MATCH (both are cross products).
	lead := 0
	for lead < len(cs) {
		f, ok := cs[lead].(*forClause)
		if !ok || hasVars(f.list) {
			break
		}
		lead++
	}
	if lead > 0 && lead < len(cs) {
		if _, ok := cs[lead].(*matchClause); ok {
			reordered := append([]clause{cs[lead]}, cs[:lead]...)
			cs = append(reordered, cs[lead+1:]...)
		}
	}
	if _, ok := cs[0].(*matchClause); !ok {
		return nil, fmt.Errorf("gql: a query must start with MATCH")
	}
	return cs, nil
}

func (ps *parser) clause() (clause, error) {
	t := ps.peek()
	switch {
	case t.kw("match"):
		ps.next()
		return ps.match(false)
	case t.kw("optional"):
		ps.next()
		if err := ps.expectKw("match"); err != nil {
			return nil, err
		}
		return ps.match(true)
	case t.kw("where"), t.kw("filter"):
		ps.next()
		ps.acceptKw("where") // FILTER WHERE
		e, err := ps.expr()
		if err != nil {
			return nil, err
		}
		return &filterClause{cond: e}, nil
	case t.kw("let"):
		ps.next()
		lc := &letClause{}
		for {
			name, ok := ps.ident()
			if !ok {
				return nil, ps.errf("expected a LET variable")
			}
			if err := ps.expect("="); err != nil {
				return nil, err
			}
			e, err := ps.expr()
			if err != nil {
				return nil, err
			}
			lc.names = append(lc.names, name)
			lc.exprs = append(lc.exprs, e)
			if !ps.accept(",") {
				break
			}
		}
		return lc, nil
	case t.kw("for"):
		ps.next()
		name, ok := ps.ident()
		if !ok {
			return nil, ps.errf("expected a FOR variable")
		}
		if err := ps.expectKw("in"); err != nil {
			return nil, err
		}
		e, err := ps.expr()
		if err != nil {
			return nil, err
		}
		fc := &forClause{name: name, list: e}
		if ps.peek().kw("with") && (ps.peekN(1).kw("offset") || ps.peekN(1).kw("ordinality")) {
			ps.next()
			fc.ordinality = ps.next().kw("ordinality")
			if fc.idx, ok = ps.ident(); !ok {
				return nil, ps.errf("expected an index variable")
			}
		}
		return fc, nil
	case t.kw("unwind"):
		ps.next()
		e, err := ps.expr()
		if err != nil {
			return nil, err
		}
		if err := ps.expectKw("as"); err != nil {
			return nil, err
		}
		name, ok := ps.ident()
		if !ok {
			return nil, ps.errf("expected an UNWIND variable")
		}
		return &forClause{name: name, list: e}, nil
	case t.kw("order"):
		ps.next()
		if err := ps.expectKw("by"); err != nil {
			return nil, err
		}
		oc := &orderClause{}
		for {
			e, err := ps.expr()
			if err != nil {
				return nil, err
			}
			it := orderItem{x: e}
			switch {
			case ps.acceptKw("desc"), ps.acceptKw("descending"):
				it.desc = true
			case ps.acceptKw("asc"), ps.acceptKw("ascending"):
			}
			if ps.acceptKws("nulls", "first") {
				it.nulls = "first"
			} else if ps.acceptKws("nulls", "last") {
				it.nulls = "last"
			}
			oc.items = append(oc.items, it)
			if !ps.accept(",") {
				break
			}
		}
		return oc, nil
	case t.kw("offset"), t.kw("skip"):
		ps.next()
		e, err := ps.expr()
		if err != nil {
			return nil, err
		}
		return &offsetClause{n: e}, nil
	case t.kw("limit"):
		ps.next()
		e, err := ps.expr()
		if err != nil {
			return nil, err
		}
		return &limitClause{n: e}, nil
	case t.kw("return"), t.kw("with"):
		ps.next()
		return ps.returnClause(t.kw("with"))
	case t.kw("next"):
		ps.next()
		if ps.acceptKw("yield") {
			// NEXT YIELD a, b: the projection is the previous RETURN's.
			for {
				if _, ok := ps.ident(); !ok {
					return nil, ps.errf("expected a YIELD name")
				}
				if !ps.accept(",") {
					break
				}
			}
		}
		return &nextClause{}, nil
	}
	return nil, ps.errf("expected a clause")
}

func (ps *parser) returnClause(with bool) (clause, error) {
	rc := &returnClause{with: with}
	if ps.acceptKw("distinct") {
		rc.distinct = true
	} else {
		ps.acceptKw("all")
	}
	if ps.accept("*") {
		rc.star = true
		if !ps.accept(",") {
			return rc, nil
		}
	}
	for {
		e, err := ps.expr()
		if err != nil {
			return nil, err
		}
		it := returnItem{x: e}
		if ps.acceptKw("as") {
			name, ok := ps.ident()
			if !ok {
				return nil, ps.errf("expected an alias after AS")
			}
			it.alias = name
		}
		rc.items = append(rc.items, it)
		if !ps.accept(",") {
			break
		}
	}
	return rc, nil
}

func (ps *parser) match(optional bool) (clause, error) {
	mc := &matchClause{optional: optional}
	// Path-search / match modes before the patterns.
	for {
		switch {
		case ps.acceptKw("walk"):
			mc.mode = "all"
		case ps.acceptKw("trail"):
			mc.mode = "unique_edges"
		case ps.acceptKw("acyclic"), ps.acceptKw("simple"):
			mc.mode = "none"
		case ps.peek().kw("repeatable") || ps.peek().kw("different"):
			// REPEATABLE ELEMENTS / DIFFERENT EDGES|RELATIONSHIPS
			ps.next()
			ps.next()
		default:
			goto patterns
		}
		ps.acceptKw("path")
		ps.acceptKw("paths")
	}
patterns:
	for {
		pp, err := ps.pathPattern()
		if err != nil {
			return nil, err
		}
		mc.paths = append(mc.paths, pp)
		if !ps.accept(",") {
			break
		}
	}
	if ps.acceptKw("where") {
		e, err := ps.expr()
		if err != nil {
			return nil, err
		}
		mc.where = e
	}
	return mc, nil
}

func (ps *parser) pathPattern() (*pathPat, error) {
	pp := &pathPat{}
	ps.searchPrefix(pp)
	if (ps.peek().kind == tIdent || ps.peek().kind == tQuotedIdent) && ps.peekN(1).is("=") {
		pp.name, _ = ps.ident()
		ps.next()
	}
	ps.searchPrefix(pp)
	// openCypher shortestPath(...) / allShortestPaths(...).
	wrapped := false
	if (ps.peek().kw("shortestpath") || ps.peek().kw("allshortestpaths")) && ps.peekN(1).is("(") {
		if ps.next().kw("allshortestpaths") {
			pp.shortest = "all"
		} else {
			pp.shortest = "any"
		}
		ps.next()
		wrapped = true
	}
	n, err := ps.nodePattern()
	if err != nil {
		return nil, err
	}
	pp.nodes = append(pp.nodes, n)
	for {
		e, ok, err := ps.edgePattern()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		n, err := ps.nodePattern()
		if err != nil {
			return nil, err
		}
		pp.edges = append(pp.edges, e)
		pp.nodes = append(pp.nodes, n)
	}
	if wrapped {
		if err := ps.expect(")"); err != nil {
			return nil, err
		}
	}
	return pp, nil
}

// searchPrefix parses an optional path search prefix (ANY SHORTEST,
// ALL SHORTEST, SHORTEST k, ANY [k], ALL) and path mode.
func (ps *parser) searchPrefix(pp *pathPat) {
	switch {
	case ps.acceptKws("all", "shortest"):
		pp.shortest = "all"
	case ps.acceptKws("any", "shortest"):
		pp.shortest = "any"
	case ps.peek().kw("shortest") && ps.peekN(1).kind == tInt:
		ps.p += 2
		pp.shortest = "any"
	case ps.acceptKw("shortest"):
		pp.shortest = "any"
	case ps.peek().kw("any") && !ps.peekN(1).is("("):
		ps.next()
		if ps.peek().kind == tInt {
			ps.next()
		}
	case ps.acceptKw("all"):
	}
	for _, m := range []string{"walk", "trail", "acyclic", "simple"} {
		if ps.acceptKw(m) {
			ps.acceptKw("path")
			ps.acceptKw("paths")
		}
	}
}

func (ps *parser) nodePattern() (*nodePat, error) {
	if err := ps.expect("("); err != nil {
		return nil, err
	}
	n := &nodePat{}
	var err error
	n.name, _ = ps.ident()
	if n.label, err = ps.labelSpec(); err != nil {
		return nil, err
	}
	if ps.peek().is("{") {
		if n.keys, n.vals, err = ps.propMap(); err != nil {
			return nil, err
		}
	}
	if ps.acceptKw("where") {
		if n.where, err = ps.expr(); err != nil {
			return nil, err
		}
	}
	if err := ps.expect(")"); err != nil {
		return nil, err
	}
	return n, nil
}

// labelSpec parses an optional ": labelExpr" / "IS labelExpr".
func (ps *parser) labelSpec() (labelExpr, error) {
	if !ps.accept(":") && !ps.acceptKw("is") {
		return nil, nil
	}
	return ps.labelOrExpr()
}

func (ps *parser) labelOrExpr() (labelExpr, error) {
	l, err := ps.labelAndExpr()
	if err != nil {
		return nil, err
	}
	for ps.accept("|") {
		ps.accept(":") // openCypher :A|:B
		r, err := ps.labelAndExpr()
		if err != nil {
			return nil, err
		}
		l = labelOr{l, r}
	}
	return l, nil
}

func (ps *parser) labelAndExpr() (labelExpr, error) {
	l, err := ps.labelFactor()
	if err != nil {
		return nil, err
	}
	for ps.peek().is("&") || ps.peek().is(":") {
		ps.next()
		r, err := ps.labelFactor()
		if err != nil {
			return nil, err
		}
		l = labelAnd{l, r}
	}
	return l, nil
}

func (ps *parser) labelFactor() (labelExpr, error) {
	switch {
	case ps.accept("!"):
		x, err := ps.labelFactor()
		if err != nil {
			return nil, err
		}
		return labelNot{x}, nil
	case ps.accept("%"):
		return labelAny{}, nil
	case ps.accept("("):
		x, err := ps.labelOrExpr()
		if err != nil {
			return nil, err
		}
		return x, ps.expect(")")
	}
	name, ok := ps.ident()
	if !ok {
		return nil, ps.errf("expected a label")
	}
	return labelName{name}, nil
}

func (ps *parser) propMap() ([]string, []expr, error) {
	if err := ps.expect("{"); err != nil {
		return nil, nil, err
	}
	var keys []string
	var vals []expr
	if ps.accept("}") {
		return keys, vals, nil
	}
	for {
		k, ok := ps.ident()
		if !ok {
			if t := ps.peek(); t.kind == tString {
				ps.next()
				k = t.text
			} else {
				return nil, nil, ps.errf("expected a property name")
			}
		}
		if err := ps.expect(":"); err != nil {
			return nil, nil, err
		}
		v, err := ps.expr()
		if err != nil {
			return nil, nil, err
		}
		keys = append(keys, k)
		vals = append(vals, v)
		if !ps.accept(",") {
			break
		}
	}
	return keys, vals, ps.expect("}")
}

// edgePattern parses an edge between two node patterns, or reports
// ok=false when the path ends.
func (ps *parser) edgePattern() (*edgePat, bool, error) {
	t0, t1, t2 := ps.peek(), ps.peekN(1), ps.peekN(2)
	e := &edgePat{min: 1, max: 1}
	backward := false
	switch {
	case t0.is("<") && t1.is("-"):
		backward = true
		ps.p += 2
	case t0.is("-"):
		ps.p++
	case t0.is("~") || (t0.is("<") && t1.is("~")):
		return nil, false, ps.errf("undirected (~) edges are not supported")
	default:
		return nil, false, nil
	}
	_ = t2
	if ps.peek().is("[") {
		ps.next()
		if err := ps.edgeFiller(e); err != nil {
			return nil, false, err
		}
		if err := ps.expect("]"); err != nil {
			return nil, false, err
		}
		if err := ps.expect("-"); err != nil {
			return nil, false, err
		}
	} else if ps.peek().is("-") && !backward {
		// openCypher --> / -- (and <-- handled below).
		ps.next()
	} else if backward && ps.peek().is("-") {
		ps.next() // <--
	}
	switch {
	case backward:
		if ps.peek().is(">") {
			return nil, false, ps.errf("bidirectional <-> edges are not supported")
		}
		e.dir = dirBackward
	case ps.accept(">"):
		e.dir = dirForward
	default:
		e.dir = dirAny
	}
	// GQL quantifiers after the edge.
	switch {
	case ps.peek().is("{") && (ps.peekN(1).kind == tInt || ps.peekN(1).is(",")):
		ps.next()
		e.varLen = true
		lo, hi := 0, -1
		if ps.peek().kind == tInt {
			lo, _ = strconv.Atoi(ps.next().text)
			hi = lo
		}
		if ps.accept(",") {
			hi = -1
			if ps.peek().kind == tInt {
				hi, _ = strconv.Atoi(ps.next().text)
			}
		}
		if err := ps.expect("}"); err != nil {
			return nil, false, err
		}
		e.min, e.max = lo, hi
	case ps.peek().is("*") && !e.varLen:
		ps.next()
		e.varLen, e.min, e.max = true, 0, -1
	case ps.peek().is("+") && !e.varLen:
		ps.next()
		e.varLen, e.min, e.max = true, 1, -1
	case ps.peek().is("?") && !e.varLen:
		ps.next()
		e.varLen, e.min, e.max = true, 0, 1
	}
	return e, true, nil
}

// edgeFiller parses the inside of [ ... ]: [name][:label][*range][{props}][WHERE cond].
func (ps *parser) edgeFiller(e *edgePat) error {
	var err error
	e.name, _ = ps.ident()
	if e.label, err = ps.labelSpec(); err != nil {
		return err
	}
	if ps.accept("*") {
		// openCypher variable length: *, *n, *m..n, *..n, *m..
		e.varLen, e.min, e.max = true, 1, -1
		if ps.peek().kind == tInt {
			e.min, _ = strconv.Atoi(ps.next().text)
			e.max = e.min
		}
		if ps.accept("..") {
			e.max = -1
			if ps.peek().kind == tInt {
				e.max, _ = strconv.Atoi(ps.next().text)
			}
		}
	}
	if ps.peek().is("{") {
		if e.keys, e.vals, err = ps.propMap(); err != nil {
			return err
		}
	}
	if ps.acceptKw("where") {
		if e.where, err = ps.expr(); err != nil {
			return err
		}
	}
	return nil
}

// Expressions, lowest precedence first.

func (ps *parser) wrap(start int, build func(text string) expr) expr {
	end := ps.toks[ps.p-1].end
	return build(strings.TrimSpace(ps.src[start:end]))
}

func (ps *parser) expr() (expr, error) { return ps.orExpr() }

func (ps *parser) orExpr() (expr, error) {
	start := ps.peek().pos
	l, err := ps.xorExpr()
	if err != nil {
		return nil, err
	}
	for ps.acceptKw("or") {
		r, err := ps.xorExpr()
		if err != nil {
			return nil, err
		}
		ll := l
		l = ps.wrap(start, func(s string) expr { return &binary{exprBase{s}, "or", false, ll, r} })
	}
	return l, nil
}

func (ps *parser) xorExpr() (expr, error) {
	start := ps.peek().pos
	l, err := ps.andExpr()
	if err != nil {
		return nil, err
	}
	for ps.acceptKw("xor") {
		r, err := ps.andExpr()
		if err != nil {
			return nil, err
		}
		ll := l
		l = ps.wrap(start, func(s string) expr { return &binary{exprBase{s}, "xor", false, ll, r} })
	}
	return l, nil
}

func (ps *parser) andExpr() (expr, error) {
	start := ps.peek().pos
	l, err := ps.notExpr()
	if err != nil {
		return nil, err
	}
	for ps.acceptKw("and") {
		r, err := ps.notExpr()
		if err != nil {
			return nil, err
		}
		ll := l
		l = ps.wrap(start, func(s string) expr { return &binary{exprBase{s}, "and", false, ll, r} })
	}
	return l, nil
}

func (ps *parser) notExpr() (expr, error) {
	start := ps.peek().pos
	if ps.acceptKw("not") {
		x, err := ps.notExpr()
		if err != nil {
			return nil, err
		}
		return ps.wrap(start, func(s string) expr { return &unary{exprBase{s}, "not", x} }), nil
	}
	return ps.cmpExpr()
}

func (ps *parser) cmpExpr() (expr, error) {
	start := ps.peek().pos
	l, err := ps.addExpr()
	if err != nil {
		return nil, err
	}
	for {
		t := ps.peek()
		var op string
		not := false
		switch {
		case t.is("="), t.is("<>"), t.is("!="), t.is("<"), t.is("<="), t.is(">"), t.is(">="), t.is("=~"):
			ps.next()
			op = t.text
			if op == "!=" {
				op = "<>"
			}
		case t.kw("is"):
			ps.next()
			n := ps.acceptKw("not")
			if !ps.acceptKw("null") {
				return nil, ps.errf("expected NULL after IS")
			}
			ll := l
			l = ps.wrap(start, func(s string) expr { return &isNull{exprBase{s}, ll, n} })
			continue
		case t.kw("in"):
			ps.next()
			op = "in"
		case t.kw("starts") && ps.peekN(1).kw("with"):
			ps.p += 2
			op = "starts"
		case t.kw("ends") && ps.peekN(1).kw("with"):
			ps.p += 2
			op = "ends"
		case t.kw("contains"):
			ps.next()
			op = "contains"
		case t.kw("not") && ps.peekN(1).kw("in"):
			ps.p += 2
			op, not = "in", true
		case t.kw("not") && ps.peekN(1).kw("contains"):
			ps.p += 2
			op, not = "contains", true
		case t.kw("not") && (ps.peekN(1).kw("starts") || ps.peekN(1).kw("ends")) && ps.peekN(2).kw("with"):
			op = "starts"
			if ps.peekN(1).kw("ends") {
				op = "ends"
			}
			ps.p += 3
			not = true
		default:
			return l, nil
		}
		r, err := ps.addExpr()
		if err != nil {
			return nil, err
		}
		ll, o, nn := l, op, not
		l = ps.wrap(start, func(s string) expr { return &binary{exprBase{s}, o, nn, ll, r} })
	}
}

func (ps *parser) addExpr() (expr, error) {
	start := ps.peek().pos
	l, err := ps.mulExpr()
	if err != nil {
		return nil, err
	}
	for {
		t := ps.peek()
		if !(t.is("+") || t.is("-") || t.is("||")) {
			return l, nil
		}
		ps.next()
		r, err := ps.mulExpr()
		if err != nil {
			return nil, err
		}
		ll := l
		l = ps.wrap(start, func(s string) expr { return &binary{exprBase{s}, t.text, false, ll, r} })
	}
}

func (ps *parser) mulExpr() (expr, error) {
	start := ps.peek().pos
	l, err := ps.unaryExpr()
	if err != nil {
		return nil, err
	}
	for {
		t := ps.peek()
		if !(t.is("*") || t.is("/") || t.is("%") || t.is("^")) {
			return l, nil
		}
		ps.next()
		r, err := ps.unaryExpr()
		if err != nil {
			return nil, err
		}
		ll := l
		l = ps.wrap(start, func(s string) expr { return &binary{exprBase{s}, t.text, false, ll, r} })
	}
}

func (ps *parser) unaryExpr() (expr, error) {
	start := ps.peek().pos
	if t := ps.peek(); t.is("-") || t.is("+") {
		ps.next()
		x, err := ps.unaryExpr()
		if err != nil {
			return nil, err
		}
		return ps.wrap(start, func(s string) expr { return &unary{exprBase{s}, t.text, x} }), nil
	}
	return ps.postfix()
}

func (ps *parser) postfix() (expr, error) {
	start := ps.peek().pos
	x, err := ps.primary()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case ps.peek().is("."):
			ps.next()
			name, ok := ps.ident()
			if !ok {
				return nil, ps.errf("expected a property name after '.'")
			}
			xx := x
			x = ps.wrap(start, func(s string) expr { return &propRef{exprBase{s}, xx, name} })
		case ps.peek().is("["):
			ps.next()
			i, err := ps.expr()
			if err != nil {
				return nil, err
			}
			if err := ps.expect("]"); err != nil {
				return nil, err
			}
			xx := x
			x = ps.wrap(start, func(s string) expr { return &indexRef{exprBase{s}, xx, i} })
		default:
			return x, nil
		}
	}
}

func (ps *parser) primary() (expr, error) {
	start := ps.peek().pos
	t := ps.peek()
	switch t.kind {
	case tInt:
		ps.next()
		return &lit{exprBase{t.text}, litInt, t.text}, nil
	case tFloat:
		ps.next()
		return &lit{exprBase{t.text}, litFloat, t.text}, nil
	case tString:
		ps.next()
		return &lit{exprBase{ps.src[t.pos:t.end]}, litString, t.text}, nil
	case tParam:
		ps.next()
		return &param{exprBase{"$" + t.text}, t.text}, nil
	case tQuotedIdent:
		ps.next()
		return &varRef{exprBase{t.text}, t.text}, nil
	case tPunct:
		switch t.text {
		case "(":
			ps.next()
			x, err := ps.expr()
			if err != nil {
				return nil, err
			}
			return x, ps.expect(")")
		case "[":
			ps.next()
			if (ps.peek().kind == tIdent || ps.peek().kind == tQuotedIdent) && ps.peekN(1).kw("in") {
				return nil, ps.errf("list comprehensions ([x IN list | ...]) are not supported")
			}
			var items []expr
			if !ps.accept("]") {
				for {
					x, err := ps.expr()
					if err != nil {
						return nil, err
					}
					items = append(items, x)
					if !ps.accept(",") {
						break
					}
				}
				if err := ps.expect("]"); err != nil {
					return nil, err
				}
			}
			return ps.wrap(start, func(s string) expr { return &listLit{exprBase{s}, items} }), nil
		case "{":
			keys, vals, err := ps.propMap()
			if err != nil {
				return nil, err
			}
			return ps.wrap(start, func(s string) expr { return &mapLit{exprBase{s}, keys, vals} }), nil
		}
		return nil, ps.errf("unexpected %q", t.text)
	case tIdent:
	default:
		return nil, ps.errf("expected an expression")
	}
	low := strings.ToLower(t.text)
	switch low {
	case "true", "false":
		ps.next()
		return &lit{exprBase{t.text}, litBool, low}, nil
	case "null":
		ps.next()
		return &lit{exprBase{t.text}, litNull, ""}, nil
	case "case":
		ps.next()
		return ps.caseExpr(start)
	case "cast":
		if ps.peekN(1).is("(") {
			ps.p += 2
			x, err := ps.expr()
			if err != nil {
				return nil, err
			}
			if err := ps.expectKw("as"); err != nil {
				return nil, err
			}
			var tn []string
			for ps.peek().kind == tIdent {
				tn = append(tn, strings.ToUpper(ps.next().text))
			}
			if ps.accept("(") { // VARCHAR(10) and the like
				for !ps.accept(")") {
					ps.next()
				}
			}
			if err := ps.expect(")"); err != nil {
				return nil, err
			}
			return ps.wrap(start, func(s string) expr { return &castExpr{exprBase{s}, x, strings.Join(tn, " ")} }), nil
		}
	}
	ps.next()
	if ps.peek().is("(") {
		ps.next()
		c := &call{name: low}
		if ps.accept("*") {
			c.star = true
		} else if !ps.peek().is(")") {
			if ps.acceptKw("distinct") {
				c.distinct = true
			} else {
				ps.acceptKw("all")
			}
			for {
				x, err := ps.expr()
				if err != nil {
					return nil, err
				}
				c.args = append(c.args, x)
				if !ps.accept(",") {
					break
				}
			}
		}
		if err := ps.expect(")"); err != nil {
			return nil, err
		}
		return ps.wrap(start, func(s string) expr { c.text = s; return c }), nil
	}
	return &varRef{exprBase{t.text}, t.text}, nil
}

func (ps *parser) caseExpr(start int) (expr, error) {
	ce := &caseExpr{}
	if !ps.peek().kw("when") {
		x, err := ps.expr()
		if err != nil {
			return nil, err
		}
		ce.operand = x
	}
	for ps.acceptKw("when") {
		w, err := ps.expr()
		if err != nil {
			return nil, err
		}
		if err := ps.expectKw("then"); err != nil {
			return nil, err
		}
		th, err := ps.expr()
		if err != nil {
			return nil, err
		}
		ce.whens = append(ce.whens, w)
		ce.thens = append(ce.thens, th)
	}
	if ps.acceptKw("else") {
		x, err := ps.expr()
		if err != nil {
			return nil, err
		}
		ce.els = x
	}
	if err := ps.expectKw("end"); err != nil {
		return nil, err
	}
	if len(ce.whens) == 0 {
		return nil, fmt.Errorf("gql: CASE needs at least one WHEN")
	}
	return ps.wrap(start, func(s string) expr { ce.text = s; return ce }), nil
}

func hasVars(x expr) bool {
	found := false
	walk(x, func(e expr) bool {
		if _, ok := e.(*varRef); ok {
			found = true
		}
		return !found
	})
	return found
}
