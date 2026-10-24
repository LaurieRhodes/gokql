# gokql documentation

Start with [getting started](getting-started.md). Every KQL example in
these pages that is not marked as needing a network or your own files
is run by the test suite (`cmd/gokql/docs_examples_test.go`), and the
output shown is what gokql prints.

## Use cases

| Page | For |
|---|---|
| [Agent memory](use-cases/agent-memory.md) | Giving an LLM agent a durable store: schema, write discipline, current-state functions, provenance, semantic recall, a ready-made skill |
| [Security data triage](use-cases/security-triage.md) | Hunting and summarising exported sign-in logs, cloud findings and alerts |
| [KQL development](use-cases/kql-development.md) | Writing and testing queries, functions and update policies before they reach ADX |
| [Research knowledge graphs](use-cases/research-graphs.md) | Claims linked to evidence, competing hypotheses, corrections that cascade |
| [A local cluster](use-cases/local-cluster.md) | Specialised databases kept apart and queried together |

## Guides

| Guide | Covers |
|---|---|
| [Storage](guide/storage.md) | What is on disk, history, concurrency, compaction, dropping |
| [Writing data](guide/writing-data.md) | Every write command, current state, update policies, materialized views, stored functions |
| [Files and external data](guide/files-and-external-data.md) | CSV, JSON and Parquet as tables, `externaldata`, `infer_storage_schema`, `http_request` |
| [The local cluster](guide/cluster.md) | `gokql.yaml`, `database()`, `cluster()`, aliases, `macro-expand` |
| [Graphs](guide/graphs.md) | `make-graph`, `graph-match`, graph models and snapshots, GQL and openCypher |
| [Geospatial](guide/geospatial.md) | Distances, containment, shapes, geohash/S2/H3 cells, lookups |
| [Embeddings and AI](guide/embeddings-and-ai.md) | Embedding backends, chunking, vector search, `ai_embeddings`, `ai_chat_completion` |
| [Security](guide/security.md) | Read-only access, the callout policy, keys, serving safely |
| [Serving](guide/serving.md) | The HTTP endpoint, read-only serving, several databases |

## Reference

- [Reference index](reference/README.md): operators, functions, aggregations, plugins, commands and CLI, generated from the engine
- [Configuration](reference/configuration.md): `gokql.yaml`
- [Compatibility with ADX](compatibility.md)
- [For AI agents](for-agents.md) and [llms.txt](../llms.txt)

## Development

- [Development](dev/README.md): architecture, building, testing, contributing
- [History](dev/history/): the sprint logs and backlogs this documentation replaced
