package engine

import "testing"

// Expected rows from the Kusto emulator (engine 2025.07.06).
func TestActivityEngagement(t *testing.T) {
	src := `datatable(Id:string, Ts:datetime, D:string)["a",datetime(2020-01-01 10:00),"x","b",datetime(2020-01-01 11:00),"y","a",datetime(2020-01-02 09:00),"x","c",datetime(2020-01-03 01:00),"x","a",datetime(2020-01-05),"y","b",datetime(2020-01-08),"x","d",datetime(2020-01-08 05:00),"x","c",datetime(2020-01-09),"y","e",datetime(2020-01-07),"x"]`
	tbl := queryResult(t, src+` | evaluate activity_engagement(Id, Ts, 1d, 36h) | order by Ts asc`)
	expectColNames(t, tbl, "Ts", "dcount_activities_inner", "dcount_activities_outer", "activity_ratio")
	expectRows(t, tbl, 7)
	expectCell(t, tbl, 0, 1, "2")
	expectCell(t, tbl, 1, 2, "1") // outer window [01-01 12:00, 01-03) on raw times
	tbl = queryResult(t, src+` | evaluate activity_engagement(Id, Ts, datetime(2020-01-01 12:00), datetime(2020-01-07), 1d, 3d) | order by Ts asc`)
	expectRows(t, tbl, 4)
	expectCell(t, tbl, 0, 0, "2020-01-01T12:00:00Z") // windows aligned to Start
	expectCell(t, tbl, 3, 0, "2020-01-06T12:00:00Z") // End inclusive
	expectCell(t, tbl, 1, 2, "2")
	tbl = queryResult(t, src+` | evaluate activity_engagement(Id, Ts, 1d, 3d, D) | order by Ts asc, D asc`)
	expectColNames(t, tbl, "Ts", "D", "dcount_activities_inner", "dcount_activities_outer", "activity_ratio")
	expectRows(t, tbl, 8)
	expectCell(t, tbl, 6, 3, "3")
}
