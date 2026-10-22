package engine

import (
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// graph_model_test.go — persistent graph models, snapshots, graph()
// and labels().

const gmModel = ".create-or-alter graph_model Simple ```\n" + `{
  "Schema": {
    "Nodes": {"Person": {"name": "string"}, "Company": {"name": "string"}},
    "Edges": {"works_at": {"since": "long"}, "knows": {}}
  },
  "Definition": {
    "Steps": [
      {"Kind": "AddNodes", "Query": "People | project id, name, properties, kind", "NodeIdColumn": "id", "Labels": ["Person"], "LabelsColumn": "kind"},
      {"Kind": "AddNodes", "Query": "Companies", "NodeIdColumn": "id", "Labels": ["Company"]},
      {"Kind": "AddEdges", "Query": "WorksAt", "SourceColumn": "person", "TargetColumn": "company", "Labels": ["works_at"]},
      {"Kind": "AddEdges", "Query": "Knows", "SourceColumn": "a", "TargetColumn": "b", "Labels": ["knows"], "LabelsColumn": "how"}
    ]
  }
}` + "\n```"

func graphModelScope(t *testing.T) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.set-or-append People <| datatable(id:string, name:string, properties:dynamic, kind:string) [`+
		`"p1","Alice",dynamic({"age":25}),"Engineer", "p2","Bob",dynamic({"age":30}),"Manager", `+
		`"p3","Emma",dynamic({"age":26}),"Engineer", "p4","Dan",dynamic({"age":41}),"Manager"]`)
	diskExec(t, eng, `.set-or-append Companies <| datatable(id:string, name:string) ["c1","TechCorp", "c2","DataSoft"]`)
	diskExec(t, eng, `.set-or-append WorksAt <| datatable(person:string, company:string, since:long) [`+
		`"p1","c1",2019, "p2","c1",2015, "p3","c1",2021, "p4","c2",2010]`)
	diskExec(t, eng, `.set-or-append Knows <| datatable(a:string, b:string, how:string) [`+
		`"p1","p2","colleague", "p2","p4","friend", "p4","p9","friend"]`)
	diskExec(t, eng, gmModel)
	return eng, dir
}

func gmRows(t *testing.T, eng *Engine, q string) []string {
	t.Helper()
	tbl := diskQuery(t, eng, q)
	var out []string
	for _, r := range tbl.Rows {
		cells := make([]string, len(r))
		for i, v := range r {
			cells[i] = types.FormatValue(v, tbl.Schema.Columns[i].Type)
		}
		out = append(out, strings.Join(cells, " | "))
	}
	return out
}

func expectGM(t *testing.T, eng *Engine, q string, want ...string) {
	t.Helper()
	got := gmRows(t, eng, q)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s\n got:\n  %s\nwant:\n  %s", q, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestGraphModelQueryAndLabels(t *testing.T) {
	eng, _ := graphModelScope(t)
	// The graph-sample-data / labels() reference queries, over data shaped like "Simple".
	expectGM(t, eng, `graph("Simple") | graph-match (person)-[works_at]->(company)
    where labels(person) has "Person" and company.name == "TechCorp"
    project employee_name = person.name, employee_age = person.properties.age, employee_labels = labels(person)
    | sort by employee_name asc`,
		`Alice | 25 | ["Person","Engineer"]`, `Bob | 30 | ["Person","Manager"]`, `Emma | 26 | ["Person","Engineer"]`)
	expectGM(t, eng, `graph("Simple") | graph-match (person1)-->(company)<--(person2)
    where person1.id != person2.id and labels(company) has "Company"
    project colleague1 = person1.name, colleague2 = person2.name, company = company.name | sort by colleague1 asc, colleague2 asc | take 1`,
		`Alice | Bob | TechCorp`)
	// labels() inside map/all/any, of edges and of inner nodes; p9 is a
	// placeholder node (referenced by an edge only): no properties.
	expectGM(t, eng, `graph("Simple") | graph-match (p)-[k*2..3]->(q)
    where p.name == "Alice" and all(k, labels() has "knows")
    project q.id, chain = map(k, labels()), inner = map(inner_nodes(k), labels()), n = labels(q) | extend hops = array_length(chain) | sort by hops asc | project-away hops`,
		`p4 | [["knows","colleague"],["knows","friend"]] | [["Person","Manager"]] | ["Person","Manager"]`,
		` | [["knows","colleague"],["knows","friend"],["knows","friend"]] | [["Person","Manager"],["Person","Manager"]] | []`)
	// make-graph graphs have no labels.
	expectGM(t, eng, `WorksAt | make-graph person --> company | graph-match (a)-[e]->(b) project l = labels(a), m = labels(e) | take 1`, `[] | []`)
	diskQueryError(t, eng, `graph("Simple") | graph-match (a)-->(b) project l = labels(zz)`)
	diskQueryError(t, eng, `graph("Simple") | graph-match (a)-->(b) project l = labels()`)
	diskQueryError(t, eng, `graph("Nope") | graph-match (a)-->(b) project a.id`)
}

func TestGraphSnapshotsPointInTime(t *testing.T) {
	eng, dir := graphModelScope(t)
	count := `| graph-match (p)-[w]->(c) where labels(w) has "works_at" project p.id | count`
	diskExec(t, eng, `.make graph_snapshot S1 from Simple`)
	diskExec(t, eng, `.set-or-append WorksAt <| datatable(person:string, company:string, since:long) ["p4","c1",2024]`)
	expectGM(t, eng, `graph("Simple") `+count, `4`) // latest snapshot, frozen before the append
	diskExec(t, eng, `.make async graph_snapshot S2 from Simple`)
	expectGM(t, eng, `graph("Simple") `+count, `5`)
	expectGM(t, eng, `graph("Simple", "S1") `+count, `4`)
	diskQueryError(t, eng, `.make graph_snapshot S1 from Simple`) // names are unique per model
	diskQueryError(t, eng, `graph("Simple", "S9") `+count)

	// Snapshots persist: a fresh engine on the same directory reads them,
	// with typed properties intact.
	eng2 := discoverEngine(t, dir)
	expectGM(t, eng2, `graph("Simple", "S1") | graph-match (p)-[w]->(c) where p.name == "Bob" and labels(w) has "works_at" project w.since, typeof = gettype(w.since), age = p.properties.age`,
		`2015 | long | 30`)
	expectGM(t, eng2, `.show graph_snapshots Simple | project Name, ModelName`, `S1 | Simple`, `S2 | Simple`)
	if tbl := diskQuery(t, eng2, `.show graph_snapshot Simple.S1 details`); tbl.Rows[0][5] != int64(7) || tbl.Rows[0][6] != int64(7) {
		t.Fatalf("S1 counts %v %v", tbl.Rows[0][5], tbl.Rows[0][6])
	}

	diskExec(t, eng2, `.drop graph_snapshot Simple.S2`)
	expectGM(t, eng2, `graph("Simple") `+count, `4`)
	diskExec(t, eng2, `.drop graph_snapshots Simple`)
	expectGM(t, eng2, `graph("Simple") `+count, `5`) // no snapshot: built from the model
	expectGM(t, eng2, `.show graph_snapshots * | count`, `0`)
}

func TestGraphModelVersionsAndDrop(t *testing.T) {
	eng, _ := graphModelScope(t)
	diskExec(t, eng, strings.Replace(gmModel, `"Labels": ["Company"]`, `"Labels": ["Company"], "LabelsColumn": "name"`, 1))
	expectGM(t, eng, `.show graph_model Simple with (id="*") | count`, `2`)
	expectGM(t, eng, `.show graph_models | project Name`, `Simple`)
	expectGM(t, eng, `graph("Simple") | graph-match (c) where labels(c) has "Company" project c.name, labels(c) | sort by c_name asc`,
		`DataSoft | ["Company","DataSoft"]`, `TechCorp | ["Company","TechCorp"]`)
	diskExec(t, eng, `.make graph_snapshot S from Simple`)
	diskExec(t, eng, `.drop graph_model Simple`)
	expectGM(t, eng, `.show graph_models | count`, `0`)
	expectGM(t, eng, `.show graph_snapshots * | count`, `0`)
	diskQueryError(t, eng, `graph("Simple") | graph-match (a)-->(b) project a.id`)
	diskQueryError(t, eng, `.drop graph_model Simple`)
	// Recreated after a drop: the old versions do not come back.
	diskExec(t, eng, gmModel)
	expectGM(t, eng, `.show graph_model Simple with (id="*") | count`, `1`)
}

func TestGraphModelValidation(t *testing.T) {
	eng, _ := graphModelScope(t)
	bad := []string{
		`{"Definition": {"Steps": []}}`,
		`{"Definition": {"Steps": [{"Kind": "AddThings", "Query": "People", "NodeIdColumn": "id"}]}}`,
		`{"Definition": {"Steps": [{"Kind": "AddNodes", "Query": "People"}]}}`,
		`{"Definition": {"Steps": [{"Kind": "AddEdges", "Query": "Knows", "SourceColumn": "a"}]}}`,
		`{"Definition": {"Steps": [{"Kind": "AddNodes", "Query": "People | where (", "NodeIdColumn": "id"}]}}`,
		`{"Schema": {"Nodes": {"Person": {}}}, "Definition": {"Steps": [{"Kind": "AddNodes", "Query": "People", "NodeIdColumn": "id", "Labels": ["Robot"]}]}}`,
		`{"Definition": {"Steps": [{"Kind": "AddNodes", "Query": "People", "NodeIdColum": "id"}]}}`,
		`not json`,
	}
	for _, b := range bad {
		diskQueryError(t, eng, ".create-or-alter graph_model Bad ```"+b+"```")
	}
	// A step whose column is missing fails when the graph is built.
	diskExec(t, eng, ".create-or-alter graph_model Late ```"+`{"Definition": {"Steps": [{"Kind": "AddNodes", "Query": "People", "NodeIdColumn": "nope"}]}}`+"```")
	diskQueryError(t, eng, `graph("Late") | graph-match (a) project a.id`)
	diskQueryError(t, eng, `.make graph_snapshot S from Late`)
}

func TestGraphSnapshotValueRoundTrip(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.set-or-append N <| datatable(id:long, d:datetime, s:timespan, r:real, b:bool, i:int, j:dynamic, m:decimal) [`+
		`9007199254740993, datetime(2024-01-02 03:04:05.1234567), 1d, 0.1, true, 7, dynamic({"x":[1,2]}), decimal(1.10)]`)
	diskExec(t, eng, `.set-or-append E <| datatable(s:long, t:long) [9007199254740993, 9007199254740993]`)
	diskExec(t, eng, ".create-or-alter graph_model M ```"+`{"Definition": {"Steps": [
		{"Kind": "AddNodes", "Query": "N | extend nan = real(nan)", "NodeIdColumn": "id"},
		{"Kind": "AddEdges", "Query": "E", "SourceColumn": "s", "TargetColumn": "t"}]}}`+"```")
	q := `| graph-match (a)-->(b) project a.id, a.d, a.s, a.r, a.b, a.i, a.j, a.m, a.nan, typeof = gettype(a.m)`
	live := gmRows(t, eng, `graph("M") `+q)
	diskExec(t, eng, `.make graph_snapshot S from M`)
	snap := gmRows(t, discoverEngine(t, dir), `graph("M", "S") `+q)
	if strings.Join(live, "\n") != strings.Join(snap, "\n") || len(live) != 1 {
		t.Fatalf("snapshot round trip:\n live %v\n snap %v", live, snap)
	}
	if !strings.HasPrefix(live[0], "9007199254740993 | ") {
		t.Fatalf("long id lost precision: %s", live[0])
	}
}
