package engine

import (
	"encoding/json"
	"testing"
)

// func_series_test.go — Tier 1 (element-wise) and Tier 2 (fill)
// series_* functions. Every test below is checked against a real,
// worked example from real ADX's own docs where one exists, or
// hand-traced against the docs' own stated algorithm where it
// doesn't — not just "does it run". See func_series.go's own top
// comment for the two findings (bool comparison output, two different
// missing-element conventions across function families) that
// corrected an earlier planning-stage guess.

func seriesJSONArray(t *testing.T, s string) []interface{} {
	t.Helper()
	var arr []interface{}
	if err := json.Unmarshal([]byte(s), &arr); err != nil {
		t.Fatalf("seriesJSONArray: invalid JSON %q: %v", s, err)
	}
	return arr
}

// TestSeriesMultiplyWorkedExample guards real ADX's own series_multiply
// worked example exactly, across all three rows.
func TestSeriesMultiplyWorkedExample(t *testing.T) {
	result := queryResult(t, `range x from 1 to 3 step 1
		| extend y = x * 2
		| extend z = y * 2
		| project s1 = pack_array(x,y,z), s2 = pack_array(z, y, x)
		| extend s1_multiply_s2 = series_multiply(s1, s2)`)
	rIdx := result.Schema.ColumnIndex("s1_multiply_s2")
	want := [][]float64{{4, 4, 4}, {16, 16, 16}, {36, 36, 36}}
	for i, w := range want {
		arr := seriesJSONArray(t, result.Rows[i][rIdx].(string))
		for j, wv := range w {
			if arr[j].(float64) != wv {
				t.Errorf("row %d elem %d = %v, want %v", i, j, arr[j], wv)
			}
		}
	}
}

// TestSeriesArithmeticMismatchedLengths guards the arithmetic family's
// own documented null-padding rule: "Any non-numeric element or
// non-existing element (arrays of different sizes) yields a null
// element value" — verified against series_multiply's own docs.
func TestSeriesArithmeticMismatchedLengths(t *testing.T) {
	result := queryResult(t, `print r = series_add(dynamic([1,2,3]), dynamic([10,20]))`)
	arr := seriesJSONArray(t, result.Rows[0][0].(string))
	if len(arr) != 3 {
		t.Fatalf("expected length 3 (max of inputs), got %d: %v", len(arr), arr)
	}
	if arr[0].(float64) != 11 || arr[1].(float64) != 22 || arr[2] != nil {
		t.Errorf("got %v, want [11,22,null]", arr)
	}
}

// TestSeriesComparisonReturnsBooleans guards a real, corrected finding
// from this feature's own research: comparison functions return a
// dynamic array of BOOLEANS, not 1.0/0.0 floats (verified directly
// against real ADX's own series_greater_equals docs, which state this
// explicitly — an earlier planning-stage guess had assumed the
// opposite).
func TestSeriesComparisonReturnsBooleans(t *testing.T) {
	result := queryResult(t, `print s1 = dynamic([1,2,4]), s2 = dynamic([4,2,1])
		| extend r = series_greater_equals(s1, s2)`)
	rIdx := result.Schema.ColumnIndex("r")
	arr := seriesJSONArray(t, result.Rows[0][rIdx].(string))
	want := []bool{false, true, true} // 1>=4=false, 2>=2=true, 4>=1=true
	for i, w := range want {
		b, ok := arr[i].(bool)
		if !ok {
			t.Fatalf("elem %d is %T, not bool: %v", i, arr[i], arr[i])
		}
		if b != w {
			t.Errorf("elem %d = %v, want %v", i, b, w)
		}
	}
}

// TestSeriesUnaryMath spot-checks the unary math family, including
// that series_log/series_exp use the natural base — matching the
// already-verified-correct scalar log()/exp() functions
// (func_convert.go), confirmed live before relying on it rather than
// assumed.
func TestSeriesUnaryMath(t *testing.T) {
	result := queryResult(t, `print r1 = series_abs(dynamic([-1,2,-3])),
		r2 = series_sign(dynamic([-5,0,5])),
		r3 = series_ceiling(dynamic([1.1,1.9])),
		r4 = series_floor(dynamic([1.1,1.9])),
		r5 = series_exp(dynamic([1])),
		r6 = series_sin(dynamic([0]))`)

	abs := seriesJSONArray(t, result.Rows[0][result.Schema.ColumnIndex("r1")].(string))
	if abs[0].(float64) != 1 || abs[1].(float64) != 2 || abs[2].(float64) != 3 {
		t.Errorf("series_abs = %v, want [1,2,3]", abs)
	}
	sign := seriesJSONArray(t, result.Rows[0][result.Schema.ColumnIndex("r2")].(string))
	if sign[0].(float64) != -1 || sign[1].(float64) != 0 || sign[2].(float64) != 1 {
		t.Errorf("series_sign = %v, want [-1,0,1]", sign)
	}
	ceil := seriesJSONArray(t, result.Rows[0][result.Schema.ColumnIndex("r3")].(string))
	if ceil[0].(float64) != 2 || ceil[1].(float64) != 2 {
		t.Errorf("series_ceiling = %v, want [2,2]", ceil)
	}
	floor := seriesJSONArray(t, result.Rows[0][result.Schema.ColumnIndex("r4")].(string))
	if floor[0].(float64) != 1 || floor[1].(float64) != 1 {
		t.Errorf("series_floor = %v, want [1,1]", floor)
	}
	exp := seriesJSONArray(t, result.Rows[0][result.Schema.ColumnIndex("r5")].(string))
	if d := exp[0].(float64) - 2.718281828459045; d > 1e-9 || d < -1e-9 {
		t.Errorf("series_exp(1) = %v, want e (natural base, not 10)", exp[0])
	}
	sin := seriesJSONArray(t, result.Rows[0][result.Schema.ColumnIndex("r6")].(string))
	if sin[0].(float64) != 0 {
		t.Errorf("series_sin(0) = %v, want 0", sin[0])
	}
}

// TestSeriesFillBackwardWorkedExample guards real ADX's own
// series_fill_backward worked example exactly.
func TestSeriesFillBackwardWorkedExample(t *testing.T) {
	result := queryResult(t, `print r = series_fill_backward(dynamic([111, null, 36, 41, null, null, 16, 61, 33, null, null]))`)
	arr := seriesJSONArray(t, result.Rows[0][0].(string))
	want := []interface{}{111.0, 36.0, 36.0, 41.0, 16.0, 16.0, 16.0, 61.0, 33.0, nil, nil}
	if len(arr) != len(want) {
		t.Fatalf("length = %d, want %d", len(arr), len(want))
	}
	for i, w := range want {
		if arr[i] != w {
			t.Errorf("elem %d = %v, want %v", i, arr[i], w)
		}
	}
}

// TestSeriesFillForward is series_fill_backward's own mirror image —
// hand-traced against the same algorithm in the opposite direction
// (see func_series.go's own doc comment for why no separate real-ADX
// worked example was needed to trust this one).
func TestSeriesFillForward(t *testing.T) {
	result := queryResult(t, `print r = series_fill_forward(dynamic([null, null, 36, 41, null, null, 16, 61, 33, null, null]))`)
	arr := seriesJSONArray(t, result.Rows[0][0].(string))
	want := []interface{}{nil, nil, 36.0, 41.0, 41.0, 41.0, 16.0, 61.0, 33.0, 33.0, 33.0}
	if len(arr) != len(want) {
		t.Fatalf("length = %d, want %d", len(arr), len(want))
	}
	for i, w := range want {
		if arr[i] != w {
			t.Errorf("elem %d = %v, want %v", i, arr[i], w)
		}
	}
}

// TestSeriesFillConst guards real ADX's own series_fill_const worked
// example (both a float and an int constant_value).
func TestSeriesFillConst(t *testing.T) {
	result := queryResult(t, `print fill_const1 = series_fill_const(dynamic([111, null, 36, 41, 23, null, 16, 61, 33, null, null]), 0.0),
		fill_const2 = series_fill_const(dynamic([111, null, 36, 41, 23, null, 16, 61, 33, null, null]), -1)`)
	c1 := seriesJSONArray(t, result.Rows[0][result.Schema.ColumnIndex("fill_const1")].(string))
	c2 := seriesJSONArray(t, result.Rows[0][result.Schema.ColumnIndex("fill_const2")].(string))
	if c1[1].(float64) != 0.0 || c1[5].(float64) != 0.0 {
		t.Errorf("fill_const1 nulls not replaced with 0.0: %v", c1)
	}
	if c2[1].(float64) != -1 || c2[5].(float64) != -1 {
		t.Errorf("fill_const2 nulls not replaced with -1: %v", c2)
	}
	// Non-null positions must survive unchanged.
	if c1[0].(float64) != 111 || c1[2].(float64) != 36 {
		t.Errorf("fill_const1 changed non-null values: %v", c1)
	}
}

// TestSeriesFillLinearBasicInterpolation guards straightforward
// interior linear interpolation.
func TestSeriesFillLinearBasicInterpolation(t *testing.T) {
	result := queryResult(t, `print r = series_fill_linear(dynamic([1, null, null, 4]))`)
	arr := seriesJSONArray(t, result.Rows[0][0].(string))
	want := []float64{1, 2, 3, 4}
	for i, w := range want {
		if arr[i].(float64) != w {
			t.Errorf("elem %d = %v, want %v", i, arr[i], w)
		}
	}
}

// TestSeriesFillLinearAllPlaceholder guards real ADX's own documented
// rule: "If the whole array consists of the missing_value_placeholder,
// the array is filled with constant_value, or 0 if not specified."
func TestSeriesFillLinearAllPlaceholder(t *testing.T) {
	result := queryResult(t, `print r = series_fill_linear(dynamic([null,null,null,null]))`)
	arr := seriesJSONArray(t, result.Rows[0][0].(string))
	for i, v := range arr {
		if v.(float64) != 0 {
			t.Errorf("elem %d = %v, want 0 (documented default constant_value)", i, v)
		}
	}
}

// TestSeriesDivideByZeroYieldsNull confirms this engine's own
// documented, deliberate choice for an undocumented real-ADX edge case
// (divide by zero) — null, not +Inf/NaN silently smuggled into a
// dynamic array.
func TestSeriesDivideByZeroYieldsNull(t *testing.T) {
	result := queryResult(t, `print r = series_divide(dynamic([10,20]), dynamic([2,0]))`)
	arr := seriesJSONArray(t, result.Rows[0][0].(string))
	if arr[0].(float64) != 5 {
		t.Errorf("elem 0 = %v, want 5", arr[0])
	}
	if arr[1] != nil {
		t.Errorf("elem 1 (divide by zero) = %v, want null", arr[1])
	}
}

// TestSeriesWrongArgCountRejected confirms a clear error, not a panic
// or silent misinterpretation, for the wrong number of arguments.
func TestSeriesWrongArgCountRejected(t *testing.T) {
	queryError(t, `print r = series_add(dynamic([1,2]))`)
	queryError(t, `print r = series_abs(dynamic([1,2]), dynamic([3,4]))`)
}

// TestSeriesSumWorkedExample guards real ADX's own series_sum worked
// example exactly: [1,2,3,4] -> 10.
func TestSeriesSumWorkedExample(t *testing.T) {
	result := queryResult(t, `print arr=dynamic([1,2,3,4]) | extend series_sum=series_sum(arr)`)
	sIdx := result.Schema.ColumnIndex("series_sum")
	if result.Rows[0][sIdx].(float64) != 10 {
		t.Errorf("series_sum([1,2,3,4]) = %v, want 10", result.Rows[0][sIdx])
	}
}

// TestSeriesProduct guards the natural, unambiguous product analog of
// series_sum (no fetched real-ADX worked example found for this one
// specifically — see func_series.go's own Tier 3 comment for why that
// doesn't reduce confidence here).
func TestSeriesProduct(t *testing.T) {
	result := queryResult(t, `print r = series_product(dynamic([1,2,3,4]))`)
	if result.Rows[0][0].(float64) != 24 {
		t.Errorf("series_product([1,2,3,4]) = %v, want 24", result.Rows[0][0])
	}
}

// TestSeriesMagnitude guards series_magnitude against a simple 3-4-5
// right-triangle case (sqrt(3^2+4^2) = 5) — verified against real
// ADX's own documented definition ("square root of the dot product of
// the series with itself") rather than assumed from the name alone.
func TestSeriesMagnitude(t *testing.T) {
	result := queryResult(t, `print r = series_magnitude(dynamic([3,4]))`)
	if result.Rows[0][0].(float64) != 5 {
		t.Errorf("series_magnitude([3,4]) = %v, want 5", result.Rows[0][0])
	}
}

// TestSeriesPearsonCorrelationWorkedExample guards real ADX's own
// series_pearson_correlation worked example exactly: a perfectly
// linearly correlated pair (s2 = 2*s1) yields a correlation
// coefficient of 1. This is also the regression test for a real,
// separate bug this feature surfaced and fixed: make_list/make_set
// previously stringified every element before JSON-marshaling
// ("1","2",... instead of 1,2,...), which silently broke this exact
// real-ADX-documented calling pattern (summarize make_list(...) then
// feeding the result into a series_* function) — see aggregation.go's
// own updated make_list/make_set comments for the full fix.
func TestSeriesPearsonCorrelationWorkedExample(t *testing.T) {
	result := queryResult(t, `range s1 from 1 to 5 step 1
		| extend s2 = 2 * s1
		| summarize s1 = make_list(s1), s2 = make_list(s2)
		| extend correlation_coefficient = series_pearson_correlation(s1, s2)`)
	cIdx := result.Schema.ColumnIndex("correlation_coefficient")
	got := result.Rows[0][cIdx].(float64)
	if d := got - 1.0; d > 1e-9 || d < -1e-9 {
		t.Errorf("correlation_coefficient = %v, want 1 (perfect linear correlation)", got)
	}
}

// TestSeriesPearsonCorrelationMismatchedLengthYieldsNull guards real
// ADX's own documented rule for this function specifically — stricter
// than the Tier 1 arithmetic family's per-position null: "Any
// non-numeric element or nonexisting element (arrays of different
// sizes) yields a null RESULT" (the whole scalar result, not a
// per-element skip).
func TestSeriesPearsonCorrelationMismatchedLengthYieldsNull(t *testing.T) {
	result := queryResult(t, `print r = series_pearson_correlation(dynamic([1,2,3]), dynamic([1,2]))`)
	if result.Rows[0][0] != nil {
		t.Errorf("series_pearson_correlation with mismatched lengths = %v, want nil", result.Rows[0][0])
	}
}

// TestSeriesStatsDynamicWorkedExample guards real ADX's own
// series_stats_dynamic worked example exactly (allowing tiny
// floating-point last-digit tolerance on stdev/variance).
func TestSeriesStatsDynamicWorkedExample(t *testing.T) {
	result := queryResult(t, `print x=dynamic([23, 46, 23, 87, 4, 8, 3, 75, 2, 56, 13, 75, 32, 16, 29])
		| project stats=series_stats_dynamic(x)`)
	var stats map[string]interface{}
	if err := json.Unmarshal([]byte(result.Rows[0][0].(string)), &stats); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	checks := map[string]float64{
		"min": 2, "min_idx": 8, "max": 87, "max_idx": 3,
		"avg": 32.8, "sum": 492, "len": 15,
	}
	for k, want := range checks {
		got, ok := stats[k].(float64)
		if !ok {
			t.Fatalf("stats[%q] missing or not a number: %v", k, stats[k])
		}
		if got != want {
			t.Errorf("stats[%q] = %v, want %v", k, got, want)
		}
	}
	if d := stats["stdev"].(float64) - 28.503633853548269; d > 1e-6 || d < -1e-6 {
		t.Errorf("stats[stdev] = %v, want ~28.5036...", stats["stdev"])
	}
	if d := stats["variance"].(float64) - 812.457142857143; d > 1e-6 || d < -1e-6 {
		t.Errorf("stats[variance] = %v, want ~812.457...", stats["variance"])
	}
}

// TestMakeListPreservesNativeTypes and TestMakeSetPreservesNativeTypes
// are the direct regression guards for the make_list/make_set fix
// itself: JSON array elements must be genuine JSON numbers/booleans
// for a numeric/bool column, not quoted strings.
func TestMakeListPreservesNativeTypes(t *testing.T) {
	result := queryResult(t, `datatable(x:long)[1,2,3] | summarize make_list(x)`)
	arr := seriesJSONArray(t, result.Rows[0][0].(string))
	for i, want := range []float64{1, 2, 3} {
		got, ok := arr[i].(float64)
		if !ok {
			t.Fatalf("elem %d is %T, not a JSON number: %v", i, arr[i], arr[i])
		}
		if got != want {
			t.Errorf("elem %d = %v, want %v", i, got, want)
		}
	}
}

func TestMakeSetPreservesNativeTypes(t *testing.T) {
	result := queryResult(t, `datatable(x:long)[3,1,2,1] | summarize make_set(x)`)
	arr := seriesJSONArray(t, result.Rows[0][0].(string))
	if len(arr) != 3 {
		t.Fatalf("expected 3 distinct values, got %d: %v", len(arr), arr)
	}
	for i, v := range arr {
		if _, ok := v.(float64); !ok {
			t.Fatalf("elem %d is %T, not a JSON number: %v", i, v, v)
		}
	}
}

// TestMakeListDatetimeFormatsCorrectly confirms the fix's type-aware
// conversion (via valueForJSONArray, reused from make_series.go)
// applies to make_list too — a datetime column must format as an ISO
// string, not a raw UnixNano integer.
func TestMakeListDatetimeFormatsCorrectly(t *testing.T) {
	result := queryResult(t, `datatable(t:datetime)[datetime(2020-01-01)] | summarize make_list(t)`)
	arr := seriesJSONArray(t, result.Rows[0][0].(string))
	s, ok := arr[0].(string)
	if !ok {
		t.Fatalf("elem 0 is %T, not a string: %v", arr[0], arr[0])
	}
	if s != "2020-01-01T00:00:00.0000000Z" {
		t.Errorf("elem 0 = %q, want 2020-01-01T00:00:00.0000000Z", s)
	}
}

