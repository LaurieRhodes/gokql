package engine

import "testing"

// mv_apply_reference_test.go — the mv-apply reference examples, and
// string operators on null operands.

func TestMvApplyReferenceExamples(t *testing.T) {
	cases := []struct {
		name, q string
		cols    []string
		rows    [][]interface{}
	}{
		{"top2 summarize", `let data = datatable (index: int, Arr: dynamic)[0, dynamic([5, 1, 3]), 1, dynamic([4, 10, 8, 7])];
			data | mv-apply Arr2=Arr to typeof(long) on (top 2 by Arr2 | summarize Top2=make_list(Arr2)) | project index, Top2`,
			[]string{"index", "Top2"}, [][]interface{}{{int32(0), "[5,3]"}, {int32(1), "[10,8]"}}},
		{"two columns", `datatable (Val:int, Arr1:dynamic, Arr2:dynamic)[
			1, dynamic(['A1', 'A2', 'A3']), dynamic([10, 30, 7]),
			7, dynamic(['B1', 'B2', 'B5']), dynamic([15, 11, 50]),
			3, dynamic(['C1', 'C2', 'C3', 'C4']), dynamic([6, 40, 20, 8])]
			| mv-apply NewArr1=Arr1, NewArr2=Arr2 to typeof(long) on (
			  top 2 by NewArr2 | summarize NewArr1=make_list(NewArr1), NewArr2=make_list(NewArr2))
			| project Val, NewArr1, NewArr2`,
			[]string{"Val", "NewArr1", "NewArr2"}, [][]interface{}{
				{int32(1), `["A2","A1"]`, "[30,10]"}, {int32(7), `["B5","B1"]`, "[50,15]"}, {int32(3), `["C2","C3"]`, "[40,20]"}}},
		{"with_itemindex", `let data = datatable (row: int, Arr: dynamic)[0, dynamic([5, 1, 3]), 1, dynamic([4, 10, 8, 7])];
			data | mv-apply with_itemindex=index value=Arr to typeof(long) on (
			  // here you have 'index' column
			  where index >= 2) | project row, value, index`,
			[]string{"row", "value", "index"}, [][]interface{}{
				{int32(0), int64(3), int64(2)}, {int32(1), int64(8), int64(2)}, {int32(1), int64(7), int64(3)}}},
		{"zip pad", `datatable (Val: int, Arr1: dynamic, Arr2: dynamic)[
			1, dynamic(['A1', 'A2']), dynamic(['B1', 'B2', 'B3']), 5, dynamic(['C1', 'C2']), dynamic(['D1'])]
			| mv-apply T1=Arr1, T2=Arr2 on (extend Out = strcat(T1, "_", T2) | summarize Out= make_list(Out))
			| project Val, Out`,
			[]string{"Val", "Out"}, [][]interface{}{{int32(1), `["A1_B1","A2_B2","_B3"]`}, {int32(5), `["C1_D1","C2_"]`}}},
		{"property bag", `datatable(Source: string, Target: string, Count: long)['555-1234', '555-1212', 46, '555-2424', '', int(null)]
			| extend Pack = pack_all()
			| mv-apply removeProperties = Pack on (mv-expand kind = array Pack
			  | where Pack[1] !startswith "555" | summarize propsToRemove = make_set(Pack[0]))
			| extend values = bag_remove_keys(Pack, propsToRemove) | project values`,
			[]string{"values"}, [][]interface{}{{`{"Source":"555-1234","Target":"555-1212"}`}, {`{"Source":"555-2424"}`}}},
	}
	for _, c := range cases {
		r := queryResult(t, c.q)
		assertColumns(t, columnNames(&r.Schema), c.cols...)
		assertRows(t, r.Rows, c.rows)
	}
}

// The full output keeps the input row's columns ahead of the
// subquery's new ones (reference: index, Arr, Top2).
func TestMvApplyCarriesInputColumns(t *testing.T) {
	r := queryResult(t, `datatable (index: int, Arr: dynamic)[0, dynamic([5, 1, 3])]
		| mv-apply Arr2=Arr to typeof(long) on (summarize Top=max(Arr2))`)
	assertColumns(t, columnNames(&r.Schema), "index", "Arr", "Top")
}

func TestStringOperatorsOnNull(t *testing.T) {
	r := queryResult(t, `print a = dynamic(null) !startswith "5", b = dynamic(null) startswith "5",
		c = dynamic(null) !contains "x", d = dynamic(null) has "x", e = int(null) == 1`)
	assertRows(t, r.Rows, [][]interface{}{{true, false, true, false, false}})
}
