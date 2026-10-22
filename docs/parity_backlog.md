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
| Stored numeric nulls | `int`/`long`/`real` nulls are written to extents as 0: `vortex_bridge.go` builds the primitive arrays non-nullable, with no validity buffer. `isnull()` never matches a stored numeric column, and comparisons see 0 instead of null. Query-time nulls (datatable, print, computed columns) are correct. Fixing it needs validity support in vortex-go and the bridge, a storage-format change |

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
| `punycode_from_string`, `punycode_to_string`, `punycode_domain_to_string`, `punycode_domain_from_string` | `func_punycode.go`, RFC 3492 without IDNA folding; all four references exact (including `。`/`｡` label separators and preserved case) |
| `series_stats` with multi-column assignment `(a, b) = series_stats(x)` and unnamed expansion to `series_stats_x_min` ... `series_stats_x_variance` | `multi_column.go` expands it into per-statistic reads of series_stats_dynamic; reference exact. Unnamed statistics keep their generated names when only some are named, as Kusto.Language does |
| `series_fit_line`, `series_fit_line_dynamic`, `series_fit_poly` (x series, degree) | `func_series_fit.go` with the multi-column expansion; series_fit_line reference exact (x = 1..n, sample variance, rvariance = SSres/(n-1)); polynomial coefficients checked against numpy.polyfit |
| `buildschema` | `agg_buildschema.go`; reference exact, key and alternative order by first appearance |
| `fork` (`Name =`, `as` in the subquery, GenericResult otherwise) and `facet by ... [with (...)]` (count_<Column> tables) | `fork_facet.go`; `Engine.ExecuteAll` returns every result table, the CLI prints each under `[Name]`, the server returns Table_0, Table_1, ... Both must be the last operator |
| Generated column names follow Kusto.Language everywhere: unnamed extend/project expressions (`strlen(s)` → `strlen_s`, `tostring(x)` → `x`, `d.a.b` → `d_a_b`, otherwise `ColumnN` skipping existing names), print (`print_N`), summarize by (`ColumnN`), aggregates (`make_list(x)` → `list_x`, `take_any(x)` → `x`, `binary_all_and(x)` → `x`), duplicates suffixed (`count_`, `count_1`). Previously the expression text (`x + 1`, `strlen('a')`) | `result_names.go`; the per-function name kinds are generated from Kusto.Language by `tools/gen-result-names/gen.js` |
| `arg_max`/`arg_min` returned only the returned expressions; now the maximized expression first, then the returned ones (the reference's `State, BeginLat, BeginLocation`), with written and tuple names applied in order. `take_any(x, y)` and `take_any(*)` added. Summarize aggregate names given as `['a b'] = ...`; `countif(x >= 3)` no longer misparsed as an assignment | `pickRowColumns` (operators.go). **Changes the output of a query that names a multi-column arg_max/arg_min** (`Best = arg_min(p, NodeId)` now labels `p` Best, as ADX does) |
| `beta_cdf`, `beta_inv`, `beta_pdf`, `welch_test` | `func_beta.go`; the reference examples to ADX's 15 displayed digits (the pdf example is the exact rational value), other points against SciPy to 1e-14. beta_inv follows its reference example where the page's prose contradicts it (p > 1 is null, alpha <= 0 NaN). welch_test is two-sided with Welch–Satterthwaite df: 0.021 on the reference data |
| `extend a = x + 1, b = a * 2`: a later assignment can use a column an earlier one defines (in order, as Kusto.Language resolves it; project, print and summarize do not allow this). Previously `column "a" not found` | `extendSplitPoint` splits the extend and applies the parts in sequence |
| `hll`, `hll_if`, `hll_merge` (aggregate and scalar), `dcount_hll`, `tdigest` (with weight), `tdigest_merge`/`merge_tdigest` (aggregate and scalar), `percentile_tdigest` (with `typeof(T)`), `percentiles_array_tdigest`, `percentrank_tdigest`, `rank_tdigest`; `evaluate dcount_intersect` | `func_sketch.go`. Exact sketches (the distinct set, the weighted sample) since dcount and percentiles here are exact; the dynamic values are this engine's own, not ADX's binary sketches. dcount_intersect reference example exact. Types and aggregate names (`merge_tdigests_t`) from Kusto.Language |
| `evaluate sequence_detect` | `plugin_sequence_detect.go`; the reference output reproduces from an input reconstructed to contain its rows plus distractors. Every chain within the windows is reported, as the reference shows |
| `evaluate activity_counts_metrics`, `activity_metrics`, `active_users_count`, `new_activity_metrics` (Cohort and Lookback arguments); `week`/`month`/`year` steps | `plugin_activity.go`; activity_counts_metrics, active_users_count and new_activity_metrics example 1 exact; activity_metrics' examples use rand(), so only its stated formulas and NaN first row are checked. active_users_count needs the lookback window to exclude its lower bound, (t - LookbackWindow, t], to reproduce the reference. Periods use exact int64 arithmetic (a float64 datetime cannot hold `End - 1tick`) |
| `evaluate sliding_window_counts`, `rolling_percentile`, `schema_merge` | `plugin_rolling.go`, `plugin_schema_merge.go`; every reference example exact (sliding_window_counts 1, rolling_percentile 2, schema_merge 3). Output schemas from Kusto.Language (`Count`/`Dcount`). Unpinned: bins whose window is empty are not reported; Bin strings week/month/year unsupported; schema_merge's type-reduction rules beyond int/long/real and its mismatch message |
| Arithmetic and comparison on mixed numeric types: `2 * 1.5` gave 2, `x + 0.5` and `x < 1.5` truncated the real to the long's integer, `int` operands failed (`unsupported operation: int32 + int32`), `1h * 1.5` truncated the factor, `90m / 1h` divided as integers; `2 * 1h` was typed long. Present since before this backlog | `evalMixedNumeric` and `numericPromote` (eval.go); result types follow Kusto.Language (int arithmetic widens to long; timespan times or divided by a number is a timespan; timespan / timespan is real) |
| `percentile`/`percentiles`/`percentiles_array` interpolated (median of 1..10 was 5.5) and were always real; columns were named `percentile_x` | nearest-rank as ADX documents (median 5), result keeps the input type (int widens to long; datetime and timespan work), columns `percentile_x_50`, `percentile_x_99_5`, `percentile__50` for a non-column expression; a written name applies to the first column, `(a, b) = percentiles(...)` names each. `percentilew`, `percentilesw`, `percentilesw_array` added (weight = repetitions). ADX's T-Digest estimate can differ from the exact rank on large populations |
| `tostring`/`strcat`/`strcat_delim` of datetime and timespan returned the raw integer (`1577836800000000000`) | now `2020-01-01T00:00:00Z` (seven fractional digits when not a whole second) and `[-][d.]hh:mm:ss[.fffffff]`; the static type picks the format (`kql_tostring.go`). The whole-second datetime form is the one ADX shows; not pinned by a reference example |
| Bracket-quoted identifiers `['name']` / `["name"]` in expressions (with `.p`/`['k']` access after them), assignment names, `project`, `project-away/-rename/-reorder/-keep`, datatable and column-definition schemas; previously a parse error or a column literally named `['x']` | `unquoteIdent` in the parser |
| `evaluate pivot` (default `count()`, any aggregate, explicit or default identifier columns, `: OutputSchema`) and `evaluate narrow` | `plugin_pivot.go`; the five pivot reference examples' shapes on a small table. Pivot columns in first-appearance order; an empty cell is 0 for count/dcount/sum, `[]`/`{}` for make_list/make_set/make_bag, otherwise null. An empty pivot value names its column `Column1` (unverified) |
| `evaluate ipv4_lookup` / `ipv6_lookup`: lookup table as any tabular expression, extra key columns, `return_unmatched` (named or positional); every matching prefix is returned, in lookup-table order. `as hint.materialized=true T` parses | `plugin_ip_lookup.go`; the three ipv4_lookup reference examples exact (`external_data` replaced by an equivalent datatable) |
| `search` (ADX form): `[T \|] search [kind=case_sensitive] [in (T1, T*)] Predicate`; bare terms (has/hasprefix/hassuffix/contains/regex by wildcard position), `Col:"term"`, `Col == ...`, `matches regex`, `and`/`or`/`not`, `search *`; every column via tostring; output `$table` + union kind=outer schema. Replaces the gokql hit-list form (TableName, Column, RowKey, Value) | `search.go`; the reference syntax table and examples; output schema checked against Kusto.Language (`$table, a, b_long, b_string, n, c` for heterogeneous tables) |
| `union` kind=outer: a name's typed variants together at the name's first-seen position (`a, b_long, b_string, n, c`) | `buildUnionSchema`; both leg orders against Kusto.Language |
| `sort by ['quoted name']` | `parseOrderBy` |
| `decimal`: `TypeDecimal` (exact base-10, 38 digits; + − × exact, ÷ to 28 places), `decimal(...)` literals from source text, `todecimal`, `parse_version`, comparison and arithmetic with numbers (decimal dominates), `sum`/`avg`/`min`/`max`/`-if` exact, `abs`/`round`/`floor`/`bin`/`ceiling`, storage as `kql.decimal` text, REST cells as strings | `pkg/types/decimal.go`, `pkg/engine/decimal.go`; todecimal and both parse_version references exact; result types per Kusto.Language |
| Unary minus on any operand (`-(x)`, `-abs(x)`); previously only before a numeric literal | `parsePrimary` |
| Result types of `iff`/`iif`/`case`/`coalesce`/`max_of`/`min_of` (common type of the results; were always string, splitting union columns and sorting numbers as text) and of `abs`/`round`/`ceiling`, `real()`/`double()`/`long()`/`int()` | `unifyResultTypes`; Kusto.Language result types. Two odm-pendants stored functions (LogicFrontier, LogicBrief) that failed on the string-typed `coalesce` now run |
| Graph core: `graph-match` comma-separated sequences sharing node variables, repeated variables, anonymous `()`/`-->`/`<--`/`--`/`-[*a..b]->`, `*0..n`, `cycles=none/all/unique_edges`, `map`/`all`/`any`/`inner_nodes`; `make-graph with_node_id=` and two node tables | `graph_match.go` (traversal planned from the bound end of each element), `graph_parse.go`; every graph-match / make-graph / map / all / any / inner_nodes reference example exact (`graph_compliance_test.go`). Found on the way: `set_union`/`set_intersect`/`set_difference` sorted their result; ADX keeps first-seen order |
| Graph operators: `graph-shortest-paths` (output=any/all), `graph-mark-components` (weak/strong), `graph-to-table` with_node_id/with_source_id/with_target_id (hash ids) and `nodes as N, edges as E`, `make-graph ... partitioned-by Col (...)`; query batches (`Q1; Q2` returns both results, `as` names visible to later statements); `geo_distance_2points` | `graph_components.go`, `graph_match.go` (shared walker), `graph.go`; every reference example of the four operators exact (`graph_ops_test.go`). Found on the way: a batch silently returned only its last statement's result; a line break inside a parenthesised sub-pipeline (fork, partitioned-by) failed to parse; graph-to-table node order now follows ADX (node-table rows, then edge-only nodes) |
| Persistent graph models: `.create-or-alter graph_model` (versions), `.show graph_model(s)`, `.drop graph_model`, `.make [async] graph_snapshot`, `.show graph_snapshot(s)`, `.drop graph_snapshot(s)`, `graph("M" [, "S"])`, `labels()` | `graph_model.go`, `graph_model_parse.go`; append-only system tables like `_Functions`, snapshot data frozen per snapshot GUID with typed values round-tripping exactly; the graph-sample-data and labels() reference queries return their documented rows over equivalent data (`graph_model_test.go`). `.show graph_*` commands can be piped |
| Docs sweep of every graph code block (dataexplorer-docs, 153 blocks): node_id/node_degree_in/node_degree_out, `graph()` snapshot=/bool arguments, let-bound and function-returned graphs, scalar hop bounds, cycles= on shortest paths, snapshot statistics/data statistics/failures, model policies, showAll, `.show operations` | `docs_sweep_test.go`; remaining failures all need Microsoft's sample graphs, external tables, the python plugin, or are doc errors (an "Ellen" row absent from its data, `project x as y`, two commands on adjacent lines) |
| General fixes found by the sweep: a keyword after a line break (`summarize ... \n by a` grouped nothing; `order by\n`, `parse ... with\n`); `summarize count() * 2` silently dropped `* 2` — expressions over aggregates (`toint(sum(x))`, `sum(a)/count()`, `array_slice(make_list(x),0,5)`) now computed, nested aggregates rejected; join dropped the right key (ADX keeps it, `a1`; unmatched outer rows lost their key) — lookup keeps dropping it; `let x = (multi-line tabular);`, `let n = union (...), (...);`, `let X = TabularFn();` | `findKeyword`/`keywordFold`, `applySummarizePost`, `buildJoinSchema` (Kusto.Language's rename rule); the nine stored functions in the scopes that use join give identical output |
| Also: `take`/`top` with scalar counts, `table(name)`, named function arguments, triple-backtick multi-line strings, `kind = inner` spacing and `kind=inner(T)`, `withsource=['quoted name']`, `column_ifexists(paramName, ...)`, `array_strcat` | parser and engine; covered in `docs_sweep_test.go` |
| Second full docs sweep (every query code block of dataexplorer-docs): null semantics (`null != 5` is true, `null == null` and ordering/arithmetic on null are null, three-valued `and`/`or`/`not`; `int(null)+1` was false), summarize over empty input returns one row (0 / NaN / `[]` / null by aggregate), IEEE real division (`1.0/0` is ∞), `make_datetime` and `datetime(y, m, d[, h, m, s])`, unpadded and spaced date literals (`datetime(1970-5-12)`, `datetime(2017 - 08 - 01)`), period functions with offset and one-tick ends, `make_timespan` real seconds, UDF result types, `tolong`/`toreal` string parsing, `min_of`/`max_of` typing, relaxed `parse`, `url_encode`/`url_decode` (.NET escaping), `parse_url`/`parse_urlquery`/`parse_path` bag shapes, `array_sort_*` (several arrays, `<arg>_sorted` columns, nulls_last), `series_fill_linear` int arrays, `strlen` in characters, `parse_ipv4` with a prefix, `ipv4_is_in_any_range` bare addresses, hash functions on datetimes, `bag_has_key` JSONPath, `unicode_codepoints_to_string` variadic, typed `pack_array`/`make_list` elements, string escapes (`\u`, `\U`, `\x`, octal, `\r` …), `parse-kv` escape option, `join`/`lookup` with a bare right table, `summarize by` with no aggregates, `between (a .. b)` over names, `project-reorder` wildcards with order, union withsource reporting `as` names, `consume` | `docs_sweep2_test.go`. Two odm-pendants functions (LogicFamily, LogicSurprise) change: `where constp != true` now keeps rows where the property is absent, as ADX does |

## P2 — real but less frequent

- `evaluate` plugins. `bag_unpack`, `ipv4_lookup`, `ipv6_lookup`,
  `pivot`, `narrow`, `schema_merge`, `rolling_percentile`,
  `sliding_window_counts`, `activity_counts_metrics`,
  `activity_metrics`, `active_users_count`, `new_activity_metrics`,
  `sequence_detect` and `dcount_intersect` exist. Remaining:
  `funnel_sequence` and `funnel_sequence_completion` (references use
  StormEvents only; funnel_sequence returns three tables), and
  `activity_engagement` and `session_count`, whose references show
  only charts (of random data), so their algorithms are unpinned.
  Pattern-mining plugins (`autocluster`,
  `basket`, `diffpatterns`) have the same specification problem as
  `reduce` and need their algorithm pinned before work starts.

- GQL and openCypher graph query languages (MATCH ... RETURN ...
  NEXT, with a `#crp query_graph_reference` client property), each
  with its own reference section in the docs. The `python` plugin
  (plotly_graph_fl).

### Remaining plugin output schemas (Kusto.Language 12.4.1)

Input `datatable(Id:string, Ts:datetime, D:string, v:long, s:string)`;
use these names and types, since the reference pages often disagree
with their own examples.

| Plugin call | Output columns |
|---|---|
| `activity_counts_metrics(Id, Ts, start, end, 1d, D)` | `Ts:datetime, D, count:long, dcount:long, new_dcount:long, aggregated_dcount:long` |
| `activity_engagement(Id, Ts, [start, end,] 1d, 30d [, D])` | `Ts:datetime, [D,] dcount_activities_inner:long, dcount_activities_outer:long, activity_ratio:real` |
| `active_users_count(Id, Ts, start, end, 7d, 1d, 2, 7d, D)` | `Ts:datetime, D, dcount:long` |
| `new_activity_metrics(Id, Ts, start, end, 1d, dynamic(null), D)` | `from_Ts:datetime, to_Ts:datetime, D, dcount_new_values:long, dcount_retained_values:long, dcount_churn_values:long, retention_rate:real, churn_rate:real` |
| `activity_metrics(Id, Ts, start, end, 1d, D)` | `Ts:datetime, D, dcount_values:long, dcount_newvalues:long, retention_rate:real, churn_rate:real` |
| `funnel_sequence(Id, Ts, start, end, 10m, 1d, s, dynamic([...]))` | `Ts:datetime, prev:string, next:string, dcount:long, samples:dynamic` |
| `funnel_sequence_completion(Id, Ts, start, end, 1d, s, dynamic([...]), dynamic([1h, 1d]))` | `Ts:datetime, s:string, Period:timespan, dcount:long` |
| `dcount_intersect(v, s)` (hll arguments) | input columns plus `s0:long, s1:long, ...` |
| `sequence_detect(Ts, 5h, 8h, s == 'a', s == 'b', Id)` | `Id, _Ts:datetime (per step), Duration:timespan` |
| `session_count(Id, Ts, start, end, 1d, 1d, D)` | `Ts:datetime, D, count_sessions:long` |

## P3 — carried over

`hash_combine`/
`hash_many`, `series_fit_2lines` (the reference does not say which
side owns the split point and shows no numbers), series signal
processing (decompose, outliers, periods),
`datetime_list_timezones`, `reduce`. Details in the existing backlog
documents.

## Out of scope (unchanged)

Cluster, cursor and principal introspection; geo; HLL and T-Digest
sketches. See `scalar_function_backlog.md`.
