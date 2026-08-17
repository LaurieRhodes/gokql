package parser

import "testing"

// dynamic_singlequote_test.go — normalizeSingleQuotedJSON, found and
// fixed as a real, recurring gap during this session's own testing
// (not hypothetical): real ADX's own documentation examples for
// several functions (bag_merge, bag_remove_keys, has_any_index, among
// others) freely use single quotes inside dynamic({...}) literals as
// ordinary, valid KQL grammar — strict JSON (which this engine's own
// json.Valid check enforces) never permits single quotes at all, so a
// direct copy-paste of those real, documented examples failed with
// "not valid JSON" until requoted by hand, twice, in two genuinely
// separate pieces of work this same session.

func TestNormalizeSingleQuotedJSONBasic(t *testing.T) {
	got := normalizeSingleQuotedJSON(`{'a':1,'b':2}`)
	want := `{"a":1,"b":2}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeSingleQuotedJSONArray(t *testing.T) {
	got := normalizeSingleQuotedJSON(`['this', 'example']`)
	want := `["this", "example"]`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestNormalizeSingleQuotedJSONDoubleQuotedUnaffected guards that an
// already-double-quoted string is copied through completely
// unchanged — the actual point of this being a purely additive fix,
// not a risk to any query that never used single quotes at all.
func TestNormalizeSingleQuotedJSONDoubleQuotedUnaffected(t *testing.T) {
	got := normalizeSingleQuotedJSON(`{"a":1,"b":"hello"}`)
	want := `{"a":1,"b":"hello"}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestNormalizeSingleQuotedJSONEmbeddedDoubleQuote guards that a
// literal " found INSIDE a single-quoted string is correctly escaped
// once re-emitted as a double-quoted one — without this, the
// resulting JSON would be broken (an unescaped " would prematurely
// close the string).
func TestNormalizeSingleQuotedJSONEmbeddedDoubleQuote(t *testing.T) {
	got := normalizeSingleQuotedJSON(`{'msg': 'she said "hi"'}`)
	want := `{"msg": "she said \"hi\""}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestNormalizeSingleQuotedJSONEscapedSingleQuote guards that \' (an
// escaped single quote WITHIN a single-quoted string, needed there to
// avoid prematurely closing it) is correctly unescaped to a bare ' —
// no longer needing escaping once the outer delimiter becomes ".
func TestNormalizeSingleQuotedJSONEscapedSingleQuote(t *testing.T) {
	got := normalizeSingleQuotedJSON(`{'msg': 'it\'s here'}`)
	want := `{"msg": "it's here"}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestNormalizeSingleQuotedJSONMixedQuotes guards single and double
// quotes appearing together in the same literal — real ADX permits
// this, not an either/or choice for the whole literal.
func TestNormalizeSingleQuotedJSONMixedQuotes(t *testing.T) {
	got := normalizeSingleQuotedJSON(`{"a":1, 'b':2}`)
	want := `{"a":1, "b":2}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestNormalizeSingleQuotedJSONNested guards a nested object/array,
// both using single quotes throughout — the recursive case, not just
// a single, flat level.
func TestNormalizeSingleQuotedJSONNested(t *testing.T) {
	got := normalizeSingleQuotedJSON(`{'outer': {'inner': ['a', 'b']}}`)
	want := `{"outer": {"inner": ["a", "b"]}}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
