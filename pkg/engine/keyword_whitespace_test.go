package engine

import "testing"

// keyword_whitespace_test.go — end-to-end regression tests for
// normalizeLeadingKeywordWhitespace (pkg/parser), confirming real
// queries formatted with a line break right after an operator keyword
// parse and execute correctly, not just the unit-level string
// transformation itself.

func TestPrintWithNewlineBeforeFirstArg(t *testing.T) {
	result := queryResult(t, "print\n\tr = 1")
	if result.Rows[0][0] != int64(1) {
		t.Errorf("got %v, want int64(1)", result.Rows[0][0])
	}
}

func TestWhereWithNewlineBeforePredicate(t *testing.T) {
	result := queryResult(t, "datatable(x:long)[1,2,3]\n| where\n\tx > 1")
	if result.RowCount() != 2 {
		t.Errorf("expected 2 rows (x=2,3), got %d", result.RowCount())
	}
}

func TestExtendWithNewlineBeforeAssignment(t *testing.T) {
	result := queryResult(t, "datatable(x:long)[1]\n| extend\n\ty = x + 1")
	yIdx := result.Schema.ColumnIndex("y")
	if result.Rows[0][yIdx] != int64(2) {
		t.Errorf("y = %v, want int64(2)", result.Rows[0][yIdx])
	}
}

func TestProjectAwayWithNewline(t *testing.T) {
	result := queryResult(t, "datatable(x:long,y:long)[1,2]\n| project-away\n\ty")
	if len(result.Schema.Columns) != 1 || result.Schema.Columns[0].Name != "x" {
		t.Errorf("expected only column x, got %+v", result.Schema.Columns)
	}
}

func TestMakeSeriesWithNewlineAfterKeyword(t *testing.T) {
	result := queryResult(t, "range x from 1 to 3 step 1\n| make-series\n\tavg(x) on x from 1 to 3 step 1")
	if result.RowCount() != 1 {
		t.Errorf("expected 1 row, got %d", result.RowCount())
	}
}

