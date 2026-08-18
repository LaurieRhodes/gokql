package engine

import (
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// func_parse_xml_test.go — parse_xml(), added 2026-08-18. Every case
// below is taken directly from real ADX's own parse-xml-function.md:
// its 7-row conversion table (one dedicated test per row) plus its
// full "duration" worked example, checked against the doc's own
// exact numeric leaf values via dot-access, matching the doc's own
// d.duration.value / d["duration"]["min"] retrieval example.

// TestParseXMLConversionTableRows guards all 7 rows of real ADX's own
// documented XML-to-JSON conversion table, individually.
func TestParseXMLConversionTableRows(t *testing.T) {
	tests := []struct {
		name string
		xml  string
		want string
	}{
		{"empty element (row 1)", `<e/>`, `{"e":null}`},
		{"text only (row 2)", `<e>text</e>`, `{"e":"text"}`},
		{"attribute only (row 3)", `<e name="value" />`, `{"e":{"@name":"value"}}`},
		{"attribute plus text (row 4)", `<e name="value">text</e>`, `{"e":{"#text":"text","@name":"value"}}`},
		{"distinct children (row 5)", `<e><a>text</a><b>text</b></e>`, `{"e":{"a":"text","b":"text"}}`},
		{"repeated children collapse to array (row 6)", `<e><a>text</a><a>text</a></e>`, `{"e":{"a":["text","text"]}}`},
		{"mixed text and child (row 7)", `<e> text <a>text</a> </e>`, `{"e":{"#text":"text","a":"text"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := queryResult(t, `print r = parse_xml("`+kqlEscapeDoubleQuoted(tt.xml)+`")`)
			if got := result.Rows[0][0]; got != tt.want {
				t.Errorf("parse_xml(%q) = %v, want %v", tt.xml, got, tt.want)
			}
		})
	}
}

// TestParseXMLThreeChildrenCollapseToArray extends row 6's own
// two-element case to three occurrences, guarding the "promote to
// slice on 2nd, append on 3rd+" logic specifically (not exercised by
// the doc's own 2-element example alone).
func TestParseXMLThreeChildrenCollapseToArray(t *testing.T) {
	result := queryResult(t, `print r = parse_xml("<e><a>1</a><a>2</a><a>3</a></e>")`)
	want := `{"e":{"a":[1,2,3]}}`
	if got := result.Rows[0][0]; got != want {
		t.Errorf("r = %v, want %v", got, want)
	}
}

// TestParseXMLDurationWorkedExample guards real ADX's own full
// worked example end to end: an XML declaration prolog (must be
// skipped, not error), 7 numeric-looking leaf children under a
// "duration" root, and retrieval via both dot-access and
// bracket-access exactly as the doc's own example does
// (duration_value=d.duration.value, duration_min=d["duration"]["min"]),
// checked against the doc's own stated exact values (118.0 and 100.0
// respectively) by NUMERIC equality via types.ToFloat64 -- not by Go
// type or exact textual form. JSON itself has no int/float
// distinction, and this engine's own dynamic representation is a
// plain JSON-encoded string, so "118.0" and "118" are the identical
// value once round-tripped through json.Marshal/Unmarshal; asserting
// a specific Go type here would test this engine's own incidental
// number-decoding preference, not real ADX's documented contract.
func TestParseXMLDurationWorkedExample(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<duration>
    <value>118.0</value>
    <count>5.0</count>
    <min>100.0</min>
    <max>150.0</max>
    <stdDev>0.0</stdDev>
    <sampledValue>118.0</sampledValue>
    <sum>118.0</sum>
</duration>`
	result := queryResult(t, "let context_custom_metrics = '"+xml+"';\n"+
		`print d = parse_xml(context_custom_metrics)`+
		` | extend duration_value=d.duration.value, duration_min=d["duration"]["min"]`)
	dvIdx := result.Schema.ColumnIndex("duration_value")
	dmIdx := result.Schema.ColumnIndex("duration_min")
	if got := types.ToFloat64(result.Rows[0][dvIdx]); got != 118.0 {
		t.Errorf("duration_value = %v (%T), want 118.0", result.Rows[0][dvIdx], result.Rows[0][dvIdx])
	}
	if got := types.ToFloat64(result.Rows[0][dmIdx]); got != 100.0 {
		t.Errorf("duration_min = %v (%T), want 100.0", result.Rows[0][dmIdx], result.Rows[0][dmIdx])
	}
}

// TestParseXMLInvalidReturnsNull guards real ADX's own documented
// "or null, if the XML format is invalid" contract.
func TestParseXMLInvalidReturnsNull(t *testing.T) {
	result := queryResult(t, `print r = parse_xml("not valid xml at all <<<")`)
	if result.Rows[0][0] != nil {
		t.Errorf("r = %v, want null", result.Rows[0][0])
	}
}

// TestParseXMLNullPropagation guards null input propagating to null,
// matching this codebase's established convention elsewhere.
func TestParseXMLNullPropagation(t *testing.T) {
	result := queryResult(t, `print r = parse_xml(dynamic(null))`)
	if result.Rows[0][0] != nil {
		t.Errorf("r = %v, want null", result.Rows[0][0])
	}
}

// TestParseXMLNestedElementsNoAttributesOrText guards a case beyond
// the doc's own 7 rows: a chain of nested elements with no text
// leaves at all (only structural nesting), ending in an empty leaf.
func TestParseXMLNestedElementsNoAttributesOrText(t *testing.T) {
	result := queryResult(t, `print r = parse_xml("<a><b><c/></b></a>")`)
	want := `{"a":{"b":{"c":null}}}`
	if got := result.Rows[0][0]; got != want {
		t.Errorf("r = %v, want %v", got, want)
	}
}

// TestParseXMLOversizedInputReturnsNull guards real ADX's own
// documented 1 MB (1,048,576 byte) input-length cap: a longer string
// must return null, not attempt to parse it. Uses a real repeated
// character (not zero bytes) as padding so the only variable under
// test is length, not content validity.
func TestParseXMLOversizedInputReturnsNull(t *testing.T) {
	padding := make([]byte, maxXMLInputBytes+1)
	for i := range padding {
		padding[i] = 'x'
	}
	xmlStr := `<e>` + string(padding) + `</e>`
	result, ok := evalParseXML(xmlStr)
	if ok {
		t.Fatalf("evalParseXML on a %d-byte input succeeded (result %v), want null (over the 1MB cap)", len(xmlStr), result)
	}
}

// kqlEscapeDoubleQuoted escapes a literal double quote so an XML
// fixture (which uses double quotes for its own attribute values) can
// be embedded inside a KQL double-quoted string literal without
// terminating it early — a real, easy-to-miss gap the first version
// of this test file hit directly (row 3/4's own attribute fixtures
// failed to parse as KQL at all before this existed).
func kqlEscapeDoubleQuoted(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}
