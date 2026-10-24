# KQL development and testing

KQL usually lives in a service: Sentinel analytics rules, ADX
functions and update policies, Defender hunting queries. Developing
against the service means a workspace, data you may not be allowed to
copy, and no unit tests. gokql runs the same KQL locally, so queries
and functions can be written against small fixtures, tested in CI, and
deployed once they pass.

## Fixtures as datatables

A function under test reads a table; the test supplies one. Tabular
parameters make that explicit:

```kql
.create-or-alter function with (docstring = "Users with N or more failed sign-ins in a window")
BruteForceCandidates(Signins: (TimeGenerated: datetime, UserPrincipalName: string, ResultType: string), threshold: long = 3, window: timespan = 10m) {
    Signins
    | where ResultType != "0"
    | summarize Failures = count() by UserPrincipalName, Window = bin(TimeGenerated, window)
    | where Failures >= threshold
}
```

```kql
let fixture = datatable(TimeGenerated: datetime, UserPrincipalName: string, ResultType: string)
  [datetime(2026-10-01 09:00:01), "ana", "50126",
   datetime(2026-10-01 09:00:05), "ana", "50126",
   datetime(2026-10-01 09:00:09), "ana", "50126",
   datetime(2026-10-01 09:00:20), "raj", "50126",
   datetime(2026-10-01 09:01:00), "raj", "0"];
BruteForceCandidates(fixture)
```

```output
UserPrincipalName  Window                Failures
─────────────────  ──────                ────────
ana                2026-10-01T09:00:00Z  3
(1 rows)
```

## Assertions

`assert()` fails the query when its condition is false, so a test is
a query that either returns or errors, and gokql's exit status turns
that into pass or fail. (These tests are for gokql: ADX accepts only
constant conditions in `assert()`. The functions under test are
portable; the assertions are not.)

```kql
let fixture = datatable(TimeGenerated: datetime, UserPrincipalName: string, ResultType: string)
  [datetime(2026-10-01 09:00:01), "ana", "50126",
   datetime(2026-10-01 09:00:05), "ana", "50126"];
let got = toscalar(BruteForceCandidates(fixture, 2) | count);
print ok = assert(got == 1, strcat("expected 1 candidate, got ", tostring(got)))
```

```output
ok
────
true
(1 rows)
```

```kql error
let fixture = datatable(TimeGenerated: datetime, UserPrincipalName: string, ResultType: string)
  [datetime(2026-10-01 09:00:01), "ana", "0"];
let got = toscalar(BruteForceCandidates(fixture) | count);
print ok = assert(got == 1, strcat("expected 1 candidate, got ", tostring(got)))
```

```output
Error: print: assert() has failed with message: 'expected 1 candidate, got 0'
```

Keep the functions and the tests as `.kql` files and run them in CI:
first load the functions into a fresh database, then run each test:

```bash
rm -rf ./build/db
for f in functions/*.kql; do gokql -db ./build/db < "$f" || exit 1; done
for t in tests/*.kql; do
  gokql -db ./build/db -mode csv < "$t" > /dev/null || { echo "FAIL $t"; exit 1; }
done
```

Statements in a piped file run in order; any failure makes the exit
status 1 ([getting started](../getting-started.md#5-pipes-and-scripts)).

## Update policies

Ingestion-time logic is the hardest to test in a service, because it
only runs when data arrives. Locally it runs on every append:

```kql
.create table RawEvents (Raw: string)
```

```kql
.create table Events (Time: datetime, Host: string, Level: string, Message: string)
```

```kql
.create-or-alter function ParseRawEvents() {
    RawEvents
    | parse Raw with Time:datetime " " Host " [" Level "] " Message
    | project Time, Host, Level, Message
}
```

```kql
.alter table Events policy update @'[{"IsEnabled": true, "Source": "RawEvents", "Query": "ParseRawEvents()", "IsTransactional": true}]'
```

```kql
.set-or-append RawEvents <| datatable(Raw:string)
  ["2026-10-01T08:00:00Z web01 [info] started",
   "2026-10-01T08:05:00Z web01 [error] disk full"]
```

```kql
Events
| project Time, Host, Level, Message
```

```output
Time                  Host   Level  Message
────                  ────   ─────  ───────
2026-10-01T08:00:00Z  web01  info   started
2026-10-01T08:05:00Z  web01  error  disk full
(2 rows)
```

The same policy JSON and function deploy to ADX unchanged.

## Sentinel and Log Analytics

Microsoft Sentinel runs on Log Analytics, which supports a subset of
ADX's KQL: no management commands, no `http_request` or most other
plugins, and its own tables (`SigninLogs`, `SecurityEvent`, ...) with
their own column types, such as `SigninLogs.ResultType` being a
string. gokql accepts the full ADX language, so a query that passes
here can still be rejected by Sentinel. Keep analytics-rule queries to
operators and functions Log Analytics documents, give fixture tables
the real Sentinel schema, and test the final rule in the workspace.

## How close is "the same"?

gokql is checked against Microsoft's documentation examples and the
Kusto emulator: 983 of the 1,042 deterministic examples agree, and
most differences are results ADX leaves unspecified, such as the order
of rows without `order by` ([compatibility](../compatibility.md)). Write
tests that do not depend on unspecified order (sort, or compare sets),
and they will hold on both engines. Sentinel-specific tables, ASIM
functions and `_GetWatchlist` are not built in; define stand-ins as
tables or functions in the test database.

## With an AI assistant

An assistant that writes KQL can run it here, read the error, and fix
it before a person sees it. Point it at [for-agents.md](../for-agents.md)
and a database of fixtures; give it `-readonly` if it should not
change them.

## See also

- [Writing data](../guide/writing-data.md): update policies, functions, materialized views
- [Reference](../reference/README.md): what is supported
