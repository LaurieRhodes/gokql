---
name: gokql-memory
description: Keep tasks, findings, decisions and their provenance across sessions in a gokql memory scope (a directory queried with KQL). Use when asked to remember project state, track open work, record a finding, decision or correction with its source, or recall what is known from an existing scope.
---

# gokql memory

A scope is one directory, queried and written with the `gokql` binary:

    gokql -db <SCOPE> -mode jsonl -e '<KQL>'

Replace `<SCOPE>` with the project's scope directory (for example
`~/memory/<project>`). Use `-readonly` for anything that only reads.
Errors arrive as one line on stderr with a non-zero exit status: read
the message and correct the command rather than retrying it unchanged.

## Rules

1. **Never update in place.** A changed record is a new row with the
   same `Id`. History is kept; current state is computed.
2. **Read through the functions** below, not with hand-written
   collapse queries. They are the shared definition of "current".
3. **Write in batches**: one `.set-or-append ... <| datatable(...)[...]`
   per batch of rows. Never write two versions of the same `Id` in one
   command.
4. **Every finding has a `Source`** (file and page, URL, or the task
   that produced it) unless it is a hypothesis.
5. **Corrections are explicit**: a new finding, the old one rewritten
   with `Lifecycle = "corrected"`, and a `supersedes` edge whose
   `Basis` says why. Then run `DependsOn("<old id>")` and review each
   dependent.
6. **Set `SessionId`** on every row you write to a value unique to
   this session.
7. Do not supply `_TimeReceived`; the engine stamps it.

## First use: create the scope

Run once per scope. `.create-merge` and `.create-or-alter` make these
safe to run again:

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
.create-or-alter function with (folder = "memory", docstring = "Open and blocked tasks, highest priority first") OpenTasks() {
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
become `corrected`.

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
correction. Read the open tasks before starting new work.

## Writing

```kql
.set-or-append Tasks <| datatable(Id:string, Title:string, Status:string, Priority:long, Project:string, Note:string, SessionId:string)
  ["T-1", "Find the source of the 1929 bearing", "open", 1, "demo", "", "session-1"]
```

```kql
.set-or-append Findings <| datatable(Id:string, Kind:string, Claim:string, Confidence:string, Lifecycle:string, Project:string, Source:string, SessionId:string)
  ["F-1", "finding", "The 1929 bearing comes from a compass traverse", "tested", "active", "demo", "report-1929.pdf p4", "session-1"]
```

```kql
.set-or-append Edges <| datatable(EdgeId:string, Src:string, Dst:string, Rel:string, Basis:string, Status:string, SessionId:string)
  ["E-1", "F-1", "T-1", "about", "", "asserted", "session-1"]
```

```kql
.set-or-append Tasks <| datatable(Id:string, Title:string, Status:string, Priority:long, Project:string, Note:string, SessionId:string)
  ["T-1", "Find the source of the 1929 bearing", "done", 1, "demo", "F-1", "session-1"]
```

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
- Every finding of kind `finding` has an `about` edge to its task.
- Every `supersedes` edge's `Dst` has `Lifecycle == "corrected"`, and
  `DependsOn()` was reviewed for it.
- If `.show tables` shows a table with many extents, run
  `.compact table <T>` (old files are kept until `.gc`).
