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

import (
	"fmt"
	"strings"
)

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
	"series_fft":  {"__series_fft_dynamic", []multiKey{{"real", "todynamic"}, {"imag", "todynamic"}}},
	"series_ifft": {"__series_ifft_dynamic", []multiKey{{"real", "todynamic"}, {"imag", "todynamic"}}},
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
	if fc.Name == "array_sort_asc" || fc.Name == "array_sort_desc" {
		return expandArraySort(name, fc)
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

// expandArraySort: array_sort_asc/desc over several arrays returns one
// column per array, named <arg>_sorted (array<i>_sorted for an
// unnamed argument) unless a tuple names them. A single name keeps
// the call as one column holding the array of arrays.
func expandArraySort(name string, fc *FuncCall) []Assignment {
	arrays := fc.Args
	if n := len(arrays); n > 1 {
		if lit, ok := arrays[n-1].(*Literal); ok {
			if _, isBool := lit.Value.(bool); isBool {
				arrays = arrays[:n-1]
			}
		}
	}
	n := strings.TrimSpace(name)
	if len(arrays) < 2 || (n != "" && !strings.HasPrefix(n, "(")) {
		return nil
	}
	var written []string
	if n != "" {
		for _, w := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(n, "("), ")"), ",") {
			written = append(written, unquoteIdent(w))
		}
	}
	out := make([]Assignment, len(arrays))
	for i, a := range arrays {
		col := fmt.Sprintf("array%d_sorted", i)
		if an, ok := ExprResultName(a); ok && an != "" {
			col = an + "_sorted"
		}
		if i < len(written) && written[i] != "" {
			col = written[i]
		}
		out[i] = Assignment{Name: col, Expr: &AccessExpr{Object: fc, Path: []AccessKey{{Index: i}}}}
	}
	return out
}
