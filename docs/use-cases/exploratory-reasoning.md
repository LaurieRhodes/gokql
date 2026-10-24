# Exploratory reasoning: varying definitions to find what else is true

Most knowledge stores record conclusions. This pattern records the
reasoning: observations, assumptions, hypotheses and tests are nodes,
and the if/then relations between them are edges ("this hypothesis
holds only if that assumption does", "this test supports that
hypothesis"). The engine then does three things a person or an AI
assistant cannot easily do by hand:

1. **Generate variants.** Each hypothesis is built from a few
   definitions (what counts as a mark, what unit it counts, where
   counting starts). Listing the alternatives for each definition as
   data lets a query generate every combination as its own hypothesis.
2. **Test them all, and keep every result.** Each variant is tested by
   a query and the result is written back as a node and an edge.
   Nothing is deleted, so variants that failed stay visible and prune
   the space.
3. **Propagate doubt.** When an assumption is put in doubt, every
   hypothesis that rests on it becomes stale at once, and a frontier
   query says what to verify or re-test next.

The result is a way to learn things that were not the question: a
variant nobody expected can fit best, and a conclusion can turn out to
rest on an assumption nobody had checked. It suits an AI assistant
well: it can propose new definitions, run the loop, and read the
frontier, while every step stays in the record for a person to audit.

The method was developed for exploratory work on a real
archaeological artefact. This page shows it on an invented one.

## The puzzle

A carved bone has eight groups of notches. Some notches are faint. Is
it a count, and of what?

```kql
.set-or-append Marks <| datatable(Group:long, Clear:long, Faint:long)
  [1, 7, 1,  2, 8, 0,  3, 7, 0,  4, 7, 1,  5, 8, 0,  6, 7, 0,  7, 8, 0,  8, 7, 1]
```

## The logic graph

Two tables hold the reasoning. `Nodes` are things believed, assumed or
tested; `Edges` relate them. Both are append-only: a changed status is
a new row, and the latest row of each node wins.

```kql
.create-merge table Nodes (NodeId: string, Kind: string, Title: string, Status: string, Signature: string, Params: dynamic, Metrics: dynamic, SessionId: string)
```

```kql
.create-merge table Edges (EdgeId: string, Src: string, Rel: string, Dst: string, Basis: string, Status: string, SessionId: string)
```

```kql
.create-or-alter function LogicN() { Nodes | summarize arg_max(_TimeReceived, *) by NodeId }
```

```kql
.create-or-alter function LogicE() { Edges | summarize arg_max(_TimeReceived, *) by EdgeId | where Status != "retracted" }
```

| `Kind` | Meaning | Statuses |
|---|---|---|
| `Observation` | Something seen | `observed` |
| `Assumption` | Something taken as true without proof | `unverified`, `verified`, `in-doubt`, `refuted` |
| `Hypothesis` | A claim to test, built from definitions | `open`, `supported`, `weakened`, `refuted` |
| `Test` | A test of one hypothesis and its result | its verdict |

| `Rel` | Reads as |
|---|---|
| `assumes` | Src holds only if Dst holds |
| `supports`, `weakens`, `refutes` | Src (a test or observation) bears on Dst |

The observation and the assumptions any reading of the bone needs:

```kql
.set-or-append Nodes <| datatable(NodeId:string, Kind:string, Title:string, Status:string, Signature:string, Params:dynamic, Metrics:dynamic, SessionId:string)
  ["O-MARKS", "Observation", "Eight groups of notches; three faint notches", "observed", "", dynamic({}), dynamic({}), "s1",
   "A-ONE-PER-DAY", "Assumption", "Each notch records one day", "unverified", "", dynamic({}), dynamic({}), "s1",
   "A-FIRST-COMPLETE", "Assumption", "The first group is complete", "unverified", "", dynamic({}), dynamic({}), "s1",
   "A-FAINT-ACCIDENTAL", "Assumption", "Faint notches are accidental", "unverified", "", dynamic({}), dynamic({}), "s1",
   "A-FAINT-INTENDED", "Assumption", "Faint notches are intended marks", "unverified", "", dynamic({}), dynamic({}), "s1"]
```

## Definitions as data

A hypothesis about the bone depends on three definitions, each with
alternatives. Each alternative carries the number the test needs, a
prior (how expected it is), and the assumption it brings with it:

```kql
.set-or-append Axes <| datatable(Axis:string, Value:string, Number:real, Prior:real, Assumes:string)
  ["reading", "strict", 0, 0, "A-FAINT-ACCIDENTAL",
   "reading", "inclusive", 0, 0, "A-FAINT-INTENDED",
   "cycle", "week", 7.0, 0.6, "",
   "cycle", "lunar_quarter", 7.3826, 0.2, "",
   "cycle", "half_month", 14.7653, 0.2, "",
   "start", "first_group", 0, 0, "A-FIRST-COMPLETE",
   "start", "second_group", 1, 0, ""]
```

- **reading**: count only clear notches, or faint ones too.
- **cycle**: what one group stands for: a week, a quarter of the
  lunar month (7.38 days), or half a lunar month.
- **start**: count from the first group, or from the second, in case
  the first is incomplete.

Every combination is a hypothesis. Its `Signature` lists its
definitions, and its assumptions follow from them:

```kql
.create-or-alter function Variants() {
    let r = Axes | where Axis == "reading" | project Reading = Value, RA = Assumes, k = 1;
    let c = Axes | where Axis == "cycle" | project Cycle = Value, Length = Number, Prior, k = 1;
    let s = Axes | where Axis == "start" | project Start = Value, Skip = tolong(Number), SA = Assumes, k = 1;
    r | join kind=inner c on k | join kind=inner s on k
    | extend Signature = strcat("reading=", Reading, ";cycle=", Cycle, ";start=", Start)
    | extend NodeId = strcat("H-", Reading, "-", Cycle, "-", Start)
    | project NodeId, Signature, Reading, Cycle, Length, Start, Skip, Prior,
        Assumes = pack_array("A-ONE-PER-DAY", RA, SA)
}
```

## The exploration loop

Four commands make the loop. Each only adds what is missing, so the
same four can be run again whenever a definition is added.

Write the new hypotheses:

```kql
.set-or-append Nodes <| Variants()
| join kind=leftanti (Nodes | where Kind == "Hypothesis" | distinct NodeId) on NodeId
| project NodeId, Kind = "Hypothesis",
    Title = strcat("The notches count days in ", Cycle, "s, read ", Reading, ", from the ", Start),
    Status = "open", Signature, Params = bag_pack("prior", Prior), Metrics = dynamic({}), SessionId = "s1"
```

Record what each one assumes:

```kql
.set-or-append Edges <| Variants()
| mv-expand A = Assumes to typeof(string)
| where isnotempty(A)
| project EdgeId = strcat(NodeId, ">", A), Src = NodeId, Rel = "assumes", Dst = A, Basis = "", Status = "asserted", SessionId = "s1"
| join kind=leftanti (Edges | distinct EdgeId) on EdgeId
```

The test: if each group stands for one cycle, the running total of
notches after group *n* should be close to *n* cycle lengths. The
root-mean-square miss, in days, is the result:

```kql
.create-or-alter function TestVariants() {
    let marks = Marks | order by Group asc | extend StrictCum = row_cumsum(Clear), AllCum = row_cumsum(Clear + Faint);
    let first = toscalar(Marks | where Group == 1 | project Clear);
    let firstAll = toscalar(Marks | where Group == 1 | project Clear + Faint);
    Variants() | extend k = 1
    | join kind=inner (marks | extend k = 1) on k
    | where Group > Skip
    | extend Cum = iff(Reading == "strict", StrictCum, AllCum), Base = iff(Skip == 0, 0, iff(Reading == "strict", first, firstAll))
    | extend Residual = (Cum - Base) - (Group - Skip) * Length
    | summarize Rms = round(sqrt(avg(Residual * Residual)), 2) by NodeId
    | extend Verdict = case(Rms < 1, "supports", Rms < 5, "weakens", "refutes")
}
```

Run the tests not yet run, and record each as a test node, a verdict
edge, and a new status for its hypothesis:

```kql
.set-or-append Edges <| TestVariants()
| join kind=leftanti (Edges | where Rel in ("supports", "weakens", "refutes") | project NodeId = Dst) on NodeId
| project EdgeId = strcat("T-", NodeId, ">", NodeId), Src = strcat("T-", NodeId), Rel = Verdict, Dst = NodeId,
    Basis = strcat("rms ", tostring(Rms), " days"), Status = "asserted", SessionId = "s1"
```

```kql
.set-or-append Nodes <| TestVariants()
| join kind=inner (LogicN() | where Kind == "Hypothesis" and Status == "open") on NodeId
| project NodeId, Kind, Title, Status = case(Verdict == "supports", "supported", Verdict == "weakens", "weakened", "refuted"),
    Signature, Params, Metrics = bag_pack("rms_days", Rms), SessionId = "s1"
```

## What the exploration found

```kql
LogicN()
| where Kind == "Hypothesis"
| project NodeId, Status, RmsDays = todouble(Metrics.rms_days)
| order by RmsDays asc
```

```output
NodeId                                  Status     RmsDays
──────                                  ──────     ───────
H-strict-lunar_quarter-first_group      supported  0.3
H-strict-lunar_quarter-second_group     supported  0.43
H-inclusive-lunar_quarter-second_group  weakened   1.35
H-inclusive-lunar_quarter-first_group   weakened   1.81
H-strict-week-first_group               weakened   1.9
H-strict-week-second_group              weakened   2.04
H-inclusive-week-second_group           weakened   3.05
H-inclusive-week-first_group            weakened   3.72
H-inclusive-half_month-second_group     refuted    31.69
H-strict-half_month-second_group        refuted    32.72
H-inclusive-half_month-first_group      refuted    35.5
H-strict-half_month-first_group         refuted    37.33
(12 rows)
```

Weeks were the expected answer, and they fit worse than lunar
quarters: read strictly, the bone counts two lunar months to within
half a day.

## Questions the graph answers

**Is a whole region of definitions dead?** A signature fragment
selects every variant that shares those definitions:

```kql
.create-or-alter function Region(sig: string) {
    let parts = split(sig, ";");
    LogicN()
    | where Kind == "Hypothesis" and array_length(set_difference(parts, split(Signature, ";"))) == 0
    | summarize Variants = count(), Statuses = make_set(Status), BestRmsDays = min(todouble(Metrics.rms_days))
}
```

```kql
Region("cycle=half_month")
```

```output
Variants  Statuses     BestRmsDays
────────  ────────     ───────────
4         ["refuted"]  31.69
(1 rows)
```

```kql
Region("cycle=lunar_quarter;reading=strict")
```

```output
Variants  Statuses       BestRmsDays
────────  ────────       ───────────
2         ["supported"]  0.3
(1 rows)
```

**What was surprising?** A good fit for something unexpected is worth
more attention than a good fit for what everyone assumed. The score
here, one minus the prior times a fit term, is one simple choice; any
ranking that rewards low prior and good fit will do:

```kql
.create-or-alter function Surprise() {
    LogicN()
    | where Kind == "Hypothesis" and Status == "supported"
    | extend Prior = todouble(Params.prior), RmsDays = todouble(Metrics.rms_days)
    | extend Surprise = round((1 - Prior) * exp(-RmsDays), 2)
    | project NodeId, Prior, RmsDays, Surprise
    | order by Surprise desc
}
```

```kql
Surprise()
```

```output
NodeId                               Prior  RmsDays  Surprise
──────                               ─────  ───────  ────────
H-strict-lunar_quarter-first_group   0.2    0.3      0.59
H-strict-lunar_quarter-second_group  0.2    0.43     0.52
(2 rows)
```

Testing many variants and keeping the best invites false discoveries.
The record makes that visible: the number of variants tested in a
family is a count over `Nodes`, and a significance level can be
corrected by it before anything is reported.

## Doubt propagates

A closer look shows the bone is broken just before the first group.
The first group may have lost notches, so the assumption that it is
complete is now in doubt. Record the observation and the change:

```kql
.set-or-append Nodes <| datatable(NodeId:string, Kind:string, Title:string, Status:string, Signature:string, Params:dynamic, Metrics:dynamic, SessionId:string)
  ["O-BREAK", "Observation", "The bone is broken just before the first group", "observed", "", dynamic({}), dynamic({}), "s2",
   "A-FIRST-COMPLETE", "Assumption", "The first group is complete", "in-doubt", "", dynamic({}), dynamic({}), "s2"]
```

```kql
.set-or-append Edges <| datatable(EdgeId:string, Src:string, Rel:string, Dst:string, Basis:string, Status:string, SessionId:string)
  ["O-BREAK>A-FIRST-COMPLETE", "O-BREAK", "weakens", "A-FIRST-COMPLETE", "notches before the break may be lost", "asserted", "s2"]
```

A hypothesis whose premise is in doubt is stale, whatever its test
said:

```kql
.create-or-alter function Effective() {
    let doubted = LogicN() | where Kind in ("Assumption", "Observation") and Status in ("in-doubt", "refuted") | project Dst = NodeId;
    let broken = LogicE() | where Rel == "assumes" | join kind=inner doubted on Dst | summarize BrokenPremises = make_set(Dst) by NodeId = Src;
    LogicN()
    | where Kind == "Hypothesis"
    | join kind=leftouter broken on NodeId
    | extend Effective = iff(array_length(BrokenPremises) > 0 and Status != "refuted", "stale", Status)
    | project NodeId, Status, Effective, BrokenPremises, RmsDays = todouble(Metrics.rms_days)
}
```

```kql
Effective()
| where Status == "supported"
```

```output
NodeId                               Status     Effective  BrokenPremises        RmsDays
──────                               ──────     ─────────  ──────────────        ───────
H-strict-lunar_quarter-first_group   supported  stale      ["A-FIRST-COMPLETE"]  0.3
H-strict-lunar_quarter-second_group  supported  supported                        0.43
(2 rows)
```

The best-fitting variant counted from the first group and is now
stale; the one that starts at the second group does not depend on the
broken edge and still holds. The conclusion survives the doubt, which
is itself a finding.

## What to do next

The frontier ranks the work: assumptions that the most live
hypotheses rest on, then hypotheses to re-test:

```kql
.create-or-alter function Frontier() {
    let live = Effective() | where Effective != "refuted" | project Src = NodeId;
    let leverage = LogicE() | where Rel == "assumes" | join kind=inner live on Src | summarize Leverage = dcount(Src) by NodeId = Dst;
    let verify = LogicN() | where Kind == "Assumption" and Status in ("unverified", "in-doubt") | join kind=inner leverage on NodeId
        | project Priority = 1, Action = "verify", Target = NodeId, Leverage, Detail = Title;
    let retest = Effective() | where Effective == "stale" and Status == "supported"
        | project Priority = 2, Action = "re-test", Target = NodeId, Leverage = 0, Detail = strcat("premise in doubt: ", strcat_array(BrokenPremises, ", "));
    union verify, retest
    | order by Priority asc, Leverage desc, Target asc
}
```

```kql
Frontier()
```

```output
Priority  Action   Target                              Leverage  Detail
────────  ──────   ──────                              ────────  ──────
1         verify   A-ONE-PER-DAY                       8         Each notch records one day
1         verify   A-FAINT-ACCIDENTAL                  4         Faint notches are accidental
1         verify   A-FAINT-INTENDED                    4         Faint notches are intended marks
1         verify   A-FIRST-COMPLETE                    4         The first group is complete
2         re-test  H-strict-lunar_quarter-first_group  0         premise in doubt: A-FIRST-COMPLETE
(5 rows)
```

"Each notch records one day" carries the most weight and has never
been checked: that is where the next effort is best spent.

## Adding a definition

New ideas enter as data. Suppose the groups are ten-day periods:

```kql
.set-or-append Axes <| datatable(Axis:string, Value:string, Number:real, Prior:real, Assumes:string)
  ["cycle", "ten_days", 10.0, 0.1, ""]
```

Running the same four loop commands again generates and tests only the
four new variants:

<details>
<summary>The loop, run again</summary>

```kql
.set-or-append Nodes <| Variants()
| join kind=leftanti (Nodes | where Kind == "Hypothesis" | distinct NodeId) on NodeId
| project NodeId, Kind = "Hypothesis",
    Title = strcat("The notches count days in ", Cycle, "s, read ", Reading, ", from the ", Start),
    Status = "open", Signature, Params = bag_pack("prior", Prior), Metrics = dynamic({}), SessionId = "s3"
```

```kql
.set-or-append Edges <| Variants()
| mv-expand A = Assumes to typeof(string)
| where isnotempty(A)
| project EdgeId = strcat(NodeId, ">", A), Src = NodeId, Rel = "assumes", Dst = A, Basis = "", Status = "asserted", SessionId = "s3"
| join kind=leftanti (Edges | distinct EdgeId) on EdgeId
```

```kql
.set-or-append Edges <| TestVariants()
| join kind=leftanti (Edges | where Rel in ("supports", "weakens", "refutes") | project NodeId = Dst) on NodeId
| project EdgeId = strcat("T-", NodeId, ">", NodeId), Src = strcat("T-", NodeId), Rel = Verdict, Dst = NodeId,
    Basis = strcat("rms ", tostring(Rms), " days"), Status = "asserted", SessionId = "s3"
```

```kql
.set-or-append Nodes <| TestVariants()
| join kind=inner (LogicN() | where Kind == "Hypothesis" and Status == "open") on NodeId
| project NodeId, Kind, Title, Status = case(Verdict == "supports", "supported", Verdict == "weakens", "weakened", "refuted"),
    Signature, Params, Metrics = bag_pack("rms_days", Rms), SessionId = "s3"
```

</details>

```kql
Region("cycle=ten_days")
```

```output
Variants  Statuses     BestRmsDays
────────  ────────     ───────────
4         ["refuted"]  10.39
(1 rows)
```

The idea is tested and recorded in seconds, and the record now says
that ten-day periods were considered and why they were dropped.

## Using it with an AI assistant

The loop divides the work well. The assistant proposes definitions
(new axis values, new assumptions), runs the loop, reads `Frontier()`
and `Surprise()`, and writes what it concludes as observations and
assumption changes. A person reviews the surprises and the
assumptions with the most leverage, which is where judgement matters.
Because every variant, test and change of status is a row with a
session and a time, the path to any conclusion can be replayed.

The same tables work for any domain where a claim rests on choices of
definition: which events count as an incident, which threshold marks a
failure, which baseline a measurement is taken against.

## See also

- [Research knowledge graphs](research-graphs.md): claims linked to evidence
- [Agent memory](agent-memory.md): the append-only discipline this builds on
- [Graphs](../guide/graphs.md): querying the same edges with `graph-match`, GQL or openCypher
