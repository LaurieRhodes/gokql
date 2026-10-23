# Serving over HTTP

`gokql -serve` makes a database available over HTTP, speaking the REST
protocol of the Kusto emulator, so Kusto clients and tools can use it.

```bash
gokql -serve -db ./scope -addr 127.0.0.1:8080
```

## Protocol

| Endpoint | Body | Runs |
|---|---|---|
| `POST /v1/rest/query` | `{"db": "<name>", "csl": "<query>", "properties": {...}}` | Queries |
| `POST /v1/rest/mgmt` | the same | Management commands |

Responses are `{"Tables": [...]}` in the Kusto v1 format. The database
name clients send is the directory's name, or `-dbname`.

```bash
curl -s localhost:8080/v1/rest/query -H 'Content-Type: application/json' \
  -d '{"db": "scope", "csl": "Findings | count"}'
```

`properties` carries what Kusto clients send there:

- `Parameters`: values for `declare query_parameters(...)` in the query.
- `Options`: `query_language` (`gql` or `opencypher`) and
  `query_graph_reference`, for [GQL and openCypher](graphs.md#gql-and-opencypher).

[kustainer-ui](https://github.com/LaurieRhodes/kustainer-ui) and other
clients written for the emulator work unchanged.

## Configuration

The `server` section of the database's
[`gokql.yaml`](../reference/configuration.md) configures a server
started with just `gokql -serve -db DIR`. Flags given on the command
line take precedence.

```yaml
server:
  addr: 127.0.0.1:8443
  name: notes                    # the database name clients send
  tls:
    cert: /etc/gokql/cert.pem
    key: /etc/gokql/key.pem
  databases:                     # more databases on the same endpoint
    - name: stars
      path: /data/scopes/stars
    - name: texts
      path: /data/scopes/texts
```

`server.databases` serves further databases from the same endpoint,
each selected by the request's `db`. Unlike the read-only databases of
the [local cluster](cluster.md), they are writable by clients unless
marked `readonly`. A server can do both: serve several databases, each
of which lists others in its own `gokql.yaml`.

## Read-only serving: fixed data for a chatbot or an application

A common deployment is a fixed body of data queried by many clients
that must never change it: a chatbot answering from company policies,
an application looking up reference data, an agent given a curated
knowledge base. Make the server read-only:

```yaml
# gokql.yaml in /srv/handbook
server:
  addr: 127.0.0.1:8080
  name: handbook
  readonly: true          # clients may query; every write command is refused
  databases:
    - name: products
      path: /srv/products
      readonly: true
```

Clients can run any query, including `.show` commands, and get
`the database is open read-only` for anything else. The data is
maintained separately, by opening the directory with the CLI (or a
second, writable server bound to a private address); the read-only
server sees each update on its next request. `gokql -serve -readonly`
does the same from the command line, and a top-level `readonly: true`
makes the database read-only for every client, CLI included.

## Behaviour

- Each request opens the database afresh, as a CLI invocation does, so
  writes by other processes are visible to the next request and
  nothing is cached between requests.
- `-readonly` or `server.readonly` makes the server refuse write
  commands; `server.databases[].readonly` does so for one database.
- There is no authentication. See [security](security.md#serving).

## See also

- [Getting started](../getting-started.md)
- [CLI reference](../reference/cli.md)
