# Security data triage

Security teams already write KQL every day in Microsoft Sentinel and
Defender. gokql runs the same queries on a laptop over exported data:
sign-in logs, cloud security findings, alerts and audit trails in CSV
or JSON. Nothing leaves the machine, nothing needs a workspace, and
the queries carry back to Sentinel unchanged.

The examples use the sample files in `testdata/docs/` (paths relative
to the repository root): ten Entra-style sign-in events and four cloud
security findings.

## Load the exports

```kql
.set-or-append SigninLogs <| csv("./testdata/docs/signins.csv")
```

```kql
.set-or-append CloudFindings <| json("./testdata/docs/findings.json")
| project FindingId = id, Severity = severity, ResourceType = tostring(resource.type),
    Resource = tostring(resource.name), Region = tostring(resource.region), Rule = rule,
    Cves = cves, FirstSeen = firstSeen
```

`csv()` and `json()` infer the schema. For a repeatable load, declare
it with `externaldata` or `.create table` and `.ingest csv`
([files and external data](../guide/files-and-external-data.md)).

## Hunting queries

Failed sign-ins followed by a success, the shape of a password-spray
or brute-force hit:

```kql
SigninLogs
| summarize Failures = countif(ResultType != 0), Successes = countif(ResultType == 0),
    FirstFailure = minif(TimeGenerated, ResultType != 0), LastSuccess = maxif(TimeGenerated, ResultType == 0)
    by UserPrincipalName, IPAddress, Location
| where Failures >= 2 and Successes > 0 and LastSuccess > FirstFailure
| project UserPrincipalName, IPAddress, Location, Failures, Minutes = datetime_diff("minute", LastSuccess, FirstFailure)
```

```output
UserPrincipalName    IPAddress     Location  Failures  Minutes
─────────────────    ─────────     ────────  ────────  ───────
raj@contoso.example  198.51.100.7  IN        2         0
ana@contoso.example  192.0.2.44    RU        3         1
(2 rows)
```

A user signing in from a country they have not used before:

```kql
let known = SigninLogs
    | where TimeGenerated < datetime(2026-10-01 09:00)
    | summarize KnownLocations = make_set(Location) by UserPrincipalName;
SigninLogs
| where TimeGenerated >= datetime(2026-10-01 09:00) and ResultType == 0
| join kind=inner known on UserPrincipalName
| where not(set_has_element(KnownLocations, Location))
| project TimeGenerated, UserPrincipalName, Location, KnownLocations, AppDisplayName
```

```output
TimeGenerated         UserPrincipalName    Location  KnownLocations  AppDisplayName
─────────────         ─────────────────    ────────  ──────────────  ──────────────
2026-10-01T09:16:02Z  ana@contoso.example  RU        ["AU"]          Azure Portal
(1 rows)
```

The same queries work as Sentinel analytics rules: the column names
follow `SigninLogs`.

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

Because gokql databases are append-only, a triage log written as
rows keeps its own history: who decided what, when, and what changed
later.

```kql
.set-or-append Triage <| datatable(FindingId:string, Decision:string, Owner:string, Note:string)
  ["wiz-003", "fix-now", "platform", "SSH exposed with weak credentials",
   "wiz-004", "accept", "security", "purge protection planned for Q1"]
```

```kql
CloudFindings
| join kind=leftouter (Triage | summarize arg_max(_TimeReceived, *) by FindingId) on FindingId
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
should only investigate. Sentinel-specific tables and functions
(`_GetWatchlist`, ASIM parsers) are not built in; load watchlists as
tables and define the parsers you need as stored functions.

## See also

- [Files and external data](../guide/files-and-external-data.md)
- [KQL development](kql-development.md): testing analytics rules before deploying them
