package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCompactBasicMerge: N extents merge into 1, all rows preserved
// exactly (order-independent — compared as a set), old files renamed
// to .superseded rather than deleted — including the original
// .create table shell file, which is exactly as redundant as the 3
// row-bearing extents once the new, equally self-describing merged
// extent exists (see compact.go's "SCHEMA-ONLY SHELLS" doc comment).
// TestCompactDatabaseIncludesDictionaries guards the actual incident
// this command responds to: a real scope had 232 _Dictionaries
// extents while every OTHER table was already correctly compacted to
// 1 -- because nothing iterating .show tables' output (what a
// per-table "compact everything I can see" workflow naturally uses)
// could ever have known _Dictionaries existed at all. .compact
// database must include it by construction, not by remembering to.
func TestCompactDatabaseIncludesDictionaries(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table A (Id: string, Status: string)`)
	// Enough repeated values across enough rows to pass the
	// cardinality-ratio gate and force a real _Dictionaries write,
	// across two separate batches so _Dictionaries itself accumulates
	// more than one extent too, not just table A.
	diskExec(t, eng, `.set-or-append A <| datatable(Id:string, Status:string) `+
		`["a1","open","a1","open","a1","open","a2","blocked"]`)
	diskExec(t, eng, `.set-or-append A <| datatable(Id:string, Status:string) `+
		`["a3","open","a3","open","a3","open"]`)

	if got := countGlob(t, dir, "_Dictionaries_*.vtx"); got < 2 {
		t.Fatalf("test setup: expected _Dictionaries to have multiple extents before compacting, got %d", got)
	}

	result := diskQuery(t, eng, `.compact database`)
	sawDictionaries := false
	for _, row := range result.Rows {
		if row[0] == "_Dictionaries" {
			sawDictionaries = true
			if row[1] != "OK" {
				t.Errorf("expected _Dictionaries to be actually compacted (Result=OK), got %v", row[1])
			}
		}
	}
	if !sawDictionaries {
		t.Fatal("expected _Dictionaries to appear in .compact database's output, it did not")
	}

	if got := countGlob(t, dir, "_Dictionaries_*.vtx"); got != 1 {
		t.Errorf("expected _Dictionaries to have exactly 1 active extent after .compact database, got %d", got)
	}

	// Data correctness after compacting the table _Dictionaries backs.
	got := diskQuery(t, eng, `A | summarize c=count() by Status | sort by Status asc`)
	expectRows(t, got, 2)
	expectCell(t, got, 0, 0, "1")
	expectCell(t, got, 0, 1, "blocked")
	expectCell(t, got, 1, 0, "6")
	expectCell(t, got, 1, 1, "open")

	// Re-running on an already-compact scope must report that, not
	// error or redundantly rewrite anything.
	again := diskQuery(t, eng, `.compact database`)
	for _, row := range again.Rows {
		if row[1] != "already compact" {
			t.Errorf("expected every table already compact on second run, got %v for %v", row[1], row[0])
		}
	}
}

// TestGCDatabaseIncludesDictionaries mirrors the compact test above
// for .gc database — _Dictionaries' .superseded files must be
// collected and removed alongside every other table's, not silently
// left behind because they're outside .show tables' listing.
func TestGCDatabaseIncludesDictionaries(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table A (Id: string, Status: string)`)
	diskExec(t, eng, `.set-or-append A <| datatable(Id:string, Status:string) `+
		`["a1","open","a1","open","a1","open","a2","blocked"]`)
	diskExec(t, eng, `.set-or-append A <| datatable(Id:string, Status:string) `+
		`["a3","open","a3","open","a3","open"]`)
	diskExec(t, eng, `.compact database`)

	if got := countGlob(t, dir, "_Dictionaries_*.vtx.superseded"); got == 0 {
		t.Fatal("test setup: expected _Dictionaries to have .superseded files after .compact database")
	}

	result := diskQuery(t, eng, `.gc database`)
	sawDictionariesRemoval := false
	for _, row := range result.Rows {
		if row[0] == "_Dictionaries" {
			if filesRemoved, ok := row[1].(int64); ok && filesRemoved > 0 {
				sawDictionariesRemoval = true
			}
		}
	}
	if !sawDictionariesRemoval {
		t.Error("expected .gc database to report removed files for _Dictionaries")
	}

	if got := countGlob(t, dir, "_Dictionaries_*.vtx.superseded"); got != 0 {
		t.Errorf("expected 0 superseded _Dictionaries files remaining after .gc database, got %d", got)
	}

	// Data still correct after gc.
	got := diskQuery(t, eng, `A | count`)
	expectCell(t, got, 0, 0, "7")
}

func TestCompactBasicMerge(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Id: string, N: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, N:long) ["a", 1, "b", 2]`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, N:long) ["c", 3]`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, N:long) ["d", 4, "e", 5]`)

	before := diskQuery(t, eng, `T | sort by Id asc`)
	diskExec(t, eng, `.compact table T`)
	after := diskQuery(t, eng, `T | sort by Id asc`)

	if tableCSV(t, before) != tableCSV(t, after) {
		t.Fatalf("data changed across compaction:\nbefore:\n%s\nafter:\n%s", tableCSV(t, before), tableCSV(t, after))
	}
	expectRows(t, after, 5)

	supersededCount := countGlob(t, dir, "T_*.vtx.superseded")
	if supersededCount != 4 {
		t.Errorf("expected 4 superseded files (the 3 original data extents + the .create table shell), got %d", supersededCount)
	}
	// Real (row-bearing) live extents, per the engine's own logical
	// view. Exactly 1: the shell is gone too, not merely excluded from
	// this list — see TestCompactConsolidatesSchemaShell for a direct,
	// file-level assertion of that.
	if got := len(eng.Catalog.GetTable("T").Extents); got != 1 {
		t.Errorf("expected exactly 1 live extent after compaction, got %d", got)
	}
}

// TestCompactConsolidatesSchemaShell directly exercises the case that
// prompted this: examining a real memory-scope directory on disk and
// noticing every table always has (at minimum) two files — the
// zero-row shell .create table writes, and the actual data extent —
// even though the shell is provably redundant the moment any
// row-bearing extent exists (any extent's footer already carries the
// same schema). A single .set-or-append after .create table leaves
// exactly that pair; compacting should collapse it to just the one
// new, self-describing extent — not merge only the "real" extents and
// leave the shell behind, which the first version of this feature did.
func TestCompactConsolidatesSchemaShell(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Id: string, N: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, N:long) ["a", 1]`)

	// Exactly the shell + one data extent at this point — the pattern
	// observed in a real scope directory.
	if got := countGlob(t, dir, "T_*.vtx"); got != 2 {
		t.Fatalf("test setup: expected shell + 1 data extent (2 files), got %d", got)
	}

	result := diskQuery(t, eng, `.compact table T`)
	if got := result.Rows[0][0].(string); got != "OK" {
		t.Fatalf("expected OK (there WAS work to do: the shell), got %q: %v", got, result.Rows[0])
	}
	if got := result.Rows[0][2].(int64); got != 2 { // ExtentsBefore
		t.Errorf("ExtentsBefore: expected 2 (1 data extent + 1 shell), got %v", got)
	}
	if got := result.Rows[0][3].(int64); got != 2 { // ExtentsSuperseded
		t.Errorf("ExtentsSuperseded: expected 2, got %v", got)
	}

	// Exactly one active .vtx file survives: the new merged extent.
	if got := countGlob(t, dir, "T_*.vtx"); got != 1 {
		t.Errorf("expected exactly 1 active file after compaction, got %d", got)
	}
	if got := countGlob(t, dir, "T_*.vtx.superseded"); got != 2 {
		t.Errorf("expected 2 superseded files (data extent + shell), got %d", got)
	}

	after := diskQuery(t, eng, `T | sort by Id asc`)
	expectRows(t, after, 1)
	expectCell(t, after, 0, 0, "a")

	// A fresh engine (simulating a new process) must still see the
	// table (schema recovered from the surviving real extent, not a
	// dedicated shell) and its data correctly.
	fresh := discoverEngine(t, dir)
	freshResult := diskQuery(t, fresh, `T | sort by Id asc`)
	expectRows(t, freshResult, 1)
	expectCell(t, freshResult, 0, 0, "a")
}

// TestCompactThenFreshDiscoveryExcludesSuperseded is the real proof of
// the design: a FRESH engine (simulating a new process/session opening
// the scope from scratch, the way the CLI actually works) must see
// only the compacted data, with superseded files automatically
// excluded by the discovery glob alone — no special-case filtering
// logic needed anywhere in discover.go.
func TestCompactThenFreshDiscoveryExcludesSuperseded(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Id: string, N: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, N:long) ["a", 1]`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, N:long) ["b", 2]`)
	diskExec(t, eng, `.compact table T`)

	fresh := discoverEngine(t, dir)
	got := diskQuery(t, fresh, `T | sort by Id asc`)
	expectRows(t, got, 2) // not 4 — superseded originals must not double-count
	expectCell(t, got, 0, 0, "a")
	expectCell(t, got, 1, 0, "b")
}

// TestCompactAlreadyCompactIsNoOp: a table with exactly one live
// extent and no leftover shell file reports already-compact and
// changes nothing. Note this genuinely requires a PRIOR compaction
// pass first — a freshly-created-then-once-ingested table still has
// its .create table shell sitting alongside the one data extent (2
// files, not 1), which is itself real work for compact to do now (see
// TestCompactConsolidatesSchemaShell) — so the true no-op state is
// "already been compacted once," not "only ingested once."
func TestCompactAlreadyCompactIsNoOp(t *testing.T) {
	eng := discoverEngine(t, t.TempDir())
	diskExec(t, eng, `.create table T (Id: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string) ["a"]`)

	first := diskQuery(t, eng, `.compact table T`)
	if got := first.Rows[0][0].(string); got != "OK" {
		t.Fatalf("first compact: expected OK (shell file needs consolidating), got %q: %v", got, first.Rows[0])
	}

	second := diskQuery(t, eng, `.compact table T`)
	expectCell(t, second, 0, 0, "already compact")
}


// TestCompactRejectedOnCatalogMode: .compact must refuse cleanly on a
// legacy catalog-mode database, pointing at .merge instead.
func TestCompactRejectedOnCatalogMode(t *testing.T) {
	eng := diskEngineEmpty(t) // catalog mode, not discovery
	diskExec(t, eng, `.create table T (Id: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string) ["a"]`)

	_, err := runStmt(t, eng, `.compact table T`)
	if err == nil {
		t.Fatal("expected .compact to be rejected on a catalog-mode database")
	}
}

// TestCompactDoesNotTouchConcurrentlyAddedExtent: an extent added by
// another writer in the gap between snapshot and supersession must
// survive completely untouched — not merged, not renamed, and its
// rows must still be queryable afterward exactly as written. Under
// the revised (rename, not delete) design this is even more clearly
// safe than before: nothing is ever destroyed, only ever renamed, and
// only ever the exact snapshotted set.
func TestCompactDoesNotTouchConcurrentlyAddedExtent(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Id: string, N: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, N:long) ["a", 1]`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, N:long) ["b", 2]`)

	var concurrentExtentID string
	compactAfterSnapshotHook = func() {
		result := diskQuery(t, eng, `.set-or-append T <| datatable(Id:string, N:long) ["c", 3]`)
		concurrentExtentID = result.Rows[0][1].(string)
	}
	defer func() { compactAfterSnapshotHook = nil }()

	diskExec(t, eng, `.compact table T`)

	got := diskQuery(t, eng, `T | sort by Id asc`)
	expectRows(t, got, 3)
	expectCell(t, got, 2, 0, "c")
	expectCell(t, got, 2, 1, "3")

	found := false
	for _, ext := range eng.Catalog.GetTable("T").Extents {
		if ext.ID == concurrentExtentID {
			found = true
		}
	}
	if !found {
		t.Fatal("concurrently-added extent was removed from the live set by compaction")
	}
	// 1 merged + 1 concurrent, per the engine'''s logical view.
	if got := len(eng.Catalog.GetTable("T").Extents); got != 2 {
		t.Errorf("expected 2 live extents (1 merged + 1 concurrent), got %d", got)
	}
}

// TestGCRemovesSupersededFiles: .gc physically removes exactly the
// .superseded files for the named table, nothing else, and has no
// effect on query results (they were already excluded from scanning).
func TestGCRemovesSupersededFiles(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Id: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string) ["a"]`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string) ["b"]`)
	diskExec(t, eng, `.compact table T`)

	beforeGC := diskQuery(t, eng, `T | sort by Id asc`)
	gcResult := diskQuery(t, eng, `.gc table T`)
	afterGC := diskQuery(t, eng, `T | sort by Id asc`)

	if tableCSV(t, beforeGC) != tableCSV(t, afterGC) {
		t.Fatal(".gc changed query results — it must only touch already-invisible files")
	}
	if gcResult.Rows[0][1].(int64) != 3 {
		t.Errorf("expected 3 superseded files removed (2 data extents + the .create table shell), got %v", gcResult.Rows[0][1])
	}
	if countGlob(t, dir, "T_*.vtx.superseded") != 0 {
		t.Error("superseded files still present on disk after .gc")
	}
}

// TestGCWithNothingToCollectIsSafe: .gc on a table that was never
// compacted is a safe no-op.
func TestGCWithNothingToCollectIsSafe(t *testing.T) {
	eng := discoverEngine(t, t.TempDir())
	diskExec(t, eng, `.create table T (Id: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string) ["a"]`)

	result := diskQuery(t, eng, `.gc table T`)
	expectCell(t, result, 0, 1, "0")
}

func countGlob(t *testing.T, dir, pattern string) int {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	return len(m)
}


// TestCompactWhereDropsExcludedRows: a Where clause on .compact drops
// rows the predicate excludes, keeping only the rest, and reports
// RowsScanned/RowsDropped/RowsPreserved accurately.
func TestCompactWhereDropsExcludedRows(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Id: string, N: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, N:long) ["a", 1, "b", 2]`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, N:long) ["c", 3, "d", 4]`)

	result := diskQuery(t, eng, `.compact table T where N > 2`)
	if got := result.Rows[0][4].(int64); got != 4 {
		t.Errorf("RowsScanned: expected 4, got %v", got)
	}
	if got := result.Rows[0][5].(int64); got != 2 {
		t.Errorf("RowsDropped: expected 2, got %v", got)
	}
	if got := result.Rows[0][6].(int64); got != 2 {
		t.Errorf("RowsPreserved: expected 2, got %v", got)
	}

	after := diskQuery(t, eng, `T | sort by Id asc`)
	expectRows(t, after, 2)
	expectCell(t, after, 0, 0, "c")
	expectCell(t, after, 1, 0, "d")
}

// TestCompactWhereRewritesSingleExtent: a Where clause must NOT be
// short-circuited by the "already compact" (extent count <= 1) check —
// a single extent can still contain obsolete rows, and dropping them
// is the whole point of running compact with a Where clause. This is
// the exact regression the short-circuit's original form (unconditional
// on extent count alone) would have caused.
func TestCompactWhereRewritesSingleExtent(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Id: string, N: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, N:long) ["a", 1, "b", 2, "c", 3]`)

	// Exactly one extent exists at this point.
	if got := len(eng.Catalog.GetTable("T").Extents); got != 1 {
		t.Fatalf("test setup: expected 1 extent, got %d", got)
	}

	result := diskQuery(t, eng, `.compact table T where N >= 2`)
	if got := result.Rows[0][0].(string); got != "OK" {
		t.Fatalf("expected OK result even for a single-extent compact-with-where, got %q: %v", got, result.Rows[0])
	}
	if got := result.Rows[0][5].(int64); got != 1 {
		t.Errorf("RowsDropped: expected 1, got %v", got)
	}

	after := diskQuery(t, eng, `T | sort by Id asc`)
	expectRows(t, after, 2)
	expectCell(t, after, 0, 0, "b")
	expectCell(t, after, 1, 0, "c")
}

// TestCompactWhereCanDropAllRows: a Where clause matching zero rows
// produces a valid, empty (but still queryable, still correctly
// zero-row) table — the same mechanism persistDiscoverySchema already
// relies on for zero-row extents, exercised here via the compact path
// instead.
func TestCompactWhereCanDropAllRows(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Id: string, N: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, N:long) ["a", 1, "b", 2]`)

	result := diskQuery(t, eng, `.compact table T where N > 1000`)
	if got := result.Rows[0][6].(int64); got != 0 {
		t.Errorf("RowsPreserved: expected 0, got %v", got)
	}

	after := diskQuery(t, eng, `T | count`)
	expectCell(t, after, 0, 0, "0")
}

// TestCompactWhereWithLetBoundAntiJoin exercises the actual motivating
// use case: a memory scope's "supersession, never update" convention
// (see the nergal/baba scope skill files this pattern was validated
// against with real data) — a corrected row is a NEW row plus a
// `supersedes`-typed edge to the old one, and compact's Where clause
// can reference a let-bound anti-join table exactly the same way an
// ordinary | where operator already can.
func TestCompactWhereWithLetBoundAntiJoin(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table Findings (Id: string, Claim: string)`)
	diskExec(t, eng, `.create table Edges (Src: string, Dst: string, Rel: string)`)
	diskExec(t, eng, `.set-or-append Findings <| datatable(Id:string, Claim:string) ["f1", "original claim", "f2", "corrected claim", "f3", "unrelated claim"]`)
	diskExec(t, eng, `.set-or-append Edges <| datatable(Src:string, Dst:string, Rel:string) ["f2", "f1", "supersedes"]`)

	diskExec(t, eng, `let superseded = Edges | where Rel == "supersedes" | project Dst; .compact table Findings where Id !in (superseded)`)

	after := diskQuery(t, eng, `Findings | sort by Id asc`)
	expectRows(t, after, 2)
	expectCell(t, after, 0, 0, "f2")
	expectCell(t, after, 1, 0, "f3")

	// Edges itself was never touched — only Findings was compacted.
	edgeCount := diskQuery(t, eng, `Edges | count`)
	expectCell(t, edgeCount, 0, 0, "1")
}

// TestDropTableRemovesEverything guards a real, live bug: .drop table
// reported success but left the table's zero-row schema-shell extent
// behind (persistDiscoverySchema's file, deliberately excluded from
// catalog.GetTable(name).Extents by design — the same reason
// findActiveShellFiles/findAllTableFiles exist at all). A fresh
// discovery-mode re-open of the same directory still found the
// "dropped" table, with zero rows but genuinely still present.
func TestDropTableRemovesEverything(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table X (Id: string)`)
	diskExec(t, eng, `.set-or-append X <| datatable(Id:string) ["a","b"]`)

	if got := countGlob(t, dir, "X_*.vtx"); got != 2 {
		t.Fatalf("test setup: expected shell + 1 data extent (2 files), got %d", got)
	}

	diskExec(t, eng, `.drop table X`)

	if got := countGlob(t, dir, "X_*.vtx"); got != 0 {
		t.Errorf("expected 0 files remaining in the discoverable location after drop, got %d", got)
	}

	// The real symptom: a FRESH discovery-mode engine against the same
	// directory must not find the table at all, not find it with 0 rows.
	fresh := discoverEngine(t, dir)
	if got := fresh.Catalog.GetTable("X"); got != nil {
		t.Errorf("expected table X to be genuinely gone after drop, but a fresh discovery still found it: %+v", got)
	}

	// The whole point of archiving instead of deleting: the files must
	// still exist somewhere recoverable — .drop table causing real,
	// irrecoverable data loss (a different session reaching for it to
	// fix a single row and losing an entire table with no way back) is
	// exactly the incident this design change responds to.
	archived := countRecursiveGlob(t, filepath.Join(dir, ".dropped"), "X_*.vtx")
	if archived != 2 {
		t.Errorf("expected both files (shell + data extent) to be archived under .dropped/, found %d", archived)
	}
}

// TestDropTableRemovesSupersededToo guards the .compact-then-drop
// sequence: leftover .superseded files from a prior compact that was
// never .gc'd must also be removed by .drop table, not just the
// currently-active extent.
func TestDropTableRemovesSupersededToo(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table X (Id: string)`)
	diskExec(t, eng, `.set-or-append X <| datatable(Id:string) ["a","b"]`)
	diskExec(t, eng, `.set-or-append X <| datatable(Id:string) ["c"]`)
	diskExec(t, eng, `.compact table X`)

	if got := countGlob(t, dir, "X_*.vtx.superseded"); got == 0 {
		t.Fatal("test setup: expected some superseded files after compact")
	}

	diskExec(t, eng, `.drop table X`)

	if got := countGlob(t, dir, "X_*"); got != 0 {
		t.Errorf("expected 0 files (active or superseded) remaining in the discoverable location after drop, got %d", got)
	}

	// Both active and superseded files must be archived, not just
	// active ones — a partial archive would be a partial safety net.
	archived := countRecursiveGlob(t, filepath.Join(dir, ".dropped"), "X_*")
	if archived == 0 {
		t.Errorf("expected archived files (active and superseded) under .dropped/, found none")
	}
}

// TestDropTableArchiveIsActuallyRecoverable is the most direct proof
// of the whole point of this design: not just that archived files
// exist on disk somewhere, but that a session can point a fresh
// discovery-mode engine directly at the archive subdirectory and read
// the original data back out, exactly as it was before the drop. This
// is the concrete recovery path for the incident that motivated
// archiving over deletion in the first place — .drop table reached
// for to fix a single row, an entire table lost with the old,
// deletion-based version of this command. With archiving: still
// findable, still readable, by copying (or just pointing okql at) the
// .dropped/<table>_<id>/ subdirectory.
func TestDropTableArchiveIsActuallyRecoverable(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table X (Id: string, Note: string)`)
	diskExec(t, eng, `.set-or-append X <| datatable(Id:string, Note:string) ["a", "important data", "b", "also important"]`)

	diskExec(t, eng, `.drop table X`)

	matches, err := filepath.Glob(filepath.Join(dir, ".dropped", "X_*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly 1 archive subdirectory, found %d: %v", len(matches), matches)
	}
	archiveSubdir := matches[0]

	// Point a completely fresh engine directly at the archive location
	// and confirm the original rows read back correctly.
	recovery := discoverEngine(t, archiveSubdir)
	got := diskQuery(t, recovery, `X | sort by Id asc`)
	expectRows(t, got, 2)
	expectCell(t, got, 0, 0, "a")
	expectCell(t, got, 0, 1, "important data")
	expectCell(t, got, 1, 0, "b")
	expectCell(t, got, 1, 1, "also important")
}

// countRecursiveGlob counts files matching pattern anywhere under
// root, recursively — unlike countGlob, which only checks root
// itself. Used to verify archived files landed in a per-drop
// subdirectory under root without needing to know that subdirectory's
// generated (timestamp+random) name in advance.
func countRecursiveGlob(t *testing.T, root, pattern string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		matched, matchErr := filepath.Match(pattern, filepath.Base(path))
		if matchErr != nil {
			return matchErr
		}
		if matched {
			count++
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return count
}
