package parser

import "testing"

// verbatim_string_test.go — @"..."/@'...' verbatim string literals and
// typeof(...), added 2026-08-18 as part of a larger regex-support unit
// of work (extract's optional typeLiteral argument needs typeof;
// verbatim strings are how virtually every real KQL regex example
// avoids double-backslash-escaping a pattern). Verified against real
// KQL's own documented escaping rules before implementing: backslash
// is literal throughout a verbatim string, and the only escape is
// doubling the enclosing quote character.

func mustParseExpr(t *testing.T, query string) {
	t.Helper()
	if _, err := Parse(query); err != nil {
		t.Fatalf("Parse(%q): %v", query, err)
	}
}

func TestVerbatimStringBackslashIsLiteral(t *testing.T) {
	result := evalPrintString(t, `print r = @"a\nb"`)
	want := `a\nb`
	if result != want {
		t.Errorf("@\"a\\nb\" = %q, want %q (backslash-n literal, not a newline)", result, want)
	}
}

func TestVerbatimStringDoubledQuoteEscape(t *testing.T) {
	result := evalPrintString(t, `print r = @"say ""hi"" here"`)
	want := `say "hi" here`
	if result != want {
		t.Errorf(`@"say ""hi"" here" = %q, want %q`, result, want)
	}
}

func TestVerbatimStringSingleQuoteForm(t *testing.T) {
	result := evalPrintString(t, `print r = @'c:\windows\system'`)
	want := `c:\windows\system`
	if result != want {
		t.Errorf("@'c:\\windows\\system' = %q, want %q", result, want)
	}
}

func TestVerbatimStringUnterminatedIsError(t *testing.T) {
	_, err := Parse(`print r = @"unterminated`)
	if err == nil {
		t.Fatal("expected error for unterminated verbatim string, got none")
	}
}

func TestTypeofParsesKnownTypes(t *testing.T) {
	for _, tn := range []string{"long", "int", "real", "double", "bool", "datetime", "date", "timespan", "guid", "dynamic", "string"} {
		mustParseExpr(t, `print r = extract("x", 1, "y", typeof(`+tn+`))`)
	}
}

func TestTypeofRejectsUnknownType(t *testing.T) {
	_, err := Parse(`print r = extract("x", 1, "y", typeof(notarealtype))`)
	if err == nil {
		t.Fatal("expected error for typeof(notarealtype), got none")
	}
}

// evalPrintString is a tiny helper that parses a single `print r =
// <expr>` query via the exported Parse function and returns the
// resulting literal's string form, for tests that only need to
// confirm the LEXED/PARSED value, not run through the full engine.
// Verbatim strings parse to plain string Literal nodes (same AST shape
// as a regular string literal), so this just walks the parsed AST.
func evalPrintString(t *testing.T, query string) string {
	t.Helper()
	stmt, err := Parse(query)
	if err != nil {
		t.Fatalf("Parse(%q): %v", query, err)
	}
	q, ok := stmt.(*Query)
	if !ok || len(q.Operators) == 0 {
		t.Fatalf("Parse(%q): expected a Query with a print operator", query)
	}
	p, ok := q.Operators[0].(*PrintOp)
	if !ok || len(p.Expressions) == 0 {
		t.Fatalf("Parse(%q): expected a PrintOp with one expression, got %T", query, q.Operators[0])
	}
	lit, ok := p.Expressions[0].Expr.(*Literal)
	if !ok {
		t.Fatalf("Parse(%q): expected a Literal expression, got %T", query, p.Expressions[0].Expr)
	}
	s, ok := lit.Value.(string)
	if !ok {
		t.Fatalf("Parse(%q): expected a string literal value, got %T (%v)", query, lit.Value, lit.Value)
	}
	return s
}
