package engine

import "testing"

// func_jaccard_formatbytes_test.go — jaccard_index, format_bytes,
// added 2026-08-18. Every case below is taken directly from real
// ADX's own docs' worked examples.

// --- jaccard_index ---

func TestJaccardIndexWorkedExample(t *testing.T) {
	result := queryResult(t, `print set1=dynamic([1,2,3]), set2=dynamic([1,2,3,4])
		| extend jaccard=jaccard_index(set1, set2)`)
	got, ok := result.Rows[0][2].(float64)
	if !ok || got != 0.75 {
		t.Errorf("jaccard = %v (%T), want float64(0.75)", result.Rows[0][2], result.Rows[0][2])
	}
}

// TestJaccardIndexIdenticalSets guards a simple sanity case beyond
// the one worked example: identical sets have a Jaccard index of 1.
func TestJaccardIndexIdenticalSets(t *testing.T) {
	result := queryResult(t, `print r = jaccard_index(dynamic([1,2,3]), dynamic([1,2,3]))`)
	got, ok := result.Rows[0][0].(float64)
	if !ok || got != 1.0 {
		t.Errorf("r = %v (%T), want float64(1.0)", result.Rows[0][0], result.Rows[0][0])
	}
}

// TestJaccardIndexDisjointSets guards the other simple sanity case:
// disjoint sets have a Jaccard index of 0.
func TestJaccardIndexDisjointSets(t *testing.T) {
	result := queryResult(t, `print r = jaccard_index(dynamic([1,2]), dynamic([3,4]))`)
	got, ok := result.Rows[0][0].(float64)
	if !ok || got != 0.0 {
		t.Errorf("r = %v (%T), want float64(0.0)", result.Rows[0][0], result.Rows[0][0])
	}
}

// TestJaccardIndexDuplicatesIgnored guards real ADX's own documented
// "duplicate values in the input arrays are ignored" rule.
func TestJaccardIndexDuplicatesIgnored(t *testing.T) {
	result := queryResult(t, `print r = jaccard_index(dynamic([1,1,1,2]), dynamic([1,2,2,2]))`)
	got, ok := result.Rows[0][0].(float64)
	if !ok || got != 1.0 {
		t.Errorf("r = %v (%T), want float64(1.0) -- both sets dedup to {1,2}", result.Rows[0][0], result.Rows[0][0])
	}
}

// --- format_bytes ---

func TestFormatBytesWorkedExamples(t *testing.T) {
	result := queryResult(t, `print
		v1 = format_bytes(564),
		v2 = format_bytes(10332, 1),
		v3 = format_bytes(20010332),
		v4 = format_bytes(20010332, 2),
		v5 = format_bytes(20010332, 0, "KB")`)
	want := []string{"564 Bytes", "10.1 KB", "19 MB", "19.08 MB", "19541 KB"}
	names := []string{"v1", "v2", "v3", "v4", "v5"}
	for i, w := range want {
		got := result.Rows[0][i]
		if got != w {
			t.Errorf("%s = %v, want %v", names[i], got, w)
		}
	}
}

// TestFormatBytesInvalidUnitReturnsNull guards a reasonable,
// undocumented-by-example edge case: an unrecognized explicit units
// string returns null rather than silently falling back to auto-
// detection or erroring, matching this codebase's established
// convention for invalid input elsewhere.
func TestFormatBytesInvalidUnitReturnsNull(t *testing.T) {
	result := queryResult(t, `print r = format_bytes(1024, 0, "NotAUnit")`)
	if result.Rows[0][0] != nil {
		t.Errorf("r = %v, want null", result.Rows[0][0])
	}
}

// TestFormatBytesNullPropagation guards null input propagating to
// null.
func TestFormatBytesNullPropagation(t *testing.T) {
	result := queryResult(t, `print r = format_bytes(dynamic(null))`)
	if result.Rows[0][0] != nil {
		t.Errorf("r = %v, want null", result.Rows[0][0])
	}
}
