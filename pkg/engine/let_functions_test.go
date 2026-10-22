package engine

import "testing"

// let_functions_test.go — let functions with tabular bodies, tabular
// (*) parameters, default parameter values, parameterised views, and
// column_names_of (its three reference examples).

func TestLetTabularFunction(t *testing.T) {
	r := queryResult(t, `let f = (p:int) { datatable(A:string, B:int)["x", 1, "y", 2] | where B >= p }; f(2)`)
	assertRows(t, r.Rows, [][]interface{}{{"y", int32(2)}})
}

func TestLetFunctionPrintBody(t *testing.T) {
	r := queryResult(t, `let MyFunction1 = () { print A="", B=1 }; MyFunction1()`)
	assertColumns(t, columnNames(&r.Schema), "A", "B")
}

func TestLetFunctionBodyWithLets(t *testing.T) {
	r := queryResult(t, `let f = () { let y = 2; datatable(a:long)[1,2,3] | where a > y }; f() | count`)
	if got := r.Rows[0][0]; got != int64(1) {
		t.Errorf("count = %v, want 1", got)
	}
}

func TestScalarFunctionDefaultParameter(t *testing.T) {
	r := queryResult(t, `let f = (x:long = 2) { x * 2 }; print a = f(), b = f(5)`)
	assertRows(t, r.Rows, [][]interface{}{{int64(4), int64(10)}})
}

func TestInvokeWildcardTabularParameterWithDefault(t *testing.T) {
	const q = `let T = datatable(a:long)[1,2,3];
		let g = (t:(*), n:long = 1) { t | where a > n };
		T | invoke g(`
	r := queryResult(t, q+`) | count`)
	if got := r.Rows[0][0]; got != int64(2) {
		t.Errorf("default: count = %v, want 2", got)
	}
	r = queryResult(t, q+`2) | count`)
	if got := r.Rows[0][0]; got != int64(1) {
		t.Errorf("n=2: count = %v, want 1", got)
	}
}

func TestParameterisedView(t *testing.T) {
	r := queryResult(t, `let V = view (k:long) { datatable(a:long)[1,2,3] | where a == k }; V(3)`)
	assertRows(t, r.Rows, [][]interface{}{{int64(3)}})
}

func TestScalarFunctionUsedAsTableFails(t *testing.T) {
	if _, err := queryErr(`let f = (x:long) { x * 2 }; f(1) | count`); err == nil {
		t.Error("expected an error using a scalar function as a table")
	}
}

func TestColumnNamesOfReference(t *testing.T) {
	for _, q := range []string{
		`let T = datatable(A:string, B:int) []; print Columns=column_names_of(T)`,
		`let MyFunction1 = () { print A="", B=1 }; print Columns=column_names_of(MyFunction1())`,
		`let MyFunction1 = (param1:int) { print A="", B=1 }; print Columns=column_names_of(MyFunction1(1))`,
	} {
		r := queryResult(t, q)
		if got := r.Rows[0][0]; got != `["A","B"]` {
			t.Errorf("%s: %v, want [\"A\",\"B\"]", q, got)
		}
	}
}
