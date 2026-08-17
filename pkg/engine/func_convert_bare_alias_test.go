package engine

import "testing"

// func_convert_bare_alias_test.go — the bare int()/long()/real()/
// double()/bool() type-cast aliases added 2026-08-15 while wiring up
// the make-series + series_fill_* integration (real ADX's own
// documented default=double(null) idiom needed double() to exist at
// all, and to correctly propagate null rather than silently returning
// 0.0/0 — a real, separate, pre-existing bug in toreal/todouble/toint/
// tolong found and fixed alongside adding these aliases).

// TestBareRealDoubleAliases confirms the new bare forms work and
// match their "to"-prefixed counterparts exactly.
func TestBareRealDoubleAliases(t *testing.T) {
	result := queryResult(t, `print a = real(5), b = double(5), c = long(3.7), d = int(3.7), e = bool(1)`)
	if result.Rows[0][0].(float64) != 5 {
		t.Errorf("real(5) = %v, want 5", result.Rows[0][0])
	}
	if result.Rows[0][1].(float64) != 5 {
		t.Errorf("double(5) = %v, want 5", result.Rows[0][1])
	}
	if result.Rows[0][2].(int64) != 3 {
		t.Errorf("long(3.7) = %v, want 3", result.Rows[0][2])
	}
	if result.Rows[0][3].(int64) != 3 {
		t.Errorf("int(3.7) = %v, want 3", result.Rows[0][3])
	}
	if result.Rows[0][4].(bool) != true {
		t.Errorf("bool(1) = %v, want true", result.Rows[0][4])
	}
}

// TestConversionFunctionsPropagateNull is the regression guard for the
// real bug found alongside these aliases: toreal(null)/todouble(null)/
// toint(null)/tolong(null) (and the new bare forms) previously
// silently returned 0/0.0 instead of null — breaking real ADX's own
// documented default=double(null) idiom, whose entire point is
// distinguishing "no data" from "a real value of zero."
func TestConversionFunctionsPropagateNull(t *testing.T) {
	result := queryResult(t, `print a = toreal(null), b = todouble(null), c = toint(null), d = tolong(null),
		e = real(null), f = double(null), g = int(null), h = long(null)`)
	for i, name := range []string{"toreal", "todouble", "toint", "tolong", "real", "double", "int", "long"} {
		if result.Rows[0][i] != nil {
			t.Errorf("%s(null) = %v, want nil", name, result.Rows[0][i])
		}
	}
}

// TestBareAliasesYieldToUserDefinedFunction is the regression guard
// for the real collision this session's own TestUDFBasic caught
// immediately: a user-defined scalar function named the same as one
// of these new bare aliases (e.g. `let double = (x: long) { x * 2 }`)
// must still be callable as the user's own function, not silently
// shadowed by the new built-in.
func TestBareAliasesYieldToUserDefinedFunction(t *testing.T) {
	result := queryResult(t, `let double = (x: long) { x * 2 }; print R = double(21)`)
	got, ok := result.Rows[0][0].(int64)
	if !ok || got != 42 {
		t.Fatalf("double(21) with a user-defined 'double' = %v (%T), want int64(42) — the user function must win", result.Rows[0][0], result.Rows[0][0])
	}
}

// TestMakeSeriesSeriesFillForwardIntegration is the actual real-world
// pattern that surfaced both bugs this file guards: make-series's own
// documented default=double(null) idiom, piped directly into
// series_fill_forward — the reason series_fill_* exists at all.
func TestMakeSeriesSeriesFillForwardIntegration(t *testing.T) {
	result := queryResult(t, `range x from 1 to 5 step 1
		| make-series avg(x) default=double(null) on x from 1 to 6 step 1
		| extend filled = series_fill_forward(avg_x)`)
	filledIdx := result.Schema.ColumnIndex("filled")
	arr := seriesJSONArray(t, result.Rows[0][filledIdx].(string))
	want := []float64{1, 2, 3, 4, 5}
	if len(arr) != len(want) {
		t.Fatalf("length = %d, want %d: %v", len(arr), len(want), arr)
	}
	for i, w := range want {
		if arr[i].(float64) != w {
			t.Errorf("elem %d = %v, want %v", i, arr[i], w)
		}
	}
}

