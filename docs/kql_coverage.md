# KQL Language Coverage Analysis

Based on Microsoft's official [Kusto-Query-Language](https://github.com/microsoft/Kusto-Query-Language) repository (ANTLR grammar + symbol definitions), cross-checked function-by-function and operator-by-operator against the actual source, not assumed from a prior pass.

**Last updated:** 2026-08-17 (Sprint 14: series_* Tier 3 closed, minus series_stats)
**Project stats:** 30,830 lines Go, 66 source files, 554 tests across 49 test files (verified fresh this session, not carried over)

This rewrite corrects a significant amount of drift in the previous version (dated 2026-02-28): several operators, comparison forms, and aggregation functions listed as "not yet implemented" or "out of scope" were already implemented — some before that version was even written, some added across sessions since without this document being updated. Several entire capability areas (stored functions, materialized views, federation, automatic ingest-time timestamping, host-embedding extension points, vector similarity) were built since and never documented here at all. Every claim below was checked directly against the source or with a live query before being written down.

**Counting methodology, stated plainly rather than implied**: each "Implemented" table row is counted once, even where it bundles several grammar symbols together (e.g. "startswith / !startswith" is one row covering two symbols; "binary_all_or / binary_all_and / binary_all_xor" is one row covering three). The denominators (47, 35, 61) are carried over unchanged from the original version's own external reference point (the real ANTLR grammar's symbol/rule count) and were not independently re-derived here. This means the fractions below are directionally accurate but not an apples-to-apples symbol count — a real, honest caveat, not a precision this document can actually back up without parsing the grammar directly.

---

> **Active work queue: [`parity_backlog.md`](./parity_backlog.md)** (re-enumeration of 2026-10-22). It supersedes the "Not Yet Implemented: None" statements below, which held for operator and function names but not for operator options, window functions, literal syntax or plugins.

## Tabular Operators

### Implemented (41/53)

| Operator             | Status | Notes                                                                                                               |
| --------------------- | ------ | ------------------------------------------------------------------------------------------------------------------- |
| where / filter        | ✅      | Full predicate support                                                                                              |
| project                | ✅      | Column selection + computed columns; bare/unnamed expressions auto-name from their own text, matching real ADX     |
| project-away           | ✅      | Remove columns by name                                                                                              |
| project-rename         | ✅      | Rename columns                                                                                                      |
| project-reorder        | ✅      | Reorder columns (specified first, remainder follow)                                                                 |
| project-keep           | ✅      | Keep columns matching wildcard patterns                                                                             |
| extend                 | ✅      | Add computed columns; bare/unnamed expressions auto-name from their own text                                       |
| take / limit           | ✅      | Row limiting                                                                                                        |
| count                  | ✅      | Row counting                                                                                                        |
| distinct               | ✅      | Deduplication                                                                                                       |
| order by / sort by     | ✅      | Multi-column, asc/desc; null ordering matches real Kusto (nulls smallest: asc-first, desc-last)                     |
| top                    | ✅      | Top N by a single ranking expression, matching real ADX's own restriction (no multi-key form exists there either)  |
| sample                 | ✅      | Random sampling                                                                                                     |
| summarize              | ✅      | 34 aggregation functions, by-clause, expressions                                                                    |
| join                   | ✅      | 8 kinds (inner, leftouter, leftanti, leftsemi, rightouter, rightanti, rightsemi, fullouter), subquery, $left/$right |
| union                  | ✅      | Pipe + standalone forms; kind=inner/outer, withsource=, isfuzzy=, wildcard legs, per-type column suffixing (2026-10-22) |
| mv-expand / mvexpand   | ✅      | JSON array flattening, alias support, multi-column                                                                  |
| serialize              | ✅      | Row ordering with optional computed columns, enables window functions                                              |
| print                  | ✅      | Literal expression output; bare/unnamed expressions auto-name from their own text                                  |
| parse                  | ✅      | Extract fields with patterns (kind=simple, relaxed, regex with named groups)                                       |
| getschema               | ✅      | Return ColumnName, ColumnOrdinal, DataType, ColumnType                                                              |
| datatable               | ✅      | Inline tabular data with typed columns; dynamic({...}) values accept both single- and double-quoted JSON            |
| lookup                  | ✅      | Dimension enrichment join by table name; kinds leftouter (default) and inner                                        |
| render                  | ✅      | Parsed as metadata (visualization + with-properties), pass-through in engine                                       |
| mv-apply                | ✅      | Per-row array expansion + subquery pipeline; optional rename and to typeof(T)                                       |
| make-graph              | ✅      | Directed graph from edge table: Src --> Dst [with Nodes on NodeId]; isolated + edge-only nodes handled              |
| graph-match             | ✅      | Path patterns (a)-[e]->(b), variable-length -[e*1..5]->, where over dotted vars, required project; unique edges     |
| graph-to-table          | ✅      | Materialize graph as edges (default) or nodes                                                                       |
| range                   | ✅      | Table-generating source: int/long/real/datetime/timespan, let-bound and standalone forms                            |
| evaluate                | ✅      | General plugin-dispatch mechanism (a registry, not a single hard-coded case); bag_unpack is the first plugin        |
| search                  | ✅      | Free-text search across all columns or a named column, `in (T1, T2)` or database-wide                              |
| find                    | ✅      | Cross-table search: explicit table list or database-wide, project-smart and explicit project [, pack_all()]        |
| partition               | ✅      | Group-by-column, run a sub-pipeline per group, union results (the `(SubQuery)` implicit-source form)                |
| parse-where             | ✅      | Same pattern grammar as parse, but drops non-matching rows entirely instead of nulling them (2026-08-15)             |
| parse-kv                | ✅      | Key-value extraction, "specified delimiter" mode (pair_delimiter/kv_delimiter, quote, escape, greedy) — see the Not Yet Implemented note below for the two documented modes NOT covered (2026-08-15) |
| make-series              | ✅      | Main syntax only (real ADX's own docs recommend it over the alternate `in range(...)` form); from/to both required explicitly (real ADX's own data-driven auto-detect for an omitted from/to is not implemented); verified exactly against every value in real ADX's own worked examples, including the bin_at floor-division edge case and kind=nonempty (2026-08-15) |
| as                       | ✅      | Binds a name to the input tabular expression; scoped to same-query, downstream reference (a later join/union subquery) — cross-statement reference and the withsource=/source_/$table integration are out of scope (2026-08-15) |
| scan                     | ✅      | Single-step state machine (declare/step/output=/StepName.ColumnName state references); verified exactly against both of real ADX's own worked examples including the reset-at-10 case — multi-step sequences and with_match_id are out of scope (2026-08-15) |
| project-by-names         | ✅      | Select + reorder columns by name/wildcard/dynamic array/column_names_of(Table); verified against all five of real ADX's own worked examples (2026-08-15) |
| sample-distinct          | ✅      | Up to N distinct values of one column, deterministic first-N-encountered with early exit (2026-08-15) |
| invoke                   | ✅      | Stored functions only (`.create-or-alter function`) — real ADX's own primary worked example's inline tabular `let`-lambda is a separate, genuine gap this operator doesn't solve; see its own scope note below (2026-08-15) |

### Not Yet Implemented

None — the full, cross-checked enumeration of real ADX query operators (2026-08-14) surfaced 7 genuine gaps this document itself had missed entirely, all closed as of 2026-08-15: `parse-where`, `parse-kv`, `make-series`, `as`, `scan`, `project-by-names`, `sample-distinct`. See the "Out of Scope" section below for the operators deliberately not implemented (distinct from a gap — a considered decision, not an oversight).

### Out of Scope

facet, fork, top-nested, top-hitters,
macro-expand, assert-schema, consume, execute-and-cache, reduce, externaldata / external_table / inline_external_table

`externaldata`/`external_table`/`inline_external_table` were explicitly deprioritized (2026-08-14) — not needed for this project's current use; `inline_external_table` is the same external-table family under a different syntactic form, not a separate capability. `consume`'s own real purpose (distributed query-cost estimation, deliberately not transmitting data between cluster nodes) has no meaning for a single-node engine with no cluster to avoid transmitting across — out of scope for that reason, not overlooked. `partition`'s own `{SubQueryWithSource}` braces form (legacy-strategy-only in real ADX, an explicit tabular source referencing the current partition key via `toscalar(Column)`) is also out of scope — a query using it gets a clear, explicit parse error, not silent mis-parsing; the far more common `(SubQuery)` implicit-source form covers native/shuffle/legacy alike here since this engine has no distributed execution to differentiate.

**`reduce`'s own scope note (2026-08-15, genuinely deferred, not attempted)**: `reduce` groups string values by similarity (Vaarandi's SLCT algorithm — "A Data Clustering Algorithm for Mining Patterns From Event Logs," IPOM 2003) into a `pattern`/`count`/`representative` result. Real ADX's own docs specify the input/output shape and a handful of worked examples, but NOT the precise algorithm internals needed to reproduce them faithfully. Specifically: reverse-engineering the docs' own first worked example (`MachineLearningX0`..`MachineLearningX9` → pattern `MachineLearning*`) against the documented default tokenization rule ("every non-alphanumeric character is a separator") doesn't work — those strings contain no separator characters at all, so under a pure word-frequency-counting reading each one should be a single, indivisible token, yet the output pattern wildcards only the trailing `X4`/`X7`/etc. while keeping `MachineLearning` literal. That means real ADX's own implementation does a CHARACTER-level prefix/suffix decomposition on top of whatever word-frequency counting the paper's public abstract describes, and the exact mechanics of that step, plus the precise mapping from ADX's own 0–1 `threshold` parameter to the paper's absolute minimum-support count `N`, aren't reliably determinable from the sources available (search summaries and abstract fragments; the full original paper PDF's exact text wasn't successfully retrievable). A guessed implementation would run and look plausible without any confidence the `threshold` knob behaves as a real ADX user would expect, or that pattern generation matches on the log/alert-message case this operator is actually valuable for (clustering near-identical security findings) — the exact case where a plausible-but-wrong grouping would be worse than an obvious gap. Deliberately left unimplemented rather than shipped with unverified precision; revisit if the actual SLCT reference source (the `logparser` project's wrapped C implementation) or the full paper text becomes available to read directly.

**`parse-kv`'s own scope note (2026-08-15)**: only the "specified delimiter" mode (`pair_delimiter=..., kv_delimiter=...`, with optional `quote=`, `escape=`, `greedy=true`) is implemented — every worked example in real ADX's own docs uses this mode. The "non-specified delimiter" mode (no pair_delimiter/kv_delimiter given, any nonalphanumeric character treated as a delimiter) and the "regex" mode (`regex=RegexPattern`) are real, separately-documented alternate forms of the same operator that are NOT implemented — a query using either gets a clear, explicit parse error naming which mode is unsupported, not silent mis-parsing.

**`make-series`'s own scope note (2026-08-15)**: only the main syntax (`from start`/`to end`/`step step`) is implemented — real ADX's own docs explicitly recommend it over the alternate `on AxisColumn in range(start, stop, step)` form, so that form is deliberately out of scope. `from` and `to` are both required explicitly here — real ADX's own "auto-detect from the data" behavior for an omitted `from`/`to` is not implemented; a query omitting either gets a clear parse error. The `Aggregation` clause is scoped to real aggregation-function calls (`avg(Col)`, `sum(Col)`, etc.) — one of real ADX's own docs examples incidentally uses a bare scalar literal as an aggregation entry (`other=-1`) alongside its actual subject; that specific edge case is not supported and produces a clear parse error rather than a silent misinterpretation. `kind=nonempty` is implemented only for the unambiguous case (zero input rows AND no `by` clause) — real ADX's own precise behavior for `kind=nonempty` combined with a `by` clause over zero input rows isn't covered by any worked example found, so that combination is left as the ordinary zero-row result rather than guessed.

Note on "shuffle": real ADX's `shuffle` is a distributed-execution STRATEGY HINT (`hint.strategy=shuffle`, `hint.shufflekey=...`, usable on `join`/`summarize`/`partition`), not a standalone tabular operator at all — already correctly recognized and silently skipped wherever this engine accepts a `hint.xxx=` prefix (`partition`, `evaluate`), since there's no distributed execution here for a shuffle strategy to actually change.

---

## Comparison & String Operators

### Implemented (22/35)

| Operator                       | Case                              | Status |
| -------------------------------- | ------------------------------------ | ------ |
| ==                                | exact                                 | ✅      |
| != / <>                          | exact                                 | ✅      |
| =~                                | case-insensitive ==                   | ✅      |
| !~                                | case-insensitive !=                   | ✅      |
| <, <=, >, >=                      | numeric/string/datetime               | ✅      |
| contains / !contains             | case-insensitive                      | ✅      |
| contains_cs / !contains_cs       | case-sensitive                        | ✅      |
| has / !has                       | case-insensitive                      | ✅      |
| has_cs / !has_cs                 | case-sensitive                        | ✅      |
| has_any                          | set membership (case-insensitive)     | ✅      |
| has_all                          | set membership (case-insensitive)     | ✅      |
| hasprefix / !hasprefix           | word-boundary prefix, case-insensitive| ✅      |
| hassuffix / !hassuffix           | word-boundary suffix, case-insensitive| ✅      |
| startswith / !startswith         | case-insensitive                      | ✅      |
| startswith_cs / !startswith_cs   | case-sensitive                        | ✅      |
| endswith / !endswith             | case-insensitive                      | ✅      |
| endswith_cs / !endswith_cs       | case-sensitive                        | ✅      |
| like / !like                     | wildcard (* / ?)                      | ✅      |
| matches regex                    | regex                                 | ✅      |
| in / !in                         | literal list, let-bound table, OR a tabular subquery `in (T \| where ...)` | ✅ |
| in~ / !in~                       | case-insensitive set membership       | ✅      |
| between / !between               | range (numeric/datetime)              | ✅      |

### Not Yet Implemented

None known — the previous version's "hasprefix/hassuffix" and "like/!like" entries were already implemented and working; verified live, not assumed.

---

## Scalar Functions

### Implemented (~178 entry points)

Functions are organized across focused source files.

#### String — `func_string.go` (35 functions)

| Function                     | Notes                                         |
| ----------------------------- | ------------------------------------------------ |
| strlen / string_size          | String length (string_size returns bytes)         |
| tolower / toupper              | Case conversion                                    |
| strcat                          | Concatenation (variadic)                           |
| strcat_delim                    | Concatenation with delimiter                       |
| strcmp                          | String comparison (-1, 0, 1)                       |
| strrep                          | String repetition                                  |
| translate                       | Character translation                              |
| substring                       | Substring extraction (start, optional length)      |
| split                           | Split to JSON array                                |
| extract                         | Regex capture group extraction                     |
| extract_all                     | All regex matches as JSON array                    |
| replace_string                  | Literal string replacement                         |
| replace_regex                   | Regex replacement                                  |
| trim / trim_start / trim_end    | Regex-based trimming                               |
| indexof                         | First occurrence index (with optional start)       |
| indexof_regex                   | First occurrence index of a regex match, with optional start/length/occurrence; verified against real ADX's own worked example, added 2026-08-18 (Sprint 17) |
| countof                         | Occurrence count                                   |
| has_any_index                   | Index of the first matching item from an array, or -1 |
| reverse                         | String reversal                                    |
| isempty / isnotempty            | Empty/null string tests                            |
| url_decode                      | URL percent-decoding                               |
| regex_quote                      | Regex metacharacter escaping via Go's `regexp.QuoteMeta`; verified against real ADX's own worked example; added 2026-08-18 (Sprint 27) |
| replace_strings                   | Multi-pattern replacement; sequential, order-dependent passes with match protection (reverse-engineered from real ADX's own worked examples, not assumed from the name); added 2026-08-18 (Sprint 27) |
| unicode_codepoints_from_string / to_utf8 | String to array of Unicode codepoints; `to_utf8` is real ADX's own documented deprecated alias; verified against real ADX's own worked examples including non-ASCII/mixed-script text; added 2026-08-18 (Sprint 27) |
| unicode_codepoints_to_string       | Inverse of unicode_codepoints_from_string; verified by round trip against real ADX's own worked example; added 2026-08-18 (Sprint 27) |
| isascii                             | ASCII validity test (every byte in 0x00-0x7F); added 2026-08-18 (Sprint 27) |
| isutf8                               | UTF-8 validity test via Go's `utf8.ValidString`; added 2026-08-18 (Sprint 27) |

#### Datetime — `func_datetime.go` (47 entry points / ~39 unique)

| Function                                                                | Notes                                                                |
| ---------------------------------------------------------------------- | ---------------------------------------------------------------------- |
| now                                                                       | Current UTC time                                                        |
| ingestion_time                                                             | Accessor for this engine's own real, `_TimeReceived` column (analogous to real ADX's hidden `$IngestionTime`); null if the column is absent or unset for the row, per real ADX's own documented contract; added 2026-08-18 (Sprint 29) |
| ago                                                                       | Relative time (timespan subtraction)                                    |
| datetime / todatetime / make_datetime                                    | Datetime construction/parsing; also accepts M/d/yyyy (US short-date, e.g. `02/17/2016 08:40:01`) alongside ISO 8601 — verified against real ADX's own worked example (Sprint 17, 2026-08-18) |
| bin / floor                                                              | Datetime and numeric binning                                            |
| datetime_diff                                                             | Difference in units (year→millisecond)                                  |
| datetime_add                                                              | Add interval to datetime (year, month, day, hour, minute, second)       |
| totimespan                                                                | Parse timespan from string (KQL `d.hh:mm:ss.fffffff` and Go formats)    |
| make_timespan                                                             | Construct timespan from components (2–4 args)                           |
| format_timespan                                                           | Format timespan with custom pattern (d, hh, mm, ss, fff)                |
| startofday / startofweek / startofmonth / startofyear                    | Period start truncation                                                  |
| endofday / endofweek / endofmonth / endofyear                            | Period end                                                               |
| format_datetime                                                          | KQL format strings, rewritten from a naive replacer to direct parsing; supports every documented specifier, including bare (no-leading-zero) and F-class conditional forms |
| dayofweek / dayofmonth / dayofyear                                       | Day extraction                                                           |
| year / month / day / hour / minute / second / millisecond / microsecond  | Component extraction — corrected 2026-08-18: this row previously implied these are standalone callable functions; verified directly while implementing `datetime_part` (Sprint 28) that they are NOT — only differently-named aliases exist (`getyear`, `monthofyear`, `hourofday`, `getmonth`, `dayofmonth`, plus `bin`/`startof*`/`endof*`/`datetime_add`/`datetime_diff`'s own internal `part` string dispatch, which is a separate, non-callable mechanism). `datetime_part("year", dt)` etc. now provides genuine standalone component extraction for all of these plus `quarter`/`week_of_year`/`dayofyear`/`nanosecond` in one function (Sprint 28). |
| quarter                                                                  | Quarter of year (1–4)                                                   |
| week_of_year / weekofyear                                                | ISO week number                                                        |
| getmonth / getyear / hourofday / monthofyear                             | Component extraction (aliases)                                          |
| datetime_part / datepart                                                  | Standalone component extraction by string part name (year, quarter, month, week_of_year, day, dayofyear, hour, minute, second, millisecond, microsecond, nanosecond); `datepart` is real ADX's own documented deprecated alias; verified against real ADX's own 12-part worked example exactly; added 2026-08-18 (Sprint 28) |
| unixtime_seconds_todatetime                                              | Unix epoch conversion                                                   |
| unixtime_milliseconds_todatetime                                         | Unix epoch conversion (ms)                                              |
| unixtime_microseconds_todatetime / unixtime_nanoseconds_todatetime         | Unix epoch conversion (μs / ns); negative (pre-1970) input supported per real ADX's own documented contract; added 2026-08-18 (Sprint 28) |
| datetime_local_to_utc / datetime_utc_to_local                              | IANA timezone conversion via Go's own `time.LoadLocation`; embedded `time/tzdata` for deployment portability; verified against real ADX's own worked examples including a DST-transition ambiguity case; added 2026-08-18 (Sprint 28) |

#### Type Conversion, Math & Conditionals — `func_convert.go` (34 entry points / ~28 unique)

| Function                   | Notes                                                |
| ---------------------------- | ------------------------------------------------------ |
| toint / tolong                | Integer conversion                                       |
| todouble / toreal              | Float conversion                                          |
| tostring                        | String conversion (handles int, float, bool, null)         |
| tobool / toboolean              | Boolean conversion (string, int, float)                    |
| parse_json / todynamic          | JSON parsing for property access                            |
| isnull / isnotnull              | Null tests                                                  |
| iff / iif                        | Ternary conditional                                          |
| case                             | Multi-branch conditional                                     |
| coalesce                         | First non-null/non-empty value                               |
| max_of / min_of                  | Scalar min/max (variadic)                                     |
| round                             | Rounding with optional precision                              |
| format_bytes                      | Human-readable byte size formatting, base-1024 with auto/explicit unit selection and optional precision; verified against real ADX's own 5-case worked example exactly; added 2026-08-18 (Sprint 29) |
| abs                               | Absolute value (preserves int64 for integers)                 |
| pow                               | Power                                                          |
| sqrt                              | Square root                                                   |
| log / log2 / log10                | Logarithms                                                     |
| ceiling                            | Ceiling                                                        |
| exp / exp2 / exp10                 | Exponentials                                                   |
| pi                                  | Pi constant                                                    |
| sign                                | Sign (-1, 0, 1)                                                |
| isnan / isinf / isfinite            | Float special value tests                                      |
| sin / cos / tan / asin / acos / atan | Trigonometry, radian in/out; added 2026-08-17 (Sprint 15), shares implementation with series_sin/series_cos/etc |
| atan2                                 | Angle in radians between axes; (y,x) argument order verified against real ADX worked example; added 2026-08-17 (Sprint 15) |
| degrees / radians                      | Angle unit conversion; added 2026-08-17 (Sprint 15)             |

#### XML — `func_xml.go` (1 function)

| Function   | Notes                                                                 |
| ----------- | ---------------------------------------------------------------------- |
| parse_xml    | XML-to-JSON conversion per real ADX's own 7-row conversion table (element/attribute/text/repeated-child-array rules); dispatch case lives in `func_convert.go`, implementation in its own dedicated file; added 2026-08-18 (Sprint 21) |

#### Dynamic (JSON/Array/Bag) — `func_dynamic.go` (39 functions)

| Function                                   | Notes                                                                    |
| --------------------------------------------- | --------------------------------------------------------------------------- |
| array_length                                    | JSON array length                                                              |
| array_concat                                    | Concatenate arrays                                                             |
| array_index_of                                  | Find element in array                                                          |
| array_reverse                                    | Reverse array                                                                   |
| array_slice                                      | Slice array (start, end)                                                       |
| array_sort_asc / array_sort_desc                 | Sort array                                                                      |
| array_shift_left / array_shift_right              | Shift array elements; a fill value; negative shift on the left form reaches the right form's own direction |
| array_rotate_left / array_rotate_right              | Rotate array elements; negative rotate_count reaches the opposite direction (same pattern as the shift family); added 2026-08-18 (Sprint 22) |
| array_split                                          | Split at one or more indices (scalar int or dynamic array of ints); negative indices convert to array_length+value; added 2026-08-18 (Sprint 22) |
| array_sum                                             | Sum of numeric array elements as a double; null if any element is non-numeric; added 2026-08-18 (Sprint 22) |
| array_iff / array_iif                                  | Element-wise conditional selection from two arrays or scalars by a parallel condition array; numeric-nonzero/boolean truthiness, null for non-numeric/non-boolean conditions or missing when_true/when_false entries; added 2026-08-18 (Sprint 22) |
| zip                                                | Combine N arrays into an array of same-index tuples; output length is the longest input, missing slots become null |
| bag_keys                                            | Get object property names                                                      |
| bag_has_key                                         | Check property exists                                                          |
| bag_pack / pack                                     | Create JSON object from key-value pairs                                        |
| pack_array                                          | Create JSON array from arguments                                               |
| repeat                                               | Array of a value repeated N times; count=0 empty array, count<0 null; verified against real ADX's own worked example; added 2026-08-18 (Sprint 27) |
| pack_dictionary                                     | Create JSON object from alternating keys/values                                |
| bag_merge                                            | Merge 2–64 property bags; leftmost argument's key wins on collision            |
| bag_remove_keys                                       | Remove top-level keys, or nested keys via `$.key.prop` JSONPath                |
| bag_zip                                                | Build a property bag from a keys array + a values array; non-string keys excluded (positionally, not reassigned), missing/extra entries handled per real ADX's own 6-case worked example; added 2026-08-18 (Sprint 23) |
| bag_set_key                                             | Set/overwrite a key (root-level or `$.a.b` JSONPath); auto-creates missing intermediate objects along a nested path; null if input isn't a property bag; added 2026-08-18 (Sprint 23) |
| bag_pack_columns                                         | Build a property bag from bare column references, using each column's own name as the property key; added 2026-08-18 (Sprint 23) |
| dynamic_to_json                                           | Canonical string form of a dynamic value; property-bag keys sorted alphabetically; non-dynamic scalars go through the same conversion as `tostring()`; added 2026-08-18 (Sprint 23) |
| set_union / set_intersect / set_difference           | Set operations on arrays                                                       |
| set_has_element                                        | Membership test on an array/set, sharing `array_index_of`'s own element-equality check; added 2026-08-18 (Sprint 23) |
| jaccard_index                                           | Set similarity, `\|intersection\|/\|union\|`, duplicates ignored; verified against real ADX's own worked example exactly; added 2026-08-18 (Sprint 29) |
| treepath                                              | Get all paths in a dynamic object                                              |
| extract_json / extractjson                            | Real JSONPath walker (`$`/`.prop`/`['prop']`/`[n]`), optional `typeLiteral` conversion; verified against real ADX's own simple worked example plus the full JSONPath subset doc's 11-path worked example; added 2026-08-18 (Sprint 19) |
| parse_csv                                              | Parse a single CSV record (RFC4180-style quoting) into a string array; only the first record is taken for multi-line input |

#### Network, Encoding & Hashing — `func_net.go` (48 functions)

| Function               | Notes                                                         |
| ----------------------- | ----------------------------------------------------------------- |
| parse_ipv4               | IP to long representation                                          |
| ipv4_is_private            | RFC 1918 check; embedded-prefix bug fixed 2026-08-17 (see Sprint 15) |
| ipv4_is_in_range            | CIDR range membership; equal-IPs-no-range bug fixed 2026-08-17 (see Sprint 15) |
| has_ipv4                     | Check if text contains IPv4 (exact or CIDR match); delimiting bug fixed 2026-08-17 (see Sprint 15) |
| ipv4_compare                  | Compare IPs with optional prefix mask (-1, 0, 1); embedded-prefix bug fixed 2026-08-17 (see Sprint 15) |
| format_ipv4                    | Format IP with optional prefix mask (subnet extraction); embedded-prefix bug fixed 2026-08-17 (see Sprint 15) |
| has_any_ipv4                     | Added 2026-08-17 (Sprint 15) — text search for any of several IPs, variadic or dynamic-array form |
| has_ipv4_prefix                   | Added 2026-08-17 (Sprint 15) — text search for an IPv4 address prefix |
| has_any_ipv4_prefix                 | Added 2026-08-17 (Sprint 15) — text search for any of several IPv4 prefixes |
| ipv4_is_in_any_range                  | Added 2026-08-17 (Sprint 15) — CIDR range membership against several ranges |
| ipv4_is_match                           | Added 2026-08-17 (Sprint 15) — min-of-embedded-and-explicit-prefix match |
| ipv4_range_to_cidr_list                  | Added 2026-08-17 (Sprint 15) — largest-fitting-aligned-block CIDR splitting |
| ipv4_netmask_suffix                       | Added 2026-08-17 (Sprint 15) — extract embedded prefix, default 32 |
| format_ipv4_mask                           | Added 2026-08-17 (Sprint 15) — format_ipv4 plus CIDR-notation suffix |
| parse_ipv4_mask                             | Added 2026-08-17 (Sprint 15) — ip+prefix to masked long |
| parse_ipv6                                    | Added 2026-08-17 (Sprint 15) — canonical, fully-expanded IPv6 string; empty string on conversion failure |
| parse_ipv6_mask                                | Added 2026-08-17 (Sprint 15) — ip+prefix to canonical masked IPv6 string; +96 offset for a dot-bearing embedded IPv4 prefix, verified against all 7 rows of the real worked-example table |
| ipv6_compare                                    | Added 2026-08-17 (Sprint 15) — -1/0/1 comparison, min-of-embedded-and-explicit-prefix, accepts mixed IPv4/IPv6 notation |
| ipv6_is_match                                    | Added 2026-08-17 (Sprint 15) — verified against all 21 rows across both real worked-example tables, including 4 mixed-notation rows |
| ipv6_is_in_range                                  | Added 2026-08-17 (Sprint 15) — verified against real 3-row worked example (2 true, 1 false) |
| ipv6_is_in_any_range                               | Added 2026-08-17 (Sprint 15) — variadic or dynamic-array range list, verified against real worked example |
| parse_user_agent                                    | Added 2026-08-18 (Sprint 15) — real ua-parser/uap-core regex database (1270 patterns), embedded via go:embed; see `pkg/engine/uaparser`; verified against 2 real worked examples byte-for-byte |
| hash_sha256                     | SHA-256 hash                                                            |
| hash_md5                         | MD5 hash                                                                 |
| hash_sha1                         | SHA-1 hash                                                                |
| hash / hash_xxhash64                | Real xxhash64 (`github.com/cespare/xxhash/v2`), optional `mod`; verified bit-for-bit against real ADX's own worked examples for string and datetime inputs (the datetime case required independently deriving the .NET-ticks conversion — see Sprint 24); long/int/real/timespan handled by consistent inference, not independently worked-example-verified; added 2026-08-18 (Sprint 24) |
| new_guid                           | Generate v4 UUID                                                           |
| toguid                              | String-to-guid conversion; strips hyphens anywhere (not position-validated), first 32 hex chars, null if invalid; verified against real ADX's own 3-row worked example; added 2026-08-18 (Sprint 25) |
| tohex                                | Two's-complement hex string; int/long width distinction via `inferExprType`; optional zero-pad `minLength`; verified against real ADX's own 5-case worked example; added 2026-08-18 (Sprint 25) |
| gettype                               | Runtime type name; static-type branch plus a dynamic-value JSON-shape introspection branch (`dictionary`/`array`/`int`/`null`/etc); verified against real ADX's own 12-case worked example; added 2026-08-18 (Sprint 25) |
| base64_encode_tostring               | Base64 encoding                                                             |
| base64_decode_tostring                 | Base64 decoding                                                               |
| base64_decode_toarray                    | Base64 to array of long byte values; verified against real ADX's own worked example; added 2026-08-18 (Sprint 26) |
| base64_encode_fromarray                   | Inverse of base64_decode_toarray; verified by round trip against real ADX's own worked example; added 2026-08-18 (Sprint 26) |
| base64_decode_toguid                       | Base64 to guid, via .NET's own internal mixed-endian byte layout; a genuine doc discrepancy found and resolved (see Sprint 26); added 2026-08-18 (Sprint 26) |
| base64_encode_fromguid                      | Inverse of base64_decode_toguid; verified exactly against real ADX's own worked example; added 2026-08-18 (Sprint 26) |
| gzip_compress_to_base64_string / gzip_decompress_from_base64_string | Standard gzip via `compress/gzip`; decompress returns empty string (not null) on failure, matching real ADX's own documented contract exactly; added 2026-08-18 (Sprint 26) |
| zlib_compress_to_base64_string / zlib_decompress_from_base64_string | Standard zlib via `compress/zlib`; same empty-string-on-failure contract; interoperability verified directly against Python's own `zlib` module; added 2026-08-18 (Sprint 26) |
| url_encode_component                     | URL percent-encoding (space → %20)                                              |
| url_encode                                 | URL percent-encoding, application/x-www-form-urlencoded style (space → +)        |
| parse_url                                    | URL decomposition (Scheme, Host, Port, Path, Query, Fragment)                      |
| parse_urlquery                                 | Query string parameter parsing                                                      |
| parse_path                                       | File path decomposition (DirectoryName, FileName, Extension)                          |
| rand                                               | Random float or random int (with optional bound)                                        |

#### Vector Similarity & Embeddings — `func_vector.go` (3 functions)

Vector similarity as native KQL, no external vector database required.

> **Extension boundary.** `series_cosine_similarity` and
> `series_dot_product` are real ADX functions and are parity surface.
> `embed_text()` is **not** an ADX function; together with
> `.embed-into` and `.chunk-file` it is the only non-Microsoft surface
> in this engine, and the only surface that talks outside the process.
> The parity engine itself carries no inference dependencies:
> `pkg/embed` is stdlib only. See `docs/embeddings.md` for backend
> configuration, the `--pooling` trap, and the known gaps.

| Function                    | Notes                                                                 |
| ------------------------------ | -------------------------------------------------------------------------- |
| embed_text                       | Generate an embedding vector via Ollama's native embedding endpoint          |
| series_cosine_similarity           | Cosine similarity between two numeric vectors                                  |
| series_dot_product                   | Dot product between two numeric vectors                                          |

Also see `.embed-into` under Management Commands for bulk embedding of an entire column at ingest time.

#### Time Series — `func_series.go` (31 functions, 2026-08-17)

Element-wise array math, gap-filling, and summary statistics — the operations `make-series`'s own output array is for. Full detail, exact verification against real ADX's own docs, and every real bug found and fixed while building these: [`docs/timeseries_backlog.md`](./timeseries_backlog.md).

| Family | Functions | Notes |
| --- | --- | --- |
| Arithmetic | series_add, series_subtract, series_multiply, series_divide, series_pow | Element-wise over two dynamic arrays; null for a position missing from the shorter array or non-numeric |
| Comparison | series_equals, series_not_equals, series_less, series_less_equals, series_greater, series_greater_equals | Returns a dynamic array of **booleans** (verified directly against real ADX's own docs — corrects an earlier planning-stage guess of 1.0/0.0) |
| Unary math | series_abs, series_sign, series_ceiling, series_floor, series_log, series_exp | series_log/series_exp use the natural base, matching this engine's own already-correct scalar log()/exp() |
| Trig | series_sin, series_cos, series_tan, series_asin, series_acos, series_atan | |
| Gap filling | series_fill_forward, series_fill_backward, series_fill_const, series_fill_linear | series_fill_backward verified exactly against its own real-ADX worked example; series_fill_linear's fill_edges default (true) is an explicitly flagged assumption, not confirmed from available sources |
| Summary statistics | series_sum, series_product, series_magnitude, series_pearson_correlation, series_stats_dynamic | series_pearson_correlation's null rule verified STRICTER than the arithmetic family's own: any non-numeric element or length mismatch nulls the whole scalar result, not just one position. `series_stats` (the multi-column sibling of series_stats_dynamic) deliberately NOT implemented — needs destructuring-assignment grammar (`extend (a,b,c) = expr`) this engine doesn't have anywhere |

#### Window Functions (via serialize)

| Function   | Notes                                                 |
| ----------- | --------------------------------------------------------- |
| row_number   | Sequential row index (1-based)                             |
| prev          | Value from previous row (optional offset and default)        |
| next           | Value from next row (optional offset and default)              |

### Not Yet Implemented (notable gaps)

Correction to an earlier version of this document, which claimed "no standalone scalar-function gaps identified" — that was wrong, found by cross-checking against a full real-ADX scalar-function enumeration (2026-08-14) rather than the earlier, narrower pass this document itself had done. **~110 real, confirmed gaps found**, verified with a combination of source-grep and live CLI spot-checks (a few names in the raw reference list turned out to be false positives during that verification — e.g. `not` is already implemented here, just via a different mechanism than the check pattern used for the rest). Full list, organized by category and priority, with implementation notes for a future session to work from directly: **[`docs/scalar_function_backlog.md`](./scalar_function_backlog.md)**.

### Out of Scope

Geo functions (50+), machine learning plugins, graph functions, cluster/cursor/principal introspection functions (current_cluster_endpoint, current_principal, cursor_after, extent_id, and similar — meaningful only in a real, multi-tenant clustered deployment, no equivalent concept in this engine's single-node model). See `scalar_function_backlog.md` for the full, itemized out-of-scope list including exactly which hll/tdigest/punycode entry points these cover. Series functions are NOT purely out of scope — see [`docs/timeseries_backlog.md`](./timeseries_backlog.md): Tier 1 (element-wise, 22 functions) and Tier 2 (gap-filling, 4 functions) were closed 2026-08-17 (see the Time Series subsection above); Tier 3 (summary statistics, 6 functions) is a genuine, medium-priority remaining gap; Tiers 4-5 (curve-fitting, signal-processing/anomaly-detection, 14 functions) are low priority for this project's own use cases.

---

## Aggregation Functions

### Implemented (24/61)

| Function                         | Status | Notes                                                        |
| ----------------------------------- | ------ | ---------------------------------------------------------------- |
| count                                 | ✅      |                                                                    |
| countif                                | ✅      | Conditional                                                        |
| dcount / count_distinct                 | ✅      | This engine's dcount is already an exact set count (no HyperLogLog sketch); count_distinct is aliased to identical logic, matching real ADX's own documented "exact" semantics for it |
| dcountif / count_distinctif               | ✅      | Conditional, same aliasing as above                                 |
| sum / sumif                                 | ✅      | With conditional variant                                            |
| avg / avgif                                   | ✅      | With conditional variant                                            |
| min / max                                       | ✅      | Type-aware                                                          |
| minif / maxif                                     | ✅      | Conditional min/max                                                 |
| make_set / make_set_if                              | ✅      | Distinct values as JSON array                                        |
| make_list / make_list_if                              | ✅      | All values as JSON array (values stored as their string form before JSON-marshaling — a known characteristic, not yet fixed) |
| make_list_with_nulls                                    | ✅      | Like make_list, but keeps null values instead of dropping them          |
| make_bag / make_bag_if                                    | ✅      | JSON object aggregation from key-value pairs                             |
| arg_max / arg_min                                            | ✅      | Row with max/min value; star form `arg_max(Col, *)` expands to every source column |
| any / take_any                                                 | ✅      | Arbitrary row; any is real ADX's own documented deprecated alias for take_any |
| anyif / take_anyif                                                | ✅      | Conditional variant of any/take_any                                       |
| percentile                                                          | ✅      | Single percentile                                                          |
| percentiles                                                           | ✅      | Multiple percentiles in one call, expands to N output columns named `percentile_expr_pN` |
| percentiles_array                                                       | ✅      | Multiple percentiles as one dynamic array column, named `percentiles_expr`; accepts either comma-separated values or a dynamic array argument |
| stdev / stdevif                                                           | ✅      | Standard deviation (sample), with conditional variant                       |
| stdevp                                                                       | ✅      | Standard deviation (population)                                              |
| variance / varianceif                                                          | ✅      | Variance (sample), with conditional variant                                    |
| variancep / variancepif                                                          | ✅      | Variance (population), with conditional variant                                  |
| covariance / covarianceif                                                          | ✅      | Sample covariance between two columns, with conditional variant; added 2026-08-17 (Sprint 15), output column auto-named `covariance_x_y` (both args, not just the first) |
| covariancep / covariancepif                                                          | ✅      | Population covariance between two columns, with conditional variant; added 2026-08-17 (Sprint 15) |
| binary_all_or / binary_all_and / binary_all_xor                                    | ✅      | Bitwise aggregation                                                                 |
| strcat_delim                                                                          | ✅      | String concatenation aggregation                                                      |

### Not Yet Implemented

| Function                    | Priority | Notes                            |
| ------------------------------ | -------- | ------------------------------------ |
| **hll / hll_if / hll_merge**     | 🟢 LOW   | HyperLogLog sketch structures — real value is distributed/incremental aggregation across queries or materialized views, which matters less for a single-node engine |
| **tdigest / tdigest_merge / merge_tdigest**       | 🟢 LOW   | T-Digest sketch structures, same reasoning as hll above          |
| **buildschema**                     | 🟢 LOW   | Infer minimal schema admitting all values of a dynamic column     |
| **percentilew / percentilesw**       | 🟢 LOW   | Weighted percentile variants                                     |

---

## Stored (Persisted) Functions & Materialized Views

Not present in any prior version of this document — built across several sessions after the previous pass.

### Stored Functions

`.create-or-alter function Name(params) { body }` — persisted, named, reusable tabular queries, resolved and re-parsed from stored source text at call time (not compiled).

| Feature                       | Status | Notes                                                                                     |
| -------------------------------- | ------ | --------------------------------------------------------------------------------------------- |
| Parameterless functions             | ✅      | `Name()` calls                                                                                  |
| Scalar parameters                     | ✅      | Positional, bound via synthetic `let` statements, type-checked                                    |
| Tabular parameters                      | ✅      | `(T:(x:long), v:long)`; `T:(*)` any-schema form; ordering (tabular before scalar) enforced         |
| Tabular arguments with their own `let`     | ✅      | `MyFilter((let x = 10; T \| where Y >= x), 9)`                                                       |
| Nested function calls as arguments            | ✅      | One stored function's result passed as another's tabular argument                                    |
| `.show functions` / `.show function Name`        | ✅      | Introspection                                                                                          |
| `.drop function` / `.drop functions`               | ✅      |                                                                                                           |

### Materialized Views

`.create materialized-view Name on table Source { Query }` — incrementally maintained on write, not recomputed from scratch each read.

| Feature                          | Status | Notes                                                                              |
| ------------------------------------ | ------ | --------------------------------------------------------------------------------------- |
| Create / show / drop                    | ✅      | Single `summarize` as the last operator, 16 supported aggregates                          |
| Incremental maintenance on write            | ✅      | Detached per-write maintenance; readers wait for in-flight maintenance before scanning       |
| Truly incremental aggregates                  | ✅      | count/sum/min/max/arg_max/arg_min/take_any/make_set/make_list/make_bag                        |
| Full-recompute aggregates                       | ✅      | avg/dcount fall back to full recompute (correct, not streaming — needs hidden state)             |

---

## Federation (Cross-Database / Cross-Cluster Queries)

Not present in any prior version of this document.

| Feature                     | Status | Notes                                                                          |
| -------------------------------- | ------ | ---------------------------------------------------------------------------------- |
| `database('alias').Table` syntax    | ✅      | Real-ADX-conformant cross-database source reference                                  |
| Operator pushdown across the boundary | ✅      | where/project*/extend/take/sample/count/distinct/sort/top/summarize/getschema/parse/serialize/mv-expand pushed to the remote engine; longest pushable prefix computed automatically |

---

## `_TimeReceived` (Automatic Ingest-Time Column)

Not present in any prior version of this document.

| Feature                              | Status | Notes                                                                                    |
| ----------------------------------------- | ------ | ---------------------------------------------------------------------------------------------- |
| Automatic on every table by default          | ✅      | Real, ordinary visible column, appended last — matches real ADX/Log Analytics's own storage-level behavior |
| Hidden from default query output              | ✅      | Matches real Log Analytics's own documented rule ("won't show... unless you explicitly specify the column in the output"); revealed when a query references it by name anywhere |
| Per-table opt-out                               | ✅      | `.create table T (...) with (notimereceived=true)`                                               |
| Scope-level opt-out                               | ✅      | `.okql-schema-options.json` → `{"disableTimeReceived": true}`                                      |
| Survives compaction unchanged                       | ✅      | Stamped only for a genuinely new write (nil value); a compaction rewrite leaves an existing value untouched |
| `.alter-merge table T (Col: type)`                    | ✅      | Real-ADX-conformant schema evolution — retrofit any column (including `_TimeReceived`) onto a table created before it existed; existing rows read back as null for the new column |

---

## Host Integration (`ExternalFunctionResolver`)

Not present in any prior version of this document. A small, additive extension point (`pkg/engine/external_resolver.go`) for a host application embedding this engine as a Go module dependency to resolve a tabular function-call source against its own external system instead of this engine's stored-function catalog — e.g. a host's own GraphQL-backed API surfaced as `connectors(first: 100) | where status == "ERROR"`, with everything after the first `|` running through this engine's real, unmodified pipeline. Checked before the normal stored-function lookup, matching the precedence built-in table functions already get over a same-named stored function. `nil` by default; zero impact unless a host sets it.

---

## File-Based Querying (Table-Valued Functions)

Query external files directly without ingestion — the DuckDB-style workflow.

| Function          | Status | Notes                                                                |
| ------------------- | ------ | ---------------------------------------------------------------------- |
| csv("path")            | ✅      | Auto-detects schema (datetime, bool, int, float, string) from sample     |
| json("path")             | ✅      | Reads JSON arrays or single objects                                       |
| ndjson("path")             | ✅      | Newline-delimited JSON                                                     |
| parquet("path")              | ✅      | Full Parquet support via parquet-go (all physical + logical types)          |

All file sources produce standard `*types.Table` and feed into the full KQL operator pipeline. `parquet-go` is consumed as its real, public, tagged version — no local `replace` directive or manually-cloned sibling repo required (fixed 2026-08-14; the previous local-path setup had no code reason behind it, it's a plain unmodified upstream clone).

**xlsx/`excelize` support was deliberately removed (2026-08-15)**: the project's actual direction is Vortex-columnar storage, not DuckDB-style multi-format parity, and .xlsx (non-columnar, an external dependency pulling in 7 module entries transitively) no longer served that direction. CSV/JSON/NDJSON/Parquet all remain, and cover the project's real, active use cases.

---

## Management Commands

Not documented anywhere in the previous version of this file.

| Command                                        | Notes                                                                                     |
| --------------------------------------------------- | ----------------------------------------------------------------------------------------------- |
| `.create table T (Col: type, ...)`                     | With optional `with (notimereceived=true)`                                                          |
| `.create-merge table T (...)`                            | Create-or-extend: adds missing columns to an existing table, or creates it if absent                  |
| `.alter-merge table T (Col: type, ...)`                    | Schema evolution on an existing table; rejects a type change to an existing column                      |
| `.create-or-alter function Name(params) { body }`            | See Stored Functions above                                                                                |
| `.create [ifnotexists] materialized-view Name on table T {...}` | See Materialized Views above                                                                                 |
| `.drop table / function / functions / materialized-view`         | Archival drop (not delete) for tables, matching real ADX semantics                                              |
| `.ingest csv into table T from "path"`                              | CSV file ingest                                                                                                   |
| `.ingest inline into table T <\| data`                                | Inline tabular data ingest                                                                                          |
| `.set-or-append T <\| query`                                             | Write query results into a table, creating it if absent                                                              |
| `.embed-into T <\| query`                                                  | Bulk-embed a Text column through Ollama's batch endpoint, one HTTP call per batch                                       |
| `.compact table T` / `.compact database`                                    | Extent merging/compaction, including across the whole scope                                                              |
| `.merge` / `.gc` / `.chunk-file`                                               | Extent maintenance, garbage collection, and manual chunk-file operations                                                   |
| `.show tables / table T / functions / function Name`                            | Introspection                                                                                                                |

---

## CLI / REPL Features

### Invocation Patterns (DuckDB-style)

| Pattern                           | Description                           |
| ------------------------------------ | ------------------------------------------ |
| `okql`                                  | In-memory REPL (no database required)         |
| `okql mydb.vortex`                        | Persistent REPL with database                   |
| `okql mydb.vortex "Logs \| take 5"`         | One-shot query against database                    |
| `okql "print 6 * 7"`                          | One-shot query, in-memory                             |
| `echo "query" \| okql`                          | Pipe mode                                                |
| `okql -readonly mydb.vortex`                      | Read-only mode                                              |

### Output Modes

| Mode  | Flag          | Description                            |
| ------- | --------------- | ------------------------------------------- |
| table    | `-mode table`      | Aligned columns with headers (default)          |
| csv        | `-mode csv`          | Comma-separated values                              |
| tsv          | `-mode tsv`            | Tab-separated values                                   |
| json           | `-mode json`             | Pretty-printed JSON array                                  |
| jsonl            | `-mode jsonl`              | Newline-delimited JSON                                        |

### REPL Dot-Commands

| Command                                | Description                          |
| ----------------------------------------- | ------------------------------------------ |
| `.open [PATH]`                                | Switch database (no arg = in-memory)          |
| `.schema [TABLE]`                               | Show table schema(s)                              |
| `.timer [on\|off]`                                | Show query execution time                             |
| `.mode [table\|csv\|json\|jsonl\|tsv]`              | Output format                                            |
| `.output [FILE\|-]`                                   | Redirect output to file                                     |
| `.read FILE`                                            | Execute KQL script file                                        |
| `.help`                                                    | List all supported dot-commands as a queryable table              |
| `.version`                                                   | Show version                                                          |
| `.quit`                                                        | Exit                                                                     |

### Configuration

- `~/.okqlrc` — Startup commands (dot-commands only)
- `.vortex` — Recommended database directory extension (names the actual on-disk format; `.okql` still recognized for backward compatibility, no longer the suggested default — see docs/cli_design.md)

---

## Other Language Features

### Implemented

| Feature             | Status | Notes                                                |
| ---------------------- | ------ | ----------------------------------------------------- |
| Let statements             | ✅      | Scalar and tabular bindings, chaining, including a tabular binding whose own value has its own `let` |
| Subqueries                    | ✅      | In join, union, in-operator, and `toscalar()`                                                            |
| toscalar()                       | ✅      | Bridges a tabular expression into a scalar context; evaluated once per operator invocation, before any per-row loop, never via shared state (a real concurrency bug was found and fixed here mid-session before landing) |
| JSON dot-access                    | ✅      | `col.property` and `col["key"]` syntax                                                                       |
| Dynamic literals                     | ✅      | JSON arrays and objects in expressions; both single- and double-quoted strings accepted, matching real ADX's own tolerant grammar |
| Timespan literals                      | ✅      | 1h, 30m, 1d, 7d, etc.                                                                                            |
| Timespan parsing                         | ✅      | KQL `d.hh:mm:ss.fffffff` format                                                                                     |
| Datetime arithmetic                        | ✅      | datetime ± timespan                                                                                                   |
| Pipe operator                                | ✅      | Standard KQL chaining                                                                                                   |
| Multiple statements                            | ✅      | Semicolon-separated, correctly respecting `{...}`/`(...)` nesting (a real parser bug — semicolons inside a function/materialized-view body were once misread as top-level separators — was found and fixed) |
| Aliased expressions                              | ✅      | `NewName = expression` in extend, summarize, project                                                                     |
| Scalar user-defined functions                      | ✅      | `let f = (x: long) { x * 2 }`                                                                                               |
| Stored (persisted) functions                         | ✅      | See Stored Functions above — a distinct capability from scalar UDFs                                                          |

### Not Yet Implemented

| Feature                    | Priority | Notes                            |
| ------------------------------ | -------- | ------------------------------------- |
| **Query parameters**              | 🟢 LOW   | `declare query_parameters(...)`          |

---

## Vortex Storage Engine Features

| Feature                       | Status | Notes                                      |
| ---------------------------------- | ------ | ------------------------------------------------ |
| Columnar storage                       | ✅      | Vortex format (25 decoders)                          |
| Zone map pruning                         | ✅      | Predicate pushdown, 6× speedup on 50K rows              |
| Multi-zone extents                         | ✅      | 8,192 rows per zone chunk                                 |
| Column projection                            | ✅      | Only reads required columns from disk                       |
| Schema evolution on read                       | ✅      | A column requested but absent from a specific extent (e.g. one written before an `.alter-merge table`) reads back as null for that extent, not an error |
| Database-wide shared dictionary                  | ✅      | Low-cardinality string columns deduplicated across the whole database via `_Dictionaries`, not just per-extent |
| CSV ingestion                                      | ✅      | `.ingest csv into table T from "file.csv"`                    |
| Extent merging                                       | ✅      | Compaction of small extents                                     |
| Catalog persistence                                    | ✅      | JSON-based table/extent metadata                                   |
| In-memory mode                                           | ✅      | Transient catalog with no disk persistence                            |

---

## Development Roadmap

### ✅ Sprint 1: Quick Wins — COMPLETE

1. ✅ project-away, project-rename, print (new operators)
2. ✅ between / !between (range expressions)
3. ✅ =~, !~, contains_cs, has_cs, startswith_cs, endswith_cs + all negations, matches regex
4. ✅ functions.go refactored into 4 focused files

### ✅ Sprint 2: Core Language Gaps + CLI/UX Redesign — COMPLETE

1. ✅ **parse operator** — simple, relaxed, and regex with named capture groups
2. ✅ **getschema** — return schema as table
3. ✅ **Math functions** (17) — round, abs, pow, sqrt, log/log2/log10, ceiling, exp/exp2/exp10, pi, sign, isnan/isinf/isfinite
4. ✅ **tobool** — boolean type conversion
5. ✅ **Dynamic/array functions** (17) — array_length/concat/index_of/reverse/slice/sort, bag_keys/has_key/pack, pack_array/dictionary, set operations, treepath
6. ✅ **String utilities** (5) — strcmp, strrep, translate, strcat_delim, string_size, url_decode
7. ✅ **Datetime functions** (13 new) — datetime_add, totimespan, make_timespan, endofyear, dayofyear, quarter, week_of_year, microsecond, and KQL timespan format parsing
8. ✅ **CLI redesign** — DuckDB-style positional args, in-memory default, 5 output modes (table/csv/json/jsonl/tsv), dot-commands, RC file
9. ✅ **File-based querying** — csv(), json(), ndjson() table-valued functions with auto-schema detection
10. ✅ **Parquet support** — parquet() table function via parquet-go, full type mapping

### ✅ Sprint 3: KQL Parity + Advanced Features — COMPLETE

1. ✅ **in~ / !in~** — case-insensitive set membership
2. ✅ **has_any / has_all** — set-based string operators
3. ✅ **minif / maxif** — conditional min/max aggregations
4. ✅ **make_bag / make_bag_if** — JSON object aggregation
5. ✅ **percentiles (multi)** — multiple percentiles in one call
6. ✅ **sample** — random row sampling operator
7. ✅ **datatable** — inline tabular data with typed columns
8. ✅ **IP functions** — has_ipv4, ipv4_compare, format_ipv4
9. ✅ **Hashing & identity** — hash_md5, hash_sha1, new_guid
10. ✅ **format_timespan / rand** — timespan formatting + random number generation
11. ✅ **project-reorder / project-keep** — column management operators
12. ✅ **serialize + window functions** — row_number(), prev(), next() with offset and default support

### ✅ Sprint 4: Polish & Remaining Gaps — COMPLETE

1. ✅ **lookup** — simplified join for dimension table enrichment
2. ✅ **render** — parsed as metadata, engine pass-through
3. ✅ **Scalar user-defined functions** — via let lambda
4. ✅ **hasprefix / hassuffix** — word-boundary prefix/suffix matching
5. ✅ **like / !like** — wildcard pattern matching
6. ✅ **stdevif / stdevp / variancep / varianceif / variancepif** — full statistical function family
7. ✅ **binary_all_xor** — bitwise XOR aggregation

### ✅ Sprint 5: Graph Operators — COMPLETE

1. ✅ **make-graph** — directed graph from an edge table, optional node table
   (`Edges | make-graph Src --> Dst with Nodes on NodeId`). Full edge rows and
   node property rows carried into an adjacency-list graph.
2. ✅ **graph-to-table** — materialize `edges` (default) or `nodes`; derived
   single-NodeId form when no node table was supplied.
3. ✅ **graph-match** — path patterns `(a)-[e]->(b)`, multi-hop chains, and
   variable-length `-[e*1..5]->`. Pattern variables surface as dotted columns
   (`a.Kind`, `e.Src`); variable-length edge variables bind a JSON edge array
   (`array_length(e)`, `e[0].Col`). Optional `where`, required `project`.
   Edges are unique within a matched path (terminates cycles).

Pipeline plumbing: `pipeState` threads either a table or a graph between
operators; `applyOperator` stays pure table→table.

### ✅ Sprint 6: Stored Functions, Materialized Views, Federation — COMPLETE

1. ✅ **Stored functions** — scalar and tabular parameters, tabular arguments with their own `let`, nested calls as arguments
2. ✅ **Materialized views** — create/show/drop, incremental maintenance on write
3. ✅ **Federation** — cross-database `database('alias').Table` with operator pushdown across the boundary
4. ✅ **`_TimeReceived`** — automatic ingest-time column, hidden-by-default display rule, `.alter-merge table` schema evolution
5. ✅ **`toscalar()` / `in (subquery)`** — bridging tabular expressions into scalar/predicate context, safely (no shared state)
6. ✅ **`range`** — table-generating source operator
7. ✅ **`evaluate` (general plugin dispatch) + `bag_unpack`** — first plugin on a registry, not a one-off special case
8. ✅ **`find` / `partition`** — cross-table search predecessor, group-and-subquery
9. ✅ **`ExternalFunctionResolver`** — host-embedding extension point (wizTainer integration)
10. ✅ **Aggregation function sweep** — take_any/take_anyif, count_distinct/count_distinctif, percentiles_array, varianceif/variancepif, make_list_with_nulls, zip, array_shift_left/right, bag_merge, bag_remove_keys, url_encode, parse_csv, has_any_index, anyif
11. ✅ **Embeddings & vector similarity** — embed_text, series_cosine_similarity, series_dot_product, `.embed-into` bulk embedding
12. ✅ **Dependency hygiene** — dropped unnecessary local `replace` directive for parquet-go (a plain, unmodified upstream clone; no code reason for local pinning)
13. ✅ **xlsx/`excelize` removed** — deliberate scope narrowing (2026-08-15): the project's real direction is Vortex-columnar storage, not DuckDB-style multi-format parity; xlsx was non-columnar, untested in real practice, and pulled in 7 module entries transitively for no active use

### ✅ Sprint 7: Tabular Operator Gap Closure (parse-where, parse-kv) — COMPLETE (2026-08-15)

1. ✅ **parse-where** — same pattern grammar as `parse` (shared via a new `parseParsePatternClause`/`applyParseCore` factoring so the two operators can't drift), but always drops non-matching rows instead of nulling them, matching real ADX's own documented behavior
2. ✅ **parse-kv** — "specified delimiter" mode (`pair_delimiter`/`kv_delimiter`, `quote`, `escape`, `greedy`); verified against every worked example in real ADX's own docs (basic split, quoted key+value, escaped-quote-in-value, greedy mode) with exact expected values, not just "does it run"; the non-specified-delimiter and regex modes are explicitly out of scope, rejected with a clear error
3. ✅ **Real, pre-existing bug found and fixed along the way**: `splitPipe`/`splitRespectingParens`/`findKeyword` shared a naive "quote preceded by exactly one backslash = escaped" check that mishandles a doubled `\\` (KQL's own escape for one literal backslash, e.g. parse-kv's own `escape='\\'` option) — a backslash-parity check (`precededByOddBackslashes`) replaces it in those three functions. The same naive pattern still exists in 9 other call sites in `parser.go`, deliberately left unfixed this session (real regression risk to a 470-test suite for a fix outside this session's actual scope) — flagged here as a known follow-up, not silently left undocumented.
4. ✅ 16 new tests (12 engine-level in `parse_where_test.go`/`parse_kv_test.go`, 4 parser-level regression tests in `backslash_parity_test.go`), full suite + `-race` clean, verified against all three real production scopes (dumuzi=70, nergal=154, girsu-paper≥100)

### ✅ Sprint 8: make-series — COMPLETE (2026-08-15)

1. ✅ **make-series** — main syntax only (`from`/`to`/`step`, `by` clause, `kind=nonempty`, multiple `Column=Aggregation[default=...]` entries), reusing `summarize`'s own `parseAggregation`/`computeAgg`/auto-naming machinery directly rather than a parallel implementation. Verified exactly against every value in real ADX's own worked examples: the primary `avg(metric)` example (9-element array `[4,3,5,0,10.5,4,3,8,6.5]`, byte-for-byte-matching `.0000000Z` datetime formatting), the empty-input-produces-zero-rows case, `kind=nonempty`, and the `sum()+default()+mv-expand` example — plus a `by`-clause test and a real-typed-axis test not covered by any single real-ADX doc snippet. Proper floor-division `bin_at` (not Go's truncating integer division), verified against the exact edge case real ADX's own example exercises: a data point before `from` correctly lands one bin early and is excluded from the output range entirely. 2^20 array-length cap enforced; non-numeric aggregations and non-positive step both rejected with clear errors.
2. ✅ **Two more real, pre-existing bugs found and fixed along the way**:
   - `convertDataTableValue` only tried evaluating a datatable literal token as a KQL expression first for `TypeDynamic` columns — so a `datetime(...)`-wrapped literal (completely standard, documented KQL, confirmed directly against Microsoft's own `datatable-operator.md` worked example) failed outright in any other typed column, handing the still-wrapped text straight to `types.ParseValue`. Fixed by extending the same expression-first treatment to any unquoted, function-call-shaped token (checked via a quote-prefix exclusion + `(`-containing heuristic) — narrow enough to leave quoted strings and bare numeric/bool literals untouched, since an initial unconditional broadening caused exactly that regression and a pre-existing test (`TestDatatableDatetimeNotEpochZero`) caught it immediately.
   - A step-size bug: `inferExprType` has no way to recover a `let`-bound scalar's original declared type (its `ColumnRef` case only checks a schema, silently defaulting to `TypeString` for anything else) — so `let interval = 1d; ... step interval` silently skipped the ticks→nanoseconds conversion, producing bins 100x too small (14m24s-spaced instead of 1-day-spaced). Fixed by keying the unit-conversion decision off the AxisColumn's own type instead of re-inferring the step expression's type — more robust than a workaround, since real ADX's own grammar already requires step's dimension to match the axis (a timespan step for a datetime axis), making the axis type the authoritative source regardless of how step was spelled in the query.
3. ✅ 12 new `make-series` tests + 1 new `datatable` regression test (`TestDatatableFuncCallLiteral`), full suite (500 tests) + `-race` + `go vet` clean, verified against all three real production scopes (dumuzi=70, nergal=154, girsu-paper=100)

### ✅ Sprint 9: as + scan — COMPLETE (2026-08-15)

1. ✅ **as** — binds a name to the input tabular expression by registering it into `e.letContext.Tables` (the same map table-source resolution already checks first); scoped to same-query, downstream reference (a later `join`/`union` subquery within the SAME query) — cross-statement reference and real ADX's own `withsource=`/`source_`/`$table` column-naming integration are both explicitly out of scope, verified as genuinely unsupportable given this engine's `CompoundStatement` shape rather than merely unimplemented.
2. ✅ **scan** — single-step state machine (`declare(...)`, `step Name [output=all|last|none] : Condition => Column=Assignment[,...]`); verified exactly against both of real ADX's own worked examples, including hand-tracing the two-column reset-at-10 example row by row. `StepName.ColumnName` state references reuse this engine's existing JSON dot-access machinery (a synthetic column holding the current state as JSON) rather than a new evaluation case. Multi-step sequences and `with_match_id` are out of scope, rejected with a clear error.
3. ✅ **A real, live data race found and fixed while implementing `as`**: the first version of `executeQuery`'s own let-context save/restore wrapper (needed because `applyAs` can lazily create a `LetContext` mid-pipeline) unconditionally touched the shared, package-level `activeLetContext` on every query, not just ones using `as` — `go test -race` caught this immediately via `TestToScalarConcurrentEnginesUseCorrectOwnEngine` (30 independent Engine instances running truly concurrently). Fixed by checking upfront whether a query's own pipeline actually contains an `AsOp` before wrapping at all.
4. ✅ 11 new tests (4 in `as_test.go`, 7 in `scan_operator_test.go` — kept deliberately separate from the pre-existing `scan_test.go`, which covers this engine's own internal storage-scan strategies, an unrelated pre-existing meaning of "scan"), full suite (510 tests) + `-race` clean, verified against all three real production scopes.

### ✅ Sprint 10: project-by-names — COMPLETE (2026-08-15)

1. ✅ **project-by-names** — select and reorder columns by exact name, wildcard pattern, `dynamic([...])` array literal, a let-bound/parameter-bound dynamic array reference, or `column_names_of(TableRef)` (a small, deliberately narrow special-cased form, not a general scalar function). Verified against all five of real ADX's own worked examples, including the `column_names_of(Source)` + dynamic-array-parameter lookup pattern and the "ignore nonexisting columns" case. Reuses `matchesAnyPattern` (already built for `project-keep`) for wildcard matching; wired into `planner.go`'s row-preserving list and `_TimeReceived` explicit-reference check, matching `project-keep`'s own established precedent for both.
2. ✅ 5 new tests, full suite (515 tests) + `-race` + `go vet` clean, verified against all three real production scopes.

### ✅ Sprint 11: sample-distinct — COMPLETE (2026-08-15)

1. ✅ **sample-distinct** — `NumberOfValues of ColumnName`, returning a single output column with up to N distinct values via a deterministic first-N-distinct-encountered scan with early exit (real ADX itself documents no specific sampling distribution to match, only that results are "biased, not fair"). Wired into `planner.go` as both a schema barrier and a narrowing operator, mirroring `distinct`'s own existing treatment exactly.
2. ✅ 6 new tests (including a non-numeric string-column case, N larger than the actual distinct-value count, and negative-N rejection), full suite (521 tests) + `-race` + `go vet` clean, verified against all three real production scopes.

**This closes all 7 of the original tabular operator gaps** (`parse-where`, `parse-kv`, `make-series`, `as`, `scan`, `project-by-names`, `sample-distinct`) — every one verified against real ADX's own documented behavior with exact worked-example values, not just "does it parse."

### ✅ Sprint 12: invoke — COMPLETE, reduce deliberately deferred (2026-08-15)

Beyond the original 7-gap backlog: a review of the 13 "Out of Scope" operators surfaced two (`invoke`, `reduce`) worth reconsidering given this project's actual use (Wiz security-finding triage, where `reduce`'s log-clustering purpose is directly relevant) rather than leaving the whole "out of scope" bucket undifferentiated.

1. ✅ **invoke** — scoped to stored functions (`.create-or-alter function`) only; see its own scope note above (Implemented table) for why real ADX's own primary worked example (an inline tabular `let`-lambda) isn't covered. Desugars `T | invoke F(args)` into a stored-function call with `T` prepended as the implicit first tabular argument.
2. ✅ **A real, foundational bug found and fixed while researching invoke, landed as its own separate commit before invoke itself**: a `let`-bound (or `as`-bound) table passed as any stored function's tabular argument — an ordinary pattern, `let T = ...; MyFunc((T))` — failed with "table not found." Root cause: the argument's AST was re-executed a second time inside the callee's own `executeCompound` call, by which point the callee's fresh `LetContext` had already replaced the caller's. Fixed via a new `PrecomputedTable` AST node that captures the argument's result once, in the caller's still-current scope, removing both the bug and an already-acknowledged double-execution inefficiency in one change.
3. ✅ **Two more real bugs found and fixed while verifying `invoke` against real ADX's own `clipped_average` worked example end to end**: `kqlTypesCompatible` had no long/int→real widening at all (a bare integer literal argument for a real-typed parameter was rejected outright); `percentiles` (plural — real ADX's own actual spelling in this exact example) wasn't recognized as an aggregation function, only the singular `percentile` — aliased for the single-percentile-argument case only, not a claim of full multi-percentile support.
4. **A genuinely separate, deeper limitation found (not fixed) while testing the `PrecomputedTable` fix**: `LetContext` isn't lexically scoped/chained at all — a nested compound tabular argument with its own further `let` bindings still can't see an outer-scope name, since every `executeCompound` call installs a fully isolated context with no parent-chain fallback. Documented in `TestLetBoundTableWithOwnLetsAsTabularArgument`'s own comment as a real, bigger architectural gap for a future session, not silently papered over.
5. ❌ **reduce — deliberately deferred, not attempted.** Real ADX's own docs specify `reduce`'s input/output shape and a few worked examples but not the precise algorithm internals (Vaarandi's SLCT). Reverse-engineering the docs' own first worked example against the documented default tokenization rule doesn't work cleanly — see the "Out of Scope" section's own `reduce` note above for the specific reasoning (a character-level prefix/suffix decomposition step that the public sources available don't specify precisely enough to reproduce with confidence). Given this project's actual use case (triaging real security findings), a plausible-but-uncalibrated implementation was judged worse than an honest, documented gap.
6. ✅ 9 new tests across the three commits (3 for the `PrecomputedTable` fix, 5 for `invoke` itself, 2 regression tests for the type-widening/percentiles-alias fixes — one more than 9 due to a pre-existing test's comment update not counted separately). Full suite (531 tests) passes with `-race`, `go vet` clean throughout. Verified against all three real production scopes at every step.

Tabular operators: 41/53 implemented, 0 gaps remaining, 12 deliberately out of scope (`reduce` among them, now with its own specific, honest reasoning rather than a blanket "not prioritized").

### ✅ Sprint 13: series_* Tier 1 + Tier 2 — COMPLETE (2026-08-17)

1. ✅ **26 functions**: all of Tier 1 (arithmetic, comparison, unary math, trig — 22 functions) and all of Tier 2 (gap-filling — 4 functions). See the Scalar Functions section's own new "Time Series" subsection above for the full function list and `docs/timeseries_backlog.md` for the complete verification detail per function. Two real findings corrected earlier planning-stage guesses in that same backlog document: comparison functions return **booleans**, not 1.0/0.0 (verified directly against real ADX's own docs); the arithmetic and cosine-similarity function families use two genuinely *different* documented conventions for mismatched array lengths (null-padding vs. truncate-to-shorter), not one.
2. ✅ **Two more real, separate bugs found and fixed while wiring up the actual make-series + series_fill_forward integration path end to end** (the documented reason series_fill_* exists at all, not a hypothetical): bare `double()`/`real()` (and `int()`/`long()`/`bool()`) didn't exist at all as type-cast functions, only their `to`-prefixed forms did; and `toint`/`tolong`/`todouble`/`toreal` silently returned 0/0.0 for a null argument instead of propagating null, breaking real ADX's own documented `default=double(null)` idiom outright. Adding the bare aliases caused one real, immediately-caught regression (`TestUDFBasic`, already in the suite, exercising a user-defined function literally named `double`) — fixed via a narrow user-function-wins check rather than broadening to every built-in.
3. ✅ 15 new tests across two files, full suite (547 tests) passes with `-race`, `go vet` clean, verified against all three real production scopes.

Scalar functions: ~178 entry points implemented (was ~150). 23 `series_*` functions remain: Tier 3 (summary statistics, 6 functions, MED priority) is the natural next step; Tiers 4-5 (curve fitting, signal processing, 14 functions) stay LOW priority per this project's own use cases.

### ✅ Sprint 14: series_* Tier 3 (minus series_stats) — COMPLETE (2026-08-17)

1. ✅ **5 functions**: series_sum, series_product, series_magnitude, series_pearson_correlation, series_stats_dynamic. Verified exactly against real ADX's own worked examples where fetched (series_sum, series_stats_dynamic — every field of its 9-field object checked); series_pearson_correlation's null rule confirmed stricter than the Tier 1 arithmetic family's own (a whole-result null, not per-position). `series_stats` itself (the multi-column sibling) deliberately not implemented — its real syntax needs destructuring-assignment grammar (`extend (a,b,c) = expr`) that doesn't exist anywhere in this engine's parser, a real, separate, bigger gap documented inline rather than faked.
2. ✅ **A real, separate bug found and fixed while verifying series_pearson_correlation against its own real-ADX worked example** (summarize make_list(...), that function's own documented calling pattern): make_list/make_set — and their `_if`/`_with_nulls` variants, 6 case blocks total, each needing the same fix separately since none of them shared the logic — stringified every element before JSON-marshaling, so make_list(LongColumn) silently produced a JSON array of quoted strings instead of numbers. Fixed by storing each element's native typed value (reusing valueForJSONArray from make_series.go for correct datetime/long/real JSON encoding). make_set's own sort.Strings call (silently wrong for numeric sets) was removed in the same pass — real ADX documents make_set as unordered, so sorting was never a real requirement.
3. ✅ 10 new tests, full suite (555 tests) passes with `-race`, `go vet` clean, verified against all three real production scopes.

Scalar functions: ~183 entry points implemented. `series_*`: 31/49 implemented (`series_stats` deliberately deferred, documented above). 18 functions remain, all in Tiers 4-5 (curve fitting, signal processing) — already LOW priority, no MED-priority time-series work left.

### ✅ Sprint 15: scalar trigonometry + IPv4/IPv6 family + parse_command_line/parse_user_agent + covariance — COMPLETE (2026-08-17/18)

1. ✅ **Scalar trigonometry (9 functions)**: `sin`, `cos`, `tan`, `asin`, `acos`, `atan`, `atan2`, `degrees`, `radians` (`func_convert.go`), each verified against real ADX's own worked examples (`atan2(1,1)=Pi/4` etc; `degrees(pi()/4)=45` etc; `radians(90)=1.5707963267949` etc). The pre-existing `series_sin`/`series_cos`/etc family (`func_series.go`) previously called `math.Sin`/`math.Cos`/etc directly with no scalar counterpart to share logic with — refactored both families onto six shared `trigXxx` helpers so they can't silently drift apart, verified directly with a scalar-vs-series parity test.
2. ✅ **IPv4 text-match/range/mask family (9 new functions)**: `has_any_ipv4`, `has_ipv4_prefix`, `has_any_ipv4_prefix`, `ipv4_is_in_any_range`, `ipv4_is_match`, `ipv4_range_to_cidr_list`, `ipv4_netmask_suffix`, `format_ipv4_mask`, `parse_ipv4_mask` (`func_net.go`), each verified against real ADX's own worked-example tables — including `ipv4_range_to_cidr_list` hand-traced bit-by-bit against its exact 3-block worked example before trusting the algorithm.
3. ✅ **Five real, pre-existing bugs found and fixed in already-implemented sibling IPv4 functions**, all reproduced live before fixing, not hypothetical:
   - `has_ipv4`'s own comment claimed "word boundary aware" but the implementation was a plain `strings.Contains` with no boundary check at all — an IP glued directly to preceding digits (real ADX's own documented "improperly delimited" case) matched when it must not.
   - `format_ipv4` couldn't parse an address argument carrying its own embedded `/prefix` at all (`net.ParseIP` fails outright on the literal suffix), and neither it nor the new `format_ipv4_mask` combined an embedded prefix with an explicit one via MIN — real ADX's own 4-row worked-example table requires both fixes together (`format_ipv4('192.168.1.1/24', 32)` must be `"192.168.1.0"`, not null).
   - `ipv4_compare` had the identical embedded-prefix parsing failure — 3 of 4 rows in its own first real worked-example table returned null instead of the documented `0`.
   - `ipv4_is_private` called `net.ParseIP` directly, failing on an embedded `/prefix` suffix.
   - `ipv4_is_in_range` called `net.ParseCIDR` directly on the range argument, which requires a `/N` suffix to be present at all — the documented "equal IPs, no range notation" case (implicit /32) returned null instead of true.
   All five now route through a new shared `parseIPv4WithPrefix` helper plus, where real semantics call for it, a min-of-all-supplied-prefixes rule — the same pattern already correct in `ipv4_is_match`, applied consistently instead of each function inventing its own parsing.
4. ✅ **parse_command_line** (`func_net.go`): implements the standard Win32 CommandLineToArgvW tokenization algorithm — real ADX's own docs cite this exact algorithm by name for `parser_type="windows"` (the only currently-documented value). Verified exactly against the real worked example (`parse_command_line('echo "hello world!"', 'windows') == ["echo","hello world!"]`) plus the backslash-run parity rule (even run before a quote → N/2 literal backslashes, quote toggles; odd run → literal escaped quote), hand-verified via `strlen` before trusting the raw test input.
5. ✅ **covariance / covariancep / covarianceif / covariancepif** (`aggregation.go`): sample/population covariance with conditional variants, same shape as the already-implemented variance family. Verified exactly against real ADX's own worked example (`covariance(x,y) == 20.5` for `x=[1,2,3,4,5], y=[14,10,17,20,50]`). Confirmed "null values are ignored" means PAIRWISE exclusion (either side null drops the whole pair), and that the auto-generated output column name uses BOTH argument names (`covariance_x_y`) — a real divergence from every other single-column aggregate's `function_argname[0]` naming convention, requiring a dedicated naming branch in `pkg/parser/parser.go`'s `parseAggregation`.
6. ✅ **`parse_user_agent` — REVERSED 2026-08-18, now implemented**: the original deferral (item 6 in an earlier pass of this document) was based on the actual `ua-parser`/`uap-core` regex pattern database being assumed unfetchable from this session. That assumption was directly challenged and checked rather than left standing: the database (`regexes.yaml`, Apache License 2.0, Copyright 2009 Google Inc — the exact database real ADX's own docs describe `parse_user_agent()` as being "built on regex checks... against") is public and was confirmed fetchable. Every one of its 1270 patterns (433 `user_agent_parsers`, 204 `os_parsers`, 633 `device_parsers`) was confirmed to compile successfully under Go's RE2-based `regexp` engine — zero backreferences, lookaheads, or other PCRE-only constructs anywhere in the file — before writing a single line of the new `pkg/engine/uaparser` package, which embeds the real, unmodified data via `go:embed` and implements uap-core's own published matching specification exactly (first-match-wins per category, replacement-template expansion, `regex_flag:'i'` case-insensitivity, `Family="Other"` fallback on no match), compiled once via `sync.Once` rather than per call. Verified exactly against two of real ADX's own worked examples: the Nokia N81/SymbianOS/Series60 case (a deliberately tricky one — the raw string contains a literal `Safari/4` substring that a naive parser would misclassify, but the real answer, `"Nokia OSS Browser"` version 3.1 sourced from a completely different token, `Series60/3.1`, falls straight out of the real pattern database's own rule for exactly this combination) and the AdobeAIR single-string-`look_for` case, both matching byte-for-byte including exact JSON key order (guaranteed via ordered Go structs, since `encoding/json` sorts map keys alphabetically — which would wrongly put `Brand` before `Family` in the `Device` object — but preserves struct field order exactly).
7. ✅ **IPv6 family (6 functions)**: `parse_ipv6`, `parse_ipv6_mask`, `ipv6_compare`, `ipv6_is_match`, `ipv6_is_in_range`, `ipv6_is_in_any_range` (`func_net.go`) — the ambiguity flagged in an earlier pass of this same sprint was resolved by fetching the FULL real worked-example tables directly (not via search snippets, which had truncated and in one case misleadingly-labeled the exact numbers needed). Two genuinely non-obvious rules were found ONLY in the actual numeric output of these tables, stated nowhere in the prose docs: (a) an embedded prefix on an address whose TEXT contains a literal `.` character gets a `+96` offset into 128-bit space, verified via `parse_ipv6_mask`'s own 7-row table (`'192.168.255.255/24'` + explicit 124 produces the IDENTICAL output to unprefixed `'192.168.255.255'` + explicit 120 — both effective at 120); (b) the literal `"::a.b.c.d"` spelling — which Go's own `net.ParseIP` renders with NO `ffff` inserted, confirmed directly against Go's real behavior — is auto-canonicalized by real ADX to the IPv4-mapped form instead. Every one of real ADX's own 21 combined `ipv6_is_match` worked-example rows (including 4 genuinely mixed IPv4/IPv6-notation rows) was hand-verified and matches exactly.
8. ✅ **A real bug found and fixed in this sprint's own new IPv6 code**, before it was ever committed: an early version of the embedded-prefix-offset decision (rule (a) above) inspected the parsed BYTE VALUE rather than the address's own source TEXT, and wrongly rejected `'0:0:0:0:0:ffff:c0a8:ac/60'` (real ADX's own `ipv6_is_in_range` worked example — written entirely in colon-hex-group notation with no dot anywhere, even though its parsed value happens to fall in the `::ffff:0:0/96` range) as an out-of-range IPv4 prefix (`60 > 32`). Fixed by deciding the offset purely from whether the address text contains a literal `.` — caught by fetching and checking the real worked-example table's actual output (which also corrected an earlier, wrong assumption made mid-session that all three of that table's rows returned `true`; the real, documented third row is `false`).
9. ✅ 42 new tests across seven files (`func_trig_test.go`, `func_ipv4_test.go`, `func_parse_command_line_test.go`, `aggregation_covariance_test.go`, `func_ipv6_test.go`, `pkg/engine/uaparser/uaparser_test.go`, `func_parse_user_agent_test.go`) — one worked-example test per new function plus a dedicated regression test per bug found — full suite passes with `-race`, `go vet` clean, verified against all three real production scopes at every commit boundary.

Scalar functions: ~200 entry points implemented (was ~183). Aggregation functions: covariance family added (4 new). New dependency: `gopkg.in/yaml.v3` (BSD/MIT-style, standard Go YAML library), used only by `pkg/engine/uaparser` to load the embedded `regexes.yaml` pattern database once at startup.

### ✅ Sprint 16: regex capability audit — @"..." literals, typeof(), extract/extract_all/replace_regex fixes, full parse-operator kind=regex rewrite — COMPLETE (2026-08-18)

Prompted by a direct question — "replace_regex(), parse kind=regex, extract(regex, captureGroup, columnName) are all native regex capabilities in kql. do we support them currently?" — that turned into a real, substantial audit once checked against actual behavior rather than assumed from having case blocks present.

1. ✅ **`@"..."`/`@'...'` verbatim string literals** (`expr.go`) — a real, total gap: previously "unexpected character: @", with no working syntax for it at all. This was the actual blocker for writing ANY of real ADX's own documented regex examples verbatim in this engine — `extract`, `extract_all`, `replace_regex`, and `parse kind=regex` all use `@"..."` throughout their own official docs specifically to avoid double-backslash-escaping a pattern.
2. ✅ **`typeof(long)`** (`expr.go`) — also a total gap, added to support `extract()`'s optional 4th `typeLiteral` argument.
3. ✅ **`extract()`**: added the missing optional 4th `typeLiteral` argument (`typeof(int)` etc, via `types.ParseValue`), and fixed a real bug where no-match/out-of-range-group returned `""` instead of the documented `null`.
4. ✅ **`extract_all()`**: rewritten — was returning the whole regex match instead of the capture group's own content, and the optional `captureGroups` selector argument (numeric or named, single value for 1-D or multiple for 2-D output) was entirely unsupported. Verified against all 4 of real ADX's own worked examples on a GUID test string, including the trickiest one (a selector mixing named and numeric group references).
5. ✅ **`replace_regex()`**: fixed a real, silent-wrong-output bug — `rewrite_pattern` was passed straight to Go's own `$1`-style backreference syntax, but real ADX's own documented and worked-example syntax is `\0`/`\1`/`\2`. A query copy-pasted straight from Microsoft's own docs previously produced the literal, unsubstituted text `"was: \1"` instead of `"was: 1"`, with no error at all.
6. ✅ **`parse`/`parse-where` `kind=regex` completely rewritten** — it previously implemented a wrong syntax model entirely (a single hand-written regex with NAMED capture groups, not real KQL syntax at all). Real ADX documents `kind=regex` as using the exact same fragment pattern syntax as `kind=simple`, just interpreting each literal fragment as a regex snippet rather than literal text, stitched into one combined regex. Rewritten and verified against real ADX's own "Regex mode" and "Regex mode with regex flags" worked examples (including confirming, against a fully worked example rather than an ambiguous abbreviated doc snippet elsewhere on the same page, that the real default is GREEDY matching, not non-greedy) — `flags=regexFlags` (`U`/`i`/`s`/`m`) now supported via Go's RE2 inline-flag syntax.
7. ✅ **Six real, pre-existing bugs found and fixed** in the parse operator along the way — five present since this repository's very first commit, one introduced-then-caught within this same rewrite before ever being committed:
   - The field-name scanner never stopped at `:`, so `col:type` syntax has never actually worked — it produced a column literally named `"col:"` plus a spurious extra column named after the type.
   - A `*` wildcard's own skipped text was mistakenly captured as a real field's value, shifting every subsequent field-to-column assignment out of alignment.
   - A field followed directly by another field fragment (the common `field: type * "next literal"` idiom real ADX's own examples use throughout) lost that field's value entirely.
   - The quoted-literal scanner never processed backslash escapes at all — real ADX's own `"\\)"`-style escaped literals passed through as raw double backslashes, corrupting the generated regex.
   - `kind=regex`'s capture-group assignment was purely positional, breaking the moment any literal fragment contained its own regex group (which real ADX's own examples do) — fixed via named capture groups.
   - Column type annotations (`col:long`, `col:date`) were parsed and then silently discarded entirely for every kind — every extracted field became a plain string column regardless of declared type. Fixed via `types.ParseValue`.
8. ✅ A genuine design tension was found and resolved, not just a bug: non-greedy captures resolve to empty whenever nothing follows them (the pattern's true tail), while greedy captures over-match whenever the boundary literal is short/common and appears more than once. Resolved via non-greedy-except-the-final-fragment. A further structural necessity: numeric (`long`/`int`/`real`) fields need a type-appropriate character class in both `simple`/`relaxed` and `regex` modes — no purely generic-capture design can disambiguate `"27"` from `"27, "` before a wildcard, traced by hand through RE2's own backtracking order before concluding it.
9. **RESOLVED 2026-08-18 (Sprint 17), see that section below** — at the time of Sprint 16 this was an out-of-scope limitation: real ADX's own "Parse and extend results" worked example declares `releaseTime`/`previousLockTime` as `:date` using `MM/DD/YYYY`-format data, which this engine's datetime parser didn't support.
10. ✅ New tests across three files (`pkg/parser/verbatim_string_test.go`, `pkg/engine/func_regex_functions_test.go`, `pkg/engine/parse_operator_rewrite_test.go`) covering every worked example above plus a dedicated regression test per bug found. Full suite passes with `-race`, `go vet` clean, zero regressions across the entire pre-existing suite (two pre-existing tests updated because they exercised the OLD, incorrect `kind=regex` syntax), verified against all three production scopes.

### ✅ Sprint 17: todatetime() MM/DD/YYYY support — COMPLETE (2026-08-18)

Closes Sprint 16 item 9 and `kql_coverage.md`'s own former "Not Yet Implemented" item 7 — the top-priority carryover from that session.

1. ✅ **`types.ParseValue(..., TypeDatetime)` now accepts M/d/yyyy (US-culture short-date) forms.** Real ADX's own documented "Supported formats" table (`scalar-data-types/datetime.md`) lists only ISO 8601 / RFC 822 / RFC 850 / Sortable — none numeric month-first — but cross-checking against real ADX's own "Parse and extend results" worked example (`parse-operator.md`/`parse-where-operator.md`) shows source values like `lockTime=02/17/2016 08:40:01` DO convert into real, non-null datetime columns there; only the same example's deliberately-malformed sentinel values (`nonValidDateTime`, `invalid_datetime`) come back null. That means real ADX's actual parser is more permissive than its own published table (almost certainly .NET's culture-aware `DateTime.TryParse` under the hood) — verified against the worked example's own success/failure split, not assumed from general .NET familiarity. Added three new layouts (`1/2/2006 15:04:05`, `1/2/2006 15:04`, `1/2/2006`) to the existing layout-list loop in `pkg/types/types.go`; Go's `1`/`2` verbs accept both zero-padded and unpadded month/day, confirmed directly, so one layout per time-component variant covers both paddings. Month-before-day only (US convention) — the worked example's own `02/17` (day 17) is unambiguous evidence for this ordering.
2. ✅ **New tests** — `pkg/types/datetime_mmddyyyy_test.go`: 7 positive cases (padded/unpadded month and day, date-only, no-seconds, the real worked-example's own `previousLockTime` value), 4 negative cases confirming the new layouts don't over-match genuinely invalid input (including real ADX's own `nonValidDateTime`/`invalid_datetime` sentinels), and a regression guard that every pre-existing ISO format is unaffected.
3. ✅ **`pkg/engine/parse_operator_rewrite_test.go`'s `TestParseSimpleWorkedExample` updated** to actually assert `releaseTime`/`previousLockTime` as real converted `:date` values (via `types.FormatValue`), closing out the test's own previous comment explaining why those columns were left unchecked.
4. Full suite passes with `-race`, `go vet` clean, zero regressions. Verified against all three production scopes (dumuzi Findings=70, nergal Findings=154, girsu-paper `LatestTasks()`=100).

### ✅ Sprint 18: indexof_regex — COMPLETE (2026-08-18)

Closes the first item of `scalar_function_backlog.md`'s "Parse functions" section — the regex foundation audited and fixed in Sprint 16 is confirmed solid, not a fresh unknown.

1. ✅ **`indexof_regex(string, match [, start [, length [, occurrence]]])`** implemented in `func_string.go`, verified exactly against real ADX's own worked example (`indexof-regex-function.md`), all 5 cases, exact output values: `0`, `3`, `-1`, `-1`, `null`.
2. **A genuine discrepancy found between two sections of the SAME doc page** while verifying: the Parameters table's own prose for `start` describes a negative value as offsetting the search start from the end of the string (reads like boilerplate copied from `indexof()`'s own doc), but the Returns section states plainly "returns null if start is less than 0" — and the worked example's own `idx5` case (`start=-1`) confirms a null result, not an end-relative offset. Implemented per the worked example (ground truth) over the ambiguous/self-contradicting prose. `occurrence < 0` and `length < -1` also return null per the same Returns section, though neither is exercised by the worked example's 5 cases — taken as stated since nothing in the docs contradicts them.
3. ✅ Matches are non-overlapping (explicitly documented) via Go's `regexp.FindAllStringIndex`, the same RE2 engine already used throughout this codebase for `extract()`, `extract_all()`, `replace_regex()`, and the `parse` operator — no new regex semantics introduced.
4. ✅ New tests — `pkg/engine/func_indexof_regex_test.go`: the full worked example end to end in one query, a no-match case, null-propagation for both source and match arguments (mirroring `parse_ipv6`/`parse_command_line`'s own convention), the two undocumented-by-example-but-stated null conditions (negative occurrence, length < -1), and an occurrence-beyond-match-count case guarding against an out-of-range panic.
5. ✅ `inferExprType` (`eval.go`) updated to return `TypeLong` for `indexof_regex`, alongside the pre-existing `indexof` entry — the same easy-to-miss step this project's own conventions call out explicitly.
6. Full suite passes with `-race`, `go vet` clean, zero regressions. Verified against all three production scopes (dumuzi Findings=70, nergal Findings=154, girsu-paper `LatestTasks()`=100).

### ✅ Sprint 19: extract_json / extractjson — COMPLETE; parse_version BLOCKED, not attempted (2026-08-18)

1. ✅ **`extract_json(jsonPath, dataSource [, typeLiteral])` / `extractjson()` alias** implemented as a real, general JSONPath walker (`evalJSONPath`, `func_dynamic.go`), supporting exactly real ADX's own documented subset (`jsonpath.md`): `$` root, `.prop` dot access, `['prop']`/`["prop"]` bracket access (for property names with special characters), `[n]` 0-based array index. This turned out to be a meaningfully different capability from a `parse_json` + dot-access shortcut, not just a naming variant — KQL's own dot-access syntax on a `dynamic` value can't express bracket or array-index paths at all, so `extract_json` is the only way to reach e.g. `Details['Response Code']` or `Tags[0]` in one call.
2. ✅ Verified against BOTH real worked examples: `extract-json-function.md`'s own simple case (`extract_json("$.name", json, typeof(string))` on `{"name":"John",...}` → `"John"`), and the full `jsonpath.md` doc page's own 11-path example against its own shown JSON document, all 11 paths checked individually.
3. **A second real intra-page doc discrepancy found** (after Sprint 18's `indexof_regex` one, same session): `jsonpath.md`'s own last listed example path, `"$.Tags[0]"`, does NOT actually resolve against the SAME page's own shown JSON document — `Tags` is nested under `Details` there, not root-level. This reads like a copy-paste typo (a missing `Details.` prefix) rather than a genuinely different resolution rule, since every one of the other 10 example paths matches the document's actual structure exactly. Confirmed directly: the doc's own literal path returns null against that document (correct per the JSONPath grammar the same page documents), while the corrected path `"$.Details.Tags[0]"` resolves to `"startup"`, consistent with the other 10 examples' own pattern. Both are asserted in the test suite (`TestExtractJsonDocPageOwnPathIsATypo`, `TestExtractJsonJSONPathSubsetWorkedExample`) rather than silently working around the doc's own mistake without recording it.
4. ✅ New, general JSONPath walker deliberately NOT built on `bag_remove_keys`'s existing `removeNestedKey` helper — that one only supports dotted `"$.a.b"` paths (real ADX's own `bag_remove_keys` docs explicitly state array indexing isn't supported there) and is a delete-in-place mutator, not a value-returning reader; reusing it would mean bolting bracket/index support onto code whose own doc comment specifically disclaims it, and risking a regression in a working, independently-tested function for no real benefit.
5. ✅ No-typeLiteral form returns the extracted value's natural string form (string leaf unquoted, number/bool via plain textual form, object/array re-marshaled to compact JSON text so a further `extract_json`/`parse_json` call still works) — no worked example exercises this exact case with a non-string leaf, so this was inferred from the Parameters table (`type` explicitly optional) plus this codebase's own established `extract()` convention, not from a specific worked-example value.
6. ✅ New tests — `pkg/engine/func_extract_json_test.go`: both real worked examples end to end, the doc-typo case (both the literal and corrected path), no-match, invalid-JSON dataSource, null propagation for both arguments, object-leaf re-marshaling, `extractjson()` alias equivalence, and typeLiteral conversion (reusing `extract()`'s own established `types.ParseValue` pattern).
7. ✅ `inferExprType` (`eval.go`) updated to return `TypeString` for `extract_json`/`extractjson`, grouped with the pre-existing `extract` entry (same approximation: the optional typeLiteral arg isn't reflected in static type inference, matching how `extract` itself is already handled).
8. **`parse_version` investigated, found genuinely BLOCKED — not a small addition despite its place in the original priority list.** Fetched `parse-version-function.md`, reverse-derived the exact formula (`result = part1×10²⁴ + part2×10¹⁶ + part3×10⁸ + part4`) and confirmed it against all 7 rows of the doc's own worked-example table. The blocker: even `parse_version("1")` (the simplest possible real-world case) evaluates to 10²⁴, roughly 5 orders of magnitude past `int64`'s own maximum (~9.2×10¹⁸) — there is no real version string this function could return without overflow using this engine's existing numeric types. Real ADX returns `decimal` (128-bit); this codebase has no decimal or big-integer type anywhere (confirmed by direct source search of `pkg/types` — no `TypeDecimal`, no `math/big` usage). A `float64` implementation was considered and rejected as a silent-wrong-answer risk: at 10²⁴ magnitude, `float64`'s ~15-17 significant digits can't distinguish different `part4` values once `part1 >= 1`. A fixed-width zero-padded string encoding was also considered and rejected: the doc's own second worked example depends on real numeric `<` comparison against the result, which a string encoding can't provide without new, special-cased comparison-operator logic. See `scalar_function_backlog.md`'s own `parse_version` scope note for the full reasoning — this is scoped as its own future unit of work (a real `TypeDecimal` addition to `pkg/types`, threaded through the same surfaces `TypeLong`/`TypeReal` already cover), not folded into this session's remaining time.
9. Full suite passes with `-race`, `go vet` clean, zero regressions. Verified against all three production scopes (dumuzi Findings=70, nergal Findings=154, girsu-paper `LatestTasks()`=100).

### ✅ Sprint 20: inferExprType let-bound scalar type resolution — COMPLETE (2026-08-18)

Closes the last item of the priority list: the `inferExprType` gap found (not fixed) during the Sprint 15-adjacent LetContext lexical-scoping fix, commit `bed111f`.

1. ✅ **`LetContext` gained a `ScalarTypes map[string]types.KQLType` field** (`engine.go`), populated alongside the pre-existing `Scalars` map at scalar-let-bind time (`executeCompound`'s own `ScalarExpr` case), via `inferExprType(rewritten, &types.Schema{})` — the SAME expression-based type inference `applyPrint`/`applyExtend`/`applyProject` already use for every other column. Deliberately NOT derived from the let binding's raw runtime `types.Value` — that would reintroduce the exact ambiguity the original deferral was about: an `int64` value alone can't tell `long` from `datetime` from `timespan` (all three share that internal representation), the same reasoning already documented for make-series's own step-unit conversion (Sprint 8) and print's own datetime-typing fix.
2. ✅ **New `LookupScalarType()` method** on `LetContext`, mirroring the pre-existing `LookupScalar`'s parent-chain fallback — an outer scope's scalar type stays resolvable from a nested compound tabular argument, composing correctly with Sprint-adjacent commit `bed111f`'s own parent-chain scoping fix rather than only working one level deep.
3. ✅ **`inferExprType`'s `ColumnRef` case** (`eval.go`) now falls back to `activeLetContext.LookupScalarType(e.Name)` when a name isn't a real schema column, before defaulting to `TypeString` — this is the actual fix site; everything else exists to feed it the right answer.
4. ✅ All three `LetContext` construction sites (`executeCompound`, `applyInvoke`, `applyAs`) updated to initialize the new map alongside the pre-existing ones, for consistency even though only `executeCompound` currently writes to it.
5. ✅ New tests — `pkg/engine/inferexprtype_letbound_scalar_test.go`: the exact repro from `bed111f`'s own commit message (a declared `T:(v:long)` parameter now accepts `print v = <scalar let>`), a genuinely-mismatched-type case that must still be rejected (proving this isn't a blanket permissiveness change), a datetime-vs-long case specifically proving the fix reads the ORIGINAL expression's type rather than the ambiguous raw value, and a nested-let case proving composition with the pre-existing parent-chain lookup.
6. ✅ `letcontext_scoping_test.go`'s own file comment (which had explicitly documented this as a known, deliberately-unfixed limitation) updated to point at this fix instead of describing it as open — its own tests deliberately left using `(*)` any-schema parameters unchanged, since the fix doesn't change what those specific tests need to check (parent-chain scoping rules, not type resolution).
7. Full suite passes with `-race`, `go vet` clean, zero regressions — notably including every pre-existing test despite this touching `LetContext`, one of the most central, widely-read pieces of engine state in the codebase (the same caution `bed111f`'s own commit message called out). Verified against all three production scopes (dumuzi Findings=70, nergal Findings=154, girsu-paper `LatestTasks()`=100).

### ✅ Sprint 21: parse_xml — COMPLETE (2026-08-18)

Closes the last remaining item from this session's original Parse-functions priority list (`parse_version` excluded — see Sprint 19's own item 8, genuinely blocked on a missing decimal type, not something this sprint revisits).

1. ✅ **`parse_xml(xml)`** implemented in a new dedicated file, `pkg/engine/func_xml.go` (`evalParseXML`), dispatched from `func_convert.go` alongside `parse_json`/`todynamic` for consistency with how the docs describe the two functions identically ("Interprets a string as ... value, ... returns dynamic"). Follows real ADX's own fully-documented 7-row XML-to-JSON conversion table exactly (`parse-xml-function.md`): `<e/>`→`{"e":null}`, `<e>text</e>`→`{"e":"text"}`, `<e name="value" />`→`{"e":{"@name":"value"}}`, `<e name="value">text</e>`→`{"e":{"@name":"value","#text":"text"}}`, distinct children→nested object, repeated same-named children→array collapse, mixed text+children→both an `"a"` key and a `"#text"` key. Every row verified individually, plus the doc's own full "duration" worked example end to end (XML declaration prolog correctly skipped without erroring, 7 numeric-looking leaf children, retrieved via both `d.duration.value` dot-access and `d["duration"]["min"]` bracket-access exactly as the doc's own example does, checked against its exact stated values 118.0/100.0).
2. **One deliberate, clearly-flagged inference, not a directly-stated rule**: the conversion table itself shows every leaf value as a bare string, but the SAME doc page's own worked example feeds numeric-looking element text (`"118.0"`, `"5.0"`, etc.) and shows the resulting JSON holding genuine numeric values, not quoted strings — so element-own leaf text gets number coercion (`coerceXMLLeafText`: int if it parses as one, else float, else left as a string) wherever it appears (bare leaf value, or under a `"#text"` key). Deliberately NOT applied to attribute values — no worked example demonstrates attribute-value coercion in either direction, so none is assumed there.
3. ✅ Real ADX's own documented 1 MB (1,048,576 byte) input-length cap and "or null, if invalid" contract both implemented exactly (`maxXMLInputBytes` constant matches the documented figure verbatim, not rounded or approximated) — an oversized or malformed input returns null, not an error, matching this codebase's established convention for every other parse/extract function (`extract_json`, `parse_json`'s own tolerant fallback).
4. ✅ Everything besides element nodes, attributes, and text nodes is silently skipped (comments, processing instructions, the XML declaration itself) — implemented by simply having no case for those token types in the recursive walker's own switch, rather than an explicit skip-list, matching real ADX's own stated "everything else will be skipped" rule structurally rather than as a special-cased exception.
5. ✅ New tests — `pkg/engine/func_parse_xml_test.go`: one dedicated test per conversion-table row (7 total), a 3-element array-collapse case extending the table's own 2-element example, the full duration worked example end to end (checked by numeric equality via `types.ToFloat64`, not exact Go type or textual form — JSON itself has no int/float distinction, so asserting a specific Go type here would test this engine's own incidental number-decoding preference rather than real ADX's documented contract), invalid-XML-returns-null, null propagation, a no-attributes/no-text nested-structure case beyond the table's own 7 rows, and the 1 MB cap enforced exactly at the documented byte boundary (verified directly against `evalParseXML`, not just through the CLI, to pin down the exact boundary behavior).
6. Full suite passes with `-race`, `go vet` clean, zero regressions. Verified against all three production scopes (dumuzi Findings=70, nergal Findings=154, girsu-paper `LatestTasks()`=100).

### ✅ Sprint 22: array function family — array_rotate_left/right, array_split, array_sum, array_iff/array_iif — COMPLETE (2026-08-18)

Closes the "Array functions" item from `scalar_function_backlog.md`'s Medium-priority tier — five small, contained additions to the existing `array_*` family, each verified against real ADX's own docs before implementing.

1. ✅ **`array_rotate_left`/`array_rotate_right`** (`func_dynamic.go`): rotates array elements by `rotate_count`, negative reaching the opposite direction — the identical documented mechanism already used for `array_shift_left`/`array_shift_right`. Implemented via a single modular-arithmetic rotation shared by both names (rotate_right calls the same logic with `rotate_count` negated). All FOUR combinations verified individually against real ADX's own two separate worked-example pages, not just one direction assumed symmetric from the other: `array_rotate_left([1,2,3,4,5], 2)`→`[3,4,5,1,2]`, `array_rotate_left([1,2,3,4,5], -2)`→`[4,5,1,2,3]`, `array_rotate_right([1,2,3,4,5], 2)`→`[4,5,1,2,3]`, `array_rotate_right([1,2,3,4,5], -2)`→`[3,4,5,1,2]`.
2. ✅ **`array_split(array, index)`** (`func_dynamic.go`): splits at one or more indices into N+1 sub-arrays. `index` accepts EITHER a scalar int OR a dynamic array of ints — both forms verified against their own separate real worked examples (`array_split([1,2,3,4,5], 2)`→`[[1,2],[3,4,5]]`; `array_split([1,2,3,4,5], dynamic([1,3]))`→`[[1],[2,3],[4,5]]`). Real ADX's own documented (but not worked-example-exercised) negative-index rule — "negative values are converted to `array_length` + value" — implemented and separately tested (`array_split([1,2,3,4,5], -1)`→`[[1,2,3,4],[5]]`).
3. ✅ **`array_sum(array)`** (`func_dynamic.go`): sum of numeric elements as a double, verified against its own worked example (`array_sum([1,2,3,4])`→`10`). Real ADX's own documented "if the array contains elements of non-numeric types, the result is null" rule implemented and tested. One inference flagged, not directly worked-example-verified: "non-numeric" is read as excluding JSON booleans too (a bool is a distinct JSON type from a number, not an implicit 0/1), the most direct reading of the docs' own phrasing against JSON's own type system — no worked example tests a boolean array element either way.
4. ✅ **`array_iff`/`array_iif`** (`func_dynamic.go`, documented as equivalent aliases, both routed to the same case): element-wise conditional selection, verified against ALL FOUR of real ADX's own worked examples with exact values — all-array arguments (`[1,5,3]`), a numeric condition array with SCALAR (not array) `when_true`/`when_false` branches broadcasting to every position (`["yes","no","yes"]`), non-numeric/non-boolean condition values (string, datetime-as-string, null) each producing null in that result slot rather than an error (`[null,null,null]`), and mismatched array lengths where a missing `when_true`/`when_false` entry becomes null (`[1,2,null]`) — every branch of the documented behavior exercised by a distinct real example, not inferred from partial coverage.
5. ✅ `inferExprType` (`eval.go`) updated: `array_sum`→`TypeReal` (grouped with `todouble`/`toreal`), `array_rotate_left`/`array_rotate_right`/`array_split`/`array_iff`/`array_iif`→`TypeDynamic` (grouped with the existing `array_*` family).
6. ✅ New tests — `pkg/engine/func_array_family_test.go`: 15 tests total, one per worked-example value plus dedicated tests for the two documented-but-not-worked-example-exercised rules (negative split index, non-numeric array_sum), an empty-array rotation guard (division/modulo-by-zero safety), and an explicit `array_iif` alias-equivalence check.
7. Full suite passes with `-race`, `go vet` clean, zero regressions. Verified against all three production scopes (dumuzi Findings=70, nergal Findings=154, girsu-paper `LatestTasks()`=100).

### ✅ Sprint 23: bag/dynamic function family — bag_zip, bag_set_key, bag_pack_columns, dynamic_to_json, set_has_element — COMPLETE; column_names_of/column_ifexists deliberately deferred (2026-08-18)

Closes the "Bag/dynamic functions" item from `scalar_function_backlog.md`'s Medium-priority tier — five real, verified additions plus two deliberately-scoped-out items, not silently dropped.

1. ✅ **`bag_zip(KeysArray, ValuesArray)`** (`func_dynamic.go`): verified against ALL SIX of real ADX's own worked examples with exact values — equal-length zip, more-keys-than-values (missing → null), more-values-than-keys (extras ignored), a non-string key EXCLUDED from the result with its paired value discarded positionally (not reassigned to the next valid key — `keys=['a',8,'b'], values=[1,'2',2.5]` → `{'a':1,'b':2.5}`, not `{'a':1,'b':'2'}`), `ValuesArray` not actually an array (every value null, keys still enumerated), and `KeysArray` not actually an array (the WHOLE result is null, not an empty bag — confirmed via the docs' own `isnull()` assertion).
2. ✅ **`bag_set_key(bag, key, value)`** (`func_dynamic.go`): verified against both of its own worked examples — root-level set/overwrite, and JSONPath-nested set with AUTO-VIVIFICATION of a missing intermediate object (the docs' own second worked example's second row has no `key2` at all, yet the documented result creates `{"key2": {"prop1": "abc"}}`) — implemented via a new `setNestedKey` helper, deliberately NOT sharing `bag_remove_keys`'s existing `removeNestedKey` (that one is a correct no-op on a missing path; this one needs the opposite behavior). Real ADX's own "if the input bag isn't a property-bag, null is returned" contract also implemented and tested.
3. ✅ **`bag_pack_columns(column1, column2, ...)`** (`func_dynamic.go`): verified against its own worked example. Architecturally different from every other function added this session — the property NAME is the argument's own column identifier, not a value, so this reads `fc.Args[i]`'s underlying `*parser.ColumnRef.Name` directly rather than following the usual evaluate-then-use pattern. An argument that isn't a bare column reference (no name to use as a key) is a clear error, not a silent guess.
4. ✅ **`dynamic_to_json(expr)`** (`func_dynamic.go`): verified against its own worked example, which proves property-bag keys are sorted ALPHABETICALLY in the canonical output by constructing the same bag with two different insertion orders and asserting the two canonical strings compare equal (`AreEqual=true`). Implemented via a re-marshal through Go's own `json.Marshal`, which already sorts `map[string]interface{}` keys alphabetically by default — confirmed directly to be exactly real ADX's own documented ordering rule, not assumed from "maps often print sorted." A non-dynamic scalar input (fails to parse as JSON) falls back to the same conversion `tostring()` already implements, per the docs' own first rule.
5. ✅ **`set_has_element(set, value)`** (`func_dynamic.go`): real ADX's own docs explicitly cross-reference this as the same underlying check as `array_index_of(arr, value) != -1` ("both functions are equally performant") — implemented by reusing the EXACT same `fmt.Sprintf`-based element-equality comparison `array_index_of` already uses, rather than a second, separately-written equality check that could silently drift from it over time.
6. **`column_names_of`/`column_ifexists` deliberately NOT attempted, not silently dropped** — see `scalar_function_backlog.md`'s own scope note for the full reasoning: both need to inspect the row's own SCHEMA at evaluation time, not just operate on already-evaluated values the way every other scalar function does, which is a genuinely different design question (how a schema-aware function integrates with the general expression evaluator) than "add a case to an existing switch." Flagged as its own scoped future unit of work.
7. ✅ `inferExprType` (`eval.go`) updated: `bag_zip`/`bag_set_key`/`bag_pack_columns`→`TypeDynamic` (grouped with the existing bag/array family), `dynamic_to_json`→`TypeString`, `set_has_element`→`TypeBool` (grouped with `bag_has_key`).
8. ✅ New tests — `pkg/engine/func_bag_family_test.go`: 16 tests total, one per worked-example value (all 6 `bag_zip` cases, both `bag_set_key` cases plus its own non-bag-input null case, `bag_pack_columns`'s worked example plus a non-column-argument error case, `dynamic_to_json`'s canonical-ordering example plus a non-dynamic-scalar case, and three `set_has_element` cases covering found/not-found/string-match).
9. Full suite passes with `-race`, `go vet` clean, zero regressions. Verified against all three production scopes (dumuzi Findings=70, nergal Findings=154, girsu-paper `LatestTasks()`=100).

### ✅ Sprint 24: hash() / hash_xxhash64() — COMPLETE; hash_combine/hash_many BLOCKED, not guessed (2026-08-18)

Closes the "Hash functions" item from `scalar_function_backlog.md`'s Medium-priority tier — with a real correction to that list's own earlier assumption, and a genuine blocker documented rather than worked around with a guess.

1. **Correction to this project's own backlog note before implementing**: `hash()` is NOT "generic, algorithm-selectable" as `scalar_function_backlog.md` had assumed — real ADX's own docs state plainly that `hash()` "calculates hashes using the xxhash64 algorithm, but this may change." Caught by reading the actual docs before writing code, the same discipline this project applies throughout, not carried forward as assumed.
2. ✅ **`hash(source [, mod])` / `hash_xxhash64(source [, mod])`** implemented in `func_net.go` using a real xxhash64 implementation (`github.com/cespare/xxhash/v2`, a new, minimal dependency — resolved and confirmed to build in this sandbox via `go get` despite no explicit Go module proxy in the network allowlist; `github.com`/`codeload.github.com` alone were sufficient). Both names route to identical logic, matching what both currently document.
3. ✅ **Verified bit-for-bit against real ADX's own worked examples**, not just "produces a hash": `hash("World")` == `1846988464401551951`, `hash("World", 100)` == `51`, `hash(datetime("2015-01-01"))` == `1380966698541616202` — all three reproduced exactly.
4. **A genuinely non-obvious finding, independently derived and confirmed**: the datetime worked example does NOT match hashing any textual form of the datetime, nor this engine's own internal representation (nanoseconds since the Unix epoch) directly. It only matches the little-endian 8-byte encoding of the value converted to real ADX's own internal representation: .NET "ticks" (100-nanosecond intervals since `0001-01-01`, not `1970-01-01`). The conversion constant (`dotnetTicksAtUnixEpoch` = `621355968000000000`, the well-known .NET `DateTime.UnixEpoch.Ticks` value) was independently computed from first principles (days between the two epochs × ticks-per-day) and confirmed to reproduce the documented value exactly, not copied from a single unverified source.
5. **Scope of type coverage, honestly bounded**: `long`/`int`/`real`/`timespan` are hashed via the same little-endian-8-byte-of-the-underlying-value principle the confirmed datetime case demonstrates (`timespan` needs no epoch conversion, since this engine's own internal timespan ticks are already the same 100ns-tick unit real ADX itself uses) — this is a REASONABLE INFERENCE from the one confirmed non-string case, explicitly NOT claimed as independently worked-example-verified, since no worked example for those types was found. Every other type (`bool`, `guid`, `dynamic`) falls back to hashing the value's own string form, the safest defensible default absent any evidence — also not claimed to match real ADX bit-for-bit. `inferExprType(fc.Args[0], schema)` is used to determine which branch applies, since the raw runtime `types.Value` alone can't distinguish a `datetime` from a `long` (both are plain `int64` internally) — the same ambiguity Sprint 20 already solved generally for let-bound scalars, applied here directly to a function argument.
6. **`hash_combine`/`hash_many` deliberately NOT implemented — genuinely BLOCKED, not guessed**. `hash_combine`'s own real worked example gives exact values (`hash("Hello")=753694413698530628`, `hash("World")=1846988464401551951`, combined=`-1440138333540407281`), but the combine algorithm producing the third value from the first two isn't documented anywhere, and isn't in any public source: the `microsoft/Kusto-Query-Language` GitHub repo is the parser and semantic analyzer only, not the query execution engine. Five standard candidate combine algorithms were tried directly against the real worked example and NONE matched — a boost-style 64-bit golden-ratio combine (both argument orders), plain XOR, addition, an FNV-style multiply-and-add, and re-hashing the 16 concatenated bytes of the two inputs via xxhash64 again (both byte orders). Shipping a guessed algorithm that merely looks plausible would be a real silent-wrong-answer risk for anyone relying on these values for cross-system-comparable deduplication keys. `hash_many()` depends on the identical undocumented combine step per its own docs, so it's blocked for the same reason, not a separate investigation.
7. ✅ `inferExprType` (`eval.go`) updated: `hash`/`hash_xxhash64`→`TypeLong` (grouped with `indexof`/`countof` and the other long-returning functions).
8. ✅ New tests — `pkg/engine/func_hash_xxhash_test.go`: all three real worked-example values, the `hash("Hello")` value from `hash_combine`'s own worked example (independently verifying the input `hash()` values that example depends on, even though `hash_combine` itself isn't implemented), an alias-equivalence check between `hash`/`hash_xxhash64`, null propagation, an undocumented `mod<=0` edge case (falls back to the unmodded hash rather than dividing by zero or violating the documented `[0,mod-1]` range), and a determinism/no-collision consistency guard for the inferred (not bit-exact-verified) long/int handling.
9. Full suite passes with `-race`, `go vet` clean, zero regressions. Verified against all three production scopes (dumuzi Findings=70, nergal Findings=154, girsu-paper `LatestTasks()`=100).

### ✅ Sprint 25: toguid, tohex, gettype — COMPLETE; todecimal BLOCKED, three real inferExprType bugs found and fixed (2026-08-18)

Closes the "Type conversion" item from `scalar_function_backlog.md`'s Medium-priority tier — three real functions, one genuinely blocked item (same root cause as `parse_version`), and three real, pre-existing `inferExprType` bugs surfaced and fixed along the way, not silently worked around.

1. ✅ **`toguid(value)`** (`func_net.go`): verified against real ADX's own 3-row worked example exactly. The conversion strips hyphens WHEREVER they occur in the input (confirmed by hand-tracing the doc's own second row, whose hyphens sit in non-standard 12-4-4-12 positions yet still convert correctly — "properly located hyphens" in the prose does not mean position-validated), takes the first 32 resulting hex characters, and returns null on anything that isn't 32 valid hex characters.
2. ✅ **`tohex(value [, minLength])`** (`func_net.go`): verified against all 5 of real ADX's own worked-example values, including the load-bearing int-vs-long two's-complement width distinction for negative numbers (`tohex(toint(-256), 8) == "ffffff00"`, 32-bit width, vs `tohex(-256) == "ffffffffffffff00"`, 64-bit width) — resolved via `inferExprType(fc.Args[0], schema)`, not the runtime value alone (which can't distinguish a converted `int32` from an `int64`, both stored identically internally). A real bug was found and fixed in this case's OWN first draft before committing: the type-width floor (8 or 16 hex digits) was being applied unconditionally instead of only for negative values, which broke the simplest case, `tohex(256)` (no `minLength`), producing 16-digit-padded output instead of the correct unpadded `"100"`.
3. ✅ **`gettype(value)`** (`func_net.go`): verified against all 12 of real ADX's own documented expression/result pairs. Two branches: a static-type name (matching this engine's own pre-existing `types.KQLType.String()` naming exactly) for anything not statically `dynamic`, and a JSON-shape introspection (`dictionary`/`array`/`int`/`string`/`bool`/`null`/`real`/`long`) for anything that is. One inference beyond the directly-confirmed set, clearly flagged: a fractional dynamic number → `"real"` and a whole dynamic number outside `int32` range → `"long"` are the natural generalization from the one confirmed data point (`gettype(parse_json('1'))=="int"`), not independently worked-example-verified for those two specific shapes.
4. **Three real, pre-existing `inferExprType` bugs found and fixed while verifying `tohex`/`gettype` against real ADX's own worked examples — each one caught by getting a genuinely wrong answer from the live engine, not by inspection**:
   - `inferExprType` had NO case at all for `parse_json`/`todynamic` — every value they produce fell through to the generic `FuncCall` switch's own `TypeString` default. `gettype(parse_json(...))` reported `"string"` for every single dynamic-typed example in the doc's own table (int/dictionary/array all silently wrong) before this was found and fixed.
   - `inferExprType` had no case for `toguid()` (this same session's own earlier addition to this file) — the identical fallthrough bug, `gettype(toguid(...))` reporting `"string"` instead of `"guid"`.
   - `inferExprType` grouped `toint()` with `tolong()`, both returning `TypeLong` — erasing exactly the int/long distinction `tohex()`'s own documented behavior depends on, found while verifying `tohex(toint(-256), 8)` specifically.
5. **`todecimal` deliberately NOT implemented — genuinely BLOCKED, same root cause as `parse_version`**: real ADX's own docs confirm it returns a genuine `decimal` (128-bit) scalar, verified via its own worked example (`todecimal("123.45678") == decimal(123.45678)`). This engine has no decimal/big-integer type anywhere in `pkg/types`. Documented alongside `parse_version`'s own existing, more detailed scope note rather than duplicating the reasoning — revisit both together if a `TypeDecimal` is ever added.
6. ✅ `inferExprType` (`eval.go`) also updated with the two new functions' own types: `toguid`→`TypeGUID`, plus the three bug fixes above (`parse_json`/`todynamic`→`TypeDynamic`, `toguid`→`TypeGUID`, `toint`→`TypeInt`).
7. ✅ New tests — `pkg/engine/func_toguid_tohex_gettype_test.go`: `toguid`'s full 3-row worked example (including the non-standard-hyphen-placement row), all 5 `tohex` worked-example values as individual tests, all 7 static-branch `gettype` cases and all 5 dynamic-branch cases (including the empty-string-returns-null case, itself a small separate finding — `parse_json("")` needed distinguishing from the general malformed-non-empty-JSON-wraps-as-string case, both independently justified by `parse_json`'s own separate docs).
8. Full suite passes with `-race`, `go vet` clean, zero regressions (confirmed after all three `inferExprType` fixes, not just after the three new function additions). Verified against all three production scopes (dumuzi Findings=70, nergal Findings=154, girsu-paper `LatestTasks()`=100).

### ✅ Sprint 26: base64 array/guid + gzip/zlib compression family — COMPLETE (2026-08-18)

Closes the "Base64 / compression" item from `scalar_function_backlog.md`'s Medium-priority tier — eight functions, all verified against real ADX's own docs, including a genuine doc discrepancy found, cross-verified against a second independent worked example, and resolved.

1. ✅ **`base64_decode_toarray(base64_string)`** (`func_net.go`): verified exactly against real ADX's own worked example — `base64_decode_toarray("S3VzdG8=")` → `[75,117,115,116,111]` (the raw decoded bytes of "Kusto", each byte 0-255 as its own array element).
2. ✅ **`base64_encode_fromarray(byte_array)`**, the documented inverse: verified by round-tripping the same worked example back to `"S3VzdG8="` exactly.
3. ✅ **`base64_decode_toguid(base64_string)` / `base64_encode_fromguid(guid)`** (`func_net.go`): implemented via .NET's own internal Guid byte layout (Data1 4 bytes little-endian, Data2/Data3 2 bytes each little-endian, Data4 8 bytes as-is) — a genuinely non-obvious detail, not assumed from general familiarity with base64 or guids. **A real discrepancy was found and resolved**: `base64_decode_toguid`'s own doc page states its worked example produces a guid with `"754c"` as the third hex group, but decoding that exact base64 string through the standard mixed-endian algorithm produces `"4c75"` instead. Rather than guessing which was right, the SAME algorithm was independently checked against `base64_encode_fromguid`'s own separate worked example (`base64_encode_fromguid(toguid("...49ae..."))` == `"8jMxriJurkmwahbmqbIS6w=="`) and confirmed to reproduce that value exactly — and a direct round trip (`base64_decode_toguid(base64_encode_fromguid(toguid(...)))`) reproduces the original guid bit-for-bit. Since the two functions are documented as exact inverses of one another, and the algorithm is independently self-consistent against the second example, the standard algorithm is implemented (a two-character transposition typo is the most plausible explanation for the first page's own mismatch), not a one-off variant that would break the round trip.
4. ✅ **`gzip_compress_to_base64_string` / `gzip_decompress_from_base64_string`** (`func_net.go`, via Go's standard `compress/gzip`): verified by round trip (compressed byte output isn't stable across implementations, so a fixed expected string isn't the right verification target — standards compliance is the real documented contract). Real ADX's own documented "returns an empty result if decompression or decoding failed" contract — a genuinely different convention from most of this codebase's other decode functions, which return null — followed exactly, not defaulted to the more common pattern.
5. ✅ **`zlib_compress_to_base64_string` / `zlib_decompress_from_base64_string`** (`func_net.go`, via Go's standard `compress/zlib`, which always uses the window-size-15 real ADX documents as the only supported size): verified by round trip, same empty-string-on-failure contract as gzip. **Additionally verified for real cross-implementation interoperability**, not just internal consistency: a compressed output produced by this engine was independently decompressed successfully by Python's own `zlib` module outside the test suite, confirming genuine standards compliance rather than a self-consistent-but-nonstandard internal format.
6. ✅ `inferExprType` (`eval.go`) updated: `base64_encode_fromarray`/the four compress-decompress functions → `TypeString`, `base64_decode_toarray` → `TypeDynamic`, `base64_decode_toguid` → `TypeGUID`.
7. ✅ New tests — `pkg/engine/func_base64_compress_test.go`: the `base64_decode_toarray` worked example, the `base64_encode_fromarray` round trip, `base64_encode_fromguid`'s own directly-confirmed worked example (deliberately NOT asserting against `base64_decode_toguid`'s own suspected-typo literal value), a full guid round-trip test, both functions' own invalid-input handling (including `base64_encode_fromguid`'s own deliberate simplification — this engine returns null for an unparseable guid argument where real ADX's docs describe a hard runtime error, matching this codebase's established convention rather than adding per-argument static type-checking for one function alone), and both compression families' round trips plus their empty-string-on-failure contract.
8. Full suite passes with `-race`, `go vet` clean, zero regressions. Verified against all three production scopes (dumuzi Findings=70, nergal Findings=154, girsu-paper `LatestTasks()`=100).

### ✅ Sprint 27: String function family — repeat, regex_quote, replace_strings, unicode_codepoints_*, isascii, isutf8 — COMPLETE (2026-08-18)

Closes the "String functions" item from `scalar_function_backlog.md`'s Medium-priority tier — seven functions, including one (`replace_strings`) whose real algorithm had to be reverse-engineered from its own worked examples rather than assumed from the name.

1. **Correction to this project's own backlog note before implementing**: `repeat()` is NOT "just `strrep` under a different name" as the backlog had flagged for verification — it generates a dynamic ARRAY of a repeated VALUE (any of `bool`/`int`/`long`/`real`/`datetime`/`string`/`timespan`), unrelated to `strrep()`'s own string-repeated-with-delimiter behavior. Implemented in `func_dynamic.go` (not `func_string.go`) accordingly. Verified against real ADX's own worked example (`repeat(1, 3) == [1, 1, 1]`) plus both documented edge cases (`count == 0` → empty array, `count < 0` → null).
2. ✅ **`regex_quote(string)`** (`func_string.go`): verified exactly against real ADX's own worked example — matches Go's own `regexp.QuoteMeta` byte-for-byte, confirmed directly rather than assumed from the two functions sharing a similar purpose.
3. **`replace_strings(text, lookups, rewrites)`** (`func_string.go`) — the hard one this sprint. Its real algorithm is genuinely non-obvious and was reverse-engineered from all FOUR of real ADX's own worked examples, not assumed from the name or from `replace_string()`'s own simpler single-pattern behavior: lookups are applied as SEQUENTIAL, order-dependent passes over the text — each lookup's own non-overlapping matches are found and replaced (or left unchanged, for a non-string rewrite) in one full pass, and that matched span is then PROTECTED from every later lookup. This is what makes real ADX's own order-reversal worked example (identical lookups/rewrites, reversed array order, producing genuinely different output — `"This was..."` vs `"Thwas was..."`) come out correctly. A naive "try each lookup at each position, first match wins" single simultaneous scan was tried by hand first and does NOT reproduce both directions of that example — confirmed before choosing the sequential-passes-with-protection design actually implemented (`replaceStringsSequential`, a new helper using a `strSegment` protected/unprotected token list). Non-string lookup array elements are skipped entirely (never matched, per the docs' own stated rule); non-string rewrite elements still consume/protect the matched span but leave the original text unchanged there (verified against the docs' own dedicated "Nonstring replacement" example).
4. ✅ **`unicode_codepoints_from_string(value)` / `to_utf8(value)`** (`func_string.go`, `to_utf8` being real ADX's own documented deprecated alias, routed identically): verified against both of the doc's own worked examples, including non-ASCII symbol and mixed-script (Hebrew + Latin) text. Implemented via Go's own `[]rune` conversion, which already decodes UTF-8 into Unicode codepoints one rune at a time — exactly this function's own documented semantics, not approximated via raw bytes.
5. ✅ **`unicode_codepoints_to_string(array)`**, the documented inverse: verified by round trip against the doc's own third worked example.
6. ✅ **`isascii(value)`** (`func_string.go`): every byte in `[0x00, 0x7F]`, verified true/false against a plain-ASCII and a mixed-script string.
7. ✅ **`isutf8(value)`** (`func_string.go`): implemented via Go's own `utf8.ValidString`, verified against the doc's own worked example.
8. ✅ `inferExprType` (`eval.go`) updated: `regex_quote`/`replace_strings`/`unicode_codepoints_to_string` → `TypeString`, `unicode_codepoints_from_string`/`to_utf8`/`repeat` → `TypeDynamic`, `isascii`/`isutf8` → `TypeBool`.
9. ✅ New tests — `pkg/engine/func_string_misc_test.go`: 14 tests total, one per worked-example value across all seven functions, including a dedicated test for `replace_strings`'s own order-reversal case (the single most important behavior to guard, since it's the one a naive re-implementation would most likely get wrong) and its non-string-rewrite case.
10. Full suite passes with `-race`, `go vet` clean, zero regressions. Verified against all three production scopes (dumuzi Findings=70, nergal Findings=154, girsu-paper `LatestTasks()`=100).

### ✅ Sprint 28: datetime_part, datetime_local_to_utc/utc_to_local, unixtime_microseconds/nanoseconds_todatetime — COMPLETE; datetime_list_timezones deliberately deferred (2026-08-18)

Closes the "Datetime" item from `scalar_function_backlog.md`'s Medium-priority tier — five functions implemented, one deferred with real reasoning, plus a stale doc row corrected along the way.

1. ✅ **`datetime_part(part, datetime)` / `datepart` alias** (`func_datetime.go`): verified against ALL 12 parts of real ADX's own single worked example with exact values, in one query (`datetime(2017-10-30 01:02:03.7654321)` → year=2017, quarter=4, month=10, week_of_year=44, day=30, dayOfYear=303, hour=1, minute=2, second=3, millisecond=765, microsecond=765432, nanosecond=765432100). The backlog's own suggestion to check whether this could be "a thin wrapper" around existing component-extraction functions was followed through, not assumed: checked directly and found this engine has NO standalone `year()`/`month()`/etc. functions to wrap — only differently-named aliases (`getyear()`, `monthofyear()`, `hourofday()`) — so implemented directly against Go's own `time.Time` methods instead, reusing the same ISO-8601 `t.ISOWeek()` calculation the pre-existing `week_of_year`/`weekofyear` scalar function already uses for `week_of_year`, so the two can't silently drift apart.
2. **A stale, misleading row in this document's own String/Datetime function table was found and corrected while doing the above check**: the Datetime table's own `"year / month / day / hour / minute / second / millisecond / microsecond | Component extraction"` row implied these exist as standalone callable functions in this engine — confirmed directly that they do NOT (only the differently-named aliases above do). Corrected in place rather than left to mislead a future session the same way.
3. ✅ **`datetime_local_to_utc(from, timezone)` / `datetime_utc_to_local(from, timezone)`** (`func_datetime.go`, via Go's own `time.LoadLocation` against the real IANA timezone database): verified against all 3 rows of real ADX's own worked example (`datetime(2020-02-02 20:02:20)` local → UTC in `US/Pacific`/`America/Chicago`/`Europe/Paris`, all three matching exactly), the reverse direction's own separate worked example (`US/Pacific`/`Australia/Canberra`), AND real ADX's own dedicated DST-transition ambiguity worked example (`Europe/Brussels`, spring-forward gap producing the documented non-reversible round trip for two of four hourly steps) — all four rows of that table matched exactly, including the specific ambiguity it exists to demonstrate. Implemented via KQL's own documented "datetime is always UTC internally" convention: a "local" reading is represented by re-tagging its own Y/M/D/H/M/S/ns numbers as UTC, not by applying a naive fixed offset — confirmed this was the right model precisely because it's what reproduces the DST-ambiguity example correctly. `time/tzdata` added as a blank import to `cmd/gokql/main.go` to embed the IANA database directly into the binary, removing a dependency on the deployment environment having system `/usr/share/zoneinfo` present — a real portability improvement (this dev environment has system zoneinfo, so both worked either way here, but this closes a gap for container/minimal-OS deployments).
4. ✅ **`unixtime_microseconds_todatetime` / `unixtime_nanoseconds_todatetime`** (`func_datetime.go`): straightforward linear-scaling siblings of the pre-existing seconds/milliseconds functions; the nanoseconds variant is a direct pass-through (this engine's own internal representation already IS unix-epoch nanoseconds) rather than the float64-scale-then-convert pattern the coarser siblings use, avoiding an unnecessary precision-losing round trip at nanosecond scale. Real ADX's own documented "a datetime before the epoch has a negative timestamp value" contract verified as a real, intended case, not an error.
5. **`datetime_list_timezones()` deliberately NOT implemented — a reasoned deferral, not an oversight**: Go's standard library has no clean API to enumerate the full IANA timezone database (no `time.Zones()` or equivalent); the only portable options are parsing the raw `zone1970.tab`/`zone.tab` file from system `zoneinfo` (filesystem-dependent, and `time/tzdata`'s own embedded data doesn't include a separate enumerable index) or hard-coding a zone list (risky — would silently drift from the real IANA database over time as it's updated). There's also no worked example to verify output against, since real ADX's own version returns a large table, not a checkable single value. See `scalar_function_backlog.md`'s own scope note for the full reasoning.
6. ✅ `inferExprType` (`eval.go`) updated: `datetime_part`/`datepart` → `TypeLong`, `unixtime_microseconds_todatetime`/`unixtime_nanoseconds_todatetime`/`datetime_local_to_utc`/`datetime_utc_to_local` → `TypeDatetime`.
7. ✅ New tests — `pkg/engine/func_datetime_part_tz_test.go`: the full 12-part `datetime_part` worked example in one query, a `datepart` alias-equivalence check, both timezone functions' own worked examples, the full 4-row DST-ambiguity round-trip table, an invalid-timezone-name null case, and both new `unixtime_*` functions including the negative-pre-epoch case.
8. Full suite passes with `-race`, `go vet` clean, zero regressions. Verified against all three production scopes (dumuzi Findings=70, nergal Findings=154, girsu-paper `LatestTasks()`=100).

### ✅ Sprint 29: ingestion_time, jaccard_index, format_bytes — COMPLETE (2026-08-18)

Closes the `_TimeReceived` accessor item and the Misc item (minus `assert`, still open) from `scalar_function_backlog.md`'s Medium-priority tier — the last three items in that tier.

1. ✅ **`ingestion_time()`** (`func_datetime.go`): real ADX's own docs describe this as a function-form accessor for a HIDDEN `$IngestionTime` column, null "if there's no datetime column in the table, or the IngestionTime policy wasn't enabled." This engine's own `_TimeReceived` (`timereceived.go`) is a real, ordinary schema column internally, so this is a thin accessor reading it by name during expression evaluation. **A real discovery made while verifying this, not a bug**: `_TimeReceived` IS hidden from query OUTPUT unless referenced by name somewhere in the operator chain (`hideTimeReceivedUnlessExplicit`, `timereceived.go`) — confirmed live (a `datatable(...)` literal declaring a `_TimeReceived` column showed only 2 columns in the final result, not 3, until traced to this deliberate, already-correctly-implemented display rule). `timereceived.go`'s own header comment ("no special hidden ... mechanism was built") is now stale relative to that later addition — not fixed here, flagged for whoever next touches that file's own doc comment, since it wasn't this function's own responsibility to correct. `inferExprType` reads the real value before that later hiding step runs, so the returned VALUE matches real ADX's own null-if-absent contract exactly regardless of the display question.
2. ✅ **`jaccard_index(set1, set2)`** (`func_dynamic.go`): verified exactly against real ADX's own worked example (`jaccard_index([1,2,3], [1,2,3,4]) == 0.75`), reusing the same string-keyed dedup-set pattern `set_union`/`set_intersect`/`set_difference` already use, per the docs' own "duplicate values are ignored" rule. The undocumented both-sets-empty (0/0) case returns null rather than a guessed 0 or 1.
3. ✅ **`format_bytes(size [, precision [, units]])`** (`func_convert.go`): verified against ALL 5 of real ADX's own worked examples with exact string output (`format_bytes(564)` → `"564 Bytes"`, `format_bytes(10332, 1)` → `"10.1 KB"`, `format_bytes(20010332)` → `"19 MB"`, `format_bytes(20010332, 2)` → `"19.08 MB"`, `format_bytes(20010332, 0, "KB")` → `"19541 KB"`). Base-1024 scaling and unit auto-selection were derived directly from these numbers (`10332/1024 = 10.09...` rounds to `10.1`, confirming binary not decimal scaling), not assumed. An unrecognized explicit `units` argument returns null, matching this codebase's established convention for invalid input.
4. ✅ `inferExprType` (`eval.go`) updated: `ingestion_time` → `TypeDatetime`, `jaccard_index` → `TypeReal`, `format_bytes` → `TypeString`.
5. ✅ New tests — `pkg/engine/func_ingestion_time_test.go` (4 tests: positive/absent-column/null-row-value/argument-count cases) and `pkg/engine/func_jaccard_formatbytes_test.go` (7 tests: the worked examples for both functions plus identical-sets, disjoint-sets, and duplicates-ignored sanity cases for `jaccard_index`, and invalid-unit/null-propagation cases for `format_bytes`).
6. Full suite passes with `-race`, `go vet` clean, zero regressions. Verified against all three production scopes (dumuzi Findings=70, nergal Findings=154, girsu-paper `LatestTasks()`=100).

### Not Yet Implemented (notable gaps)

1. **hll / hll_if / hll_merge, tdigest / tdigest_merge** — sketch structures; low priority for a single-node engine
2. **buildschema, percentilesw / percentilesw_array** — niche aggregation gaps
3. **series_* Tiers 4-5** — 18 functions remain, all curve-fitting (`series_fit_line`, `series_fit_poly`, etc.) and signal-processing/anomaly-detection (`series_decompose`, `series_fft`, `series_outliers`, etc.) — already LOW priority per this project's own use cases; see `docs/timeseries_backlog.md` for the full list. No MED-priority time-series work remains.
4. **query parameters** — `declare query_parameters(...)`
5. **reduce** — see Sprint 12 item 5 and the "Out of Scope" section's own note above; revisit if the actual SLCT reference implementation (the `logparser` project's wrapped C source) or the full original paper text becomes directly readable.
~~6. `inferExprType` can't resolve a let-bound scalar's type for a declared tabular parameter~~ RESOLVED (Sprint 20, 2026-08-18) — see the Sprint 20 entry below for full detail.
~~7. `todatetime()`/`types.ParseValue(..., TypeDatetime)` doesn't support `MM/DD/YYYY` format~~ RESOLVED (Sprint 17, 2026-08-18) — see the Sprint 17 entry above for full detail.
8. **`parse_version()` — genuinely blocked on a missing decimal/big-integer numeric type**, not just unstarted. See Sprint 19's own item 8 above and `scalar_function_backlog.md`'s `parse_version` scope note for the full reasoning: even the simplest real version string (`"1"`) parses to 10²⁴, ~5 orders of magnitude past `int64`'s own maximum, and this codebase has no `TypeDecimal`/`math/big`-backed numeric type anywhere to hold it without either silent precision loss (`float64`) or breaking real ADX's own documented numeric-comparison worked example (a string encoding). Revisit as a dedicated `TypeDecimal` addition to `pkg/types`, not as a small function addition.

---

## Coverage Summary

| Category                | Implemented | KQL Total | Coverage | Notes                             |
| ---------------------------- | ----------- | --------- | -------- | -------------------------------------- |
| Tabular operators                | 41          | 53        | 77%      | 41 implemented + 0 not-yet-implemented + 12 out-of-scope = 53, a full, cross-checked enumeration (2026-08-14/15), not the earlier, incomplete 47 |
| Comparison operators                | 22          | 35        | 63%*     | Effectively complete in practice — 0 known gaps; the 35 denominator counts individual grammar symbols, this row counts table rows (see methodology note above) |
| Scalar functions                       | ~244        | 420       | 58%      | Row was stale at ~183 (last recomputed pre-Sprint 15); corrected 2026-08-18 — Sprint 15 brought it to ~200 (trig, IPv4/IPv6, parse_command_line, parse_user_agent), Sprints 18-19 added 3 more (indexof_regex, extract_json, extractjson), Sprint 21 added parse_xml, Sprint 22 added 6 more (array_rotate_left/right, array_split, array_sum, array_iff/array_iif), Sprint 23 added 5 more (bag_zip, bag_set_key, bag_pack_columns, dynamic_to_json, set_has_element), Sprint 24 added hash/hash_xxhash64, Sprint 25 added toguid/tohex/gettype, Sprint 26 added 8 more (base64 array/guid + gzip/zlib), Sprint 27 added 8 more (repeat, regex_quote, replace_strings, unicode_codepoints_*, isascii, isutf8), Sprint 28 added 5 more (datetime_part/datepart, datetime_local_to_utc/utc_to_local, unixtime_microseconds/nanoseconds_todatetime), Sprint 29 added 3 more (ingestion_time, jaccard_index, format_bytes). 300+ of the remaining gap is geo/series/obscure; series_* Tiers 1-3 (31 functions, minus series_stats) closed 2026-08-17 |
| Aggregation functions                     | 24          | 61        | 39%*     | All common + conditional variants covered — remaining gap is entirely the sketch/niche family (hll, tdigest, buildschema, weighted percentiles); denominator counts symbols, this row counts table rows |
| File formats                                 | 4           | —         | —        | CSV, JSON, NDJSON, Parquet (xlsx deliberately removed 2026-08-15, see File-Based Querying section) |
| **Practical coverage**                          | —           | —         | **~93%** | **Of security analyst queries, plus a real, persisted-function/materialized-view/federation layer beyond raw language coverage** |

The raw percentages understate practical coverage. The 47 tabular operators include ML and
distributed-system operators irrelevant to local analysis. The 420 scalar functions include
50+ geo and 50+ time-series functions. For security log analysis and knowledge-scope work —
the primary use cases — virtually all commonly-used operators and functions are implemented,
plus substantial capability beyond raw language parity: stored functions, materialized views,
federation, automatic ingest-time timestamping with retroactive schema evolution, and a host-
embedding extension point.

---

## Architecture Notes

**Design philosophy:** A focused KQL query engine for local columnar data analysis,
not a full ADX reimplementation. De-internalized (2026-08-13) from `internal/` to `pkg/`
specifically to support this: a host application (e.g. wizTainer) can now depend on this
engine as a genuine Go module via a local `replace` directive, the same pattern already used
for `vortex-go`, rather than forking it.

**File structure (`pkg/engine/`, top 20 by size):**

| File               | Lines | Responsibility                                        |
| --------------------- | ----- | ------------------------------------------------------------ |
| operators.go             | 1,402 | Tabular operator execution                                       |
| aggregation.go             | 1,071 | 34 aggregation functions                                            |
| eval.go                      | 969   | Expression evaluator, window functions, toscalar/in-subquery substitution |
| columnar_agg.go                 | 941   | Columnar fast-path aggregate execution                                    |
| graph.go                           | 793   | make-graph, graph-match, graph-to-table                                       |
| engine.go                             | 757   | Query dispatch + orchestration                                                 |
| func_dynamic.go                          | 687   | 24 dynamic/array/bag functions                                                    |
| mv_maintenance.go                            | 682   | Materialized view incremental maintenance                                          |
| func_datetime.go                                | 676   | 41 datetime function entry points                                                     |
| stored_functions.go                                | 631   | Stored (persisted) function resolution and binding                                       |
| compact.go                                            | 590   | Extent compaction                                                                            |
| func_string.go                                           | 582   | 27 string functions                                                                              |
| ingest.go                                                    | 550   | CSV ingestion + extent creation                                                                     |
| vortex_bridge.go                                                | 539   | Vortex columnar ↔ row conversion                                                                       |
| func_net.go                                                        | 512   | 18 network/encoding/hashing/vector functions                                                              |
| func_convert.go                                                        | 478   | 33 conversion/math/conditional entry points                                                                  |
| file_sources.go                                                            | 415   | CSV, JSON, NDJSON readers                                                                                        |
| storage.go                                                                    | 394   | Vortex file reading, per-file schema-evolution-aware column projection                                              |
| shareddict.go                                                                    | 388   | Database-wide shared string dictionary                                                                                 |
| columnar.go                                                                        | 383   | Columnar scan machinery                                                                                                   |

**Dependencies:**

| Dependency            | Purpose                     | Notes                                                          |
| ---------------------- | ------------------------------- | ------------------------------------------------------------------ |
| vortex-go (local)          | Columnar storage format             | Genuinely private/unpublished; local `replace` is necessary here      |
| parquet-go (public)           | Apache Parquet file reading            | Real, published version pinned directly; no local `replace` (fixed 2026-08-14). Apache 2.0 licensed — permissive, not copyleft; does not require this project's own MIT license to change |
| google/flatbuffers                  | Vortex serialization                          |                                                                                      |
| klauspost/compress                     | Compression (zstd, snappy)                       |                                                                                      |
| google/protobuf                           | Parquet metadata                                    |                                                                                      |
