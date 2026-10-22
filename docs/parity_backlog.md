# Parity Backlog — re-enumeration of 2026-10-22

The active work queue for KQL parity. Built by running every name and
form below through the CLI (`print`, `summarize`, `evaluate`, and
operator probes over a `datatable`) and recording only failures whose
error names the feature as unknown or unparsed. Reference lists:
Microsoft's scalar-function and aggregation-function index pages.

The function-name backlogs (`scalar_function_backlog.md`,
`timeseries_backlog.md`) measured names only. Most of what follows is
operator options, window functions, literal syntax and plugins, which
those lists never covered; `kql_coverage.md`'s "0 known gaps" for
tabular operators held for operator names, not for their options.

Signatures (argument and return types, optional parameters) are
checked against Microsoft's own KQL front end, `Kusto.Language`
(npm `@kusto/language-service-next`, `Kusto.Language.Bridge.js`),
where the reference pages are silent.

Priority is by frequency in Sentinel analytics rules, hunting queries
and ASIM parsers, which is this engine's main query population.

## P1 — breaks common real-world queries

| Item | Observed |
|---|---|
| none open |

## Closed

| Item | Commit notes |
|---|---|
| `union kind=inner/outer`, `withsource=`, `isfuzzy=`, `hint.concurrency`/`hint.spread`, wildcard legs (`T*`), per-type column suffixing (`x_long`, `x_int`, `x_long1`) | `applyUnion`/`buildUnionSchema`; all four union-operator reference examples exact. Standalone `union` now keeps every leg in the operator, so the first leg can carry operators and be labelled or dropped |
| `mv-expand` `kind=`/`bagexpansion=`, `with_itemindex=`, per-column `to typeof(T)`, `limit N`, legacy `mvexpand` default limit 128 | `mv_expand.go`; all seven mv-expand reference examples exact. Two existing bugs fixed: property bags were not expanded at all, and a bag zipped with an array came out null. Typed expansion is cast-only (non-conforming elements are null); `mv-apply ... to typeof(T)` now casts too, where it previously only relabelled the column |
| `lookup` with a subquery right side; `hint.*` on `summarize`, `join` and `lookup` | right side bracket-matched so an inner `on` is not taken for the lookup's; hints stripped by one shared helper |
| `summarize` output order: group-by columns, then aggregates | `byColumnsFirst`, applied to the row and columnar paths; seven tests that asserted the old positional order updated |
| `sum`/`sumif` typed as the argument promoted (int/long to long, timespan kept, real kept), exact int64 accumulation on row and columnar paths | signature from Kusto.Language (`ReturnTypeKind.Parameter0Promoted`); `partition` test that asserted real corrected |
| `datatable`/ingest timespan literals (`2d`, `1.5h`, `100ms`, `1tick`) | `types.ParseValue` |
| Static return types: 271 functions take their declared type from `Kusto.Language` (`func_signatures.go`, generated); about 100 implemented functions previously inferred as `string` (pow, sqrt, log, cos, datetime_add, totimespan, tobool, parse_csv, zip, strcmp, ipv4_compare, series_*, bag_pack, ...). `evalFunc` coerces numeric results to the declared representation, so int-typed functions (getyear, datetime_part, dayofmonth, hourofday, week_of_year, ...) now yield int and rand(N) real, as declared | probe: every fixed-return function over an empty datatable vs the declared type; remaining differences are the documented exclusions (jaccard_index, graph-match, decimal) |
| `make_list`, `make_set`, `make_bag` and `_if`/`_with_nulls` forms: optional `maxSize` (default and cap 1048576) | `aggMaxSize` |
| `datatable` string literals: single-quoted, `@'...'` verbatim and escaped forms are unquoted with the expression parser's literal rules (previously only `"..."`; single-quoted values were stored with their quotes, and a comma inside one split the row) | `convertDataTableValue`, `splitDataTableValues`. Existing scope data written through this path still holds quoted values; see the scope audit |
| `pack_all([ignore_null_empty])`; scalar `bin_at`; `bin`/`floor` rewritten on floor semantics (real sizes, negative values, pre-1970 datetimes, non-positive size gives null, Widest typing); `time(...)` | reference examples exact: pack_all, bin x3, bin_at x5 including summarize |
| Window functions: `row_number([start [, restart]])`, `prev`/`next` with offset and default, `row_cumsum`, `row_rank_dense`, `row_rank_min`, `row_window_session`; anywhere inside expressions and inside each other's arguments; in `extend`, `project`, `where` and `serialize` | `window.go`; all eight reference examples exact. Zone-map pruning skips a `where` containing a window call |
| `column_ifexists` / `columnifexists`: constant name, column type when present (default's otherwise), ADX result naming; the named column is read under projection pushdown | `func_column_ifexists.go`; reference example exact |
| `let` functions: tabular bodies (including `print`, `range`, inner `let`s), tabular parameters `(T:(*))` and `(T:(col:type))`, default parameter values (scalar and tabular functions), parameterised `view`s, `invoke` of let functions | let functions resolve through the stored-function binder; a let function shadows a stored one of the same name |
| `column_names_of(expr)` | the three reference examples exact |
| Literals: hex `0x1F`, `real(+inf)`/`real(-inf)`/`real(nan)`, unquoted `guid(...)`, obfuscated `h"..."`/`h@'...'`, adjacent string concatenation `'a' 'b'` | expression parser |
| `//` comments in any position (previously only where a parser happened to ignore trailing text) | stripped once at `parser.Parse`, outside string literals; `.ingest inline` payloads exempt |
| `materialize(expr)` as a let value or query source | a tabular let is already evaluated once |
| `mv-apply`: several zipped columns, any array expression, per-column `to typeof()`, `with_itemindex=`, `limit`; output carries the input row's columns ahead of the subquery's; bags are one element | all seven reference examples exact |
| String operators (`has`, `contains`, `startswith`, `endswith`, `hasprefix`, `like`, `matches regex`, negations, `_cs` forms) take null as `""`; previously any null operand gave false, so `null !startswith "x"` was false | `isStringOperator` |
| `range()`, `cot`, `erf`, `erfc`, `gamma`, `loggamma`, `replace` (deprecated `replace_regex` order), `assert` (constant arguments checked once per operator, so it fails with no rows) | reference examples exact (range x5 incl. the 2^20 cap, gamma, assert x3) |
| Named arguments in function calls (`f(len=3, s)`); user function names are case sensitive (a scalar let function named with capitals, e.g. `checkLength`, could not be called) | `FuncCall.ArgNames`, `FuncCall.RawName` |
| Bracket access with any expression: computed and negative indexes (`r[array_length(r) - 1]`, `r[-1]`) and keys from columns (`d[key]`) | `AccessKey.Expr`; every expression walker visits it, including projection pushdown |
| `top-nested`: levels with N or all values, named or `aggregated_<Expr>` columns, `asc`/`desc`, `with others` (deeper levels under the others row are null; undocumented) | reference example 4 exact; example 1 shape |
| `convert_angle`, `_energy`, `_force`, `_length`, `_mass`, `_speed`, `_temperature`, `_volume`: all 221 documented units (factors generated from unitsnet-js by `tools/gen-units/gen.js`; `Tilt` is the sine of the angle) | the eight reference examples exact to ADX's 15 displayed digits. AcreFoot overridden to the rounded factor ADX's output shows. Other units carry current UnitsNet factors and could differ in late digits where ADX's UnitsNet version rounded differently |
| `view` lambdas | parameterless views bind as tabular lets and are in scope for union wildcards; views with parameters bind as functions |
| Scalar bitwise: `binary_and`, `binary_or`, `binary_xor`, `binary_not`, `binary_shift_left`, `binary_shift_right`, `bitset_count_ones` | `func_binary.go`; all eight reference-page examples, shift modulo 64, negative shift gives null, arithmetic right shift |

## P2 — real but less frequent

- Scalars: `beta_cdf`, `beta_inv`, `beta_pdf`, `welch_test`.
- `as hint.materialized=true`.
- Operators previously labelled out of scope that have no cluster
  dependency: `fork`, `facet`.
- `evaluate` plugins. Only `bag_unpack` exists. Deterministic,
  documented-output plugins: `pivot`, `narrow`, `ipv4_lookup`,
  `ipv6_lookup`, `schema_merge`, `sequence_detect`,
  `rolling_percentile`, `sliding_window_counts`,
  `active_users_count`, `activity_counts_metrics`,
  `activity_engagement`, `new_activity_metrics`, `session_count`,
  `funnel_sequence`, `funnel_sequence_completion`,
  `dcount_intersect`. Pattern-mining plugins (`autocluster`,
  `basket`, `diffpatterns`) have the same specification problem as
  `reduce` and need their algorithm pinned before work starts.

## P3 — carried over

Decimal type (`todecimal`, `parse_version`), `hash_combine`/
`hash_many`, punycode, `percentilew`/`percentilesw`/
`percentilesw_array`, `buildschema`, `series_stats` and multi-column
assignment, series curve fitting and signal processing,
`datetime_list_timezones`, `reduce`. Details in the existing backlog
documents.

## Out of scope (unchanged)

Cluster, cursor and principal introspection; geo; HLL and T-Digest
sketches. See `scalar_function_backlog.md`.
