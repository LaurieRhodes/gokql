package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// Fourth docs sweep: cases checked against the Kusto emulator.
func TestSweep4EmulatorValues(t *testing.T) {
	cases := []struct{ q, want string }{
		// a failed typed conversion fails the whole parse outside relaxed mode
		{`datatable(t:string)["a=1, r=bad, z"] | parse t with "a=" a:long ", r=" r:datetime "," * | project v = isnull(a)`, "true"},
		{`datatable(t:string)["a=1, r=bad, z"] | parse kind=regex t with "a=" a:long ", r=" r:datetime "," | project v = isnull(a)`, "true"},
		{`datatable(t:string)["a=1, r=bad, z"] | parse kind=relaxed t with "a=" a:long ", r=" r:datetime "," * | project a`, "1"},
		{`datatable(t:string)["a=1, r=bad, z"] | parse-where t with "a=" a:long ", r=" r:datetime "," * | count`, "0"},
		// verbatim literals ending in a backslash
		{`print a=@"C:\", b=1 | project v = strcat(a, b)`, `C:\1`},
		{`datatable(p:string)[@"C:\", "x"] | count`, "2"},
		{`print a=@'x\', b='q' | project v = strcat(a, b)`, `x\q`},
		// parse_path roots keep their separator
		{`print v = parse_path(@"C:\temp\file.txt").RootPath`, `C:\`},
		{`print v = parse_path(@"C:file.txt").DirectoryPath`, ``},
		{`print v = parse_path(@"C:\file.txt").DirectoryPath`, `C:`},
		// "::a.b.c.d" prints in the IPv4-compatible form, compares as mapped
		{`print v = parse_ipv6('::192.168.255.255')`, "0000:0000:0000:0000:0000:0000:c0a8:ffff"},
		{`print v = parse_ipv6('192.168.255.255')`, "0000:0000:0000:0000:0000:ffff:c0a8:ffff"},
		{`print v = ipv6_compare('::192.168.255.255', '192.168.255.255')`, "0"},
		// has_ipv4_prefix needs a whole address
		{`print v = has_ipv4_prefix('x 127.0.0.256 y', '127.0.')`, "false"},
		{`print v = has_ipv4_prefix('x 127.0.0.1.5 y', '127.0.')`, "true"},
		{`print v = has_ipv4_prefix('x 127.0.0 y', '127.0.')`, "false"},
		// modulo is never negative
		{`print v = -14 % 12`, "10"},
		{`print v = -14 % -12`, "10"},
		{`print v = -14.5 % 12`, "9.5"},
		{`print v = -90m % 1h`, "00:30:00"},
		{`print v = isnull(7 % 0)`, "true"},
		// between with a timespan upper bound
		{`print v = datetime(2007-07-27 12:00) between (datetime(2007-07-27) .. 3d)`, "true"},
		{`print v = datetime(2007-07-31) between (datetime(2007-07-27) .. 3d)`, "false"},
		// indexof length bounds the match start
		{`print v = indexof("abcdefg", "cde", 1, 2)`, "2"},
		{`print v = indexof("abcdefgcde", "cde", 3, 4)`, "-1"},
		// series_dot_product broadcasts and truncates
		{`print v = series_dot_product(dynamic([1,2,4]), 2)`, "14"},
		{`print v = series_dot_product(dynamic([1,2,3]), dynamic([1,2]))`, "5"},
		// make_set/make_list flatten arrays one level
		{`datatable(a:dynamic)[dynamic([1,2]), dynamic([2,3]), dynamic(5)] | summarize v = make_set(a)`, "[1,2,3,5]"},
		{`datatable(a:dynamic)[dynamic([[1,2],3])] | summarize v = make_list(a)`, "[[1,2],3]"},
		// local → UTC across transitions (emulator): a gap takes the
		// offset of larger magnitude, a repeated hour standard time
		{`print v = datetime_local_to_utc(datetime(2022-03-27 02:00), 'Europe/Brussels')`, "2022-03-27T00:00:00Z"},
		{`print v = datetime_local_to_utc(datetime(2022-03-13 02:30), 'America/New_York')`, "2022-03-13T07:30:00Z"},
		{`print v = datetime_local_to_utc(datetime(2022-10-02 02:30), 'Australia/Sydney')`, "2022-10-01T15:30:00Z"},
		{`print v = datetime_local_to_utc(datetime(2022-11-06 01:30), 'America/New_York')`, "2022-11-06T06:30:00Z"},
		{`print v = datetime_local_to_utc(datetime(2022-04-03 02:30), 'Australia/Sydney')`, "2022-04-02T16:30:00Z"},
		{`print v = datetime_local_to_utc(datetime(2022-03-27 01:00), 'Europe/Brussels')`, "2022-03-27T00:00:00Z"},
		// search without tables covers views; $table is a column
		{"let A = view () { range MyColumn from 1 to 10 step 1 }; let B = view () { range MyColumn from 1 to 20 step 1 }; search MyColumn == 5 | summarize v = count()", "2"},
		{"let A = view () { range MyColumn from 1 to 10 step 1 }; search MyColumn == 5 | project v = $table", "A"},
		// set statements and query parameters (the emulator)
		{"set query_bin_auto_size=1h; set query_bin_auto_at=datetime(2017-01-01 00:05); print v = bin_auto(datetime(2017-01-01 01:24))", "2017-01-01T01:05:00Z"},
		{"set query_bin_auto_size=7; print v = bin_auto(9)", "7"},
		{"set query_now=datetime(2020-02-02); print v = ago(1d)", "2020-02-01T00:00:00Z"},
		{"set query_take_max_records=2; range x from 1 to 5 step 1 | summarize v = count()", "5"},
		{"set notruncation; range v from 1 to 3 step 1 | where v == 3", "3"},
		{"declare query_parameters(maxInjured:long = 90); print v = maxInjured", "90"},
		{`declare query_parameters(a:string = "x", b:long = 2); let c = 1; print v = strcat(a, b, c)`, "x21"},
		{"print a = now(), b = now() | project v = a == b", "true"},
		// split with an index; array_index_of windows; datetime_diff boundaries
		{`print v = split("aa_bb_cc", "_", 2)`, `["cc"]`},
		{`print v = split("aa_bb_cc", "_", -1)`, `["aa","bb","cc"]`},
		{`print v = isnull(split("aa_bb_cc", "_", 3))`, "true"},
		{`print v = array_index_of(dynamic([1,2,3,2]), 2, 0, 4, 2)`, "3"},
		{`print v = array_index_of(dynamic([1,2,3,2]), 2, 2, 1)`, "-1"},
		{`print v = array_index_of(dynamic([1,2,3,2]), 2, -10)`, "1"},
		{`print v = datetime_diff("day", datetime(2017-10-31 00:00:01), datetime(2017-10-30 23:59:59))`, "1"},
		{`print v = datetime_diff("quarter", datetime(2017-10-01), datetime(2016-01-01))`, "7"},
		{`print v = datetime_diff("week", datetime(2017-10-29), datetime(2017-10-28))`, "1"},
		{`print v = todatetime("12-02-2022")`, "2022-12-02T00:00:00Z"},
		{`print v = isnull(todatetime("13-02-2022"))`, "true"},
		// make-series with a constant item
		{`datatable(Time: datetime, Value: int)[datetime(2025-01-07), 10] | make-series Value=sum(Value), other=-1, k=5 default=-2 on Time from datetime(2025-01-06) to datetime(2025-01-20) step 7d | project v = k`, "[5,-2]"},
		// project-away wildcards
		{`print Ignore0=1, a=2, Ignore1=3 | project-away Ignore* | project v = a`, "2"},
	}
	for _, c := range cases {
		tbl := queryResult(t, c.q)
		if len(tbl.Rows) == 0 {
			t.Errorf("%s: no rows", c.q)
			continue
		}
		if got := cell(t, tbl, 0, len(tbl.Schema.Columns)-1); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.q, got, c.want)
		}
	}
	queryError(t, `print A=1 | project-away x`)
	queryError(t, `range x from 1 to 3 step 1 | extend b = bin_auto(x)`)
	queryError(t, `declare query_parameters(a:string); print v = 1`)
	if tbl := queryResult(t, "set query_take_max_records=2; range x from 1 to 5 step 1"); len(tbl.Rows) != 2 {
		t.Errorf("query_take_max_records=2: %d rows", len(tbl.Rows))
	}
	st, err := parser.ParseWithParameters(`declare query_parameters(a:string, n:long = 1); print v = strcat(a, n)`, map[string]string{"a": "given", "n": "5"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(catalog.NewMemory()).Execute(st)
	if err != nil || formatCell(r, 0, 0) != "given5" {
		t.Errorf("supplied parameters: %v %v", err, r)
	}
}
