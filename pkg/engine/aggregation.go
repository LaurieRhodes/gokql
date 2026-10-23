package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// findArgBestRow is the shared arg_max/arg_min row-selection logic —
// used both by the explicit-column form (computeAgg's arg_max/arg_min
// cases) and the star-expansion form (applySummarize, operators.go).
// wantMax selects arg_max semantics (true) or arg_min (false).
//
// Null handling verified against a real, documented Microsoft example
// before fixing what was here: a group where EVERY row's exprToMax
// evaluates to null still produces a result row (the first row
// encountered, arbitrarily), not no row at all. The earlier version
// of this logic returned no best row whenever every candidate was
// null, which silently dropped groups from arg_max(Version, *)-style
// aggregations entirely instead of matching real ADX's own documented
// output for exactly this case (a "Banana" group with only null
// Version values still appears in the result, with Version null and
// the OTHER star-expanded columns taken from the first Banana row).
func findArgBestRow(eng *Engine, maxExpr parser.Expr, rows []types.Row, schema *types.Schema, wantMax bool) (types.Row, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	var bestRow types.Row
	var bestVal types.Value
	var bestType types.KQLType
	haveNonNullCandidate := false

	for _, row := range rows {
		val, err := evalExpr(eng, maxExpr, schema, row)
		if err != nil {
			return nil, err
		}
		if val == nil {
			continue
		}
		if !haveNonNullCandidate {
			bestVal = val
			bestRow = row
			bestType = inferValType(val)
			haveNonNullCandidate = true
			continue
		}
		cmp := types.CompareValues(val, bestVal, bestType)
		if (wantMax && cmp > 0) || (!wantMax && cmp < 0) {
			bestVal = val
			bestRow = row
		}
	}

	if haveNonNullCandidate {
		return bestRow, nil
	}
	// Every candidate was null (or errored quietly) — fall back to the
	// first row in the group, matching real ADX's verified behavior.
	return rows[0], nil
}

func computeAgg(eng *Engine, agg parser.Aggregation, rows []types.Row, schema *types.Schema) (types.Value, error) {
	if v, ok, err := computeSketchAgg(eng, agg, rows, schema); ok {
		return v, err
	}
	switch agg.Function {
	case "count":
		return int64(len(rows)), nil

	case "countif":
		if len(agg.Args) != 1 {
			return nil, fmt.Errorf("countif requires 1 argument")
		}
		count := int64(0)
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil {
				continue
			}
			if b, ok := val.(bool); ok && b {
				count++
			}
		}
		return count, nil

	case "sum":
		if len(agg.Args) != 1 {
			return nil, fmt.Errorf("sum requires 1 argument")
		}
		if inferExprType(eng, agg.Args[0], schema) == types.TypeDecimal {
			return decimalSumAvg(eng, rows, agg.Args[0], nil, schema, false)
		}
		integral := sumIsIntegral(eng, agg.Args[0], schema)
		sum := float64(0)
		var isum int64
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			if integral {
				if n, ok := toInt64(val); ok {
					isum += n
				}
				continue
			}
			sum += types.ToFloat64(val)
		}
		if integral {
			return isum, nil
		}
		return sum, nil

	case "avg":
		if len(agg.Args) != 1 {
			return nil, fmt.Errorf("avg requires 1 argument")
		}
		if inferExprType(eng, agg.Args[0], schema) == types.TypeDecimal {
			return decimalSumAvg(eng, rows, agg.Args[0], nil, schema, true)
		}
		sum := float64(0)
		count := 0
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			sum += types.ToFloat64(val)
			count++
		}
		if count == 0 {
			return nil, nil
		}
		return sum / float64(count), nil

	case "min":
		if len(agg.Args) != 1 {
			return nil, fmt.Errorf("min requires 1 argument")
		}
		var minVal types.Value
		var valType types.KQLType
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			if minVal == nil {
				minVal = val
				valType = inferValType(val)
			} else if types.CompareValues(val, minVal, valType) < 0 {
				minVal = val
			}
		}
		return minVal, nil

	case "max":
		if len(agg.Args) != 1 {
			return nil, fmt.Errorf("max requires 1 argument")
		}
		var maxVal types.Value
		var valType types.KQLType
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			if maxVal == nil {
				maxVal = val
				valType = inferValType(val)
			} else if types.CompareValues(val, maxVal, valType) > 0 {
				maxVal = val
			}
		}
		return maxVal, nil

	case "minif":
		// minif(expr, predicate)
		if len(agg.Args) != 2 {
			return nil, fmt.Errorf("minif requires 2 arguments")
		}
		var minVal types.Value
		var valType types.KQLType
		for _, row := range rows {
			predVal, err := evalExpr(eng, agg.Args[1], schema, row)
			if err != nil {
				continue
			}
			if pred, ok := predVal.(bool); !ok || !pred {
				continue
			}
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			if minVal == nil {
				minVal = val
				valType = inferValType(val)
			} else if types.CompareValues(val, minVal, valType) < 0 {
				minVal = val
			}
		}
		return minVal, nil

	case "maxif":
		// maxif(expr, predicate)
		if len(agg.Args) != 2 {
			return nil, fmt.Errorf("maxif requires 2 arguments")
		}
		var maxVal types.Value
		var valType types.KQLType
		for _, row := range rows {
			predVal, err := evalExpr(eng, agg.Args[1], schema, row)
			if err != nil {
				continue
			}
			if pred, ok := predVal.(bool); !ok || !pred {
				continue
			}
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			if maxVal == nil {
				maxVal = val
				valType = inferValType(val)
			} else if types.CompareValues(val, maxVal, valType) > 0 {
				maxVal = val
			}
		}
		return maxVal, nil

	case "dcount", "count_distinct":
		// dcount(expr) / count_distinct(expr) — verified against real
		// ADX's own docs before adding count_distinct here: the two
		// are documented as genuinely DIFFERENT functions in real ADX
		// (dcount is an APPROXIMATE, HyperLogLog-based distinct count;
		// count_distinct is an EXACT one) -- but this engine's own
		// dcount was already implemented as an exact set count (no
		// real HyperLogLog sketch at all), so the two are simply
		// aliased to identical logic here rather than needing a
		// second, separately-implemented "truly exact" version.
		if len(agg.Args) != 1 {
			return nil, fmt.Errorf("%s requires 1 argument", agg.Function)
		}
		seen := make(map[string]bool)
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			seen[fmtV(val)] = true
		}
		return int64(len(seen)), nil

	case "make_set":
		// make_set(expr) — returns JSON array of distinct values.
		// Native-typed elements (not stringified) since 2026-08-17 —
		// found and fixed live: the previous version collected every
		// value via fmt.Sprintf("%v", val), so make_set(LongColumn)
		// produced a JSON array of QUOTED STRINGS ("1","2","3"), not
		// numbers ([1,2,3]) — silently wrong for any downstream
		// consumer expecting real JSON types (confirmed live: this
		// directly broke series_pearson_correlation fed via
		// summarize make_list(...), real ADX's own documented calling
		// pattern for that function — see func_series.go). The
		// string-keyed dedup map is kept as-is for the EQUALITY check
		// (a reasonable, type-agnostic way to detect "same value"),
		// but the value actually stored and emitted is now the
		// original typed value, run through valueForJSONArray
		// (make_series.go) for datetime/long/real-correct JSON
		// encoding — the same helper make-series's own axis/aggregate
		// arrays already use, reused rather than duplicated.
		// Sorting switched from sort.Strings (silently wrong for
		// numeric sets: "10" < "2" lexicographically) to first-
		// encountered order — real ADX documents make_set as an
		// unordered SET with no guaranteed output order at all, so
		// sort.Strings was never a real correctness requirement, just
		// an incidental default that happened to also be wrong.
		if len(agg.Args) < 1 || len(agg.Args) > 2 {
			return nil, fmt.Errorf("make_set requires 1-2 arguments (expr [, maxSize])")
		}
		maxSize, err := aggMaxSize(eng, agg, 1, schema)
		if err != nil {
			return nil, err
		}
		argType := inferExprType(eng, agg.Args[0], schema)
		seen := make(map[string]bool)
		var values []interface{}
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			for _, el := range aggElems(val, argType) {
				key := elemKey(el)
				if !seen[key] && len(values) < maxSize {
					seen[key] = true
					values = append(values, el)
				}
			}
			if len(values) >= maxSize {
				break
			}
		}
		b, _ := json.Marshal(values)
		return string(b), nil

	case "make_list":
		// make_list(expr) — returns JSON array of all values (with
		// duplicates). Native-typed elements since 2026-08-17, same
		// fix and same reasoning as make_set immediately above (this
		// was the more commonly hit of the two in practice, since
		// summarize make_list(...) is real ADX's own documented way
		// to feed a column into series_pearson_correlation and other
		// series_* functions).
		if len(agg.Args) < 1 || len(agg.Args) > 2 {
			return nil, fmt.Errorf("make_list requires 1-2 arguments (expr [, maxSize])")
		}
		maxSize, err := aggMaxSize(eng, agg, 1, schema)
		if err != nil {
			return nil, err
		}
		argType := inferExprType(eng, agg.Args[0], schema)
		var values []interface{}
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			for _, el := range aggElems(val, argType) {
				if len(values) < maxSize {
					values = append(values, el)
				}
			}
			if len(values) >= maxSize {
				break
			}
		}
		b, _ := json.Marshal(values)
		return string(b), nil

	case "make_list_with_nulls":
		// make_list_with_nulls(expr) — verified against real ADX's own
		// docs before implementing: "returns a list of all the values
		// within the group, INCLUDING null values" — the one, precise
		// difference from make_list immediately above, which silently
		// drops a null row entirely rather than keeping a null slot in
		// the output array. Native-typed elements since 2026-08-17,
		// same fix and reasoning as make_list/make_set above (this
		// previously mirrored make_list's own pre-fix stringified
		// behavior deliberately, for consistency between the two —
		// now mirrors make_list's own FIXED behavior instead, for the
		// same reason).
		if len(agg.Args) < 1 || len(agg.Args) > 2 {
			return nil, fmt.Errorf("make_list_with_nulls requires 1-2 arguments (expr [, maxSize])")
		}
		maxSize, err := aggMaxSize(eng, agg, 1, schema)
		if err != nil {
			return nil, err
		}
		argType := inferExprType(eng, agg.Args[0], schema)
		values := make([]interface{}, 0, len(rows))
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil {
				continue
			}
			if val == nil {
				if len(values) >= maxSize {
					break
				}
				values = append(values, nil)
				continue
			}
			for _, el := range aggElems(val, argType) {
				if len(values) < maxSize {
					values = append(values, el)
				}
			}
			if len(values) >= maxSize {
				break
			}
		}
		b, _ := json.Marshal(values)
		return string(b), nil

	case "make_set_if":
		// make_set_if(expr, predicate) — distinct values where
		// predicate is true. Native-typed elements since 2026-08-17,
		// same fix as make_set above (was missed initially since this
		// conditional variant duplicates make_set's own logic rather
		// than calling it, so the bug had to be fixed here separately
		// too, not automatically inherited).
		if len(agg.Args) < 2 || len(agg.Args) > 3 {
			return nil, fmt.Errorf("make_set_if requires 2-3 arguments (expr, predicate [, maxSize])")
		}
		maxSize, err := aggMaxSize(eng, agg, 2, schema)
		if err != nil {
			return nil, err
		}
		argType := inferExprType(eng, agg.Args[0], schema)
		seen := make(map[string]bool)
		var values []interface{}
		for _, row := range rows {
			predVal, err := evalExpr(eng, agg.Args[1], schema, row)
			if err != nil {
				continue
			}
			if pred, ok := predVal.(bool); !ok || !pred {
				continue
			}
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			for _, el := range aggElems(val, argType) {
				key := elemKey(el)
				if !seen[key] && len(values) < maxSize {
					seen[key] = true
					values = append(values, el)
				}
			}
			if len(values) >= maxSize {
				break
			}
		}
		b, _ := json.Marshal(values)
		return string(b), nil

	case "make_list_if":
		// make_list_if(expr, predicate). Native-typed elements since
		// 2026-08-17, same fix as make_list above, same reason as
		// make_set_if immediately above (duplicated logic, fixed
		// separately here too).
		if len(agg.Args) < 2 || len(agg.Args) > 3 {
			return nil, fmt.Errorf("make_list_if requires 2-3 arguments (expr, predicate [, maxSize])")
		}
		maxSize, err := aggMaxSize(eng, agg, 2, schema)
		if err != nil {
			return nil, err
		}
		argType := inferExprType(eng, agg.Args[0], schema)
		var values []interface{}
		for _, row := range rows {
			predVal, err := evalExpr(eng, agg.Args[1], schema, row)
			if err != nil {
				continue
			}
			if pred, ok := predVal.(bool); !ok || !pred {
				continue
			}
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			for _, el := range aggElems(val, argType) {
				if len(values) < maxSize {
					values = append(values, el)
				}
			}
			if len(values) >= maxSize {
				break
			}
		}
		b, _ := json.Marshal(values)
		return string(b), nil

	case "make_bag":
		// make_bag(dynamic_expr) — merges dynamic objects in the group into one bag.
		// Later keys override earlier ones.
		if len(agg.Args) < 1 || len(agg.Args) > 2 {
			return nil, fmt.Errorf("make_bag requires 1-2 arguments")
		}
		maxSize, err := aggMaxSize(eng, agg, 1, schema)
		if err != nil {
			return nil, err
		}
		merged := make(map[string]interface{})
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			mergeDynamicIntoBagLimited(merged, val, maxSize)
		}
		b, _ := json.Marshal(merged)
		return string(b), nil

	case "make_bag_if":
		// make_bag_if(dynamic_expr, predicate)
		if len(agg.Args) < 2 || len(agg.Args) > 3 {
			return nil, fmt.Errorf("make_bag_if requires 2-3 arguments (expr, predicate [, maxSize])")
		}
		maxSize, err := aggMaxSize(eng, agg, 2, schema)
		if err != nil {
			return nil, err
		}
		merged := make(map[string]interface{})
		for _, row := range rows {
			predVal, err := evalExpr(eng, agg.Args[1], schema, row)
			if err != nil {
				continue
			}
			if pred, ok := predVal.(bool); !ok || !pred {
				continue
			}
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			mergeDynamicIntoBagLimited(merged, val, maxSize)
		}
		b, _ := json.Marshal(merged)
		return string(b), nil

	case "arg_max":
		// arg_max(expr, ExprToReturn | *) — returns the row where expr
		// is maximized. The star form (ExprToReturn == *) is handled
		// separately, at the summarize level (applySummarize), since it
		// expands to multiple output columns — this single-value path
		// only ever runs for the explicit-column form.
		if len(agg.Args) < 2 {
			return nil, fmt.Errorf("arg_max requires at least 2 arguments")
		}
		bestRow, err := findArgBestRow(eng, agg.Args[0], rows, schema, true)
		if err != nil {
			return nil, err
		}
		if bestRow == nil {
			return nil, nil
		}
		return evalExpr(eng, agg.Args[1], schema, bestRow)

	case "arg_min":
		// arg_min(expr, ExprToReturn | *) — returns the row where expr
		// is minimized. See arg_max immediately above for the star-form
		// note; identical reasoning applies here.
		if len(agg.Args) < 2 {
			return nil, fmt.Errorf("arg_min requires at least 2 arguments")
		}
		bestRow, err := findArgBestRow(eng, agg.Args[0], rows, schema, false)
		if err != nil {
			return nil, err
		}
		if bestRow == nil {
			return nil, nil
		}
		return evalExpr(eng, agg.Args[1], schema, bestRow)

	case "any", "take_any":
		// any(expr) / take_any(expr) — returns an arbitrary value from
		// the group. Verified against real ADX's own docs before
		// adding take_any as a second name here: any() is real ADX's
		// own documented DEPRECATED alias for take_any() (not a
		// separate function this engine invented) — found live, not
		// hypothetical: this engine had only ever registered the
		// deprecated name, never the modern, recommended one, until a
		// review of the full aggregation-functions reference caught it.
		if len(agg.Args) != 1 {
			return nil, fmt.Errorf("%s requires 1 argument", agg.Function)
		}
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			return val, nil
		}
		return nil, nil

	case "anyif", "take_anyif":
		// anyif(expr, predicate) / take_anyif(expr, predicate) —
		// anyif is real ADX's own documented DEPRECATED alias for
		// take_anyif, both registered here for the same reason as
		// any/take_any immediately above. Arbitrarily selects one row
		// for which predicate evaluates to true, returning expr's
		// value for that row; null if no row satisfies the predicate —
		// mirrors "any"/"take_any" above exactly, with an added
		// per-row predicate check before accepting a candidate row.
		if len(agg.Args) != 2 {
			return nil, fmt.Errorf("%s requires 2 arguments (expr, predicate)", agg.Function)
		}
		for _, row := range rows {
			predVal, err := evalExpr(eng, agg.Args[1], schema, row)
			if err != nil {
				continue
			}
			ok, _ := predVal.(bool)
			if !ok {
				continue
			}
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			return val, nil
		}
		return nil, nil

	case "percentile", "percentiles", "percentilew", "percentilesw":
		// One percentile; percentiles(x, p1, p2, ...) with several is
		// expanded to one column each by applySummarize.
		want := 2
		if agg.Function == "percentilew" || agg.Function == "percentilesw" {
			want = 3
		}
		if len(agg.Args) != want {
			return nil, fmt.Errorf("%s: expected %d arguments, got %d", agg.Function, want, len(agg.Args))
		}
		pctVal, err := evalExpr(eng, agg.Args[want-1], schema, types.Row{})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", agg.Function, err)
		}
		vals, err := computePercentileValues(eng, agg, []float64{types.ToFloat64(pctVal)}, rows, schema)
		if err != nil {
			return nil, err
		}
		return vals[0], nil

	case "percentiles_array", "percentilesw_array":
		// percentiles_array(expr, p1, p2, ..., pN) OR
		// percentiles_array(expr, dynamic([p1, p2, ..., pN])) — verified
		// against real ADX's own docs before implementing: unlike
		// percentiles() (which expands to N separate output columns,
		// already implemented in operators.go's applySummarize),
		// percentiles_array() returns a SINGLE column holding a
		// dynamic array of the requested percentile values — real
		// ADX's own documented column-naming distinction between the
		// two ("percentile_expr_pN" for percentiles(), singular
		// "percentile_" despite the plural function name, vs
		// "percentiles_expr" for percentiles_array(), one column) is
		// already handled correctly by the shared, generic
		// FunctionName_FirstArgText auto-naming convention this engine
		// already applies to every unnamed aggregation -- no special
		// case needed here beyond computing the right VALUE.
		//
		// Real ADX's own docs explicitly confirm both argument forms
		// are valid ("Percentiles for percentiles_array can be
		// specified in a dynamic array of integer or floating-point
		// numbers... doesn't have to be literal") -- both are
		// supported here: if there are exactly 2 arguments and the
		// second evaluates to something parseJSONArray can read, it's
		// treated as the dynamic-array form; otherwise every argument
		// after the first is treated as its own, individual percentile
		// value (the comma-separated form, identical in shape to
		// percentiles()' own existing argument parsing).
		if len(agg.Args) < 2 {
			return nil, fmt.Errorf("percentiles_array requires at least 2 arguments")
		}
		var pcts []float64
		first := 1
		if agg.Function == "percentilesw_array" {
			first = 2
		}
		if len(agg.Args) == first+1 {
			arrVal, err := evalExpr(eng, agg.Args[first], schema, types.Row{})
			if err == nil {
				if arr, ok := parseJSONArray(arrVal); ok {
					for _, item := range arr {
						pcts = append(pcts, types.ToFloat64(jsonToKQLValue(item)))
					}
				}
			}
		}
		if pcts == nil {
			for _, pArg := range agg.Args[first:] {
				pVal, err := evalExpr(eng, pArg, schema, types.Row{})
				if err != nil {
					return nil, fmt.Errorf("percentiles_array: %w", err)
				}
				pcts = append(pcts, types.ToFloat64(pVal))
			}
		}
		results, err := computePercentileValues(eng, agg, pcts, rows, schema)
		if err != nil {
			return nil, err
		}
		for i, v := range results {
			if v == nil {
				results[i] = nil
			}
		}
		b, _ := json.Marshal(results)
		return string(b), nil

	case "stdev":
		// stdev(expr) — sample standard deviation
		if len(agg.Args) != 1 {
			return nil, fmt.Errorf("stdev requires 1 argument")
		}
		var values []float64
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			values = append(values, types.ToFloat64(val))
		}
		if len(values) < 2 {
			return nil, nil
		}
		mean := 0.0
		for _, v := range values {
			mean += v
		}
		mean /= float64(len(values))
		variance := 0.0
		for _, v := range values {
			d := v - mean
			variance += d * d
		}
		variance /= float64(len(values) - 1) // sample variance
		return math.Sqrt(variance), nil

	case "variance":
		// variance(expr) — sample variance
		if len(agg.Args) != 1 {
			return nil, fmt.Errorf("variance requires 1 argument")
		}
		var values []float64
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			values = append(values, types.ToFloat64(val))
		}
		if len(values) < 2 {
			return nil, nil
		}
		mean := 0.0
		for _, v := range values {
			mean += v
		}
		mean /= float64(len(values))
		variance := 0.0
		for _, v := range values {
			d := v - mean
			variance += d * d
		}
		return variance / float64(len(values)-1), nil

	case "stdevif":
		// stdevif(expr, predicate) — conditional sample standard deviation
		if len(agg.Args) != 2 {
			return nil, fmt.Errorf("stdevif requires 2 arguments")
		}
		var values []float64
		for _, row := range rows {
			pred, err := evalExpr(eng, agg.Args[1], schema, row)
			if err != nil {
				continue
			}
			if b, ok := pred.(bool); !ok || !b {
				continue
			}
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			values = append(values, types.ToFloat64(val))
		}
		if len(values) < 2 {
			return nil, nil
		}
		mean := 0.0
		for _, v := range values {
			mean += v
		}
		mean /= float64(len(values))
		variance := 0.0
		for _, v := range values {
			d := v - mean
			variance += d * d
		}
		return math.Sqrt(variance / float64(len(values)-1)), nil

	case "stdevp":
		// stdevp(expr) — population standard deviation
		if len(agg.Args) != 1 {
			return nil, fmt.Errorf("stdevp requires 1 argument")
		}
		var values []float64
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			values = append(values, types.ToFloat64(val))
		}
		if len(values) == 0 {
			return nil, nil
		}
		mean := 0.0
		for _, v := range values {
			mean += v
		}
		mean /= float64(len(values))
		variance := 0.0
		for _, v := range values {
			d := v - mean
			variance += d * d
		}
		return math.Sqrt(variance / float64(len(values))), nil

	case "variancep":
		// variancep(expr) — population variance
		if len(agg.Args) != 1 {
			return nil, fmt.Errorf("variancep requires 1 argument")
		}
		var values []float64
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			values = append(values, types.ToFloat64(val))
		}
		if len(values) == 0 {
			return nil, nil
		}
		mean := 0.0
		for _, v := range values {
			mean += v
		}
		mean /= float64(len(values))
		variance := 0.0
		for _, v := range values {
			d := v - mean
			variance += d * d
		}
		return variance / float64(len(values)), nil

	case "varianceif":
		// varianceif(expr, predicate) — conditional sample variance,
		// verified against real ADX's own aggregation-functions
		// reference before implementing: mirrors variance's own logic
		// exactly (sample variance, n-1 divisor, requires at least 2
		// qualifying values), with the same predicate-filtering style
		// stdevif/sumif/avgif already use elsewhere in this file.
		if len(agg.Args) != 2 {
			return nil, fmt.Errorf("varianceif requires 2 arguments")
		}
		var values []float64
		for _, row := range rows {
			predVal, err := evalExpr(eng, agg.Args[1], schema, row)
			if err != nil {
				continue
			}
			if pred, ok := predVal.(bool); !ok || !pred {
				continue
			}
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			values = append(values, types.ToFloat64(val))
		}
		if len(values) < 2 {
			return nil, nil
		}
		mean := 0.0
		for _, v := range values {
			mean += v
		}
		mean /= float64(len(values))
		variance := 0.0
		for _, v := range values {
			d := v - mean
			variance += d * d
		}
		return variance / float64(len(values)-1), nil

	case "variancepif":
		// variancepif(expr, predicate) — conditional population
		// variance, mirrors variancep's own logic exactly (n divisor,
		// requires at least 1 qualifying value).
		if len(agg.Args) != 2 {
			return nil, fmt.Errorf("variancepif requires 2 arguments")
		}
		var values []float64
		for _, row := range rows {
			predVal, err := evalExpr(eng, agg.Args[1], schema, row)
			if err != nil {
				continue
			}
			if pred, ok := predVal.(bool); !ok || !pred {
				continue
			}
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			values = append(values, types.ToFloat64(val))
		}
		if len(values) == 0 {
			return nil, nil
		}
		mean := 0.0
		for _, v := range values {
			mean += v
		}
		mean /= float64(len(values))
		variance := 0.0
		for _, v := range values {
			d := v - mean
			variance += d * d
		}
		return variance / float64(len(values)), nil

	case "covariance":
		// covariance(expr1, expr2) — sample covariance, added
		// 2026-08-17. Verified exactly against real ADX's own worked
		// example: datatable x=[1,2,3,4,5], y=[14,10,17,20,50] ->
		// covariance(x,y) == 20.5 (hand-computed: mean_x=3, mean_y=22.2,
		// sum of (x_i-mean_x)(y_i-mean_y) = 82.0, divided by n-1=4 ->
		// 20.5 — matches the real docs output exactly). Real ADX's own
		// docs state "Null values are ignored and don't factor into
		// the calculation" — since this is a PAIRED statistic, a row
		// is only included if BOTH expr1 and expr2 are non-null for
		// that row (pairwise exclusion), the same convention already
		// verified for series_pearson_correlation elsewhere in this
		// codebase.
		if len(agg.Args) != 2 {
			return nil, fmt.Errorf("covariance requires 2 arguments")
		}
		xs, ys := collectPairedFloats(eng, agg.Args[0], agg.Args[1], schema, rows)
		if len(xs) < 2 {
			return nil, nil
		}
		return sampleCovariance(xs, ys), nil

	case "covariancep":
		// covariancep(expr1, expr2) — population covariance, added
		// 2026-08-17. Same pairwise-null-exclusion rule as covariance
		// above; divisor is n (population), not n-1 (sample) — the
		// same n-vs-n-1 relationship already correctly implemented for
		// variance/variancep. No dedicated real-ADX worked example
		// with numeric output was found for covariancep specifically
		// (only covariance's own worked example, and the general
		// population-vs-sample distinction documented identically
		// across the whole variance/covariance function family) —
		// flagged here rather than silently claimed as equally
		// verified.
		if len(agg.Args) != 2 {
			return nil, fmt.Errorf("covariancep requires 2 arguments")
		}
		xs, ys := collectPairedFloats(eng, agg.Args[0], agg.Args[1], schema, rows)
		if len(xs) == 0 {
			return nil, nil
		}
		return populationCovariance(xs, ys), nil

	case "covarianceif":
		// covarianceif(expr1, expr2, predicate) — conditional sample
		// covariance, added 2026-08-17. Mirrors covariance's own logic
		// exactly (sample covariance, n-1 divisor, pairwise null
		// exclusion), with the predicate applied as an additional
		// per-row filter before the pairwise-null check — the same
		// predicate-filtering style varianceif/stdevif/sumif already
		// use elsewhere in this file.
		if len(agg.Args) != 3 {
			return nil, fmt.Errorf("covarianceif requires 3 arguments")
		}
		xs, ys := collectPairedFloatsIf(eng, agg.Args[0], agg.Args[1], agg.Args[2], schema, rows)
		if len(xs) < 2 {
			return nil, nil
		}
		return sampleCovariance(xs, ys), nil

	case "covariancepif":
		// covariancepif(expr1, expr2, predicate) — conditional
		// population covariance, added 2026-08-17. Mirrors
		// variancepif's own logic exactly (n divisor, requires at
		// least 1 qualifying pair).
		if len(agg.Args) != 3 {
			return nil, fmt.Errorf("covariancepif requires 3 arguments")
		}
		xs, ys := collectPairedFloatsIf(eng, agg.Args[0], agg.Args[1], agg.Args[2], schema, rows)
		if len(xs) == 0 {
			return nil, nil
		}
		return populationCovariance(xs, ys), nil

	case "sumif":
		// sumif(expr, predicate)
		if len(agg.Args) != 2 {
			return nil, fmt.Errorf("sumif requires 2 arguments")
		}
		if inferExprType(eng, agg.Args[0], schema) == types.TypeDecimal {
			return decimalSumAvg(eng, rows, agg.Args[0], agg.Args[1], schema, false)
		}
		integral := sumIsIntegral(eng, agg.Args[0], schema)
		sum := float64(0)
		var isum int64
		for _, row := range rows {
			predVal, err := evalExpr(eng, agg.Args[1], schema, row)
			if err != nil {
				continue
			}
			if pred, ok := predVal.(bool); !ok || !pred {
				continue
			}
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			if integral {
				if n, ok := toInt64(val); ok {
					isum += n
				}
				continue
			}
			sum += types.ToFloat64(val)
		}
		if integral {
			return isum, nil
		}
		return sum, nil

	case "avgif":
		// avgif(expr, predicate)
		if len(agg.Args) != 2 {
			return nil, fmt.Errorf("avgif requires 2 arguments")
		}
		if inferExprType(eng, agg.Args[0], schema) == types.TypeDecimal {
			return decimalSumAvg(eng, rows, agg.Args[0], agg.Args[1], schema, true)
		}
		sum := float64(0)
		count := 0
		for _, row := range rows {
			predVal, err := evalExpr(eng, agg.Args[1], schema, row)
			if err != nil {
				continue
			}
			if pred, ok := predVal.(bool); !ok || !pred {
				continue
			}
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			sum += types.ToFloat64(val)
			count++
		}
		if count == 0 {
			return nil, nil
		}
		return sum / float64(count), nil

	case "dcountif", "count_distinctif":
		// dcountif(expr, predicate) / count_distinctif(expr, predicate)
		// — same aliasing reasoning as dcount/count_distinct above.
		if len(agg.Args) != 2 {
			return nil, fmt.Errorf("%s requires 2 arguments", agg.Function)
		}
		seen := make(map[string]bool)
		for _, row := range rows {
			predVal, err := evalExpr(eng, agg.Args[1], schema, row)
			if err != nil {
				continue
			}
			if pred, ok := predVal.(bool); !ok || !pred {
				continue
			}
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			seen[fmtV(val)] = true
		}
		return int64(len(seen)), nil

	case "binary_all_or":
		// binary_all_or(expr) — bitwise OR of all values
		if len(agg.Args) != 1 {
			return nil, fmt.Errorf("binary_all_or requires 1 argument")
		}
		result := int64(0)
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			result |= types.ToInt64(val)
		}
		return result, nil

	case "binary_all_and":
		// binary_all_and(expr) — bitwise AND of all values
		if len(agg.Args) != 1 {
			return nil, fmt.Errorf("binary_all_and requires 1 argument")
		}
		result := int64(-1) // all bits set
		first := true
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			if first {
				result = types.ToInt64(val)
				first = false
			} else {
				result &= types.ToInt64(val)
			}
		}
		if first {
			return int64(0), nil
		}
		return result, nil

	case "binary_all_xor":
		// binary_all_xor(expr) — bitwise XOR of all values
		if len(agg.Args) != 1 {
			return nil, fmt.Errorf("binary_all_xor requires 1 argument")
		}
		result := int64(0)
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[0], schema, row)
			if err != nil || val == nil {
				continue
			}
			result ^= types.ToInt64(val)
		}
		return result, nil

	case "strcat_delim":
		// strcat_delim(delimiter, expr1, expr2, ...) — but as aggregation:
		// We support the common pattern: summarize strcat_delim(delimiter, expr)
		// which concatenates all values in the group with the delimiter.
		// Note: In real KQL this is a scalar function, but analysts often want
		// string concatenation in summarize, so we support it here too.
		if len(agg.Args) != 2 {
			return nil, fmt.Errorf("strcat_delim as aggregation requires 2 arguments (delimiter, expr)")
		}
		delimVal, err := evalExpr(eng, agg.Args[0], schema, types.Row{})
		if err != nil {
			return nil, err
		}
		delim := fmt.Sprintf("%v", delimVal)
		var parts []string
		for _, row := range rows {
			val, err := evalExpr(eng, agg.Args[1], schema, row)
			if err != nil || val == nil {
				continue
			}
			parts = append(parts, fmt.Sprintf("%v", val))
		}
		return strings.Join(parts, delim), nil

	default:
		return nil, fmt.Errorf("unsupported aggregation function: %s", agg.Function)
	}
}

// inferValType guesses a KQL type from a Go runtime value.
func inferValType(v types.Value) types.KQLType {
	switch v.(type) {
	case int64:
		return types.TypeLong
	case int32:
		return types.TypeInt
	case int:
		return types.TypeLong
	case float64:
		return types.TypeReal
	case bool:
		return types.TypeBool
	case string:
		return types.TypeString
	default:
		return types.TypeString
	}
}

// sumIsIntegral reports whether sum/sumif over expr accumulates exactly
// in int64: int, long and timespan (100 ns ticks) arguments.
func sumIsIntegral(eng *Engine, expr parser.Expr, schema *types.Schema) bool {
	switch inferExprType(eng, expr, schema) {
	case types.TypeInt, types.TypeLong, types.TypeTimespan:
		return true
	}
	return false
}

func inferAggType(eng *Engine, funcName string, args []parser.Expr, schema *types.Schema) types.KQLType {
	switch funcName {
	case "count", "countif", "dcount", "dcountif", "count_distinct", "count_distinctif":
		return types.TypeLong
	case "sum", "sumif":
		// ADX: sum of int or long is long, of timespan is timespan, of
		// decimal is decimal, otherwise real.
		if len(args) > 0 {
			switch t := inferExprType(eng, args[0], schema); t {
			case types.TypeInt, types.TypeLong:
				return types.TypeLong
			case types.TypeTimespan:
				return types.TypeTimespan
			case types.TypeDecimal:
				return types.TypeDecimal
			}
		}
		return types.TypeReal
	case "percentile", "percentiles", "percentilew", "percentilesw":
		return percentileResultType(eng, args, schema)
	case "hll", "hll_if", "hll_merge", "tdigest", "tdigest_merge", "merge_tdigest", "buildschema":
		return types.TypeDynamic
	case "avg", "avgif":
		if len(args) > 0 && inferExprType(eng, args[0], schema) == types.TypeDecimal {
			return types.TypeDecimal
		}
		return types.TypeReal
	case "stdev", "stdevif", "stdevp", "variance", "variancep", "varianceif", "variancepif", "covariance", "covariancep", "covarianceif", "covariancepif":
		return types.TypeReal
	case "min", "max", "minif", "maxif":
		if len(args) > 0 {
			if ref, ok := args[0].(*parser.ColumnRef); ok {
				if col := schema.ColumnByName(ref.Name); col != nil {
					return col.Type
				}
			}
		}
		return types.TypeDynamic
	case "arg_max", "arg_min", "any", "take_any":
		// arg_max/arg_min: the second argument's type; any/take_any of
		// one expression: that expression's.
		if len(args) > 1 {
			return inferExprType(eng, args[1], schema)
		}
		if len(args) == 1 && (funcName == "any" || funcName == "take_any") {
			if _, star := args[0].(*parser.StarExpr); !star {
				return inferExprType(eng, args[0], schema)
			}
		}
		return types.TypeDynamic

	case "anyif", "take_anyif":
		// Return type depends on the FIRST argument (expr) — unlike
		// arg_max/arg_min/any immediately above, anyif's own second
		// argument is the predicate (a bool), never the value being
		// selected, so inferring from args[1] here would be wrong.
		if len(args) > 0 {
			return inferExprType(eng, args[0], schema)
		}
		return types.TypeDynamic
	case "make_set", "make_list", "make_set_if", "make_list_if", "make_list_with_nulls", "make_bag", "make_bag_if":
		return types.TypeDynamic
	case "binary_all_or", "binary_all_and", "binary_all_xor":
		return types.TypeLong
	case "strcat_delim":
		return types.TypeString
	default:
		return types.TypeDynamic
	}
}

// mergeDynamicIntoBag merges a dynamic value into a bag (map).
// The value can be a JSON string representing an object, or a map[string]interface{}.
// maxAggElements is the default and maximum maxSize of make_list,
// make_set and make_bag (ADX reference: "The default and max value is
// 1048576").
const maxAggElements = 1048576

// aggMaxSize reads the optional constant maxSize argument at position
// idx, defaulting to and capped at maxAggElements.
func aggMaxSize(eng *Engine, agg parser.Aggregation, idx int, schema *types.Schema) (int, error) {
	if len(agg.Args) <= idx {
		return maxAggElements, nil
	}
	v, err := evalExpr(eng, agg.Args[idx], schema, make(types.Row, len(schema.Columns)))
	if err != nil {
		return 0, fmt.Errorf("%s: maxSize: %w", agg.Function, err)
	}
	n, ok := toInt64(v)
	if !ok || n <= 0 {
		return 0, fmt.Errorf("%s: maxSize must be a positive integer constant, got %v", agg.Function, v)
	}
	if n > maxAggElements {
		n = maxAggElements
	}
	return int(n), nil
}

// mergeDynamicIntoBagLimited is mergeDynamicIntoBag that adds no new
// key once the bag holds max keys (existing keys can still update).
// Keys of one input object are taken in sorted order so the cut is
// deterministic.
func mergeDynamicIntoBagLimited(bag map[string]interface{}, val interface{}, max int) {
	tmp := map[string]interface{}{}
	mergeDynamicIntoBag(tmp, val)
	keys := make([]string, 0, len(tmp))
	for k := range tmp {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, exists := bag[k]; !exists && len(bag) >= max {
			continue
		}
		bag[k] = tmp[k]
	}
}

func mergeDynamicIntoBag(bag map[string]interface{}, val interface{}) {
	switch v := val.(type) {
	case map[string]interface{}:
		for k, vv := range v {
			bag[k] = vv
		}
	case string:
		// Try parsing as JSON object
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(v), &obj); err == nil {
			for k, vv := range obj {
				bag[k] = vv
			}
		}
	}
}

// computePercentileValues computes nearest-rank percentiles of
// agg.Args[0] (weighted by agg.Args[1] for the ...w forms): the P-th
// percentile is the smallest value with at least P% of the population
// (by weight) less than or equal to it; P = 0 is the minimum. This is
// ADX's documented definition; ADX estimates it with T-Digest, which
// is exact for small populations. Results keep the value's type (int
// widens to long); an empty population gives nulls.
func computePercentileValues(eng *Engine, agg parser.Aggregation, pcts []float64, rows []types.Row, schema *types.Schema) ([]types.Value, error) {
	if len(agg.Args) < 2 {
		return nil, fmt.Errorf("%s requires at least 2 arguments", agg.Function)
	}
	weighted := strings.HasPrefix(agg.Function, "percentilew") || strings.HasPrefix(agg.Function, "percentilesw")
	type item struct {
		key float64
		val types.Value
		w   int64
	}
	var items []item
	var total int64
	for _, row := range rows {
		val, err := evalExpr(eng, agg.Args[0], schema, row)
		if err != nil || val == nil {
			continue
		}
		w := int64(1)
		if weighted {
			wv, err := evalExpr(eng, agg.Args[1], schema, row)
			if err != nil || wv == nil {
				continue
			}
			w = int64(types.ToFloat64(wv))
			if w <= 0 {
				continue
			}
		}
		if x, ok := val.(int32); ok {
			val = int64(x)
		}
		items = append(items, item{types.ToFloat64(val), val, w})
		total += w
	}
	out := make([]types.Value, len(pcts))
	if len(items) == 0 {
		return out, nil
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].key < items[j].key })
	for i, p := range pcts {
		rank := int64(math.Ceil(p/100*float64(total) - 1e-9)) // 99.9% of 1000 is 999, not float-noise 1000
		if rank < 1 {
			rank = 1
		}
		var cum int64
		out[i] = items[len(items)-1].val
		for _, it := range items {
			cum += it.w
			if cum >= rank {
				out[i] = it.val
				break
			}
		}
	}
	return out, nil
}

// percentileResultType is the type of a percentile of expr: the
// expression's own type (int widens to long).
func percentileResultType(eng *Engine, args []parser.Expr, schema *types.Schema) types.KQLType {
	if len(args) == 0 {
		return types.TypeReal
	}
	switch t := inferExprType(eng, args[0], schema); t {
	case types.TypeInt, types.TypeLong:
		return types.TypeLong
	case types.TypeDatetime, types.TypeTimespan, types.TypeReal:
		return t
	}
	return types.TypeReal
}

// --- Covariance family helpers (added 2026-08-17) ---

// collectPairedFloats evaluates expr1 and expr2 for every row and
// keeps only the (x,y) pairs where BOTH are non-null — the pairwise
// null-exclusion rule real ADX's own covariance docs describe
// ("null values are ignored") applied correctly for a two-variable
// statistic, where a null on either side makes the whole pair
// unusable, not just the null side.
func collectPairedFloats(eng *Engine, expr1, expr2 parser.Expr, schema *types.Schema, rows []types.Row) ([]float64, []float64) {
	var xs, ys []float64
	for _, row := range rows {
		xVal, err := evalExpr(eng, expr1, schema, row)
		if err != nil || xVal == nil {
			continue
		}
		yVal, err := evalExpr(eng, expr2, schema, row)
		if err != nil || yVal == nil {
			continue
		}
		xs = append(xs, types.ToFloat64(xVal))
		ys = append(ys, types.ToFloat64(yVal))
	}
	return xs, ys
}

// collectPairedFloatsIf is collectPairedFloats with an additional
// per-row predicate filter, applied before the pairwise-null check —
// the shared logic behind covarianceif/covariancepif.
func collectPairedFloatsIf(eng *Engine, expr1, expr2, predicate parser.Expr, schema *types.Schema, rows []types.Row) ([]float64, []float64) {
	var xs, ys []float64
	for _, row := range rows {
		predVal, err := evalExpr(eng, predicate, schema, row)
		if err != nil {
			continue
		}
		if pred, ok := predVal.(bool); !ok || !pred {
			continue
		}
		xVal, err := evalExpr(eng, expr1, schema, row)
		if err != nil || xVal == nil {
			continue
		}
		yVal, err := evalExpr(eng, expr2, schema, row)
		if err != nil || yVal == nil {
			continue
		}
		xs = append(xs, types.ToFloat64(xVal))
		ys = append(ys, types.ToFloat64(yVal))
	}
	return xs, ys
}

// sampleCovariance computes the sample covariance (n-1 divisor) of
// two equal-length paired float slices, verified exactly against real
// ADX's own covariance() worked example (x=[1,2,3,4,5],
// y=[14,10,17,20,50] -> 20.5). Caller must ensure len(xs) >= 2.
func sampleCovariance(xs, ys []float64) float64 {
	return covarianceSum(xs, ys) / float64(len(xs)-1)
}

// populationCovariance computes the population covariance (n divisor)
// of two equal-length paired float slices — the same n-vs-n-1
// relationship already correctly implemented for variance/variancep.
// Caller must ensure len(xs) >= 1.
func populationCovariance(xs, ys []float64) float64 {
	return covarianceSum(xs, ys) / float64(len(xs))
}

// covarianceSum computes sum((x_i - mean_x)(y_i - mean_y)) — the
// shared numerator behind both sampleCovariance and
// populationCovariance, which differ only in their divisor.
func covarianceSum(xs, ys []float64) float64 {
	meanX, meanY := 0.0, 0.0
	for i := range xs {
		meanX += xs[i]
		meanY += ys[i]
	}
	n := float64(len(xs))
	meanX /= n
	meanY /= n
	sum := 0.0
	for i := range xs {
		sum += (xs[i] - meanX) * (ys[i] - meanY)
	}
	return sum
}

// aggElems is what one value contributes to make_list/make_set and
// their variants: a dynamic array its elements (one level, nulls
// inside it kept, an empty array nothing), anything else itself.
func aggElems(val types.Value, argType types.KQLType) []interface{} {
	if argType == types.TypeDynamic {
		if str, ok := val.(string); ok {
			t := strings.TrimSpace(str)
			if strings.HasPrefix(t, "[") {
				var raw []json.RawMessage
				if json.Unmarshal([]byte(t), &raw) == nil {
					out := make([]interface{}, len(raw))
					for i, r := range raw {
						var b bytes.Buffer
						if json.Compact(&b, r) == nil {
							r = b.Bytes()
						}
						out[i] = json.RawMessage(r)
					}
					return out
				}
			}
		}
	}
	return []interface{}{valueForJSONArray(val, argType)}
}

// elemKey identifies an aggElems element for make_set's dedup.
func elemKey(el interface{}) string {
	if r, ok := el.(json.RawMessage); ok {
		return "j" + string(r)
	}
	b, err := json.Marshal(el)
	if err != nil {
		return fmt.Sprintf("v%v", el)
	}
	return "j" + string(b)
}
