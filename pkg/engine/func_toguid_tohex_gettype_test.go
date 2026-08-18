package engine

import "testing"

// func_toguid_tohex_gettype_test.go — toguid(), tohex(), gettype(),
// added 2026-08-18. Every case below is taken directly from real
// ADX's own docs' worked examples. todecimal() is deliberately NOT
// implemented -- see scalar_function_backlog.md's own scope note
// (same root cause as parse_version: no decimal/big-integer type in
// this engine).

// --- toguid ---

func TestToGuidWorkedExample(t *testing.T) {
	result := queryResult(t, `datatable(str: string)
		[
		"0123456789abcdef0123456789abcdef",
		"0123456789ab-cdef-0123-456789abcdef",
		"a string that is not a guid"
		]
		| extend guid = toguid(str)`)
	if len(result.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(result.Rows))
	}
	want := "01234567-89ab-cdef-0123-456789abcdef"
	if got := result.Rows[0][1]; got != want {
		t.Errorf("row 0 guid = %v, want %v", got, want)
	}
	// Row 1's own hyphens are in NON-standard positions
	// (12-4-4-12, not 8-4-4-4-12) -- still converts successfully,
	// confirming hyphens are stripped wherever they occur, not
	// position-validated.
	if got := result.Rows[1][1]; got != want {
		t.Errorf("row 1 (non-standard hyphen placement) guid = %v, want %v", got, want)
	}
	if result.Rows[2][1] != nil {
		t.Errorf("row 2 (not a guid at all) guid = %v, want null", result.Rows[2][1])
	}
}

// --- tohex ---

func TestToHexPositiveNoMinLength(t *testing.T) {
	result := queryResult(t, `print r = tohex(256)`)
	if got := result.Rows[0][0]; got != "100" {
		t.Errorf("tohex(256) = %v, want \"100\"", got)
	}
}

// TestToHexNegativeLongDefaultWidth guards that a negative value with
// NO minLength argument still gets the full 64-bit two's-complement
// width for a long -- this is the exact case a first, buggy
// implementation got wrong (applying a positive-value's own lack of
// a floor to the negative case too, or vice versa).
func TestToHexNegativeLongDefaultWidth(t *testing.T) {
	result := queryResult(t, `print r = tohex(-256)`)
	want := "ffffffffffffff00"
	if got := result.Rows[0][0]; got != want {
		t.Errorf("tohex(-256) = %v, want %v", got, want)
	}
}

// TestToHexNegativeIntWidth guards the int-vs-long two's-complement
// width distinction, which depends on inferExprType correctly
// resolving toint()'s own static type -- a real, pre-existing bug
// (toint grouped with tolong in inferExprType) found and fixed while
// verifying this exact case.
func TestToHexNegativeIntWidth(t *testing.T) {
	result := queryResult(t, `print r = tohex(toint(-256), 8)`)
	want := "ffffff00"
	if got := result.Rows[0][0]; got != want {
		t.Errorf("tohex(toint(-256), 8) = %v, want %v", got, want)
	}
}

func TestToHexPositiveWithMinLengthPadding(t *testing.T) {
	result := queryResult(t, `print r = tohex(256, 8)`)
	want := "00000100"
	if got := result.Rows[0][0]; got != want {
		t.Errorf("tohex(256, 8) = %v, want %v", got, want)
	}
}

// TestToHexMinLengthIgnoredWhenExceeded guards that a minLength
// shorter than the natural representation is simply ignored, not
// used to truncate.
func TestToHexMinLengthIgnoredWhenExceeded(t *testing.T) {
	result := queryResult(t, `print r = tohex(256, 2)`)
	if got := result.Rows[0][0]; got != "100" {
		t.Errorf("tohex(256, 2) = %v, want \"100\"", got)
	}
}

// --- gettype ---

func TestGetTypeStaticBranch(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{`print r = gettype("a")`, "string"},
		{`print r = gettype(111)`, "long"},
		{`print r = gettype(1==1)`, "bool"},
		{`print r = gettype(now())`, "datetime"},
		{`print r = gettype(1s)`, "timespan"},
		{`print r = gettype(123.45)`, "real"},
		{`print r = gettype(toguid("12e8b78d-55b4-46ae-b068-26d7a0080254"))`, "guid"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			result := queryResult(t, tt.query)
			if got := result.Rows[0][0]; got != tt.want {
				t.Errorf("%s = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

// TestGetTypeDynamicBranch guards the "runtime shape of the parsed
// JSON" branch -- this is what surfaced a real, pre-existing bug
// (inferExprType had no case for parse_json/todynamic at all, so
// every one of these fell through to TypeString before the fix).
func TestGetTypeDynamicBranch(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{"whole number -> int", `print r = gettype(parse_json("1"))`, "int"},
		{"string -> string", `print r = gettype(parse_json(" ` + `\"abc\"` + ` "))`, "string"},
		{"object -> dictionary", `print r = gettype(parse_json(" {` + `\"abc\":1` + `} "))`, "dictionary"},
		{"array -> array", `print r = gettype(parse_json(" [1, 2, 3] "))`, "array"},
		{"empty string -> null", `print r = gettype(parse_json(""))`, "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := queryResult(t, tt.query)
			if got := result.Rows[0][0]; got != tt.want {
				t.Errorf("%s = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}
