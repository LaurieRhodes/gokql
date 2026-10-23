package engine

import "testing"

// docs_sweep2_test.go — regressions from the second full sweep of the
// ADX query documentation. Expected values are the reference pages'
// own outputs unless noted.

func TestSweep2Scalars(t *testing.T) {
	cases := []struct{ name, query, want string }{
		{"make_datetime", `print make_datetime(2017,10,01,12,10)`, "2017-10-01T12:10:00Z"},
		{"datetime tuple form", `print datetime(2025, 6, 14)`, "2025-06-14T00:00:00Z"},
		{"unpadded datetime literal", `print datetime(1970-5-12)`, "1970-05-12T00:00:00Z"},
		{"spaced datetime literal", `print datetime(2017 - 08 - 01)`, "2017-08-01T00:00:00Z"},
		{"dayofweek", `print dayofweek(datetime(1970-5-12))`, "2.00:00:00"},
		{"make_timespan fractional", `print tostring(make_timespan(1,12,30,55.123))`, "1.12:30:55.1230000"},
		{"endofweek tick", `print endofweek(datetime(2020-01-01))`, "2020-01-04T23:59:59.9999999Z"},
		{"endofday offset", `print endofday(datetime(2020-01-01), 1)`, "2020-01-02T23:59:59.9999999Z"},
		{"startofmonth offset", `print startofmonth(datetime(2020-01-15), 2)`, "2020-03-01T00:00:00Z"},
		{"udf datetime result", `let f = (t: long) { datetime(1970-01-01) + t * 1sec }; print f(1546897531)`, "2019-01-07T21:45:31Z"},
		{"strlen characters", `print strlen("⒦⒰⒮⒯⒪")`, "5"},
		{"reverse datetime", `print reverse(datetime(2017-10-15 12:00))`, "Z0000000.00:00:21T51-01-7102"},
		{"reverse timespan", `print reverse(3h)`, "00:00:30"},
		{"strrep timespan", `print strrep(3s, 2)`, "00:00:0300:00:03"},
		{"pack_array kinds", `print pack_array(1, "2", 2s, datetime(2020-01-01), dynamic({"x":1}))`, `[1,"2","00:00:02","2020-01-01T00:00:00.0000000Z",{"x":1}]`},
		{"hash_md5 datetime", `print hash_md5(datetime(2020-01-01))`, "786c530672d1f8db31fee25ea8a9390b"},
		{"hash_sha1 datetime", `print hash_sha1(datetime(2020-01-01))`, "e903e533f4d636b4fc0dcf3cf81e7b7f330de776"},
		{"parse_ipv4 masked", `print parse_ipv4("192.168.1.1/24")`, "3232235776"},
		{"parse_ipv4 /31", `print parse_ipv4("255.255.255.255/31")`, "4294967294"},
		{"ipv4 any range bare ip", `print ipv4_is_in_any_range("127.0.0.1", dynamic(["127.0.0.1", "192.168.1.1"]))`, "true"},
		{"bag_has_key jsonpath", `print bag_has_key(dynamic({'key1': 123, 'key2': {'prop1' : 'abc'}}), '$.key2.prop1')`, "true"},
		{"parse_urlquery", `print parse_urlquery("k1=v1&k2=v2")`, `{"Query Parameters":{"k1":"v1","k2":"v2"}}`},
		{"parse_url", `print parse_url("scheme://username:password@host:1234/this/is/a/path?k1=v1&k2=v2#fragment")`,
			`{"Scheme":"scheme","Host":"host","Port":"1234","Path":"/this/is/a/path","Username":"username","Password":"password","Query Parameters":{"k1":"v1","k2":"v2"},"Fragment":"fragment"}`},
		{"parse_path", `print parse_path("file://C:/temp/file.txt:some.exe")`,
			`{"Scheme":"file","RootPath":"C:/","DirectoryPath":"C:/temp","DirectoryName":"temp","Filename":"file.txt","Extension":"txt","AlternateDataStreamName":"some.exe"}`},
		{"array_sort nulls last", `print array_sort_asc(dynamic([null,"blue","yellow","green",null]))`, `["blue","green","yellow",null,null]`},
		{"array_sort desc nulls first", `print array_sort_desc(dynamic([null,"blue","yellow","green",null]), false)`, `[null,null,"yellow","green","blue"]`},
		{"array_sort numeric", `print array_sort_asc(dynamic([10,2,1.5]))`, `[1.5,2,10]`},
		{"array_sort index", `print array_sort_asc(dynamic([3,1]), dynamic([7,8]))[1]`, `[8,7]`},
		{"series_fill_linear int", `print series_fill_linear(dynamic([null, 111, null, 36, 41, null, null, 16, 61, 33, null, null]))`, `[111,111,73,36,41,32,24,16,61,33,33,33]`},
		{"string escape \\u", `print "Hello\u00A0World"`, "Hello\u00a0World"},
		{"string escape \\x and octal", `print "\x41\101"`, "AA"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expectCell(t, queryResult(t, c.query), 0, 0, c.want)
		})
	}
}

func TestSweep2NullLogic(t *testing.T) {
	tbl := queryResult(t, `datatable(val:int)[5, int(null)]
| extend IsBiggerThan3 = val > 3
| extend IsBiggerThan3OrNull = val > 3 or isnull(val)
| extend IsEqualToNull = val == int(null)
| extend IsNotEqualToNull = val != int(null)`)
	want := [][]string{{"5", "true", "true", "false", "true"}, {"", "", "true", "", ""}}
	for r, row := range want {
		for c, v := range row {
			expectCell(t, tbl, r, c, v)
		}
	}
	tbl = queryResult(t, `datatable(ival:int, sval:string)[5, "a", int(null), "b"] | where ival != 5`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 1, "b")
	tbl = queryResult(t, `print a=int(null)+1, b=not(int(null) > 3), c=int(null)>3 and false`)
	expectCell(t, tbl, 0, 0, "")
	expectCell(t, tbl, 0, 1, "")
	expectCell(t, tbl, 0, 2, "false")
}

func TestSweep2ArraySortColumns(t *testing.T) {
	tbl := queryResult(t, `let array1 = dynamic([1,3,4,5,2]); let array2 = dynamic(["a","b","c","d","e"]); print array_sort_asc(array1,array2)`)
	if tbl.Schema.Columns[0].Name != "array1_sorted" || tbl.Schema.Columns[1].Name != "array2_sorted" {
		t.Fatalf("columns %v", tbl.Schema.Columns)
	}
	expectCell(t, tbl, 0, 0, "[1,2,3,4,5]")
	expectCell(t, tbl, 0, 1, `["a","e","b","c","d"]`)
}

func TestSweep2MakeListDynamic(t *testing.T) {
	tbl := queryResult(t, `datatable(n:string)["a","b"] | extend d = bag_pack("name", n) | summarize make_list(d)`)
	expectCell(t, tbl, 0, 0, `[{"name":"a"},{"name":"b"}]`)
}

func TestSweep2ProjectReorderWildcard(t *testing.T) {
	tbl := queryResult(t, `print b = 'b', a2='a2', a3='a3', a1='a1' | project-reorder a* asc`)
	for i, n := range []string{"a1", "a2", "a3", "b"} {
		if tbl.Schema.Columns[i].Name != n {
			t.Fatalf("column %d: %s, want %s", i, tbl.Schema.Columns[i].Name, n)
		}
	}
}

func TestSweep2UnionAsNames(t *testing.T) {
	tbl := queryResult(t, `union withsource=TableName (range x from 1 to 1 step 1 | as T1), (range x from 1 to 1 step 1 | as T2)`)
	expectCell(t, tbl, 0, 0, "T1")
	expectCell(t, tbl, 1, 0, "T2")
}

func TestSweep2ParseKvEscape(t *testing.T) {
	tbl := queryResult(t, `print str='failure="the remote host sent \\"bye!\\""'
| parse-kv str as (failure:string) with (pair_delimiter=' ', kv_delimiter='=', quote='"', escape='\\')`)
	expectCell(t, tbl, 0, 1, `the remote host sent "bye!"`)
}

func TestSweep2Batch2Scalars(t *testing.T) {
	cases := []struct{ name, query, want string }{
		{"indexof negative start", `print indexof("abcdefg","cde",-5)`, "2"},
		{"indexof occurrence", `print indexof("abcdefgabcdefg", "cde", 1, 10, 2)`, "9"},
		{"indexof range", `print indexof("abcdefg","cde",1,2)`, "2"}, // the emulator: length bounds where a match may start
		{"datatable hex", `datatable(num:long)[0x88888008, 0x42000000, 0x00767000, 0x00000005] | summarize result = toupper(tohex(binary_all_or(num)))`, "CAFEF00D"},
		{"time literal", `print 24 * 60 * time(00:01:00) / time(1s)`, "86400"},
		{"time day literal", `print tostring(time(29.09:00:05.12345))`, "29.09:00:05.1234500"},
		{"timespan alias", `print timespan(25min)`, "00:25:00"},
		{"dynamic typed values", `print dynamic({"a": datetime(1970-05-11), "b": 1d, e: long(5), f: [1, 2,]})`, `{"a":"1970-05-11T00:00:00.0000000Z","b":"1.00:00:00","e":5,"f":[1,2]}`},
		{"dynamic multiline h-string", "print dynamic({\n 'S': [\n  h@'https://x;key'\n ],\n})", `{"S":["https://x;key"]}`},
		{"estimate_data_size", `range x from 1 to 10 step 1 | extend Text = '1234567890' | summarize Total=sum(estimate_data_size(*))`, "180"},
		{"toscalar in udf", `let T1 = datatable(xdate:datetime)[datetime(1970-01-01)]; let f = (hours:long) { toscalar(T1 | summarize min(xdate) - hours*1h) }; print f(1235)`, "1969-11-10T13:00:00Z"},
		{"let print", `let T1 = print x=1; T1`, "1"},
		{"has_any dynamic", `datatable(S:string)["TEXAS","OHIO"] | where S has_any (dynamic(["ohio"]))`, "OHIO"},
		{"has_any subquery", `let P = datatable(S:string, N:long)["TEXAS", 9, "OHIO", 1]; datatable(S:string)["TEXAS","OHIO"] | where S has_any ((P | where N > 5 | project S))`, "TEXAS"},
		{"where star has", `datatable(a:string, b:long)["cow",1,"dog",22] | where * has "cow" | project b`, "1"},
		{"summarize paren by", `datatable(a:long,b:long)[1,2,1,3] | summarize sum(b)by a | project sum_b`, "5"},
		{"summarize by only", `datatable(a:long,b:long)[1,2,3,4,1,2] | summarize by h=a+b | count`, "2"},
		{"between names", `let S=1; let E=S+2; datatable(a:long)[1,2,5] | where a between (S .. E) | count`, "2"},
		{"join bare table", `let X = datatable(Key:string, V:long)["a",1,"b",2]; let Y = datatable(Key:string, W:long)["b",10]; X | join kind=inner Y on Key | project W`, "10"},
		{"unicode codepoints variadic", `print unicode_codepoints_to_string(dynamic([75, 117, 115]), 116, 111)`, "Kusto"},
		{"rows_near", `range x from 1 to 10 step 1 | evaluate rows_near(x == 5, 1) | summarize make_list(x)`, "[4,5,6]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expectCell(t, queryResult(t, c.query), 0, 0, c.want)
		})
	}
}

func TestSweep2MakeSeriesRangeAndOrder(t *testing.T) {
	tbl := queryResult(t, `datatable(a:long,t:datetime)[1,datetime(2020-01-01), 2, datetime(2020-01-03 05:00)]
| make-series hint.shufflekey = a sum(a) default = 0 on t in range(datetime(2020-01-01), datetime(2020-01-03), 1d) by a`)
	if tbl.Schema.Columns[0].Name != "a" || tbl.Schema.Columns[1].Name != "sum_a" {
		t.Fatalf("columns %v, want a, sum_a, t", tbl.Schema.Columns)
	}
	expectCell(t, tbl, 0, 1, "[1,0,0]")
	expectCell(t, tbl, 1, 1, "[0,0,2]")
}

func TestSweep2BagUnpackOutputSchemaStar(t *testing.T) {
	tbl := queryResult(t, `datatable(d:dynamic, Description: string)
[dynamic({"Name": "John", "Age":20, "height":180}), "Student"]
| evaluate bag_unpack(d) : (*, Name:string, Age:long)`)
	for i, n := range []string{"Description", "Name", "Age"} {
		if tbl.Schema.Columns[i].Name != n {
			t.Fatalf("columns %v", tbl.Schema.Columns)
		}
	}
	expectCols(t, tbl, 3)
	expectCell(t, tbl, 0, 2, "20")
}

func TestSweep2Consume(t *testing.T) {
	tbl := queryResult(t, `range x from 1 to 3 step 1 | consume`)
	expectRows(t, tbl, 0)
}

func TestSweep2FormatTimespan(t *testing.T) {
	tbl := queryResult(t, `let t = time(29.09:00:05.12345); print v1=format_timespan(t, 'dd.hh:mm:ss:FF'), v2=format_timespan(t, 'ddd.h:mm:ss [fffffff]')`)
	expectCell(t, tbl, 0, 0, "29.09:00:05:12")
	expectCell(t, tbl, 0, 1, "029.9:00:05 [1234500]")
}

func TestSweep2RightSemiKeepsEveryMatch(t *testing.T) {
	tbl := queryResult(t, `let X = datatable(Key:string, Value1:long)['a',1,'b',2,'b',3,'c',4];
let Y = datatable(Key:string, Value2:long)['b',10,'c',20,'c',30,'d',40];
X | join kind=rightsemi Y on Key`)
	expectRows(t, tbl, 3)
	expectCell(t, tbl, 2, 1, "30")
}

func TestSweep2Around(t *testing.T) {
	tbl := queryResult(t, `range dt from datetime(2021-01-01 01:00) to datetime(2021-01-01 02:00) step 1min | where around(dt, datetime(2021-01-01 01:30), 1min) | count`)
	expectCell(t, tbl, 0, 0, "3")
	tbl = queryResult(t, `print around(5, 4, 1), around(1.5, 1, 0.4), isnull(around(int(null), 1, 1)), isnull(guid(null))`)
	for c, w := range []string{"true", "false", "true", "true"} {
		expectCell(t, tbl, 0, c, w)
	}
}

func TestSweep2SeriesSignal(t *testing.T) {
	cases := []struct{ name, query, want string }{
		{"fir moving average", `print series_fir(dynamic([0,0,0,10,20,40,100]), dynamic([1,1,1,1,1]))`, "[0,0,0,2,6,14,34]"},
		{"fir centered", `print series_fir(dynamic([0,0,10,0,0]), dynamic([1,1,1]), true, true)`, "[0,3.333333333333333,3.333333333333333,3.333333333333333,0]"},
		{"fir difference", `print series_fir(dynamic([0, 0, 2, 2, 3, 3]), dynamic([1, -1]), false, false)`, "[0,0,2,0,1,0]"},
		{"fir negative normalized", `print isnull(series_fir(dynamic([1,2]), dynamic([1,-1])))`, "true"},
		{"seasonal period", `print series_seasonal(dynamic([1, 3, 5, 2, 4, 6, 1, 3, 5, 2, 4, 6]), 3)`, "[1.5,3.5,5.5,1.5,3.5,5.5,1.5,3.5,5.5,1.5,3.5,5.5]"},
		{"fft imag", `print a=dynamic([1,2,3,4]) | extend (r, i) = series_fft(a) | project i`, "[0,2,0,-2]"},
		{"ifft round trip", `print a=dynamic([1,2,3,4]) | extend (r, i) = series_fft(a) | extend (r2, i2) = series_ifft(r, i) | project r2`, "[1,2,3,4]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expectCell(t, queryResult(t, c.query), 0, 0, c.want)
		})
	}
	tbl := queryResult(t, `print a=dynamic([1,2]) | extend series_fft(a)`)
	if tbl.Schema.Columns[1].Name != "series_fft_a_real" || tbl.Schema.Columns[2].Name != "series_fft_a_imag" {
		t.Fatalf("columns %v", tbl.Schema.Columns)
	}
}
