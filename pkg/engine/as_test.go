package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// as_test.go — the `as` operator. Verified against real ADX docs
// (as-operator.md: "Binds a name to the operator's input tabular
// expression... allows the query to reference the value of the
// tabular expression multiple times"). See AsOp's own doc comment
// (pkg/parser/ast.go) for exactly what's in scope here: same-query,
// downstream reference (a later join/union subquery within the SAME
// query referencing the bound name as a source) — NOT cross-statement
// (semicolon-separated) reference, and NOT the withsource=/source_/
// $table column-naming integration real ADX's own `as` also has.

// TestAsPassThrough confirms `as` doesn't alter the tabular result at
// all — a pure pass-through, matching its own documented purpose
// (binding a name, not transforming data).
func TestAsPassThrough(t *testing.T) {
	result := queryResult(t, `datatable(x:long)[1,2,3] | as T | count`)
	if result.Rows[0][0] != int64(3) {
		t.Errorf("count = %v, want 3 (as must not alter row count)", result.Rows[0][0])
	}
}

// TestAsSelfJoin guards the operator's own primary real-world purpose:
// referencing the bound name later in the SAME query, via a join's
// subquery, without repeating the upstream pipeline or breaking the
// query into a separate `let` binding.
func TestAsSelfJoin(t *testing.T) {
	result := queryResult(t, `datatable(id:long, v:string)[1,"a",2,"b"]
		| as T1
		| join kind=inner (T1) on id
		| project id, v, v1`)
	if result.RowCount() != 2 {
		t.Fatalf("expected 2 rows, got %d", result.RowCount())
	}
	idIdx := result.Schema.ColumnIndex("id")
	vIdx := result.Schema.ColumnIndex("v")
	v1Idx := result.Schema.ColumnIndex("v1")
	seen := map[int64]bool{}
	for _, row := range result.Rows {
		id := row[idIdx].(int64)
		seen[id] = true
		if row[vIdx] != row[v1Idx] {
			t.Errorf("id=%d: v=%v, v1=%v, want equal (self-join on the as-bound name)", id, row[vIdx], row[v1Idx])
		}
	}
	if !seen[1] || !seen[2] {
		t.Errorf("expected ids 1 and 2, got %v", seen)
	}
}

// TestAsUnion guards the same name-reuse purpose via union rather
// than join — a second, independent call site exercising the same
// e.letContext.Tables registration.
func TestAsUnion(t *testing.T) {
	result := queryResult(t, `datatable(x:long)[1,2]
		| as T
		| union (T | extend x = x + 10)
		| sort by x asc`)
	if result.RowCount() != 4 {
		t.Fatalf("expected 4 rows (2 original + 2 unioned), got %d", result.RowCount())
	}
}

// TestAsDoesNotLeakAcrossQueries guards the fix for a real race/leak
// risk found while implementing this: a name bound via `as` in one
// query must NOT still be resolvable as a table source in a LATER,
// separate query against the same long-lived Engine (REPL/server
// reuse) — executeQuery's own save/restore wrapper around AsOp-using
// queries exists specifically for this.
func TestAsDoesNotLeakAcrossQueries(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `datatable(x:long)[1,2,3] | as LeakedName | count`)
	// A second, separate query referencing LeakedName as a source must
	// fail (table not found), not resolve to the first query's bound
	// table.
	stmt, err := parser.Parse(`LeakedName | count`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, err = eng.Execute(stmt)
	if err == nil {
		t.Fatalf("expected 'table not found' for LeakedName in a later query, got no error (a leak across queries)")
	}
}

