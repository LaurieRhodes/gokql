# CLI Design: okql

## Design Principles

1. **Zero-friction start** — `okql` with no args must do something useful
2. **DuckDB-style positional args** — database path is positional, not a flag
3. **Files are first-class data sources** — query CSV/JSON/Parquet directly
4. **KQL stays KQL** — don't invent new syntax; extend via dot-commands
5. **Progressive disclosure** — simple things are simple, complex things are possible

---

## Invocation

```
okql [OPTIONS] [DATABASE] [QUERY]
```

### Examples (proposed)

```bash
# Interactive REPL, in-memory (no persistence)
okql

# Interactive REPL, persistent database (created if not exists)
okql ./security.db

# One-shot query on persistent database
okql ./security.db "Logs | where Score > 80 | count"

# One-shot query, in-memory (useful with file-based sources)
okql "print Answer = 6 * 7"

# Pipe mode
cat queries.kql | okql ./security.db

# Current -e flag still works (backward compat)
okql -db ./security.db -e "Logs | take 5"

# Read-only mode
okql -readonly ./security.db
```

### Argument Resolution

1. If first non-flag arg looks like a path (contains `/`, `\`, or `.vortex`/`.db`/`.okql`/`.duckdb` extension), treat as DATABASE
2. If first non-flag arg looks like KQL (contains `|`, starts with `.`, or known table name), treat as QUERY
3. If two positional args: first is DATABASE, second is QUERY
4. If no positional args: in-memory REPL

This mimics DuckDB's `duckdb [database] [query]` pattern.

---

## File-Based Querying (The Big Feature)

### Direct CSV querying

The killer UX for a security analyst:

```kql
// Query a CSV file directly as a table source — no ingest step
csv("./firewall_logs.csv") | where Action == "DENY" | summarize count() by SrcIP

// Schema detection happens automatically
csv("./events.csv") | getschema

// KQL-style: file path as table name (auto-detected by extension)
"./firewall_logs.csv" | where Action == "DENY" | count
```

### Direct JSON querying

```kql
json("./alerts.json") | where Severity == "High" | project AlertName, TimeGenerated

// NDJSON (newline-delimited) — common for log exports
ndjson("./export.jsonl") | take 10
```

### Future: Parquet and Vortex direct access

```kql
// Parquet — standard columnar format for analytics exports
parquet("./data.parquet") | where Region == "APAC" | summarize count() by Country
parquet("./data.parquet") | getschema

// Vortex files directly (without a database)
vortex("./extent_00001.vtx") | take 10
```

### Implementation approach

These aren't new operators — they're **table-valued functions** that return
a `*types.Table`. The parser sees `csv("path")` as a function call in the
source position. The engine resolves it by:

1. Reading the file
2. Auto-detecting schema (for CSV: sniff headers + sample rows)
3. Returning an in-memory Table that flows into the normal operator pipeline

This is architecturally identical to how DuckDB's `read_csv()` works —
it's a table function, not special syntax.

---

## REPL Dot-Commands

### Current (keep)
```
.help                              Show help
.quit / .exit                      Exit REPL
.create table T (...)              Create table
.create-merge table T (...)        Create or add columns
.drop table T                      Drop table
.drop extent <id>                  Drop extent
.show tables                       List tables
.show table T extents              Show extents
.show database                     Database info
.ingest csv into table T from "f"  Ingest CSV file
.ingest inline into table T <|     Inline data
```

### New (add)
```
.open [PATH]                       Switch database (no arg = in-memory)
.open -readonly PATH               Open read-only
.timer [on|off]                    Show query execution time
.mode [table|csv|json|jsonl|tsv]   Output format (default: table)
.output [FILE|-]                   Redirect output to file (- = stdout)
.read FILE                         Execute commands from a KQL script file
.schema [TABLE]                    Quick schema view
.version                           Show version info
```

### Output modes

| Mode | Use case |
|------|----------|
| table | Default — human-readable aligned columns |
| csv | Pipe to other tools, import to Excel |
| json | Array of objects — programmatic consumption |
| jsonl | Newline-delimited JSON — streaming/log pipelines |
| tsv | Tab-separated — Excel paste-friendly |

---

## Database Conventions

### File extension

Use `.vortex` as the recommended database extension — it names the actual
underlying storage format (Vortex columnar), not the CLI tool
(`okql`, unaffected by the 2026-08-15 project rename to gokql, kept
deliberately since it's the short command people type daily). `.okql` is
still recognized for backward compatibility, but is no longer the suggested
default: it now reads as ambiguous between the CLI binary name and the old
project name, neither of which describes the actual on-disk format the way
`.vortex` does. `.db` is still avoided as the default for the same reason as
before (ambiguous with every other embedded database format):

```bash
okql security.vortex              # Names the actual on-disk format
okql ./logs.vortex "Logs | count" # One-shot
```

But accept any extension — the database is a directory containing:
```
security.vortex/
  catalog.json          # Table definitions, extent metadata
  extents/
    Logs_00000001.vtx   # Vortex columnar extent files
    Logs_00000002.vtx
```

### In-memory mode

When no database path is given, okql operates purely in-memory:
- Tables created with `.create table` exist only for the session
- CSV/JSON file queries work (data loaded transiently)
- On exit, nothing is persisted
- Startup message makes this clear:

```
gokql v0.x.x
Connected to a transient in-memory database.
Use .open PATH to connect to a persistent database.

okql>
```

### Persistent mode

When a database path is given:

```
gokql v0.x.x
Database: ./security.vortex (3 tables, 47 extents)

okql>
```

---

## Config File (~/.okqlrc)

Executed on REPL startup (not in -e or pipe mode). Contains dot-commands:

```
.mode table
.timer on
```

---

## CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| (positional 1) | — | Database path OR query |
| (positional 2) | — | Query (if positional 1 is database) |
| -e QUERY | — | Execute query and exit (backward compat) |
| -db PATH | — | Database path (backward compat) |
| -readonly | false | Open database read-only |
| -mode MODE | table | Output mode: table, csv, json, jsonl, tsv |
| -v | false | Verbose: show scan diagnostics |
| -version | — | Print version and exit |
| -help | — | Print help and exit |
| -no-rc | false | Don't load ~/.okqlrc |

---

## Output Format: Table Mode Improvements

Current table output is functional but basic. Future improvements:

1. **Column type hints** in header (like DuckDB's duckbox mode)
2. **Truncation** for long string columns (with ... indicator)
3. **Row count** always shown
4. **Null rendering** — show `null` instead of empty string
5. **Datetime formatting** — ISO 8601 by default, compact for columns

---

## Migration Path

The current `-db` flag continues to work. The new positional args
are additive. Nothing breaks.

Phase 1: Positional args, .timer, .mode, .output, .read, .version
Phase 2: csv() / json() / ndjson() / parquet() table functions for file-based querying
Phase 3: .open for database switching, ~/.okqlrc
Phase 4: Excel support (optional dependency) — built, then deliberately removed 2026-08-15 (non-columnar, unused in practice, real dependency footprint for no active benefit; see docs/kql_coverage.md)

---

## Comparison with DuckDB

| Aspect | DuckDB | okql (proposed) |
|--------|--------|-----------------|
| Default | In-memory | In-memory |
| Positional args | `duckdb [db] [query]` | `okql [db] [query]` |
| File query | `SELECT * FROM 'f.csv'` | `csv("f.csv") \| take 10` |
| Dot commands | SQLite-style | KQL management commands + extensions |
| Output modes | .mode (many) | .mode table/csv/json/jsonl/tsv |
| RC file | ~/.duckdbrc | ~/.okqlrc |
| Read-only | -readonly | -readonly |
| Timer | .timer on | .timer on |
| Language | SQL | KQL (SQL layer future) |
| Storage | DuckDB format | Vortex columnar |
| External files | Parquet, CSV, JSON, Iceberg | CSV, JSON, NDJSON, Parquet |

