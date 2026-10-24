# gokql for AI agents

This page is written for a language model that will use gokql through
a shell. It assumes you already know KQL; it lists how to run gokql,
where it differs from Azure Data Explorer, and how to recover from
errors.

## Running queries

```bash
gokql -db <DIR> -mode jsonl -e '<KQL>'      # one statement
gokql -db <DIR> -mode csv < script.kql       # several statements
```

- Use `-mode jsonl` (one JSON object per row) or `-mode csv` for
  results you will read; the default `table` mode is for people.
- Always pass `-db` with an absolute path, and `-e` for a single
  statement (a lone argument is guessed to be a query or a directory).
- Put the query in single quotes and use double quotes for strings
  inside it; an apostrophe inside a double-quoted KQL string needs no
  escaping. For writes, long or multi-line queries, send them on stdin
  with a quoted heredoc (`<<'EOF'`), which avoids shell quoting; a
  quoted heredoc does not expand `$VARIABLES`. Statements on stdin are
  separated by blank lines; the full rules are in
  [the CLI reference](reference/cli.md#statements-on-stdin-and-in-files).
  Every statement runs even if an earlier one failed.
- On error, gokql prints one line, `Error: ...`, to stderr and exits
  with status 1. Read the message; it usually names the fix.
- `-readonly` refuses all writes. Use it whenever you only need to look.
- Without `-db` the database is in-memory and disappears on exit.

## Finding your way around a database

| To learn | Run |
|---|---|
| Tables, row and extent counts | `.show tables` |
| A table's columns and types | `T \| getschema \| project ColumnName, ColumnType` |
| Stored functions (the database's own vocabulary) | `.show functions \| project Name, Parameters, DocString` |
| A function's body | `.show function F` |
| Other databases reachable from here | `.show databases` |
| Every management command | `.help` |
| A sample | `T \| take 5` |

Prefer a database's stored functions to writing your own versions of
them: they encode its conventions (for example, how "current" is
defined).

## Writing

```kql
.set-or-append Notes <| datatable(Id:string, Text:string, Source:string)
  ["N-1", "First note", "meeting 2026-10-01",
   "N-2", "Second note", "report.pdf p3"]
```

- Writes append. To change a record, append a new row with the same
  key, and read the latest with
  `T | summarize arg_max(_TimeReceived, *) by Id`. Fix mistakes the
  same way; do not use `.drop` or `.delete` unless asked.
- Write many rows per command (one `datatable`), not one command per
  row.
- The query must supply every column of an existing table, matched by
  name. A missing value is a typed null: `long(null)`, `real(null)`,
  `datetime(null)`; strings use `""`.
- An extra column is an error unless you add
  `with (extend_schema=true)`.
- `datatable` accepts `datetime(...)` literals and ISO strings for
  `datetime` columns, and `dynamic([...])` / `dynamic({...})` for
  `dynamic` ones.
- `.set-or-append` creates the table if it does not exist;
  `.create-merge table T (...)` declares one up front.
- Never write two rows with the same key in one command: they get the
  same `_TimeReceived`. Separate commands get later stamps.

## Where gokql differs from ADX

| Topic | In gokql |
|---|---|
| Ingestion time | Every row has a hidden `_TimeReceived` (not shown by `*` or `take`; name it to use it). `ingestion_time()` returns it. Do not write it yourself. |
| Strings | Never null, as in ADX: test with `isempty()`, not `isnull()`. |
| Row order | Unspecified without `order by`, as in ADX, and may differ from ADX. Sort whenever order matters. |
| Databases | A database is a directory, named after it. Other databases come from the directory's `gokql.yaml` and are read-only from here; `database("name").T` and `cluster("name").database("name").T` work as in ADX. |
| Outbound calls | `http_request` and the `ai_*` plugins need a callout policy rule in `gokql.yaml`; `externaldata` over http is allowed unless a rule denies it. You cannot change the policy with a command; ask the operator. |
| Sentinel and Defender | Their tables, ASIM parsers and `_GetWatchlist` are not built in. |
| Charts | `render` is accepted and ignored. |
| GQL, openCypher | `#crp query_language=gql` (or `opencypher`) and `#crp query_graph_reference=<graph>` lines before the query; `#crp` sets a client request property. |
| Not available | `python()`, `R()`, the SQL/MySQL/PostgreSQL/Cosmos DB request plugins, `external_table()`, `stored_query_result()`, `geo_info_from_ip_address()`. |
| gokql additions | `csv()`, `json()`, `ndjson()`, `parquet()`, `arrow()`, `vortex()` read files as tables, `-mode arrow` and `-mode vortex` write Apache Arrow and portable Vortex; `embed_text()`, `.embed-into` and `.chunk-file` do embeddings. |

Whether a specific operator, function, aggregation or plugin is
supported is in [reference/support.json](reference/support.json)
(`"supported": true/false`, with a note when gokql differs).

## Errors and what to do

| Error contains | Cause and fix |
|---|---|
| `table "X" not found` | Wrong name (names are case sensitive) or wrong database: `.show tables`, `.show databases`. |
| `column "X" not found` | Check `getschema` for the exact name (case sensitive). |
| `query result is missing column "X"` | A write must supply every column: add `X = <typed null>`. |
| `with (extend_schema=true)` | The write adds a column the table lacks: add the option, or drop the column. |
| `unsupported function: f` | Not implemented (see `support.json`), or a typo. |
| `read-only` | The database or session is read-only; do not retry the write. |
| `callout ... is not allowed` | The URL needs a callout rule in `gokql.yaml`; report it to the operator. |
| `parse: ...` | Syntax: the message names the operator and the unexpected text. |

## Patterns that work well

- Current state: `T | summarize arg_max(_TimeReceived, *) by Id`.
- History of one record: `T | where Id == "X" | order by _TimeReceived asc`.
- Incremental reads: remember `print cursor_current()`, later read
  `T | where cursor_after("<cursor>")`.
- Relationships: keep an edge table (`Src`, `Dst`, `Rel`) and query
  it with `make-graph` / `graph-match`, or GQL/openCypher.
- Validation on write: an update policy with `assert()` and
  `"IsTransactional": true` rejects bad rows ([writing data](guide/writing-data.md#validating-writes-update-policies)).
- Memory across sessions: [agent memory](use-cases/agent-memory.md)
  and its [skill](use-cases/agent-memory-skill/SKILL.md).

## Further reading

- [Getting started](getting-started.md)
- [Writing data](guide/writing-data.md), [Storage](guide/storage.md)
- [Reference](reference/README.md), [Compatibility](compatibility.md)
