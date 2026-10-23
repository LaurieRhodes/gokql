# Agent memory

An LLM agent forgets everything between sessions. gokql gives it a
store it can read and write through a shell, in a language it already
writes well, with properties that suit how agents work:

- **Append-only.** An agent cannot destroy what it wrote earlier by
  updating a row; a correction is a new row, and the old one is still
  there to audit.
- **Current state is a query.** Stored functions define "the latest
  version of each task", "the findings still believed", "what depends
  on this". Every session, and every agent, reads the same definitions.
- **Provenance and relationships.** Findings carry their source, and
  edges record why one finding replaced another or depends on another,
  so a correction can be traced to everything it affects.
- **Semantic recall** over the same tables, with embeddings.
- **No server.** A directory per project, any number of agents
  appending to it, `-readonly` for those that should only look.

This page builds a small memory scope step by step. A ready-to-use
skill that teaches an agent the same discipline is in
[agent-memory-skill/SKILL.md](agent-memory-skill/SKILL.md).

## Schema

Three tables carry most of the value: work to do, things learned, and
the links between them.

```kql
.create table Tasks (Id: string, Title: string, Status: string, Priority: long, Project: string, Note: string, SessionId: string)
```

```kql
.create table Findings (Id: string, Kind: string, Claim: string, Confidence: string, Lifecycle: string, Project: string, Source: string, SessionId: string)
```

```kql
.create table Edges (EdgeId: string, Src: string, Dst: string, Rel: string, Basis: string, Status: string, SessionId: string)
```

- `Findings.Kind`: `finding`, `decision`, `constraint`, `hypothesis`,
  `correction`, `citation`.
- `Findings.Confidence` (how sure): `hypothesis`, `tested`, `locked`.
  `Findings.Lifecycle` (whether still believed): `active`, `corrected`,
  `abandoned`. They are independent: a `locked` finding can later be
  `corrected` without losing the record that it was once locked.
- `Edges.Rel`: `supersedes` (Src replaces Dst), `chosen-over` (Src was
  preferred to Dst), `depends-on` (Src assumes Dst), `about`
  (finding about a task). `Status` is `asserted` or `retracted`.

## The function library

The functions are the contract: agents read through them instead of
writing their own collapse logic.

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

## A session writes

Writes are batches of `datatable` rows: one command per batch, not one
per row. A record changes by writing a new row with the same `Id`;
never write two versions of one record in the same command (they would
share one `_TimeReceived`).

```kql
.set-or-append Tasks <| datatable(Id:string, Title:string, Status:string, Priority:long, Project:string, Note:string, SessionId:string)
  ["T-1", "Establish the gate bearing", "open", 1, "girsu", "", "s1",
   "T-2", "Test the midsummer sunrise reading", "open", 2, "girsu", "", "s1"]
```

```kql
.set-or-append Findings <| datatable(Id:string, Kind:string, Claim:string, Confidence:string, Lifecycle:string, Project:string, Source:string, SessionId:string)
  ["F-1", "finding", "Gate bearing is 59.5 degrees", "tested", "active", "girsu", "survey-1929.pdf p12", "s1",
   "F-2", "hypothesis", "Gate faces the midsummer sunrise", "hypothesis", "active", "girsu", "", "s1"]
```

```kql
.set-or-append Edges <| datatable(EdgeId:string, Src:string, Dst:string, Rel:string, Basis:string, Status:string, SessionId:string)
  ["E-1", "F-2", "F-1", "depends-on", "the sunrise azimuth test needs the bearing", "asserted", "s1",
   "E-2", "F-1", "T-1", "about", "", "asserted", "s1"]
```

```kql
.set-or-append Tasks <| datatable(Id:string, Title:string, Status:string, Priority:long, Project:string, Note:string, SessionId:string)
  ["T-1", "Establish the gate bearing", "done", 1, "girsu", "F-1", "s1"]
```

## A later session corrects

A new survey changes the bearing. The correction is a new finding, the
old one is marked `corrected`, and a `supersedes` edge records why:

```kql
.set-or-append Findings <| datatable(Id:string, Kind:string, Claim:string, Confidence:string, Lifecycle:string, Project:string, Source:string, SessionId:string)
  ["F-3", "correction", "Gate bearing is 60.1 degrees", "tested", "active", "girsu", "survey-2016.pdf table 3", "s2",
   "F-1", "finding", "Gate bearing is 59.5 degrees", "tested", "corrected", "girsu", "survey-1929.pdf p12", "s2"]
```

```kql
.set-or-append Edges <| datatable(EdgeId:string, Src:string, Dst:string, Rel:string, Basis:string, Status:string, SessionId:string)
  ["E-3", "F-3", "F-1", "supersedes", "2016 total-station survey replaces the 1929 compass bearing", "asserted", "s2"]
```

What does the correction put at risk? Everything that depends on the
old finding, however indirectly:

```kql
DependsOn("F-1")
```

```output
Id    Claim                             Hops
────  ─────                             ────
F-2   Gate faces the midsummer sunrise  1
(1 rows)
```

## Starting a session

A new session (or a new agent) orients itself with three reads:

```kql
CurrentFindings()
| project Id, Kind, Claim, Confidence, Source
| order by Id asc
```

```output
Id    Kind        Claim                             Confidence  Source
────  ────        ─────                             ──────────  ──────
F-2   hypothesis  Gate faces the midsummer sunrise  hypothesis
F-3   correction  Gate bearing is 60.1 degrees      tested      survey-2016.pdf table 3
(2 rows)
```

```kql
OpenTasks()
| project Id, Title, Priority
```

```output
Id    Title                               Priority
────  ─────                               ────────
T-2   Test the midsummer sunrise reading  2
(1 rows)
```

```kql
LatestFindings()
| where SessionId == "s2"
| project Id, Claim, Lifecycle
| order by Id asc
```

```output
Id    Claim                         Lifecycle
────  ─────                         ─────────
F-1   Gate bearing is 59.5 degrees  corrected
F-3   Gate bearing is 60.1 degrees  active
(2 rows)
```

The full history is still there:

```kql
Findings
| where Id == "F-1"
| project Claim, Lifecycle, SessionId, _TimeReceived
| order by _TimeReceived asc
| project-away _TimeReceived
```

```output
Claim                         Lifecycle  SessionId
─────                         ─────────  ─────────
Gate bearing is 59.5 degrees  active     s1
Gate bearing is 59.5 degrees  corrected  s2
(2 rows)
```

## Semantic recall

With an [embedding backend](../guide/embeddings-and-ai.md), embed the
findings and search by meaning, filtering before ranking:

```kql no-run
.embed-into FindingsEmbeddings <| CurrentFindings() | project Id, Text = Claim, Project
```

```kql no-run
let q = embed_text("what do we know about the orientation of the gate?");
FindingsEmbeddings
| where Project == "girsu"
| extend Score = series_cosine_similarity(Embedding, q)
| top 5 by Score desc
| join kind=inner (CurrentFindings()) on Id
| project Score, Id, Claim
```

## Several agents

Agents can share one scope: writes are independent files and
`.compact` coordinates with readers ([storage](../guide/storage.md)).
Give each session its own `SessionId`. Records written by more than
one session are either a deliberate later change (as F-1's correction
here) or two agents colliding, and are worth a look when agents run in
parallel:

```kql
Findings
| summarize Sessions = dcount(SessionId) by Id
| where Sessions > 1
```

```output
Id    Sessions
────  ────────
F-1   2
(1 rows)
```

A claim table (`Claims (TaskId, SessionId, Kind, At)`) with a
read-side expiry is enough for agents to avoid working on the same
task; nothing needs a lock.

## Running it

An agent with a shell needs only:

```bash
gokql -db ~/memory/girsu -mode jsonl -e 'OpenTasks() | project Id, Title'
gokql -db ~/memory/girsu -e '.set-or-append Findings <| datatable(...)[...]'
```

Give read-only agents `-readonly`. Errors are one line on stderr with
a non-zero exit status, which an agent can read and correct.

## See also

- [The skill](agent-memory-skill/SKILL.md): this discipline as instructions an agent follows
- [For AI agents](../for-agents.md): what an LLM should know about gokql's KQL
- [Research knowledge graphs](research-graphs.md): the same ideas at research scale
