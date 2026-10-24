# Research knowledge graphs

<!-- gokql:setup
.set-or-append Claims <| datatable(Id:string, Text:string, Kind:string, Confidence:string)
  ["C1", "The gate is aligned to the midsummer sunrise", "hypothesis", "hypothesis",
   "C2", "The gate is a drain outlet", "hypothesis", "hypothesis",
   "C3", "Gate bearing is 60.1 degrees", "measurement", "tested",
   "C4", "Midsummer sunrise azimuth at the site, c. 2100 BC, is 60.4 degrees", "computation", "tested",
   "C5", "Silt deposits fill the channel behind the gate", "observation", "tested",
   "C6", "The gate's sill is level with the channel floor", "observation", "tested"]

.set-or-append Sources <| datatable(Id:string, Citation:string)
  ["S1", "2016 total-station survey, table 3",
   "S2", "Excavation report 1929, p. 41",
   "S3", "Ephemeris computation, notebook 7"]

.set-or-append Links <| datatable(Src:string, Dst:string, Rel:string)
  ["C1", "C3", "depends-on",
   "C1", "C4", "depends-on",
   "C2", "C5", "depends-on",
   "C2", "C6", "depends-on",
   "C3", "S1", "cites",
   "C4", "S3", "cites",
   "C5", "S2", "cites",
   "C6", "S2", "cites"]
-->

Research produces claims that rest on other claims, measurements and
sources, and it keeps more than one explanation alive until the
evidence decides. A graph holds that structure naturally: claims and
sources are nodes, and "depends on", "cites", "contradicts" and
"supersedes" are edges. gokql stores the nodes and edges as ordinary
append-only tables and queries them as a graph in KQL, GQL or
openCypher, so the same data answers "what supports this?", "what
breaks if this measurement is wrong?" and "which hypothesis has more
independent support?".

This is the [agent memory](agent-memory.md) pattern at research scale,
and it suits a person and an AI assistant working on the same body of
evidence: the assistant can test variants and record what it finds,
while the history of every claim stays intact.

## The data

Two competing hypotheses about a gate, the measurements and
observations each depends on, and the sources those cite:

```kql
Claims
| project Id, Kind, Text
```

```output
Id    Kind         Text
────  ────         ────
C1    hypothesis   The gate is aligned to the midsummer sunrise
C2    hypothesis   The gate is a drain outlet
C3    measurement  Gate bearing is 60.1 degrees
C4    computation  Midsummer sunrise azimuth at the site, c. 2100 BC, is 60.4 degrees
C5    observation  Silt deposits fill the channel behind the gate
C6    observation  The gate's sill is level with the channel floor
(6 rows)
```

## The graph

```kql
.create-or-alter function Evidence() {
    let nodes = union (Claims | project Id, Text, Label = Kind), (Sources | project Id, Text = Citation, Label = "source");
    Links | make-graph Src --> Dst with nodes on Id
}
```

## What does each hypothesis rest on?

Everything a hypothesis depends on, down to its sources:

```kql
Evidence()
| graph-match (h)-[e*1..4]->(s)
    where h.Label == "hypothesis" and s.Label == "source"
    project Hypothesis = h.Id, Source = s.Text
| summarize Sources = make_set(Source) by Hypothesis
| order by Hypothesis asc
```

```output
Hypothesis  Sources
──────────  ───────
C1          ["2016 total-station survey, table 3","Ephemeris computation, notebook 7"]
C2          ["Excavation report 1929, p. 41"]
(2 rows)
```

`C1` rests on a survey and a computation, two independent sources;
`C2` rests on two observations from one excavation report. The same
question in openCypher:

```kql
#crp query_language=opencypher
#crp query_graph_reference=Evidence
MATCH (h:hypothesis)-[*1..4]->(s:source)
RETURN h.Id AS hypothesis, count(DISTINCT s) AS independent_sources
ORDER BY hypothesis
```

```output
hypothesis  independent_sources
──────────  ───────────────────
C1          2
C2          1
(2 rows)
```

## What breaks if a measurement is wrong?

If the bearing in `C3` is revised, every claim upstream of it needs
review:

```kql
Evidence()
| graph-match (affected)-[e*1..4]->(m)
    where m.Id == "C3"
    project Affected = affected.Id, Text = affected.Text, Hops = array_length(e)
```

```output
Affected  Text                                          Hops
────────  ────                                          ────
C1        The gate is aligned to the midsummer sunrise  1
(1 rows)
```

## Recording a contest between hypotheses

A contradiction is an edge too. When a new observation counts against
one hypothesis, append it; nothing is overwritten, and the graph now
carries the conflict:

```kql
.set-or-append Claims <| datatable(Id:string, Text:string, Kind:string, Confidence:string)
  ["C7", "The gate's opening faces away from the channel", "observation", "tested"]
```

```kql
.set-or-append Links <| datatable(Src:string, Dst:string, Rel:string)
  ["C7", "C2", "contradicts",
   "C7", "S1", "cites"]
```

```kql
Evidence()
| graph-match (o)-[e]->(h)
    where e.Rel == "contradicts"
    project Observation = o.Text, Against = h.Text
```

```output
Observation                                     Against
───────────                                     ───────
The gate's opening faces away from the channel  The gate is a drain outlet
(1 rows)
```

## Working with assumption variants

Keeping hypotheses side by side means asking "if we assume X, what
follows?" without deleting the alternative.
[Exploratory reasoning](exploratory-reasoning.md) takes this further:
alternative definitions are listed as data, every combination becomes
a hypothesis tested by a query, and doubt about an assumption
propagates to everything that rests on it. A variant explored and
abandoned remains visible as history, which is the point: the record
of what was tried is part of the research.

## Practical notes

- Keep `Id`s short and stable; cite them in prose and in edges.
- Give every claim a `Kind` and a `Confidence`, and every
  measurement or observation a `cites` edge to a source.
- Corrections are new rows plus a `supersedes` edge, never edits; see
  [agent memory](agent-memory.md#a-later-session-corrects).
- For large literatures, chunk and embed the sources
  ([embeddings](../guide/embeddings-and-ai.md)) and link claims to the
  chunks they were extracted from.

## See also

- [Exploratory reasoning](exploratory-reasoning.md): generating and testing hypothesis variants from alternative definitions
- [Graphs](../guide/graphs.md): graph-match, graph models, GQL and openCypher in full
- [Agent memory](agent-memory.md)
