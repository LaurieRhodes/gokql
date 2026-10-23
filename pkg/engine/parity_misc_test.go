package engine

import "testing"

func TestParityMiscSweepFixes(t *testing.T) {
	cases := []struct{ q, want string }{
		// bag_pack keeps argument order and embeds dynamic values as JSON.
		{`print tostring(bag_pack("z", 1, "a", dynamic([1,2]), "m", "s"))`, `{"z":1,"a":[1,2],"m":"s"}`},
		{`print tostring(bag_pack("k", 1, "j", 2, "k", 3))`, `{"k":3,"j":2}`},
		// in (dynamic([...])) expands the array.
		{`print 2 in (dynamic([1,2,3]))`, "true"},
		{`print "b" in (dynamic(["a","c"]))`, "false"},
		// Datetime bins align to 0001-01-01, so 7d bins start on Mondays.
		{`print bin(datetime(2007-01-03), 7d)`, "2007-01-01T00:00:00Z"},
		{`print bin(datetime(2007-01-03 13:00), 1d)`, "2007-01-03T00:00:00Z"},
		// percentiles_array_tdigest takes a trailing typeof() for the output type.
		{`datatable(x:long)[1,2,3,4,5] | summarize d = tdigest(x) | project p = percentiles_array_tdigest(d, 50, typeof(long)) | project tostring(p)`, "[3]"},
	}
	for _, c := range cases {
		tbl := queryResult(t, c.q)
		expectCell(t, tbl, 0, 0, c.want)
	}
}
