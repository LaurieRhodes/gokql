package engine

import "testing"

// func_string_misc_test.go — repeat, regex_quote, replace_strings,
// unicode_codepoints_from_string/to_string, isascii, isutf8, added
// 2026-08-18. Every case below is taken directly from real ADX's own
// docs' worked examples.

// --- repeat ---

func TestRepeatWorkedExample(t *testing.T) {
	result := queryResult(t, `print r = repeat(1, 3)`)
	if got := result.Rows[0][0]; got != `[1,1,1]` {
		t.Errorf("repeat(1, 3) = %v, want [1,1,1]", got)
	}
}

func TestRepeatZeroCountEmptyArray(t *testing.T) {
	result := queryResult(t, `print r = repeat(1, 0)`)
	if got := result.Rows[0][0]; got != `[]` {
		t.Errorf("repeat(1, 0) = %v, want []", got)
	}
}

func TestRepeatNegativeCountNull(t *testing.T) {
	result := queryResult(t, `print r = repeat(1, -1)`)
	if result.Rows[0][0] != nil {
		t.Errorf("repeat(1, -1) = %v, want null", result.Rows[0][0])
	}
}

// --- regex_quote ---

func TestRegexQuoteWorkedExample(t *testing.T) {
	result := queryResult(t, `print result = regex_quote('(so$me.Te^xt)')`)
	want := `\(so\$me\.Te\^xt\)`
	if got := result.Rows[0][0]; got != want {
		t.Errorf("regex_quote(...) = %v, want %v", got, want)
	}
}

// --- replace_strings ---

func TestReplaceStringsSimple(t *testing.T) {
	result := queryResult(t, `print Message="A magic trick can turn a cat into a dog"
		| extend Outcome = replace_strings(Message, dynamic(["cat","dog"]), dynamic(["dog","pigeon"]))`)
	want := "A magic trick can turn a dog into a pigeon"
	if got := result.Rows[0][1]; got != want {
		t.Errorf("Outcome = %v, want %v", got, want)
	}
}

func TestReplaceStringsEmptyStringRemoves(t *testing.T) {
	result := queryResult(t, `print Message="A magic trick can turn a cat into a dog"
		| extend Outcome = replace_strings(Message, dynamic(["turn"," into a dog"]), dynamic(["disappear",""]))`)
	want := "A magic trick can disappear a cat"
	if got := result.Rows[0][1]; got != want {
		t.Errorf("Outcome = %v, want %v", got, want)
	}
}

// TestReplaceStringsOrderMatters guards the single most important,
// least obvious behavior: identical lookups/rewrites in a DIFFERENT
// order produce genuinely different output, because each lookup's own
// matches PROTECT that span of text from every later lookup. A naive
// "try each lookup at each position, first match wins" single-scan
// algorithm does NOT reproduce this pair correctly (confirmed by hand
// -tracing before choosing the sequential-passes-with-protection
// design actually implemented).
func TestReplaceStringsOrderMatters(t *testing.T) {
	result := queryResult(t, `print Message="This is an example of using replace_strings()"
		| extend Outcome1 = replace_strings(Message, dynamic(["This","is"]), dynamic(["This","was"])),
		         Outcome2 = replace_strings(Message, dynamic(["is","This"]), dynamic(["was","This"]))`)
	want1 := "This was an example of using replace_strings()"
	want2 := "Thwas was an example of using replace_strings()"
	if got := result.Rows[0][1]; got != want1 {
		t.Errorf("Outcome1 = %v, want %v", got, want1)
	}
	if got := result.Rows[0][2]; got != want2 {
		t.Errorf("Outcome2 = %v, want %v", got, want2)
	}
}

// TestReplaceStringsNonStringRewriteKeepsOriginal guards real ADX's
// own documented behavior: a matched span whose corresponding rewrite
// element isn't a string is left unchanged (not removed, not an
// error) -- but the match still counts as consumed, so a LATER lookup
// can't re-match inside it.
func TestReplaceStringsNonStringRewriteKeepsOriginal(t *testing.T) {
	result := queryResult(t, `print Message="This is an example of using replace_strings()"
		| extend Outcome = replace_strings(Message, dynamic(["This","is"]), dynamic([12345,"was"]))`)
	want := "This was an example of using replace_strings()"
	if got := result.Rows[0][1]; got != want {
		t.Errorf("Outcome = %v, want %v", got, want)
	}
}

// --- unicode_codepoints_from_string / unicode_codepoints_to_string ---

func TestUnicodeCodepointsFromStringNonAsciiSymbols(t *testing.T) {
	result := queryResult(t, `print arr = unicode_codepoints_from_string("⒦⒰⒮⒯⒪")`)
	want := `[9382,9392,9390,9391,9386]`
	if got := result.Rows[0][0]; got != want {
		t.Errorf("unicode_codepoints_from_string(...) = %v, want %v", got, want)
	}
}

func TestUnicodeCodepointsFromStringMixedScript(t *testing.T) {
	result := queryResult(t, `print arr = unicode_codepoints_from_string("קוסטו - Kusto")`)
	want := `[1511,1493,1505,1496,1493,32,45,32,75,117,115,116,111]`
	if got := result.Rows[0][0]; got != want {
		t.Errorf("unicode_codepoints_from_string(...) = %v, want %v", got, want)
	}
}

func TestUnicodeCodepointsRoundTrip(t *testing.T) {
	result := queryResult(t, `print str = unicode_codepoints_to_string(unicode_codepoints_from_string("Kusto"))`)
	if got := result.Rows[0][0]; got != "Kusto" {
		t.Errorf("round trip = %v, want \"Kusto\"", got)
	}
}

// TestToUtf8DeprecatedAlias guards that to_utf8() (real ADX's own
// documented deprecated alias) produces the same result as
// unicode_codepoints_from_string().
func TestToUtf8DeprecatedAlias(t *testing.T) {
	result := queryResult(t, `print a = unicode_codepoints_from_string("Kusto"), b = to_utf8("Kusto")`)
	if result.Rows[0][0] != result.Rows[0][1] {
		t.Errorf("unicode_codepoints_from_string = %v, to_utf8 = %v, want equal", result.Rows[0][0], result.Rows[0][1])
	}
}

// --- isascii / isutf8 ---

func TestIsUtf8WorkedExample(t *testing.T) {
	result := queryResult(t, `print result=isutf8("some string")`)
	if result.Rows[0][0] != true {
		t.Errorf("isutf8(\"some string\") = %v, want true", result.Rows[0][0])
	}
}

func TestIsAsciiTrueAndFalse(t *testing.T) {
	result := queryResult(t, `print a=isascii("some string"), b=isascii("קוסטו")`)
	if result.Rows[0][0] != true {
		t.Errorf("isascii(\"some string\") = %v, want true", result.Rows[0][0])
	}
	if result.Rows[0][1] != false {
		t.Errorf("isascii(\"קוסטו\") = %v, want false", result.Rows[0][1])
	}
}
