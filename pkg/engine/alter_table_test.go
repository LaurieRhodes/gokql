package engine

import "testing"

// TestAlterMergeTableAddsColumn guards the core capability, verified
// against real ADX's own .alter-merge table docs before building
// this: an existing table gains a new column, existing rows get null
// for it, new rows can populate it.
func TestAlterMergeTableAddsColumn(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (X: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(X:string) ["a"]`)

	diskExec(t, eng, `.alter-merge table T (Score: long)`)

	tableDef := eng.Catalog.GetTable("T")
	if tableDef.Schema.ColumnIndex("Score") < 0 {
		t.Fatalf("expected Score column added, got: %v", tableDef.Schema.Columns)
	}

	tbl := diskQuery(t, eng, `T | project X, Score`)
	expectRows(t, tbl, 1)
	scoreIdx := tbl.Schema.ColumnIndex("Score")
	if tbl.Rows[0][scoreIdx] != nil {
		t.Errorf("expected the pre-existing row's Score to be null, got %v", tbl.Rows[0][scoreIdx])
	}
}

// TestAlterMergeTableRequiresExistingTable guards real ADX's own
// documented distinction: unlike .create-merge table, .alter-merge
// table never creates a table that doesn't already exist.
func TestAlterMergeTableRequiresExistingTable(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskQueryError(t, eng, `.alter-merge table DoesNotExist (X: string)`)
}

// TestAlterMergeTableRejectsTypeConflict guards real ADX's own
// documented restriction: an existing column given a DIFFERENT type
// is a hard error ("use .alter column instead"), not silently
// accepted, coerced, or ignored.
func TestAlterMergeTableRejectsTypeConflict(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (X: string)`)
	diskQueryError(t, eng, `.alter-merge table T (X: long)`)
}

// TestAlterMergeTableMixedOldNewExtentsReadCorrectly is the actual
// motivating scenario this command exists for: a table with rows
// written BEFORE the alter-merge (old extents, genuinely missing the
// new column at the file level) and rows written AFTER (new extents,
// with real values) must both read back correctly in the SAME query
// — guards the real, live bug found and fixed alongside this command:
// vortex.Scan() itself hard-errors when asked for a column absent
// from a specific FILE's own physical schema, which every
// pre-alter-merge extent is by definition. Fixed in
// storage.go's openExtentChunks, which now filters the requested
// column list down to whatever a given file actually has before
// calling vf.Scan() at all, letting the (already correctly written,
// but previously unreachable) per-chunk "leave it nil" fallback in
// ScanExtent actually run.
func TestAlterMergeTableMixedOldNewExtentsReadCorrectly(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (X: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(X:string) ["old"]`) // pre-alter-merge extent

	diskExec(t, eng, `.alter-merge table T (Score: long)`)

	diskExec(t, eng, `.set-or-append T <| datatable(X:string, Score:long) ["new",42]`) // post-alter-merge extent

	tbl := diskQuery(t, eng, `T | project X, Score | sort by X asc`)
	expectRows(t, tbl, 2)
	xIdx := tbl.Schema.ColumnIndex("X")
	scoreIdx := tbl.Schema.ColumnIndex("Score")

	if tbl.Rows[0][xIdx] != "new" || tbl.Rows[0][scoreIdx] != int64(42) {
		t.Errorf("expected row 0 (X=new) Score=42, got X=%v Score=%v", tbl.Rows[0][xIdx], tbl.Rows[0][scoreIdx])
	}
	if tbl.Rows[1][xIdx] != "old" || tbl.Rows[1][scoreIdx] != nil {
		t.Errorf("expected row 1 (X=old, from the pre-alter-merge extent) Score=nil, got X=%v Score=%v", tbl.Rows[1][xIdx], tbl.Rows[1][scoreIdx])
	}
}

// TestAlterMergeTableUnblocksTimeReceivedRetrofit is the concrete,
// end-to-end scenario this whole command was built to unblock: a
// table created before the automatic _TimeReceived column existed
// (or one that opted out at creation time) can retroactively gain it,
// and the exact motivating pattern -- arg_max(_TimeReceived, *) by Id
// -- works correctly across a mix of pre- and post-retrofit rows.
func TestAlterMergeTableUnblocksTimeReceivedRetrofit(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Id: string, Status: string) with (notimereceived=true)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Status:string) ["a","old"]`)

	diskExec(t, eng, `.alter-merge table T (_TimeReceived: datetime)`)

	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Status:string) ["a","new"]`)

	tbl := diskQuery(t, eng, `T | summarize arg_max(_TimeReceived, *) by Id`)
	expectRows(t, tbl, 1)
	statusIdx := tbl.Schema.ColumnIndex("Status")
	// The post-retrofit row has a real, non-null _TimeReceived; the
	// pre-retrofit row's is null. arg_max's own null-handling (fixed
	// earlier this session) means a real value always outranks a null
	// one, so "new" must win here.
	if tbl.Rows[0][statusIdx] != "new" {
		t.Errorf("expected the post-retrofit row (with a real _TimeReceived) to win arg_max, got Status=%v", tbl.Rows[0][statusIdx])
	}
}

// --- null ordering in sort / top ---
//
// Guards a real, live bug found via a different model's testing
// (Kimi), against real data: null values sorted BACKWARDS from real
// Kusto's own documented rule ("nulls are smallest" -- asc puts them
// first, desc puts them last), traced to CompareValues (types.go)
// having its two nil-handling branches exactly swapped. Reproduced
// here via the same real-world mechanism that surfaces it in
// production: rows written BEFORE an .alter-merge table retrofit
// genuinely have a null value for the new column, mixed with rows
// written after that DO have a real value -- not a synthetic nil
// literal, the actual shape of the bug as reported.

// TestSortByDescPutsNullsLast guards the core, single-key case.
func TestSortByDescPutsNullsLast(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Id: string) with (notimereceived=true)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string) ["old1","old2"]`)
	diskExec(t, eng, `.alter-merge table T (X: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, X:long) ["new1",100]`)

	tbl := diskQuery(t, eng, `T | sort by X desc | project Id`)
	expectRows(t, tbl, 3)
	if tbl.Rows[0][0] != "new1" {
		t.Errorf("expected the real, non-null value (new1) first in desc order, got %v", tbl.Rows[0][0])
	}
	if tbl.Rows[2][0] == "new1" {
		t.Errorf("expected nulls last in desc order, but new1 (the only non-null row) wasn't")
	}
}

// TestSortByAscPutsNullsFirst guards the ascending direction.
func TestSortByAscPutsNullsFirst(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Id: string) with (notimereceived=true)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string) ["old1"]`)
	diskExec(t, eng, `.alter-merge table T (X: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, X:long) ["new1",100]`)

	tbl := diskQuery(t, eng, `T | sort by X asc | project Id`)
	expectRows(t, tbl, 2)
	if tbl.Rows[0][0] != "old1" {
		t.Errorf("expected the null row first in asc order, got %v", tbl.Rows[0][0])
	}
}

// TestTopAgreesWithSortByOnNulls directly guards that top N by X
// agrees with the semantically equivalent sort by X | take N — real
// ADX's own documented equivalence ("top 5 by name is equivalent to
// the expression sort by name | take 5 both from semantic and
// performance perspectives").
func TestTopAgreesWithSortByOnNulls(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Id: string) with (notimereceived=true)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string) ["old1","old2"]`)
	diskExec(t, eng, `.alter-merge table T (X: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, X:long) ["new1",100]`)

	topTbl := diskQuery(t, eng, `T | top 1 by X desc | project Id`)
	sortTbl := diskQuery(t, eng, `T | sort by X desc | take 1 | project Id`)
	expectRows(t, topTbl, 1)
	expectRows(t, sortTbl, 1)
	if topTbl.Rows[0][0] != sortTbl.Rows[0][0] {
		t.Errorf("expected top and sort-by|take to agree, got top=%v sort=%v", topTbl.Rows[0][0], sortTbl.Rows[0][0])
	}
	if topTbl.Rows[0][0] != "new1" {
		t.Errorf("expected the real, non-null value to win desc ranking, got %v", topTbl.Rows[0][0])
	}
}

// TestTopRejectsMultipleKeys guards a real, live bug found alongside
// the null-ordering one: parseTop used to accept "N by X desc, Y desc"
// silently, taking only the first key via a bare whitespace split with
// no validation of what followed it -- neither erroring nor behaving
// as a genuine multi-key sort. Real ADX's own top operator only ever
// supports a single ranking expression at all (verified directly
// against Microsoft's own docs), so this is now a clear, immediate
// parse error instead of silent mis-parsing.
func TestTopRejectsMultipleKeys(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (X: long, Y: long)`)
	diskQueryError(t, eng, `T | top 1 by X desc, Y desc`)
}
