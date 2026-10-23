package engine

import (
	"os"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// BenchmarkQueryShapes profiles a spread of query shapes against a
// synthetic database named by GOKQL_BENCH_DB (skips when unset). The
// table is Events(Id: long, Host: string 5k distinct, User: string 50k
// distinct, Sev: int 1-5, Score: real, Msg: string near-unique).
func BenchmarkQueryShapes(b *testing.B) {
	dir := os.Getenv("GOKQL_BENCH_DB")
	if dir == "" {
		b.Skip("GOKQL_BENCH_DB not set")
	}
	cat, err := catalog.Open(dir)
	if err != nil {
		b.Skipf("benchmark db not present: %v", err)
	}
	shapes := []struct{ name, q string }{
		{"GroupByLowCard", `Events | summarize S = sum(Score), M = max(Id) by Host`},
		{"FilterGroupBy", `Events | where Sev == 5 | summarize S = sum(Score) by Host`},
		{"StringEq", `Events | where Host == "host-0042" | count`},
		{"GroupByMidCard", `Events | summarize count() by User`},
		{"DcountString", `Events | summarize dcount(User) by Sev`},
		{"HasHighCard", `Events | where Msg has "12345" | count`},
		{"NumericOnly", `Events | where Score > 99.5 | summarize max(Id)`},
		{"ProjectStrings", `Events | where Sev == 1 | project Host, User, Msg | take 500000 | count`},
	}
	for _, s := range shapes {
		stmt, err := parser.Parse(s.q)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(s.name, func(b *testing.B) {
			eng := New(cat)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := eng.Execute(stmt); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkGraphShapes profiles graph construction and matching
// against GOKQL_GRAPH_BENCH_DB (skips when unset): Nodes(id long,
// name string, Label string, score long), Edges(src, dst long, Label
// string, w long) and a stored function GB() = Edges | make-graph src
// --> dst with Nodes on id.
func BenchmarkGraphShapes(b *testing.B) {
	dir := os.Getenv("GOKQL_GRAPH_BENCH_DB")
	if dir == "" {
		b.Skip("GOKQL_GRAPH_BENCH_DB not set")
	}
	cat, err := catalog.Open(dir)
	if err != nil {
		b.Skipf("benchmark db not present: %v", err)
	}
	shapes := []struct{ name, q string }{
		{"MakeGraph", `GB | graph-to-table nodes | count`},
		{"OneHop", `GB | graph-match (a)-[e]->(b) where a.score == 5 project b.id | count`},
		{"VarLen", `GB | graph-match (a)-[e*1..3]->(b) where a.id == 17 project b.id | count`},
		{"VarLenMany", `GB | graph-match (a)-[e*1..3]->(b) where a.score == 5 project b.id | count`},
		{"TwoHop", `GB | graph-match (a)-[e]->(b)-[f]->(c) where a.score == 5 and c.Label == "Hub" project c.id | count`},
		{"GQL", "#crp query_language=gql\n#crp query_graph_reference=GB\nmatch (a:Hub)-[:FOLLOWS]->(b:User) where a.score = 5 return count(*) as c"},
	}
	for _, s := range shapes {
		stmt, err := parser.Parse(s.q)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(s.name, func(b *testing.B) {
			eng := New(cat)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := eng.Execute(stmt); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkAdhoc profiles one query, $GOKQL_ADHOC_Q, against the
// database $GOKQL_ADHOC_DB (skips when unset).
func BenchmarkAdhoc(b *testing.B) {
	dir, q := os.Getenv("GOKQL_ADHOC_DB"), os.Getenv("GOKQL_ADHOC_Q")
	if dir == "" || q == "" {
		b.Skip("GOKQL_ADHOC_DB / GOKQL_ADHOC_Q not set")
	}
	cat, err := catalog.Open(dir)
	if err != nil {
		b.Skipf("db not present: %v", err)
	}
	stmt, err := parser.Parse(q)
	if err != nil {
		b.Fatal(err)
	}
	eng := New(cat)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := eng.Execute(stmt); err != nil {
			b.Fatal(err)
		}
	}
}
