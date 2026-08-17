# gokql

A local KQL (Kusto Query Language) query engine with DuckDB-style ergonomics.
Query CSV, JSON, NDJSON, and Parquet files directly with KQL — no cloud, no setup.

## Quick Start

```bash
# Build
go build -o gokql ./cmd/gokql/

# Query a CSV file immediately — no database required
gokql 'csv("firewall_logs.csv") | where Action == "DENY" | summarize count() by SrcIP'

# Query Parquet exports from Sentinel/ADX
gokql 'parquet("security_events.parquet") | where Severity == "High" | top 10 by Score desc'

# Interactive REPL
gokql
gokql> csv("logs.csv") | getschema
gokql> csv("logs.csv") | where SrcIP has "10.0.1" | summarize count() by Action
gokql> .mode json
gokql> csv("logs.csv") | where Action == "DENY" | project SrcIP, DstIP, Port
gokql> .quit
```

## Why gokql?

**KQL is the language of security operations.** Millions of analysts use it daily in
Microsoft Sentinel, Defender, and Azure Data Explorer. But running KQL queries requires
a cloud workspace — there's no way to query local files with KQL.

gokql fills that gap. It's a local KQL engine that:

- **Queries files directly** — CSV, JSON, NDJSON, Parquet, no ingestion step required
- **Runs anywhere** — single Go binary, no cloud dependency, no containers
- **Speaks KQL** — ~150 scalar function entry points, 24 aggregations, 33 tabular operators (see [KQL Coverage](docs/kql_coverage.md) for the full, verified picture)
- **Stores data in Vortex** — a modern columnar format (5× faster than Parquet)
- **Feels like DuckDB** — positional args, in-memory default, multiple output formats

### Relationship to Microsoft's official KQL

gokql is an independent, from-scratch Go implementation — it doesn't fork, embed,
or translate any code from Microsoft's own [Kusto-Query-Language](https://github.com/microsoft/Kusto-Query-Language)
repository (the official ANTLR grammar and `Kusto.Language` C# parser/library).
Microsoft's project is actively maintained, Apache 2.0 licensed, and is the real,
enterprise-grade reference implementation of the language — Microsoft has clearly made
a deliberate choice to keep the KQL grammar itself open and royalty-free, which is what
makes an independent project like this one possible at all.

If you need the full, official language surface, distributed execution, or enterprise
support, that's the project to use. gokql exists for a different, much narrower
purpose — a single-binary, local-file, no-cloud-required KQL engine for ad-hoc analysis
— and isn't trying to compete with or replace Microsoft's own work.

## Usage

### Invocation Patterns

```bash
gokql                                    # In-memory REPL
gokql mydb.vortex                        # REPL with persistent database
gokql mydb.vortex "Logs | take 5"        # One-shot query
gokql "print 6 * 7"                      # In-memory one-shot
echo "Logs | count" | gokql mydb.vortex  # Pipe mode
gokql -mode csv "print X = 1, Y = 2"    # Output as CSV
```

### File-Based Querying

Query external files directly as table sources — the killer feature:

```kql
csv("firewall.csv") | where Action == "DENY" | summarize count() by SrcIP
json("alerts.json") | where Severity == "High" | project AlertName, Count
ndjson("events.jsonl") | summarize count() by event
parquet("export.parquet") | where Score > 90 | top 10 by Score desc
```

Schema is auto-detected from file content (integers, floats, datetimes, booleans).
All four file sources feed into the full KQL operator pipeline.

### Database Mode

For persistent storage with Vortex columnar format:

```bash
gokql security.vortex

# Create and populate tables
gokql> .create table Logs (Timestamp: datetime, SrcIP: string, Action: string, Bytes: long)
gokql> .ingest csv into table Logs from "firewall_export.csv"

# Query with full KQL
gokql> Logs | where Action == "DENY" | summarize count() by SrcIP | order by count_ desc
gokql> Logs | where Timestamp > ago(1h) | summarize sum(Bytes) by bin(Timestamp, 5m)
```

### Output Modes

```bash
gokql -mode table "..."    # Aligned columns (default)
gokql -mode csv "..."      # Comma-separated
gokql -mode tsv "..."      # Tab-separated
gokql -mode json "..."     # Pretty-printed JSON array
gokql -mode jsonl "..."    # Newline-delimited JSON
```

### REPL Commands

| Command | Description |
|---------|-------------|
| `.open [PATH]` | Switch database (no arg = in-memory) |
| `.schema [TABLE]` | Show table schema(s) |
| `.timer on/off` | Show query execution time |
| `.mode table/csv/json/jsonl/tsv` | Output format |
| `.output FILE/-` | Redirect output to file |
| `.read FILE` | Execute KQL script file |
| `.help` | Full command reference |

## KQL Coverage

### Tabular Operators (24)

`where` `project` `project-away` `project-rename` `project-reorder` `project-keep`
`extend` `take/limit` `count` `distinct` `order by/sort by` `top` `sample`
`summarize` `join` (8 kinds) `union` `mv-expand` `serialize` `print` `parse`
`getschema` `datatable`

### Scalar Functions (138)

**String:** strlen, tolower/toupper, strcat, substring, split, extract, extract_all,
replace_string, replace_regex, trim, indexof, countof, reverse, isempty/isnotempty...

**Datetime:** now, ago, datetime_add, datetime_diff, bin, format_datetime,
format_timespan, startofday/week/month/year, endofday/week/month/year,
year/month/day/hour/minute/second...

**Math:** round, abs, pow, sqrt, log/log2/log10, ceiling, exp, pi, sign, rand...

**Dynamic:** array_length, array_concat, array_slice, bag_keys, bag_pack, pack_array,
set_union/intersect/difference, treepath, parse_json...

**Network:** parse_ipv4, ipv4_is_private, ipv4_is_in_range, has_ipv4, ipv4_compare,
format_ipv4, hash_sha256/md5/sha1, new_guid, parse_url, base64_encode/decode...

**Conditionals:** iff/iif, case, coalesce, max_of, min_of, isnull/isnotnull...

**Window:** row_number, prev, next (via serialize)

### Aggregation Functions (27)

`count` `countif` `sum` `sumif` `avg` `avgif` `min` `max` `minif` `maxif`
`dcount` `dcountif` `make_set` `make_set_if` `make_list` `make_list_if`
`make_bag` `make_bag_if` `arg_max` `arg_min` `any` `percentile` `percentiles`
`stdev` `variance` `binary_all_or` `binary_all_and`

### Comparison Operators (29)

`==` `!=` `=~` `!~` `<` `<=` `>` `>=` `contains` `has` `startswith` `endswith`
(+ `_cs` and negation variants for all) `matches regex` `in` `!in` `in~` `!in~`
`has_any` `has_all` `between` `!between`

### Other Features

- `let` statements (scalar and tabular bindings)
- JSON dot-access (`col.property`, `col["key"]`)
- Subqueries (in join, union, in-operator)
- Dynamic literals, timespan literals, datetime arithmetic
- Multiple statements (semicolon-separated)

For complete coverage details, see [docs/kql_coverage.md](docs/kql_coverage.md).

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     KQL Parser (~3,100 lines)               │
│        AST, expression grammar, operator parsing            │
└──────────────────────┬──────────────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────────────┐
│                  Query Engine (~5,200 lines)                │
│    24 operators, 138 functions, 27 aggregations, 8 joins    │
│    Works entirely on in-memory *types.Table — no Vortex     │
└───────┬──────────────────────────────────┬──────────────────┘
        │                                  │
┌───────▼──────────┐              ┌────────▼─────────────────┐
│  File Sources    │              │  Vortex Storage Layer    │
│  (~780 lines)    │              │  (~1,350 lines)          │
│  csv, json,      │              │  Columnar read/write,    │
│  ndjson, parquet  │              │  zone map pruning,       │
│                  │              │  column projection       │
└──────────────────┘              └──────────────────────────┘
```

The query engine has **zero Vortex imports** — it operates entirely on generic in-memory
tables. Vortex appears only at storage boundaries. This means the engine works identically
whether data comes from CSV files, Parquet exports, or Vortex columnar storage.

## Storage: Vortex Columnar Format

For persistent databases, gokql uses **[vortex-go](https://github.com/LaurieRhodes/vortex-go)**,
a native Go implementation of the Vortex columnar format.

| Metric | Parquet | Vortex | Improvement |
|--------|---------|--------|-------------|
| File size | 45.6 MB | 19.3 MB | 58% smaller |
| Full scan | 188 ms | 40 ms | 5× faster |
| Projected scan (2/7 cols) | 188 ms | 16 ms | 12× faster |

Features: 25 encoding decoders, zone map pruning (8,192 rows/zone), column projection,
type-aware compression (FoR+BitPacking, dictionary+FSST, Pcodec, ALP).

## Building

```bash
git clone https://github.com/LaurieRhodes/gokql.git
cd gokql
go build -o gokql ./cmd/gokql/
```

Requires Go 1.24+ and local clones of
[vortex-go](https://github.com/LaurieRhodes/vortex-go) and
[parquet-go](https://github.com/parquet-go/parquet-go) as sibling directories
(configured via `replace` directives in `go.mod`).

## Example: Security Log Analysis

```kql
// Query a CSV firewall export — no database setup needed
csv("firewall_logs.csv")
| where Action == "DENY"
| extend Subnet = format_ipv4(SrcIP, 24)
| summarize
    Attempts = count(),
    TargetPorts = make_set(Port)
    by Subnet
| order by Attempts desc
| take 10
```

```kql
// Detect brute force patterns from Parquet export
parquet("auth_events.parquet")
| where EventType == "LoginFailed"
| summarize
    FailCount = count(),
    DistinctAccounts = dcount(UserName)
    by SrcIP, bin(Timestamp, 5m)
| where FailCount > 10
| order by FailCount desc
```

```kql
// Time-series anomaly detection with window functions
csv("metrics.csv")
| order by Timestamp asc
| serialize
    RowNum = row_number(),
    PrevValue = prev(Value, 1, 0)
| extend Spike = Value > PrevValue * 5
| where Spike == true
```

## Documentation

| Document | Content |
|----------|---------|
| [KQL Coverage](docs/kql_coverage.md) | Complete operator/function coverage analysis |
| [CLI Design](docs/cli_design.md) | CLI design principles and command reference |
| [KSHARD → Vortex Mapping](docs/engineering/KSHARD_TO_VORTEX_MAPPING.md) | Design decisions for storage migration |
| [KQL Storage Design](docs/engineering/KQL_STORAGE_DESIGN_AND_VORTEX_PROTOTYPE.md) | Architecture blueprint |
| [Kustainer Storage Engine](docs/engineering/KUSTAINER_STORAGE_ENGINE.md) | KSHARD forensic analysis |

## Related Projects

- **[Kusto-Query-Language](https://github.com/microsoft/Kusto-Query-Language)** — Microsoft's official, actively-maintained, Apache 2.0 licensed KQL grammar and parser; the enterprise-grade option
- **[vortex-go](https://github.com/LaurieRhodes/vortex-go)** — Native Go Vortex reader/writer
- **[Vortex](https://github.com/spiraldb/vortex)** — Rust reference implementation (Linux Foundation)
- **[kustainer-ui](https://github.com/LaurieRhodes/kustainer-ui)** — Web UI for Kusto.Personal

## Licence

[MIT](LICENSE)
