package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// mv_expand_options_test.go — mv-expand per the ADX reference: the
// seven worked examples first, then the documented rules for null,
// empty and scalar values, limit, cast-only typing and legacy mvexpand.

func TestMvExpandArray(t *testing.T) {
	r := queryResult(t, `datatable (a: int, b: dynamic)[1, dynamic([10, 20]), 2, dynamic(['a', 'b'])]
		| mv-expand b`)
	assertRows(t, r.Rows, [][]interface{}{
		{int32(1), int64(10)}, {int32(1), int64(20)}, {int32(2), "a"}, {int32(2), "b"},
	})
}

func TestMvExpandBag(t *testing.T) {
	r := queryResult(t, `datatable (a: int, b: dynamic)[
		1, dynamic({"prop1": "a1", "prop2": "b1"}),
		2, dynamic({"prop1": "a2", "prop2": "b2"})] | mv-expand b`)
	assertRows(t, r.Rows, [][]interface{}{
		{int32(1), `{"prop1":"a1"}`}, {int32(1), `{"prop2":"b1"}`},
		{int32(2), `{"prop1":"a2"}`}, {int32(2), `{"prop2":"b2"}`},
	})
}

func TestMvExpandBagToKeyValuePairs(t *testing.T) {
	r := queryResult(t, `datatable (a: int, b: dynamic)[
		1, dynamic({"prop1": "a1", "prop2": "b1"}),
		2, dynamic({"prop1": "a2", "prop2": "b2"})]
		| mv-expand kind=array b | extend key = b[0], val = b[1]`)
	assertRows(t, r.Rows, [][]interface{}{
		{int32(1), `["prop1","a1"]`, "prop1", "a1"},
		{int32(1), `["prop2","b1"]`, "prop2", "b1"},
		{int32(2), `["prop1","a2"]`, "prop1", "a2"},
		{int32(2), `["prop2","b2"]`, "prop2", "b2"},
	})
}

func TestMvExpandZipped(t *testing.T) {
	r := queryResult(t, `datatable (a: int, b: dynamic, c: dynamic)[
		1, dynamic({"prop1": "a", "prop2": "b"}), dynamic([5, 4, 3])] | mv-expand b, c`)
	assertRows(t, r.Rows, [][]interface{}{
		{int32(1), `{"prop1":"a"}`, int64(5)},
		{int32(1), `{"prop2":"b"}`, int64(4)},
		{int32(1), nil, int64(3)},
	})
}

func TestMvExpandCartesian(t *testing.T) {
	r := queryResult(t, `datatable (a: int, b: dynamic, c: dynamic)[
		1, dynamic({"prop1": "a", "prop2": "b"}), dynamic([5, 6])]
		| mv-expand b | mv-expand c`)
	assertRows(t, r.Rows, [][]interface{}{
		{int32(1), `{"prop1":"a"}`, int64(5)}, {int32(1), `{"prop1":"a"}`, int64(6)},
		{int32(1), `{"prop2":"b"}`, int64(5)}, {int32(1), `{"prop2":"b"}`, int64(6)},
	})
}

func TestMvExpandToTypeof(t *testing.T) {
	r := queryResult(t, `datatable (a: string, b: dynamic, c: dynamic)[
		"Constant", dynamic([1, 2, 3, 4]), dynamic([6, 7, 8, 9])]
		| mv-expand b, c to typeof(int)`)
	want := []types.KQLType{types.TypeString, types.TypeDynamic, types.TypeInt}
	for i, w := range want {
		if got := r.Schema.Columns[i].Type; got != w {
			t.Errorf("column %d type = %v, want %v", i, got, w)
		}
	}
	if got := r.Rows[0][2]; got != int32(6) {
		t.Errorf("c[0] = %v (%T), want int32(6)", got, got)
	}
}

func TestMvExpandWithItemIndex(t *testing.T) {
	r := queryResult(t, `range x from 1 to 4 step 1 | summarize x = make_list(x)
		| mv-expand with_itemindex=Index x`)
	assertColumns(t, columnNames(&r.Schema), "x", "Index")
	assertRows(t, r.Rows, [][]interface{}{
		{int64(1), int64(0)}, {int64(2), int64(1)}, {int64(3), int64(2)}, {int64(4), int64(3)},
	})
}

// Null gives one null row, an empty collection gives none, a scalar
// gives itself.
func TestMvExpandNullEmptyScalar(t *testing.T) {
	r := queryResult(t, `datatable (a: int, b: dynamic)[1, dynamic(null), 2, dynamic([]), 3, dynamic({}), 4, dynamic(7)]
		| mv-expand b`)
	assertRows(t, r.Rows, [][]interface{}{{int32(1), nil}, {int32(4), int64(7)}})
}

func TestMvExpandLimit(t *testing.T) {
	r := queryResult(t, `datatable (a: int, b: dynamic)[1, dynamic([1,2,3]), 2, dynamic([4,5,6])]
		| mv-expand b limit 2 | count`)
	if got := r.Rows[0][0]; got != int64(4) {
		t.Errorf("count = %v, want 4 (2 per input row)", got)
	}
}

func TestMvExpandLegacyDefaultLimit(t *testing.T) {
	r := queryResult(t, `print b = repeat(1, 200)
		| mvexpand b | count`)
	if got := r.Rows[0][0]; got != int64(128) {
		t.Errorf("count = %v, want 128", got)
	}
}

// Cast-only: an element that is not a string does not become one.
func TestMvExpandCastNonConformingIsNull(t *testing.T) {
	r := queryResult(t, `print b = dynamic(["x", 5, true]) | mv-expand b to typeof(string)`)
	assertRows(t, r.Rows, [][]interface{}{{"x"}, {nil}, {nil}})
}

func TestMvExpandExpressionNamed(t *testing.T) {
	r := queryResult(t, `print a = 1 | mv-expand x = pack_array(a, a + 1) to typeof(long)`)
	assertRows(t, r.Rows, [][]interface{}{{int64(1), int64(1)}, {int64(1), int64(2)}})
}

func TestMvApplyTypedElementsAreCast(t *testing.T) {
	r := queryResult(t, `print d = dynamic(["7", 8])
		| mv-apply x = d to typeof(long) on (project x)`)
	// Columns: d (carried from the input row), x.
	if got := r.Rows[0][1]; got != nil {
		t.Errorf("x[0] = %v, want null (a string is not a long)", got)
	}
	if got := r.Rows[1][1]; got != int64(8) {
		t.Errorf("x[1] = %v (%T), want int64(8)", got, got)
	}
}

// mv-apply does not expand property bags (reference: use mv-expand in
// the subquery); a bag is a single element.
func TestMvApplyBag(t *testing.T) {
	r := queryResult(t, `print d = dynamic({"k1": 1, "k2": 2})
		| mv-apply e = d on (project e)`)
	expectRows(t, r, 1)
	if got := r.Rows[0][r.Schema.ColumnIndex("e")]; got != `{"k1":1,"k2":2}` {
		t.Errorf("e = %v, want the whole bag", got)
	}
}
