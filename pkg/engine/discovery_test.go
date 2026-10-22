package engine

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// discoverEngine opens dir catalog-free.
func discoverEngine(t *testing.T, dir string) *Engine {
	t.Helper()
	cat, err := catalog.Discover(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	return New(cat)
}

// TestLegacyCatalogDatabase opens a database laid out by older
// versions: extents under extents/ and a catalog.json, here listing a
// table no extent file defines. Tables come from the files only; the
// catalog.json is not read.
func TestLegacyCatalogDatabase(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "extents"), 0755); err != nil {
		t.Fatal(err)
	}
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table E (Id: long, Host: string, Score: real)`)
	for ext := 0; ext < 3; ext++ {
		rows := make([]string, 0, 40)
		for i := 0; i < 40; i++ {
			id := ext*40 + i
			rows = append(rows, itoa(id)+",host"+itoa(id%4)+","+itoa(id)+".5")
		}
		diskExec(t, eng, ".ingest inline into table E <| "+joinLines(rows))
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "extents", "E_*.vtx")); len(m) == 0 {
		t.Fatal("extents not written under extents/")
	}

	queries := []string{
		`E | count`,
		`E | summarize C = count(), S = sum(Score) by Host | sort by Host asc`,
		`E | where Id > 100 | sort by Id desc | take 5`,
		`E | where Host == "host2" | summarize M = max(Id)`,
	}
	want := make([]string, len(queries))
	for i, q := range queries {
		want[i] = tableCSV(t, diskQuery(t, eng, q))
	}

	legacy := `{"name":"x","tables":{"Ghost":{"name":"Ghost","schema":{"Columns":[{"Name":"A","Type":1}]},"extents":[]}},"version":9}`
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	deng := New(cat)
	if got := deng.Catalog.ListTables(); len(got) != 1 || got[0] != "E" {
		t.Fatalf("tables: %v, want [E]", got)
	}
	for i, q := range queries {
		got := tableCSV(t, diskQuery(t, deng, q))
		if got != want[i] {
			t.Errorf("mismatch for %q:\nbefore:\n%s\nreopened:\n%s", q, want[i], got)
		}
	}
}

// TestDiscoveryTypeFidelity verifies kql.* extension identities
// recover exact KQL types — datetime, timespan, dynamic, guid — from
// footers alone.
func TestDiscoveryTypeFidelity(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Ts: datetime, Dur: timespan, Tags: dynamic, G: guid, N: long)`)
	diskExec(t, eng, `.ingest inline into table T <| 2024-01-15T10:30:00Z,00:05:00,"{""a"":1}",8f1c2d3e-0000-0000-0000-000000000001,42`)

	reopened := discoverEngine(t, dir)
	tbl := reopened.Catalog.GetTable("T")
	if tbl == nil {
		t.Fatal("table T not discovered")
	}
	// _TimeReceived included deliberately -- every table gets this
	// automatic column by default (see timereceived.go), so a
	// discovered table's own type fidelity check needs to account for
	// it too, not just the user-declared columns.
	wantTypes := map[string]string{"Ts": "datetime", "Dur": "timespan", "Tags": "dynamic", "G": "guid", "N": "long", "_TimeReceived": "datetime"}
	for _, col := range tbl.Schema.Columns {
		if got := col.Type.String(); got != wantTypes[col.Name] {
			t.Errorf("column %s: discovered type %s, want %s", col.Name, got, wantTypes[col.Name])
		}
	}

	// Datetime semantics must survive the round trip.
	res := diskQuery(t, reopened, `T | where Ts > datetime(2024-01-01) | count`)
	expectCell(t, res, 0, 0, "1")
}

// TestDiscoveryCreateTablePersists verifies a zero-row schema extent
// carries an empty table across reopen.
func TestDiscoveryCreateTablePersists(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table Empty (A: long, B: string)`)

	reopened := discoverEngine(t, dir)
	tbl := reopened.Catalog.GetTable("Empty")
	if tbl == nil {
		t.Fatal("empty table not discovered from schema extent")
	}
	// 3, not 2 -- the 2 declared columns plus the automatic
	// _TimeReceived column every table gets by default (see
	// timereceived.go).
	if len(tbl.Schema.Columns) != 3 {
		t.Fatalf("schema: %+v", tbl.Schema)
	}
	if len(tbl.Extents) != 0 {
		t.Fatalf("zero-row schema extent must not enter the scan list: %+v", tbl.Extents)
	}
	res := diskQuery(t, reopened, `Empty | count`)
	expectCell(t, res, 0, 0, "0")
}

// TestDiscoveryConcurrentIngest runs two engines against the same
// bare directory ingesting simultaneously; unique ids + atomic rename
// must make every row land with no coordination.
func TestDiscoveryConcurrentIngest(t *testing.T) {
	dir := t.TempDir()
	setup := discoverEngine(t, dir)
	diskExec(t, setup, `.create table C (Id: long, W: string)`)

	const writers, batches = 4, 6
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			eng := discoverEngine(t, dir)
			for b := 0; b < batches; b++ {
				rows := make([]string, 0, 10)
				for i := 0; i < 10; i++ {
					rows = append(rows, itoa(w*1000+b*10+i)+",w"+itoa(w))
				}
				stmt, err := parser.Parse(".ingest inline into table C <| " + joinLines(rows))
				if err != nil {
					errs[w] = err
					return
				}
				if _, err := eng.Execute(stmt); err != nil {
					errs[w] = err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	for w, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", w, err)
		}
	}

	reopened := discoverEngine(t, dir)
	res := diskQuery(t, reopened, `C | count`)
	expectCell(t, res, 0, 0, itoa(writers*batches*10))
	res = diskQuery(t, reopened, `C | summarize N = count() by W | sort by W asc`)
	expectRows(t, res, writers)
	expectCell(t, res, 0, 1, itoa(batches*10))
}

// TestMergeExtentsIsCompact: .merge table T extents, ADX's command,
// merges a table's extents the way .compact does.
func TestMergeExtentsIsCompact(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table M (A: long)`)
	diskExec(t, eng, `.ingest inline into table M <| 1`)
	diskExec(t, eng, `.ingest inline into table M <| 2`)
	diskExec(t, eng, `.merge table M extents`)
	fresh := discoverEngine(t, dir)
	if n := len(fresh.Catalog.GetTable("M").Extents); n != 1 {
		t.Fatalf("%d extents after .merge, want 1", n)
	}
	expectCell(t, diskQuery(t, fresh, `M | summarize S = sum(A)`), 0, 0, "3")
}

// TestParseExtentName pins the filename convention.
func TestParseExtentName(t *testing.T) {
	cases := []struct{ in, table, id string }{
		{"Events_0000000b.vtx", "Events", "0000000b"},
		{"My_Table_00ff.vtx", "My_Table", "00ff"},
		{"Events_18f2a9c04d3e1b7f9a2c4e01.vtx", "Events", "18f2a9c04d3e1b7f9a2c4e01"},
		{"noext_0b", "", ""},
		{"NoUnderscore.vtx", "", ""},
		{"Bad_suffixZ.vtx", "", ""},
		{"_0b.vtx", "", ""},
	}
	for _, c := range cases {
		table, id := catalog.ParseExtentNameForTest(c.in)
		if table != c.table || id != c.id {
			t.Errorf("%q: got (%q,%q) want (%q,%q)", c.in, table, id, c.table, c.id)
		}
	}
}

// TestDiscoveryConcurrentIngestStress is a genuinely higher-frequency
// stress test than TestDiscoveryConcurrentIngest (backlog P3 item 18):
// 20 writers x 25 batches x 20 rows = 10,000 rows via 500 concurrent
// extent-commit operations, checking not just that the final row count
// is exactly right but that no two extents anywhere in the table ever
// share an ID (the actual collision the nanosecond+random-suffix
// scheme is designed to make astronomically unlikely — this puts a
// real number behind that claim instead of just the probabilistic
// argument in newExtentID's own comment).
func TestDiscoveryConcurrentIngestStress(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test skipped in -short mode")
	}
	dir := t.TempDir()
	setup := discoverEngine(t, dir)
	diskExec(t, setup, `.create table S (Id: long, W: string)`)

	const writers, batches, rowsPerBatch = 20, 25, 20
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			eng := discoverEngine(t, dir)
			for b := 0; b < batches; b++ {
				rows := make([]string, 0, rowsPerBatch)
				for i := 0; i < rowsPerBatch; i++ {
					rows = append(rows, itoa(w*100000+b*1000+i)+",w"+itoa(w))
				}
				stmt, err := parser.Parse(".ingest inline into table S <| " + joinLines(rows))
				if err != nil {
					errs[w] = err
					return
				}
				if _, err := eng.Execute(stmt); err != nil {
					errs[w] = err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	for w, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", w, err)
		}
	}

	reopened := discoverEngine(t, dir)
	res := diskQuery(t, reopened, `S | count`)
	expectCell(t, res, 0, 0, itoa(writers*batches*rowsPerBatch))

	// No two extents anywhere in the table share an ID.
	seen := map[string]bool{}
	for _, ext := range reopened.Catalog.GetTable("S").Extents {
		if seen[ext.ID] {
			t.Fatalf("extent ID collision detected: %s", ext.ID)
		}
		seen[ext.ID] = true
	}
	if len(seen) != writers*batches {
		t.Errorf("expected %d distinct extents (one per batch), got %d", writers*batches, len(seen))
	}

	// Per-writer row counts must each be exact — catches any subtle
	// cross-writer data corruption that a bare total wouldn't reveal
	// (e.g. one writer's rows silently overwriting another's).
	perWriter := diskQuery(t, reopened, `S | summarize c = count() by W | sort by W asc`)
	expectRows(t, perWriter, writers)
	for _, row := range perWriter.Rows {
		if row[1].(int64) != int64(batches*rowsPerBatch) {
			t.Errorf("writer row count mismatch: %+v", row)
		}
	}
}
