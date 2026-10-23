# Writing data

gokql's write commands are ADX's management commands, with the same
names, options and results. All of them append: a write adds new
extents (files) and never changes an existing one. Changing a record
means writing a new version of it, and the current state is a query.

## Tables

A table exists once it has a schema, from `.create table` or from the
first write.

```kql
.create table Readings (Station: string, At: datetime, TempC: real)
```

```kql
.create-merge table Readings (Station: string, At: datetime, TempC: real, Humidity: real)
```

`.create-merge` creates a table or adds the columns it lacks;
`.alter-merge table T (...)` adds columns to an existing table. Older
extents read a new column as null. Column types are `string`, `long`,
`int`, `real`, `decimal`, `bool`, `datetime`, `timespan`, `guid` and
`dynamic`.

## Appending rows

| Command | Behaviour |
|---|---|
| `.set-or-append T <\| query` | Append, creating `T` if absent |
| `.append T <\| query` | Append; `T` must exist |
| `.set T <\| query` | Create `T` from the query; fails if it exists |
| `.set-or-replace T <\| query` | Replace `T`'s rows with the query's |
| `.ingest inline into table T <\| rows` | Append CSV text given inline |
| `.ingest csv into table T from "path"` | Append a CSV file |

The query can be anything tabular. A `datatable` literal is the usual
way to write a batch, and one call with many rows is better than many
calls with one row each (fewer extents):

```kql
.set-or-append Readings <| datatable(Station:string, At:datetime, TempC:real, Humidity:real)
  ["north", datetime(2026-10-01 06:00), 8.5, 0.81,
   "north", datetime(2026-10-01 12:00), 14.0, 0.62,
   "south", datetime(2026-10-01 06:00), 11.2, 0.77]
```

Columns are matched by name, and the query must supply every column of
the table (write `real(null)` and the like for a value you do not
have). A column the table lacks is an error unless
`with (extend_schema=true)` adds it:

```kql
.set-or-append Readings with (extend_schema=true) <| print Station = "east", At = datetime(2026-10-01 06:00), TempC = 9.9, Humidity = real(null), Wind = 12.0
```

```kql
Readings
| project Station, TempC, Wind
| order by Station asc, TempC asc
```

```output
Station  TempC  Wind
───────  ─────  ────
east     9.9    12
north    8.5
north    14
south    11.2
(4 rows)
```

Results come from any query, so derived tables are one command:

```kql
.set-or-append DailyMeans <| Readings
| summarize MeanC = avg(TempC) by Station, Day = bin(At, 1d)
```

### Ingestion time

Every row is stamped with a hidden `_TimeReceived` column at write
time. It is not shown unless named, `ingestion_time()` returns it, and
queries may use it like any column. Rows written by one command share
one stamp.

## Current state: the latest version of each record

Append-only storage keeps every version. The current version of each
record is the one received last:

```kql
.set-or-append Readings <| datatable(Station:string, At:datetime, TempC:real, Humidity:real, Wind:real)
  ["north", datetime(2026-10-01 12:00), 13.6, 0.62, real(null)]
```

```kql
Readings
| summarize arg_max(_TimeReceived, *) by Station, At
| where Station == "north"
| project Station, At, TempC
| order by At asc
```

```output
Station  At                    TempC
───────  ────                  ─────
north    2026-10-01T06:00:00Z  8.5
north    2026-10-01T12:00:00Z  13.6
(2 rows)
```

Wrap the pattern in a stored function so every reader uses the same
definition:

```kql
.create-or-alter function with (folder = "library", docstring = "Latest reading per station and time")
LatestReadings() {
    Readings | summarize arg_max(_TimeReceived, *) by Station, At
}
```

## Replacing and deleting

`.set-or-replace` writes the query's rows as the table's new content.
The old extents are superseded, not deleted, until `.gc` removes them
([storage](storage.md)). `with (recreate_schema=true)` lets the new
rows have a different schema.

`.delete table T records <| T | where ...` removes the rows matching
the predicate. Only the extents that hold matching rows are rewritten,
and the remaining rows keep their original ingestion time.
`with (whatif=true)` reports what would be removed:

```kql
.delete table Readings records with (whatif=true) <| Readings | where TempC < 9
```

```output
Result  RecordsMatched  ExtentsRewritten
──────  ──────────────  ────────────────
WhatIf  1               0
(1 rows)
```

## Several commands at once

`.execute database script` runs control commands in order, one per
line:

```kql
.execute database script with (ContinueOnErrors=true) <|
.create-merge table Stations (Station: string, Region: string)
.set-or-append Stations <| datatable(Station:string, Region:string)["north", "N", "south", "S"]
```

```output
OperationId  CommandType     CommandText                                                                                      Result     Reason
───────────  ───────────     ───────────                                                                                      ──────     ──────
1            .create-merge   .create-merge table Stations (Station: string, Region: string)                                   Completed
2            .set-or-append  .set-or-append Stations <| datatable(Station:string, Region:string)["north", "N", "south", "S"]  Completed
(2 rows)
```

`ContinueOnErrors=true` runs the rest after a failure;
`ThrowOnErrors=true` makes a failure an error; `Atomic=true` (a gokql
addition) writes nothing unless every command succeeds.

## Validating writes: update policies

An update policy runs a query over the rows just appended to a source
table and appends its result to the policy's table. With
`IsTransactional`, a failure of that query fails the source write too.
With `assert()` in the query, that makes the target table hold only
rows that passed validation:

```kql
.create table RawObservations (Id: string, Status: string)
```

```kql
.create table Observations (Id: string, Status: string)
```

```kql
.create-or-alter function ValidObservations() {
    RawObservations
    | extend ok = assert(Status in ("asserted", "measured", "cited"), "unknown Status")
    | project-away ok
}
```

```kql
.alter table Observations policy update @'[{"IsEnabled": true, "Source": "RawObservations", "Query": "ValidObservations()", "IsTransactional": true}]'
```

```kql
.set-or-append RawObservations <| datatable(Id:string, Status:string)["o1", "measured"]
```

The command's result has one row per table written: `OK` for
`RawObservations` and `OK: update policy -> Observations`. A row that
fails validation fails the whole write, source included:

```kql error
.set-or-append RawObservations <| datatable(Id:string, Status:string)["o2", "guessed"]
```

```output
Error: update policy of Observations: function "ValidObservations": extend "ok": assert() has failed with message: 'unknown Status' (transactional update policy: nothing was written)
```

```kql
Observations
| project Id, Status
```

```output
Id    Status
────  ──────
o1    measured
(1 rows)
```

Inside the policy's query, the source table's name refers to the newly
appended rows only. Policies cascade, up to eight levels.
`"BlockDirectWrites": true` (a gokql addition) refuses direct writes to
the target, so its only way in is through the policy.
`.show table T policy update` shows a policy and
`.delete table T policy update` removes it.

## Materialized views

A materialized view keeps an aggregation current as its source table is
appended to:

```kql
.create materialized-view StationMax on table Readings {
    Readings | summarize MaxC = max(TempC) by Station
}
```

```kql
StationMax
| order by Station asc
```

```output
Station  MaxC
───────  ────
east     9.9
north    14
south    11.2
(3 rows)
```

Query a view by name or as `materialized_view("StationMax")`.
`.show materialized-views` lists them and
`.drop materialized-view V` removes one.

## Stored functions

`.create function`, `.create-or-alter function` and
`.create function ifnotexists` store a function with optional
`docstring` and `folder` properties. Stored functions are versioned: a
redefinition appends, and the latest wins. `.show functions`,
`.show function F` and `.drop function F` manage them. Parameters work
as in ADX, including tabular parameters and defaults:

```kql
.create-or-alter function StationReadings(name: string, minC: real = -100.0) {
    LatestReadings() | where Station == name and TempC >= minC
}
```

```kql
StationReadings("north", 10.0)
| project At, TempC
```

```output
At                    TempC
────                  ─────
2026-10-01T12:00:00Z  13.6
(1 rows)
```

## See also

- [Storage](storage.md): extents, concurrency, compaction and dropping
- [Files and external data](files-and-external-data.md): loading CSV, JSON and Parquet
- [Commands reference](../reference/commands.md)
