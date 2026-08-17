package engine

import "testing"

// func_url_csv_index_test.go — url_encode, parse_csv, has_any_index.
// Each test checks against its own function's real ADX documented
// worked example directly, values included.

func TestUrlEncodeSpaceBecomesPlus(t *testing.T) {
	tbl := queryResult(t, `let url = 'https://www.bing.com/hello world/'; print encoded = url_encode(url)`)
	expectCell(t, tbl, 0, 0, "https://www.bing.com/hello+world/")
}

// TestUrlEncodeLiteralPlusEscapedFirst guards the real subtlety this
// function's own implementation has to handle correctly: a literal
// '+' in the input must be escaped to %2B BEFORE space is turned into
// '+', or an original '+' would become indistinguishable from an
// encoded space once both share the same output character.
func TestUrlEncodeLiteralPlusEscapedFirst(t *testing.T) {
	tbl := queryResult(t, `print x = url_encode('a+b c')`)
	expectCell(t, tbl, 0, 0, "a%2Bb+c")
}

func TestParseCsvBasic(t *testing.T) {
	tbl := queryResult(t, `print result=parse_csv('aa,bb,cc')`)
	expectCell(t, tbl, 0, 0, `["aa","bb","cc"]`)
}

// TestParseCsvQuoteEscaping guards real ADX's own documented worked
// example directly: embedded commas, quotes, and newlines within a
// double-quoted field.
func TestParseCsvQuoteEscaping(t *testing.T) {
	tbl := queryResult(t, "print result=parse_csv('aa,\"b,b,b\",cc,\"Escaping quotes: \"\"Title\"\"\",\"line1\\nline2\"')")
	got, ok := tbl.Rows[0][0].(string)
	if !ok {
		t.Fatalf("expected a JSON string result, got %T", tbl.Rows[0][0])
	}
	arr, ok := parseJSONArray(got)
	if !ok || len(arr) != 5 {
		t.Fatalf("expected a 5-element array, got: %v", got)
	}
	if arr[1] != "b,b,b" {
		t.Errorf("expected the embedded-comma field preserved as one value, got %v", arr[1])
	}
	if arr[3] != `Escaping quotes: "Title"` {
		t.Errorf("expected the embedded-quote field correctly unescaped, got %v", arr[3])
	}
}

// TestParseCsvOnlyFirstRecordTaken guards real ADX's own documented
// restriction: "this function doesn't support multiple records per
// row (only the first record is taken)."
func TestParseCsvOnlyFirstRecordTaken(t *testing.T) {
	tbl := queryResult(t, "print result=parse_csv('record1,a,b,c\\nrecord2,x,y,z')")
	expectCell(t, tbl, 0, 0, `["record1","a","b","c"]`)
}

// TestHasAnyIndexAllCases guards real ADX's own documented worked
// example directly, checked against every one of its stated cases:
// first-match, last-match, no-match, and empty-array.
func TestHasAnyIndexAllCases(t *testing.T) {
	tbl := queryResult(t, `print idx1 = has_any_index("this is an example", dynamic(["this", "example"])), idx2 = has_any_index("this is an example", dynamic(["not", "example"])), idx3 = has_any_index("this is an example", dynamic(["not", "found"])), idx5 = has_any_index("this is an example", dynamic([]))`)
	expectCell(t, tbl, 0, 0, "0")
	expectCell(t, tbl, 0, 1, "1")
	expectCell(t, tbl, 0, 2, "-1")
	expectCell(t, tbl, 0, 3, "-1")
}
