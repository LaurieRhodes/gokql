# gokql

**A local KQL engine in one binary.** gokql runs Kusto Query Language
(the language of Azure Data Explorer, Microsoft Sentinel, Defender and
Fabric) over a directory of columnar files on your own machine. There
is no server, no cloud account and no network dependency.

It is built first for **AI agents**, which already write KQL well and
need somewhere durable to keep what they learn. It works just as well
for **analysts and engineers** who want ADX semantics offline.

```bash
gokql -db ./notes -e '.set-or-append Findings <| datatable(Id:string, Claim:string, Confidence:string)
  ["F-1", "The gate faces the solstice sunrise", "tested"]'
gokql -db ./notes -mode jsonl -e 'Findings | where Confidence == "tested"'
```

## What it is for

| Use | What gokql gives you | Start here |
|---|---|---|
| **Agent memory** | A store an LLM reads and writes in a language it already knows: append-only history, current state by query, provenance links, semantic search | [agent memory](docs/use-cases/agent-memory.md) |
| **Security data triage** | Sentinel-style hunting queries over exported logs, Wiz or Defender findings, CSV and JSON, on a laptop | [security triage](docs/use-cases/security-triage.md) |
| **KQL development** | Write and test queries, functions and update policies offline before they reach ADX | [KQL development](docs/use-cases/kql-development.md) |
| **Research knowledge graphs** | Facts linked to their sources, competing hypotheses side by side, queried as graphs (KQL, GQL or openCypher) | [research graphs](docs/use-cases/research-graphs.md) |
| **Exploratory reasoning** | Hypotheses generated from alternative definitions, tested by queries, with doubt propagating along if/then edges and a frontier of what to check next | [exploratory reasoning](docs/use-cases/exploratory-reasoning.md) |
| **A local "cluster"** | Several specialised databases queried together with `database()` and `cluster()` | [local cluster](docs/use-cases/local-cluster.md) |

## A taste

```kql
.set-or-append Events <| datatable(Time:datetime, User:string, Action:string, Bytes:long)
  [datetime(2026-10-01 08:00), "ana", "login", 0,
   datetime(2026-10-01 08:05), "ana", "download", 52000000,
   datetime(2026-10-01 09:10), "raj", "login", 0,
   datetime(2026-10-01 09:12), "raj", "download", 1200,
   datetime(2026-10-01 23:40), "ana", "download", 880000000]
```

```kql
Events
| where Action == "download"
| summarize Downloads = count(), TotalMB = sum(Bytes) / 1e6, Last = max(Time) by User
| order by TotalMB desc
```

```output
User  Downloads  TotalMB  Last
────  ─────────  ───────  ────
ana   2          932      2026-10-01T23:40:00Z
raj   1          0.0012   2026-10-01T09:12:00Z
(2 rows)
```

Every KQL example in this repository's documentation, except those
marked as needing a network or your own files, is run by the test
suite, and the output shown is what gokql prints.

## Why gokql

- **The language models already write.** KQL is used daily across
  Microsoft's security and data products and is extensively documented.
  gokql follows Azure Data Explorer's semantics, checked against
  Microsoft's documentation examples and against the Kusto emulator:
  983 of the 1,042 deterministic examples in Microsoft's KQL
  documentation give the same results; most of the rest are results ADX
  leaves unspecified, such as row order. A query a model writes from
  that knowledge almost always runs as written. See
  [compatibility](docs/compatibility.md).
- **One binary, one directory.** A database is a directory of
  self-describing Vortex columnar files. Nothing to install or run
  alongside it, no catalog file to corrupt. Any number of processes can
  read and append to the same directory.
- **Append-only, with history.** Writes add files rather than changing
  earlier ones. Every row is stamped with the time it arrived, so the
  current value of a record is one `arg_max` away and every earlier
  value is still there. (Replacing, deleting and compacting supersede
  files; nothing is removed until `.gc`.)
- **Fast where it matters.** Filters and aggregations run on the
  compressed columns in parallel: counting, filtering and grouping ten
  million rows takes tens of milliseconds on a laptop
  ([performance](docs/performance.md)).
- **Built for tools.** `-mode csv | tsv | json | jsonl | arrow | vortex`, errors as one
  line on stderr, non-zero exit on failure. An HTTP mode speaks the
  Kusto REST protocol for existing clients.
- **More than queries.** Graphs (KQL `make-graph`/`graph-match`, plus
  GQL and openCypher), geospatial functions (S2, H3, geohash),
  time series, text embeddings and vector search, and HTTP and LLM
  calls behind an allow-list.

## Coverage at a glance

Generated from the engine by the test suite
([reference](docs/reference/README.md)):

| Area | Supported |
|---|---|
| Tabular operators | 51 of 51 |
| Scalar functions | 386 of 392 |
| Aggregation functions | 57 of 59 |
| Window functions | 7 of 7 |
| Plugins (`evaluate`) | 33 of 41 |
| Management commands | tables, ingestion, functions, materialized views, update policies, graph models, scripts |

What is missing is listed with reasons in the reference: mainly plugins
that would embed Python or R or need database drivers.

## Install

Requires Go 1.24.9 or later. gokql builds against
[vortex-go](https://github.com/LaurieRhodes/vortex-go), which must be
checked out beside it:

```bash
git clone https://github.com/LaurieRhodes/vortex-go.git
git clone https://github.com/LaurieRhodes/gokql.git
cd gokql
go build -o gokql ./cmd/gokql
./gokql -version
```

Then follow [getting started](docs/getting-started.md).

## Documentation

- [Getting started](docs/getting-started.md): first database, writes, queries, the REPL, serving
- [Guides](docs/README.md): storage, writing data, files and external data, the local cluster, graphs, geospatial, embeddings and AI, security, HTTP serving
- [Reference](docs/reference/README.md): every operator, function, aggregation, plugin, command and CLI flag, with support status
- [Compatibility with ADX](docs/compatibility.md): how parity is checked and where gokql differs
- [Performance](docs/performance.md): loading and query times over ten million rows
- [For AI agents](docs/for-agents.md): the compact rules an LLM needs to use gokql well ([llms.txt](llms.txt))
- [Development](docs/dev/README.md): architecture, building, testing, contributing

## Relationship to Microsoft's KQL

gokql is an independent Go implementation. It does not fork, embed or
translate code from Microsoft's
[Kusto-Query-Language](https://github.com/microsoft/Kusto-Query-Language)
repository, whose openly published grammar and documentation make a
project like this possible. For distributed execution, the full service
surface or enterprise support, use Azure Data Explorer; gokql is a
local, single-node engine for a narrower job. "Kusto" and "KQL" are
used here only to describe the language implemented.

## Licence

[MIT](LICENSE). Descriptions quoted from Microsoft's KQL documentation
are CC BY 4.0, attributed where they appear.
