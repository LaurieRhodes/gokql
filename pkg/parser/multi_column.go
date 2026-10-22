package parser

// multi_column.go — functions that return several columns:
//
//	extend (Name1, Name2, ...) = series_stats(x [, ignore_nonfinite])
//	project series_stats(x)
//
// series_stats is expanded into one assignment per statistic, each
// reading its key from series_stats_dynamic: min, min_idx, max,
// max_idx, avg, stdev, variance. Written names apply in order; the
// rest are series_stats_<x>_<stat>, as Kusto.Language names them
// (it keeps every column even when only some are named).

import "strings"

var seriesStatsKeys = []struct{ key, conv string }{
	{"min", "todouble"}, {"min_idx", "tolong"}, {"max", "todouble"}, {"max_idx", "tolong"},
	{"avg", "todouble"}, {"stdev", "todouble"}, {"variance", "todouble"},
}

// expandMultiColumn returns the assignments a multi-column call
// stands for, or nil when expr is not one. name is the written name
// ("" when none; "(a, b)" for a tuple).
func expandMultiColumn(name string, expr Expr) []Assignment {
	fc, ok := expr.(*FuncCall)
	if !ok || fc.Name != "series_stats" {
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
	dyn := &FuncCall{Name: "series_stats_dynamic", RawName: "series_stats_dynamic", Args: fc.Args}
	out := make([]Assignment, len(seriesStatsKeys))
	for i, k := range seriesStatsKeys {
		n := "series_stats_" + arg + "_" + k.key
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
