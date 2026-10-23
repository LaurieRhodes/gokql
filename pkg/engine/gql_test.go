package engine

import (
	"sort"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/gql"
)

// gqlMovies is the graph of ADX's GQL reference examples.
const gqlMovies = `let G = () {
    let Movies = datatable (Title: string, Description: string, Year: int, Label2: dynamic)
    [
        'T1', 'A movie about space', 1995, dynamic(["Movie","Survival","Adventure","Drama","History"]),
        'T2', 'Romantic comedy',     2011, dynamic(["Movie","Comedy","Drama","Romance"]),
        'T3', 'War film',            2020, dynamic(["Movie","Drama","History","War"]),
    ]
    | extend Label = 'Movie';
    let People = datatable (Name: string, Born: int, Label2: dynamic)
    [
        'Tom',   1956, dynamic(["Person","Male","BestActorAward"]),
        'Kevin', 1958, dynamic(["Person","Male"]),
        'Ron',   1954, dynamic(["Person","Male","BestDirectorAward"]),
        'Julia', 1967, dynamic(["Person","Female","BestActressAward"]),
    ]
    | extend Label = 'Person';
    let Actions = datatable(Name: string, Movie: string, Label: string, Label2: dynamic, Role: string)
    [
        'Tom',   'T1', 'ACTED_IN', dynamic(["ACTED_IN"]), 'Role1',
        'Kevin', 'T1', 'ACTED_IN', dynamic(["ACTED_IN"]), 'Role2',
        'Ron',   'T1', 'DIRECTED', dynamic(["DIRECTED"]), 'Director',
        'Tom',   'T2', 'ACTED_IN', dynamic(["ACTED_IN"]), 'Role3',
        'Tom',   'T2', 'DIRECTED', dynamic(["DIRECTED"]), 'Director',
        'Tom',   'T3', 'ACTED_IN', dynamic(["ACTED_IN"]), 'Role4',
    ];
    Actions
    | make-graph Name --> Movie with People on Name, Movies on Title
};
`

// gqlRows runs a GQL / openCypher query over the movies graph and
// renders the result as "c1,c2;..." (rows sorted unless the query
// orders them), preceded by the column names.
func gqlRows(t *testing.T, src string, cypher bool) string {
	t.Helper()
	kql, err := gql.Translate(src, "G()", cypher)
	if err != nil {
		t.Fatalf("translate %q: %v", src, err)
	}
	tbl := queryResult(t, gqlMovies+kql)
	var cols []string
	for _, c := range tbl.Schema.Columns {
		cols = append(cols, c.Name)
	}
	var rows []string
	for i := range tbl.Rows {
		var cs []string
		for c := range tbl.Schema.Columns {
			cs = append(cs, cell(t, tbl, i, c))
		}
		rows = append(rows, strings.Join(cs, ","))
	}
	if !strings.Contains(strings.ToLower(src), "order by") {
		sort.Strings(rows)
	}
	return strings.Join(cols, ",") + ": " + strings.Join(rows, ";")
}

// The examples of ADX's GQL clause reference, with the documented
// results (or, where the documentation's table is inconsistent with
// the query, the GQL-correct one).
func TestGQLReferenceExamples(t *testing.T) {
	cases := []struct{ q, want string }{
		{`match (p:Person)-[:ACTED_IN]->(m:Movie)
		  where (p.Name = 'Tom' or p.Name = 'Ron') and m.Title <> 'T1' and p.Born > 1901
		  return p.Name as name, m.Title as title`, "name,title: Tom,T2;Tom,T3"},
		{`match (p:Person) where not (p.Born < 1960) return p.Name as name`, "name: Julia"},
		{`match (p:Person) where p.Name not contains 'n' return p.Name`, "p.Name: Julia;Tom"},
		{`match (p:Person {Name: 'Tom'}) return p.Name as name, p.Born as born`, "name,born: Tom,1956"},
		{`match (p:Person)-[:ACTED_IN {Role: 'Role2'}]->(m:Movie) return p.Name as name, m.Title as title`, "name,title: Kevin,T1"},
		{`match (p:Person {Name: 'Kevin'})-[:ACTED_IN {Role: 'Role2'}]->(m:Movie {Title: 'T1'})
		  return p.Name as name, m.Title as title`, "name,title: Kevin,T1"},
		{`match (p:Person)-[e where 'DIRECTED' in labels(e)]->(m:Movie where m.Title <> 'T1')
		  return p.Name as name, m.Title as title`, "name,title: Tom,T2"},
		{`match (p:Person) where p.Name in ['Tom', 'Kevin'] return p.Name as name`, "name: Kevin;Tom"},
		{`match (p:Person)-[:ACTED_IN]->(m:Movie) return distinct p.Name as name`, "name: Kevin;Tom"},
		{`match (p:Person)-[:ACTED_IN]->(m:Movie)
		  return m.Title as Title, count(p.Name) as Actors, collect_list(distinct p.Name) as Names`,
			`Title,Actors,Names: T1,2,["Tom","Kevin"];T2,1,["Tom"];T3,1,["Tom"]`},
		{`match (p:Person) return p.Name as name, p.Born as born order by p.Born desc`,
			"name,born: Julia,1967;Kevin,1958;Tom,1956;Ron,1954"},
		{`match (p:Person)-[:ACTED_IN]->(m:Movie) return p.Name as Name, m.Title as Title order by Name desc, Title asc`,
			"Name,Title: Tom,T1;Tom,T2;Tom,T3;Kevin,T1"},
		{`match (p:Person) return p.Name as name order by p.Born asc limit 2`, "name: Ron;Tom"},
		{`match (p:Person) order by p.Born asc limit 2 return p.Name as name`, "name: Ron;Tom"},
		{`match (m:Movie) return m.Title as title order by m.Title offset 1 limit 1`, "title: T2"},
		{`match () limit 1 for c in ['a', 'b'] with offset idx return c, idx`, "c,idx: a,0;b,1"},
		{`match () limit 1 for c in ['a', 'b'] with ordinality idx return c, idx`, "c,idx: a,1;b,2"},
		{`match (p:Person) where p.Name starts with 'K' let BirthYear = p.Born return p.Name as Name, BirthYear`,
			"Name,BirthYear: Kevin,1958"},
		{`match (m:Movie) let threshold = 2000 filter m.Year >= threshold return m.Title as title`, "title: T2;T3"},
		{`match (p:Person) let a = 1 let b = a + 3 return p.Name as name, a, b`,
			"name,a,b: Julia,1,4;Kevin,1,4;Ron,1,4;Tom,1,4"},
		{"match (p:Person)-[:ACTED_IN]->(m:Movie) return p.Name as Actor, m.Title as Title, m.Year as `Year` " +
			"next filter `Year` = 2020 return Actor, Title", "Actor,Title: Tom,T3"},
		{`match (p:Person {Name: 'Tom'}) return p next match (p)-[:ACTED_IN]->(m:Movie) return m.Title`,
			"m.Title: T1;T2;T3"},
		{`match (p:Person)-[:ACTED_IN]->(m:Movie) return p.Name as Actor next return count(*) as Rows`, "Rows: 4"},
		{`match (p:Person) return p.Name as name union match (m:Movie) return m.Title as name`,
			"name: Julia;Kevin;Ron;T1;T2;T3;Tom"},
		{`MATCH (p:Person)-[:ACTED_IN]->(m:Movie)
		  WHERE m.Year >= 1990 AND p.Name STARTS WITH 'T'
		  RETURN p.Name AS actor, COUNT(m) AS movieCount
		  NEXT FILTER movieCount > 1 RETURN actor, movieCount ORDER BY movieCount DESC LIMIT 1`,
			"actor,movieCount: Tom,3"},
		{`match p = (n0:Person)-[:DIRECTED]->(:Movie {Title: 'T1'}) return * next return path_length(p) as Hops`,
			"Hops: 1"},
	}
	for _, c := range cases {
		if got := gqlRows(t, c.q, false); got != c.want {
			t.Errorf("%s\n got  %s\n want %s", c.q, got, c.want)
		}
	}
}

func TestGQLPatterns(t *testing.T) {
	cases := []struct {
		q, want string
		cypher  bool
	}{
		{q: `match (n:Person|Movie) return count(*) as c`, want: "c: 7"},
		{q: `match (n:!Movie) return n.Name as name`, want: "name: Julia;Kevin;Ron;Tom"},
		{q: `match (n:%) return count(*) as c`, want: "c: 7"},
		{q: `match (a:Person)-[:ACTED_IN]->(m:Movie)<-[:DIRECTED]-(d:Person) return a.Name as a, d.Name as d, m.Title as t`,
			want: "a,d,t: Kevin,Ron,T1;Tom,Ron,T1;Tom,Tom,T2"},
		{q: `match (a:Person)-[:ACTED_IN]->(m:Movie), (d:Person)-[:DIRECTED]->(m) return count(*) as c`, want: "c: 3"},
		{q: `match p = (a:Person {Name: 'Kevin'})-[e]-{1,3}(b) return path_length(p) as len, count(*) as c`,
			want: "len,c: 1,1;2,2;3,3"},
		{q: `match (a:Person {Name: 'Kevin'})-[e:ACTED_IN]-{1,3}(b:Person) return distinct b.Name as n`, want: "n: Tom"}, // trails: no edge twice
		{q: `match all shortest p = (a:Person {Name: 'Kevin'})-[e]-{1,5}(b:Person) return b.Name as n, path_length(p) as len`,
			want: "n,len: Ron,2;Tom,2"},
		{q: `match (p:Person) optional match (p)-[:DIRECTED]->(m:Movie) return p.Name as name, m.Title as title`,
			want: "name,title: Julia,;Kevin,;Ron,T1;Tom,T2"},
		{q: `match (p:Person) return p.Name as name, case when p.Born < 1956 then 'old' when p.Born < 1960 then 'mid' else 'young' end as age`,
			want: "name,age: Julia,young;Kevin,mid;Ron,old;Tom,mid"},
		{q: `match (p:Person {Name: 'Tom'}) return cast(p.Born as string) as b, char_length(p.Name) as len, left(p.Name, 2) as l, lower(p.Name) || '!' as x`,
			want: "b,len,l,x: 1956,3,To,tom!"},
		{q: `match (p:Person)-[e]->(m) return labels(e) as l, count(*) as c`, want: `l,c: ["ACTED_IN"],4;["DIRECTED"],2`},
		// openCypher
		{q: `MATCH (p:Person)-[r:ACTED_IN]->(m:Movie) WHERE r.Role = 'Role2' RETURN p.Name AS n, type(r) AS t`,
			want: "n,t: Kevin,ACTED_IN", cypher: true},
		{q: `MATCH (p:Person) WITH p.Name AS name, p.Born AS born WHERE born > 1955 RETURN name ORDER BY name SKIP 1 LIMIT 2`,
			want: "name: Kevin;Tom", cypher: true},
		{q: `MATCH (p:Person)-[:ACTED_IN]->(m:Movie) RETURN p.Name AS name, count(m) AS c`,
			want: "name,c: Kevin,1;Tom,3", cypher: true},
		{q: `MATCH (a:Person {Name: 'Kevin'})-[*1..3]-(b:Person) RETURN DISTINCT b.Name AS n`,
			want: "n: Ron;Tom", cypher: true},
		{q: `UNWIND [1, 2, 3] AS x MATCH (p:Person) RETURN count(*) AS c`, want: "c: 12", cypher: true},
		{q: `MATCH (a:Person)-->(m:Movie)<--(b:Person) WHERE a.Name < b.Name RETURN count(*) AS c`, want: "c: 3", cypher: true},
		{q: `MATCH (p:Person) RETURN toUpper(p.Name) AS u, size(p.Name) AS s ORDER BY u LIMIT 1`,
			want: "u,s: JULIA,5", cypher: true},
	}
	for _, c := range cases {
		if got := gqlRows(t, c.q, c.cypher); got != c.want {
			t.Errorf("%s\n got  %s\n want %s", c.q, got, c.want)
		}
	}
}

func TestGQLErrors(t *testing.T) {
	for _, q := range []string{
		`return 1`,
		`match (p:Person)`,
		`match (p)-[e]~(q) return p`,
		`match (p:Person) return [x in [1,2] | x] as l`,
		`match (p:Person) where count(*) > 1 return p.Name`,
		`match (p:Person) return $param as x`,
	} {
		if _, err := gql.Translate(q, "G()", false); err == nil {
			t.Errorf("%s: expected a translation error", q)
		}
	}
}

// A graph model as the graph reference: labels come from the model.
func TestGQLGraphModel(t *testing.T) {
	eng, _ := graphModelScope(t)
	expectGM(t, eng, "#crp query_language=gql\n#crp query_graph_reference=graph(\"Simple\")\n"+
		"match (p:Person)-[:works_at]->(c:Company {name: 'TechCorp'}) return p.name as n order by n",
		"Alice", "Bob", "Emma")
	expectGM(t, eng, "#crp query_language=opencypher\n#crp query_graph_reference=graph(\"Simple\")\n"+
		"MATCH (a:Manager)-[:knows]->(b) RETURN a.name AS a, labels(b) AS l",
		`Bob | ["Person","Manager"]`, `Dan | []`)
}
