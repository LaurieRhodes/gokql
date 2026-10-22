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

Priority is by frequency in Sentinel analytics rules, hunting queries
and ASIM parsers, which is this engine's main query population.

## P1 — breaks common real-world queries

| Item | Observed |
|---|---|
| `union isfuzzy=true`, `union withsource=Col`, `union kind=` | option parsed as a table name |
| `mv-expand` options: `kind=`, `bagexpansion=`, `with_itemindex=`, `to typeof(T)`, `limit N` | each rejected by the parser |
| `mv-apply ... = <expression>` (only a bare column works) | "column not found" |
| `lookup` with a parenthesised subquery on the right (`join` accepts it) | right side resolved as a table name |
| `hint.*` on `summarize` and `join` (`hint.strategy=shuffle`, `hint.shufflekey=`, `hint.strategy=broadcast`) | hint parsed as an aggregate or rejected |
| Window functions in `extend`/`project` after `serialize` (only `serialize x = f()` works) | "only works inside serialize" |
| `row_cumsum`, `row_rank_dense`, `row_rank_min`, `row_window_session` | unsupported |
| `column_ifexists`, `column_names_of` | unsupported; schema-aware evaluation needed |
| `pack_all()` | unsupported |
| `bin_at()` as a scalar (make-series has its own path) | unsupported |
| `make_list`/`make_set` optional `MaxSize` argument | "requires 1 argument" |
| Tabular parameter with wildcard schema, `(T:(*))` | "unknown KQL type" |
| Default parameter values, `(x:long = 2)` | "unknown KQL type" |
| `view` lambdas, `let f = view () { ... }` | parse error |
| `materialize()` | parse error |
| `bag_pack(...)` typed as string by `inferExprType`, so a following `evaluate bag_unpack` rejects it | type inference bug |
| Literals: hex `0x1F`, `guid(...)`, `real(+inf)`/`real(-inf)`/`real(nan)`, obfuscated `h"..."`, adjacent string-literal concatenation `'a' 'b'` | parse errors |

## Closed

| Item | Commit notes |
|---|---|
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
