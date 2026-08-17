package parser

import "testing"

// normalize_leading_keyword_test.go — normalizeLeadingKeywordWhitespace,
// found and fixed 2026-08-17. Every keyword-prefixed operator/statement
// dispatch in this parser is written as a literal
// `strings.HasPrefix(lower, "keyword ")` check, correct for the
// overwhelmingly common case but wrong whenever a query is formatted
// with a line break (or a tab) right after the keyword instead of a
// plain space — first noticed as a print-specific quirk, then
// confirmed live to be systemic (where and extend have the identical
// failure). Fixed once, at the two real entry points
// (parseOperator, parseQuery), rather than patching each of the 40+
// individual keyword-prefix checks.

func TestNormalizeLeadingKeywordWhitespace(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"already single space, unchanged", "where x > 1", "where x > 1"},
		{"newline after keyword", "where\n\tx > 1", "where x > 1"},
		{"tab after keyword", "where\tx > 1", "where x > 1"},
		{"multiple spaces after keyword", "where    x > 1", "where x > 1"},
		{"hyphenated keyword", "project-away\ny", "project-away y"},
		{"no trailing content at all (bare keyword)", "count", "count"},
		{"empty string", "", ""},
		{"whitespace only, no leading keyword token", "   ", "   "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeLeadingKeywordWhitespace(c.in)
			if got != c.want {
				t.Errorf("normalizeLeadingKeywordWhitespace(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

