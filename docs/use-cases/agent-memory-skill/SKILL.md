---
name: gokql-memory
description: Keep tasks, findings, decisions and their provenance across sessions in a gokql memory scope (a directory queried with KQL). Use when asked to remember project state, track open work, record a finding, decision or correction with its source, or recall what is known from an existing scope.
---

# gokql memory

The memory is one gokql database: a directory, queried and written
with the `gokql` binary. Always pass `-db` with an absolute path (a
quoted `~` is not expanded):

    gokql -db /abs/path/to/memory -mode jsonl -e '<one statement>'

For writes, and anything with quotes or several lines, send the
statements on stdin with a quoted heredoc, separated by blank lines:

    gokql -db /abs/path/to/memory -mode jsonl <<'EOF'
    <statement>

    <statement>
    EOF

Inside KQL, write strings in double quotes; an apostrophe needs no
escaping there ("Woolley's bearing"). Use `-readonly` for anything that
only reads. Errors arrive as one line on stderr with exit status 1:
read the message and correct the statement rather than retrying it
unchanged. Statements after a failing one still run, so check the
output of multi-statement writes.

## Rules

1. **Never update in place, and never delete.** A changed record is a
   new row with the same `Id`; mistakes are fixed by appending, not by
   `.drop` or `.delete`. History is kept; current state is computed.
2. **Read current state through the functions** below rather than
   writing your own "latest row" queries. They are the shared
   definition of "current". (Querying the base tables for history is
   fine.)
3. **Write in batches**: one `.set-or-append ... <| datatable(...)[...]`
   per batch. Different `Id`s in one command are fine; two rows with
   the *same* `Id` in one command are not (they would share one
   timestamp).
4. **Every finding has a `Source`** (file and page, URL, or task Id)
   unless its `Kind` is `hypothesis`.
5. **A correction is three writes**: a new finding; the old finding
   written again with `Lifecycle = "corrected"`; and a `supersedes`
   edge from new to old whose `Basis` says why. `Kind = "correction"`
   alone changes nothing. Then run `DependsOn("<old id>")` and review
   each dependent.
6. **`SessionId`**: choose one at the start of the session, short and
   unique, such as `s` plus the date and a letter (`s1024a`), and put it
   on every row you write. **Ids**: `<prefix>-<SessionId>-<n>`, such as
   `F-s1024a-3`, are unique across sessions and agents without
   querying.
7. Do not supply `_TimeReceived`; the engine stamps it with the time of
   the write.

## First use: create the scope

Run once per database (as one heredoc). `.create-merge` and
`.create-or-alter` make these safe to run again:

```kql
.create-merge table Tasks (Id: string, Title: string, Status: string, Priority: long, Project: string, Note: string, SessionId: string)
```

```kql
.create-merge table Findings (Id: string, Kind: string, Claim: string, Confidence: string, Lifecycle: string, Project: string, Source: string, SessionId: string)
```

```kql
.create-merge table Edges (EdgeId: string, Src: string, Dst: string, Rel: string, Basis: string, Status: string, SessionId: string)
```

```kql
.create-or-alter function with (folder = "memory", docstring = "Latest row per task") LatestTasks() {
    Tasks | summarize arg_max(_TimeReceived, *) by Id
}
```

```kql
.create-or-alter function with (folder = "memory", docstring = "Latest row per finding") LatestFindings() {
    Findings | summarize arg_max(_TimeReceived, *) by Id
}
```

```kql
.create-or-alter function with (folder = "memory", docstring = "Asserted edges, latest row per EdgeId") LiveEdges() {
    Edges | summarize arg_max(_TimeReceived, *) by EdgeId | where Status == "asserted"
}
```

```kql
.create-or-alter function with (folder = "memory", docstring = "Unfinished tasks, highest priority first") OpenTasks() {
    LatestTasks() | where Status in ("open", "in-progress", "blocked") | order by Priority asc, Id asc
}
```

```kql
.create-or-alter function with (folder = "memory", docstring = "Active findings not superseded or rejected") CurrentFindings() {
    LatestFindings()
    | where Lifecycle == "active"
    | join kind=leftanti (LiveEdges() | where Rel in ("supersedes", "chosen-over") | project Id = Dst) on Id
}
```

```kql
.create-or-alter function with (folder = "memory", docstring = "Findings that depend, directly or through others, on the given one") DependsOn(id: string) {
    let nodes = LatestFindings() | project Id, Claim;
    LiveEdges()
    | where Rel == "depends-on"
    | make-graph Src --> Dst with nodes on Id
    | graph-match (dependent)-[e*1..8]->(target)
        where target.Id == id
        project Id = dependent.Id, Claim = dependent.Claim, Hops = array_length(e)
}
```

## Vocabulary

| Column | Values |
|---|---|
| `Tasks.Status` | `open`, `in-progress`, `blocked`, `done`, `abandoned` |
| `Tasks.Priority` | 1 (highest) to 5 |
| `Findings.Kind` | `finding`, `decision`, `constraint`, `hypothesis`, `correction`, `citation` |
| `Findings.Confidence` | `hypothesis`, `tested`, `locked` |
| `Findings.Lifecycle` | `active`, `corrected`, `abandoned` |
| `Edges.Rel` | `supersedes` (Src replaces Dst), `chosen-over` (Src preferred to Dst), `depends-on` (Src assumes Dst), `about` (finding about a task) |
| `Edges.Status` | `asserted`, `retracted` |

Confidence and Lifecycle are independent: a `locked` finding can later
become `corrected`. Set `locked` only for what the user has confirmed
or a decision they made; do not lock your own conclusions.

## At the start of a session

```kql
CurrentFindings()
| where Confidence == "locked"
| project Id, Claim, Source
```

```kql
OpenTasks()
| project Id, Title, Status, Priority
```

```kql
.show functions
| project Name, Folder, DocString
```

Locked findings must not be contradicted without an explicit
correction. Read the open tasks before starting new work. For more
context, `CurrentFindings()` without the filter lists everything
currently believed.

## Writing

```kql
.set-or-append Tasks <| datatable(Id:string, Title:string, Status:string, Priority:long, Project:string, Note:string, SessionId:string)
  ["T-s1024a-1", "Find the source of the 1929 bearing", "open", 1, "demo", "", "s1024a"]
```

```kql
.set-or-append Findings <| datatable(Id:string, Kind:string, Claim:string, Confidence:string, Lifecycle:string, Project:string, Source:string, SessionId:string)
  ["F-s1024a-1", "finding", "The 1929 bearing comes from a compass traverse", "tested", "active", "demo", "report-1929.pdf p4", "s1024a"]
```

```kql
.set-or-append Findings <| datatable(Id:string, Kind:string, Claim:string, Confidence:string, Lifecycle:string, Project:string, Source:string, SessionId:string)
  ["F-s1024a-2", "hypothesis", "The gate was laid out on the midsummer sunrise", "hypothesis", "active", "demo", "", "s1024a"]
```

```kql
.set-or-append Edges <| datatable(EdgeId:string, Src:string, Dst:string, Rel:string, Basis:string, Status:string, SessionId:string)
  ["E-s1024a-1", "F-s1024a-1", "T-s1024a-1", "about", "", "asserted", "s1024a",
   "E-s1024a-2", "F-s1024a-2", "F-s1024a-1", "depends-on", "the sunrise test uses the bearing", "asserted", "s1024a"]
```

```kql
.set-or-append Tasks <| datatable(Id:string, Title:string, Status:string, Priority:long, Project:string, Note:string, SessionId:string)
  ["T-s1024a-1", "Find the source of the 1929 bearing", "done", 1, "demo", "answered by F-s1024a-1", "s1024a"]
```

```kql
DependsOn("F-s1024a-1")
```

`DependsOn` returns no rows when nothing depends on the finding.

Retract an edge by writing its `EdgeId` again with `Status =
"retracted"`. Abandon a finding by writing it again with `Lifecycle =
"abandoned"`.

## Recall

Exact: `CurrentFindings() | where Claim has "bearing"`. By meaning,
when a `FindingsEmbeddings` table exists (`.embed-into
FindingsEmbeddings <| CurrentFindings() | project Id, Text = Claim,
Project`):

```kql no-run
let q = embed_text("<question>");
FindingsEmbeddings
| where Project == "<project>"
| extend Score = series_cosine_similarity(Embedding, q)
| top 5 by Score desc
| join kind=inner (CurrentFindings()) on Id
| project Score, Id, Claim, Source
```

## Before ending a session

- Every task you worked on has a current status.
- Every finding, decision and correction that came from a task has an
  `about` edge to it.
- Every `supersedes` edge's `Dst` has `Lifecycle == "corrected"`, and
  `DependsOn()` was reviewed for it.
- If `.show tables` shows a table with many extents, run
  `.compact table <T>` (old files are kept until `.gc`).
