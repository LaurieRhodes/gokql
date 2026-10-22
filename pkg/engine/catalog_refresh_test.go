package engine

import (
	"fmt"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
)

func countOf(t *testing.T, eng *Engine, table string) string {
	t.Helper()
	return fmt.Sprint(cellVal(t, diskQuery(t, eng, table+` | summarize C = count()`), 0, 0))
}

func ingestRows(t *testing.T, eng *Engine, table string, from, n int) {
	t.Helper()
	rows := make([]string, 0, n)
	for i := from; i < from+n; i++ {
		rows = append(rows, itoa(i)+",r"+itoa(i))
	}
	diskExec(t, eng, ".ingest inline into table "+table+" <| "+joinLines(rows))
}

// Two long-lived engines on one database each see the other's
// ingests, compaction and table creation at their next statement.
func TestLongLivedEnginesSeeEachOther(t *testing.T) {
	dir := t.TempDir()
	a, b := discoverEngine(t, dir), discoverEngine(t, dir)
	diskExec(t, a, `.create table T (Id: long, S: string)`)
	ingestRows(t, a, "T", 0, 10)
	if got := countOf(t, b, "T"); got != "10" {
		t.Fatalf("b sees %s rows after a's ingest, want 10", got)
	}
	// Two commits inside one directory-mtime tick are both seen.
	ingestRows(t, b, "T", 10, 5)
	ingestRows(t, a, "T", 15, 5)
	for name, eng := range map[string]*Engine{"a": a, "b": b, "fresh": discoverEngine(t, dir)} {
		if got := countOf(t, eng, "T"); got != "20" {
			t.Errorf("%s sees %s rows, want 20", name, got)
		}
	}
	diskExec(t, b, `.compact table T`)
	if got := countOf(t, a, "T"); got != "20" {
		t.Errorf("a sees %s rows after b's compaction, want 20 (no double count)", got)
	}
	if n := len(a.Catalog.GetTable("T").Extents); n != 1 {
		t.Errorf("a sees %d extents after compaction, want 1", n)
	}
	diskExec(t, b, `.create table U (Id: long, S: string)`)
	ingestRows(t, b, "U", 0, 3)
	if got := countOf(t, a, "U"); got != "3" {
		t.Errorf("a sees %s rows in b's new table, want 3", got)
	}
}

// After Refresh a discovery catalog equals a fresh Discover of the
// same directory: tables, schemas and extent lists.
func TestDiscoveryRefreshEqualsFreshDiscover(t *testing.T) {
	dir := t.TempDir()
	a := discoverEngine(t, dir)
	b := discoverEngine(t, dir)
	diskExec(t, a, `.create table T (Id: long, S: string)`)
	ingestRows(t, a, "T", 0, 4)
	diskExec(t, a, `.alter-merge table T (Extra: real)`)
	// b's statement refreshes first, so it writes the altered schema.
	// The empty fields are _TimeReceived (placed before Extra) and Extra.
	diskExec(t, b, ".ingest inline into table T <| 4,r4,,1.5\n5,r5,,2.5")
	if err := a.Catalog.Refresh(); err != nil {
		t.Fatal(err)
	}
	fresh, err := catalog.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range fresh.ListAllTables() {
		got, want := a.Catalog.GetTable(name), fresh.GetTable(name)
		if got == nil {
			t.Errorf("table %s missing after refresh", name)
			continue
		}
		if fmt.Sprint(got.Schema) != fmt.Sprint(want.Schema) {
			t.Errorf("%s schema: refreshed %v, fresh %v", name, got.Schema, want.Schema)
		}
		if fmt.Sprint(got.Extents) != fmt.Sprint(want.Extents) {
			t.Errorf("%s extents differ:\nrefreshed %v\nfresh     %v", name, got.Extents, want.Extents)
		}
	}
	if len(a.Catalog.ListAllTables()) != len(fresh.ListAllTables()) {
		t.Errorf("tables: refreshed %v, fresh %v", a.Catalog.ListAllTables(), fresh.ListAllTables())
	}
}
