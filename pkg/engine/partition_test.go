package engine

import "testing"

// partition_test.go — the partition operator. Verified against real
// ADX's own partition operator docs before implementing.

func TestPartitionGroupsAndCounts(t *testing.T) {
	tbl := queryResult(t, `datatable(x:long, y:string) [1,"a",1,"b",2,"c",2,"d",2,"e"] | partition by x (count) | sort by Count asc`)
	expectRows(t, tbl, 2)
	expectCell(t, tbl, 0, 0, "2")
	expectCell(t, tbl, 1, 0, "3")
}

// TestPartitionMultiOperatorSubquery guards a multi-operator subquery
// pipeline (summarize | ...), not just a single, trivial operator.
func TestPartitionMultiOperatorSubquery(t *testing.T) {
	tbl := queryResult(t, `datatable(g:string, v:long) ["a",10,"a",30,"b",5,"b",100,"b",7] | partition by g (summarize total=sum(v) by g) | sort by g asc`)
	expectRows(t, tbl, 2)
	totalIdx := tbl.Schema.ColumnIndex("total")
	// sum() over long correctly promotes to real (float64), matching
	// real ADX's own sum() semantics — not int64.
	if tbl.Rows[0][totalIdx] != float64(40) || tbl.Rows[1][totalIdx] != float64(112) {
		t.Errorf("expected totals 40 (a) and 112 (b), got %v and %v", tbl.Rows[0][totalIdx], tbl.Rows[1][totalIdx])
	}
}

// TestPartitionHintStrategySkipped guards that hint.strategy=X (both
// spaced and unspaced around '=', both confirmed valid real-ADX
// syntax from the docs' own worked examples) is recognized and
// silently ignored, not rejected — this engine has no distributed
// execution to hint at all.
func TestPartitionHintStrategySkipped(t *testing.T) {
	unspaced := queryResult(t, `datatable(x:long) [1,2] | partition hint.strategy=native by x (count)`)
	spaced := queryResult(t, `datatable(x:long) [1,2] | partition hint.strategy = native by x (count)`)
	expectRows(t, unspaced, 2)
	expectRows(t, spaced, 2)
}

// TestPartitionExplicitSourceFormRejectedClearly guards that the
// deliberately out-of-scope "{SubQueryWithSource}" form gets a clear,
// explicit error, not silent mis-parsing into something else.
func TestPartitionExplicitSourceFormRejectedClearly(t *testing.T) {
	eng := diskEngineEmpty(t)
	_, err := runStmt(t, eng, `datatable(x:long) [1,2] | partition by x {datatable(y:long) [1]}`)
	if err == nil {
		t.Fatal("expected a clear error for the unsupported {SubQueryWithSource} form")
	}
}
