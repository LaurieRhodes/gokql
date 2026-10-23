# Graphs

<!-- gokql:setup
.set-or-append Statements <| datatable(Id:string, Text:string, Label:string)
  ["s1", "The gate faces the midsummer sunrise", "Claim",
   "s2", "The gate is a drain outlet", "Claim",
   "r1", "1929 survey: gate bearing 59.5 degrees", "Source",
   "r2", "2016 survey: gate bearing 60.1 degrees", "Source",
   "r3", "Excavation report: silt in the channel", "Source"]

.set-or-append Links <| datatable(Src:string, Dst:string, Rel:string)
  ["s1", "r1", "supported_by",
   "s1", "r2", "supported_by",
   "s2", "r3", "supported_by",
   "s2", "r2", "contradicted_by"]
-->

gokql has ADX's graph semantics: build a graph from tables with
`make-graph`, match patterns with `graph-match`, and keep named graph
models that are snapshotted for reuse. The same graphs can be queried
in GQL or openCypher, which gokql translates into `graph-match`.

The examples use a small evidence graph: statements (claims and
sources) and the links between them.

```kql
Statements
| project Id, Label, Text
```

```output
Id    Label   Text
────  ─────   ────
s1    Claim   The gate faces the midsummer sunrise
s2    Claim   The gate is a drain outlet
r1    Source  1929 survey: gate bearing 59.5 degrees
r2    Source  2016 survey: gate bearing 60.1 degrees
r3    Source  Excavation report: silt in the channel
(5 rows)
```

## Build and match

`make-graph` turns an edge table into a graph, with an optional node
table. `graph-match` finds a pattern and projects what it needs:

```kql
Links
| make-graph Src --> Dst with Statements on Id
| graph-match (claim)-[link]->(source)
    where claim.Label == "Claim" and link.Rel == "supported_by"
    project Claim = claim.Text, Source = source.Text
| order by Claim asc, Source asc
```

```output
Claim                                 Source
─────                                 ──────
The gate faces the midsummer sunrise  1929 survey: gate bearing 59.5 degrees
The gate faces the midsummer sunrise  2016 survey: gate bearing 60.1 degrees
The gate is a drain outlet            Excavation report: silt in the channel
(3 rows)
```

Patterns can be variable length (`-[e*1..3]->`), named and constrained
with `all()`, `any()`, `map()` and `inner_nodes()`;
`graph-shortest-paths` finds shortest paths, `graph-mark-components`
labels connected components, and `graph-to-table` turns a graph back
into node or edge tables.

A source that both supports one claim and contradicts another is a
point of contention:

```kql
Links
| make-graph Src --> Dst with Statements on Id
| graph-match (a)-[e1]->(s)<-[e2]-(b)
    where e1.Rel == "supported_by" and e2.Rel == "contradicted_by"
    project Source = s.Text, Supports = a.Text, Contradicts = b.Text
```

```output
Source                                  Supports                              Contradicts
──────                                  ────────                              ───────────
2016 survey: gate bearing 60.1 degrees  The gate faces the midsummer sunrise  The gate is a drain outlet
(1 rows)
```

## Stored graphs

A stored function that returns a graph can be matched directly, so the
graph's definition lives in one place:

```kql
.create-or-alter function Evidence() {
    Links | make-graph Src --> Dst with Statements on Id
}
```

```kql
Evidence()
| graph-match (c)-[e]->(s)
    project Claim = c.Id, Rel = e.Rel, Source = s.Id
| summarize Links = count() by Rel
| order by Rel asc
```

```output
Rel              Links
────             ─────
contradicted_by  1
supported_by     3
(2 rows)
```

Graph models are ADX's persistent form: a JSON definition of how nodes
and edges are drawn from tables, versioned on each change, and
snapshots that freeze a model's result:

````kql
.create-or-alter graph_model EvidenceModel ```
{
  "Schema": {
    "Nodes": {"Claim": {"Text": "string"}, "Source": {"Text": "string"}},
    "Edges": {"supported_by": {}, "contradicted_by": {}}
  },
  "Definition": {
    "Steps": [
      {"Kind": "AddNodes", "Query": "Statements", "NodeIdColumn": "Id", "LabelsColumn": "Label"},
      {"Kind": "AddEdges", "Query": "Links", "SourceColumn": "Src", "TargetColumn": "Dst", "LabelsColumn": "Rel"}
    ]
  }
}
```
````

```kql
.make graph_snapshot Now from EvidenceModel
```

```kql
graph("EvidenceModel")
| graph-match (c)-[e]->(s)
    where labels(c) has "Claim" and labels(e) has "contradicted_by"
    project Claim = c.Text, Source = s.Text
```

```output
Claim                       Source
─────                       ──────
The gate is a drain outlet  2016 survey: gate bearing 60.1 degrees
(1 rows)
```

In KQL's `graph-match`, labels are tested with `labels()`; the
`(c:Claim)` form belongs to GQL and openCypher, below.
`graph("Model")` uses the latest snapshot and `graph("Model", "Name")`
a named one. `.show graph_models`, `.show graph_snapshots M` and the
`.drop` forms manage them ([commands](../reference/commands.md)).

## GQL and openCypher

Set `query_language` to `gql` or `opencypher` and name the graph with
`query_graph_reference`, as client request properties at the top of
the query:

```kql
#crp query_language=opencypher
#crp query_graph_reference=Evidence
MATCH (c:Claim)-[r]->(s:Source)
RETURN c.Text AS claim, r.Rel AS rel, s.Text AS source
ORDER BY claim, source
```

```output
claim                                 rel              source
─────                                 ────             ──────
The gate faces the midsummer sunrise  supported_by     1929 survey: gate bearing 59.5 degrees
The gate faces the midsummer sunrise  supported_by     2016 survey: gate bearing 60.1 degrees
The gate is a drain outlet            contradicted_by  2016 survey: gate bearing 60.1 degrees
The gate is a drain outlet            supported_by     Excavation report: silt in the channel
(4 rows)
```

```kql
#crp query_language=gql
#crp query_graph_reference=graph("EvidenceModel")
MATCH (c:Claim)-[:supported_by]->(s:Source)
RETURN c.Text AS claim, count(s) AS sources
ORDER BY claim
```

```output
claim                                 sources
─────                                 ───────
The gate faces the midsummer sunrise  2
The gate is a drain outlet            1
(2 rows)
```

`query_graph_reference` is any KQL expression that yields a graph: a
stored function (`Evidence` or `Evidence()`), or `graph("Model")` /
`graph("Model", "Snapshot")`. Over HTTP, the same two settings go in
the request's `properties.Options`. Labels come from a `Label` property
(a string or an array) and from a graph model's declared labels.

The translation follows the GQL standard (ISO/IEC 39075) in the
dialect ADX documents, plus openCypher. Where the Kusto emulator
departs from its own documentation (it rejects `LET`, `FOR`, `NEXT`
and `FILTER`, and gets `OPTIONAL MATCH` and `CASE` wrong), gokql
follows the documentation and the standard.

### Supported

**Patterns.** Nodes `(v:Label {prop: value} WHERE cond)`; edges in
either direction or both, full (`-[e]->`) or abbreviated (`->`, `-->`);
label expressions `A|B`, `A&B`, `!A`, `%` and openCypher `:A:B`;
quantifiers `{m,n}`, `*`, `+`, `?` and openCypher `[*m..n]` (unbounded
ones stop at 64 hops); several patterns and several `MATCH` clauses
(shared variables join, disconnected patterns cross-join); path
variables `p = (...)`; `ANY SHORTEST`, `ALL SHORTEST`,
`shortestPath()`, `allShortestPaths()`; match modes `WALK`, `TRAIL`
(default) and `ACYCLIC`/`SIMPLE`.

**Clauses.** `MATCH`, `OPTIONAL MATCH` (a real left outer join),
`WHERE`, `FILTER`, `LET`, `FOR ... IN ... [WITH OFFSET|ORDINALITY]`,
`UNWIND`, `RETURN [DISTINCT]` with aggregates grouped by the other
items, `ORDER BY ... [NULLS FIRST|LAST]`, `OFFSET`/`SKIP`, `LIMIT`,
`NEXT` and openCypher `WITH`, `UNION [ALL]`.

**openCypher expressions.** Pattern predicates (`WHERE (p)-[:R]->()`,
`exists(...)`, `EXISTS { ... }`), `COUNT { ... }`, `size(pattern)`,
pattern and list comprehensions, `any`/`all`/`none`/`single`,
`reduce(acc = init, x IN list | acc + expr)`, map projection
`n {.a, .*}`, query parameters `$name` (from the request's
`Parameters`), `startNode()`/`endNode()`.

**Expressions and functions.** The usual operators including `||`,
`STARTS WITH`, `CONTAINS`, `=~`, `IS NULL`, `CASE`, `CAST`; entity
functions (`labels`, `type`, `id`, `properties`, `keys`, `nodes`,
`relationships`, `length`); string, numeric, date and list functions;
aggregates `count`, `sum`, `avg`, `min`, `max`, `collect`, `stdev`,
`percentileCont`, `percentileDisc`. Any other function name is passed
to KQL unchanged.

A property an entity lacks reads as null. A node returned on its own
is a bag of its properties; a path is an array of alternating nodes
and edges.

### Not supported

Undirected `~` edges; quantified parenthesised path patterns
`((a)-->(b)){2}`; `CALL { ... }`; nested pattern or list expressions
inside a comprehension; `reduce` other than `acc + expr`; graph
modification (`CREATE`, `MERGE`, `SET`, `DELETE`; ADX graphs are
read-only too); `=` between entities (compare `id()` instead).

## See also

- [Research knowledge graphs](../use-cases/research-graphs.md)
- [Operators reference](../reference/operators.md#graph-operators)
