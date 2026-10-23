package engine

import "testing"

func TestDiffPatterns(t *testing.T) {
	src := `range i from 0 to 1999 step 1 | extend G=iff(i%3==0,"a","b"), A=tostring(iff(i%3==0, i%4, i%5)), B=tostring(iff(i%3==0 and i%7<3, "k", "m")), C=(i*7)%3 | project G, A, B, C`
	tbl := queryResult(t, src+` | evaluate diffpatterns(G, 'a', 'b')`)
	expectColNames(t, tbl, "SegmentId", "CountA", "CountB", "PercentA", "PercentB", "PercentDiffAB", "A", "B", "C")
	// the emulator's four segments lead: C=0 (all of A), C=1 and C=2
	// (half of B each; ADX writes them m,1 and m,2, the same rows), k
	want := [][]string{
		{"667", "0", "100", "", "", "0"},
		{"0", "667", "50.04", "", "", "1"},
		{"0", "666", "49.96", "", "", "2"},
		{"286", "0", "42.88", "", "k", ""},
	}
	for i, w := range want {
		expectCell(t, tbl, i, 1, w[0])
		expectCell(t, tbl, i, 2, w[1])
		expectCell(t, tbl, i, 5, w[2])
		expectCell(t, tbl, i, 6, w[3])
		expectCell(t, tbl, i, 7, w[4])
		expectCell(t, tbl, i, 8, w[5])
	}
	// a higher threshold keeps only the large differences
	tbl = queryResult(t, src+` | evaluate diffpatterns(G, 'a', 'b', '~', 0.45)`)
	expectRows(t, tbl, 3)
	// custom wildcards
	tbl = queryResult(t, src+` | evaluate diffpatterns(G, 'a', 'b', '~', 0.45, '~', '*', long(-1))`)
	expectCell(t, tbl, 0, 6, "*")
	expectCell(t, tbl, 1, 7, "*")
}
