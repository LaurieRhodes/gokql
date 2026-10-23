package engine

import (
	"strings"
	"testing"
)

// sort/order by and top with expression keys and explicit null
// placement. Expected orders are the Kusto emulator's.
func TestSortByExpression(t *testing.T) {
	src := `datatable(a:long, s:string, n:long)[3,"b",long(null),1,"C",5,2,"a",2,4,"dd",7]`
	cases := []struct{ q, want string }{
		{`sort by a * -1`, "1,2,3,4"}, // was silently sorted by a
		{`order by tolower(s) asc`, "2,3,1,4"},
		{`sort by strlen(s) desc, a asc`, "4,1,2,3"},
		{`top 2 by strcat(s, "x") asc`, "1,2"}, // a comma inside the key
		{`sort by n asc`, "3,2,1,4"},           // nulls first ascending by default
		{`sort by n`, "4,1,2,3"},               // and last descending
		{`sort by n asc nulls last`, "2,1,4,3"},
		{`sort by n desc nulls first`, "3,4,1,2"},
		{`top 2 by n nulls first`, "3,4"},
		{`sort by ['a'] asc`, "1,2,3,4"},
	}
	for _, c := range cases {
		tbl := queryResult(t, src+" | "+c.q+" | project a")
		var got []string
		for i := range tbl.Rows {
			got = append(got, cell(t, tbl, i, 0))
		}
		if g := strings.Join(got, ","); g != c.want {
			t.Errorf("%s: got %s, want %s", c.q, g, c.want)
		}
	}
	queryError(t, src+` | top 2 by a, s`)
}

// On a stored table the columnar top-N path must leave expression keys
// to the row path.
func TestSortByExpressionStored(t *testing.T) {
	eng := nullsEngine(t)
	tbl := diskExec(t, eng, `F | top 3 by k * -1 | project k`)
	for i, want := range []string{"0", "1", "2"} {
		if got := cell(t, tbl, i, 0); got != want {
			t.Errorf("row %d: k = %s, want %s", i, got, want)
		}
	}
	tbl = diskExec(t, eng, `F | sort by v asc nulls last | take 1 | project v`)
	if got := cell(t, tbl, 0, 0); got != "0" {
		t.Errorf("sort by v asc nulls last: first v = %q, want 0", got)
	}
}
