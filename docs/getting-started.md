# Getting started

This walk-through creates a database, writes to it, queries it, and
shows the ways to drive gokql: one-shot commands, pipes, the REPL and
HTTP. It assumes you have built the binary ([install](../README.md#install)).

## 1. A database is a directory

```bash
gokql -db ./scope -e '.create table Tasks (Id: string, Title: string, Status: string, Priority: long)'
```

`./scope` is created on the first write. From now on it holds the
`.vtx` files ("extents") that each write adds, each carrying its own
schema. There
is no other state: copy the directory and you have copied the database.
Without `-db`, gokql uses a temporary in-memory database.

The examples below are KQL; run each with
`gokql -db ./scope -e '<query>'`, paste it into the REPL, or pipe it in.

## 2. Write rows

`.set-or-append` appends the rows of any query, creating the table if
needed. A `datatable` literal is the usual way to write a batch:

```kql
.set-or-append Tasks <| datatable(Id:string, Title:string, Status:string, Priority:long)
  ["T-1", "Load the firewall export", "open", 1,
   "T-2", "Draft the summary", "open", 2,
   "T-3", "Check the date range", "open", 3]
```

Writes never change earlier rows. To change a record, append a new row
with the same key:

```kql
.set-or-append Tasks <| datatable(Id:string, Title:string, Status:string, Priority:long)
  ["T-1", "Load the firewall export", "done", 1]
```

## 3. Query

```kql
Tasks
| summarize Versions = count() by Id
| order by Id asc
```

```output
Id    Versions
────  ────────
T-1   2
T-2   1
T-3   1
(3 rows)
```

Each row carries a hidden `_TimeReceived` column, stamped by the
engine on write. The current state of every task is the latest row per
`Id`:

```kql
Tasks
| summarize arg_max(_TimeReceived, *) by Id
| project Id, Title, Status
| order by Id asc
```

```output
Id    Title                     Status
────  ─────                     ──────
T-1   Load the firewall export  done
T-2   Draft the summary         open
T-3   Check the date range      open
(3 rows)
```

Save it as a stored function so nobody has to remember it:

```kql
.create-or-alter function with (docstring = "Latest row per task") CurrentTasks() {
    Tasks | summarize arg_max(_TimeReceived, *) by Id
}
```

```kql
CurrentTasks()
| where Status == "open"
| order by Priority asc
| project Id, Title
```

```output
Id    Title
────  ─────
T-2   Draft the summary
T-3   Check the date range
(2 rows)
```

`.show tables`, `.show functions` and `.help` describe what is there:

```kql
.show tables
| project TableName, Rows, Extents
```

```output
TableName  Rows  Extents
─────────  ────  ───────
Tasks      4     2
(1 rows)
```

## 4. Output for programs

The default output is a table for people. Programs should ask for
`csv`, `tsv`, `json` or `jsonl`:

```bash
gokql -db ./scope -mode jsonl -e 'CurrentTasks() | project Id, Status | order by Id asc'
# {"Id":"T-1","Status":"done"}
# {"Id":"T-2","Status":"open"}
# {"Id":"T-3","Status":"open"}
```

Errors go to stderr as one line and the exit status is non-zero.
`-readonly` refuses every write, which is the right default for an
agent that should only look.

## 5. Pipes and scripts

Without `-e`, gokql reads statements from stdin when it is not a
terminal. Separate statements with a blank line; a statement may span
lines (the exact rules are in
[the CLI reference](reference/cli.md#statements-on-stdin-and-in-files)).
Every statement runs; if any fails, the exit status is 1.

```bash
cat <<'EOF' | gokql -db ./scope -mode csv
Tasks | count

CurrentTasks()
| where Status == "open"
| count
EOF
```

## 6. The REPL

`gokql -db ./scope` with a terminal opens an interactive prompt for KQL,
management commands and the REPL commands listed in
[the CLI reference](reference/cli.md#repl). `~/.gokqlrc` may set `.mode` and `.timer` at start-up
(`-no-rc` skips it).

## 7. Query files without loading them

`csv()`, `json()`, `ndjson()` and `parquet()` read a file as a table
for one query, and `externaldata` reads files or URLs the way ADX does:

```bash
gokql -mode csv -e 'csv("./export.csv") | summarize count() by Action'
```

[Files and external data](guide/files-and-external-data.md) covers
these and loading them into tables.

## 8. Serve over HTTP

```bash
gokql -serve -db ./scope -addr 127.0.0.1:8080
```

The server speaks the Kusto REST protocol (`POST /v1/rest/query` and
`/v1/rest/mgmt`), so Kusto clients and
[kustainer-ui](https://github.com/LaurieRhodes/kustainer-ui) can use
it. It listens on this machine only by default and has no
authentication: see [serving](guide/serving.md) before exposing it
further.

## Next

- [Writing data](guide/writing-data.md): every write command, update policies, materialized views
- [Storage](guide/storage.md): what is on disk, concurrency, compaction
- [For AI agents](for-agents.md): the rules to give a model that will use gokql
- [Reference](reference/README.md): everything gokql accepts
