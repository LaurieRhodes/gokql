package engine

// setappend_test.go — .set-or-append coverage: idiomatic
// datatable(...)[...] literal insertion as a CSV-free alternative to
// .ingest inline / .ingest csv. Motivated directly by two real CSV
// escaping failures found via the memory-scope pilot (a column-count
// mismatch that silently corrupted an Edges table, and free-text
// claims containing unescaped commas shifting later columns) — both
// failure classes are structurally impossible here, since KQL's own
// string-literal parser handles quoting rather than a comma-splitter.

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func runStmt(t *testing.T, eng *Engine, q string) (*types.Table, error) {
	t.Helper()
	stmt, err := parser.Parse(q)
	if err != nil {
		return nil, err
	}
	return eng.Execute(stmt)
}

func TestSetOrAppendDatatableLiteral(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table T (Id: string, Note: string, N: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id: string, Note: string, N: long) ["a", "hello, world", 1, "b", "quoted ""word"" here", 2]`)

	got := diskQuery(t, eng, `T | count`)
	expectCell(t, got, 0, 0, "2")

	got = diskQuery(t, eng, `T | where Id == "a" | project Note`)
	expectCell(t, got, 0, 0, "hello, world")
}

func TestSetOrAppendCreatesTableIfAbsent(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.set-or-append Fresh <| datatable(X: long, Y: string) [1, "one", 2, "two"]`)

	got := diskQuery(t, eng, `Fresh | count`)
	expectCell(t, got, 0, 0, "2")
}

func TestSetOrAppendMissingColumnErrors(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table T (Id: string, Note: string, N: long)`)

	_, err := runStmt(t, eng, `.set-or-append T <| datatable(Id: string, Note: string) ["a", "no N column"]`)
	if err == nil {
		t.Fatal("expected missing-column error, got success")
	}

	got := diskQuery(t, eng, `T | count`)
	expectCell(t, got, 0, 0, "0")
}

func TestSetOrAppendTypeMismatchErrors(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table T (Id: string, N: long)`)

	_, err := runStmt(t, eng, `.set-or-append T <| datatable(Id: string, N: string) ["a", "not a number"]`)
	if err == nil {
		t.Fatal("expected type-mismatch error, got success")
	}
}

// TestSetOrAppendColumnOrderIndependent: the datatable's column order
// need not match the table's — matched by name.
func TestSetOrAppendColumnOrderIndependent(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table T (Id: string, N: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(N: long, Id: string) [42, "reordered"]`)

	got := diskQuery(t, eng, `T | where Id == "reordered" | project N`)
	expectCell(t, got, 0, 0, "42")
}

// TestSetCreatesNewTable: .set on an absent table creates it and
// writes the query's rows — real Kusto create-only semantics.
func TestSetCreatesNewTable(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.set Fresh <| datatable(X: long) [1, 2, 3]`)
	got := diskQuery(t, eng, `Fresh | count`)
	expectCell(t, got, 0, 0, "3")
}

// TestSetOnExistingTableErrorsCleanly: .set was previously parsed but
// had no engine dispatch at all ("unsupported statement type" on any
// use). Now it must fail clearly and WITHOUT modifying the table when
// the target already exists, per real Kusto create-only semantics.
func TestSetOnExistingTableErrorsCleanly(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table T (X: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(X: long) [1]`)

	err := tryExec(t, eng, `.set T <| datatable(X: long) [2, 3]`)
	if err == nil {
		t.Fatal("expected .set on an existing table to error")
	}
	got := diskQuery(t, eng, `T | count`)
	expectCell(t, got, 0, 0, "1") // unchanged
}

// TestHelpListsCommands: .help must exist and return a real, queryable
// table covering at least the core dot-commands. Found live during the
// backlog pass — there was previously no in-tool way to discover
// .drop extent existed short of reading parser source.
func TestHelpListsCommands(t *testing.T) {
	eng := diskEngineEmpty(t)
	tbl := diskQuery(t, eng, `.help`)
	if len(tbl.Rows) < 10 {
		t.Fatalf(".help returned only %d rows, expected the full command list", len(tbl.Rows))
	}
	found := map[string]bool{}
	for _, row := range tbl.Rows {
		found[row[0].(string)] = true
	}
	for _, must := range []string{".set-or-append T <| query", ".drop extent <guid>", ".help"} {
		if !found[must] {
			t.Errorf(".help is missing an entry for %q", must)
		}
	}
}

// TestSearchOperator: search "term" scans string columns across every
// table without the caller naming which table/column holds the term —
// backlog P1 item 8, motivated by the retrieval-quality assessment
// finding every lexical query had to know its target table/column
// in advance.
func TestSearchOperator(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table A (Id: string, Note: string, N: long)`)
	diskExec(t, eng, `.set-or-append A <| datatable(Id:string, Note:string, N:long) ["a1", "mentions apricot here", 1, "a2", "no fruit mentioned", 2]`)
	diskExec(t, eng, `.create table B (Key: string, Text: string)`)
	diskExec(t, eng, `.set-or-append B <| datatable(Key:string, Text:string) ["b1", "an apricot orchard"]`)

	// Finds the term across BOTH tables with no table/column specified.
	tbl := diskQuery(t, eng, `search "apricot"`)
	expectRows(t, tbl, 2)

	// Scoped to one table.
	tbl = diskQuery(t, eng, `search in (A) "apricot"`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "A")

	// Word-bounded like has, not naive substring — "apricot" must not
	// match inside a longer unrelated word.
	diskExec(t, eng, `.set-or-append A <| datatable(Id:string, Note:string, N:long) ["a3", "notapricotrelated", 3]`)
	tbl = diskQuery(t, eng, `search "apricot"`)
	expectRows(t, tbl, 2) // still 2, not 3 — a3's note doesn't word-bound match

	// Real, further-pipeable table.
	tbl = diskQuery(t, eng, `search "apricot" | summarize c = count() by TableName | sort by TableName asc`)
	expectRows(t, tbl, 2)
	expectCell(t, tbl, 0, 0, "1")
	expectCell(t, tbl, 0, 1, "A")

	// Long, numeric-only, and dynamic columns are excluded from the
	// scan (only string/guid/dynamic columns are searched); confirms N
	// (long) never spuriously matches text terms.
	tbl = diskQuery(t, eng, `search "1"`)
	for _, row := range tbl.Rows {
		if row[1] == "N" {
			t.Errorf("search matched a long column: %+v", row)
		}
	}
}

// TestSearchWildcard guards a real, live silent-wrong-answer bug: `*`
// in a search term had no wildcard handling at all before this --
// search "Tammu*" looked for the literal six-character text "Tammu*"
// (asterisk included), matched nothing, and returned zero rows with
// no error, on a real corpus that genuinely contained "Tammuz".
// Trailing `*` -> prefix match and leading `*` -> suffix match are
// verified against real ADX docs (a trailing wildcard maps to
// hasprefix there); a wildcard on both ends collapses to plain,
// word-bounded search on the stripped middle, matching this engine's
// existing has semantics for the unwildcarded case -- not separately
// verified against ADX for that specific combination.
func TestSearchWildcard(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table A (Id: string, Note: string)`)
	diskExec(t, eng, `.set-or-append A <| datatable(Id:string, Note:string) `+
		`["a1", "the Tammuz cult spread west", "a2", "unrelated entry"]`)

	// Trailing wildcard -> prefix match.
	tbl := diskQuery(t, eng, `search "Tammu*"`)
	expectRows(t, tbl, 1)

	// Leading wildcard -> suffix match.
	tbl = diskQuery(t, eng, `search "*muz"`)
	expectRows(t, tbl, 1)

	// No wildcard, exact word -> still works (regression check).
	tbl = diskQuery(t, eng, `search "Tammuz"`)
	expectRows(t, tbl, 1)

	// A literal asterisk-free miss still correctly returns nothing.
	tbl = diskQuery(t, eng, `search "Nomatch*"`)
	expectRows(t, tbl, 0)

	// Both-ends wildcard is genuinely unbounded substring containment,
	// NOT word-boundary-aware has — a first version of this fix got
	// this wrong (collapsed to hasTerm), caught live: "*amm*" must
	// match "Tammuz" even though "amm" isn't a whole word or
	// boundary-aligned within it.
	tbl = diskQuery(t, eng, `search "*amm*"`)
	expectRows(t, tbl, 1)
}

// TestSearchUnknownTableErrors: search in (NoSuchTable) "x" must fail
// clearly, not silently return zero rows.
func TestSearchUnknownTableErrors(t *testing.T) {
	eng := diskEngineEmpty(t)
	_, err := runStmt(t, eng, `search in (NoSuchTable) "x"`)
	if err == nil {
		t.Fatal("expected an error for an unknown table")
	}
}

// TestPipedSimpleCommands: .show tables / .show database / .help
// support trailing pipeline operators — backlog item 19 (P1.5),
// found live: neither previously worked at all ("unknown command").
func TestPipedSimpleCommands(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table Alpha (X: long)`)
	diskExec(t, eng, `.create table Beta (Y: string)`)

	tbl := diskQuery(t, eng, `.show tables | where TableName == "Alpha"`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "Alpha")

	tbl = diskQuery(t, eng, `.help | where Command has "compact"`)
	expectRows(t, tbl, 1)

	tbl = diskQuery(t, eng, `.show database | project Tables`)
	expectCell(t, tbl, 0, 0, "2")
}

// TestPipedCommandDoesNotAffectCompoundCommands is the critical
// regression check: .set-or-append (and every other command with its
// own internal <| query grammar) must be completely unaffected by the
// new piping support — a bare '|' inside such a command's OWN query
// must still be parsed as part of THAT query, never misattributed to
// an outer pipeline continuation over the command's summary result.
func TestPipedCommandDoesNotAffectCompoundCommands(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table T (X: long, Y: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(X:long, Y:long) [1,2,3,4] | where X > 1`)

	// If the pipe had been misattributed to the OUTER result (the
	// {Result,ExtentId,RowsAppended} summary table), this would have
	// errored (no column X there) instead of correctly filtering the
	// INNER datatable literal down to one row before ingest.
	got := diskQuery(t, eng, `T | count`)
	expectCell(t, got, 0, 0, "1")
	got = diskQuery(t, eng, `T`)
	expectCell(t, got, 0, 0, "3")
}

// TestUnpipedDotCommandsUnaffected: every dot-command without a
// trailing pipe must behave exactly as before.
func TestUnpipedDotCommandsUnaffected(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table T (X: long)`)
	tbl := diskQuery(t, eng, `.show tables`)
	expectRows(t, tbl, 1)
}
