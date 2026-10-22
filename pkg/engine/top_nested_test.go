package engine

import (
	"sort"
	"strings"
	"testing"
)

// top_nested_test.go — top-nested against the reference examples
// (example 4's data and expected rows; example 1's shape) and
// with others.

func TestTopNestedReferenceLatestPerIdentity(t *testing.T) {
	r := queryResult(t, `datatable(id: string, timestamp: datetime, otherInformation: string)[
		"Barak", datetime(2015-01-01), "1", "Barak", datetime(2016-01-01), "2",
		"Barak", datetime(2017-01-20), "3", "Donald", datetime(2017-01-20), "4",
		"Donald", datetime(2017-01-18), "5", "Donald", datetime(2017-01-19), "6"]
		| top-nested of id by Ignore0=max(1),
		  top-nested 2 of timestamp by Ignore1=max(timestamp),
		  top-nested of otherInformation by Ignore2=max(1)
		| project-away Ignore0, Ignore1, Ignore2`)
	assertColumns(t, columnNames(&r.Schema), "id", "timestamp", "otherInformation")
	var got []string
	for i := range r.Rows {
		got = append(got, formatCell(r, i, 0)+" "+formatCell(r, i, 1)+" "+formatCell(r, i, 2))
	}
	sort.Strings(got)
	want := []string{"Barak 2016-01-01T00:00:00Z 2", "Barak 2017-01-20T00:00:00Z 3",
		"Donald 2017-01-19T00:00:00Z 6", "Donald 2017-01-20T00:00:00Z 4"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("rows = %v, want %v", got, want)
	}
}

func TestTopNestedLevelsAndNames(t *testing.T) {
	r := queryResult(t, `datatable(State:string, EventType:string, EndLocation:string, Damage:long)[
		"CA","Fire","",100, "CA","Fire","",50, "CA","Wind","",30, "CA","Flow","",20,
		"OK","Ice","",80, "OK","Flood","COMMERCE",10, "OK","Flood","X",2, "TX","Hail","",5]
		| top-nested 2 of State by sum(Damage),
		  top-nested 3 of EventType by sum(Damage),
		  top-nested 1 of EndLocation by sum(Damage)`)
	assertColumns(t, columnNames(&r.Schema), "State", "aggregated_State", "EventType",
		"aggregated_EventType", "EndLocation", "aggregated_EndLocation")
	assertRows(t, r.Rows, [][]interface{}{
		{"CA", int64(200), "Fire", int64(150), "", int64(150)},
		{"CA", int64(200), "Wind", int64(30), "", int64(30)},
		{"CA", int64(200), "Flow", int64(20), "", int64(20)},
		{"OK", int64(92), "Ice", int64(80), "", int64(80)},
		{"OK", int64(92), "Flood", int64(12), "COMMERCE", int64(10)},
	})
}

func TestTopNestedWithOthersAndAsc(t *testing.T) {
	r := queryResult(t, `datatable(S:string, D:long)["a",5, "b",3, "c",1, "d",1]
		| top-nested 2 of S with others="rest" by T=sum(D)`)
	assertRows(t, r.Rows, [][]interface{}{{"a", int64(5)}, {"b", int64(3)}, {"rest", int64(2)}})
	r = queryResult(t, `datatable(S:string, D:long)["a",5, "b",3, "c",1]
		| top-nested 1 of S by T=sum(D) asc`)
	assertRows(t, r.Rows, [][]interface{}{{"c", int64(1)}})
}
