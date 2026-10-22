package engine

import "testing"

// make_maxsize_test.go — the optional maxSize argument of make_list,
// make_set, make_bag and their variants (Kusto.Language signature:
// maxSize, integer, optional; reference default and maximum 1048576).
func TestMakeAggregatesMaxSize(t *testing.T) {
	const T = `datatable(a:long, k:string)[1,"x", 2,"y", 2,"z", 3,"w", 4,"v"]`
	cases := []struct{ agg, want string }{
		{`make_list(a, 2)`, `[1,2]`},
		{`make_set(a, 2)`, `[1,2]`},
		{`make_set(a, 3)`, `[1,2,3]`},
		{`make_list_if(a, a > 1, 2)`, `[2,2]`},
		{`make_set_if(a, a > 1, 2)`, `[2,3]`},
		{`make_list_with_nulls(a, 3)`, `[1,2,2]`},
		{`make_list(a)`, `[1,2,2,3,4]`},
		{`make_bag(bag_pack(k, a), 2)`, `{"x":1,"y":2}`},
		{`make_bag_if(bag_pack(k, a), a > 1, 1)`, `{"y":2}`},
	}
	for _, c := range cases {
		r := queryResult(t, T+` | summarize r = `+c.agg)
		if got := r.Rows[0][0]; got != c.want {
			t.Errorf("%s = %v, want %s", c.agg, got, c.want)
		}
	}
}

func TestMakeListMaxSizeInvalid(t *testing.T) {
	if _, err := queryErr(`datatable(a:long)[1] | summarize make_list(a, 0)`); err == nil {
		t.Error("expected an error for maxSize 0")
	}
}
