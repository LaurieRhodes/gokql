# Architecture

gokql is a parser, an in-memory execution engine and a storage layer
over a directory of Vortex files, in one Go binary.

```
text ──► pkg/parser ──► Statement ──► pkg/engine ──► *types.Table ──► cmd/gokql (output)
                                        │
                     pkg/catalog ◄──────┘  (extents on disk: discovery, scans, writes)
```

## Parsing (`pkg/parser`)

`parser.Parse` splits the input into statements (`;`), expands
`declare pattern`, `alias database` and `macro-expand` textually, and
returns a `Statement`:

- `*Query`: a source (table, function call, `datatable`, `range`,
  `print`, `union`, `find`, `search`, `externaldata`, file sources,
  `evaluate`) and a list of `Operator`s, one per pipe segment.
- `*CompoundStatement`: `let` statements, query batches and `set`
  options around a final statement.
- One type per management command (`*SetOrAppendCmd`, `*CreateTableCmd`,
  ...). `*PipedCommand` wraps a `.show` command followed by operators.

Expressions parse into `Expr` trees (`FuncCall`, `BinaryExpr`,
`ColumnRef`, literals). `#crp query_language=gql|opencypher` hands the
text to `pkg/gql`, which translates GQL and openCypher into a
`graph-match` query.

## Execution (`pkg/engine`)

`Engine.Execute` dispatches on the statement type. Queries resolve
their source to a `*types.Table` (rows of `types.Value`, a schema of
named, typed columns) and apply each operator in turn
(`applyPipeline`); most operators are functions from table to table.

- **Scans.** For a stored table, the planner works out which columns
  the pipeline needs and which leading `where` predicates can be tested
  against zone statistics, then scans the table's extents in parallel,
  reading only those columns and skipping zones that cannot match.
  `take` stops early.
- **Expressions** are evaluated per row by `evalExpr`; functions are
  dispatched by name through per-area files (`func_string.go`,
  `func_datetime.go`, `func_geo*.go`, `func_series*.go`, ...). Static
  result types come from `inferExprType` and the generated
  `func_signatures.go`.
- **Aggregations** (`aggregation.go`) implement `summarize`,
  `make-series` and `top-nested`, with columnar fast paths for common
  shapes.
- **Plugins** register themselves in `evaluatePlugins`
  (`plugin_*.go`); `evaluate` looks them up by name.
- **Graphs**: `make-graph` builds an in-memory graph; `graph-match`,
  `graph-shortest-paths` and friends run over it (`graph_*.go`). Graph
  models and snapshots persist as system tables.
- **The cluster layer** (`federation.go`, `cluster.go`) resolves
  `database()` and `cluster()` through `gokql.yaml` (`pkg/config`),
  opening the other database's catalog and pushing row-local operators
  down to it.
- **Callouts** (`callout.go`) check every outbound URL against the
  callout policy.
- **Writes** (`storage.go`, `ingest.go`, `compact.go`) turn a result
  table into one or more new extents, run update policies
  (`updatepolicy.go`) and materialized-view maintenance, and stamp
  `_TimeReceived`.

## Storage (`pkg/catalog`, vortex-go)

A database is the set of `<Table>_<id>.vtx` files in a directory.
`catalog.Open` discovers them by reading each file's footer (cached by
size and modification time) and groups them into tables; there is no
catalog file. Each extent is written to a temporary name and renamed
into place, so readers never see a partial extent. A shared or
exclusive `flock` on `_scope.lock` keeps `.compact`, the one operation
that rewrites, consistent with concurrent readers.

Column encodings, zone statistics and the file format come from
[vortex-go](https://github.com/LaurieRhodes/vortex-go). System tables
(`_Functions`, `_MaterializedViews`, `_UpdatePolicies`,
`_Dictionaries`, `_Graph*`) use the same format.

## The CLI and server (`cmd/gokql`)

`main.go` parses flags, opens the catalog, and either runs one
statement, runs the statements on stdin (`script.go` splits them),
starts the REPL, or starts `-serve` (`server.go`), which opens a fresh
catalog and engine per HTTP request.

## Tests

Most behaviour is tested against expected results taken from
Microsoft's documentation or the Kusto emulator, with the source noted
in the test. `TestReferenceDocs` and `TestDocsExamples` keep the
documentation in step with the engine ([AGENTS.md](../../AGENTS.md)).
