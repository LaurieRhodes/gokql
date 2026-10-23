# GQL and openCypher

gokql runs GQL (ISO/IEC 39075, in the dialect Azure Data Explorer
documents) and openCypher queries over its graphs. `pkg/gql` translates
each query into a KQL `graph-match` pipeline, which the ordinary engine
then runs. Every graph feature therefore lives in one place, and the
translation can be inspected (`GQL_SHOW='...' go test ./pkg/gql -run TestShow -v`).

The goal is a usable tool, not bit-exact ADX parity. Where ADX's
emulator and the GQL standard disagree, gokql follows the standard. For
example, OPTIONAL MATCH is a real left outer join, CASE evaluates every
branch, and OFFSET skips rows.

## Running a query

Choose the language and the graph with client request properties:

```
#crp query_language=gql
#crp query_graph_reference=G
MATCH (p:Person)-[:ACTED_IN]->(m:Movie)
WHERE m.Year >= 1990
RETURN p.Name AS actor, count(m) AS movies
```

- `query_language` is `gql` or `opencypher`.
- `query_graph_reference` is any KQL expression that yields a graph:
  - a stored function such as `G`, either with or without `()`;
  - `graph("Model")` or `graph("Model", "Snapshot")`.
- Over REST (`gokql -serve`), the same two settings go in the request's
  `properties.Options`, as Kusto clients send them.

Labels come from a property column named `Label`, which may hold a
string or a dynamic array. Graph models also use their declared labels.

## Supported

**Patterns**
- Node `(v:Label {prop: value} WHERE cond)`, with every part optional.
- Edges, each either full (with `[...]`) or abbreviated:
  - forward: `-[e]->` or `->`;
  - backward: `<-[e]-` or `<-`;
  - either direction: `-[e]-` or `-`;
  - openCypher shorthand: `-->`, `<--`, `--`.
- Label expressions: `A|B`, `A&B`, `!A`, `%` (any label), parentheses, and openCypher `:A:B`.
- Quantifiers:
  - GQL: `{m,n}`, `{m}`, `{,n}`, `*`, `+`, `?`;
  - openCypher: `[*]`, `[*n]`, `[*m..n]`, `[*..n]`.
  - Unbounded quantifiers stop at 64 hops.
  - A label or property filter on a variable-length edge applies to every edge in it.
- Several comma-separated patterns, and several MATCH clauses:
  - shared variables join;
  - disconnected patterns form a cross product.
- Path variables `p = (...)`.
- `ANY SHORTEST` and `ALL SHORTEST`, plus openCypher `shortestPath(...)` and `allShortestPaths(...)`.
- Match modes: `WALK`, `TRAIL` (the default) and `ACYCLIC`/`SIMPLE`.

**Clauses**
- `MATCH`, `OPTIONAL MATCH`, `WHERE`, `FILTER`, `LET`.
- `FOR x IN list [WITH OFFSET|ORDINALITY i]`, and openCypher `UNWIND list AS x`.
- `RETURN [DISTINCT] items | *`, with aggregates grouped by the other items.
- `ORDER BY ... [ASC|DESC] [NULLS FIRST|LAST]`, `OFFSET`/`SKIP`, `LIMIT`. These can follow RETURN or stand before it.
- `NEXT`, and openCypher `WITH`. A node returned before NEXT can be matched again after it.
- `UNION [ALL]`.

**Expressions**
- Operators:
  - `= <> < <= > >=`, `AND OR XOR NOT`, `+ - * / % ^`, and `||` for string concatenation;
  - `[NOT] IN`, `[NOT] STARTS WITH`, `ENDS WITH`, `CONTAINS`;
  - `IS [NOT] NULL`, and `=~` (a regex that must match the whole string).
- `CASE` (simple and searched), and `CAST(x AS type)`.
- Lists `[...]`, maps `{k: v}`, property access `x.p`, indexing `x[i]`, and backtick identifiers.

**Functions**
- Entity and path: `labels`, `type`, `element_id`/`id`, `properties`, `keys`, `nodes`, `edges`/`relationships`, `path_length`/`length`.
- String:
  - `upper`/`toUpper`, `lower`/`toLower`, `trim`/`ltrim`/`rtrim`, `left`, `right`, `substring`;
  - `char_length`, `size`, `string_join`, `split`, `replace`, `reverse`;
  - `toString`, `toInteger`, `toFloat`, `toBoolean`.
- Numeric: `abs`, `ceil`, `floor`, `round`, `sqrt`, `exp`, `ln`, `log10`, `sign`, the trigonometric functions, `mod`, `power`, `rand`, `e`.
- Date and time: `zoned_datetime`, `duration({days: .., hours: ..})`, `duration_between`, `now`.
- List and JSON: `head`, `last`, `tail`, `range`, `coalesce`, `parse_json_string`, `to_json_string`.
- Aggregates: `count(*)`, `count([DISTINCT] x)`, `sum`, `avg`, `min`, `max`, `collect_list`/`collect` (DISTINCT gives a set), `stddev`/`stdev`, `stddev_pop`.
- Any other function name passes through to KQL unchanged, so KQL functions can be used directly.

Values:
- A node or edge variable returned on its own is a bag of its properties.
- A path is an array of nodes and edges, alternating.

## Not supported

- Undirected `~` edges.
- Quantified parenthesized path patterns `((a)-[]->(b)){2}`.
- List comprehensions.
- `EXISTS`, query parameters (`$x`), and graph modification.
- `=`/`<>` between entities. Compare `element_id()` instead.
