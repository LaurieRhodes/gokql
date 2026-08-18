package engine

import (
	"encoding/json"
	"encoding/xml"
	"strconv"
	"strings"
)

// func_xml.go — parse_xml(), added 2026-08-18. Real ADX's own docs
// (parse-xml-function.md) give a precise, 7-row XML-to-JSON conversion
// table plus one full worked example; this implementation follows
// that table exactly for every case it covers, and documents the two
// places it had to reasonably infer behavior the table doesn't
// directly specify (see maxXMLInputBytes and coerceXMLLeafText below).

// maxXMLInputBytes matches real ADX's own documented limit exactly:
// "Maximal input string length for parse_xml is 1 MB (1,048,576
// bytes). Longer strings interpretation will result in a null
// object." — not an inferred or rounded value.
const maxXMLInputBytes = 1048576

// evalParseXML converts an XML-formatted string to this engine's own
// dynamic representation (a JSON-encoded string), following real
// ADX's own documented conversion table:
//
//	<e/>                             -> {"e": null}
//	<e>text</e>                      -> {"e": "text"}
//	<e name="value" />               -> {"e": {"@name": "value"}}
//	<e name="value">text</e>         -> {"e": {"@name": "value", "#text": "text"}}
//	<e><a>text</a><b>text</b></e>    -> {"e": {"a": "text", "b": "text"}}
//	<e><a>text</a><a>text</a></e>    -> {"e": {"a": ["text", "text"]}}
//	<e> text <a>text</a> </e>        -> {"e": {"#text": "text", "a": "text"}}
//
// All seven rows are verified directly, one test per row. The
// document's root element becomes the single top-level JSON key
// (confirmed by the docs' own worked example: a bare <duration>...
// </duration> document becomes {"duration": {...}}, not a bare
// object with the child keys at the top level).
//
// Returns (value, true) on success, (nil, false) on invalid XML or an
// oversized input — real ADX's own docs state both cases return null,
// not an error, matching this codebase's established convention
// elsewhere (extract_json, parse_json's own tolerant-string fallback).
func evalParseXML(s string) (string, bool) {
	if len(s) > maxXMLInputBytes {
		return "", false
	}
	dec := xml.NewDecoder(strings.NewReader(s))
	// Skip everything before the root element: XML declaration
	// (<?xml version="1.0"?>), comments, whitespace. Real ADX's own
	// docs state "only element nodes, attributes and text nodes will
	// be translated. Everything else will be skipped" — this applies
	// at every level, not just the prolog, so the recursive walker
	// below (parseXMLElementValue) also silently ignores
	// xml.Comment/xml.ProcInst/xml.Directive tokens wherever they
	// appear, via the switch's own lack of a case for them.
	var root xml.StartElement
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", false
		}
		if se, ok := tok.(xml.StartElement); ok {
			root = se
			break
		}
	}
	val, ok := parseXMLElementValue(dec, root)
	if !ok {
		return "", false
	}
	wrapped := map[string]interface{}{root.Name.Local: val}
	b, err := json.Marshal(wrapped)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// parseXMLElementValue consumes tokens up to and including start's
// own matching EndElement, and returns the JSON-shaped value for
// that element per the conversion table above. attrs always become
// "@name" keys (kept as their literal string values — no worked
// example shows attribute-value numeric coercion, unlike element text
// content, so none is applied here; only coerceXMLLeafText's own
// callers touch text content). Repeated same-named child elements
// collapse into an array (table row 6) — first occurrence stored
// directly, a second occurrence promotes it to a 2-element slice, any
// further occurrence appends.
func parseXMLElementValue(dec *xml.Decoder, start xml.StartElement) (interface{}, bool) {
	attrs := make(map[string]interface{}, len(start.Attr))
	for _, a := range start.Attr {
		attrs["@"+a.Name.Local] = a.Value
	}
	children := make(map[string]interface{})
	var textParts []string

	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			childVal, ok := parseXMLElementValue(dec, t)
			if !ok {
				return nil, false
			}
			name := t.Name.Local
			if existing, seen := children[name]; seen {
				if arr, isArr := existing.([]interface{}); isArr {
					children[name] = append(arr, childVal)
				} else {
					children[name] = []interface{}{existing, childVal}
				}
			} else {
				children[name] = childVal
			}
		case xml.CharData:
			// Whitespace-only chunks (the indentation between
			// pretty-printed sibling elements in table rows 5/6) are
			// dropped entirely — confirmed by those two rows
			// producing no "#text" key at all despite the literal XML
			// containing whitespace between <a> and <b>. A chunk with
			// real content is trimmed of surrounding whitespace (row
			// 7: " text " -> "text").
			if trimmed := strings.TrimSpace(string(t)); trimmed != "" {
				textParts = append(textParts, trimmed)
			}
		case xml.EndElement:
			if t.Name == start.Name {
				return buildXMLElementResult(attrs, children, textParts), true
			}
			// A mismatched end element means malformed XML the
			// decoder itself didn't already reject — treat as
			// invalid rather than silently misassembling structure.
			return nil, false
		}
	}
}

// buildXMLElementResult applies the table's own branching structure
// once an element's attributes, children, and direct text chunks are
// all known:
//   - no attrs, no children, no text  -> nil (row 1: <e/>)
//   - no attrs, no children, has text -> the (possibly numeric-
//     coerced) text itself (row 2: <e>text</e>)
//   - anything else (attrs and/or children present) -> an object,
//     attrs merged in as "@name" keys, children merged in by tag
//     name, plus a "#text" key if there was also direct text content
//     (rows 3, 4, 5, 6, 7)
func buildXMLElementResult(attrs, children map[string]interface{}, textParts []string) interface{} {
	hasAttrs := len(attrs) > 0
	hasChildren := len(children) > 0
	text := strings.Join(textParts, " ")
	hasText := text != ""

	if !hasAttrs && !hasChildren {
		if !hasText {
			return nil
		}
		return coerceXMLLeafText(text)
	}

	obj := make(map[string]interface{}, len(attrs)+len(children)+1)
	for k, v := range attrs {
		obj[k] = v
	}
	for k, v := range children {
		obj[k] = v
	}
	if hasText {
		obj["#text"] = coerceXMLLeafText(text)
	}
	return obj
}

// coerceXMLLeafText converts an element's own direct text content to
// a number when the text is a valid integer or decimal literal,
// otherwise leaves it as a string. This is a REASONABLE INFERENCE,
// not a directly-documented rule: parse-xml-function.md's own
// conversion table states every leaf value as a bare string ("text"),
// but the SAME page's worked example feeds numeric-looking element
// text ("118.0", "5.0", "100.0", etc, from <value>118.0</value> and
// siblings) and shows the resulting JSON with genuine numeric values
// (118.0, not "118.0", i.e. unquoted in the shown JSON), not strings —
// so SOME coercion clearly happens for element text content. Real
// ADX's own parse_json() docs describe the identical pattern ("If
// possible, the value is converted into relevant data types"), and
// this is the most direct, defensible generalization from that one
// concrete, confirmed example. NOT applied to attribute values (no
// worked example shows attribute-value coercion either way, so none
// is assumed there — see parseXMLElementValue's own doc comment).
func coerceXMLLeafText(s string) interface{} {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}
