package engine

import "testing"

// Expected rows from the Kusto emulator (engine 2025.07.06).
func TestSessionCount(t *testing.T) {
	tbl := queryResult(t, `datatable(Id:string, Ts:datetime)["a",datetime(2020-01-01 00:10),"a",datetime(2020-01-01 02:05),"b",datetime(2020-01-01 00:55),"b",datetime(2020-01-01 02:50),"c",datetime(2020-01-01 00:05),"c",datetime(2020-01-01 00:06),"c",datetime(2020-01-01 03:00)] | evaluate session_count(Id, Ts, datetime(2020-01-01), datetime(2020-01-01 06:00), 1h, 2h) | order by Ts asc`)
	expectColNames(t, tbl, "Ts", "count_sessions")
	expectRows(t, tbl, 3)
	for i, want := range []string{"3", "0", "1"} {
		expectCell(t, tbl, i, 1, want)
	}
	// Start/End inclusive, history before Start ignored, a gap equal to
	// the lookback opens a new session, bins from bin() not Start.
	tbl = queryResult(t, `datatable(Id:string, Ts:datetime)["a",datetime(2020-01-01 00:40),"a",datetime(2020-01-01 02:40),"b",datetime(2020-01-01 00:35),"c",datetime(2020-01-01 01:29),"c",datetime(2020-01-01 06:31), "d", datetime(2020-01-01 00:20)] | evaluate session_count(Id, Ts, datetime(2020-01-01 00:30), datetime(2020-01-01 06:30), 1h, 2h) | order by Ts asc`)
	expectRows(t, tbl, 3)
	for i, want := range []string{"2", "1", "1"} {
		expectCell(t, tbl, i, 1, want)
	}
	tbl = queryResult(t, `datatable(Id:string, Ts:datetime, D:string)["a",datetime(2020-01-01 00:10),"x","a",datetime(2020-01-01 01:20),"y","b",datetime(2020-01-01 00:20),"x","b",datetime(2020-01-01 06:00),"x"] | evaluate session_count(Id, Ts, datetime(2020-01-01), datetime(2020-01-01 06:00), 1h, 2h, D) | order by Ts asc, D asc`)
	expectColNames(t, tbl, "Ts", "D", "count_sessions")
	for i, want := range []string{"2", "1", "1"} {
		expectCell(t, tbl, i, 2, want)
	}
}
