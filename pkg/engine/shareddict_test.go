package engine

import "testing"

// TestSharedDictSameProcessRefresh guards the ONE staleness guarantee
// this redesign still makes (see shareddict.go's file-level doc
// comment for what changed and why): a single, long-lived Engine that
// writes MORE data for a table+column after already caching that
// column's dictionary must see its own new entries on the next read,
// without restarting. This is the original TestSearchOperator bug
// (ingest, query, ingest more, query again — all one Engine, no
// concurrency at all) and it's not optional: SaveExtent's post-write
// cache refresh (storage.go) is what makes this hold, not
// getSharedDict re-checking on every read (that check no longer
// exists — see TestSharedDictCrossEngineNotAutoRefreshed for the
// limitation this implies).
func TestSharedDictSameProcessRefresh(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table X (Id: long, Host: string)`)
	// 6 rows cycling 2 distinct hosts (2/6 ratio), not 2 rows of 2
	// distinct hosts (1.0 ratio) — the latter looks structurally
	// unique within a single small write and correctly will NOT get
	// dictionary-encoded at all under the cardinality-ratio gate (see
	// resolveDictDecisions), which would make this test's own premise
	// (that Host IS dict-encoded) false before it gets anywhere near
	// the staleness question this test actually exercises.
	diskExec(t, eng, ".ingest inline into table X <| 1,host0\n2,host1\n3,host0\n4,host1\n5,host0\n6,host1")

	res := diskExec(t, eng, `X | summarize C = count() by Host | sort by Host asc`)
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 hosts before second ingest, got %d: %v", len(res.Rows), res.Rows)
	}

	// Same Engine, more data, introducing a genuinely new dictionary
	// value — the exact TestSearchOperator shape.
	diskExec(t, eng, ".ingest inline into table X <| 7,host2\n8,host2\n9,host2")

	res = diskExec(t, eng, `X | summarize C = count() by Host | sort by Host asc`)
	if len(res.Rows) != 3 {
		t.Fatalf("expected 3 hosts after second ingest on the SAME engine, got %d: %v", len(res.Rows), res.Rows)
	}
}

// TestSharedDictCrossEngineNotAutoRefreshed documents, as an explicit
// assertion rather than an implicit gap, the trade-off this redesign
// deliberately made: a DIFFERENT Engine instance (standing in for a
// different process, sharing the same on-disk scope) extending a
// dictionary is NOT picked up by an already-caching Engine without
// restarting. The earlier, sidecar-file version of this feature did
// guarantee this (re-checking a cheap on-disk commit marker on every
// read); this version doesn't, because that check is no longer cheap
// once dictionaries are a real, scannable table — see shareddict.go's
// file-level doc comment for the full reasoning. This test exists so
// a future change that silently regresses same-process refresh (the
// guarantee that IS still required — see
// TestSharedDictSameProcessRefresh) gets caught, while making clear
// this specific cross-engine gap is accepted, not overlooked.
func TestSharedDictCrossEngineNotAutoRefreshed(t *testing.T) {
	dir := t.TempDir()
	engA := discoverEngine(t, dir)
	diskExec(t, engA, `.create table X (Id: long, Host: string)`)
	diskExec(t, engA, ".ingest inline into table X <| 1,host0\n2,host1\n3,host0\n4,host1\n5,host0\n6,host1")

	// Populates engA's own dictCache["X.Host"]. 3, not 2: every
	// dict-ref decision unconditionally includes "" as a candidate
	// (see resolveDictDecisions) so it's guaranteed to occupy code 0 —
	// host0/host1 plus that reserved empty-string entry.
	sd, err := engA.getSharedDict("X", "Host")
	if err != nil {
		t.Fatalf("getSharedDict: %v", err)
	}
	if len(sd.values) != 3 {
		t.Fatalf("expected 3 cached values (host0, host1, \"\"), got %d: %v", len(sd.values), sd.values)
	}

	// A SEPARATE Engine instance, same on-disk scope, extends the
	// same dictionary — standing in for a different process.
	engB := discoverEngine(t, dir)
	diskExec(t, engB, ".ingest inline into table X <| 7,host2\n8,host2\n9,host2")

	// Engine A's cache is unaffected — this is the accepted
	// limitation, asserted explicitly rather than left undocumented.
	sd, err = engA.getSharedDict("X", "Host")
	if err != nil {
		t.Fatalf("getSharedDict after cross-engine extend: %v", err)
	}
	if len(sd.values) != 3 {
		t.Fatalf("expected engA's cache to stay at 3 values (accepted cross-engine staleness), got %d: %v",
			len(sd.values), sd.values)
	}

	// A fresh Engine instance against the same scope sees the full,
	// current state — restarting (a new Engine) is the workaround.
	// 4, not 3: host0, host1, host2, plus the reserved "" entry.
	engC := discoverEngine(t, dir)
	sd, err = engC.getSharedDict("X", "Host")
	if err != nil {
		t.Fatalf("getSharedDict on fresh engine: %v", err)
	}
	if len(sd.values) != 4 {
		t.Fatalf("expected a FRESH engine to see all 4 values, got %d: %v", len(sd.values), sd.values)
	}
}

// TestDictionariesTableSelfExclusion guards the deadlock/infinite-
// recursion risk flagged directly in shareddict.go's file-level doc
// comment: writing a dict-eligible column into _Dictionaries itself
// must never try to dictionary-encode _Dictionaries' own columns.
// Verified by checking the actual on-disk array kind, not just that
// the write succeeds without hanging (a hang would fail the test via
// Go's own test timeout, but wouldn't explain why) — a genuinely
// dict-ref-encoded column would decode as unsigned integers under
// isDictRefArray; a correctly-excluded one decodes as plain strings.
func TestDictionariesTableSelfExclusion(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table T (Id: string, Status: string)`)
	// Enough repeated values that Status WOULD normally dictionary-
	// encode, forcing _Dictionaries to actually be written to.
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Status:string) ["a","open","b","open","c","open","d","blocked"]`)

	res := diskExec(t, eng, `T | summarize C = count() by Status | sort by Status asc`)
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 distinct statuses, got %d: %v", len(res.Rows), res.Rows)
	}

	// _Dictionaries itself must now exist and be queryable normally —
	// if self-exclusion were broken, this table's own write would have
	// deadlocked before ever reaching this point. 3, not 2: "open",
	// "blocked", plus the unconditionally-reserved "" entry (see
	// resolveDictDecisions) — no row in this test's data has an empty
	// Status, but the dictionary always carries the entry regardless,
	// guaranteeing it occupies code 0.
	dictRows := diskExec(t, eng, `_Dictionaries | where TableName == "T" and ColumnName == "Status" | project Value`)
	if len(dictRows.Rows) != 3 {
		t.Fatalf("expected 3 dictionary entries for T.Status (open, blocked, \"\"), got %d: %v", len(dictRows.Rows), dictRows.Rows)
	}
}

// TestDictRefEmptyStringNeverPanicsOrCorrupts guards a real, severe bug
// found against live production data (a Claude-Memory scope's Edges
// table, Basis left null/empty by design for structural edges that
// don't carry a rationale): a dict-ref-encoded column whose null rows
// fell back to code 0 without any guarantee that code 0 was actually a
// safe, reserved value. Depending on extent layout this manifested two
// ways — silent value substitution (a null row decoding as whatever
// real value happened to occupy code 0) or an outright panic
// (index out of range in colVec.value, when the dictionary had zero
// entries at all). Fixed by having every dict-ref decision
// unconditionally include "" as a candidate value AND guaranteeing it
// gets the lowest newly-assigned code whenever it's genuinely new —
// not relying on Go's randomized map iteration order, which was the
// actual bug in the first version of this fix (verified live: ""
// landed at code 1 instead of 0 on a real write before this was
// caught).
func TestDictRefEmptyStringNeverPanicsOrCorrupts(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table T (Id: string, Val: string)`)

	// Enough real repeated values to pass the cardinality-ratio gate,
	// mixed with empty-string (-> null, per types.ParseValue's ""==null
	// rule) rows — the exact shape that silently corrupted before this
	// fix, verified via case comparison below, not just "didn't crash".
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Val:string) `+
		`["a","open","b","open","c","open","d","","e","","f","blocked","g","open","h",""]`)

	res := diskExec(t, eng, `T | sort by Id asc`)
	expectRows(t, res, 8)
	want := map[string]string{
		"a": "open", "b": "open", "c": "open", "d": "",
		"e": "", "f": "blocked", "g": "open", "h": "",
	}
	for _, row := range res.Rows {
		id := row[0].(string)
		got := ""
		if row[1] != nil {
			got = row[1].(string)
		}
		if got != want[id] {
			t.Errorf("row %s: expected Val=%q, got %q (empty-string/null corruption if this is a real, non-empty value)",
				id, want[id], got)
		}
	}

	// The specifically more severe case: an extent where EVERY row's
	// value is null. Before the fix this panicked outright (colVec.value
	// indexing into a zero-length dictVals) rather than merely
	// corrupting a value — verified live against a preserved repro
	// before this test was written.
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Val:string) ["i","","j","","k",""]`)
	res = diskExec(t, eng, `T | where Id in ("i","j","k")`)
	expectRows(t, res, 3)
	for _, row := range res.Rows {
		if row[1] != nil && row[1].(string) != "" {
			t.Errorf("expected empty/null Val for %v, got %q", row[0], row[1])
		}
	}
}
