# A local cluster of specialised databases

<!-- gokql:setup
@primary project

@db catalogue
.set-or-append Stars <| datatable(Name:string, Constellation:string, Magnitude:real)["Antares", "Scorpius", 1.06, "Aldebaran", "Taurus", 0.86, "Regulus", "Leo", 1.4, "Spica", "Virgo", 0.97]

@db corpus
.set-or-append Mentions <| datatable(Text:string, Line:long, Star:string)["MUL.APIN I", 12, "Antares", "MUL.APIN I", 14, "Aldebaran", "Astrolabe B", 3, "Antares", "Enuma Anu Enlil 50", 7, "Regulus"]

.set-or-append Questions <| datatable(Id:string, Question:string, Status:string)["Q1", "Which stars do the earliest texts name most?", "open"]
-->

Some data belongs in separate databases: a reference catalogue that
rarely changes, a corpus that grows by import, the working notes of a
project, a log source per system. Separate directories keep each one's
ownership, backups and access clear, and let different processes
write to different databases without touching each other. Analysis
still needs them together.

gokql answers as a cluster of such databases. The database you open
lists the others in its [`gokql.yaml`](../reference/configuration.md),
and queries reach them with ADX's own syntax, `database("name")` and
`cluster("name").database("name")`, which LLMs and Kusto users
already know.

## Layout

```
/data/scopes/
  catalogue/      reference data: Stars
  corpus/         imported texts: Mentions
  project/        working notes: Questions   <- opened by the analyst
    gokql.yaml
```

```yaml
# /data/scopes/project/gokql.yaml
cluster: research
databases:
  - name: catalogue
    path: /data/scopes/catalogue
  - name: corpus
    path: /data/scopes/corpus
```

## Querying across

```kql
.show databases
| project DatabaseName, DatabaseAccessMode
```

```output
DatabaseName  DatabaseAccessMode
────────────  ──────────────────
project       ReadWrite
catalogue     ReadOnly
corpus        ReadOnly
(3 rows)
```

```kql
database("corpus").Mentions
| summarize Mentions = count(), Texts = make_set(Text) by Star
| join kind=inner (database("catalogue").Stars) on $left.Star == $right.Name
| project Star, Constellation, Magnitude, Mentions, Texts
| order by Mentions desc, Star asc
```

```output
Star       Constellation  Magnitude  Mentions  Texts
────       ─────────────  ─────────  ────────  ─────
Antares    Scorpius       1.06       2         ["MUL.APIN I","Astrolabe B"]
Aldebaran  Taurus         0.86       1         ["MUL.APIN I"]
Regulus    Leo            1.4        1         ["Enuma Anu Enlil 50"]
(3 rows)
```

The other databases are read-only from here; the project database
stays writable, so results can be saved next to the question they
answer:

```kql
.set-or-append Answers <| database("corpus").Mentions
| summarize Mentions = count() by Star
| extend QuestionId = "Q1"
```

```kql
Answers
| order by Mentions desc, Star asc
| project QuestionId, Star, Mentions
```

```output
QuestionId  Star       Mentions
──────────  ────       ────────
Q1          Antares    2
Q1          Aldebaran  1
Q1          Regulus    1
(3 rows)
```

## Growing it

- **Another cluster**: list a second group of databases under
  `clusters` and reach it with `cluster("archive").database(...)`.
- **Several writers**: each importer writes its own database; the
  analyst's database reads them all.
- **One endpoint**: `gokql -serve` can serve several databases at once
  (`server.databases`), each read-only or writable, for clients such
  as a chatbot ([serving](../guide/serving.md)).
- **One query per database**: `macro-expand` runs a subquery against
  each database of a group and unions the results
  ([the local cluster](../guide/cluster.md#aliases-and-macro-expand)).

## See also

- [The local cluster guide](../guide/cluster.md): every feature, with examples
- [Configuration](../reference/configuration.md)
