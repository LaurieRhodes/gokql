package gql

// Expressions. Every node records the source text it was parsed from,
// which names RETURN items that have no alias.

type expr interface{ src() string }

type exprBase struct{ text string }

func (b exprBase) src() string { return b.text }

type litKind int

const (
	litInt litKind = iota
	litFloat
	litString
	litBool
	litNull
)

type lit struct {
	exprBase
	kind litKind
	val  string // number text, decoded string, "true"/"false"
}

type listLit struct {
	exprBase
	items []expr
}

type mapLit struct {
	exprBase
	keys []string
	vals []expr
}

type varRef struct {
	exprBase
	name string
}

type param struct {
	exprBase
	name string
}

type propRef struct {
	exprBase
	x    expr
	name string
}

type indexRef struct {
	exprBase
	x, i expr
}

type unary struct {
	exprBase
	op string // "not", "-", "+"
	x  expr
}

type binary struct {
	exprBase
	op   string // or xor and = <> < <= > >= + - * / % || in starts ends contains =~
	not  bool   // NOT IN / NOT STARTS WITH / ...
	l, r expr
}

type isNull struct {
	exprBase
	x   expr
	not bool
}

type call struct {
	exprBase
	name     string // lower case
	args     []expr
	distinct bool
	star     bool // count(*)
}

type caseExpr struct {
	exprBase
	operand expr // simple CASE x WHEN ...; nil for searched CASE
	whens   []expr
	thens   []expr
	els     expr
}

type castExpr struct {
	exprBase
	x  expr
	to string // upper-case type name
}

// Patterns.

type labelExpr interface{}

type labelName struct{ name string }
type labelOr struct{ l, r labelExpr }
type labelAnd struct{ l, r labelExpr }
type labelNot struct{ x labelExpr }
type labelAny struct{}

type nodePat struct {
	name  string
	label labelExpr
	keys  []string
	vals  []expr
	where expr
}

type edgeDir int

const (
	dirForward edgeDir = iota
	dirBackward
	dirAny
)

type edgePat struct {
	name     string
	label    labelExpr
	keys     []string
	vals     []expr
	where    expr
	dir      edgeDir
	varLen   bool
	min, max int // max < 0: unbounded
}

type pathPat struct {
	name     string // path variable, "" if none
	shortest string // "", "any" or "all"
	nodes    []*nodePat
	edges    []*edgePat // edges[i] joins nodes[i] and nodes[i+1]
}

// Clauses.

type clause interface{}

type matchClause struct {
	optional bool
	mode     string // cycles: "", "all" (WALK), "none" (ACYCLIC/SIMPLE), "unique_edges" (TRAIL)
	paths    []*pathPat
	where    expr
}

type filterClause struct{ cond expr }

type letClause struct {
	names []string
	exprs []expr
}

type forClause struct {
	name       string
	list       expr
	idx        string
	ordinality bool // WITH ORDINALITY: 1-based index; WITH OFFSET: 0-based
}

type orderItem struct {
	x     expr
	desc  bool
	nulls string // "", "first", "last"
}

type orderClause struct{ items []orderItem }
type offsetClause struct{ n expr }
type limitClause struct{ n expr }

type returnItem struct {
	x     expr
	alias string // "" when none
}

type returnClause struct {
	distinct bool
	star     bool
	items    []returnItem
	with     bool // openCypher WITH: a projection that continues the query
}

// nextClause separates the linear parts of a query (GQL NEXT; an
// openCypher WITH implies one).
type nextClause struct{}

// query is one or more linear queries combined with UNION.
type query struct {
	parts    [][]clause
	unionAll []bool // unionAll[i] joins parts[i] and parts[i+1]
}

// patternExpr is a pattern used as an expression: an openCypher pattern
// predicate or exists(...) ("exists"), COUNT { ... } or size(pattern)
// ("count"), or a pattern comprehension [pattern WHERE c | x] ("list").
type patternExpr struct {
	exprBase
	kind  string
	paths []*pathPat
	where expr
	proj  expr // "list" only
}

// listExpr is an openCypher list expression over a variable v bound to
// each element of list in turn: a comprehension [v IN list WHERE c | p]
// ("comp"), a quantifier any/all/none/single(v IN list WHERE c), or
// reduce(acc = init, v IN list | proj).
type listExpr struct {
	exprBase
	kind  string
	v     string
	list  expr
	where expr
	proj  expr
	acc   string // reduce
	init  expr   // reduce
}

// mapProj is an openCypher map projection x {.a, k: v, .*}.
type mapProj struct {
	exprBase
	x    expr
	keys []string
	vals []expr
	star bool
}
