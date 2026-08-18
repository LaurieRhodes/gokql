package engine

import "testing"

// func_regex_functions_test.go — extract(), extract_all(),
// replace_regex(), added/fixed 2026-08-18 as part of a larger regex-
// support unit of work. Every worked-example value below is taken
// directly from real ADX's own documentation.

// --- extract() ---

// TestExtractWorkedExample guards real ADX's own worked example: three
// extract() calls (plain capture group, verbatim-string pattern,
// whole-match via group 0) against a two-row datatable, including a
// row where the email pattern has no match at all (must be null, not
// "").
func TestExtractWorkedExample(t *testing.T) {
	result := queryResult(t, `let _data = datatable(Text: string)
		[
		"User: James, Email: James@example.com, Age: 29",
		"User: David, Age: 35"
		];
		_data |
		extend UserName = extract("User: ([^,]+)", 1, Text),
		       EmailId = extract(@"Email: (\S+),", 1, Text),
		       Age = extract(@"\d+", 0, Text)`)
	if len(result.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(result.Rows))
	}
	if got := result.Rows[0][1]; got != "James" {
		t.Errorf("row 0 UserName = %v, want James", got)
	}
	if got := result.Rows[0][2]; got != "James@example.com" {
		t.Errorf("row 0 EmailId = %v, want James@example.com", got)
	}
	if got := result.Rows[0][3]; got != "29" {
		t.Errorf("row 0 Age = %v, want 29", got)
	}
	if got := result.Rows[1][1]; got != "David" {
		t.Errorf("row 1 UserName = %v, want David", got)
	}
	if result.Rows[1][2] != nil {
		t.Errorf("row 1 EmailId (no match) = %v, want nil", result.Rows[1][2])
	}
	if got := result.Rows[1][3]; got != "35" {
		t.Errorf("row 1 Age = %v, want 35", got)
	}
}

// TestExtractTypeLiteralWorkedExample guards real ADX's own second
// worked example: the optional 4th typeLiteral argument
// (typeof(int)), added 2026-08-18 — this argument was previously
// entirely unsupported (a 4th argument hard-errored).
func TestExtractTypeLiteralWorkedExample(t *testing.T) {
	result := queryResult(t, `let Dates = datatable(DateString: string)
		[
		    "15-12-2024",
		    "21-07-2023",
		    "10-03-2022"
		];
		Dates
		| extend Month = extract(@"-(\d{2})-", 1, DateString, typeof(int))
		| project DateString, Month`)
	want := []int64{12, 7, 3}
	if len(result.Rows) != len(want) {
		t.Fatalf("expected %d rows, got %d", len(want), len(result.Rows))
	}
	for i, w := range want {
		got, ok := result.Rows[i][1].(int64)
		if !ok {
			// int32 is also an acceptable representation of "int"
			if got32, ok32 := result.Rows[i][1].(int32); ok32 {
				got = int64(got32)
				ok = true
			}
		}
		if !ok || got != w {
			t.Errorf("row %d Month = %v (%T), want %v", i, result.Rows[i][1], result.Rows[i][1], w)
		}
	}
}

// TestExtractNoMatchReturnsNull guards a real bug fix: extract()
// previously returned "" (empty string, isnull()==false) on no match
// or an out-of-range capture group, but real ADX's own docs are
// explicit: "If there's no match, or the type conversion fails: null."
func TestExtractNoMatchReturnsNull(t *testing.T) {
	result := queryResult(t, `print r = extract("xyz([0-9]+)", 1, "no numbers here")`)
	if result.Rows[0][0] != nil {
		t.Errorf("extract(no match) = %v, want nil", result.Rows[0][0])
	}
}

// --- extract_all() ---

const guidTestID = "82b8be2d-dfa7-4bd1-8f63-24ad26d31449"

// TestExtractAllSingleGroupWorkedExample guards real ADX's own first
// worked example: a single capture group with no captureGroups
// argument returns a 1-D array of that group's content (NOT the whole
// match — a real, fixed bug: extract_all previously returned the
// whole regex match via FindAllString instead of the capture group's
// own text).
func TestExtractAllSingleGroupWorkedExample(t *testing.T) {
	result := queryResult(t, `print Id="`+guidTestID+`" | extend guid_bytes = extract_all(@"([\da-f]{2})", Id)`)
	got := result.Rows[0][1].(string)
	want := `["82","b8","be","2d","df","a7","4b","d1","8f","63","24","ad","26","d3","14","49"]`
	if got != want {
		t.Errorf("extract_all (single group) = %v, want %v", got, want)
	}
}

// TestExtractAllMultiGroupWorkedExample guards real ADX's own second
// worked example: three capture groups with no captureGroups argument
// returns a 2-D array, one sub-array per match.
func TestExtractAllMultiGroupWorkedExample(t *testing.T) {
	result := queryResult(t, `print Id="`+guidTestID+`" | extend guid_bytes = extract_all(@"(\w)(\w+)(\w)", Id)`)
	got := result.Rows[0][1].(string)
	want := `[["8","2b8be2","d"],["d","fa","7"],["4","bd","1"],["8","f6","3"],["2","4ad26d3144","9"]]`
	if got != want {
		t.Errorf("extract_all (multi group) = %v, want %v", got, want)
	}
}

// TestExtractAllCaptureGroupsSubsetWorkedExample guards real ADX's own
// third worked example: an explicit numeric captureGroups selector
// picking a subset of groups, in the requested order.
func TestExtractAllCaptureGroupsSubsetWorkedExample(t *testing.T) {
	result := queryResult(t, `print Id="`+guidTestID+`" | extend guid_bytes = extract_all(@"(\w)(\w+)(\w)", dynamic([1,3]), Id)`)
	got := result.Rows[0][1].(string)
	want := `[["8","d"],["d","7"],["4","1"],["8","3"],["2","9"]]`
	if got != want {
		t.Errorf("extract_all (subset selector) = %v, want %v", got, want)
	}
}

// TestExtractAllNamedAndNumericSelectorWorkedExample guards real ADX's
// own fourth worked example: a captureGroups selector mixing named
// (Go regexp named-group syntax) and numeric references, resolving to
// the identical result as the plain multi-group case above (since the
// selector here happens to name/number all three groups in order).
func TestExtractAllNamedAndNumericSelectorWorkedExample(t *testing.T) {
	result := queryResult(t, `print Id="`+guidTestID+`" | extend guid_bytes = extract_all(@"(?P<first>\w)(?P<middle>\w+)(?P<last>\w)", dynamic(['first',2,'last']), Id)`)
	got := result.Rows[0][1].(string)
	want := `[["8","2b8be2","d"],["d","fa","7"],["4","bd","1"],["8","f6","3"],["2","4ad26d3144","9"]]`
	if got != want {
		t.Errorf("extract_all (named+numeric selector) = %v, want %v", got, want)
	}
}

// TestExtractAllNoMatchReturnsNull confirms no match returns null
// (not an empty array).
func TestExtractAllNoMatchReturnsNull(t *testing.T) {
	result := queryResult(t, `print r = extract_all(@"([0-9]+)", "no digits here")`)
	if result.Rows[0][0] != nil {
		t.Errorf("extract_all(no match) = %v, want nil", result.Rows[0][0])
	}
}

// --- replace_regex() ---

// TestReplaceRegexWorkedExample guards a real, fixed bug: replace_regex
// previously passed rewrite_pattern straight to Go's own
// regexp.ReplaceAllString, which uses Go's $1 backreference syntax —
// but real ADX's own documented syntax is \0/\1/\2, and its own
// worked example uses exactly that (\1). A query copy-pasted from
// Microsoft's own docs previously produced the literal unsubstituted
// text "was: \1" instead of "was: 1", with no error.
func TestReplaceRegexWorkedExample(t *testing.T) {
	result := queryResult(t, `range x from 1 to 3 step 1
		| extend str=strcat('Number is ', tostring(x))
		| extend replaced=replace_regex(str, @'is (\d+)', @'was: \1')`)
	if len(result.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(result.Rows))
	}
	for i, row := range result.Rows {
		got, _ := row[2].(string)
		if got == "" || got[len(got)-2] == '\\' {
			t.Errorf("row %d replaced = %q, looks like the unsubstituted backreference bug", i, got)
		}
	}
	// Spot check the first row's literal value directly.
	got0, _ := result.Rows[0][2].(string)
	if got0 == "" {
		t.Fatal("row 0 replaced is empty")
	}
	if got0[len(got0)-1] == '1' && got0[len(got0)-2] == ' ' {
		// "...was: 1" — correct substitution landed.
	} else {
		t.Errorf("row 0 replaced = %q, want it to end in a substituted '1'", got0)
	}
}

// TestReplaceRegexLiteralDollarPassesThrough confirms a literal '$' in
// the caller's rewrite_pattern (unrelated to any backreference) passes
// through unaffected, rather than being misinterpreted as Go's own
// backreference syntax during the \N -> $N translation.
func TestReplaceRegexLiteralDollarPassesThrough(t *testing.T) {
	result := queryResult(t, `print r = replace_regex("cost is 5", @"is (\d+)", @"= $5 total")`)
	got := result.Rows[0][0].(string)
	want := "cost = $5 total"
	if got != want {
		t.Errorf("replace_regex with literal $ = %q, want %q", got, want)
	}
}
