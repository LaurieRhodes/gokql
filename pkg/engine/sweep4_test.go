package engine

import "testing"

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
}
