package engine

import (
	"sort"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// graph_compliance_test.go — the graph-match / make-graph reference
// examples from the ADX documentation, with their documented outputs,
// plus edge cases for the pattern features (anonymous elements, zero-
// length ranges, cycles=, repeated and shared variables, multiple node
// tables).

// graphRows runs q and returns its rows as "a | b | c" strings, sorted
// (graph-match output order is unspecified).
func graphRows(t *testing.T, q string) []string {
	t.Helper()
	tbl := queryResult(t, q)
	out := make([]string, 0, len(tbl.Rows))
	for _, r := range tbl.Rows {
		cells := make([]string, len(r))
		for i, v := range r {
			cells[i] = types.FormatValue(v, tbl.Schema.Columns[i].Type)
		}
		out = append(out, strings.Join(cells, " | "))
	}
	sort.Strings(out)
	return out
}

func expectGraphRows(t *testing.T, q string, want ...string) {
	t.Helper()
	got := graphRows(t, q)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rows mismatch\n got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

const gcEmployees = `let employees = datatable(name:string, age:long)
[
  "Alice", 32,
  "Bob", 31,
  "Eve", 27,
  "Joe", 29,
  "Chris", 45,
  "Alex", 35,
  "Ben", 23,
  "Richard", 39,
];
let reports = datatable(employee:string, manager:string)
[
  "Bob", "Alice",
  "Chris", "Alice",
  "Eve", "Bob",
  "Ben", "Chris",
  "Joe", "Alice",
  "Richard", "Bob"
];
`

const gcStations = `let connections = datatable(from_station:string, to_station:string, line:string)
[
  "Central", "North", "red",
  "North", "Central", "red",
  "Central", "South",  "red",
  "South", "Central",  "red",
  "South", "South-West", "red",
  "South-West", "South", "red",
  "South-West", "West", "red",
  "West", "South-West", "red",
  "Central", "East", "blue",
  "East", "Central", "blue",
  "Central", "West", "blue",
  "West", "Central", "blue",
];
let stations = datatable(station:string, wifi:bool)
[
  "Central", true,
  "North", false,
  "South", false,
  "South-West", true,
  "West", true,
  "East", false
];
`

func TestGraphDocMapReportingPath(t *testing.T) {
	expectGraphRows(t, gcEmployees+`reports
| make-graph employee --> manager with employees on name
| graph-match (alice)<-[reports*1..5]-(employee)
  where alice.name == "Alice" and employee.age < 30
  project employee = employee.name, age = employee.age, reportingPath = map(reports, manager)`,
		`Joe | 29 | ["Alice"]`, `Eve | 27 | ["Alice","Bob"]`, `Ben | 23 | ["Alice","Chris"]`)
}

func TestGraphDocInnerNodes(t *testing.T) {
	expectGraphRows(t, gcEmployees+`reports
| make-graph employee --> manager with employees on name
| graph-match (manager)<-[reports*1..5]-(employee)
  where manager.name == "Alice" and all(inner_nodes(reports), age < 40)
  project employee = employee.name, manager = manager.name, reportingPath = map(inner_nodes(reports), name)`,
		`Bob | Alice | []`, `Chris | Alice | []`, `Joe | Alice | []`,
		`Eve | Alice | ["Bob"]`, `Richard | Alice | ["Bob"]`)
}

func TestGraphDocAllRoundTrips(t *testing.T) {
	expectGraphRows(t, gcStations+`connections
| make-graph from_station --> to_station with_node_id=station
| graph-match (start)-[outward*1..5]->(destination)-[return*1..5]->(start)
  where start.station != destination.station and all(outward, line == "red") and all(return, line == "blue")
  project from = start.station, outward_stations = strcat_array(map(inner_nodes(outward), station), "->"), to = destination.station, return_stations = strcat_array(map(inner_nodes(return), station), "->"), back=start.station`,
		`Central | North->Central->South->South-West | West |  | Central`,
		`West | South-West->South->Central->North | Central |  | West`,
		`Central | South->South-West | West |  | Central`,
		`West | South-West->South | Central |  | West`,
		`Central | North->Central->South->South-West | West | Central->East | Central`,
		`West | South-West->South->Central->North | Central | East->Central | West`,
		`Central | South->South-West | West | Central->East | Central`,
		`West | South-West->South | Central | East->Central | West`)
}

func TestGraphDocMapCyclesNone(t *testing.T) {
	expectGraphRows(t, gcStations+`connections
| make-graph from_station --> to_station with stations on station
| graph-match cycles=none (start)-[connections*1..5]->(destination)
  where start.station == "South-West" and destination.station == "East"
  project stopovers = strcat_array(map(inner_nodes(connections), station), "->"), stopovers_with_wifi = set_intersect(map(inner_nodes(connections), station), map(inner_nodes(connections), iff(wifi, station, "")))`,
		`West->Central | ["West","Central"]`, `South->Central | ["Central"]`)
}

func TestGraphDocAnyInnerNodes(t *testing.T) {
	expectGraphRows(t, gcStations+`connections
| make-graph from_station --> to_station with stations on station
| graph-match cycles=none (start)-[connections*2..5]->(destination)
  where start.station == "South-West" and destination.station == "North" and any(inner_nodes(connections), wifi)
  project from = start.station, stations = strcat_array(map(inner_nodes(connections), station), "->"), to = destination.station`,
		`South-West | South->Central | North`, `South-West | West->Central | North`)
}

func TestGraphDocAnyEdges(t *testing.T) {
	q := `let Locations = datatable(LocationName: string, LocationType: string) [
  "New York", "City",
  "San Francisco", "City",
  "Chicago", "City",
  "Los Angeles", "City",
  "Seattle", "Warehouse"
];
let Routes = datatable(OriginLocationID: string, DestinationLocationID: string, TransportMode: string) [
  "New York", "San Francisco", "Truck",
  "New York", "Chicago", "Train",
  "San Francisco", "Los Angeles", "Truck",
  "Chicago", "Seattle", "Train",
  "Los Angeles", "New York", "Truck",
  "Seattle", "San Francisco", "Train"
];
Routes
| make-graph OriginLocationID --> DestinationLocationID with Locations on LocationName
| graph-match (src)-[route*1..2]->(dest)
  where any(route, TransportMode == "Train")
  project src.LocationName, dest.LocationName, route_TransportModes = map(route, TransportMode)
`
	tbl := queryResult(t, q)
	if got := tbl.Schema.Columns[0].Name + "," + tbl.Schema.Columns[1].Name; got != "src_LocationName,dest_LocationName" {
		t.Fatalf("column names %s", got)
	}
	expectGraphRows(t, q,
		`Seattle | San Francisco | ["Train"]`, `Chicago | Seattle | ["Train"]`, `New York | Chicago | ["Train"]`,
		`Seattle | Los Angeles | ["Train","Truck"]`, `Chicago | San Francisco | ["Train","Train"]`,
		`New York | Seattle | ["Train","Train"]`, `Los Angeles | Chicago | ["Truck","Train"]`)
}

func TestGraphDocMultipleSequences(t *testing.T) {
	expectGraphRows(t, `let Entities = datatable(name:string, type:string, age:long)
[
  "Alice", "Person", 23,
  "Bob", "Person", 31,
  "Eve", "Person", 17,
  "Mallory", "Person", 29,
  "Apollo", "System", 99
];
let Actions = datatable(source:string, destination:string, action_type:string)
[
  "Alice", "Bob", "communicatesWith",
  "Alice", "Apollo", "trusts",
  "Bob", "Apollo", "hasPermission",
  "Eve", "Alice", "attacks",
  "Mallory", "Alice", "attacks",
  "Mallory", "Bob", "attacks"
];
Actions
| make-graph source --> destination with Entities on name
| graph-match (mallory)-[attacks]->(compromised)-[hasPermission]->(apollo), (compromised)-[communicates]-(alice)
  where mallory.name == "Mallory" and apollo.name == "Apollo" and attacks.action_type == "attacks" and hasPermission.action_type == "hasPermission" and alice.name == "Alice"
  project Attacker = mallory.name, Compromised = compromised.name, System = apollo.name
`, `Mallory | Bob | Apollo`)
}

const gcAttackEdges = `let edges = datatable(source:string, destination:string, edge_type:string)
[
  "Alice", "Bob", "communicatesWith",
  "Alice", "Trent", "trusts",
  "Bob", "Trent", "hasPermission",
  "Eve", "Alice", "attacks",
  "Mallory", "Alice", "attacks",
  "Mallory", "Bob", "attacks"
];
`

func TestGraphDocWithNodeId(t *testing.T) {
	expectGraphRows(t, gcAttackEdges+`edges
| make-graph source --> destination with_node_id=name
| graph-match (mallory)-[attacks]->(compromised)-[hasPermission]->(trent)
  where mallory.name == "Mallory" and trent.name == "Trent" and attacks.edge_type == "attacks" and hasPermission.edge_type == "hasPermission"
  project Attacker = mallory.name, Compromised = compromised.name, System = trent.name`,
		`Mallory | Bob | Trent`)
}

const gcChain = `let E = datatable(Src:string, Dst:string) ["a","b", "b","c", "c","d"];
`

func TestGraphAnonymousElements(t *testing.T) {
	g := gcChain + `E | make-graph Src --> Dst with_node_id=id | `
	expectGraphRows(t, g+`graph-match (x)-->()-->(y) project x.id, y.id`, `a | c`, `b | d`)
	expectGraphRows(t, g+`graph-match (y)<--()<--(x) project x.id, y.id`, `a | c`, `b | d`)
	expectGraphRows(t, g+`graph-match (x)--(y) where x.id == "b" project y.id`, `a`, `c`)
	expectGraphRows(t, g+`graph-match (x)-[*2..3]->(y) where x.id == "a" project y.id`, `c`, `d`)
	expectGraphRows(t, g+`graph-match (x)-[]->(y) where x.id == "c" project y.id`, `d`)
	expectGraphRows(t, g+`graph-match (x) project x.id`, `a`, `b`, `c`, `d`)
}

func TestGraphZeroLengthRange(t *testing.T) {
	expectGraphRows(t, gcChain+`E | make-graph Src --> Dst with_node_id=id
| graph-match (x)-[e*0..2]->(y) where x.id == "a"
  project y.id, n = array_length(e), m = map(e, Dst), a = all(e, false), b = any(e, true), i = map(inner_nodes(e), id)`,
		`a | 0 | [] | true | false | []`, `b | 1 | ["b"] | false | true | []`, `c | 2 | ["b","c"] | false | true | ["b"]`)
}

func TestGraphCyclesModes(t *testing.T) {
	g := `let E = datatable(Src:string, Dst:string) ["a","b", "b","a"];
E | make-graph Src --> Dst with_node_id=id | graph-match `
	tail := `(x)-[e*1..4]->(y) where x.id == "a" project y.id, n = array_length(e)`
	expectGraphRows(t, g+tail, `b | 1`, `a | 2`)
	expectGraphRows(t, g+`cycles=unique_edges `+tail, `b | 1`, `a | 2`)
	expectGraphRows(t, g+`cycles=all `+tail, `b | 1`, `a | 2`, `b | 3`, `a | 4`)
	expectGraphRows(t, g+`cycles=none `+tail, `b | 1`)
	// A repeated variable closes the cycle explicitly, in every mode.
	expectGraphRows(t, g+`cycles=none (x)-->(y)-->(x) project x.id, y.id`, `a | b`, `b | a`)
	queryError(t, g+`cycles=sometimes `+tail)
}

func TestGraphSharedVariables(t *testing.T) {
	g := `let E = datatable(Src:string, Dst:string, K:string) ["a","b","p", "a","c","q", "d","b","r", "d","c","s"];
E | make-graph Src --> Dst with_node_id=id | graph-match `
	// Diamond: two sequences sharing both endpoints. y = a would reuse
	// edges a->b and a->c, which unique_edges forbids; cycles=all allows.
	diamond := `(x)-[e1]->(m1)<-[e2]-(y), (x)-[e3]->(m2)<-[e4]-(y) where x.id == "a" and m1.id == "b" and m2.id == "c" project y.id, e1.K, e2.K, e3.K, e4.K`
	expectGraphRows(t, g+diamond, `d | p | r | q | s`)
	expectGraphRows(t, g+`cycles=all `+diamond, `a | p | p | q | q`, `d | p | r | q | s`)
	// The second sequence is traversed from its shared right end.
	expectGraphRows(t, g+`(x)-[e1]->(m), (z)-[e2]->(m) where x.id == "a" and z.id != "a" project m.id, z.id, e2.K`,
		`b | d | r`, `c | d | s`)
	queryError(t, g+`(x)-[e]->(y), (x)-[e]->(z) project x.id`) // duplicate edge variable
	queryError(t, g+`(x)-[y]->(y) project x.id`)               // edge and node share a name
	queryError(t, g+`(x)-[e]->(y) project m = map(x, id)`)     // not an edge variable: map unknown
	queryError(t, g+`(x)-[e]->(y) project m = inner_nodes(e)`) // inner_nodes outside all/any/map
}

func TestGraphTwoNodeTables(t *testing.T) {
	// Properties merge by node id; the first table's id column also
	// carries the id of nodes known only from edges.
	q := `let People = datatable(pid:string, age:long) ["a", 30, "b", 40];
let Hosts = datatable(hid:string, os:string, age:long) ["c", "linux", 5, "a", "win", 99];
let E = datatable(Src:string, Dst:string) ["a","b", "b","c", "c","d"];
E | make-graph Src --> Dst with People on pid, Hosts on hid with_node_id=nid `
	expectGraphRows(t, q+`| graph-to-table nodes | project nid, pid, age, hid, os`,
		`a | a | 30 | a | win`, `b | b | 40 |  | `, `c |  | 5 | c | linux`, `d | d |  |  | `)
	expectGraphRows(t, q+`| graph-match (x)-->(y) project x.nid, x.age, y.os`,
		`a | 30 | `, `b | 40 | linux`, `c | 5 | `)
}
