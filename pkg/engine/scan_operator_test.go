package engine

import "testing"

// scan_operator_test.go — the scan OPERATOR (| scan declare(...) with
// (step ...)), scoped to a single step (see ScanOp's own doc comment,
// pkg/parser/ast.go, for exactly why). Not to be confused with
// scan_test.go, which covers this engine's internal storage-scan
// strategies (ScanRowLimit, parallel extent scanning) — an unrelated,
// pre-existing meaning of "scan" in this codebase's own naming, kept
// as a separate file specifically to avoid conflating the two. Every
// test below is checked against a real, worked example from real
// ADX's own docs (scan-operator.md) with exact expected values, or
// hand-traced against the docs' own stated semantics where no single
// worked example covers a case.

// TestScanOperatorCumulativeSum guards real ADX's own primary scan
// worked example exactly: "Calculate the cumulative sum for an input
// column. The result of this example is equivalent to using
// row_cumsum()."
func TestScanOperatorCumulativeSum(t *testing.T) {
	result := queryResult(t, `range x from 1 to 5 step 1
		| scan declare (cumulative_x:long=0) with (
		    step s1: true => cumulative_x = x + s1.cumulative_x;
		  )`)
	want := []int64{1, 3, 6, 10, 15}
	if result.RowCount() != len(want) {
		t.Fatalf("expected %d rows, got %d", len(want), result.RowCount())
	}
	cIdx := result.Schema.ColumnIndex("cumulative_x")
	for i, w := range want {
		got := result.Rows[i][cIdx]
		gi, ok := got.(int64)
		if !ok || gi != w {
			t.Errorf("row %d cumulative_x = %v (%T), want %d", i, got, got, w)
		}
	}
}

// TestScanOperatorTwoColumnResetLogic guards real ADX's own second
// scan worked example: two independently-tracked cumulative columns,
// each resetting to the current record's value whenever its own
// running sum reaches 10 or more. Hand-traced against the docs' own
// stated example rather than a literal output table (not captured
// verbatim in the fetched docs page), verified arithmetically row by
// row.
func TestScanOperatorTwoColumnResetLogic(t *testing.T) {
	result := queryResult(t, `range x from 1 to 5 step 1
		| extend y = 2 * x
		| scan declare (cumulative_x:long=0, cumulative_y:long=0) with (
		    step s1: true => cumulative_x = iff(s1.cumulative_x >= 10, x, x + s1.cumulative_x),
		                      cumulative_y = iff(s1.cumulative_y >= 10, y, y + s1.cumulative_y);
		  )`)
	wantX := []int64{1, 3, 6, 10, 5}  // resets at row 5 (prev=10 >= 10)
	wantY := []int64{2, 6, 12, 8, 18} // resets at row 4 (prev=12 >= 10)
	if result.RowCount() != 5 {
		t.Fatalf("expected 5 rows, got %d", result.RowCount())
	}
	cxIdx := result.Schema.ColumnIndex("cumulative_x")
	cyIdx := result.Schema.ColumnIndex("cumulative_y")
	for i := 0; i < 5; i++ {
		if got := result.Rows[i][cxIdx].(int64); got != wantX[i] {
			t.Errorf("row %d cumulative_x = %v, want %d", i, got, wantX[i])
		}
		if got := result.Rows[i][cyIdx].(int64); got != wantY[i] {
			t.Errorf("row %d cumulative_y = %v, want %d", i, got, wantY[i])
		}
	}
}

// TestScanOperatorConditionFalseDropsRow confirms a non-matching row
// produces NO output row at all (not a null-filled row) — "A record
// for each MATCH of a record from the input to a step" per real ADX
// docs.
func TestScanOperatorConditionFalseDropsRow(t *testing.T) {
	result := queryResult(t, `range x from 1 to 5 step 1
		| scan declare (c:long=0) with ( step s1: x % 2 == 0 => c = x; )`)
	if result.RowCount() != 2 {
		t.Fatalf("expected 2 rows (only x=2,4 match), got %d", result.RowCount())
	}
	xIdx := result.Schema.ColumnIndex("x")
	if result.Rows[0][xIdx] != int64(2) || result.Rows[1][xIdx] != int64(4) {
		t.Errorf("expected x=2,4, got %v, %v", result.Rows[0][xIdx], result.Rows[1][xIdx])
	}
}

// TestScanOperatorOutputNone confirms output=none suppresses the
// output row.
func TestScanOperatorOutputNone(t *testing.T) {
	result := queryResult(t, `range x from 1 to 3 step 1
		| scan declare (c:long=0) with ( step s1 output=none: true => c = x + s1.c; )`)
	if result.RowCount() != 0 {
		t.Fatalf("expected 0 rows with output=none, got %d", result.RowCount())
	}
}

// TestScanOperatorMultiStepMatchId is the reference's "Events between
// Start and Stop" example: three steps and with_match_id.
func TestScanOperatorMultiStepMatchId(t *testing.T) {
	tbl := queryResult(t, `let Events = datatable (Ts: timespan, Event: string) [
    0m, "A", 1m, "Start", 2m, "B", 3m, "D", 4m, "Stop", 6m, "C", 8m, "Start", 11m, "E", 12m, "Stop"];
Events
| sort by Ts asc
| scan with_match_id=m_id with
(
    step s1: Event == "Start";
    step s2: Event != "Start" and Event != "Stop" and Ts - s1.Ts <= 5m;
    step s3: Event == "Stop" and Ts - s1.Ts <= 5m;
)`)
	want := [][2]string{{"Start", "0"}, {"B", "0"}, {"D", "0"}, {"Stop", "0"}, {"Start", "1"}, {"E", "1"}, {"Stop", "1"}}
	expectRows(t, tbl, len(want))
	for i, w := range want {
		expectCell(t, tbl, i, 1, w[0])
		expectCell(t, tbl, i, 2, w[1])
	}
}

// TestScanOperatorSessions is the reference's "Sessions tagging"
// example: Check 1 promotion through an output=none step starts a new
// match.
func TestScanOperatorSessions(t *testing.T) {
	tbl := queryResult(t, `let Events = datatable (Ts: timespan, Event: string) [
    0m, "A", 1m, "A", 2m, "B", 3m, "D", 32m, "B", 36m, "C", 38m, "D", 41m, "E", 75m, "A"];
Events
| sort by Ts asc
| scan with_match_id=session_id declare (sessionStart: timespan) with
(
    step inSession: true => sessionStart = iff(isnull(inSession.sessionStart), Ts, inSession.sessionStart);
    step endSession output=none: Ts - inSession.sessionStart > 30m;
)`)
	expectRows(t, tbl, 9)
	for i, w := range []string{"0", "0", "0", "0", "1", "1", "1", "1", "2"} {
		expectCell(t, tbl, i, 3, w)
	}
	expectCell(t, tbl, 4, 2, "00:32:00")
	expectCell(t, tbl, 8, 2, "01:15:00")
}

// TestScanOperatorAssignmentToUndeclaredColumnRejected confirms a
// clear error, not a silent no-op or panic, for an assignment
// targeting a column never named in declare(...).
func TestScanOperatorAssignmentToUndeclaredColumnRejected(t *testing.T) {
	queryError(t, `range x from 1 to 3 step 1
		| scan declare (c:long=0) with ( step s1: true => bogus = x; )`)
}
