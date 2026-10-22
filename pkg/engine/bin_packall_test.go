package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// bin_packall_test.go — bin/floor and bin_at against the reference
// examples and the floor semantics; pack_all; datatable string
// literals in all KQL forms.

func TestBinAndBinAtReference(t *testing.T) {
	cases := []struct {
		expr, want, typ string
	}{
		{`bin(4.5, 1)`, "4", "real"},
		{`bin(time(16d), 7d)`, "14.00:00:00", "timespan"},
		{`bin(datetime(1970-05-11 13:45:07), 1d)`, "1970-05-11T00:00:00Z", "datetime"},
		{`bin_at(6.5, 2.5, 7)`, "4.5", "real"},
		{`bin_at(time(1h), 1d, 12h)`, "-12:00:00", "timespan"},
		{`bin_at(datetime(2017-05-15 10:20:00.0), 1d, datetime(1970-01-01 12:00:00.0))`, "2017-05-14T12:00:00Z", "datetime"},
		{`bin_at(datetime(2017-05-17 10:20:00.0), 7d, datetime(2017-06-04 00:00:00.0))`, "2017-05-14T00:00:00Z", "datetime"},
		// floor semantics and null rules
		{`bin(7.8, 2.5)`, "7.5", "real"},
		{`bin(-5, 2)`, "-6", "long"},
		{`bin(datetime(1969-12-31 12:00), 1d)`, "1969-12-31T00:00:00Z", "datetime"},
		{`bin(5, -1)`, "", "long"},
		{`bin(10, 0)`, "", "long"},
		{`floor(7, 5)`, "5", "long"},
	}
	for _, c := range cases {
		r := queryResult(t, `print x = `+c.expr+`, t = gettype(`+c.expr+`)`)
		if got := formatCell(r, 0, 0); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
		if got := r.Rows[0][1]; got != c.typ {
			t.Errorf("gettype(%s) = %v, want %s", c.expr, got, c.typ)
		}
	}
}

func TestBinAtSummarizeReference(t *testing.T) {
	r := queryResult(t, `datatable(Date:datetime, NumOfEvents:int)[
		datetime(2018-02-24T15:14),3, datetime(2018-02-24T15:24),4,
		datetime(2018-02-23T16:14),4, datetime(2018-02-23T17:29),4,
		datetime(2018-02-26T15:14),5]
		| summarize TotalEvents=sum(NumOfEvents) by bin_at(Date, 1d, datetime(2018-02-24 15:14:00.0000000))
		| sort by Date asc`)
	assertColumns(t, columnNames(&r.Schema), "Date", "TotalEvents")
	want := []struct {
		d string
		n int64
	}{{"2018-02-23T15:14:00Z", 8}, {"2018-02-24T15:14:00Z", 7}, {"2018-02-26T15:14:00Z", 5}}
	for i, w := range want {
		if got := formatCell(r, i, 0); got != w.d {
			t.Errorf("row %d Date = %s, want %s", i, got, w.d)
		}
		if got := r.Rows[i][1]; got != w.n {
			t.Errorf("row %d TotalEvents = %v, want %d", i, got, w.n)
		}
	}
}

func TestPackAllReference(t *testing.T) {
	r := queryResult(t, `datatable(Source:string,Target:string,CharsCount:long)[
		'555-1234','555-1212',46, '555-1234','555-1213',50,
		'555-1313','',42, '','555-3456',74]
		| extend Packed=pack_all(), PackedIgnoreNullEmpty=pack_all(true)
		| project Packed, PackedIgnoreNullEmpty`)
	want := [][]interface{}{
		{`{"Source":"555-1234","Target":"555-1212","CharsCount":46}`, `{"Source":"555-1234","Target":"555-1212","CharsCount":46}`},
		{`{"Source":"555-1234","Target":"555-1213","CharsCount":50}`, `{"Source":"555-1234","Target":"555-1213","CharsCount":50}`},
		{`{"Source":"555-1313","Target":"","CharsCount":42}`, `{"Source":"555-1313","CharsCount":42}`},
		{`{"Source":"","Target":"555-3456","CharsCount":74}`, `{"Target":"555-3456","CharsCount":74}`},
	}
	assertRows(t, r.Rows, want)
}

// Single-quoted, verbatim and escaped string literals in datatable
// rows are unquoted like the same literals in a query; a comma inside
// a single-quoted value does not split the row.
func TestDatatableStringLiteralForms(t *testing.T) {
	r := queryResult(t, `datatable(s:string, n:long)['abc',1, "def",2, @'x\y',3, '',4, 'a,b',5, 'it\'s',6]`)
	want := [][]interface{}{
		{"abc", int64(1)}, {"def", int64(2)}, {`x\y`, int64(3)},
		{"", int64(4)}, {"a,b", int64(5)}, {"it's", int64(6)},
	}
	assertRows(t, r.Rows, want)
}

// formatCell renders a cell the way the CLI shows it.
func formatCell(r *types.Table, row, col int) string {
	return types.FormatValue(r.Rows[row][col], r.Schema.Columns[col].Type)
}
