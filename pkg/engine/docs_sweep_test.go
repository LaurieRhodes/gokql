package engine

import (
	"strings"
	"testing"
)

// docs_sweep_test.go — behaviour found by running every graph-related
// code block of the ADX documentation (dataexplorer-docs) through the
// engine, and checked against Kusto.Language: graph functions and
// sources, and the general language gaps those blocks exposed.

const sweepEmployees = `let employees = datatable(name:string, age:long)
["Alice", 32, "Bob", 31, "Eve", 27, "Joe", 29, "Chris", 45, "Alex", 35, "Ben", 23, "Richard", 39];
let reports = datatable(employee:string, manager:string)
["Bob", "Alice", "Chris", "Alice", "Eve", "Bob", "Ben", "Chris", "Joe", "Alice", "Richard", "Bob"];
`

func TestGraphDocNodeId(t *testing.T) {
	expectGraphRows(t, `let employees = datatable(name:string, age:long) ["Alice", 32, "Bob", 31, "Eve", 27];
let reports = datatable(employee:string, manager:string) ["Bob", "Alice", "Chris", "Alice", "Eve", "Bob"];
reports
| make-graph employee --> manager with employees on name
| graph-match (employee)-[reports*1..3]->(manager)
    where node_id(employee) startswith "E"
    project manager1 = node_id(manager), manager2 = map(inner_nodes(reports), node_id())`,
		`Bob | []`, `Alice | ["Bob"]`)
}

func TestGraphDocNodeDegree(t *testing.T) {
	// The reference output also lists an "Ellen" row that its own data
	// cannot produce; these are the rows the data gives.
	expectGraphRows(t, sweepEmployees+`reports
| make-graph employee --> manager with employees on name
| graph-match (manager)<-[reports*1..3]-(employee)
    where node_degree_in(manager) == 3 and any(inner_nodes(reports), node_degree_in() > 1)
    project manager.name, employee.name,
            reports_and_inner_nodes_degree_in = map(inner_nodes(reports), strcat(name, " has ", node_degree_in(), " reports")),
            degree_in_m=node_degree_in(manager),
            degree_out_e=node_degree_out(employee)`,
		`Alice | Eve | ["Bob has 2 reports"] | 3 | 1`, `Alice | Richard | ["Bob has 2 reports"] | 3 | 1`)
	expectGraphRows(t, `let reports = datatable(employee:string, manager:string)
["Bob", "Alice", "Chris", "Alice", "Eve", "Bob", "Ben", "Chris", "Joe", "Alice", "Richard", "Bob", "Alice", "Jim"];
reports | make-graph employee --> manager with_node_id=name
| graph-match (manager)<-[reports]-(employee)
where node_degree_out(manager) == 0
project manager.name, employee.name, di_m=node_degree_in(manager), do_m=node_degree_out(manager), di_e=node_degree_in(employee), do_e=node_degree_out(employee)`,
		`Jim | Alice | 1 | 0 | 3 | 1`)
	// Unnamed graph columns as the emulator names them: a pattern
	// property inside a function has no name (strlen_), a graph
	// function of a variable is <fn>_<var>.
	tbl := queryResult(t, `datatable(s:string,d:string)["x","y"] | make-graph s --> d with_node_id=n | graph-match (a)-->(b) project strlen(a.n), tolower(b.n), a.n, node_degree_out(a)`)
	if got := tableColumns(tbl); got != "strlen_,Column1,a_n,node_degree_out_a" {
		t.Fatalf("columns %s", got)
	}
	queryError(t, `datatable(s:string,d:string)["x","y"] | make-graph s --> d | graph-match (a)-[e]->(b) project node_id()`)
	queryError(t, `datatable(s:string,d:string)["x","y"] | make-graph s --> d | graph-match (a)-[e]->(b) project node_id(e)`)
}

func TestGraphLetBoundAndFunctionGraphs(t *testing.T) {
	g := `let E = datatable(s:string, d:string) ["a","b", "b","c", "x","y"];
let G = E | make-graph s --> d with_node_id=id;
`
	// A let-bound graph, reused; graph-mark-components works on a copy.
	expectGraphRows(t, g+`let C = G | graph-mark-components; C | graph-to-table nodes | project id, ComponentId`,
		`a | 0`, `b | 0`, `c | 0`, `x | 1`, `y | 1`)
	expectGraphRows(t, g+`let C = G | graph-mark-components; G | graph-to-table nodes | getschema | project ColumnName`, `id`)
	// A function whose body is a graph.
	expectGraphRows(t, `let F = (minW:long) { datatable(s:string, d:string, w:long)["a","b",1, "b","c",5] | where w >= minW | make-graph s --> d with_node_id=n };
F(2) | graph-match (x)-->(y) project x.n, y.n`, `b | c`)
	expectGraphRows(t, `let F = () { datatable(s:string, d:string)["p","q"] | make-graph s --> d };
let H = F(); H | graph-to-table edges`, `p | q`)
}

func TestGraphHopBoundsFromScalars(t *testing.T) {
	chain := `let E = datatable(s:string,d:string)["a","b","b","c","c","d","d","e"]; `
	expectGraphRows(t, chain+`let lo = 2; let hi = 3; E | make-graph s --> d with_node_id=n | graph-match (x)-[e*lo..(hi+0)]->(y) where x.n == "a" project y.n`,
		`c`, `d`)
	expectGraphRows(t, chain+`let f = (lo:long, hi:long) { E | make-graph s --> d with_node_id=n | graph-shortest-paths cycles=none (x)-[e*lo..hi]->(y) where x.n == "a" project y.n }; f(1, 2)`,
		`b`, `c`)
	queryError(t, chain+`let hi = 100; E | make-graph s --> d | graph-match (x)-[e*1..hi]->(y) project y.NodeId`) // beyond the hop limit
	queryError(t, chain+`E | make-graph s --> d | graph-match (x)-[e*1..nope]->(y) project y.NodeId`)
}

func TestJoinKeepsRightKeys(t *testing.T) {
	tbl := queryResult(t, `datatable(a:string, b:long, n:int)["x",1,1] | join kind=inner (datatable(a:string, c:datetime, b:string)["x",datetime(2020-01-01),"q"]) on a`)
	if got := tableColumns(tbl); got != "a,b,n,a1,c,b1" {
		t.Fatalf("columns %s", got)
	}
	tbl = queryResult(t, `datatable(a:string, a1:long)["x",1] | join (datatable(a:string, a1:string)["x","p"]) on a`)
	if got := tableColumns(tbl); got != "a,a1,a2,a11" {
		t.Fatalf("columns %s", got)
	}
	// Spacing of kind, and a subquery right after the kind.
	expectGraphRows(t, `datatable(a:long)[1,2] | join kind = leftouter (datatable(a:long, b:long)[1,5]) on a | project a, b`, `1 | 5`, `2 | `)
	expectGraphRows(t, `datatable(a:long)[1,2] | join kind=inner(datatable(a:long, b:long)[1,5]) on a | project a, b`, `1 | 5`)
}

func TestSummarizeExpressionsOverAggregates(t *testing.T) {
	d := `datatable(a:long, g:string)[1,'x',2,'x',2,'y'] | `
	expectGraphRows(t, d+`summarize c = count() * 2`, `6`)
	expectGraphRows(t, d+`summarize s = strcat("n=", count())`, `n=3`)
	expectGraphRows(t, d+`summarize x = array_length(make_set(a)), m = max(a) by g`, `x | 2 | 2`, `y | 1 | 2`)
	expectGraphRows(t, d+`summarize r = sum(a) / count(), count() by g`, `x | 1 | 2`, `y | 2 | 1`)
	expectGraphRows(t, d+`summarize z = max(a) - min(a), l = array_slice(make_list(a), 0, 1)`, `1 | [1,2]`)
	for q, cols := range map[string]string{
		`summarize toint(sum(a))`:                               "sum_a",
		`summarize count() * 2, sum(a)`:                         "Column1,sum_a",
		`summarize percentiles(a, 50, 90), z = max(a) - min(a)`: "percentile_a_50,percentile_a_90,z",
		`summarize arg_max(a, g), n = dcount(g) + 0`:            "a,g,n",
	} {
		if got := tableColumns(queryResult(t, d+q)); got != cols {
			t.Errorf("%s: columns %s, want %s", q, got, cols)
		}
	}
	queryError(t, d+`summarize a + 1`)
	queryError(t, d+`summarize max(count())`)
}

func TestLineBreaksAndMultilineStrings(t *testing.T) {
	d := `datatable(a:long, s:string)[1,"x y",2,"z w"]`
	expectGraphRows(t, d+" | summarize c=count()\nby a", `1 | 1`, `2 | 1`)
	expectGraphRows(t, d+" | order by\na desc | project a", `1`, `2`)
	expectGraphRows(t, d+" | parse s with\nf:string \" \" g:string | project f", `x`, `z`)
	expectGraphRows(t, "let x = (\n  datatable(a:long)[1]\n  | extend b = 2\n  );\nx", `1 | 2`)
	expectGraphRows(t, "let n =\n    union\n        (datatable(a:long)[1]),\n        (datatable(a:long)[2]);\nn | count", `2`)
	expectGraphRows(t, "let s = ```line \"one\"; // kept\n\ttwo | three```; print n = strlen(s), has = s has \"three\"", `32 | true`)
	expectGraphRows(t, "print x = \"a```b\", y = ```p```", "a```b | p")
}

func TestScalarCountsTableNameAndNamedArguments(t *testing.T) {
	expectGraphRows(t, `print array_strcat(dynamic([1,2]), ",")`, `1,2`)
	expectGraphRows(t, `let n = 2; range x from 1 to 10 step 1 | take n * 2 | count`, `4`)
	expectGraphRows(t, `let n = 2; range x from 1 to 10 step 1 | top n by x desc`, `10`, `9`)
	expectGraphRows(t, `let f = (k:long) { range x from 1 to 10 step 1 | sort by x desc | take k }; f(3)`, `10`, `8`, `9`)
	expectGraphRows(t, `let T = datatable(a:long)[1,2,3]; let f = (n:string) { table(n) | count }; f("T")`, `3`)
	expectGraphRows(t, `let f = (T:(*), a:long = 1, b:string = "x") { T | project a2 = a, b2 = b }; f(datatable(z:long)[1], b = "named")`, `1 | named`)
	expectGraphRows(t, `let f = (a:long = 1, b:string = "x") { print a, b }; f(b="q", a=5)`, `5 | q`)
	queryError(t, `let f = (a:long = 1, b:string = "x") { print a, b }; f(b="q", 5)`)
	expectGraphRows(t, `let f = (c:string) { datatable(a:long, b:string)[1,"x"] | project v = column_ifexists(c, "none") }; f("b") | union (f("zz"))`, `none`, `x`)
	expectGraphRows(t, "let nodes = view() { datatable(a:long)[1] | count };\nlet edges = view() { datatable(a:long)[1,2] | count };\nunion withsource=['Graph element'] nodes, edges",
		`edges | 2`, `nodes | 1`)
}

func TestGraphSourceArguments(t *testing.T) {
	eng, _ := graphModelScope(t)
	count := `| graph-match (p)-[w]->(c) where labels(w) has "works_at" project p.id | count`
	diskExec(t, eng, `.make graph_snapshot S1 from Simple`)
	diskExec(t, eng, `.set-or-append WorksAt <| datatable(person:string, company:string, since:long) ["p4","c1",2024]`)
	expectGM(t, eng, `graph("Simple") `+count, `4`)
	expectGM(t, eng, `graph("Simple", false) `+count, `4`)
	expectGM(t, eng, `graph("Simple", true) `+count, `5`) // transient: built from the model now
	expectGM(t, eng, `graph("Simple", snapshot="S1") `+count, `4`)
	expectGM(t, eng, `let G = graph('Simple', true); G `+count, `5`)
	diskQueryError(t, eng, `graph("Simple", snapshot=true) `+count)
}

func TestGraphSnapshotReportsAndPolicies(t *testing.T) {
	eng, _ := graphModelScope(t)
	diskExec(t, eng, `.make graph_snapshot S1 from Simple`)
	tbl := diskQuery(t, eng, `.show graph_snapshot Simple.S1 statistics`)
	if got := tableColumns(tbl); got != "DatabaseName,ModelName,ModelId,Name,SnapshotTime,TotalCpu,MemoryPeak,Duration,NodesCount,EdgesCount,NodesSize,EdgesSize,Details" {
		t.Fatalf("statistics columns %s", got)
	}
	if tbl.Rows[0][8] != int64(7) || !strings.Contains(tbl.Rows[0][12].(string), `"Kind":"AddEdges"`) {
		t.Fatalf("statistics row %v", tbl.Rows[0])
	}
	expectGM(t, eng, `.show graph_snapshots Simple data statistics | project EntityKind, TotalElementCount`, `Nodes | 7`, `Edges | 7`)
	expectGM(t, eng, `.show graph_snapshots * statistics | count`, `1`)

	// A failed build is recorded and listed.
	diskExec(t, eng, ".create-or-alter graph_model Late ```"+`{"Definition": {"Steps": [{"Kind": "AddNodes", "Query": "People", "NodeIdColumn": "nope"}]}}`+"```")
	diskQueryError(t, eng, `.make graph_snapshot F1 from Late`)
	expectGM(t, eng, `.show graph_snapshots Late failures | project Name, FailureKind`, `F1 | Permanent`)
	expectGM(t, eng, `.show operations | summarize count() by State | sort by State asc`, `Completed | 1`, `Failed | 1`)

	// Policies: defaults, alter, validation, delete.
	expectGM(t, eng, `.show graph_model Simple policy snapshot_indexing | project Policy`, `{"IndexingLevel":"Default"}`)
	diskExec(t, eng, ".alter graph_model Simple policy graph_query ```\n{\n  \"DefaultSnapshotQueryBehavior\": \"LatestIndexedSnapshot\"\n}\n```")
	expectGM(t, eng, `.show graph_model * policy graph_query | project EntityName, Policy`,
		`[`+eng.databaseName()+`].[Late] | {"DefaultSnapshotQueryBehavior":"LatestSnapshot"}`,
		`[`+eng.databaseName()+`].[Simple] | {"DefaultSnapshotQueryBehavior":"LatestIndexedSnapshot"}`)
	diskQueryError(t, eng, ".alter graph_model Simple policy snapshot_indexing ```{\"IndexingLevel\": \"Maximum\"}```")
	diskQueryError(t, eng, ".alter graph_model Simple policy nonsense ```{}```")
	diskExec(t, eng, `.delete graph_model Simple policy graph_query`)
	expectGM(t, eng, `.show graph_model Simple policy graph_query | project Policy`, `{"DefaultSnapshotQueryBehavior":"LatestSnapshot"}`)

	// showAll lists every version; details carries the full column set.
	diskExec(t, eng, gmModel)
	expectGM(t, eng, `.show graph_models with (showAll = true) | where Name == "Simple" | count`, `2`)
	if got := tableColumns(diskQuery(t, eng, `.show graph_models details`)); got != "Name,CreationTime,Id,SnapshotsCount,Model,AuthorizedPrincipals,RetentionPolicy" {
		t.Fatalf("details columns %s", got)
	}
	// A definition written with a leading comment, line breaks inside
	// strings and a trailing comma.
	diskExec(t, eng, "// comment\n.create-or-alter graph_model Loose ```\n{\"Definition\": {\"Steps\": [{\"Kind\": \"AddNodes\", \"Query\": \"People\n| project id, name\", \"NodeIdColumn\": \"id\",},]}}\n```")
	expectGM(t, eng, `graph("Loose") | graph-match (n) project n.name | count`, `4`)
}
