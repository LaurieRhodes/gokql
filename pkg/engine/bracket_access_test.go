package engine

import "testing"

// bracket_access_test.go — computed and negative indexes, and keys
// taken from columns.

func TestComputedBracketAccess(t *testing.T) {
	r := queryResult(t, `print r = range(1, 5)
		| project last = r[array_length(r) - 1], neg = r[-1], neg2 = r[-2], oob = r[-9],
		  k = dynamic({"a":1,"b":2})[strcat("b", "")], nested = dynamic({"x":[10,20]})["x"][-1]`)
	assertRows(t, r.Rows, [][]interface{}{{int64(5), int64(5), int64(4), nil, int64(2), int64(20)}})
}

// A column used only inside brackets must be read from storage.
func TestBracketKeyColumnReadFromStorage(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.set-or-append T <| datatable(d:dynamic, key:string, n:long)[dynamic({"p":5,"q":6}), "q", 1]`)
	r := diskQuery(t, eng, `T | where n > 0 | project v = d[key]`)
	expectRows(t, r, 1)
	expectCell(t, r, 0, 0, "6")
}
