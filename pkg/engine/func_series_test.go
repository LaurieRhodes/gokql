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

