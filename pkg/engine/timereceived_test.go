package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTimeReceivedHiddenFromBareScan guards the core visibility rule,
// verified against real Log Analytics/ADX's own documented behavior
// (quoted directly in timereceived.go): a bare table scan must not
// show _TimeReceived at all.
func TestTimeReceivedHiddenFromBareScan(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (X: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(X:string) ["a"]`)

	tbl := diskQuery(t, eng, `T`)
	if tbl.Schema.ColumnIndex("_TimeReceived") >= 0 {
		t.Fatalf("expected _TimeReceived hidden from a bare scan, got columns: %v", tbl.Schema.Columns)
	}
}

// TestTimeReceivedHiddenFromGetSchema guards getschema specifically —
// a separate code path from the column-stripping wrapper (see
// operators.go's applyGetSchema, whose output has no column literally
// named _TimeReceived at all; the name would otherwise appear as a
// ColumnName VALUE within a row).
func TestTimeReceivedHiddenFromGetSchema(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (X: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(X:string) ["a"]`)

	tbl := diskQuery(t, eng, `T | getschema`)
	nameIdx := tbl.Schema.ColumnIndex("ColumnName")
	for _, row := range tbl.Rows {
		if row[nameIdx] == "_TimeReceived" {
			t.Fatalf("expected _TimeReceived excluded from getschema's own output, found it")
		}
	}
}

// TestTimeReceivedVisibleWhenExplicitlyProjected guards the other
// half of the rule: an explicit reference reveals it.
func TestTimeReceivedVisibleWhenExplicitlyProjected(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (X: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(X:string) ["a"]`)

	tbl := diskQuery(t, eng, `T | project X, _TimeReceived`)
	if tbl.Schema.ColumnIndex("_TimeReceived") < 0 {
		t.Fatalf("expected _TimeReceived visible when explicitly projected, columns: %v", tbl.Schema.Columns)
	}
}

// TestTimeReceivedVisibleInArgMaxStarForm guards the actual motivating
// case this whole feature exists for: arg_max(_TimeReceived, *) by Id
// both ranks correctly AND surfaces _TimeReceived in the output,
// since it's explicitly named as the maximized column.
func TestTimeReceivedVisibleInArgMaxStarForm(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (X: string, Y: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(X:string, Y:long) ["a",1]`)
	diskExec(t, eng, `.set-or-append T <| datatable(X:string, Y:long) ["a",99]`)

	tbl := diskQuery(t, eng, `T | summarize arg_max(_TimeReceived, *) by X`)
	expectRows(t, tbl, 1)
	if tbl.Schema.ColumnIndex("_TimeReceived") < 0 {
		t.Fatalf("expected _TimeReceived in star-form arg_max output, columns: %v", tbl.Schema.Columns)
	}
	yIdx := tbl.Schema.ColumnIndex("Y")
	if tbl.Rows[0][yIdx] != int64(99) {
		t.Errorf("expected the most recently written row (Y=99) to win, got Y=%v", tbl.Rows[0][yIdx])
	}
}

// TestTimeReceivedSurvivesCompaction is the actual point of storing
// this as real per-row column data rather than deriving it from
// extent metadata (see timereceived.go's own doc comment, and
// compact.go's confirmed behavior of writing every merged extent
// under a single, fresh timestamp) -- a row's ORIGINAL write-time
// value must be unchanged after compaction, not silently replaced
// with the compaction's own time.
func TestTimeReceivedSurvivesCompaction(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (X: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(X:string) ["a"]`)

	before := diskQuery(t, eng, `T | project _TimeReceived`)
	beforeVal := before.Rows[0][0]

	diskExec(t, eng, `.compact table T`)

	after := diskQuery(t, eng, `T | project _TimeReceived`)
	afterVal := after.Rows[0][0]

	if beforeVal != afterVal {
		t.Errorf("expected _TimeReceived unchanged across compaction, before=%v after=%v", beforeVal, afterVal)
	}
}

// TestTimeReceivedScopeOptOut guards .okql-schema-options.json's
// disableTimeReceived — a scope that wants plain, unmodified
// KQL-over-files semantics with no engine-added columns at all.
func TestTimeReceivedScopeOptOut(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, schemaOptionsFileName), []byte(`{"disableTimeReceived": true}`), 0644); err != nil {
		t.Fatal(err)
	}
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (X: string)`)

	tableDef := eng.Catalog.GetTable("T")
	if tableDef.Schema.ColumnIndex("_TimeReceived") >= 0 {
		t.Fatalf("expected no _TimeReceived column at all with the scope opted out, got: %v", tableDef.Schema.Columns)
	}
}

// TestTimeReceivedPerTableOptOut guards the per-table
// with (notimereceived=true) override.
func TestTimeReceivedPerTableOptOut(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (X: string) with (notimereceived=true)`)

	tableDef := eng.Catalog.GetTable("T")
	if tableDef.Schema.ColumnIndex("_TimeReceived") >= 0 {
		t.Fatalf("expected no _TimeReceived column with the per-table opt-out, got: %v", tableDef.Schema.Columns)
	}
}

// TestTimeReceivedCSVIngestStillWorks guards the real bug found and
// fixed during this work: CSV ingest's field-count validation used to
// compare against the table's FULL schema column count, which would
// reject every CSV once _TimeReceived became automatic (a real CSV
// can never supply a value for an engine-generated column).
func TestTimeReceivedCSVIngestStillWorks(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (X: string, Y: long)`)

	csvPath := filepath.Join(dir, "data.csv")
	if err := os.WriteFile(csvPath, []byte("a,1\nb,2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	diskExec(t, eng, `.ingest csv into table T from "`+csvPath+`"`)

	tbl := diskQuery(t, eng, `T | count`)
	expectCell(t, tbl, 0, 0, "2")
}
