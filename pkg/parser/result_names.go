package parser

// result_names.go — generated names for unnamed expressions, as
// Kusto.Language derives them (functions' result-name kinds are in
// result_names_gen.go):
//
//	column x                → x
//	path d.a.b, d['a'][0]  → d_a_b, d_a_0
//	f(x) by f's kind:
//	  FirstArgument          → the first argument's name (tostring(x) → x)
//	  NameAndFirstArgument   → f_<arg> (strlen(s) → strlen_s, strlen('a') → strlen_)
//	  PrefixAndFirstArgument → <prefix>_<arg> (sum(x) → sum_x, make_list(x) → list_x)
//	  PrefixOnly             → <prefix>
//	  OnlyArgument           → the argument's name, with exactly one argument
//
// Anything else has no derived name (ok false); extend, project and
// summarize by then name it ColumnN.

import (
	"fmt"
	"strings"
)

type resultNameKind int

const (
	ResultFirstArgument resultNameKind = iota + 1
	ResultNameAndFirstArgument
	ResultPrefixAndFirstArgument
	ResultPrefixOnly
	ResultOnlyArgument
	ResultFirstArgumentValueIfColumn
)

type resultName struct {
	kind   resultNameKind
	prefix string
}

// ExprResultName is the generated name of an unnamed expression.
func ExprResultName(e Expr) (string, bool) {
	return exprResultName(e, scalarResultNames)
}

// AggResultName is the generated name of an unnamed aggregate call.
func AggResultName(fc *FuncCall) (string, bool) {
	return callResultName(fc, aggregateResultNames)
}

func exprResultName(e Expr, table map[string]resultName) (string, bool) {
	switch x := e.(type) {
	case *ColumnRef:
		return x.Name, true
	case *AccessExpr:
		base, ok := exprResultName(x.Object, scalarResultNames)
		if !ok {
			return "", false
		}
		parts := []string{base}
		for _, k := range x.Path {
			switch {
			case k.Name != "":
				parts = append(parts, k.Name)
			case k.Expr == nil && k.Index >= 0:
				parts = append(parts, fmt.Sprint(k.Index))
			default:
				return "", false
			}
		}
		return strings.Join(parts, "_"), true
	case *FuncCall:
		return callResultName(x, table)
	}
	return "", false
}

func callResultName(fc *FuncCall, table map[string]resultName) (string, bool) {
	rn, ok := table[fc.Name]
	if !ok {
		return "", false
	}
	first := func() (string, bool) {
		if len(fc.Args) == 0 {
			return "", false
		}
		return exprResultName(fc.Args[0], scalarResultNames)
	}
	switch rn.kind {
	case ResultFirstArgument:
		return first()
	case ResultOnlyArgument:
		if len(fc.Args) != 1 {
			return "", false
		}
		return first()
	case ResultNameAndFirstArgument:
		a, _ := first()
		return fc.Name + "_" + a, true
	case ResultPrefixAndFirstArgument:
		a, _ := first()
		return rn.prefix + "_" + a, true
	case ResultPrefixOnly:
		return rn.prefix, true
	}
	return "", false
}

// UniqueName returns name, or name followed by 1, 2, ... when taken.
func UniqueName(name string, taken func(string) bool) string {
	if !taken(name) {
		return name
	}
	for i := 1; ; i++ {
		if n := fmt.Sprintf("%s%d", name, i); !taken(n) {
			return n
		}
	}
}

// GraphResultName is ExprResultName inside graph-match / graph-
// shortest-paths, as the engine (the Kusto emulator) names results:
//
//	a.name (bare)                    → a_name
//	strlen(a.name), abs(strlen(a.x)) → strlen_ (a pattern property
//	                                   inside a function has no name)
//	tostring(a.name)                 → none (ColumnN)
//	labels(a), node_degree_out(a)    → labels_a, node_degree_out_a
//	map(e, s), all(e, true)          → e_s, e_Column
//
// isVar reports pattern variable names. For a call the name is always
// settled (ok); "" then means ColumnN.
func GraphResultName(e Expr, isVar func(string) bool) (string, bool) {
	fc, ok := e.(*FuncCall)
	if !ok || len(fc.Args) == 0 {
		return ExprResultName(e)
	}
	if ref, ok := fc.Args[0].(*ColumnRef); ok && isVar(ref.Name) {
		switch fc.Name {
		case "labels", "node_degree_in", "node_degree_out", "node_id":
			return fc.Name + "_" + ref.Name, true
		case "map", "all", "any":
			n := "Column"
			if len(fc.Args) > 1 {
				if a, ok := ExprResultName(fc.Args[1]); ok && a != "" {
					n = a
				}
			}
			return ref.Name + "_" + n, true
		}
	}
	// A call's name is settled here: "" (with ok) means none, so
	// the caller numbers it ColumnN.
	n, ok := ExprResultName(blankPatternProps(fc, isVar))
	if !ok {
		n = ""
	}
	return n, true
}

// blankPatternProps copies a call with every pattern property v.p in
// its arguments (at any depth) replaced by a column of no name.
func blankPatternProps(e Expr, isVar func(string) bool) Expr {
	switch x := e.(type) {
	case *AccessExpr:
		if ref, ok := x.Object.(*ColumnRef); ok && isVar(ref.Name) {
			return &ColumnRef{Name: ""}
		}
	case *FuncCall:
		c := *x
		c.Args = make([]Expr, len(x.Args))
		for i, a := range x.Args {
			c.Args[i] = blankPatternProps(a, isVar)
		}
		return &c
	}
	return e
}
