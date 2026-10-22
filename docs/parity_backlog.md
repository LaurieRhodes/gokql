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
| `mv-apply ... = <expression>` (only a bare column works) | "column not found" |
| Window functions in `extend`/`project` after `serialize` (only `serialize x = f()` works) | "only works inside serialize" |
| `row_cumsum`, `row_rank_dense`, `row_rank_min`, `row_window_session` | unsupported |
| `column_ifexists`, `column_names_of` | unsupported; schema-aware evaluation needed |
| `pack_all()` | unsupported |
| `bin_at()` as a scalar (make-series has its own path) | unsupported |
| Tabular parameter with wildcard schema, `(T:(*))` | "unknown KQL type" |
| Default parameter values, `(x:long = 2)` | "unknown KQL type" |
| `materialize()` | parse error |
| Literals: hex `0x1F`, `guid(...)`, `real(+inf)`/`real(-inf)`/`real(nan)`, obfuscated `h"..."`, adjacent string-literal concatenation `'a' 'b'` | parse errors |

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
| `view` lambdas | parameterless views bind as tabular lets and are in scope for union wildcards; views with parameters bind as functions |
| Scalar bitwise: `binary_and`, `binary_or`, `binary_xor`, `binary_not`, `binary_shift_left`, `binary_shift_right`, `bitset_count_ones` | `func_binary.go`; all eight reference-page examples, shift modulo 64, negative shift gives null, arithmetic right shift |

## P2 — real but less frequent

- Scalars: `range()` (array generator), `cot`, `replace` (deprecated
  alias of `replace_regex`), `assert`, `erf`, `erfc`, `gamma`,
  `loggamma`, `beta_cdf`, `beta_inv`, `beta_pdf`, `welch_test`.
- Units conversion family, not in any earlier list: `convert_angle`,
  `convert_energy`, `convert_force`, `convert_length`, `convert_mass`,
  `convert_speed`, `convert_temperature`, `convert_volume`.
- `as hint.materialized=true`.
- Operators previously labelled out of scope that have no cluster
  dependency: `top-nested`, `fork`, `facet`.
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
- `print x = "s" // comment` on a single line (comments work elsewhere).

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
