# Command line

```
gokql [flags] [DB-DIR] [QUERY]
```

| Invocation | Does |
|---|---|
| `gokql` | REPL on a temporary in-memory database |
| `gokql ./scope` | REPL on a database directory (created on first write) |
| `gokql ./scope 'T \| count'` | Run one query or command and exit |
| `gokql 'print 6 * 7'` | One query against an in-memory database |
| `gokql -db ./scope -e 'T \| count'` | The same with flags |
| `echo 'T \| count' \| gokql -db ./scope` | Run the statements on stdin and exit |

A single positional argument is taken as a query if it looks like one
(contains `|`, starts with a command or `print`, ...), otherwise as a
database directory.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `-db DIR` | | Database directory |
| `-e QUERY` | | Run one query or command and exit |
| `-mode M` | `table` | Output: `table`, `csv`, `tsv`, `json`, `jsonl` |
| `-readonly` | false | Refuse every write command |
| `-no-rc` | false | Do not read `~/.gokqlrc` |
| `-v` | false | Print scan diagnostics to stderr |
| `-version` | | Print the version and build (VCS revision) and exit |
| `-serve` | false | Serve `-db` over HTTP ([serving](../guide/serving.md)) |
| `-addr ADDR` | `:8080` | Listen address for `-serve` (overrides `server.addr`) |
| `-dbname NAME` | directory name | Database name clients send to `-serve` (overrides `server.name`) |
| `-write-config` | | Write `gokql.yaml` into `-db` (a template, or converted from older JSON files) and exit |
| `-embed-check` | | Probe the embedding backend and report whether it is ready, then exit |
| `-embed-setup` | | Write a setup script for a local embedding server, then exit (writes only; runs nothing) |
| `-embed-setup-path FILE` | `setup-embeddings.sh` | Where `-embed-setup` writes |
| `-embed-eval FILE` | | Run a retrieval-quality eval set against `-db`, report recall@K and MRR, then exit |
| `-embed-eval-table T` | `E` | Embeddings table for `-embed-eval` |
| `-embed-eval-chunks QUERY` | `Chunks \| project Id, Text` | Subquery giving Id and Text for `-embed-eval` |
| `-embed-eval-min X` | `0.8` | Minimum recall@K for `-embed-eval` to exit 0 |
| `-discover` | | Accepted for old scripts; has no effect |

## Output and exit status

- Results go to stdout in the chosen mode. `json` is one array per
  result; `jsonl` one object per row. Keys follow the column order, and
  `dynamic` values are JSON, not escaped strings.
- A statement that returns several tables (`fork`, `facet`, batches)
  prints each, headed by its name.
- Errors go to stderr as one line, `Error: ...`.
- The exit status is 0 on success and 1 if the query, or any statement
  read from stdin, failed.

## Statements on stdin and in files

A statement may span lines. A line continues the statement before it
when that statement has an open bracket, string or ```` ``` ```` block,
or ends with `|`, `,` or `;`, or when the line starts with `|`, `[`,
`(`, `,`, `)`, `]` or `}`. A blank line ends a statement, as does any
other line. A statement that starts with `#crp` lines (GQL or
openCypher) runs to the next blank line. Whole-line `//` comments are
skipped. `.read FILE` in the REPL follows the same rules.

## REPL

The REPL accepts KQL, management commands and:

| Command | Does |
|---|---|
| `.help` | List the management commands (also a queryable table) |
| `.schema [T]` | Show table schemas |
| `.mode M` | Change the output mode |
| `.timer on\|off` | Show each statement's elapsed time |
| `.output FILE` / `.output -` | Send results to a file / back to the terminal |
| `.read FILE` | Run the statements in a file |
| `.open [DIR]` | Switch database (none: in-memory) |
| `.version` | Show the version |
| `.quit` | Exit |

`~/.gokqlrc` may hold `.mode` and `.timer` lines, applied at start-up.

## Environment

| Variable | Meaning |
|---|---|
| `GOKQL_EMBED` | Embedding backend ([embeddings and AI](../guide/embeddings-and-ai.md)) |
| `GOKQL_EMBED_API_KEY` | API key for that backend |
| `GOKQL_AI_API_KEY` | API key for `ai_embeddings` and `ai_chat_completion*` endpoints |
