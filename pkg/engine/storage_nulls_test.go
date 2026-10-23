package engine

import (
	"fmt"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// storage_nulls_test.go — stored numeric, bool, datetime and timespan
// nulls survive the round trip to extents (they were written as 0 /
// false), through the row path and every columnar fast path.

// nullsEngine: table F (k:long, v:long, r:real, b:bool, d:datetime,
// s:string) in two extents of 100 rows; v, r, b and d are null where
// i%10 == 0, and v == 0 where i%10 == 5 (a real 0 next to the nulls).
func nullsEngine(t *testing.T) *Engine {
	return diskEngineWith(t, ".create table F (k:long, v:long, r:real, b:bool, d:datetime, s:string)", func(ext, i int) string {
		k := ext*100 + i
		if i%10 == 0 {
			return fmt.Sprintf("%d,,,,,s%d", k, i%3)
		}
		v := k
		if i%10 == 5 {
			v = 0
		}
		return fmt.Sprintf("%d,%d,%d.5,%v,2020-01-01T00:00:%02dZ,s%d", k, v, k, i%2 == 0, i%60, i%3)
	})
}

func TestStoredNullsRoundTrip(t *testing.T) {
	eng := nullsEngine(t)
	q := func(query string) *types.Table {
		t.Helper()
		return diskExec(t, eng, query)
	}
	one := func(query, want string) {
		t.Helper()
		got := cell(t, q(query), 0, 0)
		if got != want {
			t.Errorf("%s: got %s, want %s", query, got, want)
		}
	}
	one(`F | where isnull(v) | count`, "20")
	one(`F | where isnull(r) | count`, "20")
	one(`F | where isnull(b) | count`, "20")
	one(`F | where isnull(d) | count`, "20")
	one(`F | where v == 0 | count`, "20")  // the real zeros only
	one(`F | where v != 0 | count`, "180") // null != 0 is true
	one(`F | where v > -1 | count`, "180") // nulls fail ordering
	one(`F | where v < 1 | count`, "20")   // zeros, not nulls
	one(`F | summarize countif(isnull(v))`, "20")
	one(`F | summarize min(v)`, "0")
	one(`F | summarize avg(r)`, fmt.Sprint((func() float64 {
		sum, n := 0.0, 0
		for k := 0; k < 200; k++ {
			if k%10 != 0 {
				sum += float64(k) + 0.5
				n++
			}
		}
		return sum / float64(n)
	})()))
	one(`F | summarize dcount(b)`, "2")
	// A null group key stays apart from a real 0.
	one(`F | summarize c=count() by v | where isnull(v) | project c`, "20")
	one(`F | summarize c=count() by v | where v == 0 | project c`, "20")
	one(`F | where isnull(v) | summarize c=count() by s | count`, "3")
}
