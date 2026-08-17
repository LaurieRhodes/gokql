package engine

import "testing"

// find_test.go — the find operator, the older cross-table search
// predecessor to search. Each test checks against real ADX's own
// documented worked examples directly, values included, not just
// "does it run without erroring".

func findFixtureScope(t *testing.T) *Engine {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table EventsTable1 (Session_Id: string, Level: string, EventText: string, Version: string)`)
	diskExec(t, eng, `.set-or-append EventsTable1 <| datatable(Session_Id:string, Level:string, EventText:string, Version:string) [
		"acbd207d-51aa-4df7-bfa7-be70eb68f04e","Information","Some Text1","v1.0.0",
		"acbd207d-51aa-4df7-bfa7-be70eb68f04e","Error","Some Text2","v1.0.0",
		"28b8e46e-3c31-43cf-83cb-48921c3986fc","Error","Some Text3","v1.0.1",
		"8f057b11-3281-45c3-a856-05ebb18a3c59","Information","Some Text4","v1.1.0"]`)
	diskExec(t, eng, `.create table EventsTable2 (Session_Id: string, Level: string, EventText: string, EventName: string)`)
	diskExec(t, eng, `.set-or-append EventsTable2 <| datatable(Session_Id:string, Level:string, EventText:string, EventName:string) [
		"f7d5f95f-f580-4ea6-830b-5776c8d64fdd","Information","Some Other Text1","Event1",
		"acbd207d-51aa-4df7-bfa7-be70eb68f04e","Information","Some Other Text2","Event2",
		"acbd207d-51aa-4df7-bfa7-be70eb68f04e","Error","Some Other Text3","Event3",
		"15eaeab5-8576-4b58-8fc6-478f75d8fee4","Error","Some Other Text4","Event4"]`)
	return eng
}

// TestFindExplicitProjectPackAll guards real ADX's own primary
// worked example directly, including the pack_ column's default name
// (found to be inconsistent with the docs' own prose, verified
// against two separate concrete worked examples instead).
func TestFindExplicitProjectPackAll(t *testing.T) {
	eng := findFixtureScope(t)
	tbl := diskQuery(t, eng, `find in (EventsTable1, EventsTable2) where Session_Id == 'acbd207d-51aa-4df7-bfa7-be70eb68f04e' and Level == 'Error' project EventText, Version, EventName, pack_all()`)
	expectRows(t, tbl, 2)
	if tbl.Schema.ColumnIndex("pack_") < 0 {
		t.Fatalf("expected a pack_ column, got: %v", tbl.Schema.Columns)
	}
	srcIdx := tbl.Schema.ColumnIndex("source_")
	textIdx := tbl.Schema.ColumnIndex("EventText")
	if tbl.Rows[0][srcIdx] != "EventsTable1" || tbl.Rows[0][textIdx] != "Some Text2" {
		t.Errorf("expected row 0 from EventsTable1/Some Text2, got %v/%v", tbl.Rows[0][srcIdx], tbl.Rows[0][textIdx])
	}
	if tbl.Rows[1][srcIdx] != "EventsTable2" || tbl.Rows[1][textIdx] != "Some Other Text3" {
		t.Errorf("expected row 1 from EventsTable2/Some Other Text3, got %v/%v", tbl.Rows[1][srcIdx], tbl.Rows[1][textIdx])
	}
}

// TestFindProjectSmartDefault guards the default project-smart output
// schema: columns explicit in the predicate + common columns as real
// output columns, everything else packed.
func TestFindProjectSmartDefault(t *testing.T) {
	eng := findFixtureScope(t)
	tbl := diskQuery(t, eng, `find Session_Id == 'acbd207d-51aa-4df7-bfa7-be70eb68f04e'`)
	expectRows(t, tbl, 4)
	for _, name := range []string{"source_", "Session_Id", "Level", "EventText", "pack_"} {
		if tbl.Schema.ColumnIndex(name) < 0 {
			t.Errorf("expected column %q in project-smart output, got: %v", name, tbl.Schema.Columns)
		}
	}
	// Version/EventName are table-specific, not common — must be
	// packed, not real output columns.
	if tbl.Schema.ColumnIndex("Version") >= 0 || tbl.Schema.ColumnIndex("EventName") >= 0 {
		t.Errorf("expected table-specific columns packed, not promoted, got: %v", tbl.Schema.Columns)
	}
}

// TestFindProjectPackAllDefaultName guards project pack_all() with no
// other columns listed — default column name "pack_", matching real
// ADX's own concrete worked example (not the docs' own, inconsistent
// prose claiming "column1").
func TestFindProjectPackAllDefaultName(t *testing.T) {
	eng := findFixtureScope(t)
	tbl := diskQuery(t, eng, `find Session_Id == 'acbd207d-51aa-4df7-bfa7-be70eb68f04e' project pack_all()`)
	expectRows(t, tbl, 4)
	if len(tbl.Schema.Columns) != 2 || tbl.Schema.Columns[1].Name != "pack_" {
		t.Errorf("expected exactly [source_, pack_], got: %v", tbl.Schema.Columns)
	}
}

// TestFindORPredicateAcrossDifferentColumns directly guards a real,
// live bug found and fixed while testing this against real ADX's own
// documented example: an earlier version required EVERY predicate-
// referenced column present in a table before scanning it at all,
// silently returning zero rows for an OR predicate spanning columns
// from different tables — real ADX's own docs explicitly explain each
// table is filtered by whichever part of the predicate it can
// actually evaluate, not all of it.
func TestFindORPredicateAcrossDifferentColumns(t *testing.T) {
	eng := findFixtureScope(t)
	tbl := diskQuery(t, eng, `find Version == 'v1.0.0' or EventName == 'Event1' project Session_Id, EventText, Version, EventName`)
	expectRows(t, tbl, 3)
	srcIdx := tbl.Schema.ColumnIndex("source_")
	table1Count, table2Count := 0, 0
	for _, row := range tbl.Rows {
		switch row[srcIdx] {
		case "EventsTable1":
			table1Count++
		case "EventsTable2":
			table2Count++
		}
	}
	if table1Count != 2 || table2Count != 1 {
		t.Errorf("expected 2 rows from EventsTable1 (Version match) and 1 from EventsTable2 (EventName match), got %d/%d", table1Count, table2Count)
	}
}

// TestFindWithsourceRenamesColumn guards the withsource=ColumnName
// option.
func TestFindWithsourceRenamesColumn(t *testing.T) {
	eng := findFixtureScope(t)
	tbl := diskQuery(t, eng, `find withsource=Origin in (EventsTable1) where Level == 'Error'`)
	if tbl.Schema.ColumnIndex("Origin") < 0 {
		t.Fatalf("expected the renamed source column 'Origin', got: %v", tbl.Schema.Columns)
	}
	if tbl.Schema.ColumnIndex("source_") >= 0 {
		t.Errorf("expected no default source_ column when withsource is used, got: %v", tbl.Schema.Columns)
	}
}

// TestFindTableNotContainingAnyPredicateColumnFilteredOut guards the
// real, correct condition (zero overlap, not full coverage): a table
// with genuinely NONE of the predicate's referenced columns is
// skipped entirely and doesn't error.
func TestFindTableNotContainingAnyPredicateColumnFilteredOut(t *testing.T) {
	eng := findFixtureScope(t)
	diskExec(t, eng, `.create table Unrelated (Foo: string)`)
	diskExec(t, eng, `.set-or-append Unrelated <| datatable(Foo:string) ["bar"]`)

	tbl := diskQuery(t, eng, `find in (EventsTable1, Unrelated) where Level == 'Error'`)
	srcIdx := tbl.Schema.ColumnIndex("source_")
	for _, row := range tbl.Rows {
		if row[srcIdx] == "Unrelated" {
			t.Error("expected Unrelated (no Level column at all) filtered out entirely")
		}
	}
}
