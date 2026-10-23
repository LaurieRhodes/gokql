package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// Let and name forms found missing by the docs sweep. Expected values
// are the Kusto emulator's.
func TestLetForms(t *testing.T) {
	cases := []struct{ q, want string }{
		// scalar function bodies with local lets
		{`let f = (a:real) { let two = 2.0; let k = two * a; k + 1 }; print r=f(3)`, "7"},
		// a let function that forwards to a tabular one
		{`let foo1 = (a:long, b:long, n:long) { range x from a to b step n }; let foo2 = (n:long) { foo1(1, 100, n) }; foo2(10) | count`, "10"},
		// alias and materialize of a tabular let
		{`let s = datatable(a:int)[1,2]; let m = s; m | count`, "2"},
		{`let s = datatable(a:int)[1,2,3]; let m = materialize(s); m | count`, "3"},
		{`let a = 5; let b = a; print r=b`, "5"},
		// bracket-quoted names
		{`let ['a b'] = 3; print r=['a b']`, "3"},
		{`let ['t z'] = datatable(a:int)[1]; ['t z'] | count`, "1"},
		{`print x=1 | as ['x y']; ['x y'] | project x`, "1"},
		// .[expr] access
		{`let p = dynamic({"a b":{"k":7}}); let k = "k"; print r=p["a b"].[k]`, "7"},
		// toscalar of a scalar
		{`print r=toscalar(2)`, "2"},
		{`print r=isnotempty(toscalar(new_guid()))`, "true"},
		// parameterless functions named without parentheses
		{`let x = () {42}; print r=x`, "42"},
		{`let x = () {42}; datatable(a:int)[1] | extend r = x + a | project r`, "43"},
		{`let T = datatable(a:int)[1]; let f = () { T }; f | count`, "1"},
	}
	for _, c := range cases {
		tbl := queryResult(t, c.q)
		if len(tbl.Rows) == 0 || len(tbl.Schema.Columns) == 0 {
			t.Errorf("%s: empty result", c.q)
			continue
		}
		if got := cell(t, tbl, 0, len(tbl.Schema.Columns)-1); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.q, got, c.want)
		}
	}
	tbl := queryResult(t, `let x = () {42}; datatable(a:int)[1] | extend r = x + a`)
	if typ := tbl.Schema.Columns[1].Type; typ != types.TypeLong {
		t.Errorf("x + a: type %v, want long", typ)
	}
}
