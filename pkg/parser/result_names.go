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
// shortest-paths, where Kusto.Language names a function of a pattern
// property by the property alone: strlen(a.name) → strlen_name, while
// a bare a.name stays a_name. isVar reports pattern variable names.
func GraphResultName(e Expr, isVar func(string) bool) (string, bool) {
	fc, ok := e.(*FuncCall)
	if !ok || len(fc.Args) == 0 {
		return ExprResultName(e)
	}
	if ae, ok := fc.Args[0].(*AccessExpr); ok && len(ae.Path) == 1 && ae.Path[0].Name != "" {
		if ref, ok := ae.Object.(*ColumnRef); ok && isVar(ref.Name) {
			c := *fc
			c.Args = append([]Expr{&ColumnRef{Name: ae.Path[0].Name}}, fc.Args[1:]...)
			return callResultName(&c, scalarResultNames)
		}
	}
	return ExprResultName(e)
}
