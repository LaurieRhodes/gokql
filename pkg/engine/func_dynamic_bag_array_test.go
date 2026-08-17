package engine

import "testing"

// func_dynamic_bag_array_test.go — zip, array_shift_left/right,
// bag_merge, bag_remove_keys. Each test traces to a real ADX
// documented example, verified before implementing, not assumed.

func TestZipEqualLengthArrays(t *testing.T) {
	tbl := queryResult(t, `print result = zip(dynamic([1,3,5]), dynamic([2,4,6]))`)
	expectCell(t, tbl, 0, 0, `[[1,2],[3,4],[5,6]]`)
}

// TestZipUnequalLengthFillsNull guards real ADX's own documented
// behavior: output length is the LONGEST input array, not the
// shortest — a shorter array's missing slot fills with null.
func TestZipUnequalLengthFillsNull(t *testing.T) {
	tbl := queryResult(t, `print result = zip(dynamic(["A", 1, 1.5]), dynamic([{}, "B"]))`)
	expectCell(t, tbl, 0, 0, `[["A",{}],[1,"B"],[1.5,null]]`)
}

func TestArrayShiftLeftBasic(t *testing.T) {
	tbl := queryResult(t, `print result = array_shift_left(dynamic([1,2,3,4,5]), 2)`)
	expectCell(t, tbl, 0, 0, `[3,4,5,null,null]`)
}

func TestArrayShiftLeftWithDefaultValue(t *testing.T) {
	tbl := queryResult(t, `print result = array_shift_left(dynamic([1,2,3,4,5]), 2, -1)`)
	expectCell(t, tbl, 0, 0, `[3,4,5,-1,-1]`)
}

// TestArrayShiftLeftNegativeShiftsRight guards real ADX's own
// documented mechanism for a right-shift: a negative shift_count on
// array_shift_left, no separate implementation needed for the
// direction itself.
func TestArrayShiftLeftNegativeShiftsRight(t *testing.T) {
	tbl := queryResult(t, `print result = array_shift_left(dynamic([1,2,3,4,5]), -2, -1)`)
	expectCell(t, tbl, 0, 0, `[-1,-1,1,2,3]`)
}

// TestArrayShiftRightMatchesNegatedLeft guards that array_shift_right
// is genuinely the same underlying function with the direction
// inverted, not a separate, potentially-drifting implementation.
func TestArrayShiftRightMatchesNegatedLeft(t *testing.T) {
	tbl := queryResult(t, `print result = array_shift_right(dynamic([1,2,3,4,5]), 2, -1)`)
	expectCell(t, tbl, 0, 0, `[-1,-1,1,2,3]`)
}

// TestBagMergeLeftmostWins guards real ADX's own documented
// precedence rule directly, against the real docs' own worked
// example.
func TestBagMergeLeftmostWins(t *testing.T) {
	tbl := queryResult(t, `print result = bag_merge(dynamic({"A1":12, "B1":2, "C1":3}), dynamic({"A2":81, "B2":82, "A1":1}))`)
	got, ok := tbl.Rows[0][0].(string)
	if !ok {
		t.Fatalf("expected a JSON string result, got %T", tbl.Rows[0][0])
	}
	// Key order isn't meaningful in JSON; check the decoded shape
	// instead of an exact string match.
	obj, ok := parseJSONObject(got)
	if !ok {
		t.Fatalf("result did not parse as a JSON object: %q", got)
	}
	if obj["A1"] != float64(12) {
		t.Errorf("expected leftmost bag's A1=12 to win over the rightmost bag's A1=1, got %v", obj["A1"])
	}
	if obj["B1"] != float64(2) || obj["C1"] != float64(3) || obj["A2"] != float64(81) || obj["B2"] != float64(82) {
		t.Errorf("expected all non-conflicting keys preserved, got %v", obj)
	}
}

func TestBagRemoveKeysTopLevel(t *testing.T) {
	tbl := queryResult(t, `datatable(input:dynamic) [dynamic({"key1" : 123, "key2": "abc"}), dynamic({"key1" : "value", "key3": 42.0})] | extend result=bag_remove_keys(input, dynamic(["key2", "key4"])) | project result`)
	expectRows(t, tbl, 2)
	obj0, ok := parseJSONObject(tbl.Rows[0][0])
	if !ok || len(obj0) != 1 || obj0["key1"] != float64(123) {
		t.Errorf("expected row 0 to be {key1:123} with key2 removed, got %v", tbl.Rows[0][0])
	}
	obj1, ok := parseJSONObject(tbl.Rows[1][0])
	if !ok || len(obj1) != 2 {
		t.Errorf("expected row 1 unaffected (no key2 or key4 present), got %v", tbl.Rows[1][0])
	}
}

// TestBagRemoveKeysNestedJSONPath guards the "$.key2.prop1" nested
// form directly against real ADX's own worked example.
func TestBagRemoveKeysNestedJSONPath(t *testing.T) {
	tbl := queryResult(t, `datatable(input:dynamic) [dynamic({"key1": 123, "key2": {"prop1" : "abc", "prop2": "xyz"}, "key3": [100, 200]})] | extend result=bag_remove_keys(input, dynamic(["$.key2.prop1", "key3"])) | project result`)
	obj, ok := parseJSONObject(tbl.Rows[0][0])
	if !ok {
		t.Fatalf("result did not parse as a JSON object: %v", tbl.Rows[0][0])
	}
	if _, has := obj["key3"]; has {
		t.Error("expected key3 removed entirely")
	}
	key2, ok := obj["key2"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected key2 to survive as a nested object, got %v", obj["key2"])
	}
	if _, has := key2["prop1"]; has {
		t.Error("expected key2.prop1 removed by the nested JSONPath form")
	}
	if key2["prop2"] != "xyz" {
		t.Errorf("expected key2.prop2 to survive untouched, got %v", key2["prop2"])
	}
}

// TestBagMergeSingleQuotedLiteralWorks directly guards that real
// ADX's own bag_merge documented worked example — which uses single
// quotes throughout — no longer fails with "not valid JSON" (a real,
// live gap found and fixed alongside this batch of work, in
// normalizeSingleQuotedJSON, parser package).
func TestBagMergeSingleQuotedLiteralWorks(t *testing.T) {
	tbl := queryResult(t, `print result = bag_merge(dynamic({'A1':12, 'B1':2, 'C1':3}), dynamic({'A2':81, 'B2':82, 'A1':1}))`)
	obj, ok := parseJSONObject(tbl.Rows[0][0])
	if !ok || obj["A1"] != float64(12) {
		t.Errorf("expected the leftmost bag's A1=12 to win, got %v", tbl.Rows[0][0])
	}
}

// TestHasAnyIndexSingleQuotedLiteralWorks directly guards the second
// real ADX documented example this same gap blocked.
func TestHasAnyIndexSingleQuotedLiteralWorks(t *testing.T) {
	tbl := queryResult(t, `print idx1 = has_any_index('this is an example', dynamic(['this', 'example']))`)
	expectCell(t, tbl, 0, 0, "0")
}
