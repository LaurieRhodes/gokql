# Security data triage

Security teams already write KQL every day in Microsoft Sentinel and
Defender. gokql runs the same queries on a laptop over exported data:
sign-in logs, cloud security findings, alerts and audit trails in CSV
or JSON. Nothing leaves the machine and nothing needs a workspace. The
hunting queries below use Sentinel's table and column names and
types, so they port to Sentinel as written; the loading commands and
the triage log are local workflow and do not.

The examples use the sample files in `testdata/docs/` (paths relative
to the repository root): thirteen Entra-style sign-in events and four cloud
security findings.

## Load the exports

```kql
.create table SigninLogs (TimeGenerated: datetime, UserPrincipalName: string, IPAddress: string, Location: string, ResultType: string, AppDisplayName: string)
```

```kql
.ingest into table SigninLogs ("./testdata/docs/signins.csv") with (format = "csv", ignoreFirstRecord = true)
```

The table uses Sentinel's types (`ResultType` is a string there), so
the queries below behave as they would in the workspace. To reload an
export, use `.set-or-replace SigninLogs <| ...` or drop the table
first; appending the same file again doubles every count.

```kql
.set-or-append CloudFindings <| json("./testdata/docs/findings.json")
| project FindingId = id, Severity = severity, ResourceType = tostring(resource.type),
    Resource = tostring(resource.name), Region = tostring(resource.region), Rule = rule,
    Cves = cves, FirstSeen = firstSeen
```

`json()` infers the schema; declaring it, as for `SigninLogs`, is more
repeatable ([files and external data](../guide/files-and-external-data.md),
which also covers portal exports and large files).

## Hunting queries

Failed sign-ins followed by a success from the same address, the
shape of a successful brute-force attempt:

```kql
SigninLogs
| summarize Failures = countif(ResultType != "0"), Successes = countif(ResultType == "0"),
    FirstFailure = minif(TimeGenerated, ResultType != "0"), LastSuccess = maxif(TimeGenerated, ResultType == "0")
    by UserPrincipalName, IPAddress, Location
| where Failures >= 2 and Successes > 0 and LastSuccess > FirstFailure
| project UserPrincipalName, IPAddress, Location, Failures, Seconds = datetime_diff("second", LastSuccess, FirstFailure)
```

```output
UserPrincipalName    IPAddress     Location  Failures  Seconds
─────────────────    ─────────     ────────  ────────  ───────
raj@contoso.example  198.51.100.7  IN        2         36
ana@contoso.example  192.0.2.44    RU        3         62
(2 rows)
```

Password spraying looks different: one address failing against many
users. Count distinct users per address:

```kql
SigninLogs
| where ResultType != "0"
| summarize Users = dcount(UserPrincipalName), Failures = count() by IPAddress
| order by Users desc
```

```output
IPAddress      Users  Failures
─────────      ─────  ────────
198.51.100.99  3      3
198.51.100.7   1      2
192.0.2.44     1      3
(3 rows)
```

A successful sign-in from a country the user had not used before, or
by a user with no history at all:

```kql
let known = SigninLogs
    | where TimeGenerated < datetime(2026-10-01 09:00)
    | summarize KnownLocations = make_set(Location) by UserPrincipalName;
SigninLogs
| where TimeGenerated >= datetime(2026-10-01 09:00) and ResultType == "0"
| join kind=leftouter known on UserPrincipalName
| where isnull(KnownLocations) or not(set_has_element(KnownLocations, Location))
| project TimeGenerated, UserPrincipalName, Location, KnownLocations, AppDisplayName
```

```output
TimeGenerated         UserPrincipalName    Location  KnownLocations  AppDisplayName
─────────────         ─────────────────    ────────  ──────────────  ──────────────
2026-10-01T09:16:02Z  ana@contoso.example  RU        ["AU"]          Azure Portal
2026-10-01T10:20:00Z  lee@contoso.example  AU                        SharePoint
(2 rows)
```

These queries use only operators and functions Log Analytics
supports, so they work as Sentinel analytics rules.

## Findings: what to fix first

```kql
CloudFindings
| extend SeverityRank = case(Severity == "CRITICAL", 1, Severity == "HIGH", 2, Severity == "MEDIUM", 3, 4)
| order by SeverityRank asc, Resource asc
| project Severity, Resource, ResourceType, Rule, Cves
```

```output
Severity  Resource   ResourceType    Rule                                       Cves
────────  ────────   ────────────    ────                                       ────
CRITICAL  vm-db-02   VirtualMachine  Exposed SSH with weak credentials          ["CVE-2024-6387"]
HIGH      vm-web-01  VirtualMachine  Public VM with critical vulnerability      ["CVE-2024-3094","CVE-2023-4863"]
MEDIUM    stlogs01   StorageAccount  Storage account allows public blob access  []
LOW       kv-shared  KeyVault        Key Vault without purge protection         []
(4 rows)
```

Each CVE and the resources it affects:

```kql
CloudFindings
| mv-expand Cve = Cves to typeof(string)
| summarize Resources = make_set(Resource) by Cve
| order by Cve asc
```

```output
Cve            Resources
────           ─────────
CVE-2023-4863  ["vm-web-01"]
CVE-2024-3094  ["vm-web-01"]
CVE-2024-6387  ["vm-db-02"]
(3 rows)
```

Joining findings to other exports (an asset inventory, owners, open
tickets) is an ordinary `join` on the resource name.

## Keeping a triage record

A triage log written as rows keeps its own history: a changed
decision is a new row, so who decided what, when, and what changed
later stays visible. It is a record, not tamper-proof storage: anyone
who can write to the database can also replace or delete rows, so
give everyone but the log's writer read-only access to it (`-readonly`,
or a read-only server).

```kql
.set-or-append Triage <| datatable(FindingId:string, Decision:string, Owner:string, Note:string)
  ["wiz-003", "fix-now", "platform", "SSH exposed with weak credentials",
   "wiz-004", "accept", "security", "purge protection planned for Q1"]
```

```kql
CloudFindings
| join kind=leftouter (Triage | extend Ingested = ingestion_time() | summarize arg_max(Ingested, *) by FindingId) on FindingId
| project FindingId, Severity, Resource, Decision = coalesce(Decision, "untriaged"), Owner
| order by FindingId asc
```

```output
FindingId  Severity  Resource   Decision   Owner
─────────  ────────  ────────   ────────   ─────
wiz-001    HIGH      vm-web-01  untriaged
wiz-002    MEDIUM    stlogs01   untriaged
wiz-003    CRITICAL  vm-db-02   fix-now    platform
wiz-004    LOW       kv-shared  accept     security
(4 rows)
```

## Working with an AI assistant

An assistant given shell access and the [agent notes](../for-agents.md)
can run these queries itself, iterate on them, and record its
conclusions in the triage table. Open the database `-readonly` when it
should only investigate, and read [security](../guide/security.md)
first: read-only stops writes, not reads of local files or, by default,
`externaldata` fetches. Sentinel-specific tables and functions
(`_GetWatchlist`, ASIM parsers) are not built in; load a watchlist CSV
as a table and define the parsers you need as stored functions.
`geo_info_from_ip_address()` is not available; load a GeoIP CSV and
use `evaluate ipv4_lookup(...)` against it instead.

## See also

- [Files and external data](../guide/files-and-external-data.md)
- [KQL development](kql-development.md): testing analytics rules before deploying them
