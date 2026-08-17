package engine

import "testing"

// func_trig_test.go — scalar sin/cos/tan/asin/acos/atan/atan2/degrees/
// radians, added 2026-08-17 alongside func_convert.go's new trig
// case. Verified against real ADX docs' own worked examples, not
// just "does it run":
//   - atan2(1,1) = Pi/4, atan2(0,-1) = Pi, atan2(-1,0) = -Pi/2
//     (learn.microsoft.com/kusto/query/atan2-function)
//   - degrees(pi()/4) = 45, degrees(pi()*1.5) = 270, degrees(0) = 0
//   - radians(90) = 1.5707963267949, radians(180) = 3.14159265358979,
//     radians(360) = 6.28318530717959
// atan2's argument order (y, x) is confirmed to match Go's own
// math.Atan2(y, x), so no argument swap was needed in the
// implementation.

const trigEpsilon = 1e-9

func approxEqual(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < trigEpsilon
}

func TestTrigAtan2WorkedExample(t *testing.T) {
	result := queryResult(t, `print atan2_0 = atan2(1,1), atan2_1 = atan2(0,-1), atan2_2 = atan2(-1,0)`)
	got0 := result.Rows[0][0].(float64)
	got1 := result.Rows[0][1].(float64)
	got2 := result.Rows[0][2].(float64)
	if !approxEqual(got0, 0.7853981633974483) { // Pi/4
		t.Errorf("atan2(1,1) = %v, want Pi/4 (0.7853981633974483)", got0)
	}
	if !approxEqual(got1, 3.141592653589793) { // Pi
		t.Errorf("atan2(0,-1) = %v, want Pi (3.141592653589793)", got1)
	}
	if !approxEqual(got2, -1.5707963267948966) { // -Pi/2
		t.Errorf("atan2(-1,0) = %v, want -Pi/2 (-1.5707963267948966)", got2)
	}
}

func TestTrigDegreesWorkedExample(t *testing.T) {
	result := queryResult(t, `print d0 = degrees(pi()/4), d1 = degrees(pi()*1.5), d2 = degrees(0)`)
	if got := result.Rows[0][0].(float64); !approxEqual(got, 45) {
		t.Errorf("degrees(pi()/4) = %v, want 45", got)
	}
	if got := result.Rows[0][1].(float64); !approxEqual(got, 270) {
		t.Errorf("degrees(pi()*1.5) = %v, want 270", got)
	}
	if got := result.Rows[0][2].(float64); !approxEqual(got, 0) {
		t.Errorf("degrees(0) = %v, want 0", got)
	}
}

func TestTrigRadiansWorkedExample(t *testing.T) {
	result := queryResult(t, `print r0 = radians(90), r1 = radians(180), r2 = radians(360)`)
	if got := result.Rows[0][0].(float64); !approxEqual(got, 1.5707963267949) {
		t.Errorf("radians(90) = %v, want 1.5707963267949", got)
	}
	if got := result.Rows[0][1].(float64); !approxEqual(got, 3.14159265358979) {
		t.Errorf("radians(180) = %v, want 3.14159265358979", got)
	}
	if got := result.Rows[0][2].(float64); !approxEqual(got, 6.28318530717959) {
		t.Errorf("radians(360) = %v, want 6.28318530717959", got)
	}
}

// TestTrigBasicIdentities spot-checks sin/cos/tan/asin/acos/atan at
// well-known values not otherwise covered by the atan2/degrees/
// radians worked examples above.
func TestTrigBasicIdentities(t *testing.T) {
	result := queryResult(t, `print a=sin(0), b=cos(0), c=tan(0), d=asin(1), e=acos(1), f=atan(1)`)
	if got := result.Rows[0][0].(float64); !approxEqual(got, 0) {
		t.Errorf("sin(0) = %v, want 0", got)
	}
	if got := result.Rows[0][1].(float64); !approxEqual(got, 1) {
		t.Errorf("cos(0) = %v, want 1", got)
	}
	if got := result.Rows[0][2].(float64); !approxEqual(got, 0) {
		t.Errorf("tan(0) = %v, want 0", got)
	}
	if got := result.Rows[0][3].(float64); !approxEqual(got, 1.5707963267948966) {
		t.Errorf("asin(1) = %v, want Pi/2", got)
	}
	if got := result.Rows[0][4].(float64); !approxEqual(got, 0) {
		t.Errorf("acos(1) = %v, want 0", got)
	}
	if got := result.Rows[0][5].(float64); !approxEqual(got, 0.7853981633974483) {
		t.Errorf("atan(1) = %v, want Pi/4", got)
	}
}

// TestTrigNullPropagation confirms the new functions follow this
// file's own established null-propagation convention (see
// TestConversionFunctionsPropagateNull) rather than silently
// returning 0 for a null argument.
func TestTrigNullPropagation(t *testing.T) {
	result := queryResult(t, `print a=sin(null), b=cos(null), c=tan(null), d=asin(null),
		e=acos(null), f=atan(null), g=atan2(null,1), h=atan2(1,null),
		i=degrees(null), j=radians(null)`)
	for i, name := range []string{"sin", "cos", "tan", "asin", "acos", "atan",
		"atan2(null,1)", "atan2(1,null)", "degrees", "radians"} {
		if result.Rows[0][i] != nil {
			t.Errorf("%s(null) = %v, want nil", name, result.Rows[0][i])
		}
	}
}

// TestSeriesTrigMatchesScalarTrig confirms the series_* trig family
// (func_series.go) and the scalar trig functions (func_convert.go)
// now share the same underlying trigXxx implementation and can't
// silently drift apart — added 2026-08-17 alongside wiring series_*
// to call the new scalar helpers instead of math.Sin/math.Cos/etc
// directly.
func TestSeriesTrigMatchesScalarTrig(t *testing.T) {
	result := queryResult(t, `print scalarCos = cos(0.5), seriesCos = series_cos(dynamic([0.5]))`)
	scalar := result.Rows[0][0].(float64)
	arr := seriesJSONArray(t, result.Rows[0][1].(string))
	series := arr[0].(float64)
	if !approxEqual(scalar, series) {
		t.Errorf("cos(0.5) = %v but series_cos([0.5])[0] = %v — should match exactly", scalar, series)
	}
}

