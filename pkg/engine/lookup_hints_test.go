package engine

import "testing"

// lookup_hints_test.go — lookup with a subquery right side, and
// distributed-execution hints accepted (and ignored) on summarize,
// join and lookup.

const lhT = `datatable(a:long, b:string)[1,"x",2,"y",3,"x"]`

func TestLookupSubqueryRightSide(t *testing.T) {
	r := queryResult(t, lhT+` | lookup (`+lhT+` | where a > 1 | project a, c = b) on a`)
	assertColumns(t, columnNames(&r.Schema), "a", "b", "c")
	assertRows(t, r.Rows, [][]interface{}{
		{int64(1), "x", nil}, {int64(2), "y", "y"}, {int64(3), "x", "x"},
	})
}

func TestLookupSubqueryInnerKind(t *testing.T) {
	r := queryResult(t, lhT+` | lookup kind=inner (`+lhT+` | where a > 1 | project a, c = b) on a | count`)
	if got := r.Rows[0][0]; got != int64(2) {
		t.Errorf("count = %v, want 2", got)
	}
}

// An "on" inside the right-side subquery belongs to the subquery.
func TestLookupSubqueryWithNestedJoin(t *testing.T) {
	r := queryResult(t, lhT+` | lookup (`+lhT+` | join kind=inner (`+lhT+`) on a | project a, c = b) on a | count`)
	if got := r.Rows[0][0]; got != int64(3) {
		t.Errorf("count = %v, want 3", got)
	}
}

func TestHintsIgnored(t *testing.T) {
	for _, q := range []string{
		lhT + ` | summarize hint.shufflekey=b n = count() by b | count`,
		lhT + ` | summarize hint.strategy=shuffle hint.num_partitions = 4 n = count() by b | count`,
		lhT + ` | join hint.strategy=broadcast (` + lhT + `) on a | distinct a | count`,
		lhT + ` | join kind=inner hint.remote=left (` + lhT + `) on a | distinct a | count`,
		lhT + ` | lookup hint.remote=local (` + lhT + ` | project a, c = b) on a | count`,
	} {
		r := queryResult(t, q)
		got := r.Rows[0][0]
		if got != int64(2) && got != int64(3) {
			t.Errorf("%s: count = %v", q, got)
		}
	}
}
