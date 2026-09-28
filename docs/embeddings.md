# Embeddings and vector similarity

This is the one part of gokql that is **not** Azure Data Explorer
parity, and the one part that talks to anything outside the process.
Both facts are deliberate and they are the same fact.

## What is parity and what is not

| Surface | Standard? | Notes |
|---|---|---|
| `series_cosine_similarity` | Yes, real ADX | Scalar function over two `dynamic` arrays |
| `series_dot_product` | Yes, real ADX | Same shape |
| `embed_text()` | **No. gokql extension** | ADX has managed embedding endpoints; this is the local equivalent |
| `.embed-into` | **No. gokql extension** | Bulk embedding of a Text column at ingest time |
| `.chunk-file` | **No. gokql extension** | Paragraph chunking, `ChunkMaxChars` default 1500 |

The KQL parity engine has no inference dependencies and never calls
out of the process. `pkg/embed` is stdlib only (`net/http`,
`encoding/json`). Adopting an in-process inference runtime was
considered and rejected: it would have put a safetensors parser, a
SIMD library and a Go version bump into the dependency tree of a query
engine, and would have put a runtime that is known to panic on
oversized inputs inside the query engine's address space.

## Choosing a backend

Configured per `Engine` via `SetEmbedder`, or per process via
`GOKQL_EMBED`. An unconfigured engine talks to Ollama on
`localhost:11434`, which is the historical default and is preserved.

| Spec | Backend |
|---|---|
| unset | Ollama, `localhost:11434`, `DefaultEmbedModel` |
| `ollama` | Same, explicit |
| `ollama:mxbai-embed-large` | Ollama, named model |
| `openai` | OpenAI-compatible, `http://localhost:8080/v1/embeddings` |
| `openai:bge-m3` | Named model, default endpoint |
| `openai:bge-m3@http://host:8080/v1/embeddings` | Named model and endpoint |
| `mock` / `mock:384` | Deterministic, no network. Tests only |

An API key is read from `GOKQL_EMBED_API_KEY` and is **never** accepted
in a spec string, because specs appear in logs, error messages and
process arguments.

### Recommended: llama-server

Lighter than Ollama for this job: one binary plus one GGUF, no model
manager, no background updater, and no keep-alive eviction to fight.

```
llama-server -m <model>.gguf --embeddings --pooling <TYPE> -c <CTX>
```

**Set `--pooling` explicitly.** It takes `none`, `mean`, `cls`, `last`
or `rank`, and if unspecified the server uses a default baked into the
GGUF at conversion time by whoever converted the file. A wrong value
returns vectors of the correct shape with the wrong meaning. Nothing
downstream can detect that: not the length check in
`cosineSimilarity`, not the vector-space guard in `.embed-into`, not a
query. The only defence is `TestLiveSemanticOrdering`.

**Match `-c` to the model's trained context length.** Setting it higher
does not extend the model; setting it lower silently truncates inputs.

A pooling-based embedding model cannot also serve chat completions on
the same process. Embeddings is a dedicated second process.

## Validated against a real server (2026-09-28)

Run on a Ryzen AI 7 PRO 350, CPU only, llama-server b11223 with
`nomic-embed-text-v1.5.f16.gguf`:

```
llama-server -m nomic-embed-text-v1.5.f16.gguf \
  --embeddings --pooling mean -c 2048 --ubatch-size 2048 --batch-size 2048 \
  --host 127.0.0.1 --port 8080 -t 8
```

- All five live checks pass. Semantic separation: related 0.698,
  unrelated 0.324.
- 281 chunks of this repo's own docs embedded in **33.5 s** (~8.4
  chunks/s, 15 batch calls), 768 dimensions.
- Whole database on disk: **1.5 MB**.
- Semantic query over 281 vectors: **0.08 s** end to end, including
  embedding the query string.
- Re-running the same `.embed-into`: **0.01 s**, 281 rows skipped.

`--ubatch-size` matters as much as `--pooling`. Left at the default
512, an input of 1,612 tokens returns a 500 with "too large to
process". That is the PHYSICAL batch size, and it must be at least
your largest chunk in tokens.

## Validate before trusting

Every test in `pkg/embed/openai_test.go` runs against a fake server
written alongside the client, so it proves self-consistency, not
correctness. Run the live harness against the real server once per
backend or model change:

```
GOKQL_LIVE_ENDPOINT=http://localhost:8080/v1/embeddings \
GOKQL_LIVE_MODEL=<name> \
go test -tags live ./pkg/embed/ -v
```

It checks that the server answers, that related text outranks
unrelated text (the `--pooling` check), that identical inputs give
identical vectors, that batched and single-input vectors agree (or
`.embed-into` and `embed_text()` produce incomparable results), and
that long inputs are not silently truncated.

## Things that will bite you

**Mixing vector spaces.** Two models of the *same width* produce
vectors that compare without error and rank nonsense.
`cosineSimilarity` only catches a width mismatch. `.embed-into`
therefore records the model that produced each vector and refuses to
append vectors from one model to a table holding another's. That guard
is bypassed if the query projects an explicit `Model` column, on the
basis that a caller who states the model per row has made the claim
deliberately.

**Constant arguments in a per-row context.** KQL evaluates scalar
functions once per row. `embed_text("my query")` inside an `extend`
over a 10,000-row table is 10,000 evaluations of the same constant.
A statement-scoped memo collapses this to one call per distinct
string; this was measured at 10 HTTP calls for a 10-row search before
the memo existed. The memo does not help a *column* argument, which is
what `.embed-into` is for.

**Concurrent engines with different backends.** Not supported. The
scalar path reaches its backend through a process-wide reference
counted scope, because `evalFunc` has no `Engine` receiver. Many
engines sharing one embedder is fine. Two *different* embedders live at
once returns `ErrConcurrentEmbedders` rather than silently using the
wrong one. The durable fix is an evaluation context threaded through
`evalExpr`'s 319 call sites.

**`Id` must be unique across the whole source query.** `.chunk-file`
numbers chunks `c001..cNNN` PER FILE, so Ids collide across files:
six of this repo's docs produced 281 chunks with only 123 distinct
Ids. Duplicate Ids break two things silently. The embeddings table is
joined back on Id, so a duplicated key turns a `top 3` into a partial
cross product (observed: three copies of one chunk's score attached to
unrelated text). And resumption skips Ids already present, so a
re-run after a partial failure would skip genuinely different chunks
that merely share an Id, losing them with no error. `.embed-into`
now refuses a source with duplicate Ids. Make them unique in the
query:

```
.embed-into E <| Chunks | extend Id = strcat(Provenance, ":", Id) | project Id, Text, Provenance
```

Whether `.chunk-file` should emit globally unique Ids itself is an
open question; it would be a behaviour change to that command.

**The `Model` column means two things.** In `.embed-into` it is read
as an INPUT ("use this model for this row") and written as an OUTPUT
("this model produced this vector"). A backend that cannot switch
models per call rejects the input reading, so the two meanings are not
separable today. Unresolved design question.

**`.embed-into` is all-or-nothing and not resumable.** A failure at row
99,000 of 100,000 discards everything, and the engine passes
`context.Background()`, so it cannot be interrupted either. Fine at
pilot scale, not at corpus scale. Known gap.

**Storage encoding.** Stored vectors use a compact form:
float32 little-endian, base64, behind a `kql.vec32:` prefix (the same
convention as `kql.dictref`). At 768 dimensions that is 4,106 bytes
against 15,077 for the old JSON array, and decoding is **35x faster**
(3.9 us vs 135 us per vector; 1,048 MB/s vs 111 MB/s). Decoding was
about 90% of the cost of a similarity ranking, so this is the single
largest performance change in this extension.

float32 is not a precision compromise: every backend in use produces
float32 internally and widens it on output. Storing float64 preserved
a precision the data never had.

**No migration is required.** Reading accepts both forms, so tables
embedded before this change keep working and a table with a mix of old
and new extents ranks correctly. `VectorEncoding = "json"` restores the
old write format if you need to hand a table to something that parses
the column itself.

`embed_text()` still returns JSON. Its result is a transient
query-time value that callers routinely inspect, and it is now
computed once per statement rather than once per row, so its encoding
is not a performance concern.

## No evaluation set exists

There is currently no way to tell whether a model or backend change
made retrieval *worse*. It produces no error, just quietly worse
answers. Before changing either, build a small labelled set of
query-to-expected-chunk pairs and measure recall@5. This is the
largest unmanaged risk in this extension.
