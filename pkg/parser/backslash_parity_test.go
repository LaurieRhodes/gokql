package parser

import "testing"

// backslash_parity_test.go — precededByOddBackslashes and the three
// quote-scanning functions that depend on it (splitPipe,
// splitRespectingParens, findKeyword). Found and fixed while
// implementing parse-kv (2026-08-15): a naive "preceded by a single
// backslash" check for an escaped quote gets a doubled '\\' (KQL's own
// escape for one literal backslash, e.g. parse-kv's escape='\\' option)
// wrong — the byte right before the quote IS a backslash, but that
// backslash is itself escaped by the one before it, so the quote should
// close, not stay open. Before the fix, splitPipe silently swallowed a
// trailing pipe segment into the previous one whenever a string literal
// ended in '\\' immediately before its closing quote.

func TestSplitPipeDoubledBackslashClosesQuote(t *testing.T) {
	// The string literal is 'a\\' -- a single literal backslash,
	// properly escaped -- followed by a real, later pipe stage. Before
	// the fix, this incorrectly produced one part (the trailing
	// "count" segment swallowed into the first).
	parts := splitPipe(`print x='a\\' | count`)
	if len(parts) != 2 {
		t.Fatalf("splitPipe: expected 2 segments, got %d: %#v", len(parts), parts)
	}
}

func TestSplitPipeSingleBackslashKeepsQuoteOpen(t *testing.T) {
	// Contrast case: a single (unescaped-pair) backslash immediately
	// before the quote SHOULD keep the string open, since it's
	// escaping that very quote character. The '|' inside must NOT be
	// treated as a pipe boundary.
	parts := splitPipe(`print x='a\' | count'`)
	if len(parts) != 1 {
		t.Fatalf("splitPipe: expected 1 segment (quote kept open by escaped char), got %d: %#v", len(parts), parts)
	}
}

func TestSplitRespectingParensDoubledBackslash(t *testing.T) {
	parts := splitRespectingParens(`'a\\',b`, ',')
	if len(parts) != 2 {
		t.Fatalf("splitRespectingParens: expected 2 parts, got %d: %#v", len(parts), parts)
	}
}

func TestPrecededByOddBackslashesParity(t *testing.T) {
	cases := []struct {
		s    string
		i    int
		want bool
	}{
		{`a'`, 1, false},   // no backslash at all
		{`a\'`, 2, true},   // one backslash: escaped
		{`a\\'`, 3, false}, // two backslashes: NOT escaped (they escape each other)
		{`a\\\'`, 4, true}, // three backslashes: escaped
	}
	for _, c := range cases {
		if got := precededByOddBackslashes(c.s, c.i); got != c.want {
			t.Errorf("precededByOddBackslashes(%q, %d) = %v, want %v", c.s, c.i, got, c.want)
		}
	}
}

