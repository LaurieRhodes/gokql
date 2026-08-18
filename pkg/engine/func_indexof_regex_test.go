package engine

import "testing"

// func_indexof_regex_test.go — indexof_regex(), added 2026-08-18.
// Every case below is taken directly from real ADX's own
// indexof-regex-function.md worked example (fetched, not assumed to
// mirror indexof()'s own semantics), including its exact output row:
// idx1=0, idx2=3, idx3=-1, idx4=-1, idx5=null.

// TestIndexofRegexWorkedExample guards the real worked example end to
// end, in one query, exactly as documented.
func TestIndexofRegexWorkedExample(t *testing.T) {
	result := queryResult(t, `print
		idx1 = indexof_regex("abcabc", @"a.c"),
		idx2 = indexof_regex("abcabcdefg", @"a.c", 0, 9, 2),
		idx3 = indexof_regex("abcabc", @"a.c", 1, -1, 2),
		idx4 = indexof_regex("ababaa", @"a.a", 0, -1, 2),
		idx5 = indexof_regex("abcabc", @"a|ab", -1)`)
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	row := result.Rows[0]
	wantIdx1 := int64(0)
	wantIdx2 := int64(3)
	wantIdx3 := int64(-1)
	wantIdx4 := int64(-1)
	if got, ok := row[0].(int64); !ok || got != wantIdx1 {
		t.Errorf("idx1 = %v (%T), want %v", row[0], row[0], wantIdx1)
	}
	if got, ok := row[1].(int64); !ok || got != wantIdx2 {
		t.Errorf("idx2 = %v (%T), want %v", row[1], row[1], wantIdx2)
	}
	if got, ok := row[2].(int64); !ok || got != wantIdx3 {
		t.Errorf("idx3 = %v (%T), want %v", row[2], row[2], wantIdx3)
	}
	if got, ok := row[3].(int64); !ok || got != wantIdx4 {
		t.Errorf("idx4 = %v (%T), want %v", row[3], row[3], wantIdx4)
	}
	if row[4] != nil {
		t.Errorf("idx5 = %v, want null (negative start argument)", row[4])
	}
}

// TestIndexofRegexNoArgsMatch guards the simplest call shape (2 args,
// all optionals defaulted) beyond the worked example's own idx1 case:
// no match at all must return -1, not null or an error.
func TestIndexofRegexNoMatch(t *testing.T) {
	result := queryResult(t, `print r = indexof_regex("hello world", @"\d+")`)
	if got := result.Rows[0][0]; got != int64(-1) {
		t.Errorf("r = %v, want -1", got)
	}
}

// TestIndexofRegexNullPropagation guards that a null source or null
// match argument propagates to null, not an error, matching this
// engine's own established null-handling convention for string
// functions elsewhere (e.g. parse_ipv6, parse_command_line).
func TestIndexofRegexNullPropagation(t *testing.T) {
	result := queryResult(t, `print a = indexof_regex(dynamic(null), @"a"), b = indexof_regex("abc", dynamic(null))`)
	if result.Rows[0][0] != nil {
		t.Errorf("a (null source) = %v, want null", result.Rows[0][0])
	}
	if result.Rows[0][1] != nil {
		t.Errorf("b (null match) = %v, want null", result.Rows[0][1])
	}
}

// TestIndexofRegexOccurrenceAndLengthOutOfDocumentedRange guards the
// Returns section's other two documented null conditions (occurrence <
// 0, length < -1) -- not exercised by the worked example's own 5
// cases, but stated plainly in the same Returns section as the
// negative-start rule that IS worked-example-verified, and nothing in
// the docs contradicts them the way the start/Parameters prose did.
func TestIndexofRegexOccurrenceAndLengthOutOfDocumentedRange(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"negative occurrence", `print r = indexof_regex("abcabc", @"a.c", 0, -1, -1)`},
		{"length less than -1", `print r = indexof_regex("abcabc", @"a.c", 0, -2)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := queryResult(t, tt.query)
			if result.Rows[0][0] != nil {
				t.Errorf("r = %v, want null", result.Rows[0][0])
			}
		})
	}
}

// TestIndexofRegexOccurrenceBeyondMatchCount guards a case not in the
// worked example but a natural extension of its idx3/idx4 pattern:
// requesting an occurrence number higher than the number of actual
// non-overlapping matches returns -1, not an out-of-range panic.
func TestIndexofRegexOccurrenceBeyondMatchCount(t *testing.T) {
	result := queryResult(t, `print r = indexof_regex("abc", @"a.c", 0, -1, 5)`)
	if got := result.Rows[0][0]; got != int64(-1) {
		t.Errorf("r = %v, want -1", got)
	}
}
