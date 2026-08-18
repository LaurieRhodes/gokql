package engine

import "testing"

// parse_operator_rewrite_test.go — the parse/parse-where operators,
// substantially rewritten 2026-08-18. Guards real ADX's own worked
// examples (parse-operator.md) plus a dedicated regression test per
// real, reproduced bug found along the way. See operators.go's
// applyParseCore and buildSimpleRelaxedRegex for the full story behind
// each fix.

// --- kind=simple worked examples ---

// TestParseSimpleWorkedExample guards real ADX's own "Parse and
// extend results" worked example end to end: * wildcards, a field
// immediately followed by another field (totalSlices: long *
// "sliceNumber="), and typed long columns actually converting to
// numeric values — all three were real, separately reproduced bugs
// before this rewrite (see git history: colon-in-fieldname bug,
// wildcard-captured-as-field bug, field-followed-by-field bug).
// releaseTime/previousLockTime are declared :date in the real example
// but deliberately NOT asserted here as converted values — this
// engine's own datetime parser doesn't yet support the MM/DD/YYYY
// format the real example's data uses (todatetime("02/17/2016
// 08:40:01") fails independently of parse entirely, confirmed live
// before writing this test — a separate, pre-existing, out-of-scope
// gap, not something this rewrite introduced or was scoped to fix).
func TestParseSimpleWorkedExample(t *testing.T) {
	result := queryResult(t, `let Traces = datatable(EventText: string)
		[
		"Event: NotifySliceRelease (resourceName=PipelineScheduler, totalSlices=27, sliceNumber=23, lockTime=02/17/2016 08:40:01, releaseTime=02/17/2016 08:40:01, previousLockTime=02/17/2016 08:39:01)",
		"Event: NotifySliceRelease (resourceName=PipelineScheduler, totalSlices=27, sliceNumber=15, lockTime=02/17/2016 08:40:00, releaseTime=02/17/2016 08:40:00, previousLockTime=02/17/2016 08:39:00)"
		];
		Traces
		| parse EventText with * "resourceName=" resourceName ", totalSlices=" totalSlices: long * "sliceNumber=" sliceNumber: long * "lockTime=" lockTime ", releaseTime=" releaseTime: date "," * "previousLockTime=" previousLockTime: date ")" *
		| project resourceName, totalSlices, sliceNumber, lockTime`)
	if len(result.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(result.Rows))
	}
	wantResourceName := "PipelineScheduler"
	wantTotalSlices := int64(27)
	wantSliceNumbers := []int64{23, 15}
	wantLockTimes := []string{"02/17/2016 08:40:01", "02/17/2016 08:40:00"}
	for i := range result.Rows {
		if got := result.Rows[i][0]; got != wantResourceName {
			t.Errorf("row %d resourceName = %v, want %v", i, got, wantResourceName)
		}
		if got, ok := result.Rows[i][1].(int64); !ok || got != wantTotalSlices {
			t.Errorf("row %d totalSlices = %v (%T), want %v as int64", i, result.Rows[i][1], result.Rows[i][1], wantTotalSlices)
		}
		if got, ok := result.Rows[i][2].(int64); !ok || got != wantSliceNumbers[i] {
			t.Errorf("row %d sliceNumber = %v (%T), want %v as int64", i, result.Rows[i][2], result.Rows[i][2], wantSliceNumbers[i])
		}
		if got := result.Rows[i][3]; got != wantLockTimes[i] {
			t.Errorf("row %d lockTime = %v, want %v", i, got, wantLockTimes[i])
		}
	}
}

// TestParseSimpleFieldFollowedByFieldRegression is a focused
// regression test for the specific bug pattern found in the worked
// example above: a real, pre-existing bug where a field immediately
// followed by another field fragment (no literal between them, e.g.
// "totalSlices=" totalSlices * "sliceNumber=") lost the first field's
// value entirely. Reproduced live before fixing: totalSlices came out
// empty against exactly this shape of pattern.
func TestParseSimpleFieldFollowedByFieldRegression(t *testing.T) {
	result := queryResult(t, `print s = "totalSlices=27, sliceNumber=15"
		| parse s with "totalSlices=" totalSlices ", " * "sliceNumber=" sliceNumber`)
	if got := result.Rows[0][1]; got != "27" {
		t.Errorf("totalSlices = %v, want 27", got)
	}
	if got := result.Rows[0][2]; got != "15" {
		t.Errorf("sliceNumber = %v, want 15", got)
	}
}

// TestParseSimpleWildcardNotCapturedRegression is a focused regression
// test for a real, pre-existing bug: a "*" wildcard's own skipped
// text was mistakenly captured as if it were a real field's value,
// shifting every subsequent field-to-column assignment out of
// alignment. Reproduced live before fixing: resourceName captured the
// wildcard-skipped prefix text instead of the intended field value.
func TestParseSimpleWildcardNotCapturedRegression(t *testing.T) {
	result := queryResult(t, `print s = "junk before resourceName=Foo, rest"
		| parse s with * "resourceName=" resourceName ","`)
	if got := result.Rows[0][1]; got != "Foo" {
		t.Errorf("resourceName = %v, want Foo (not the wildcard-skipped junk)", got)
	}
}

// TestParseSimpleColonTypeAnnotationRegression is a focused regression
// test for a real, pre-existing bug present since this repository's
// first commit: the field-name scanner never stopped at ':', so
// "totalSlices: long" produced a column literally named "totalSlices:"
// plus a spurious second column named "long", and the type annotation
// was never actually applied at all.
func TestParseSimpleColonTypeAnnotationRegression(t *testing.T) {
	result := queryResult(t, `print s = "totalSlices=27"
		| parse s with "totalSlices=" totalSlices: long`)
	if len(result.Schema.Columns) != 2 {
		t.Fatalf("expected 2 columns (s, totalSlices), got %d: %v", len(result.Schema.Columns), result.Schema.Columns)
	}
	if result.Schema.Columns[1].Name != "totalSlices" {
		t.Errorf("column name = %q, want %q (no trailing colon, no spurious 'long' column)", result.Schema.Columns[1].Name, "totalSlices")
	}
	if got, ok := result.Rows[0][1].(int64); !ok || got != 27 {
		t.Errorf("totalSlices = %v (%T), want int64(27)", result.Rows[0][1], result.Rows[0][1])
	}
}

// TestParseWildcardOnlyLastFieldRegression guards the specific
// tension found while fixing the above: switching untyped fields and
// wildcards to greedy matching (to fix the "last field captures
// nothing" problem) would have broken this exact case, where a SHORT,
// common boundary literal (a single space) appears more than once in
// the remainder — greedy backtracking finds the LAST occurrence
// instead of the first. Reproduced live: Code captured "404 Not"
// instead of "404" under a naive all-greedy design.
func TestParseWildcardOnlyLastFieldRegression(t *testing.T) {
	result := queryResult(t, `datatable (S: string) ["Error: 404 Not Found", "Error: 500 Internal"]
		| parse S with "Error: " Code " " *`)
	if got := result.Rows[0][1]; got != "404" {
		t.Errorf("row 0 Code = %v, want 404", got)
	}
	if got := result.Rows[1][1]; got != "500" {
		t.Errorf("row 1 Code = %v, want 500", got)
	}
}

// TestParseLastFieldNoTrailingLiteralRegression is the companion
// regression test to the above: a field with genuinely NOTHING after
// it in the whole pattern must still capture the remainder greedily,
// not match empty (which a naive "always non-greedy" design would
// produce, since non-greedy prefers zero characters with nothing to
// force expansion).
func TestParseLastFieldNoTrailingLiteralRegression(t *testing.T) {
	result := queryResult(t, `datatable(EventText:string) ["totalSlices=27, sliceNumber=15"]
		| parse-where EventText with "totalSlices=" totalSlices ", sliceNumber=" sliceNumber
		| project totalSlices, sliceNumber`)
	if got := result.Rows[0][0]; got != "27" {
		t.Errorf("totalSlices = %v, want 27", got)
	}
	if got := result.Rows[0][1]; got != "15" {
		t.Errorf("sliceNumber = %v, want 15", got)
	}
}

// --- kind=regex worked examples ---

// TestParseKindRegexWorkedExample guards real ADX's own "Regex mode"
// worked example: literal fragments are raw regex snippets (not
// escaped text) in this mode, and — the key fix in this rewrite —
// capture groups are matched to fields by NAME, not raw positional
// index, since the example's own literal fragments contain their own
// embedded, unnamed capture groups. A prior version of this fix (using
// positional match[1], match[2], ...) reproduced a real bug here:
// resourceName's capture extended far past its own boundary, and
// sliceNumber (declared :long) silently came out null because its
// wildly-over-captured raw text failed numeric conversion.
func TestParseKindRegexWorkedExample(t *testing.T) {
	result := queryResult(t, `let Traces=datatable(EventText: string)
		[
		"Event: NotifySliceRelease (resourceName=PipelineScheduler, totalSlices=27, sliceNumber=23, lockTime=02/17/2016 08:40:01, releaseTime=02/17/2016 08:40:01, previousLockTime=02/17/2016 08:39:01)",
		"Event: NotifySliceRelease (resourceName=PipelineScheduler, totalSlices=27, sliceNumber=15, lockTime=02/17/2016 08:40:00, releaseTime=02/17/2016 08:40:00, previousLockTime=02/17/2016 08:39:00)"
		];
		Traces
		| parse kind=regex EventText with "(.*?)[a-zA-Z]*=" resourceName @", totalSlices=\s*\d+\s*.*?sliceNumber=" sliceNumber: long ".*?(previous)?lockTime=" lockTime ".*?releaseTime=" releaseTime ".*?previousLockTime=" previousLockTime: date "\\)"
		| project resourceName, sliceNumber, lockTime, releaseTime`)
	if len(result.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(result.Rows))
	}
	wantSliceNumbers := []int64{23, 15}
	wantLockTimes := []string{"02/17/2016 08:40:01, ", "02/17/2016 08:40:00, "}
	wantReleaseTimes := []string{"02/17/2016 08:40:01, ", "02/17/2016 08:40:00, "}
	for i := range result.Rows {
		if got := result.Rows[i][0]; got != "PipelineScheduler" {
			t.Errorf("row %d resourceName = %v, want PipelineScheduler", i, got)
		}
		if got, ok := result.Rows[i][1].(int64); !ok || got != wantSliceNumbers[i] {
			t.Errorf("row %d sliceNumber = %v (%T), want %v as int64", i, result.Rows[i][1], result.Rows[i][1], wantSliceNumbers[i])
		}
		if got := result.Rows[i][2]; got != wantLockTimes[i] {
			t.Errorf("row %d lockTime = %v, want %v", i, got, wantLockTimes[i])
		}
		if got := result.Rows[i][3]; got != wantReleaseTimes[i] {
			t.Errorf("row %d releaseTime = %v, want %v", i, got, wantReleaseTimes[i])
		}
	}
}

// TestParseKindRegexFlagsDefaultGreedyWorkedExample guards real ADX's
// own "Regex mode with regex flags" worked example: with NO flags,
// the default is GREEDY (matching the real docs' own stated behavior
// — "unexpected, and include full event data since the default mode
// is greedy" — which directly contradicts a separate, abbreviated,
// unverifiable snippet elsewhere on the same real docs page claiming
// non-greedy ".*?" is the default; the fully worked, checkable example
// was trusted over the ambiguous one).
func TestParseKindRegexFlagsDefaultGreedyWorkedExample(t *testing.T) {
	result := queryResult(t, `let Traces=datatable(EventText: string)
		["Event: NotifySliceRelease (resourceName=PipelineScheduler, totalSlices=27, sliceNumber=23, lockTime=02/17/2016 08:40:01, releaseTime=02/17/2016 08:40:01, previousLockTime=02/17/2016 08:39:01)"];
		Traces
		| parse kind=regex EventText with * "resourceName=" resourceName ',' *
		| project resourceName`)
	want := "PipelineScheduler, totalSlices=27, sliceNumber=23, lockTime=02/17/2016 08:40:01, releaseTime=02/17/2016 08:40:01"
	if got := result.Rows[0][0]; got != want {
		t.Errorf("resourceName (default flags, greedy) = %v, want %v", got, want)
	}
}

// TestParseKindRegexFlagsUiWorkedExample guards real ADX's own
// worked example with flags=Ui (ungreedy + case-insensitive).
func TestParseKindRegexFlagsUiWorkedExample(t *testing.T) {
	result := queryResult(t, `let Traces=datatable(EventText: string)
		["Event: NotifySliceRelease (resourceName=PipelineScheduler, totalSlices=27, sliceNumber=23, lockTime=02/17/2016 08:40:01, releaseTime=02/17/2016 08:40:01, previousLockTime=02/17/2016 08:39:01)"];
		Traces
		| parse kind=regex flags=Ui EventText with * "RESOURCENAME=" resourceName ',' *
		| project resourceName`)
	if got := result.Rows[0][0]; got != "PipelineScheduler" {
		t.Errorf("resourceName (flags=Ui) = %v, want PipelineScheduler", got)
	}
}

// TestParseKindRegexVerbatimAndEscapeRegression is a focused
// regression test for two real, reproduced bugs found while verifying
// kind=regex: (1) the pattern clause's own literal scanner previously
// had NO @"..." verbatim-string support at all; (2) it also never
// processed backslash escapes in regular quoted literals, so "\\)"
// (KQL source for one literal backslash + a close-paren) passed
// through as two raw, uncollapsed backslash characters, corrupting
// the generated regex ("unexpected )" compile error) instead of
// producing a properly escaped ")".
func TestParseKindRegexVerbatimAndEscapeRegression(t *testing.T) {
	result := queryResult(t, `print s = "value(42)"
		| parse kind=regex s with @"value\(" num: long "\\)"`)
	if got, ok := result.Rows[0][1].(int64); !ok || got != 42 {
		t.Errorf("num = %v (%T), want int64(42)", result.Rows[0][1], result.Rows[0][1])
	}
}

// TestParseKindRegexNamedGroupPositionalRegression is a focused
// regression test for the core bug this rewrite's kind=regex matching
// fix addresses: a literal fragment containing its OWN embedded,
// unnamed capture group must not shift which match-group index a
// LATER field is read from.
func TestParseKindRegexNamedGroupPositionalRegression(t *testing.T) {
	result := queryResult(t, `print s = "a=1 b=2"
		| parse kind=regex s with "(x?)a=" first: long " (y?)b=" second: long`)
	if got, ok := result.Rows[0][1].(int64); !ok || got != 1 {
		t.Errorf("first = %v (%T), want int64(1)", result.Rows[0][1], result.Rows[0][1])
	}
	if got, ok := result.Rows[0][2].(int64); !ok || got != 2 {
		t.Errorf("second = %v (%T), want int64(2)", result.Rows[0][2], result.Rows[0][2])
	}
}
