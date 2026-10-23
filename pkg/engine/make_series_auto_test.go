package engine

import "testing"

// make-series with from and/or to omitted: per-group ranges from the
// data. Expected values are the Kusto emulator's.
func TestMakeSeriesAutoRange(t *testing.T) {
	d := `datatable(t:datetime, v:long)[datetime(2026-01-01 05:00),1, datetime(2026-01-03 13:00),2, datetime(2026-01-04 23:00),5, datetime(2026-01-06 01:00),7] | make-series s=sum(v) default=0 on t `
	g := `datatable(g:string, t:datetime, v:long)["a",datetime(2026-01-01),1, "a",datetime(2026-01-02),1, "b",datetime(2026-01-04),2] | make-series s=sum(v) default=0 on t `
	cases := []struct{ q, want string }{
		{d + `step 1d`, "[1,0,2,5,0,7]"},
		{d + `to datetime(2026-01-04) step 1d`, "[1,0,2,5]"},         // to on the grid: its bin included
		{d + `to datetime(2026-01-03 12:00) step 1d`, "[1,0,0]"},     // off the grid: bins aligned to it
		{d + `to datetime(2026-01-10) step 2d`, "[1,2,5,7,0]"},       // odd day for 2d: excluded
		{d + `from datetime(2026-01-02 12:00) step 1d`, "[0,2,5,7]"}, // through the bin holding max
		{d + `from datetime(2025-12-30) step 1d`, "[0,0,1,0,2,5,0,7]"},
		{g + `step 1d by g | where g == "b" | project s`, "[2]"}, // each group its own range
		{g + `from datetime(2026-01-01) step 1d by g | where g == "b" | project s`, "[0,0,0,2]"},
		{g + `to datetime(2026-01-05) step 1d by g | where g == "b" | project s`, "[2,0]"},
		{`datatable(t:long, v:long)[3,1, 7,2, 12,5] | make-series s=sum(v) default=0 on t step 4`, "[1,2,0,5]"},
		{`datatable(t:long, v:long)[3,1, 7,2, 12,5] | make-series s=sum(v) default=0 on t from 5 step 4`, "[2,5]"},
	}
	for _, c := range cases {
		tbl := queryResult(t, c.q)
		col := tbl.Schema.ColumnIndex("s")
		if len(tbl.Rows) == 0 || col < 0 {
			t.Errorf("%s: no series", c.q)
			continue
		}
		if got := cell(t, tbl, 0, col); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.q, got, c.want)
		}
	}
	if n := len(queryResult(t, `datatable(t:datetime, v:long)[] | make-series s=sum(v) on t step 1d`).Rows); n != 0 {
		t.Errorf("empty input: %d rows", n)
	}
}
