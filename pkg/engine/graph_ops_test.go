package engine

import (
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func tableColumns(t *types.Table) string { return strings.Join(columnNames(&t.Schema), ",") }

func cellString(v types.Value) string { return types.FormatValue(v, types.TypeDynamic) }

func executeAll(t *testing.T, q string) []*types.Table {
	t.Helper()
	stmt, err := parser.Parse(q)
	if err != nil {
		t.Fatalf("parse %q: %v", q, err)
	}
	res, err := New(catalog.NewMemory()).ExecuteAll(stmt)
	if err != nil {
		t.Fatalf("execute %q: %v", q, err)
	}
	return res
}

// graph_ops_test.go — graph-to-table options, graph-shortest-paths,
// graph-mark-components and make-graph partitioned-by against their
// ADX reference examples, plus query batches.

const gcAttackGraph = `let nodes = datatable(name:string, type:string, age:long)
[
	"Alice", "Person", 23,
	"Bob", "Person", 31,
	"Eve", "Person", 17,
	"Trent", "System", 99
];
let edges = datatable(source:string, destination:string, edge_type:string)
[
	"Alice", "Bob", "communicatesWith",
	"Alice", "Trent", "trusts",
	"Bob", "Trent", "hasPermission",
	"Eve", "Alice", "attacks",
	"Mallory", "Alice", "attacks",
	"Mallory", "Bob", "attacks"
];
` + "edges | make-graph source --> destination with nodes on name | "

func TestGraphDocToTableEdgesWithIds(t *testing.T) {
	tbl := queryResult(t, gcAttackGraph+`graph-to-table edges with_source_id=SourceId with_target_id=TargetId`)
	if got := tableColumns(tbl); got != "SourceId,TargetId,source,destination,edge_type" {
		t.Fatalf("columns %s", got)
	}
	expectGraphRows(t, gcAttackGraph+`graph-to-table edges with_source_id=SourceId with_target_id=TargetId`,
		`-3122868243544336885 | -7133945255344544237 | Alice | Bob | communicatesWith`,
		`-3122868243544336885 | 2533909231875758225 | Alice | Trent | trusts`,
		`-7133945255344544237 | 2533909231875758225 | Bob | Trent | hasPermission`,
		`4363395278938690453 | -3122868243544336885 | Eve | Alice | attacks`,
		`3855580634910899594 | -3122868243544336885 | Mallory | Alice | attacks`,
		`3855580634910899594 | -7133945255344544237 | Mallory | Bob | attacks`)
}

var gcAttackNodes = []string{
	`-3122868243544336885 | Alice | Person | 23`,
	`-7133945255344544237 | Bob | Person | 31`,
	`4363395278938690453 | Eve | Person | 17`,
	`2533909231875758225 | Trent | System | 99`,
	`3855580634910899594 | Mallory |  | `,
}

func TestGraphDocToTableNodesWithId(t *testing.T) {
	tbl := queryResult(t, gcAttackGraph+`graph-to-table nodes with_node_id=NodeId`)
	// Node-table rows first, then the node known only from edges.
	var order []string
	for _, r := range tbl.Rows {
		order = append(order, r[1].(string))
	}
	if got := strings.Join(order, ","); got != "Alice,Bob,Eve,Trent,Mallory" {
		t.Fatalf("node order %s", got)
	}
	expectGraphRows(t, gcAttackGraph+`graph-to-table nodes with_node_id=NodeId`, gcAttackNodes...)
}

func TestGraphDocToTableNodesAndEdges(t *testing.T) {
	q := gcAttackGraph + "graph-to-table nodes as N with_node_id=NodeId, edges as E with_source_id=SourceId;\nN;\nE"
	res := executeAll(t, q)
	if len(res) != 2 || res[0].Name != "N" || res[1].Name != "E" {
		t.Fatalf("want results N, E; got %d", len(res))
	}
	if got := tableColumns(res[1]); got != "SourceId,source,destination,edge_type" {
		t.Fatalf("E columns %s", got)
	}
	if len(res[0].Rows) != 5 || len(res[1].Rows) != 6 {
		t.Fatalf("rows %d, %d", len(res[0].Rows), len(res[1].Rows))
	}
	queryError(t, gcAttackGraph+`graph-to-table nodes as N, edges`)      // aliases required
	queryError(t, gcAttackGraph+`graph-to-table nodes with_source_id=X`) // option of the other output
}

func TestGraphDocShortestPaths(t *testing.T) {
	q := gcStations + `connections
| make-graph from_station --> to_station with_node_id=station
| graph-shortest-paths %s(start)-[connections*1..5]->(destination)
  where start.station == "South-West" and destination.station == "North"
  project from = start.station, path = map(connections, to_station), line = map(connections, line), to = destination.station`
	expectGraphRows(t, strings.Replace(q, "%s", "", 1),
		`South-West | ["South","Central","North"] | ["red","red","red"] | North`)
	expectGraphRows(t, strings.Replace(q, "%s", "output=all ", 1),
		`South-West | ["South","Central","North"] | ["red","red","red"] | North`,
		`South-West | ["West","Central","North"] | ["red","blue","red"] | North`)
	// Per source/target pair: every reachable destination once. The
	// search never revisits a node, so no path returns to its start.
	expectGraphRows(t, gcStations+`connections | make-graph from_station --> to_station with_node_id=station
| graph-shortest-paths (s)-[e*1..5]->(d) where s.station == "North" project d.station, n = array_length(e)`,
		`Central | 1`, `East | 2`, `South | 2`, `South-West | 3`, `West | 2`)
	g := gcStations + `connections | make-graph from_station --> to_station with_node_id=station | graph-shortest-paths `
	queryError(t, g+`(s)-[e]->(d) project d.station`)                 // needs a variable-length edge
	queryError(t, g+`(s)-[e*1..2]->(d), (s)-->(x) project d.station`) // one sequence only
	queryError(t, g+`output=some (s)-[e*1..2]->(d) project d.station`)
}

const gcChildOf = `let ChildOf = datatable(child:string, parent:string)
[
  "Alice", "Bob",
  "Carol", "Alice",
  "Carol", "Dave",
  "Greg", "Alice",
  "Greg", "Dave",
  "Howard", "Alice",
  "Howard", "Dave",
  "Eve", "Frank",
  "Frank", "Mallory",
  "Eve", "Kirk",
];
`

func TestGraphDocMarkComponents(t *testing.T) {
	tbl := queryResult(t, gcChildOf+`ChildOf | make-graph child --> parent with_node_id=name | graph-mark-components with_component_id = family | graph-to-table nodes`)
	var got []string
	for _, r := range tbl.Rows {
		got = append(got, r[0].(string)+"="+cellString(r[1]))
	}
	want := "Alice=0,Bob=0,Carol=0,Dave=0,Greg=0,Howard=0,Eve=1,Frank=1,Mallory=1,Kirk=1"
	if strings.Join(got, ",") != want {
		t.Fatalf("got %s", strings.Join(got, ","))
	}
	expectGraphRows(t, gcChildOf+`ChildOf
| make-graph child --> parent with_node_id=name
| graph-mark-components with_component_id = family
| graph-match (descendant)-[childOf*1..5]->(ancestor)
  project name = ancestor.name, lineage = map(childOf, child), family = ancestor.family
| summarize (generations, name) = arg_max(array_length(lineage),name) by family`,
		`1 | 2 | Mallory`, `0 | 2 | Bob`)
}

func TestGraphMarkComponentsStrong(t *testing.T) {
	q := `let E = datatable(S:string, D:string) ["a","b", "b","c", "c","a", "c","d", "d","e", "e","d", "f","f"];
E | make-graph S --> D with_node_id=n | graph-mark-components kind=strong | graph-to-table nodes | project n, ComponentId`
	expectGraphRows(t, q, `a | 0`, `b | 0`, `c | 0`, `d | 1`, `e | 1`, `f | 2`)
	expectGraphRows(t, strings.Replace(q, "kind=strong", "", 1), `a | 0`, `b | 0`, `c | 0`, `d | 0`, `e | 0`, `f | 1`)
	queryError(t, strings.Replace(q, "kind=strong", "kind=medium", 1))
}

func TestGraphDocPartitionedBy(t *testing.T) {
	expectGraphRows(t, `let nodes = datatable(userId:string, tenantId:string, name:string, department:string, role:string, location:dynamic)
[
    "u001", "CompanyA", "Alice Johnson", "Engineering", "Senior Developer", dynamic({"type": "Point", "coordinates": [-122.4194, 37.7749]}),
    "u002", "CompanyA", "Bob Smith", "Engineering", "Team Lead", dynamic({"type": "Point", "coordinates": [-122.4094, 37.7849]}),
    "u003", "CompanyA", "Charlie Black", "Marketing", "Manager", dynamic({"type": "Point", "coordinates": [-122.4294, 37.7649]}),
    "u004", "CompanyA", "Diana Finch", "HR", "Director", dynamic({"type": "Point", "coordinates": [-122.3994, 37.7949]}),
    "u005", "CompanyA", "Eve Wilson", "Engineering", "Junior Developer", dynamic({"type": "Point", "coordinates": [-122.4394, 37.7549]}),
    "u006", "CompanyB", "Frank Miller", "Sales", "Account Manager", dynamic({"type": "Point", "coordinates": [-74.0060, 40.7128]}),
    "u007", "CompanyB", "Grace Lee", "Engineering", "Senior Developer", dynamic({"type": "Point", "coordinates": [-74.0160, 40.7228]}),
    "u008", "CompanyB", "Henry Davis", "Marketing", "Specialist", dynamic({"type": "Point", "coordinates": [-73.9960, 40.7028]}),
    "u009", "CompanyB", "Ivy Chen", "Engineering", "Team Lead", dynamic({"type": "Point", "coordinates": [-74.0260, 40.7328]}),
    "u010", "CompanyB", "Jack Thompson", "Operations", "Manager", dynamic({"type": "Point", "coordinates": [-73.9860, 40.6928]}),
    "u011", "CompanyC", "Kate Anderson", "Finance", "Analyst", dynamic({"type": "Point", "coordinates": [-97.7431, 30.2672]}),
    "u012", "CompanyC", "Liam Murphy", "Engineering", "Architect", dynamic({"type": "Point", "coordinates": [-97.7331, 30.2772]}),
    "u013", "CompanyC", "Maya Patel", "Product", "Manager", dynamic({"type": "Point", "coordinates": [-97.7531, 30.2572]}),
    "u014", "CompanyC", "Noah Garcia", "Engineering", "Developer", dynamic({"type": "Point", "coordinates": [-97.7631, 30.2472]}),
    "u015", "CompanyC", "Olivia Rodriguez", "Marketing", "Director", dynamic({"type": "Point", "coordinates": [-97.7231, 30.2872]})
];
let edges = datatable(sourceUserId:string, targetUserId:string, tenantId:string, relationshipType:string, strength:int)
[
    "u001", "u002", "CompanyA", "reportsTo", 9,
    "u005", "u002", "CompanyA", "reportsTo", 8,
    "u002", "u003", "CompanyA", "collaborates", 6,
    "u001", "u005", "CompanyA", "mentors", 7,
    "u003", "u004", "CompanyA", "collaborates", 5,
    "u001", "u003", "CompanyA", "communicates", 4,
    "u007", "u009", "CompanyB", "reportsTo", 9,
    "u006", "u010", "CompanyB", "reportsTo", 8,
    "u008", "u006", "CompanyB", "collaborates", 6,
    "u009", "u010", "CompanyB", "communicates", 5,
    "u007", "u008", "CompanyB", "mentors", 7,
    "u006", "u007", "CompanyB", "collaborates", 6,
    "u014", "u012", "CompanyC", "reportsTo", 9,
    "u012", "u013", "CompanyC", "collaborates", 7,
    "u011", "u013", "CompanyC", "collaborates", 6,
    "u013", "u015", "CompanyC", "reportsTo", 8,
    "u012", "u015", "CompanyC", "communicates", 5,
    "u011", "u014", "CompanyC", "mentors", 6
];
edges
| make-graph sourceUserId --> targetUserId with nodes on userId partitioned-by tenantId (
    graph-match cycles=none (n1)-[e*2..4]->(n2)
        where n1.userId != n2.userId and all(e, relationshipType == "collaborates") and
            geo_distance_2points(todouble(n1.location.coordinates[0]), todouble(n1.location.coordinates[1]),
                             todouble(n2.location.coordinates[0]), todouble(n2.location.coordinates[1])) < 10000
        project Start = strcat(n1.name, " (", n1.tenantId, ")"), Tenants = map(e, tenantId), End = strcat(n2.name, " (", n2.tenantId, ")")
)
`,
		`Bob Smith (CompanyA) | ["CompanyA","CompanyA"] | Diana Finch (CompanyA)`,
		`Henry Davis (CompanyB) | ["CompanyB","CompanyB"] | Grace Lee (CompanyB)`)
}

func TestQueryBatch(t *testing.T) {
	res := executeAll(t, "print a=1; print b=2")
	if len(res) != 2 || cellString(res[0].Rows[0][0]) != "1" || cellString(res[1].Rows[0][0]) != "2" {
		t.Fatalf("batch results %v", res)
	}
	// as-bound names and interleaved lets are visible to later statements.
	res = executeAll(t, "datatable(a:long)[1,2,3] | as X; let k = 2; X | where a >= k | count")
	if len(res) != 2 || cellString(res[1].Rows[0][0]) != "2" {
		t.Fatalf("batch with as: %v", res)
	}
	// Line breaks inside a parenthesised sub-pipeline.
	res = executeAll(t, "datatable(a:long)[1] | fork (where a == 1 and\n\ta > 0)")
	if len(res) != 1 || len(res[0].Rows) != 1 {
		t.Fatalf("multi-line fork: %v", res)
	}
}

func TestGeoDistance2Points(t *testing.T) {
	tbl := queryResult(t, `print d = geo_distance_2points(-122.407628, 47.578557, -118.275287, 34.019056), bad = geo_distance_2points(300,1,1,1), zero = geo_distance_2points(1,1,1,1)`)
	d := tbl.Rows[0][0].(float64)
	if d < 1546754.351973 || d > 1546754.351974 {
		t.Fatalf("distance %v, want 1546754.35197381", d)
	}
	if tbl.Rows[0][1] != nil || tbl.Rows[0][2].(float64) != 0 {
		t.Fatalf("bad=%v zero=%v", tbl.Rows[0][1], tbl.Rows[0][2])
	}
}
