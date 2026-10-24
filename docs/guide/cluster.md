# The local cluster: several databases as one

<!-- gokql:setup
@primary notes

.set-or-append Notes <| datatable(Id:string, Text:string)["n1", "Check the Lagash calendar against MUL.APIN"]

@db stars
.set-or-append Stars <| datatable(Name:string, Constellation:string, Magnitude:real)["Antares", "Scorpius", 1.06, "Aldebaran", "Taurus", 0.86, "Regulus", "Leo", 1.4]

@db texts
.set-or-append Texts <| datatable(Id:string, Title:string, Mentions:dynamic)["MA-1", "MUL.APIN tablet 1", dynamic(["Antares", "Aldebaran"]), "E-7", "Enuma Anu Enlil 7", dynamic(["Regulus"])]

@cluster archive
.set-or-append Excavations <| datatable(Site:string, Season:long)["Girsu", 1929, "Girsu", 2016]

@config {"cluster": "lab", "principal": "researcher"}
-->

In ADX a cluster holds many databases and a query can reach any of
them with `database("name")` or `cluster("name").database("name")`.
gokql does the same with directories: one database is the one you
open, and a file inside it lists the others. Each database stays a
separate directory, written and backed up on its own, and a query
joins them as if they lived in one service.

This suits specialised databases kept apart on purpose: a catalogue of
stars, a corpus of texts and the working notes that connect them; or
one database per log source.

## The configuration file

[`gokql.yaml`](../reference/configuration.md) in the directory you
open:

```yaml
cluster: lab            # this cluster's name
principal: researcher   # what current_principal() reports
databases:              # the other databases of this cluster
  - name: stars
    path: /data/scopes/stars
  - name: texts
    path: /data/scopes/texts
clusters:               # other clusters, by their primary database
  - name: archive
    path: /data/archive/archive
```

| Field | Meaning |
|---|---|
| `databases` | The other databases of this cluster: a name and an absolute path each. They are read-only from here. |
| `clusters` | Other clusters: each path is that cluster's primary database, whose own `gokql.yaml` lists its databases. |
| `cluster` | This cluster's name, for `cluster("lab")` and `current_cluster_endpoint()`. Default `localhost`. |
| `principal` | What `current_principal()` reports. |
| `callouts` | The callout policy ([security](security.md)). |

Paths must be absolute. The databases need no configuration of their
own. A database's name is its directory's name: `stars` above, and
`archive` for the other cluster's primary database, which is why the
query below reads `cluster("archive").database("archive")`.

## Querying across databases

```kql
.show databases
| project DatabaseName, IsCurrent, DatabaseAccessMode
```

```output
DatabaseName  IsCurrent  DatabaseAccessMode
────────────  ─────────  ──────────────────
notes         true       ReadWrite
stars         false      ReadOnly
texts         false      ReadOnly
(3 rows)
```

`database("name").Table` reads a table of another database, and joins
work across databases like any other:

```kql
database("texts").Texts
| mv-expand Star = Mentions to typeof(string)
| join kind=inner (database("stars").Stars) on $left.Star == $right.Name
| project Title, Star, Constellation, Magnitude
| order by Title asc, Star asc
```

```output
Title              Star       Constellation  Magnitude
─────              ────       ─────────────  ─────────
Enuma Anu Enlil 7  Regulus    Leo            1.4
MUL.APIN tablet 1  Aldebaran  Taurus         0.86
MUL.APIN tablet 1  Antares    Scorpius       1.06
(3 rows)
```

`cluster("name").database("name")` reaches another cluster:

```kql
cluster("archive").database("archive").Excavations
| summarize Seasons = make_list(Season) by Site
```

```output
Site   Seasons
────   ───────
Girsu  [1929,2016]
(1 rows)
```

Where the rows came from is visible with `union withsource` and
wildcards. With more than one database involved, the source is
qualified as ADX writes it:

```kql
union withsource = Source database("*").*
| summarize Rows = count() by Source
| order by Source asc
```

```output
Source                   Rows
──────                   ────
database("notes").Notes  1
database("stars").Stars  3
database("texts").Texts  2
(3 rows)
```

`find` and `search` take the same wildcards:
`search in (database("*").*) "Antares"`.

## Aliases and macro-expand

`alias database` names a database for the rest of the query:

```kql
alias database cat = cluster("lab").database("stars");
database("cat").Stars
| where Magnitude < 1
| project Name
```

```output
Name
────
Aldebaran
(1 rows)
```

`macro-expand` runs one subquery per database and unions the results,
with `X.$current_database` naming the database each row came from:

```kql
macro-expand entity_group [database("stars"), database("texts")] as X (
    union X.*
    | summarize Rows = count()
    | extend Database = X.$current_database
)
| order by Database asc
```

```output
Rows  Database
────  ────────
3     stars
2     texts
(2 rows)
```

## Cluster identity

```kql
print Cluster = current_cluster_endpoint(), Database = current_database(), Principal = current_principal()
```

```output
Cluster  Database  Principal
───────  ────────  ─────────
lab      notes     researcher
(1 rows)
```

## Writes stay local

Writes go to the database you opened. Other databases are read-only
from here, and a command that names one (`.set-or-append
database("stars").Stars <| ...`) is refused. Open that database to
write to it. To serve several writable databases from one HTTP
endpoint, list them under `server.databases` ([serving](serving.md)).

Operators that run where the data is (`where`, `project`, `extend`,
`summarize`, `take`, `top` and similar) are evaluated inside the other
database before rows are returned, so a filtered read of a large
remote table moves only what it keeps.

## See also

- [The local cluster use case](../use-cases/local-cluster.md)
- [Serving](serving.md)
- [Storage](storage.md)
