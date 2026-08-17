package engine

import "testing"

// aggregation_test.go — summarize's own aggregation functions.

// TestAnyifSelectsMatchingRow guards real ADX's own documented
// take_anyif example directly (anyif is real ADX's own documented
// deprecated alias for take_anyif, not a separate function invented
// here) — verified against the exact worked example before
// implementing.
func TestAnyifSelectsMatchingRow(t *testing.T) {
	tbl := queryResult(t, `datatable(EventType:string, EventNarrative:string) ["Rain","calm day","Strong Wind","gusts of strong wind reported"] | summarize anyif(EventType, EventNarrative has "strong wind")`)
	expectCell(t, tbl, 0, 0, "Strong Wind")
}

// TestAnyifReturnsNullWhenNoMatch guards real ADX's own documented
// behavior: "if the predicate doesn't return 'true', a null value is
// produced."
func TestAnyifReturnsNullWhenNoMatch(t *testing.T) {
	tbl := queryResult(t, `datatable(EventType:string, EventNarrative:string) ["Rain","calm day"] | summarize anyif(EventType, EventNarrative has "strong wind")`)
	if tbl.Rows[0][0] != nil {
		t.Errorf("expected null when no row satisfies the predicate, got %v", tbl.Rows[0][0])
	}
}

// --- Additional aggregation functions, systematically checked against
// the full real-ADX aggregation-functions reference ---

// TestTakeAnySameAsAny guards that take_any is genuinely the modern,
// non-deprecated name for any — real ADX's own documented deprecated
// alias relationship, not a separate function.
func TestTakeAnySameAsAny(t *testing.T) {
	tbl := queryResult(t, `datatable(x:long) [1,2,3] | summarize take_any(x)`)
	if tbl.Schema.Columns[0].Name != "take_any_x" {
		t.Errorf("expected auto-name take_any_x (not any_x, matching real ADX's own distinct default naming for the modern vs deprecated form), got %q", tbl.Schema.Columns[0].Name)
	}
}

func TestTakeAnyifSameAsAnyif(t *testing.T) {
	tbl := queryResult(t, `datatable(x:long,keep:bool) [1,false,2,true] | summarize take_anyif(x, keep)`)
	expectCell(t, tbl, 0, 0, "2")
}

// TestCountDistinctExact guards count_distinct — real ADX's own
// EXACT distinct count, a genuinely different function from dcount
// (approximate), though aliased to identical logic here since this
// engine's own dcount was already exact, not approximate.
func TestCountDistinctExact(t *testing.T) {
	tbl := queryResult(t, `datatable(x:long) [1,2,2,3,3,3] | summarize count_distinct(x)`)
	expectCell(t, tbl, 0, 0, "3")
}

func TestCountDistinctifFiltersFirst(t *testing.T) {
	tbl := queryResult(t, `datatable(x:long,y:bool) [1,true,2,false,2,true,3,true] | summarize count_distinctif(x, y)`)
	expectCell(t, tbl, 0, 0, "3")
}

// TestPercentilesArrayCommaForm and TestPercentilesArrayDynamicForm
// guard both real-ADX documented argument shapes for
// percentiles_array produce identical results, and that the output
// column is correctly named "percentiles_Value" (NOT
// "percentiles_array_Value" -- a real naming bug found and fixed
// during this same work, verified directly against real ADX's own
// worked example before fixing).
func TestPercentilesArrayCommaForm(t *testing.T) {
	tbl := queryResult(t, `datatable(Value:real) [0.1,0.2,0.3,0.4,0.5,0.6,0.7,0.8,0.9,1.0] | summarize percentiles_array(Value, 5, 25, 50, 75, 95)`)
	if tbl.Schema.Columns[0].Name != "percentiles_Value" {
		t.Errorf("expected column named percentiles_Value, got %q", tbl.Schema.Columns[0].Name)
	}
	arr, ok := parseJSONArray(tbl.Rows[0][0])
	if !ok || len(arr) != 5 {
		t.Fatalf("expected a 5-element array, got: %v", tbl.Rows[0][0])
	}
}

func TestPercentilesArrayDynamicForm(t *testing.T) {
	commaResult := queryResult(t, `datatable(Value:real) [0.1,0.2,0.3,0.4,0.5,0.6,0.7,0.8,0.9,1.0] | summarize percentiles_array(Value, 5, 25, 50, 75, 95)`)
	dynResult := queryResult(t, `datatable(Value:real) [0.1,0.2,0.3,0.4,0.5,0.6,0.7,0.8,0.9,1.0] | summarize percentiles_array(Value, dynamic([5, 25, 50, 75, 95]))`)
	if commaResult.Rows[0][0] != dynResult.Rows[0][0] {
		t.Errorf("expected both argument forms to produce identical results, got %v vs %v", commaResult.Rows[0][0], dynResult.Rows[0][0])
	}
}

// TestVarianceifSampleVariance and TestVariancepifPopulationVariance
// guard both conditional variance functions against hand-calculated
// values: filtered set {2,4,6}, mean=4, sum of squared deviations=8.
// Sample variance (n-1 divisor) = 8/2 = 4. Population variance (n
// divisor) = 8/3.
func TestVarianceifSampleVariance(t *testing.T) {
	tbl := queryResult(t, `datatable(x:long,keep:bool) [2,true,3,false,4,true,5,false,6,true] | summarize varianceif(x, keep)`)
	expectCell(t, tbl, 0, 0, "4")
}

func TestVariancepifPopulationVariance(t *testing.T) {
	tbl := queryResult(t, `datatable(x:long,keep:bool) [2,true,3,false,4,true,5,false,6,true] | summarize variancepif(x, keep)`)
	got, ok := tbl.Rows[0][0].(float64)
	if !ok {
		t.Fatalf("expected a real result, got %T", tbl.Rows[0][0])
	}
	want := 8.0 / 3.0
	if got < want-0.0001 || got > want+0.0001 {
		t.Errorf("expected approximately %v, got %v", want, got)
	}
}

// TestMakeListWithNullsKeepsNulls guards real ADX's own documented
// distinction from make_list directly: "returns a list of all the
// values within the group, INCLUDING null values" — make_list itself
// silently drops a null row entirely.
func TestMakeListWithNullsKeepsNulls(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (x: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(x:string) ["a"]`)
	diskExec(t, eng, `.alter-merge table T (y: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(x:string, y:string) ["b","hasval"]`)

	withNulls := diskQuery(t, eng, `T | summarize make_list_with_nulls(y)`)
	arr, ok := parseJSONArray(withNulls.Rows[0][0])
	if !ok || len(arr) != 2 {
		t.Fatalf("expected 2 elements (including the null), got: %v", withNulls.Rows[0][0])
	}
	if arr[0] != nil {
		t.Errorf("expected the first element to be null, got %v", arr[0])
	}

	withoutNulls := diskQuery(t, eng, `T | summarize make_list(y)`)
	arr2, ok := parseJSONArray(withoutNulls.Rows[0][0])
	if !ok || len(arr2) != 1 {
		t.Errorf("expected make_list to drop the null entirely (1 element), got: %v", withoutNulls.Rows[0][0])
	}
}
