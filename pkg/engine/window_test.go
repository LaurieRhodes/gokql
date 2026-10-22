package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// window_test.go — window functions against the reference examples
// (row-cumsum, row-rank-dense, row-rank-min, row-number,
// row-window-session pages), nested window calls, and the use of window
// functions in extend, project and where.

func colInt64s(t *testing.T, r *types.Table, name string) []int64 {
	t.Helper()
	i := r.Schema.ColumnIndex(name)
	if i < 0 {
		t.Fatalf("no column %q", name)
	}
	out := make([]int64, len(r.Rows))
	for k, row := range r.Rows {
		v, ok := row[i].(int64)
		if !ok {
			t.Fatalf("%s row %d = %v (%T), want int64", name, k, row[i], row[i])
		}
		out[k] = v
	}
	return out
}

func wantInt64s(t *testing.T, label string, got, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d rows, want %d", label, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s row %d = %d, want %d", label, i, got[i], want[i])
		}
	}
}

func TestRowCumsumReference(t *testing.T) {
	r := queryResult(t, `datatable (a:long) [1, 2, 3, 4, 5, 6, 7, 8, 9, 10]
		| where a%2==0 | serialize cs=row_cumsum(a)`)
	wantInt64s(t, "cs", colInt64s(t, r, "cs"), []int64{2, 6, 12, 20, 30})

	r = queryResult(t, `datatable (name:string, month:int, salary:long)[
		"Alice", 1, 1000, "Bob", 1, 1000, "Alice", 2, 2000,
		"Bob", 2, 1950, "Alice", 3, 1400, "Bob", 3, 1450]
		| order by name asc, month asc
		| extend total=row_cumsum(salary, name != prev(name))`)
	wantInt64s(t, "total", colInt64s(t, r, "total"), []int64{1000, 3000, 4400, 1000, 2950, 4400})
}

const sea = `datatable (Airport:string, Airline:string, Departures:long)[
	"SEA", "LH", 3, "SEA", "LY", 100, "SEA", "UA", 3, "SEA", "BA", 2, "SEA", "EL", 3`

func TestRowRankDenseReference(t *testing.T) {
	r := queryResult(t, sea+`] | sort by Departures asc | extend Rank=row_rank_dense(Departures)`)
	wantInt64s(t, "Rank", colInt64s(t, r, "Rank"), []int64{1, 2, 2, 2, 3})
	r = queryResult(t, sea+`, "AMS", "EL", 1, "AMS", "BA", 1]
		| sort by Airport desc, Departures asc
		| extend Rank=row_rank_dense(Departures, prev(Airport) != Airport)`)
	wantInt64s(t, "Rank", colInt64s(t, r, "Rank"), []int64{1, 2, 2, 2, 3, 1, 1})
}

func TestRowRankMinReference(t *testing.T) {
	r := queryResult(t, sea+`] | sort by Departures asc | extend Rank=row_rank_min(Departures)`)
	wantInt64s(t, "Rank", colInt64s(t, r, "Rank"), []int64{1, 2, 2, 2, 5})
	r = queryResult(t, `datatable(Category:string, Item:string, Value:int)[
		"A", "item1", 10, "A","item2", 10, "A", "item3", 5, "A", "item4", 20,
		"B", "item2", 5, "B", "item1", 7]
		| sort by Category asc, Value asc
		| extend rank = row_rank_min(Value, Category != prev(Category))`)
	wantInt64s(t, "rank", colInt64s(t, r, "rank"), []int64{1, 2, 2, 4, 1, 2})
}

func TestRowNumberReference(t *testing.T) {
	r := queryResult(t, `datatable (Airport:string, Airline:string, Departures:long)[
		"TLV", "LH", 1, "TLV", "LY", 100, "SEA", "LH", 1, "SEA", "BA", 2, "SEA", "LY", 0]
		| sort by Airport asc, Departures desc
		| extend Rank=row_number(1, prev(Airport) != Airport)`)
	wantInt64s(t, "Rank", colInt64s(t, r, "Rank"), []int64{1, 2, 3, 1, 2})
	r = queryResult(t, `range a from 1 to 10 step 1 | sort by a desc | extend rn=row_number(7)`)
	wantInt64s(t, "rn", colInt64s(t, r, "rn"), []int64{7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
}

func TestRowWindowSessionReference(t *testing.T) {
	r := queryResult(t, `datatable (ID:string, Timestamp:datetime) [
		"1", datetime(2024-04-11 10:00:00), "2", datetime(2024-04-11 10:18:00),
		"1", datetime(2024-04-11 11:00:00), "3", datetime(2024-04-11 11:30:00),
		"2", datetime(2024-04-11 13:30:00), "2", datetime(2024-04-11 10:16:00)]
		| sort by ID asc, Timestamp asc
		| extend SessionStarted = row_window_session(Timestamp, 1h, 5m, ID != prev(ID))`)
	want := []string{"2024-04-11T10:00:00Z", "2024-04-11T11:00:00Z", "2024-04-11T10:16:00Z",
		"2024-04-11T10:16:00Z", "2024-04-11T13:30:00Z", "2024-04-11T11:30:00Z"}
	for i, w := range want {
		if got := formatCell(r, i, 2); got != w {
			t.Errorf("row %d SessionStarted = %s, want %s", i, got, w)
		}
	}
	if r.Schema.Columns[2].Type != types.TypeDatetime {
		t.Errorf("SessionStarted type = %v, want datetime", r.Schema.Columns[2].Type)
	}
}

// Window calls nested in expressions, in project and where, with
// prev/next offsets and defaults.
func TestWindowFunctionsInExpressions(t *testing.T) {
	r := queryResult(t, `range a from 1 to 5 step 1 | sort by a desc
		| extend rn = row_number(7), d = a - prev(a), n = next(a, 2, -1)
		| where prev(rn) > 7
		| project a, rn, n, twice = 2 * row_number()`)
	wantInt64s(t, "a", colInt64s(t, r, "a"), []int64{3, 2, 1})
	wantInt64s(t, "rn", colInt64s(t, r, "rn"), []int64{9, 10, 11})
	wantInt64s(t, "n", colInt64s(t, r, "n"), []int64{1, -1, -1})
	wantInt64s(t, "twice", colInt64s(t, r, "twice"), []int64{2, 4, 6})
}

// Zone-map pruning would remove rows before a window predicate reads
// them, so a where that uses one is never pushed down.
func TestZoneFilterSkipsWindowPredicates(t *testing.T) {
	schema := &types.Schema{Columns: []types.Column{{Name: "a", Type: types.TypeLong}}}
	stmt, err := parser.Parse(`T | where a > 200 and prev(a) == 200`)
	if err != nil {
		t.Fatal(err)
	}
	q := stmt.(*parser.Query)
	if f := extractZoneFilter(nil, q.Operators, schema); f != nil {
		t.Error("zone filter extracted from a predicate containing prev()")
	}
}
