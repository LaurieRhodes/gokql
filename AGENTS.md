# Working on gokql

Guidance for people and coding agents changing this repository. For
*using* gokql, see [docs/for-agents.md](docs/for-agents.md).

## Build and test

```bash
# vortex-go must be checked out beside this repository (go.mod replace ../vortex-go)
go build -o gokql ./cmd/gokql
gofmt -l pkg cmd            # must print nothing
go vet ./...
go test ./... -race -count=1 -timeout 900s -skip TestPagePoolStress
```

`TestPagePoolStress` is a long stress test; run it on its own when
touching `pkg/catalog` paging.

## Layout

| Path | Contents |
|---|---|
| `cmd/gokql` | CLI, REPL, stdin scripts, `-serve` HTTP server |
| `pkg/parser` | KQL parser: statements, operators, expressions, commands |
| `pkg/engine` | Execution: operators, functions, aggregations, plugins, commands, cluster layer, callouts |
| `pkg/catalog` | Database discovery from extent files, extent I/O |
| `pkg/types` | Values, types, formatting |
| `pkg/config` | `gokql.yaml` |
| `pkg/arrowipc` | Apache Arrow IPC reader and writer (`arrow()`, `-mode arrow`) |
| `pkg/gql` | GQL and openCypher, translated to `graph-match` |
| `pkg/s2x`, `pkg/h3` | Sphere geometry (S2 shape algebra) and H3 cells |
| `pkg/embed` | Embedding backends (Ollama, OpenAI-compatible) |
| `docs/` | Documentation (see below) |
| `testdata/docs/` | Sample files used by documentation examples |
| `tools/gen-reference` | Extracts the KQL catalogue from Microsoft's docs |

[docs/dev/architecture.md](docs/dev/architecture.md) explains how a
query flows through these.

## Rules

- **ADX semantics are the specification.** Check behaviour against
  Microsoft's documentation examples and, where they are silent or
  wrong, against the Kusto emulator; record which one a test follows.
  Exact agreement is not required where ADX leaves a result
  unspecified (row order, sketch internals), but say so in the code.
- **Do not invent syntax.** Extensions are management commands or
  functions with distinct names, and are documented as gokql additions.
- **Every change keeps the docs true.** Two tests enforce it:
  - `go test ./pkg/engine -run TestReferenceDocs` fails when
    `docs/reference/` no longer matches the engine. Regenerate with
    `-update-reference`; add a note to `docs/reference/notes.json` when
    gokql differs from ADX.
  - `go test ./cmd/gokql -run TestDocsExamples` runs every `kql` block
    in `README.md` and `docs/` and compares its output. After changing
    behaviour or an example, refresh with `-update-docs` and review
    the diff.
- **New flags** go in `docs/reference/cli.md`; new `gokql.yaml` fields
  in `config.Template` and `docs/reference/configuration.md` (both
  checked by tests). New dot-commands go in `.help`
  (`pkg/engine/help.go`).
- **Storage is append-only.** Never rewrite an extent in place; new
  data is a new file committed by rename.
- The Kusto emulator's licence forbids benchmarking: do not publish
  timings measured against it.

## Documentation layout

| Path | For |
|---|---|
| `README.md` | First contact: what gokql is, use cases, install |
| `docs/getting-started.md` | First session |
| `docs/guide/` | One page per capability |
| `docs/use-cases/` | Worked scenarios |
| `docs/reference/` | Generated and checked reference |
| `docs/for-agents.md`, `llms.txt` | Compact guidance for language models |
| `docs/compatibility.md` | How parity is measured, known differences |
| `docs/dev/` | Architecture, contributing; `history/` holds old sprint logs and backlogs |

Write documentation in plain, precise English: what a thing does,
how to use it, where it differs from ADX. Examples must run.
