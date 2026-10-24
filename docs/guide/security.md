# Security

<!-- gokql:setup
@config {"callouts": [{"CalloutType": "webapi", "CalloutUriRegex": "^https://prices\\.azure\\.com/", "CanCall": true}]}
-->

gokql is a local engine. Its safety model is about what a query, and
the person or model writing it, can do: change data, reach the
network, or reach other databases. This page lists each boundary and
how to set it.

## Read-only access

`-readonly` refuses every write command (`.set-or-append`, `.drop`,
`.create...`, `.compact`, ...) and allows queries and `.show`
commands. Use it for any agent or user that should only look:

```bash
gokql -db ./scope -readonly -mode jsonl -e 'Findings | take 5'
gokql -db ./scope -readonly -e '.drop table Findings'
# Error: the database is open read-only: write commands are refused
```

To make a database read-only whoever opens it, set it in its
[`gokql.yaml`](../reference/configuration.md):

```yaml
readonly: true
```

A server can also be read-only for its clients while the database
stays writable locally (`server.readonly`, [serving](serving.md)).
Databases reached through the [local cluster](cluster.md) are always
read-only from the database that lists them.

Read-only stops writes to the database. It does not stop a query from
reading: `csv()`, `json()`, `externaldata` and the other file sources
can read any file the gokql process can, and `externaldata` over http
is allowed by default (below). For an agent or a server whose queries
you do not fully control, run gokql as a user whose file access is
limited to what queries may see, and deny `external_data` callouts:

```yaml
readonly: true
callouts:
  - CalloutType: external_data   # no fetching from anywhere
    CalloutUriRegex: .*
    CanCall: false
```

## Outbound calls: the callout policy

Queries reach outside the process only through:

| Feature | Callout type |
|---|---|
| `http_request`, `http_request_post` | `webapi` |
| `ai_embeddings`, `ai_chat_completion`, `ai_chat_completion_prompt` with an endpoint URI | `azure_openai` |
| `externaldata` and `infer_storage_schema` over http(s) | `external_data` |

The policy uses ADX's rule format and lives under `callouts` in the
`gokql.yaml` of the database you open:

```yaml
callouts:
  - CalloutType: webapi            # http_request to the Azure prices API
    CalloutUriRegex: ^https://prices\.azure\.com/
    CanCall: true
  - CalloutType: azure_openai      # a local model for ai_chat_completion
    CalloutUriRegex: ^http://localhost:11434/
    CanCall: true
  - CalloutType: external_data     # never fetch from the intranet
    CalloutUriRegex: ^https://internal\.example/
    CanCall: false
```

- A call is allowed when a rule of its type matches the URI and no
  matching rule says `"CanCall": false`. A denying match always wins.
- With no matching rule, `webapi` and `azure_openai` calls are refused,
  with an error naming the rule to add.
- `external_data` has ADX's predefined rule `.*` (allowed), so
  `externaldata` over HTTP works unless a rule denies it. Add a denying
  rule (`CalloutType: external_data`, `CalloutUriRegex: .*`,
  `CanCall: false`) to close it.
- `.show cluster policy callout` lists the policy, below. `.alter`,
  `.alter-merge` and `.delete cluster policy callout` are refused: the
  policy is the operator's file, and no query, including one written
  by a model, can widen what it may reach.

With the first rule above in place:

```kql
.show cluster policy callout
| project EntityType, Policy
```

```output
EntityType                Policy
──────────                ──────
Cluster                   [{"CalloutType":"webapi","CalloutUriRegex":"^https://prices\\.azure\\.com/","CanCall":true}]
Cluster immutable policy  [{"CalloutType":"external_data","CalloutUriRegex":".*","CanCall":true}]
(2 rows)
```

Local files read by `csv()`, `json()`, `externaldata` and the like are
not covered by the policy: anything the gokql process can read, a
query can read.

## Keys and secrets

API keys are read only from the environment, never from query text,
connection strings or embedder specs, which end up in logs, histories
and process listings:

| Variable | Used by |
|---|---|
| `GOKQL_AI_API_KEY` | `ai_embeddings` and `ai_chat_completion*` endpoints |
| `GOKQL_EMBED_API_KEY` | gokql's own embedder (`embed_text`, `.embed-into`) |

A key is sent as `api-key` to `*.azure.com` hosts and as a bearer
token elsewhere. HTTP headers passed to `http_request` are part of
the query, so do not put secrets in queries others can see. HTTP (not
HTTPS) requests carrying an `Authorization` header are refused.

## Serving

`gokql -serve` has no authentication, like the Kusto emulator whose
protocol it speaks. Every client can run any query the server process
could: read local files through the file sources, and fetch URLs
through `externaldata` unless the callout policy denies it. So:

- It listens on `127.0.0.1:8080` (this machine) unless told otherwise;
  widen `-addr` only to a trusted network.
- Use `-readonly` or `server.readonly: true` where clients should only
  query (both refuse writes from clients; the top-level `readonly`
  also refuses them from the local CLI).
- Deny `external_data` callouts and run the server as a user with
  access only to its data, as above.
- Use TLS (`server.tls` in `gokql.yaml`) if it crosses a network, and
  put an authenticating proxy in front of it for anything wider.

See [serving](serving.md).

## Multiple writers

Several processes may write to one database safely
([storage](storage.md)). There is no per-table access control: a
process that can write, can write any table. Give writers separate
databases, joined read-only through the [local cluster](cluster.md),
when they should not touch each other's data.

## See also

- [Embeddings and AI](embeddings-and-ai.md)
- [Files and external data](files-and-external-data.md)
