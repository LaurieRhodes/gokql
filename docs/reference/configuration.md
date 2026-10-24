# Configuration: gokql.yaml

A database is configured by an optional `gokql.yaml` in its directory.
Every field is optional, and a database without the file behaves as
described throughout these docs. Unknown fields are errors, so a typo
is reported rather than ignored.

`gokql -db DIR -write-config` writes a commented template, or converts
the JSON files of older gokql versions (`.okql-federation.json`,
`.okql-server.json`, still read when `gokql.yaml` is absent, with a
warning).

## Template

This is the file `-write-config` writes; uncomment what you need.

```yaml
# gokql.yaml: configuration of this database. Every field is optional;
# delete what you do not need. Reference:
# https://github.com/LaurieRhodes/gokql/blob/main/docs/reference/configuration.md

# This cluster's name, as cluster("...") and current_cluster_endpoint() see it.
# cluster: lab

# What current_principal() reports.
# principal: analyst

# Refuse every write command, from the CLI and from -serve alike.
# readonly: false

# Other databases of this cluster, read-only from here: database("stars").T
# databases:
#   - name: stars
#     path: /data/scopes/stars

# Other clusters, by their primary database: cluster("archive").database("...").T
# clusters:
#   - name: archive
#     path: /data/archive/primary

# The callout policy, in ADX's form: which URLs http_request (webapi),
# the ai_* plugins (azure_openai) and externaldata over http (external_data)
# may reach. A matching rule with CanCall: false always wins.
# callouts:
#   - CalloutType: webapi
#     CalloutUriRegex: ^https://prices\.azure\.com/
#     CanCall: true

# gokql -serve settings. Flags given on the command line take precedence.
# server:
#   addr: 127.0.0.1:8080
#   name: notes            # the database name clients send (default: the directory's name)
#   readonly: true         # refuse write commands from clients
#   tls:
#     cert: /etc/gokql/cert.pem
#     key: /etc/gokql/key.pem
#   databases:             # more databases served from this endpoint
#     - name: handbook
#       path: /data/handbook
#       readonly: true
```

## Fields

| Field | Meaning | Guide |
|---|---|---|
| `cluster` | This cluster's name for `cluster("...")` and `current_cluster_endpoint()`; default `localhost` | [cluster](../guide/cluster.md) |
| `principal` | What `current_principal()` reports | [cluster](../guide/cluster.md) |
| `readonly` | Refuse every write command to this database, from any client | [security](../guide/security.md) |
| `databases` | Other databases of this cluster (`name`, absolute `path`), read-only from here | [cluster](../guide/cluster.md) |
| `clusters` | Other clusters, each by its primary database (`name`, `path`) | [cluster](../guide/cluster.md) |
| `callouts` | The callout policy: `CalloutType`, `CalloutUriRegex`, `CanCall`, as in ADX | [security](../guide/security.md) |
| `server.addr` | Listen address for `-serve`; default `127.0.0.1:8080` (this machine only) | [serving](../guide/serving.md) |
| `server.name` | Database name clients send; default the directory's name | [serving](../guide/serving.md) |
| `server.readonly` | Refuse write commands from clients | [serving](../guide/serving.md) |
| `server.tls.cert`, `server.tls.key` | PEM files; serve HTTPS | [serving](../guide/serving.md) |
| `server.databases` | More databases served from the same endpoint (`name`, `path`, `readonly`) | [serving](../guide/serving.md) |

Command-line flags take precedence over the file (`-addr`, `-dbname`);
`-readonly` adds to it (either makes the database read-only).

## Other files

| File | Purpose |
|---|---|
| `~/.gokqlrc` | REPL start-up: `.mode` and `.timer` lines (`~/.okqlrc` is read if it is absent) |
| `GOKQL_EMBED`, `GOKQL_EMBED_API_KEY`, `GOKQL_AI_API_KEY` | Environment: the embedding backend and API keys ([embeddings and AI](../guide/embeddings-and-ai.md)) |
