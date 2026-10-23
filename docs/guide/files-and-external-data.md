# Files and external data

gokql reads CSV, JSON and Parquet directly, loads them into tables, and
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
| `vortex("path")` | A single Vortex file |

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
(2 rows)
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

`.ingest csv into table T from "path"` appends a CSV file to an
existing table, skipping its header row, and
`.ingest inline into table T <| ...` takes CSV text in the command
itself:

```kql
.create table SignIns (TimeGenerated: datetime, UserPrincipalName: string, IPAddress: string, Location: string, ResultType: long, AppDisplayName: string)
```

```kql
.ingest csv into table SignIns from "./testdata/docs/signins.csv"
```

```output
Result                                                          RowsIngested  Extents
──────                                                          ────────────  ───────
Ingested 10 rows into SignIns from ./testdata/docs/signins.csv  10            1
(1 rows)
```

## externaldata: ADX's way to read files

`externaldata` declares the schema and reads files or URLs, as in ADX.
It reads local paths, `file://` URIs and `http(s)` URLs, in the formats
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
raj@contoso.example  IN        3
(2 rows)
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
