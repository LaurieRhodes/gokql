package engine

import "testing"

func TestReduce(t *testing.T) {
	src := `datatable(t:string)["user alice logged in from 10.0.0.1","user bob logged in from 10.0.0.2","user carol logged in from 10.0.0.3","disk /dev/sda1 full","disk /dev/sdb2 full","error code 404 on /index.html","error code 500 on /api","user dave logged out"]`
	tbl := queryResult(t, src+` | reduce by t`)
	expectColNames(t, tbl, "Pattern", "Count", "Representative")
	expectRows(t, tbl, 3)
	expectCell(t, tbl, 0, 0, "user * logged in from 10.0.0.*")
	expectCell(t, tbl, 0, 1, "3")
	expectCell(t, tbl, 0, 2, "user alice logged in from 10.0.0.1")
	expectCell(t, tbl, 1, 0, "disk /dev/* full")
	expectCell(t, tbl, 2, 0, "others")
	expectCell(t, tbl, 2, 1, "3")
	expectCell(t, tbl, 2, 2, "")

	// kind=source appends Pattern to every input row
	tbl = queryResult(t, src+` | reduce kind=source by t with threshold=0.2 | summarize u=countif(Pattern == "user * logged in from 10.0.0.*"), d=countif(Pattern == "disk /dev/* full"), o=countif(Pattern == "others"), n=count()`)
	expectCell(t, tbl, 0, 0, "3")
	expectCell(t, tbl, 0, 1, "2")
	expectCell(t, tbl, 0, 2, "3")
	expectCell(t, tbl, 0, 3, "8")

	// the reference examples (ADX gives these patterns)
	tbl = queryResult(t, `range x from 1 to 1000 step 1 | project MyText = strcat("MachineLearningX", tostring(x % 10)) | reduce by MyText with threshold=0.001, characters = "X"`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "MachineLearning*")
	expectCell(t, tbl, 0, 1, "1000")
	tbl = queryResult(t, `range x from 1 to 10 step 1 | project str = strcat("foo", "Z", tostring(x)) | reduce by str`)
	expectCell(t, tbl, 0, 0, "others")
	expectCell(t, tbl, 0, 1, "10")
	tbl = queryResult(t, `range x from 1 to 10 step 1 | project str = strcat("foo", "Z", tostring(x)) | reduce by str with characters="Z"`)
	expectCell(t, tbl, 0, 0, "foo*")

	queryError(t, src+` | reduce by t with threshold=2`)
	queryError(t, src+` | reduce kind=other by t`)
}
