package engine

import (
	"fmt"
	"strings"
	"testing"
)

func materializedViewTestScope(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Id: string, Status: string, Seq: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Status:string, Seq:long) `+
		`["a","open",1,"a","closed",3,"b","open",2]`)
	return eng
}

// TestCreateMaterializedViewStarForm guards the exact motivating case
// this feature exists for, against a REAL disk-backed table (not an
// in-memory datatable) -- the specific case that hid the planner bug
// TestSummarizeArgMaxStarAgainstRealTablePlannerBug (engine_test.go)
// guards directly. All payload columns must materialize, not just the
// group key and the maximized expression.
func TestCreateMaterializedViewStarForm(t *testing.T) {
	eng := materializedViewTestScope(t)
	diskExec(t, eng, `.create materialized-view LatestT on table T { T | summarize arg_max(Seq, *) by Id }`)

	tbl := diskQuery(t, eng, `LatestT | sort by Id asc`)
	expectRows(t, tbl, 2)
	statusIdx := tbl.Schema.ColumnIndex("Status")
	if statusIdx < 0 {
		t.Fatalf("Status column missing from materialized result entirely (columns: %v)", tbl.Schema.Columns)
	}
	if tbl.Rows[0][statusIdx] != "closed" {
		t.Errorf("expected row 0 (Id=a) Status=closed, got %v", tbl.Rows[0][statusIdx])
	}
	if tbl.Rows[1][statusIdx] != "open" {
		t.Errorf("expected row 1 (Id=b) Status=open, got %v", tbl.Rows[1][statusIdx])
	}
}

// TestCreateMaterializedViewPlainAggregate guards the non-star,
// ordinary aggregate case (count() with an alias), verified against
// real computed values, not just that creation succeeds.
func TestCreateMaterializedViewPlainAggregate(t *testing.T) {
	eng := materializedViewTestScope(t)
	diskExec(t, eng, `.create materialized-view StatusCounts on table T { T | summarize Total=count() by Status }`)

	tbl := diskQuery(t, eng, `StatusCounts | sort by Status asc`)
	expectRows(t, tbl, 2)
	totalIdx := tbl.Schema.ColumnIndex("Total")
	statusIdx := tbl.Schema.ColumnIndex("Status")
	if tbl.Rows[0][statusIdx] != "closed" || tbl.Rows[0][totalIdx] != int64(1) {
		t.Errorf("expected closed=1, got Status=%v Total=%v", tbl.Rows[0][statusIdx], tbl.Rows[0][totalIdx])
	}
	if tbl.Rows[1][statusIdx] != "open" || tbl.Rows[1][totalIdx] != int64(2) {
		t.Errorf("expected open=2, got Status=%v Total=%v", tbl.Rows[1][statusIdx], tbl.Rows[1][totalIdx])
	}
}

// TestMaterializedViewShowDisplaysRealQueryText guards that .show
// materialized-views displays the ACTUAL original query text, not a
// lossy reconstruction from the parsed AST -- a design mistake caught
// and fixed mid-implementation before this was ever tested (matching
// how stored functions' Body/ParametersText already work: keep the
// raw text, don't reconstruct it).
func TestMaterializedViewShowDisplaysRealQueryText(t *testing.T) {
	eng := materializedViewTestScope(t)
	diskExec(t, eng, `.create materialized-view with (docstring="Status counts") StatusCounts on table T { T | summarize Total=count() by Status }`)

	tbl := diskQuery(t, eng, `.show materialized-views`)
	expectRows(t, tbl, 1)
	queryIdx := tbl.Schema.ColumnIndex("Query")
	docIdx := tbl.Schema.ColumnIndex("DocString")
	if tbl.Rows[0][queryIdx] != `T | summarize Total=count() by Status` {
		t.Errorf("expected exact original query text, got %v", tbl.Rows[0][queryIdx])
	}
	if tbl.Rows[0][docIdx] != "Status counts" {
		t.Errorf("expected DocString to round-trip, got %v", tbl.Rows[0][docIdx])
	}
}

// TestMaterializedViewValidationRejectsUnsupportedAggregate guards the
// verified-against-real-ADX list of supported aggregation functions --
// percentile is genuinely NOT allowed inside a real materialized view
// at all (independent of this engine's own percentile-support gap).
func TestMaterializedViewValidationRejectsUnsupportedAggregate(t *testing.T) {
	eng := materializedViewTestScope(t)
	diskQueryError(t, eng, `.create materialized-view Bad on table T { T | summarize percentile(Seq, 50) by Id }`)
}

// TestMaterializedViewValidationRejectsSortInQuery guards the real,
// documented rule that sort/top/partition/serialize aren't supported
// inside a materialized view query.
func TestMaterializedViewValidationRejectsSortInQuery(t *testing.T) {
	eng := materializedViewTestScope(t)
	diskQueryError(t, eng, `.create materialized-view Bad on table T { T | sort by Seq desc | summarize count() by Id }`)
}

// TestMaterializedViewValidationRejectsSourceMismatch guards that the
// query must actually start from the declared source table.
func TestMaterializedViewValidationRejectsSourceMismatch(t *testing.T) {
	eng := materializedViewTestScope(t)
	diskExec(t, eng, `.create table Other (X: string)`)
	diskQueryError(t, eng, `.create materialized-view Bad on table T { Other | summarize count() }`)
}

// TestMaterializedViewValidationRequiresSummarize guards that a query
// with no summarize operator at all is rejected, not silently
// materialized as a plain filtered copy of the source table.
func TestMaterializedViewValidationRequiresSummarize(t *testing.T) {
	eng := materializedViewTestScope(t)
	diskQueryError(t, eng, `.create materialized-view Bad on table T { T | where Status == "open" }`)
}

// TestMaterializedViewValidationRequiresSummarizeLast guards that
// summarize must be the LAST operator — real ADX's own explicit rule.
func TestMaterializedViewValidationRequiresSummarizeLast(t *testing.T) {
	eng := materializedViewTestScope(t)
	diskQueryError(t, eng, `.create materialized-view Bad on table T { T | summarize count() by Id | where count_ > 0 }`)
}

// TestMaterializedViewIfNotExistsIsNoOp guards that ifnotexists on an
// existing view is a true no-op (the original materialized data is
// unaffected), not silently re-materialized under a different
// definition.
func TestMaterializedViewIfNotExistsIsNoOp(t *testing.T) {
	eng := materializedViewTestScope(t)
	diskExec(t, eng, `.create materialized-view V on table T { T | summarize count() by Id }`)
	diskExec(t, eng, `.create ifnotexists materialized-view V on table T { T | summarize Total=count() }`)

	// Still the ORIGINAL definition's shape (grouped by Id, 2 rows),
	// not the redefinition's (ungrouped, 1 row).
	tbl := diskQuery(t, eng, `V | count`)
	expectCell(t, tbl, 0, 0, "2")
}

// TestMaterializedViewTableNameCollisionRejected guards the namespace
// check against an existing, ordinary table — same reasoning as
// stored functions' equivalent check.
func TestMaterializedViewTableNameCollisionRejected(t *testing.T) {
	eng := materializedViewTestScope(t)
	diskQueryError(t, eng, `.create materialized-view T on table T { T | summarize count() }`)
}

// TestMaterializedViewDropRemovesQueryability guards that dropping a
// materialized view actually removes both the definition AND the
// materialized table itself — via the same archive-not-delete
// dropTableComplete this session already built for .drop table, so
// the underlying data remains recoverable, not a raw delete.
func TestMaterializedViewDropRemovesQueryability(t *testing.T) {
	eng := materializedViewTestScope(t)
	diskExec(t, eng, `.create materialized-view V on table T { T | summarize count() by Id }`)
	diskExec(t, eng, `.drop materialized-view V`)
	diskQueryError(t, eng, `V | count`)
}

// TestMaterializedViewDropMissingErrors guards that dropping a
// materialized view that was never defined is a clear error.
func TestMaterializedViewDropMissingErrors(t *testing.T) {
	eng := materializedViewTestScope(t)
	diskQueryError(t, eng, `.drop materialized-view NoSuchView`)
}

// --- Incremental maintenance ---
//
// Every test below creates the table AND the materialized view FIRST,
// with some initial data, then writes MORE data afterward and checks
// the view reflects it — unlike every test above this point, which
// (like materializedViewTestScope's own fixture) writes all its data
// BEFORE creating the view, so none of them exercise incremental
// maintenance at all (the view's compute-once-at-creation path is all
// they ever touch). Getting this distinction right mattered directly:
// an earlier version of the incremental-maintenance work looked
// correct against exactly that kind of test and was not — the actual
// bugs (a real deadlock, a silently-failing merge, and a genuine
// wrong-value correctness bug) only manifested on a SECOND write
// after creation, confirmed by hand before any of this was written as
// a test at all.

func materializedViewIncrementalTestScope(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Id: string, Status: string, Seq: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Status:string, Seq:long) `+
		`["a","open",1,"b","open",2]`)
	return eng
}

// TestMVIncrementalStarFormMergesExistingGroup guards the actual
// motivating case end to end: a write AFTER creation that updates an
// EXISTING group's winning row. Also guards the real correctness bug
// found and fixed during this work directly — the merged row's other
// columns (Status) must come from the SAME winning row as the
// maximized column (Seq), not be decided independently per column
// (an earlier version compared Status via its own, separate
// lexicographic comparison, producing Seq=3,Status="open" — a
// combination that never existed in any real row).
func TestMVIncrementalStarFormMergesExistingGroup(t *testing.T) {
	eng := materializedViewIncrementalTestScope(t)
	diskExec(t, eng, `.create materialized-view LatestT on table T { T | summarize arg_max(Seq, *) by Id }`)

	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Status:string, Seq:long) ["a","closed",3]`)

	tbl := diskQuery(t, eng, `LatestT | where Id == "a"`)
	expectRows(t, tbl, 1)
	seqIdx := tbl.Schema.ColumnIndex("Seq")
	statusIdx := tbl.Schema.ColumnIndex("Status")
	if tbl.Rows[0][seqIdx] != int64(3) {
		t.Errorf("expected Seq=3 (the new max), got %v", tbl.Rows[0][seqIdx])
	}
	if tbl.Rows[0][statusIdx] != "closed" {
		t.Errorf("expected Status=closed (from the SAME row as Seq=3, not merged independently), got %v", tbl.Rows[0][statusIdx])
	}
}

// TestMVIncrementalStarFormAddsNewGroup guards that a brand-new group
// key appearing in a post-creation write is added, not just merged
// into (or silently dropped from) existing groups.
func TestMVIncrementalStarFormAddsNewGroup(t *testing.T) {
	eng := materializedViewIncrementalTestScope(t)
	diskExec(t, eng, `.create materialized-view LatestT on table T { T | summarize arg_max(Seq, *) by Id }`)

	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Status:string, Seq:long) ["c","open",4]`)

	tbl := diskQuery(t, eng, `LatestT | count`)
	expectCell(t, tbl, 0, 0, "3")

	row := diskQuery(t, eng, `LatestT | where Id == "c"`)
	expectRows(t, row, 1)
}

// TestMVIncrementalStarFormPreservesUntouchedGroup guards that a
// group NOT present in a given write's delta is carried forward
// unchanged, not dropped or reset.
func TestMVIncrementalStarFormPreservesUntouchedGroup(t *testing.T) {
	eng := materializedViewIncrementalTestScope(t)
	diskExec(t, eng, `.create materialized-view LatestT on table T { T | summarize arg_max(Seq, *) by Id }`)

	// This write only touches "a" -- "b" must survive unchanged.
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Status:string, Seq:long) ["a","closed",3]`)

	tbl := diskQuery(t, eng, `LatestT | where Id == "b"`)
	expectRows(t, tbl, 1)
	seqIdx := tbl.Schema.ColumnIndex("Seq")
	if tbl.Rows[0][seqIdx] != int64(2) {
		t.Errorf("expected untouched group b to still have Seq=2, got %v", tbl.Rows[0][seqIdx])
	}
}

// TestMVIncrementalCountMerges guards a plain (non-star) trulyIncremental
// aggregate — count() must ADD the delta's partial count to the
// existing count, not replace or recompute it.
func TestMVIncrementalCountMerges(t *testing.T) {
	eng := materializedViewIncrementalTestScope(t)
	diskExec(t, eng, `.create materialized-view CountByStatus on table T { T | summarize Total=count() by Status }`)

	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Status:string, Seq:long) ["c","open",4,"d","closed",5]`)

	tbl := diskQuery(t, eng, `CountByStatus | where Status == "open"`)
	expectRows(t, tbl, 1)
	totalIdx := tbl.Schema.ColumnIndex("Total")
	if tbl.Rows[0][totalIdx] != int64(3) { // a, b (initial) + c (delta) = 3
		t.Errorf("expected merged Total=3 for open, got %v", tbl.Rows[0][totalIdx])
	}

	closedRow := diskQuery(t, eng, `CountByStatus | where Status == "closed"`)
	expectRows(t, closedRow, 1) // brand-new group from the delta
}

// TestMVIncrementalSumMerges guards sum() specifically, since its
// merge rule (addNumeric) is distinct code from count's. Initial data
// (a=1, b=2, both Status=open) sums to 3; the delta (c=10, Status=open)
// must be ADDED to that existing 3, not replace it or recompute from
// scratch, giving 13.
func TestMVIncrementalSumMerges(t *testing.T) {
	eng := materializedViewIncrementalTestScope(t)
	diskExec(t, eng, `.create materialized-view SumByStatus on table T { T | summarize Total=sum(Seq) by Status }`)

	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Status:string, Seq:long) ["c","open",10]`)

	tbl := diskQuery(t, eng, `SumByStatus | where Status == "open"`)
	expectRows(t, tbl, 1)
	totalIdx := tbl.Schema.ColumnIndex("Total")
	got := tbl.Rows[0][totalIdx]
	// Compared via formatted text, not a typed equality check: sum()
	// on a long column already produces a float64/real result in this
	// engine's own, pre-existing (and unrelated to this work) type
	// inference -- confirmed directly, not assumed, by checking a
	// plain non-MV summarize sum(Seq) | getschema shows System.Double
	// even with no materialized view involved at all. addNumeric's own
	// merge logic already handles this correctly by matching whatever
	// type the underlying aggregate actually produces.
	if fmt.Sprintf("%v", got) != "13" {
		t.Errorf("expected merged Total=13 (1+2 initial + 10 delta), got %v (%T)", got, got)
	}
}

// TestMVFallsBackToFullRecomputeForAvg guards the explicitly-documented
// scope boundary: avg is NOT in trulyIncrementalMVAggregates (needs
// hidden sum+count state this first version doesn't build), so it
// must fall back to a full recompute from the source table -- still
// CORRECT, just not truly incremental. Verified via the actual
// computed value, not just that it runs without error.
func TestMVFallsBackToFullRecomputeForAvg(t *testing.T) {
	eng := materializedViewIncrementalTestScope(t)
	diskExec(t, eng, `.create materialized-view AvgSeq on table T { T | summarize AvgVal=avg(Seq) }`)

	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Status:string, Seq:long) ["c","open",6]`)

	tbl := diskQuery(t, eng, `AvgSeq`)
	expectRows(t, tbl, 1)
	avgIdx := tbl.Schema.ColumnIndex("AvgVal")
	v := tbl.Rows[0][avgIdx]
	// (1 + 2 + 6) / 3 = 3.0 -- the CORRECT full-recompute answer, not
	// some incorrectly-merged partial average.
	f, ok := v.(float64)
	if !ok || f < 2.99 || f > 3.01 {
		t.Errorf("expected AvgVal ~3.0 (full recompute of 1,2,6), got %v", v)
	}
}

// TestMVIncrementalMakeSetUnions guards unionDynamicValues' make_set
// path specifically -- decode, de-duplicate union, re-encode. An
// earlier version of this function didn't actually decode or combine
// anything at all (silently discarded the existing state and returned
// only the delta's own partial result), caught and fixed before this
// was ever tested.
func TestMVIncrementalMakeSetUnions(t *testing.T) {
	eng := materializedViewIncrementalTestScope(t)
	diskExec(t, eng, `.create materialized-view StatusSet on table T { T | summarize Statuses=make_set(Status) }`)

	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Status:string, Seq:long) ["c","closed",6]`)

	tbl := diskQuery(t, eng, `StatusSet`)
	expectRows(t, tbl, 1)
	setIdx := tbl.Schema.ColumnIndex("Statuses")
	text := fmt.Sprintf("%v", tbl.Rows[0][setIdx])
	if !strings.Contains(text, "open") || !strings.Contains(text, "closed") {
		t.Errorf("expected the merged set to contain BOTH open (from initial data) and closed (from the delta), got %v", text)
	}
}

// TestMVReadWaitsForInFlightMerge is the single most important
// correctness property of the whole in-flight-tracking design: a read
// immediately following a write (whose maintenance goroutine may or
// may not have finished by the time the read runs, since maintenance
// is deliberately detached/async) must NEVER observe pre-merge, stale
// state. If this regresses, it regresses silently (a flaky-looking
// stale read, not a crash) -- exactly the kind of bug this test exists
// to pin down deterministically instead of leaving to chance timing.
func TestMVReadWaitsForInFlightMerge(t *testing.T) {
	eng := materializedViewIncrementalTestScope(t)
	diskExec(t, eng, `.create materialized-view LatestT on table T { T | summarize arg_max(Seq, *) by Id }`)

	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Status:string, Seq:long) ["a","closed",3]`)

	// No sleep, no retry loop -- the read itself must block until the
	// merge is done, via waitForMaterializedView. If it doesn't, this
	// is the specific test that would go flaky/fail depending on
	// scheduler timing rather than reliably pass or fail.
	tbl := diskQuery(t, eng, `LatestT | where Id == "a"`)
	expectRows(t, tbl, 1)
	statusIdx := tbl.Schema.ColumnIndex("Status")
	if tbl.Rows[0][statusIdx] != "closed" {
		t.Errorf("expected the read to wait for the in-flight merge and see Status=closed, got %v (stale pre-merge state)", tbl.Rows[0][statusIdx])
	}
}
