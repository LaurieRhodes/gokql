package engine

import "testing"

// sample_distinct_test.go — the sample-distinct operator. Verified
// against real ADX docs (sample-distinct-operator.md): "Returns a
// single column that contains up to the specified number of distinct
// values of the requested column." See SampleDistinctOp's own doc
// comment (pkg/parser/ast.go) for exactly what "up to N" means in
// this engine (a deterministic first-N-distinct-encountered scan, not
// randomized — real ADX itself documents no specific distribution to
// match, only that it isn't statistically fair).

// TestSampleDistinctBasic guards the primary documented shape: a
// single output column named after ColumnName, up to N distinct
// values.
func TestSampleDistinctBasic(t *testing.T) {
	result := queryResult(t, `datatable(x:long)[1,2,2,3,3,3,4,5] | sample-distinct 3 of x`)
	if result.RowCount() != 3 {
		t.Fatalf("expected 3 rows, got %d", result.RowCount())
	}
	if len(result.Schema.Columns) != 1 || result.Schema.Columns[0].Name != "x" {
		t.Fatalf("expected a single column named 'x', got %+v", result.Schema.Columns)
	}
	seen := map[int64]bool{}
	for _, row := range result.Rows {
		v, ok := row[0].(int64)
		if !ok {
			t.Fatalf("expected int64 value, got %T", row[0])
		}
		if seen[v] {
			t.Errorf("duplicate value %d in output — must be distinct", v)
		}
		seen[v] = true
	}
}

// TestSampleDistinctNMoreThanDistinctCount confirms requesting more
// distinct values than exist returns exactly the number that DO
// exist, not padded or errored.
func TestSampleDistinctNMoreThanDistinctCount(t *testing.T) {
	result := queryResult(t, `datatable(x:long)[1,2,2,3,3,3,4,5] | sample-distinct 100 of x`)
	if result.RowCount() != 5 {
		t.Fatalf("expected 5 rows (only 5 distinct values exist: 1,2,3,4,5), got %d", result.RowCount())
	}
}

// TestSampleDistinctZero confirms N=0 returns an empty (but
// schema-correct) result.
func TestSampleDistinctZero(t *testing.T) {
	result := queryResult(t, `datatable(x:long)[1,2,3] | sample-distinct 0 of x`)
	if result.RowCount() != 0 {
		t.Fatalf("expected 0 rows, got %d", result.RowCount())
	}
	if len(result.Schema.Columns) != 1 || result.Schema.Columns[0].Name != "x" {
		t.Errorf("expected schema to still have column 'x' even with 0 rows, got %+v", result.Schema.Columns)
	}
}

// TestSampleDistinctNegativeRejected confirms a negative
// NumberOfValues produces a clear error rather than an empty result
// or a panic.
func TestSampleDistinctNegativeRejected(t *testing.T) {
	queryError(t, `datatable(x:long)[1,2,3] | sample-distinct -1 of x`)
}

// TestSampleDistinctColumnNotFound confirms a clear error for a
// nonexistent column.
func TestSampleDistinctColumnNotFound(t *testing.T) {
	queryError(t, `datatable(x:long)[1,2,3] | sample-distinct 5 of nonexistent`)
}

// TestSampleDistinctStringColumn confirms the operator works over a
// non-numeric column type too — not accidentally numeric-only.
func TestSampleDistinctStringColumn(t *testing.T) {
	result := queryResult(t, `datatable(s:string)["a","b","a","c","b"] | sample-distinct 2 of s`)
	if result.RowCount() != 2 {
		t.Fatalf("expected 2 rows, got %d", result.RowCount())
	}
}

