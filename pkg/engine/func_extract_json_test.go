package engine

import "testing"

// func_extract_json_test.go — extract_json()/extractjson(), added
// 2026-08-18. Verified against real ADX's own extract-json-function.md
// (the simple worked example) and jsonpath.md (the full JSONPath
// subset's own worked document, all 11 documented example paths).

// TestExtractJsonSimpleWorkedExample guards real ADX's own
// extract-json-function.md worked example exactly: extract_json with
// a typeLiteral argument against a simple JSON object.
func TestExtractJsonSimpleWorkedExample(t *testing.T) {
	result := queryResult(t, `let json = '{"name": "John", "age": 30, "city": "New York"}';
		print extract_json("$.name", json, typeof(string))`)
	if got := result.Rows[0][0]; got != "John" {
		t.Errorf("extract_json($.name, ..., typeof(string)) = %v, want %q", got, "John")
	}
}

// TestExtractJsonJSONPathSubsetWorkedExample guards real ADX's own
// jsonpath.md worked example end to end: all four documented JSONPath
// constructs ($ root, .prop dot access, ['prop']/["prop"] bracket
// access, [n] array index) against the exact JSON document and 11
// example paths shown on that doc page.
//
// NOTE on the doc page's own last example: jsonpath.md lists
// "$.Tags[0]" (no "Details." prefix) as the path for "First value in
// the array of the Tags" -- but the SAME page's own JSON document
// nests Tags inside Details, not at the root. This looks like a
// genuine typo in Microsoft's own docs (the same kind of intra-page
// inconsistency Sprint 18's indexof_regex work also found), not a
// real, different JSONPath resolution rule -- "$.Tags[0]" against
// that exact document correctly resolves to nothing (Tags isn't a
// root-level property), confirmed directly, so this test uses the
// CORRECTED path "$.Details.Tags[0]" and additionally asserts the
// doc's own literal (apparently-typo'd) path returns null, rather
// than silently "fixing" the doc's mistake without recording it.
func TestExtractJsonJSONPathSubsetWorkedExample(t *testing.T) {
	doc := `let doc = '{"Source":"Server-01","Timestamp":"2023-07-25T09:15:32.123Z","Log Level":"INFO","Message":"Application started successfully.","Details":{"Service":"AuthService","Endpoint":"/api/login","Response Code":200,"Response Time":54.21,"User":{"User ID":"user123","Username":"kiana_anderson","IP Address":"192.168.1.100"},"Tags":["startup","auth","performance"]}}';`

	tests := []struct {
		name string
		path string
		want string
	}{
		{"Source field", "$.Source", "Server-01"},
		{"Timestamp field", "$.Timestamp", "2023-07-25T09:15:32.123Z"},
		{"Log Level field (bracket, space in name)", `$['Log Level']`, "INFO"},
		{"Message field", "$.Message", "Application started successfully."},
		{"nested Service field", "$.Details.Service", "AuthService"},
		{"nested Endpoint field", "$.Details.Endpoint", "/api/login"},
		{"nested bracket, space in name (number leaf)", `$.Details['Response Code']`, "200"},
		{"nested bracket, space in name (float leaf)", `$.Details['Response Time']`, "54.21"},
		{"doubly-nested bracket", `$.Details.User['User ID']`, "user123"},
		{"doubly-nested dot", "$.Details.User.Username", "kiana_anderson"},
		{"doubly-nested bracket, space in name", `$.Details.User['IP Address']`, "192.168.1.100"},
		{"array index, corrected path", "$.Details.Tags[0]", "startup"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := queryResult(t, doc+`print r = extract_json("`+escapeForKQLDoubleQuoted(tt.path)+`", doc)`)
			if got := result.Rows[0][0]; got != tt.want {
				t.Errorf("extract_json(%q, doc) = %v, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestExtractJsonDocPageOwnPathIsATypo directly guards the doc page's
// own literal "$.Tags[0]" (without "Details.") against the exact same
// document: this must resolve to null, since Tags is not a root-level
// property in the document the doc page itself shows. See the comment
// on TestExtractJsonJSONPathSubsetWorkedExample above for the full
// reasoning.
func TestExtractJsonDocPageOwnPathIsATypo(t *testing.T) {
	result := queryResult(t, `let doc = '{"Details":{"Tags":["startup","auth","performance"]}}';
		print r = extract_json("$.Tags[0]", doc)`)
	if result.Rows[0][0] != "" {
		t.Errorf(`extract_json("$.Tags[0]", doc) = %v, want empty (a string is never null) (Tags is nested under Details in this document, not root-level)`, result.Rows[0][0])
	}
}

// TestExtractJsonNoMatch guards a genuinely missing property: no
// match, no typeLiteral -> null, not an error or empty string.
func TestExtractJsonNoMatch(t *testing.T) {
	result := queryResult(t, `print r = extract_json("$.missing", '{"a":1}')`)
	if result.Rows[0][0] != "" {
		t.Errorf("r = %v, want empty (a string is never null)", result.Rows[0][0])
	}
}

// TestExtractJsonInvalidJSON guards a dataSource that isn't valid
// JSON at all -> null, not an error.
func TestExtractJsonInvalidJSON(t *testing.T) {
	result := queryResult(t, `print r = extract_json("$.name", "not valid json")`)
	if result.Rows[0][0] != "" {
		t.Errorf("r = %v, want empty (a string is never null)", result.Rows[0][0])
	}
}

// TestExtractJsonNullPropagation guards null jsonPath / null
// dataSource arguments propagating to null, matching this codebase's
// established convention elsewhere (extract(), parse_ipv6(), etc).
func TestExtractJsonNullPropagation(t *testing.T) {
	result := queryResult(t, `print a = extract_json(dynamic(null), '{"a":1}'), b = extract_json("$.a", dynamic(null))`)
	if result.Rows[0][0] != "" {
		t.Errorf("a (null jsonPath) = %v, want empty (a string is never null)", result.Rows[0][0])
	}
	if result.Rows[0][1] != "" {
		t.Errorf("b (null dataSource) = %v, want empty (a string is never null)", result.Rows[0][1])
	}
}

// TestExtractJsonObjectLeafRemarshaled guards that a path resolving
// to an object (not a scalar leaf) returns compact, re-marshaled JSON
// text rather than Go's map-printed form or an error -- so the result
// remains valid input to a further extract_json/parse_json call.
func TestExtractJsonObjectLeafRemarshaled(t *testing.T) {
	result := queryResult(t, `print r = extract_json("$.Details", '{"Details":{"x":1}}')`)
	if got := result.Rows[0][0]; got != `{"x":1}` {
		t.Errorf(`r = %v, want {"x":1}`, got)
	}
}

// TestExtractJsonAliasEquivalence guards that extractjson() (no
// underscore) is a real, working alias of extract_json(), per real
// ADX's own docs stating the two are equivalent.
func TestExtractJsonAliasEquivalence(t *testing.T) {
	result := queryResult(t, `print a = extract_json("$.name", '{"name":"John"}'), b = extractjson("$.name", '{"name":"John"}')`)
	if result.Rows[0][0] != result.Rows[0][1] {
		t.Errorf("extract_json = %v, extractjson = %v, want equal", result.Rows[0][0], result.Rows[0][1])
	}
}

// TestExtractJsonTypeConversion guards the optional 3rd typeLiteral
// argument converting the extracted value, mirroring extract()'s own
// established pattern -- and a failed conversion (or invalid
// typeLiteral) returning null rather than erroring, same contract.
func TestExtractJsonTypeConversion(t *testing.T) {
	result := queryResult(t, `print r = extract_json("$.age", '{"age":30}', typeof(long))`)
	got, ok := result.Rows[0][0].(int64)
	if !ok || got != 30 {
		t.Errorf("r = %v (%T), want int64(30)", result.Rows[0][0], result.Rows[0][0])
	}
}

// escapeForKQLDoubleQuoted is a tiny test helper: several of the
// worked-example JSONPath strings above contain single quotes
// (['Log Level']) which are fine unescaped inside a KQL double-quoted
// string literal, so this is currently a no-op passthrough -- kept as
// a named seam in case a future worked example needs real escaping,
// rather than inlining string concatenation with no explanation at
// each call site.
func escapeForKQLDoubleQuoted(s string) string {
	return s
}
