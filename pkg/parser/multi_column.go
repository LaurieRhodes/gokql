package parser

// multi_column.go — functions that return several columns:
//
//	extend (Name1, Name2, ...) = series_stats(x [, ignore_nonfinite])
//	project series_stats(x)
//
// series_stats, series_fit_line and series_fit_poly are expanded into
// one assignment per output, each reading its key from the function's
// single-bag form (series_stats_dynamic, series_fit_line_dynamic, and
// the internal __series_fit_poly_dynamic). Written names apply in order; the
// rest are <function>_<x>_<output>, as Kusto.Language names them
// (it keeps every column even when only some are named).

import "strings"

type multiKey struct{ key, conv string }

// multiColumnFuncs: each multi-column function, the function returning
// the same statistics as one bag, and its columns in order.
var multiColumnFuncs = map[string]struct {
	bag  string
	keys []multiKey
}{
	"series_stats": {"series_stats_dynamic", []multiKey{
		{"min", "todouble"}, {"min_idx", "tolong"}, {"max", "todouble"}, {"max_idx", "tolong"},
		{"avg", "todouble"}, {"stdev", "todouble"}, {"variance", "todouble"}}},
	"series_fit_line": {"series_fit_line_dynamic", []multiKey{
		{"rsquare", "todouble"}, {"slope", "todouble"}, {"variance", "todouble"},
		{"rvariance", "todouble"}, {"interception", "todouble"}, {"line_fit", "todynamic"}}},
	"series_fit_poly": {"__series_fit_poly_dynamic", []multiKey{
		{"rsquare", "todouble"}, {"coefficients", "todynamic"}, {"variance", "todouble"},
		{"rvariance", "todouble"}, {"poly_fit", "todynamic"}}},
}

// expandMultiColumn returns the assignments a multi-column call
// stands for, or nil when expr is not one. name is the written name
// ("" when none; "(a, b)" for a tuple).
func expandMultiColumn(name string, expr Expr) []Assignment {
	fc, ok := expr.(*FuncCall)
	if !ok {
		return nil
	}
	spec, ok := multiColumnFuncs[fc.Name]
	if !ok {
		return nil
	}
	var written []string
	if n := strings.TrimSpace(name); n != "" {
		if strings.HasPrefix(n, "(") && strings.HasSuffix(n, ")") {
			for _, w := range strings.Split(n[1:len(n)-1], ",") {
				written = append(written, unquoteIdent(w))
			}
		} else {
			written = []string{unquoteIdent(n)}
		}
	}
	arg := ""
	if len(fc.Args) > 0 {
		arg, _ = ExprResultName(fc.Args[0])
	}
	dyn := &FuncCall{Name: spec.bag, RawName: spec.bag, Args: fc.Args}
	out := make([]Assignment, len(spec.keys))
	for i, k := range spec.keys {
		n := fc.Name + "_" + arg + "_" + k.key
		if i < len(written) && written[i] != "" {
			n = written[i]
		}
		out[i] = Assignment{
			Name: n,
			Expr: &FuncCall{Name: k.conv, RawName: k.conv, Args: []Expr{
				&AccessExpr{Object: dyn, Path: []AccessKey{{Name: k.key, Index: -1}}},
			}},
		}
	}
	return out
}
