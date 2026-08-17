package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// diskEngine creates a disk-backed engine with a Multi table of four
// extents (400 rows, Id 0..399), exercising the real Vortex scan path.
// The in-memory harness (datatable) never touches ScanExtent, so the
// parallel and early-exit strategies need this fixture — run under
// `go test -race` it also validates concurrent extent scanning.
func diskEngine(t *testing.T) *Engine {
	t.Helper()
	cat, err := catalog.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	eng := New(cat)

	exec := func(q string) *types.Table {
		t.Helper()
		stmt, err := parser.Parse(q)
		if err != nil {
			t.Fatalf("parse %q: %v", q, err)
		}
		result, err := eng.Execute(stmt)
		if err != nil {
			t.Fatalf("execute %q: %v", q, err)
		}
		return result
	}

	exec(`.create table Multi (Id: long, Host: string)`)
	for ext := 0; ext < 4; ext++ {
		var rows []string
		for i := 0; i < 100; i++ {
			id := ext*100 + i
			rows = append(rows, fmt.Sprintf("%d,host%d", id, id%7))
		}
		exec(".ingest inline into table Multi <| " + strings.Join(rows, "\n"))
	}
	return eng
}

func diskQuery(t *testing.T, eng *Engine, q string) *types.Table {
	t.Helper()
	stmt, err := parser.Parse(q)
	if err != nil {
		t.Fatalf("parse %q: %v", q, err)
	}
	result, err := eng.Execute(stmt)
	if err != nil {
		t.Fatalf("execute %q: %v", q, err)
	}
	return result
}

func TestParallelScanMatchesSequentialOrder(t *testing.T) {
	eng := diskEngine(t)

	// Full scan takes the parallel path (4 extents, no row limit).
	tbl := diskQuery(t, eng, `Multi | count`)
	expectCell(t, tbl, 0, 0, "400")

	// Order must be deterministic: extents merge in catalog order, so
	// the first and last rows match the sequential scan exactly.
	all := diskQuery(t, eng, `Multi | project Id`)
	expectRows(t, all, 400)
	expectCell(t, all, 0, 0, "0")
	expectCell(t, all, 399, 0, "399")
}

func TestScanRowLimitEarlyExit(t *testing.T) {
	eng := diskEngine(t)

	// take reachable through 1:1 operators: early-exit path.
	tbl := diskQuery(t, eng, `Multi | take 5`)
	expectRows(t, tbl, 5)

	tbl = diskQuery(t, eng, `Multi | project Id | take 3`)
	expectRows(t, tbl, 3)
	expectCell(t, tbl, 0, 0, "0")

	// A limit larger than the table returns everything.
	tbl = diskQuery(t, eng, `Multi | take 1000`)
	expectRows(t, tbl, 400)
}

func TestScanLimitNotPushedPastFilters(t *testing.T) {
	eng := diskEngine(t)

	// where precedes take: the scan must NOT stop early, or matching
	// rows in later extents would be lost. Id >= 350 lives only in the
	// final extent.
	tbl := diskQuery(t, eng, `Multi | where Id >= 350 | take 10`)
	expectRows(t, tbl, 10)

	// summarize precedes take: full scan required for correct groups.
	tbl = diskQuery(t, eng, `Multi | summarize C = count() by Host | take 100`)
	expectRows(t, tbl, 7)
}

func TestScanRowLimitAnalysis(t *testing.T) {
	parse := func(q string) []parser.Operator {
		t.Helper()
		stmt, err := parser.Parse(q)
		if err != nil {
			t.Fatalf("parse %q: %v", q, err)
		}
		return stmt.(*parser.Query).Operators
	}

	if limit, ok := ScanRowLimit(parse(`T | take 5`)); !ok || limit != 5 {
		t.Fatalf("take 5: got (%d, %v)", limit, ok)
	}
	if limit, ok := ScanRowLimit(parse(`T | project A | extend B = 1 | take 7`)); !ok || limit != 7 {
		t.Fatalf("1:1 chain: got (%d, %v)", limit, ok)
	}
	if _, ok := ScanRowLimit(parse(`T | where A > 1 | take 5`)); ok {
		t.Fatalf("where must block limit pushdown")
	}
	if _, ok := ScanRowLimit(parse(`T | sort by A asc | take 5`)); ok {
		t.Fatalf("sort must block limit pushdown")
	}
	if _, ok := ScanRowLimit(parse(`T | count`)); ok {
		t.Fatalf("count has no scan limit")
	}
}
