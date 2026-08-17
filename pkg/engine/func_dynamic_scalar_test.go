package engine

import "testing"

// func_dynamic_scalar_test.go — the runtime dynamic(Expr) evaluator,
// added 2026-08-17 to fix a real, live bug: dynamic() only worked for
// a bare JSON array/object literal ([...]/{...}), handled entirely at
// parse time; any other argument shape (null, a string, a number, an
// already-dynamic expression) produced a FuncCall with no runtime
// evaluator anywhere in the dispatch chain, failing with "unsupported
// function: dynamic" — both dynamic(null) and dynamic("a string") are
// real, valid, documented KQL (real ADX's own dynamic data type docs).

func TestDynamicNullPropagatesAsNil(t *testing.T) {
	result := queryResult(t, `print r = dynamic(null)`)
	if result.Rows[0][0] != nil {
		t.Errorf("dynamic(null) = %v, want nil (matches every other null-propagating function's own convention, not the JSON text \"null\")", result.Rows[0][0])
	}
}

func TestDynamicWrapsString(t *testing.T) {
	result := queryResult(t, `print r = dynamic("just a string")`)
	if result.Rows[0][0] != `"just a string"` {
		t.Errorf(`dynamic("just a string") = %q, want %q (JSON-encoded, quoted)`, result.Rows[0][0], `"just a string"`)
	}
}

func TestDynamicWrapsNumericTypes(t *testing.T) {
	result := queryResult(t, `print a = dynamic(42), b = dynamic(3.14), c = dynamic(true)`)
	aIdx := result.Schema.ColumnIndex("a")
	bIdx := result.Schema.ColumnIndex("b")
	cIdx := result.Schema.ColumnIndex("c")
	if result.Rows[0][aIdx] != "42" {
		t.Errorf("dynamic(42) = %v, want \"42\" (bare JSON number, not quoted)", result.Rows[0][aIdx])
	}
	if result.Rows[0][bIdx] != "3.14" {
		t.Errorf("dynamic(3.14) = %v, want \"3.14\"", result.Rows[0][bIdx])
	}
	if result.Rows[0][cIdx] != "true" {
		t.Errorf("dynamic(true) = %v, want \"true\"", result.Rows[0][cIdx])
	}
}

// TestDynamicWrapsDatetimeAsQuotedString guards real ADX's own
// documented rule for dynamic(): a type JSON can't natively represent
// (datetime, long, real, timespan, guid) gets serialized as a JSON
// STRING, matching valueForJSONArray's own already-established
// datetime formatting (make_series.go) — reused here, not duplicated.
func TestDynamicWrapsDatetimeAsQuotedString(t *testing.T) {
	result := queryResult(t, `print r = dynamic(datetime(2020-01-01))`)
	want := `"2020-01-01T00:00:00.0000000Z"`
	if result.Rows[0][0] != want {
		t.Errorf("dynamic(datetime(2020-01-01)) = %v, want %v", result.Rows[0][0], want)
	}
}

// TestDynamicPassesThroughAlreadyDynamicValue confirms wrapping an
// already-dynamic expression doesn't double-encode it — a JSON array
// or object passed to dynamic() must come out unchanged, not turned
// into a JSON string CONTAINING that array/object's text.
func TestDynamicPassesThroughAlreadyDynamicValue(t *testing.T) {
	result := queryResult(t, `print r = dynamic(dynamic([1,2,3]))`)
	if result.Rows[0][0] != "[1,2,3]" {
		t.Errorf("dynamic(dynamic([1,2,3])) = %v, want [1,2,3] (unchanged, not double-encoded)", result.Rows[0][0])
	}
}

// TestDynamicBareArrayLiteralStillWorks is a regression guard for the
// pre-existing, parse-time-only fast path (tryConsumeBareJSONLiteral,
// expr.go) — confirming the new runtime evaluator above didn't
// accidentally change or bypass it for the common case.
func TestDynamicBareArrayLiteralStillWorks(t *testing.T) {
	result := queryResult(t, `print r = dynamic([1,2,3])`)
	if result.Rows[0][0] != "[1,2,3]" {
		t.Errorf("dynamic([1,2,3]) = %v, want [1,2,3]", result.Rows[0][0])
	}
}

