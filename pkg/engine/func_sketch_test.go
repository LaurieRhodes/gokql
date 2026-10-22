package engine

import "testing"

// func_sketch_test.go — exact hll/tdigest families, dcount_intersect's
// reference example, and extend's in-order assignments.

func TestDcountIntersectReference(t *testing.T) {
	q := `range x from 1 to 100 step 1
| extend isEven = (x % 2 == 0), isMod3 = (x % 3 == 0), isMod5 = (x % 5 == 0)
| summarize hll_even = hll(iif(isEven, x, 0), 2), hll_mod3 = hll(iif(isMod3, x, 0), 2), hll_mod5 = hll(iif(isMod5, x, 0), 2)
| evaluate dcount_intersect(hll_even, hll_mod3, hll_mod5)
| project evenNumbers = s0 - 1, even_and_mod3 = s1 - 1, even_and_mod3_and_mod5 = s2 - 1`
	assertCSVRows(t, ipLookupRows(t, q), []string{"evenNumbers,even_and_mod3,even_and_mod3_and_mod5", "50,16,3"})
}

func TestSketchFamilies(t *testing.T) {
	q := `range x from 1 to 10 step 1 | extend g = x % 2
| summarize h = hll(x), hi = hll_if(x, x > 5), t = tdigest(x) by g
| summarize hm = hll_merge(h), tm = tdigest_merge(t), hh = hll_merge(hi)
| extend d = dcount_hll(hm), d2 = dcount_hll(hh), p = percentile_tdigest(tm, 50), pl = percentile_tdigest(tm, 50, typeof(long)),
	pa = percentiles_array_tdigest(tm, 10, 90), r = rank_tdigest(tm, 4), pr = percentrank_tdigest(tm, 4), tp = gettype(pl)
| project d, d2, p, pl, pa, r, pr, tp`
	assertCSVRows(t, ipLookupRows(t, q), []string{"d,d2,p,pl,pa,r,pr,tp", "10,5,5,5,[1,9],4,40,long"})
	// datetime samples stay exact to the tick; weights repeat a value
	q2 := `range x from 1 to 3 step 1 | extend d = datetime(2020-01-01 00:00:00.0000001) + x * 1d
| summarize t = tdigest(d), tw = tdigest(x, x)
| project q = percentile_tdigest(t, 50, typeof(datetime)), w = percentile_tdigest(tw, 50, typeof(long)), w2 = percentile_tdigest(merge_tdigest(tw, tw), 100, typeof(long))`
	assertCSVRows(t, ipLookupRows(t, q2), []string{"q,w,w2", "2020-01-03T00:00:00.0000001Z,2,3"})
	r := queryResult(t, `datatable(t:dynamic)[] | summarize tdigest_merge(t), hll_merge(t) | getschema | project ColumnName`)
	if formatCell(r, 0, 0) != "merge_tdigests_t" || formatCell(r, 1, 0) != "hll_merge_t" {
		t.Errorf("aggregate names: %s, %s", formatCell(r, 0, 0), formatCell(r, 1, 0))
	}
}

func TestExtendInOrder(t *testing.T) {
	assertCSVRows(t, ipLookupRows(t, `print x = 1 | extend a = x + 1, b = a + 1, x = x + 1, y = x * 10`),
		[]string{"x,a,b,y", "2,2,3,20"})
	// in order, so x takes the y just assigned
	assertCSVRows(t, ipLookupRows(t, `print x = 1, y = 5 | extend y = x, x = y`),
		[]string{"x,y", "1,1"})
}
