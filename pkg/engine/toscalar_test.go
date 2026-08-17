package engine

import (
	"sync"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// TestToScalarInLetBinding guards the single most common real-world
// toscalar() pattern, per Microsoft's own docs: `let x = toscalar(T
// | summarize ...); ...`.
func TestToScalarInLetBinding(t *testing.T) {
	tbl := queryResult(t, `let avgY = toscalar(datatable(y:long) [1,2,3,4,5] | summarize avg(y)); print result = avgY`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "3")
}

// TestToScalarInlineInWhere guards toscalar() nested inside a
// BinaryExpr within a where predicate — the case that specifically
// exercises substituteToScalars' recursive tree-rewriting, not just
// the top-level ToScalarExpr case.
func TestToScalarInlineInWhere(t *testing.T) {
	tbl := queryResult(t, `datatable(x:long) [1,5,10,15] | where x > toscalar(datatable(y:long) [1,2,3,4,5] | summarize avg(y))`)
	expectRows(t, tbl, 3) // 5, 10, 15 -- all > avg(1..5)=3
}

// TestToScalarInExtendAppliesToEveryRow guards that toscalar() is
// evaluated ONCE and broadcast to every row, not silently different
// per row — the actual point of substituteToScalars running before
// the row loop rather than being called from within evalExpr per row.
func TestToScalarInExtendAppliesToEveryRow(t *testing.T) {
	tbl := queryResult(t, `datatable(x:long) [1,2,3] | extend avgY = toscalar(datatable(y:long) [10,20,30] | summarize avg(y))`)
	expectRows(t, tbl, 3)
	avgIdx := tbl.Schema.ColumnIndex("avgY")
	for i := 0; i < 3; i++ {
		if got := tbl.Rows[i][avgIdx]; got != float64(20) {
			t.Errorf("row %d: expected avgY=20 (broadcast to every row), got %v", i, got)
		}
	}
}

// TestToScalarInProject guards the project (not just extend) code
// path — a separate call site with its own, independently-wired
// substituteToScalars call.
func TestToScalarInProject(t *testing.T) {
	tbl := queryResult(t, `datatable(x:long) [1,2,3] | project x, avgY = toscalar(datatable(y:long) [10,20,30] | summarize avg(y))`)
	expectRows(t, tbl, 3)
	avgIdx := tbl.Schema.ColumnIndex("avgY")
	if tbl.Rows[0][avgIdx] != float64(20) {
		t.Errorf("expected avgY=20, got %v", tbl.Rows[0][avgIdx])
	}
}

// TestToScalarEmptyResultIsNull guards real ADX's own documented
// behavior: an empty tabular result converts to null, not an error —
// a legitimate outcome (e.g. a filter matching nothing).
func TestToScalarEmptyResultIsNull(t *testing.T) {
	tbl := queryResult(t, `print result = toscalar(datatable(y:long) [1,2,3] | where y > 100)`)
	expectRows(t, tbl, 1)
	if tbl.Rows[0][0] != nil {
		t.Errorf("expected null for an empty toscalar() result, got %v", tbl.Rows[0][0])
	}
}

// TestToScalarConcurrentEnginesUseCorrectOwnEngine directly guards the
// real, live concurrency bug found and fixed during this work: an
// earlier design used a shared, package-level *Engine reference
// (matching activeLetContext's own pre-existing pattern), which
// go test -race caught as unsafe given this engine's actual, tested
// concurrency model (TestDiscoveryConcurrentIngestStress: multiple
// INDEPENDENT Engine instances run truly concurrently, each on its
// own goroutine). This test runs many goroutines, each with its OWN
// engine and its OWN, DIFFERENT toscalar() subquery value, and checks
// every single one gets its own correct answer — not just that no
// race is flagged (which -race on the suite already covers), but that
// the actual VALUES are never cross-contaminated between goroutines,
// the failure mode the original bug would have caused.
func TestToScalarConcurrentEnginesUseCorrectOwnEngine(t *testing.T) {
	const n = 30
	var wg sync.WaitGroup
	results := make([]int64, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dir := t.TempDir() // matches TestDiscoveryConcurrentIngestStress's own proven per-goroutine pattern exactly
			eng := discoverEngine(t, dir)
			q := `print result = toscalar(datatable(v:long) [` + itoa(i) + `] | summarize max(v))`
			stmt, err := parser.Parse(q)
			if err != nil {
				errs[i] = err
				return
			}
			tbl, err := eng.Execute(stmt)
			if err != nil {
				errs[i] = err
				return
			}
			if v, ok := tbl.Rows[0][0].(int64); ok {
				results[i] = v
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if results[i] != int64(i) {
			t.Errorf("goroutine %d: expected its own value %d, got %d -- cross-goroutine contamination", i, i, results[i])
		}
	}
}

// --- in (subquery) ---
//
// Detection works by TRYING the ordinary scalar comma-list parse
// first and falling back to a subquery only if that fails entirely —
// purely additive, so every pre-existing form (a literal scalar list,
// a single bare identifier treated as a let-bound TableRef) is
// guarded here too, verified directly rather than assumed unaffected.

// TestInSubqueryBasic guards the exact case reported as failing (a
// different model, Kimi): "X in (subquery)" with a datatable literal
// as the subquery.
func TestInSubqueryBasic(t *testing.T) {
	tbl := queryResult(t, `datatable(x:long) [1,2,3] | where x in (datatable(y:long) [2,3])`)
	expectRows(t, tbl, 2)
}

// TestInSubqueryNegated guards !in (subquery).
func TestInSubqueryNegated(t *testing.T) {
	tbl := queryResult(t, `datatable(x:long) [1,2,3] | where x !in (datatable(y:long) [2,3])`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "1")
}

// TestInSubqueryCaseInsensitive guards in~ (subquery).
func TestInSubqueryCaseInsensitive(t *testing.T) {
	tbl := queryResult(t, `datatable(x:string) ["A","B","C"] | where x in~ (datatable(y:string) ["a","c"])`)
	expectRows(t, tbl, 2)
}

// TestInSubqueryRealFilteredPipeline guards a real table with an
// actual filtering pipeline as the subquery (not just a bare
// datatable literal) — the shape of Kimi's own real, motivating use
// case, not just the minimal reproduction.
func TestInSubqueryRealFilteredPipeline(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table Scores (Id: string, Score: long)`)
	diskExec(t, eng, `.set-or-append Scores <| datatable(Id:string, Score:long) ["a",10,"b",90,"c",50]`)

	tbl := diskQuery(t, eng, `datatable(x:string) ["a","b","c"] | where x in (Scores | where Score > 40 | project Id)`)
	expectRows(t, tbl, 2)
}

// TestInScalarListUnaffected guards that the ordinary, pre-existing
// scalar-list form of in(...) is completely unaffected by the
// subquery-detection logic added alongside it — the actual point of
// the "try scalar first, fall back" design.
func TestInScalarListUnaffected(t *testing.T) {
	tbl := queryResult(t, `datatable(x:long) [1,2,3] | where x in (1, 3)`)
	expectRows(t, tbl, 2)
}

// TestInLetBoundTableRefUnaffected guards the pre-existing
// "X in (letBoundVar)" form (a single bare identifier treated as a
// let-bound TableRef) is also completely unaffected.
func TestInLetBoundTableRefUnaffected(t *testing.T) {
	tbl := queryResult(t, `let sub = datatable(y:long) [2,3]; datatable(x:long) [1,2,3] | where x in (sub)`)
	expectRows(t, tbl, 2)
}
