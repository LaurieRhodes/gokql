package engine

import "testing"

func TestFlatStringArrayHas(t *testing.T) {
	cases := []struct {
		arr, v    string
		found, ok bool
	}{
		{`[]`, "a", false, true},
		{`["a","b"]`, "b", true, true},
		{`[ "a" , "b" ]`, "a", true, true},
		{`["a","b"]`, "c", false, true},
		{`["a\"b"]`, "a", false, false}, // escapes: decode instead
		{`["a",1]`, "1", false, false},  // non-string element
		{`["a",]`, "a", false, false},
		{`{"a":1}`, "a", false, false},
	}
	for _, c := range cases {
		found, ok := flatStringArrayHas(c.arr, c.v)
		if found != c.found || ok != c.ok {
			t.Errorf("%s has %q: got %v,%v want %v,%v", c.arr, c.v, found, ok, c.found, c.ok)
		}
	}
	// The fast path agrees with the decoding path.
	for _, q := range []string{
		`print set_has_element(dynamic(["x","y"]), "y")`,
		`print set_has_element(dynamic(["x",1]), "1")`,
		`print set_has_element(dynamic(["x\"y"]), "x\"y")`,
	} {
		if got := cell(t, queryResult(t, q), 0, 0); got != "true" {
			t.Errorf("%s: got %s", q, got)
		}
	}
}

// Ordinals follow NodeIDs (node-table rows first) and adjacency keeps
// edge order.
func TestGraphIndexOrder(t *testing.T) {
	tbl := queryResult(t, `let N = datatable(id:long, n:string)[9,"nine", 1,"one"];
		datatable(s:long, d:long)[5,1, 1,9, 5,9, 9,5]
		| make-graph s --> d with N on id
		| graph-match (a)-[e]->(b) project a.id, b.id, an = a.n, deg = node_degree_out(a)`)
	var got []string
	for i := range tbl.Rows {
		got = append(got, cell(t, tbl, i, 0)+">"+cell(t, tbl, i, 1)+":"+cell(t, tbl, i, 2)+":"+cell(t, tbl, i, 3))
	}
	want := "9>5:nine:1 1>9:one:1 5>1::2 5>9::2"
	if g := joinSpace(got); g != want {
		t.Errorf("got  %s\nwant %s", g, want)
	}
}

func joinSpace(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}

// An unread variable-length edge skips its JSON edge list; reading it
// in any form must still see it.
func TestGraphVarLenListSkip(t *testing.T) {
	g := `datatable(s:long, d:long, w:long)[1,2,10, 2,3,20, 3,4,30, 1,3,5] | make-graph s --> d`
	cases := []struct{ q, want string }{
		{`graph-match (a)-[e*1..3]->(b) where a.NodeId == 1 project b.NodeId | count`, "5"},
		{`graph-match (a)-[e*1..3]->(b) where a.NodeId == 1 project n = array_length(e) | summarize sum(n)`, "9"},
		{`graph-match (a)-[e*1..3]->(b) where a.NodeId == 1 and e[0].w == 10 project b.NodeId | count`, "3"},
		{`graph-match (a)-[e*1..3]->(b) where a.NodeId == 1 and all(e, w >= 10) project b.NodeId | count`, "3"},
		{`graph-match (a)-[e*1..3]->(b) where a.NodeId == 1 project x = iff(true, e, e) | count`, "5"},
		{`graph-match (a)-[e*1..3]->(b) where a.NodeId == 1 project e | where array_length(e) == 3 | count`, "1"},
	}
	for _, c := range cases {
		if got := cell(t, queryResult(t, g+" | "+c.q), 0, 0); got != c.want {
			t.Errorf("%s: got %s, want %s", c.q, got, c.want)
		}
	}
}
