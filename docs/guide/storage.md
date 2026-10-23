# Storage

A gokql database is a directory. This page describes what is in it,
how several processes share it, and how to keep it tidy.

## What is on disk

```
scope/
  Findings_18e14a718a745859ad3f899a.vtx    one extent: rows of Findings from one write
  Findings_18e14b008312167fa99afcf3.vtx
  Tasks_18e14acc17ee99d135f4eeec.vtx
  _Functions_18e14b0083149d1e41fd2c4f.vtx  stored functions, a table like any other
  _scope.lock                              coordinates compaction with other processes
  .dropped/                                tables removed by .drop table
  gokql.yaml                               optional: configuration (cluster, callouts, readonly, -serve)
```

- **Extents.** Each write produces one or more `<Table>_<id>.vtx`
  files in the [Vortex](https://github.com/spiraldb/vortex) columnar
  format (through [vortex-go](https://github.com/LaurieRhodes/vortex-go),
  readable by the Rust reference implementation). Every extent carries
  its own schema, so the set of tables is simply the set of extents.
  There is no catalog file.
- **System tables.** Engine state is stored as tables in the same
  format: `_Functions` (stored functions), `_MaterializedViews`,
  `_UpdatePolicies`, `_Dictionaries` (shared string dictionaries) and
  the `_Graph*` tables of graph models and snapshots. They are
  append-only like everything else, so a stored function's earlier
  definitions are its history.
- **Columnar scans.** Queries read only the columns they use, skip
  zones whose min/max statistics rule them out, and scan extents in
  parallel. Encodings include dictionary, FSST, frame-of-reference,
  bit-packing, ALP and Pcodec.

## Values and nulls

Numeric, `bool`, `datetime`, `timespan` and `guid` columns store real
nulls. Strings are never null, as in ADX: a missing string reads as
`""`, and `isempty()` is the test. `dynamic` values are stored as JSON.
Embedding vectors written by `.embed-into` use a compact float32 form
that reads back as an array.

## History and ingestion time

Writes add extents and never modify existing ones. Each row gets a
hidden `_TimeReceived` column with the time of the write that added
it. It does not appear in `T` or `project *` output, but can be named
in any query; `ingestion_time()` returns it, and
`cursor_current()` / `cursor_after()` give incremental reads:

```kql
.set-or-append Log <| datatable(Message:string)["first"]
```

```kql
Log
| extend Received = isnotnull(_TimeReceived)
| project Message, Received
```

```output
Message  Received
───────  ────────
first    true
(1 rows)
```

## Several processes, one directory

Any number of gokql processes, CLI or `-serve`, can read and append to
one directory at the same time. Writes are new files, so they do not
contend; a long-running engine notices extents written by others.
`.compact` is the one operation that rewrites, and it takes an
exclusive lock on `_scope.lock` while statements take a shared one, so
no reader ever sees rows twice. The lock is released if a process
dies.

`-readonly` opens a database so that every write command is refused.
Give read-only access to anything that should only look.

## Compaction and cleanup

Frequent small writes leave many small extents. `.show tables` reports
the count:

```kql
.show tables
| project TableName, Rows, Extents
```

```output
TableName  Rows  Extents
─────────  ────  ───────
Log        1     1
(1 rows)
```

| Command | What it does |
|---|---|
| `.compact table T` | Merge T's extents into as few as needed; the old files are renamed `.superseded`, not deleted |
| `.compact database` | The same for every table |
| `.merge table T extents` | The same as `.compact table T` |
| `.gc table T` / `.gc database` | Delete files already superseded; safe at any time |
| `.drop extent <id>` | Remove one extent (ids from `.show table T extents`) |
| `.drop table T` | Move all of T's files into `.dropped/<T>_<id>/`, out of the database |

Nothing is lost until `.gc` or until you delete `.dropped/` yourself:
a superseded or dropped file can be renamed back. Check row counts
after `.compact` before running `.gc`.

## Backups and moves

Copy or archive the directory. A copy taken while a write is in
progress may miss that write but is otherwise consistent, because every
extent is complete before it appears under its final name.

## See also

- [Writing data](writing-data.md)
- [The local cluster](cluster.md): several databases as one
- [Commands reference](../reference/commands.md)
