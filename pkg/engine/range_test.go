package engine

import "testing"

// range_test.go — the range table-generating operator, verified
// against real ADX's own range operator docs before implementing:
// "Generates a single-column table of values... This operator
// doesn't take a tabular input... The values can't reference the
// columns of any table."
//
// A real, live gap noted (but not fixed) three times before this:
// during the original tabular function parameters work, during
// manual verification of that same feature, and again during a
// systematic review of it -- each time worked around with a real
// table or datatable literal instead, since range didn't exist at
// all.

func TestRangeBasicAscending(t *testing.T) {
	tbl := queryResult(t, `range x from 1 to 5 step 1`)
	expectRows(t, tbl, 5)
	expectCell(t, tbl, 0, 0, "1")
	expectCell(t, tbl, 4, 0, "5")
}

func TestRangeStepGreaterThanOne(t *testing.T) {
	tbl := queryResult(t, `range x from 0 to 10 step 3`)
	expectRows(t, tbl, 4) // 0, 3, 6, 9 -- 10 never exactly reached
	expectCell(t, tbl, 3, 0, "9")
}

func TestRangeNegativeStep(t *testing.T) {
	tbl := queryResult(t, `range x from 10 to 0 step -2`)
	expectRows(t, tbl, 6) // 10, 8, 6, 4, 2, 0
	expectCell(t, tbl, 0, 0, "10")
	expectCell(t, tbl, 5, 0, "0")
}

func TestRangeMismatchedDirectionIsEmptyNotError(t *testing.T) {
	tbl := queryResult(t, `range x from 5 to 1 step 1`)
	expectRows(t, tbl, 0)
}

func TestRangeZeroStepErrors(t *testing.T) {
	eng := diskEngineEmpty(t)
	_, err := runStmt(t, eng, `range x from 1 to 5 step 0`)
	if err == nil {
		t.Fatal("expected an error for step=0 (would never terminate)")
	}
}

// TestRangeDatetimeWithTimespanStep guards real ADX's own worked
// example directly: "range LastWeek from ago(7d) to now() step 1d" —
// 8 entries (both endpoints inclusive), the exact count the real docs
// show for their own 7-day-span example. Also exercises the
// nanosecond/100ns-tick unit conversion between datetime (start/stop)
// and timespan (step), both int64-backed but on DIFFERENT scales —
// reusing toNanos, the same helper this engine's own datetime+timespan
// arithmetic already relies on, not a second, separate conversion.
func TestRangeDatetimeWithTimespanStep(t *testing.T) {
	tbl := queryResult(t, `range LastWeek from ago(7d) to now() step 1d | count`)
	expectCell(t, tbl, 0, 0, "8")
}

// TestRangePipedToFurtherOperators guards composability — the actual
// point of this being a genuine tabular SOURCE, not just a
// standalone command.
func TestRangePipedToFurtherOperators(t *testing.T) {
	tbl := queryResult(t, `range x from 1 to 5 step 1 | extend y = x * 2`)
	expectRows(t, tbl, 5)
	yIdx := tbl.Schema.ColumnIndex("y")
	if tbl.Rows[0][yIdx] != int64(2) || tbl.Rows[4][yIdx] != int64(10) {
		t.Errorf("expected y = x*2 across the range, got first=%v last=%v", tbl.Rows[0][yIdx], tbl.Rows[4][yIdx])
	}
}

// TestRangeLetBound guards `let r = range ...; r | ...` — real ADX's
// own documented pattern (sample operator's own docs use exactly this
// shape), which needed range recognized in isTabularSource
// (parser.go), not just as a bare top-level query source.
func TestRangeLetBound(t *testing.T) {
	tbl := queryResult(t, `let r = range x from 1 to 5 step 1; r | count`)
	expectCell(t, tbl, 0, 0, "5")
}

// TestRangeAsTabularFunctionArgument is the actual, original
// motivating case: MyFilter((range x from 1 to 10 step 1), 9) is real
// ADX's own worked example for tabular stored-function parameters
// (verified against Microsoft's own user-defined-functions docs when
// that feature was first built) -- blocked purely because range
// itself didn't exist at all, not because of anything wrong with the
// tabular-parameter machinery itself.
func TestRangeAsTabularFunctionArgument(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function MyFilter(T:(x:long), v:long) { T | where x >= v }`)

	tbl := diskQuery(t, eng, `MyFilter((range x from 1 to 10 step 1), 9)`)
	expectRows(t, tbl, 2)
	expectCell(t, tbl, 0, 0, "9")
	expectCell(t, tbl, 1, 0, "10")
}

// TestRangeNamedTableNotMisparsed guards the word-boundary check in
// both isTabularSource and the main parser dispatch: a real table
// literally named "rangeEvents" must never be misparsed as the range
// operator.
func TestRangeNamedTableNotMisparsed(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table rangeEvents (Id: string)`)

	tbl := diskQuery(t, eng, `rangeEvents | count`)
	expectCell(t, tbl, 0, 0, "0")
}
