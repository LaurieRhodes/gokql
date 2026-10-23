# Embeddings, semantic search and AI calls

gokql can turn text into embedding vectors, store them in tables, and
rank rows by similarity, so semantic search joins and filters like any
other query. It can also call language models row by row. These are
the only features that talk to anything outside the process, and each
is either explicitly configured or allowed by the
[callout policy](security.md).

| Surface | Standard KQL? | What it does |
|---|---|---|
| `series_cosine_similarity(a, b)`, `series_dot_product(a, b)` | Yes | Compare two vectors |
| `evaluate ai_embeddings(Text, ConnectionString)` | Yes | Embed a column through an endpoint, or gokql's embedder |
| `evaluate ai_chat_completion(Messages, ConnectionString)` / `ai_chat_completion_prompt(Prompt, ...)` | Yes | Ask a chat model, once per row |
| `embed_text("...")` | gokql | Embed one string with gokql's embedder |
| `.embed-into T <\| query` | gokql | Embed a query's `Text` column in bulk into table `T`, resumably |
| `.chunk-file "path" into T` | gokql | Split a document into paragraph chunks with headings and line numbers |

## Choosing an embedding backend

gokql's embedder is configured with `GOKQL_EMBED` (or by a host program
through `Engine.SetEmbedder`). Unset, it uses Ollama on
`localhost:11434`.

| `GOKQL_EMBED` | Backend |
|---|---|
| unset or `ollama` | Ollama, `localhost:11434`, the default model |
| `ollama:mxbai-embed-large` | Ollama, a named model |
| `openai` | OpenAI-compatible endpoint, `http://localhost:8080/v1/embeddings` |
| `openai:bge-m3@http://host:8080/v1/embeddings` | A named model and endpoint |
| `mock` / `mock:384` | Deterministic vectors, no network (tests) |

An API key comes from `GOKQL_EMBED_API_KEY` and is never accepted in
the spec, because specs appear in logs and process listings.
`gokql -embed-check` probes the backend; `gokql -embed-setup` writes
(but does not run) a setup script for a local
[llama-server](https://github.com/ggml-org/llama.cpp).

With llama-server, set `--pooling` explicitly (`mean` for most
embedding models) and `-c` to the model's context length. A wrong
pooling mode returns vectors of the right shape and the wrong meaning,
which nothing downstream can detect; `--ubatch-size` must be at least
your largest chunk in tokens.

## Building a searchable corpus

```kql no-run
.chunk-file "./notes/fieldwork.md" into Chunks
```

```kql no-run
.embed-into ChunkEmbeddings <| Chunks | project Id, Text, Provenance
```

`.chunk-file` writes one row per paragraph with `Id`
(`<file stem>:c<NNN>`, or `with (idprefix="key")`), `Text`,
`HeadingTrail`, `StartLine`, `EndLine` and `Provenance`. `.embed-into`
embeds the `Text` column and writes `Id`, `Embedding`, `Model` and the
other columns given. It commits in chunks of 500 rows, so an
interrupted run keeps its progress, and re-running the same command
skips Ids already embedded.

Its guards are deliberate:

- **One model per table.** Vectors from two models of the same width
  compare without error and rank nonsense, so the table records the
  model that produced each vector and refuses vectors from another.
  Choose a model with `with (model="nomic-embed-text")`.
- **Unique Ids.** The source must not repeat an `Id`; results are
  joined back on it and resumption skips by it.

## Searching

```kql no-run
let q = embed_text("which gate alignments were measured?");
ChunkEmbeddings
| extend Score = series_cosine_similarity(Embedding, q)
| top 5 by Score desc
| join kind=inner (Chunks) on Id
| project Score, HeadingTrail, Text
```

Filter before ranking (by project, date, source) to search a subset.
`embed_text` is evaluated once per statement for a constant argument,
not once per row. Measure a similarity threshold for your model and
corpus before relying on one: scores are relative.

`gokql -db DIR -embed-eval cases.json` measures retrieval quality
(recall@K and MRR) against a set of hand-written queries whose
relevant text is given as substrings, and exits non-zero below
`-embed-eval-min`, so a model or chunking change can be gated in CI.

## ai_embeddings

The ADX plugin embeds a column through an OpenAI-compatible endpoint,
or through gokql's embedder when the connection string is empty:

```kql no-run
Chunks
| take 100
| evaluate ai_embeddings(Text, "https://myai.openai.azure.com/openai/deployments/text-embedding-3-small/embeddings?api-version=2024-06-01;managed_identity=system")
```

The result adds `Text_embeddings` (and `Text_embeddings_error` with
`IncludeErrorMessages`). The `;...` authentication part is accepted
and ignored; the key comes from `GOKQL_AI_API_KEY` (sent as `api-key`
to `*.azure.com`, as a bearer token elsewhere). The endpoint must be
allowed by an `azure_openai` callout rule. Options:
`RecordsPerRequest` (batching), `ModelParameters` and
`ReturnSuccessfulOnly`.

## ai_chat_completion

```kql no-run
Findings
| take 20
| extend Prompt = strcat("Classify this claim as measurement, inference or speculation. Answer with one word.\n\n", Claim)
| evaluate ai_chat_completion_prompt(Prompt, "http://localhost:11434/v1/chat/completions", dynamic({"ModelParameters": {"model": "llama3.1", "temperature": 0}}))
| project Id, Claim, Kind = Prompt_completion
```

`ai_chat_completion` takes a `dynamic` array of `{role, content}`
messages per row; `ai_chat_completion_prompt` takes a string. Any
OpenAI-compatible chat endpoint works: Azure OpenAI, OpenAI, Ollama's
`/v1`, llama-server, LM Studio. Each row is one request, so filter and
`take` first. A failed call leaves the value null (with
`IncludeErrorMessages`, the error is in `<column>_completion_error`).

## See also

- [Security](security.md): the callout policy and API keys
- [Agent memory](../use-cases/agent-memory.md): semantic recall over findings
- [Plugins reference](../reference/plugins.md)
