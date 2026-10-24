# Files and external data

gokql reads CSV, JSON, Parquet and Apache Arrow directly, loads them into tables, and
can fetch data over HTTP when the callout policy allows it. The
examples use the sample files in `testdata/docs/`, with paths relative
to the repository root.

## Query a file as a table

| Source | Reads |
|---|---|
| `csv("path")` | CSV with a header row; column types are inferred |
| `json("path")` | A JSON array of objects |
| `ndjson("path")` | One JSON object per line |
| `parquet("path")` | Apache Parquet |
| `arrow("path")` | An Apache Arrow IPC file (Feather v2) or stream |
| `vortex("path")` | One Vortex file, such as an extent of a gokql database |

These are gokql additions for one-off work; they are not ADX syntax.
For portable KQL use `externaldata`, below.

```kql
csv("./testdata/docs/signins.csv")
| where ResultType != 0
| summarize Failures = count(), Apps = make_set(AppDisplayName) by UserPrincipalName, Location
| order by Failures desc
```

```output
UserPrincipalName    Location  Failures  Apps
─────────────────    ────────  ────────  ────
ana@contoso.example  RU        3         ["Azure Portal"]
raj@contoso.example  IN        2         ["Outlook"]
ana@contoso.example  NL        1         ["Outlook"]
raj@contoso.example  NL        1         ["Outlook"]
lee@contoso.example  NL        1         ["Outlook"]
(5 rows)
```

`getschema` shows what was inferred:

```kql
ndjson("./testdata/docs/app.jsonl")
| getschema
| project ColumnName, ColumnType
```

```output
ColumnName  ColumnType
──────────  ──────────
ts          datetime
host        string
level       string
msg         string
ms          long
(5 rows)
```

Nested JSON arrives as `dynamic` and is unpacked with the usual
operators:

```kql
json("./testdata/docs/findings.json")
| where array_length(cves) > 0
| mv-expand cve = cves to typeof(string)
| project id, severity, resource = tostring(resource.name), cve
```

```output
id       severity  resource   cve
────     ────────  ────────   ────
wiz-001  HIGH      vm-web-01  CVE-2024-3094
wiz-001  HIGH      vm-web-01  CVE-2023-4863
wiz-003  CRITICAL  vm-db-02   CVE-2024-6387
(3 rows)
```


## Apache Arrow

Arrow is how gokql exchanges tables with pandas, Polars, DuckDB and
other columnar tools without going through text.

`arrow("path")` reads the IPC file format (Feather v2, `.arrow`,
`.feather`) and the IPC stream format. Integers become `long` (`int32`
becomes `int`), floats and decimals `real`, timestamps and dates
`datetime`, durations `timespan`, strings (dictionary-encoded or not)
`string`, and lists, structs and maps `dynamic`. This file was written
by pyarrow:

```kql
arrow("./testdata/docs/readings.arrow")
| summarize Readings = count(), MaxC = max(Celsius) by Sensor
| order by Sensor asc
```

```output
Sensor  Readings  MaxC
──────  ────────  ────
s1      3         23
s2      2         19.5
s3      1         30.5
(3 rows)
```

`-mode arrow` writes a query's result as an Arrow IPC stream:

```bash
gokql -db ./scope -mode arrow -e 'SigninLogs | where ResultType != 0' > failures.arrow
```

```python
import pyarrow.ipc as ipc
df = ipc.open_stream("failures.arrow").read_pandas()
```

| KQL | Arrow |
|---|---|
| `long`, `int`, `real`, `bool`, `string` | `int64`, `int32`, `float64`, `bool`, `utf8` |
| `datetime` | `timestamp[ns, UTC]` |
| `timespan` | `duration[ns]` |
| `guid`, `decimal` | `utf8` |
| `dynamic` | `utf8` holding JSON |

Every field carries `kql.type` metadata, so `arrow()` reads gokql's own
output back with its original types. A query that returns several
results (`fork`, `facet`) is an error in this mode; use `-mode json`
for those.

## Load a file into a table

Any of these sources can feed a write:

```kql
.set-or-append Findings <| json("./testdata/docs/findings.json")
| project Id = id, Severity = severity, Resource = tostring(resource.name), Rule = rule, FirstSeen = firstSeen
```

```kql
Findings
| summarize count() by Severity
| order by Severity asc
```

```output
Severity  count_
────────  ──────
CRITICAL  1
HIGH      1
LOW       1
MEDIUM    1
(4 rows)
```

ADX's `.ingest into table T (...) with (...)` appends files to an
existing table, reading them as `externaldata` with the table's schema
(so update policies run). `.ingest csv into table T from "path"` is a
gokql shorthand for a CSV with a header row, and
`.ingest inline into table T <| ...` takes CSV text in the command
itself:

```kql
.create table SignIns (TimeGenerated: datetime, UserPrincipalName: string, IPAddress: string, Location: string, ResultType: long, AppDisplayName: string)
```

```kql
.ingest into table SignIns ("./testdata/docs/signins.csv") with (format = "csv", ignoreFirstRecord = true)
```

```kql
SignIns
| summarize SignIns = count(), Users = dcount(UserPrincipalName)
```

```output
SignIns  Users
───────  ─────
13       3
(1 rows)
```

## externaldata: ADX's way to read files

`externaldata` declares the schema and reads files or URLs, as in ADX.
It reads local paths, `file://` URIs and `http(s)` URLs (ADX itself
reads only URLs, such as blob storage, so a query with a local path
does not port), in the formats
`csv`, `tsv`, `psv`, `scsv`, `txt`, `json` and `multijson`, and
decompresses `.gz`. The format comes from `with (format=...)` or the
file extension:

```kql
externaldata(TimeGenerated: datetime, User: string, IP: string, Location: string, Result: long, App: string)
  ["./testdata/docs/signins.csv"]
  with (ignoreFirstRecord = true)
| where Location !in ("AU")
| summarize count() by User, Location
| order by User asc
```

```output
User                 Location  count_
────                 ────────  ──────
ana@contoso.example  RU        4
ana@contoso.example  NL        1
lee@contoso.example  NL        1
raj@contoso.example  IN        3
raj@contoso.example  NL        1
(5 rows)
```

JSON formats match columns by name:

```kql
externaldata(host: string, level: string, msg: string, ms: long)
  [@"./testdata/docs/app.jsonl"]
  with (format = "multijson")
| where level != "info"
```

```output
host   level  msg         ms
────   ─────  ────        ────
web01  error  disk full
db02   warn   slow query  1840
(2 rows)
```

Over HTTP, `externaldata` is allowed by default, as in ADX; a callout
policy rule can deny it ([security](security.md)).

## Exports from the Azure portal

CSV files exported from the portal's query results (Log Analytics,
Sentinel, Defender) have a few habits: headers such as
`TimeGenerated [UTC]`, 12-hour timestamps (`10/1/2026, 7:58:12.000
AM`), and `dynamic` columns written as JSON text. All three are
handled in the query:

```kql
csv("./testdata/docs/portal-signins.csv")
| project-rename TimeGenerated = ['TimeGenerated [UTC]']
| extend TimeGenerated = todatetime(TimeGenerated), DeviceDetail = parse_json(DeviceDetail)
| project TimeGenerated, UserPrincipalName, ResultType, OS = tostring(DeviceDetail.operatingSystem)
```

```output
TimeGenerated         UserPrincipalName    ResultType  OS
─────────────         ─────────────────    ──────────  ────
2026-10-01T07:58:12Z  ana@contoso.example  0           Windows 11
2026-10-01T09:15:00Z  ana@contoso.example  50126       Linux
(2 rows)
```

Exports through the API or `az monitor log-analytics query` are JSON
with ISO timestamps and read directly with `json()`. Data exported
continuously to storage arrives as one JSON object per line
(`ndjson()`, or `externaldata` with `format="multijson"`).

## Large files

`csv()`, `json()`, `ndjson()` and `externaldata` read the whole file
into memory for the query. For exports larger than memory comfortably
holds, load them into a table once (the table is stored compressed and
scanned column by column), in parts if necessary: one `.ingest into`
per file. `externaldata` accepts several files in one call and reads
at most 1 GB per call.

## Work out a schema: infer_storage_schema

`infer_storage_schema` reads files and returns a schema string ready
for `externaldata` or `.create table`:

```kql
evaluate infer_storage_schema(dynamic({
    "StorageContainers": ["./testdata/docs"],
    "FileNamePrefix": "signins",
    "DataFormat": "csv",
    "InferenceOptions": {"UseFirstRowAsHeader": true}
}))
```

```output
CslSchema
─────────
TimeGenerated:datetime, UserPrincipalName:string, IPAddress:string, Location:string, ResultType:long, AppDisplayName:string
(1 rows)
```

It accepts files, directories (filtered by `FileNamePrefix` and
`FileExtension`) and http(s) files, and `Mode` `last` (the newest file,
the default), `any` or `all`.
`infer_storage_schema_with_suggestions` also recommends types, for
example `datetime` for epoch-millisecond columns named as times.

## Web APIs: http_request

`http_request` and `http_request_post` call a web API and return the
response headers and body (parsed when it is JSON). They are refused
unless the callout policy has a `webapi` rule for the URL
([security](security.md)):

```kql no-run
let uri = "https://prices.azure.com/api/retail/prices?$filter=serviceName eq 'Azure Purview' and location eq 'EU West'";
evaluate http_request(uri)
| project Items = ResponseBody.Items
| mv-expand Items
| evaluate bag_unpack(Items)
| project productName, meterName, retailPrice, unitOfMeasure
```

`evaluate` can start a query, as here. Headers are given as a property
bag (`dynamic({"Authorization": "Bearer ..."})`). Every request carries
`x-ms-readonly: true`, as ADX's do.

## See also

- [Writing data](writing-data.md)
- [Security](security.md): the callout policy
- [Plugins reference](../reference/plugins.md)
