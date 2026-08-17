package engine

// func_series.go — Tier 1 (element-wise math/comparison) and Tier 2
// (gap-filling) series_* functions, per docs/timeseries_backlog.md's
// own suggested build order. Every function's exact semantics
// (argument shapes, null/mismatched-length handling, return element
// type) verified against real ADX's own docs before writing this, not
// assumed from the function name — see each function's own comment
// for the specific worked example or documented rule it matches.
//
// Two real findings from that verification worth stating up front,
// since they contradict what an earlier planning pass (this same
// backlog document) had guessed:
//   - The comparison family (series_equals, series_less, etc.)
//     returns a dynamic array of BOOLEANS, not 1.0/0.0 floats — real
//     ADX's own series_greater_equals docs say so explicitly
//     ("Dynamic array of booleans..."). The backlog document's own
//     note had flagged this as needing verification specifically
//     because it's an easy detail to get backwards; verifying it
//     directly reversed the original guess.
//   - Real ADX documents two DIFFERENT missing-element conventions
//     across this function family, not one: the arithmetic/comparison
//     functions (series_add, series_multiply, series_equals, ...) use
//     "different array lengths -> null for the missing positions,
//     output length = max of the two inputs" (verified against
//     series_multiply's own worked example), while
//     series_cosine_similarity (already implemented, func_vector.go)
//     uses "truncate the longer array to the shorter one" instead — a
//     real, deliberate difference between function families in real
//     ADX itself, not an inconsistency to paper over.

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func evalSeriesFunc(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {

	// --- Tier 1: arithmetic (series1, series2) -> dynamic ---
	case "series_add", "series_subtract", "series_multiply", "series_divide", "series_pow":
		return evalSeriesBinaryNumeric(fc, schema, row)

	// --- Tier 1: comparison (series1, series2) -> dynamic of bool ---
	case "series_equals", "series_not_equals", "series_less", "series_less_equals",
		"series_greater", "series_greater_equals":
		return evalSeriesBinaryBool(fc, schema, row)

	// --- Tier 1: unary math (series) -> dynamic ---
	case "series_abs", "series_sign", "series_ceiling", "series_floor",
		"series_log", "series_exp",
		"series_sin", "series_cos", "series_tan",
		"series_asin", "series_acos", "series_atan":
		return evalSeriesUnary(fc, schema, row)

	// --- Tier 2: gap filling ---
	case "series_fill_forward":
		return evalSeriesFillForward(fc, schema, row)
	case "series_fill_backward":
		return evalSeriesFillBackward(fc, schema, row)
	case "series_fill_const":
		return evalSeriesFillConst(fc, schema, row)
	case "series_fill_linear":
		return evalSeriesFillLinear(fc, schema, row)

	// --- Tier 3: summary statistics ---
	case "series_sum":
		return evalSeriesSum(fc, schema, row)
	case "series_product":
		return evalSeriesProduct(fc, schema, row)
	case "series_magnitude":
		return evalSeriesMagnitude(fc, schema, row)
	case "series_pearson_correlation":
		return evalSeriesPearsonCorrelation(fc, schema, row)
	case "series_stats_dynamic":
		return evalSeriesStatsDynamic(fc, schema, row)

	// series_stats (NOT series_stats_dynamic) is deliberately NOT
	// implemented — see this file's own Tier 3 section comment below
	// for why: its real syntax, `extend (Name, ...) = series_stats(
	// series [, ignore_nonfinite])`, expands to MULTIPLE output
	// columns from one function call, needing destructuring-
	// assignment grammar support (`(a, b, c) = expr`) that doesn't
	// exist anywhere in this engine's project/extend parser — a real,
	// separate, bigger gap than adding one more function, not
	// something to fake by returning a single dynamic value under
	// the same name (which would silently NOT match real ADX's own
	// documented multi-column output shape).
	}
	return nil, false, nil
}

// --- Tier 3: summary statistics over one (or two) arrays ---
//
// series_stats (plural output columns) is out of scope — see the
// comment inline in evalSeriesFunc's own switch above for the precise
// reason (a real grammar gap, not a value judgment about the
// function's usefulness). series_stats_dynamic (this tier's single-
// value sibling, returning the same statistics packed into one
// dynamic property bag) is implemented below and verified exactly
// against real ADX's own worked example.
//
// series_sum and series_pearson_correlation are each verified against
// their own real-ADX worked example ([1,2,3,4] -> 10; a perfectly
// correlated pair -> 1.0). series_product has no fetched worked
// example (none found in the sources checked while researching this)
// but is mathematically unambiguous — the natural analog of
// series_sum with multiplication in place of addition — so it's
// implemented with the same confidence level as series_sum rather
// than flagged as an assumption.
//
// Null/non-numeric ELEMENT handling for the three single-array
// reduction functions (sum/product/magnitude) is an explicit,
// documented assumption, not confirmed from any fetched source: null
// or non-numeric elements are SKIPPED (not counted, not propagating a
// whole-result null) — matching how this engine's own summarize
// aggregations already skip nulls by default elsewhere, and how a
// numeric-reduction function most commonly behaves in comparable
// libraries. This is a real, deliberate choice under genuine
// ambiguity, not a verified fact — flagged here plainly in case a
// future session finds real ADX actually propagates a single null
// element to the whole result (matching series_pearson_correlation's
// own explicitly documented "yields a null result" rule for arrays of
// different sizes) instead.

func evalSeriesSum(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	arr, ok, err := evalSeriesFillArrayArg(fc, schema, row)
	if err != nil {
		return nil, true, err
	}
	if !ok {
		return nil, true, nil
	}
	var sum float64
	for _, el := range arr {
		if f, ok := seriesElementToFloat(el); ok {
			sum += f
		}
	}
	return sum, true, nil
}

func evalSeriesProduct(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	arr, ok, err := evalSeriesFillArrayArg(fc, schema, row)
	if err != nil {
		return nil, true, err
	}
	if !ok {
		return nil, true, nil
	}
	product := 1.0
	for _, el := range arr {
		if f, ok := seriesElementToFloat(el); ok {
			product *= f
		}
	}
	return product, true, nil
}

// evalSeriesMagnitude implements series_magnitude(series) — "the
// square root of the dot product of the series with itself," verified
// directly against real ADX's own docs before writing this as
// math.Sqrt(sum of squares) rather than assumed from the function
// name alone.
func evalSeriesMagnitude(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	arr, ok, err := evalSeriesFillArrayArg(fc, schema, row)
	if err != nil {
		return nil, true, err
	}
	if !ok {
		return nil, true, nil
	}
	var sumSq float64
	for _, el := range arr {
		if f, ok := seriesElementToFloat(el); ok {
			sumSq += f * f
		}
	}
	return math.Sqrt(sumSq), true, nil
}

// evalSeriesPearsonCorrelation implements series_pearson_correlation
// (series1, series2) — the standard Pearson correlation coefficient.
// Verified against real ADX's own documented null rule, which is
// STRICTER than the single-array reduction functions above: "Any
// non-numeric element or nonexisting element (arrays of different
// sizes) yields a null RESULT" — the whole scalar result, not a
// per-element skip — so mismatched lengths or any non-numeric element
// anywhere fails the whole computation closed, matching that wording
// exactly rather than reusing the single-array functions' own
// skip-nulls assumption.
func evalSeriesPearsonCorrelation(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	a, b, ok, err := evalSeriesTwoArrayArgs(fc, schema, row)
	if err != nil {
		return nil, true, err
	}
	if !ok {
		return nil, true, nil
	}
	if len(a) != len(b) {
		return nil, true, nil
	}
	n := len(a)
	xs := make([]float64, n)
	ys := make([]float64, n)
	for i := 0; i < n; i++ {
		xf, xok := seriesElementToFloat(a[i])
		yf, yok := seriesElementToFloat(b[i])
		if !xok || !yok {
			return nil, true, nil
		}
		xs[i] = xf
		ys[i] = yf
	}
	if n == 0 {
		return nil, true, nil
	}
	var sumX, sumY float64
	for i := 0; i < n; i++ {
		sumX += xs[i]
		sumY += ys[i]
	}
	meanX := sumX / float64(n)
	meanY := sumY / float64(n)
	var num, denX, denY float64
	for i := 0; i < n; i++ {
		dx := xs[i] - meanX
		dy := ys[i] - meanY
		num += dx * dy
		denX += dx * dx
		denY += dy * dy
	}
	den := math.Sqrt(denX * denY)
	if den == 0 {
		return nil, true, nil
	}
	return num / den, true, nil
}

// evalSeriesStatsDynamic implements series_stats_dynamic(series[,
// ignore_nonfinite]) — verified exactly against real ADX's own worked
// example: x=[23,46,23,87,4,8,3,75,2,56,13,75,32,16,29] ->
// {"min":2,"min_idx":8,"max":87,"max_idx":3,"avg":32.8,
// "stdev":28.503633853548269,"variance":812.45714285714291,
// "sum":492,"len":15}. variance is SAMPLE variance (divide by n-1,
// matching this engine's own already-implemented aggregation-level
// "variance"), min_idx/max_idx are the 0-based index of the FIRST
// occurrence.
//
// ignore_nonfinite is accepted but not yet meaningfully implemented —
// this engine's own JSON-decoded array elements are either a finite
// float64 or already-absent (null/non-numeric skipped, same
// convention as the reduction functions above); there's no live NaN/
// Inf value that could appear in a dynamic array through this
// engine's own normal arithmetic paths to exercise the parameter's
// real distinction against, so it's accepted syntactically (to avoid
// a hard error on a real, valid call) without changing behavior.
func evalSeriesStatsDynamic(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	arr, ok, err := evalSeriesFillArrayArg(fc, schema, row)
	if err != nil {
		return nil, true, err
	}
	if !ok {
		return nil, true, nil
	}

	var vals []float64
	var idxs []int
	for i, el := range arr {
		if f, fok := seriesElementToFloat(el); fok {
			vals = append(vals, f)
			idxs = append(idxs, i)
		}
	}
	if len(vals) == 0 {
		return nil, true, nil
	}

	minV, maxV := vals[0], vals[0]
	minIdx, maxIdx := idxs[0], idxs[0]
	var sum float64
	for i, v := range vals {
		sum += v
		if v < minV {
			minV = v
			minIdx = idxs[i]
		}
		if v > maxV {
			maxV = v
			maxIdx = idxs[i]
		}
	}
	n := float64(len(vals))
	avg := sum / n
	var sumSqDiff float64
	for _, v := range vals {
		d := v - avg
		sumSqDiff += d * d
	}
	var variance, stdev float64
	if len(vals) > 1 {
		variance = sumSqDiff / (n - 1) // sample variance, matching
		// this engine's own aggregation-level "variance" already.
		stdev = math.Sqrt(variance)
	}

	obj := map[string]interface{}{
		"min":      minV,
		"min_idx":  minIdx,
		"max":      maxV,
		"max_idx":  maxIdx,
		"avg":      avg,
		"variance": variance,
		"stdev":    stdev,
		"sum":      sum,
		"len":      len(arr),
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, true, err
	}
	return string(b), true, nil
}

// --- shared helpers ---

// seriesElementToFloat converts one already-JSON-decoded array element
// to float64. Mirrors func_vector.go's own parseFloatArray element
// handling (float64 from a plain json.Unmarshal, or json.Number
// defensively) rather than duplicating a third, slightly different
// version of the same logic.
func seriesElementToFloat(el interface{}) (float64, bool) {
	switch v := el.(type) {
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// evalSeriesTwoArrayArgs evaluates and JSON-decodes both array
// arguments shared by every Tier 1 binary function. ok=false (with a
// nil value, no error) covers every real-ADX-documented "the result is
// null" case a caller should just pass through: either argument itself
// null, or not decodable as a JSON array at all.
func evalSeriesTwoArrayArgs(fc *parser.FuncCall, schema *types.Schema, row types.Row) (a, b []interface{}, ok bool, err error) {
	if len(fc.Args) != 2 {
		return nil, nil, false, fmt.Errorf("%s requires 2 arguments (series1, series2)", fc.Name)
	}
	aVal, err := evalExpr(fc.Args[0], schema, row)
	if err != nil {
		return nil, nil, false, err
	}
	bVal, err := evalExpr(fc.Args[1], schema, row)
	if err != nil {
		return nil, nil, false, err
	}
	if aVal == nil || bVal == nil {
		return nil, nil, false, nil
	}
	a, aok := parseJSONArray(aVal)
	b, bok := parseJSONArray(bVal)
	if !aok || !bok {
		return nil, nil, false, nil
	}
	return a, b, true, nil
}

// evalSeriesBinaryNumeric implements the arithmetic family
// (series_add/subtract/multiply/divide/pow): element-wise numeric op,
// output length = max(len(a), len(b)), each position null if either
// input is missing there (arrays of different sizes) or non-numeric —
// verified against series_multiply's own real-ADX worked example:
// s1=[1,2,4], s2=[4,2,1] -> s1_multiply_s2=[4,4,4] (element-wise:
// 1*4=4, 2*2=4, 4*1=4).
func evalSeriesBinaryNumeric(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	a, b, ok, err := evalSeriesTwoArrayArgs(fc, schema, row)
	if err != nil {
		return nil, true, err
	}
	if !ok {
		return nil, true, nil
	}
	var op func(x, y float64) interface{}
	switch fc.Name {
	case "series_add":
		op = func(x, y float64) interface{} { return x + y }
	case "series_subtract":
		op = func(x, y float64) interface{} { return x - y }
	case "series_multiply":
		op = func(x, y float64) interface{} { return x * y }
	case "series_divide":
		// Real ADX's own docs don't spell out divide-by-zero
		// behavior for series_divide specifically; null here is
		// consistent with this whole function family's own
		// documented "non-numeric or non-existing element yields a
		// null element value" rule — a divide-by-zero result isn't a
		// real, meaningful number either, so treating it the same
		// way (null, not +Inf/NaN silently smuggled into a dynamic
		// array) is the safer, more defensible reading rather than a
		// guess with no textual basis at all.
		op = func(x, y float64) interface{} {
			if y == 0 {
				return nil
			}
			return x / y
		}
	case "series_pow":
		op = func(x, y float64) interface{} { return math.Pow(x, y) }
	}
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	out := make([]interface{}, n)
	for i := 0; i < n; i++ {
		if i >= len(a) || i >= len(b) {
			continue // stays nil -> JSON null
		}
		xf, xok := seriesElementToFloat(a[i])
		yf, yok := seriesElementToFloat(b[i])
		if !xok || !yok {
			continue
		}
		out[i] = op(xf, yf)
	}
	return marshalSeriesArray(out)
}

// evalSeriesBinaryBool implements the comparison family: element-wise
// logical comparison, returning a dynamic array of BOOLEANS (verified
// directly against real ADX's own series_greater_equals docs — "Dynamic
// array of booleans..." — not 1.0/0.0 floats). Same length/null rules
// as evalSeriesBinaryNumeric.
func evalSeriesBinaryBool(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	a, b, ok, err := evalSeriesTwoArrayArgs(fc, schema, row)
	if err != nil {
		return nil, true, err
	}
	if !ok {
		return nil, true, nil
	}
	var op func(x, y float64) bool
	switch fc.Name {
	case "series_equals":
		op = func(x, y float64) bool { return x == y }
	case "series_not_equals":
		op = func(x, y float64) bool { return x != y }
	case "series_less":
		op = func(x, y float64) bool { return x < y }
	case "series_less_equals":
		op = func(x, y float64) bool { return x <= y }
	case "series_greater":
		op = func(x, y float64) bool { return x > y }
	case "series_greater_equals":
		op = func(x, y float64) bool { return x >= y }
	}
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	out := make([]interface{}, n)
	for i := 0; i < n; i++ {
		if i >= len(a) || i >= len(b) {
			continue
		}
		xf, xok := seriesElementToFloat(a[i])
		yf, yok := seriesElementToFloat(b[i])
		if !xok || !yok {
			continue
		}
		out[i] = op(xf, yf)
	}
	return marshalSeriesArray(out)
}

// evalSeriesUnary implements the unary math/trig family: map a single
// numeric op across one array's elements, preserving null (or any
// non-numeric element) in place rather than erroring — matching this
// whole function family's general "non-numeric -> null" convention
// rather than failing the entire array over one bad element.
// series_log/series_exp deliberately use math.Log/math.Exp (natural
// base) to match the already-verified-correct scalar log()/exp()
// functions (func_convert.go) — confirmed live before assuming, not
// copied blindly: log(e)=1, exp(1)=e, log10(1000)=3, exp10(2)=100 all
// check out already.
func evalSeriesUnary(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if len(fc.Args) != 1 {
		return nil, true, fmt.Errorf("%s requires 1 argument (series)", fc.Name)
	}
	val, err := evalExpr(fc.Args[0], schema, row)
	if err != nil {
		return nil, true, err
	}
	if val == nil {
		return nil, true, nil
	}
	arr, ok := parseJSONArray(val)
	if !ok {
		return nil, true, nil
	}
	var op func(x float64) interface{}
	switch fc.Name {
	case "series_abs":
		op = func(x float64) interface{} { return math.Abs(x) }
	case "series_sign":
		op = func(x float64) interface{} {
			switch {
			case x > 0:
				return 1.0
			case x < 0:
				return -1.0
			default:
				return 0.0
			}
		}
	case "series_ceiling":
		op = func(x float64) interface{} { return math.Ceil(x) }
	case "series_floor":
		op = func(x float64) interface{} { return math.Floor(x) }
	case "series_log":
		op = func(x float64) interface{} { return math.Log(x) }
	case "series_exp":
		op = func(x float64) interface{} { return math.Exp(x) }
	case "series_sin":
		op = func(x float64) interface{} { return math.Sin(x) }
	case "series_cos":
		op = func(x float64) interface{} { return math.Cos(x) }
	case "series_tan":
		op = func(x float64) interface{} { return math.Tan(x) }
	case "series_asin":
		op = func(x float64) interface{} { return math.Asin(x) }
	case "series_acos":
		op = func(x float64) interface{} { return math.Acos(x) }
	case "series_atan":
		op = func(x float64) interface{} { return math.Atan(x) }
	}
	out := make([]interface{}, len(arr))
	for i, el := range arr {
		xf, xok := seriesElementToFloat(el)
		if !xok {
			continue // el was already null/non-numeric -> stays nil
		}
		out[i] = op(xf)
	}
	return marshalSeriesArray(out)
}

// marshalSeriesArray is the shared tail of every Tier 1 function
// above: JSON-encode the []interface{} result into this engine's own
// dynamic-column string convention.
func marshalSeriesArray(out []interface{}) (types.Value, bool, error) {
	b, err := json.Marshal(out)
	if err != nil {
		return nil, true, err
	}
	return string(b), true, nil
}

// --- Tier 2: gap filling ---

// evalSeriesFillArrayArg evaluates the mandatory first argument
// (series, a dynamic array) shared by all four fill functions.
func evalSeriesFillArrayArg(fc *parser.FuncCall, schema *types.Schema, row types.Row) ([]interface{}, bool, error) {
	if len(fc.Args) == 0 {
		return nil, false, fmt.Errorf("%s requires at least 1 argument (series)", fc.Name)
	}
	val, err := evalExpr(fc.Args[0], schema, row)
	if err != nil {
		return nil, false, err
	}
	if val == nil {
		return nil, false, nil
	}
	arr, ok := parseJSONArray(val)
	return arr, ok, nil
}

// elementMatchesPlaceholder reports whether el (an already-JSON-
// decoded array element) equals placeholder (an evaluated scalar
// argument, nil representing the real-ADX default double(null)) —
// nil-vs-nil for the null placeholder case, numeric equality
// otherwise.
func elementMatchesPlaceholder(el interface{}, placeholder types.Value) bool {
	if placeholder == nil {
		return el == nil
	}
	elF, elOk := seriesElementToFloat(el)
	if !elOk {
		return false
	}
	return elF == types.ToFloat64(placeholder)
}

// evalSeriesFillForward implements series_fill_forward(series[,
// missing_value_placeholder]) — each placeholder position is replaced
// with the nearest non-placeholder value to its LEFT; a placeholder
// run at the very start of the array (no earlier value at all) is left
// unfilled. Symmetric with evalSeriesFillBackward below, verified
// directly against real ADX's own series_fill_backward worked example
// (see that function's own comment) rather than against a fetched
// example for THIS direction specifically, since the two are simple
// mirror images of the same algorithm.
func evalSeriesFillForward(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	arr, ok, err := evalSeriesFillArrayArg(fc, schema, row)
	if err != nil {
		return nil, true, err
	}
	if !ok {
		return nil, true, nil
	}
	placeholder, err := seriesOptionalPlaceholderArg(fc, schema, row, 1)
	if err != nil {
		return nil, true, err
	}
	out := make([]interface{}, len(arr))
	var lastSeen interface{}
	haveLastSeen := false
	for i, el := range arr {
		if elementMatchesPlaceholder(el, placeholder) {
			if haveLastSeen {
				out[i] = lastSeen
			} else {
				out[i] = el // unfilled — no earlier value yet
			}
			continue
		}
		out[i] = el
		lastSeen = el
		haveLastSeen = true
	}
	return marshalSeriesArray(out)
}

// evalSeriesFillBackward implements series_fill_backward(series[,
// missing_value_placeholder]) — each placeholder position is replaced
// with the nearest non-placeholder value to its RIGHT; a placeholder
// run at the very end (no later value at all) is left unfilled.
// Verified exactly against real ADX's own worked example: input
// [111,null,36,41,null,null,16,61,33,null,null] with the default
// (null) placeholder produces
// [111,36,36,41,16,16,16,61,33,null,null] — traced by hand against
// this implementation's own right-to-left scan before trusting it,
// not assumed to match from the algorithm description alone.
func evalSeriesFillBackward(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	arr, ok, err := evalSeriesFillArrayArg(fc, schema, row)
	if err != nil {
		return nil, true, err
	}
	if !ok {
		return nil, true, nil
	}
	placeholder, err := seriesOptionalPlaceholderArg(fc, schema, row, 1)
	if err != nil {
		return nil, true, err
	}
	out := make([]interface{}, len(arr))
	var lastSeen interface{}
	haveLastSeen := false
	for i := len(arr) - 1; i >= 0; i-- {
		el := arr[i]
		if elementMatchesPlaceholder(el, placeholder) {
			if haveLastSeen {
				out[i] = lastSeen
			} else {
				out[i] = el
			}
			continue
		}
		out[i] = el
		lastSeen = el
		haveLastSeen = true
	}
	return marshalSeriesArray(out)
}

// evalSeriesFillConst implements series_fill_const(series,
// constant_value[, missing_value_placeholder]) — every placeholder
// position is replaced with the literal constant_value, unconditionally
// (no "nearest neighbor" search, unlike forward/backward fill).
func evalSeriesFillConst(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if len(fc.Args) < 2 {
		return nil, true, fmt.Errorf("series_fill_const requires at least 2 arguments (series, constant_value)")
	}
	arr, ok, err := evalSeriesFillArrayArg(fc, schema, row)
	if err != nil {
		return nil, true, err
	}
	if !ok {
		return nil, true, nil
	}
	constVal, err := evalExpr(fc.Args[1], schema, row)
	if err != nil {
		return nil, true, err
	}
	placeholder, err := seriesOptionalPlaceholderArg(fc, schema, row, 2)
	if err != nil {
		return nil, true, err
	}
	out := make([]interface{}, len(arr))
	for i, el := range arr {
		if elementMatchesPlaceholder(el, placeholder) {
			out[i] = constVal
		} else {
			out[i] = el
		}
	}
	return marshalSeriesArray(out)
}

// evalSeriesFillLinear implements series_fill_linear(series[,
// missing_value_placeholder[, fill_edges[, constant_value]]]) — each
// placeholder run strictly BETWEEN two real values is replaced by
// linear interpolation across the gap. A placeholder run at either
// edge (no real value on one side) is governed by fill_edges: filled
// with the single available nearest real value when true, left as the
// placeholder when false. Defaults, per real ADX's own docs, verified
// directly: missing_value_placeholder = double(null); constant_value
// (used only when the WHOLE array is placeholder) = 0.
//
// fill_edges' own default was NOT found stated as plainly as the other
// two in the sources available while researching this — real ADX's
// docs describe the PARAMETER's effect precisely but the fetched text
// didn't pin down which way it defaults. Defaulting to true here
// (edges filled) as the more broadly useful behavior for this
// project's own likely use (a security-metric series rarely wants
// isolated leading/trailing gaps left as null downstream) — flagged
// here plainly as an assumption, not a confirmed fact, so a future
// session can correct it on sight if real ADX's own default turns out
// to be false.
//
// Scope note: real ADX also documents that an all-int/all-long input
// series returns ROUNDED interpolated values rather than exact ones.
// Not implemented here — by the time a dynamic array reaches this
// function it's already generic JSON-decoded float64 with no reliable
// way to recover "was this originally declared int/long" from the
// value alone, so this always returns exact (non-rounded)
// interpolation. A real, narrow, deliberately-scoped gap, not an
// oversight.
func evalSeriesFillLinear(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	arr, ok, err := evalSeriesFillArrayArg(fc, schema, row)
	if err != nil {
		return nil, true, err
	}
	if !ok {
		return nil, true, nil
	}
	placeholder, err := seriesOptionalPlaceholderArg(fc, schema, row, 1)
	if err != nil {
		return nil, true, err
	}
	fillEdges := true
	if len(fc.Args) >= 3 {
		fev, err := evalExpr(fc.Args[2], schema, row)
		if err != nil {
			return nil, true, err
		}
		if b, ok := fev.(bool); ok {
			fillEdges = b
		}
	}
	var constantValue float64 = 0
	if len(fc.Args) >= 4 {
		cv, err := evalExpr(fc.Args[3], schema, row)
		if err != nil {
			return nil, true, err
		}
		if cv != nil {
			constantValue = types.ToFloat64(cv)
		}
	}

	n := len(arr)
	out := make([]interface{}, n)
	isReal := make([]bool, n)
	vals := make([]float64, n)
	anyReal := false
	for i, el := range arr {
		if !elementMatchesPlaceholder(el, placeholder) {
			if f, ok := seriesElementToFloat(el); ok {
				isReal[i] = true
				vals[i] = f
				anyReal = true
			}
		}
	}
	if !anyReal {
		// "If the whole array consists of the missing_value_placeholder,
		// the array is filled with constant_value, or 0 if not
		// specified" — verified directly against real ADX's own docs.
		for i := range out {
			out[i] = constantValue
		}
		return marshalSeriesArray(out)
	}

	for i := 0; i < n; i++ {
		if isReal[i] {
			out[i] = vals[i]
			continue
		}
		// Find nearest real neighbor to the left and right.
		leftIdx, rightIdx := -1, -1
		for j := i - 1; j >= 0; j-- {
			if isReal[j] {
				leftIdx = j
				break
			}
		}
		for j := i + 1; j < n; j++ {
			if isReal[j] {
				rightIdx = j
				break
			}
		}
		switch {
		case leftIdx >= 0 && rightIdx >= 0:
			frac := float64(i-leftIdx) / float64(rightIdx-leftIdx)
			out[i] = vals[leftIdx] + frac*(vals[rightIdx]-vals[leftIdx])
		case leftIdx >= 0 && fillEdges:
			out[i] = vals[leftIdx]
		case rightIdx >= 0 && fillEdges:
			out[i] = vals[rightIdx]
		default:
			out[i] = arr[i] // leave as placeholder (fill_edges=false, or truly unreachable here since anyReal is true)
		}
	}
	return marshalSeriesArray(out)
}

// seriesOptionalPlaceholderArg evaluates the missing_value_placeholder
// argument at argIndex if present, defaulting to nil (representing
// double(null), real ADX's own documented default for every fill
// function) when omitted.
func seriesOptionalPlaceholderArg(fc *parser.FuncCall, schema *types.Schema, row types.Row, argIndex int) (types.Value, error) {
	if len(fc.Args) <= argIndex {
		return nil, nil
	}
	return evalExpr(fc.Args[argIndex], schema, row)
}

