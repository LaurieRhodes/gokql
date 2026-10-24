# Compatibility with Azure Data Explorer

gokql's specification is Azure Data Explorer's KQL. This page says how
agreement is measured, how close it is, and where gokql differs on
purpose.

## How it is checked

1. **Microsoft's documentation examples.** Every `kusto` code block in
   Microsoft's KQL documentation (the
   [dataexplorer-docs](https://github.com/MicrosoftDocs/dataexplorer-docs)
   repository, 1,411 blocks) is run on both gokql and the Kusto
   emulator, and the results are compared cell by cell.
2. **The Kusto emulator** ([Kustainer](https://learn.microsoft.com/azure/data-explorer/kusto-emulator-overview),
   ADX's own engine in a container, version 2025.07.06) is the
   reference where the documentation is silent, ambiguous or wrong.
   Where the two disagree, gokql follows the emulator and the test says
   so; the one exception is GQL and openCypher (below).
3. **Kusto.Language** (Microsoft's KQL front end, version 12.4.1)
   supplies function signatures, return types and plugin output
   schemas.
4. **The reference catalogue.** `docs/reference/` lists every entry of
   Microsoft's documentation and whether gokql accepts it, probed from
   the engine by the test suite.

## Results

Of the 1,411 documentation blocks, 1,042 give deterministic results
on the emulator (the others use random functions or the current time,
or are fragments the emulator cannot run). Of those:

| | Blocks |
|---|---|
| Same result, same order | 942 |
| Same rows in a different order; the query does not sort | 28 |
| Same rows in a different order; the query sorts, and rows tie | 13 |
| **Agree** | **983 (94%)** |
| Differ | 59 |

The counts move by one or two between runs, because a few examples
(shortest paths with equal-length alternatives) give different answers
on the emulator itself from one run to the next.

Most of the 59 that differ are results ADX leaves unspecified:

- **Which rows win a tie**: `top`, `take`, `arg_max` and similar pick
  among equal rows differently, so the rows themselves differ; also
  `partition` subresults and neighbour-cell lists.
- **Approximate aggregates**: `dcount`, `hll` and `tdigest` estimates
  agree within their error bounds but not digit for digit.
- **Heuristic operators and plugins** (`autocluster`, `basket`,
  `diffpatterns`, `diffpatterns_text`, the `reduce` operator, the
  `series_periods_*` functions): the output shape
  is ADX's, but ADX's selection algorithm is not published; on the
  reference examples gokql finds ADX's patterns, sometimes with more.

Some are known differences in deterministic results:

- **Byte-level encodings**: `gzip`/`zlib` compressed bytes (they
  decompress to the same data) and `hash()` of decimal values.
- **Sketches**: `hll` and `tdigest` values from ADX cannot be merged
  here, and vice versa.

## Support by area

| Area | Supported |
|---|---|
| Tabular operators | 51 of 51 |
| Scalar functions | 386 of 392 |
| Aggregation functions | 57 of 59 |
| Window functions | 7 of 7 |
| Scalar operators | 37 of 37 |
| Query statements | 7 of 8 |
| Plugins | 33 of 41 |

[support.json](reference/support.json) has the detail. What is
missing, and why:

| Missing | Why |
|---|---|
| `python()`, `R()` plugins | Would embed an interpreter in the binary |
| `sql_request`, `mysql_request`, `postgresql_request`, `cosmosdb_sql_request`, `azure_digital_twins_query_request`, `dax_request` | Need database drivers or Azure services |
| `external_table()` and the `inline_external_table` statement | External tables are not modelled; `externaldata` reads the same files |
| `stored_query_result()` | Not implemented |
| `columnnamesof()`, `extenttags()`, `percentile_array_tdigest()` | Listed in Microsoft's docs index but rejected by ADX itself |
| `geo_info_from_ip_address()` | Needs a GeoIP database |
| `restrict` statement | Access restriction belongs to a multi-tenant service |
| `argmax`, `argmin` | Deprecated forms of `arg_max`, `arg_min` |

Some features run in gokql but not in Log Analytics (Sentinel), which
supports a subset of ADX: management commands, `http_request`, most
plugins. A query that passes here may still need changes for Sentinel;
see [KQL development](use-cases/kql-development.md#sentinel-and-log-analytics).

Management commands cover tables, ingestion, stored functions,
materialized views, update policies, graph models and scripts; cluster
administration, security roles, retention and caching policies have no
equivalent on a single node.

## Deliberate differences

- **The cluster** is a set of directories configured in `gokql.yaml`,
  not a service. `database()`, `cluster()`, `.show databases`,
  `current_cluster_endpoint()`, `current_principal()` and the cursor
  functions behave as in ADX within that model
  ([the local cluster](guide/cluster.md)).
- **`_TimeReceived`** is a real hidden column holding each row's
  ingestion time, so `ingestion_time()` is queryable history.
- **GQL and openCypher** follow the documentation and the GQL standard
  where the emulator departs from its own documentation (it rejects
  `LET`, `FOR`, `NEXT` and `FILTER`, and evaluates `OPTIONAL MATCH`
  and `CASE` incorrectly).
- **`render`** is accepted and returns the data unchanged.
- **Additions**, all named so they cannot be mistaken for ADX:
  `csv()`, `json()`, `ndjson()`, `parquet()`, `arrow()`, `vortex()`
  sources; `-mode arrow` output;
  `embed_text()`, `.embed-into`, `.chunk-file`; `.compact`, `.gc`;
  `Atomic` in `.execute database script`; `BlockDirectWrites` in
  update policies; `.ingest csv into table T from "path"` (ADX's
  `.ingest into table T (...)` form is also accepted).
- **`assert()`** is evaluated per row and accepts any expression; ADX
  requires its condition to be a constant at query-analysis time, so
  row-level `assert()` validation (as in this documentation's update
  policy examples) is gokql-only.

## Reporting a difference

A difference from ADX that is not in the list above is a bug. Include
the query, gokql's output, and ADX's (or the emulator's) output.
