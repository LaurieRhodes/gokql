# gokql

A single-binary KQL (Kusto Query Language) engine over local Vortex
columnar storage, built to be a durable, queryable store that LLM agents
read and write through a shell.

```bash
gokql -db ./scope -mode csv -e 'Findings | where Confidence == "locked" | summarize count() by Project'
```

## Why

Agents need somewhere to keep state between sessions: tasks, findings,
decisions, the links between them, and text they can search later.
gokql makes that store a KQL database:

- **A language models already write.** KQL is used daily in Microsoft
  Sentinel, Defender and Azure Data Explorer, and its documentation is
  extensive. gokql follows ADX semantics closely, checked against
  Microsoft's reference examples and `Kusto.Language`, so a query a
  model writes from that knowledge runs as written.
- **One process, one directory.** A database is a directory of
  self-describing `.vtx` extent files and nothing else. There is no
  server to start, no catalog file to corrupt and no schema registry to
  keep in step. Point any number of sessions at the same directory.
- **Append-only by design.** Writes add extents and never rewrite
  history. Every row gets an engine-stamped `_TimeReceived`, so the
  current value of a record is `summarize arg_max(_TimeReceived, *) by
  Id` and the history stays available.
- **Plain output.** `-mode csv`, `tsv`, `json` or `jsonl`, errors as
  one line on stderr, a non-zero exit status on failure: easy to call
  from a tool and to parse. JSON keys follow the column order, and
  dynamic values arrive as JSON rather than escaped strings.
- **Retrieval in the same language.** Text embeddings, chunking and
  vector similarity are KQL functions and commands, so semantic search
  can be joined and filtered like any other table.

## Quick start

```bash
go build -o gokql ./cmd/gokql/

# Create a table and append to it (the directory is created on first write)
gokql -db ./scope -e '.create table Findings (Id: string, Claim: string, Confidence: string, Project: string)'
gokql -db ./scope -e '.set-or-append Findings <| datatable(Id:string, Claim:string, Confidence:string, Project:string)
  ["F-1", "Gate faces the solstice sunrise", "tested", "girsu"]'

# A later write supersedes the earlier row with the same Id
gokql -db ./scope -e '.set-or-append Findings <| datatable(Id:string, Claim:string, Confidence:string, Project:string)
  ["F-1", "Gate faces the solstice sunrise within 1.5 deg", "locked", "girsu"]'

# Save the "current state" query as a stored function
gokql -db ./scope -e '.create-or-alter function Current() { Findings | summarize arg_max(_TimeReceived, *) by Id }'

# Read it back, read-only, as JSON lines
gokql -db ./scope -readonly -mode jsonl -e 'Current() | project Id, Claim, Confidence'
```

Without `-e`, gokql opens a REPL (`.help` lists the dot-commands).
Without `-db`, it runs against a temporary in-memory database.

## Storage

Storage is [vortex-go](https://github.com/LaurieRhodes/vortex-go), a
native Go implementation of the [Vortex](https://github.com/spiraldb/vortex)
columnar format, readable by the Rust reference implementation.

- **Discovery only.** A database is the set of `<Table>_<id>.vtx`
  extents in a directory. Each extent carries its own schema; engine
  metadata (stored functions, materialized views, dictionaries) lives
  in KQL system tables stored the same way. A long-running engine
  notices when another process adds extents.
- **Columnar scans.** Column projection, per-zone min/max statistics
  for pruning, dictionary, FSST, frame-of-reference, bit-packing, ALP
  and Pcodec encodings, and parallel extent scans.
- **Real nulls.** Numeric, bool, datetime and timespan nulls are stored
  as nulls. Strings are never null, as in ADX: a missing string reads
  as `""`.
- **Schema evolution.** `.create-merge` and `.alter-merge` add columns
  to existing tables; older extents read the new columns as null.
- **Maintenance.** `.compact table T` / `.compact database` merge
  small extents; `.drop` archives rather than deletes.

## Language

The parity target is Azure Data Explorer. Each area below was checked
against Microsoft's documentation examples rather than written from
function names. [docs/kql_coverage.md](docs/kql_coverage.md) has the
full verified list and [docs/parity_backlog.md](docs/parity_backlog.md)
the remaining gaps.

- **Tabular operators:** `where`, `project*`, `extend`, `summarize`,
  `join` (all kinds), `lookup`, `union`, `mv-expand`, `mv-apply`,
  `parse`/`parse-where`, `search`, `top`/`top-nested`, `make-series`,
  `scan`, `partition`, `fork`, `as`, `materialize`, `render`
  (passthrough) and more.
- **Functions:** strings, datetime and timespan, math, dynamic/JSON,
  IPv4/IPv6, hashing, regex, `decimal`, time series (`series_*`),
  window functions, and geospatial on the S2 sphere (distances,
  containment, areas, geohash and S2 cells, `geo_polygon_lookup`,
  `geo_line_lookup`).
- **Aggregations:** the standard set with `-if` variants, `arg_max`/
  `arg_min`, percentiles, `dcount`, `hll`/`tdigest` sketches.
- **Graphs:** `make-graph`, `graph-match` (variable-length paths,
  cycles), `graph-shortest-paths`, `graph-mark-components`,
  `graph-to-table`, and persistent graph models and snapshots.
- **Database objects:** `let`, user-defined and stored functions,
  materialized views, `datatable`, query batches.

## Beyond ADX: embeddings

Not part of ADX parity, and the only feature that talks to anything
outside the process. Details: [docs/embeddings.md](docs/embeddings.md).

| Surface | What it does |
|---|---|
| `.chunk-file T <\| "path"` | Split a document into paragraph chunks with heading trails and line numbers |
| `.embed-into E <\| query` | Embed a `Text` column in bulk through a local backend (Ollama or any OpenAI-compatible endpoint, such as llama-server) |
| `embed_text("...")` | Embed a query string |
| `series_cosine_similarity(a, b)` | Rank stored vectors (standard ADX function) |

```kql
let q = embed_text("which gate alignments were tested?");
FindingsEmbeddings
| extend score = series_cosine_similarity(Embedding, q)
| top 5 by score desc
| join kind=inner (Current()) on Id
| project Id, Claim, score
```

## Getting data in

- `.set-or-append T <| query` and `.ingest inline into table T <| rows`
  for writes from an agent.
- `.ingest csv into table T from "path"` for bulk loads.
- `csv("path")`, `json()`, `ndjson()` and `parquet()` read a file as a
  table source for one-off queries and imports; they are not the
  storage layer.

## Serving

`gokql -serve -db ./scope -addr :8080` serves one database over HTTP
using Kustainer's REST protocol (`POST /v1/rest/query`,
`/v1/rest/mgmt`), so existing Kusto clients and
[kustainer-ui](https://github.com/LaurieRhodes/kustainer-ui) can query
it.

## Command line

```
gokql [flags] [db-dir] [query]

  -db DIR       database directory
  -e QUERY      run one query or command and exit
  -readonly     open the database read-only
  -mode M       table (default), csv, tsv, json, jsonl
  -no-rc        don't load ~/.okqlrc
  -serve        HTTP mode (with -addr, -dbname)
  -v            show scan diagnostics
```

`echo 'Findings | count' | gokql -db ./scope` reads a query from stdin.

## Building

```bash
git clone https://github.com/LaurieRhodes/vortex-go.git
git clone https://github.com/LaurieRhodes/gokql.git
cd gokql
go build -o gokql ./cmd/gokql/
```

Requires Go 1.24.9 or later. vortex-go must be a sibling directory
(`replace ../vortex-go` in `go.mod`). The other dependencies are
parquet-go, xxhash, golang/geo and yaml.v3.

## Relationship to Microsoft's KQL

gokql is an independent Go implementation. It does not fork, embed or
translate code from Microsoft's
[Kusto-Query-Language](https://github.com/microsoft/Kusto-Query-Language)
repository, the official grammar and `Kusto.Language` parser, which is
Apache 2.0 licensed and openly maintained. That openness is what makes a
project like this possible. For the full language surface, distributed
execution or enterprise support, use Azure Data Explorer; gokql is a
local, single-node engine for a narrower job.

## Licence

[MIT](LICENSE)
