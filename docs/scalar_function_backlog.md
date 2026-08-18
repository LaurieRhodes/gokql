# Scalar & Aggregation Function Backlog

A prioritized, categorized work queue for functions confirmed genuinely
missing against the real ADX language surface — built for a future
session to pick up directly, not just a raw name dump.

**Built:** 2026-08-14, cross-checked against a full real-ADX scalar and
aggregation function enumeration provided directly, verified with a
combination of source-grep and live CLI spot-checks (a few names in the
raw list turned out to be false positives — e.g. `not` is already
implemented, just via a different mechanism than the grep pattern used
to check the rest; each category below states how it was verified).

See `kql_coverage.md` for the full picture of what's already
implemented — this document only covers what isn't.

---

## How to use this list

Each item below is a real, confirmed gap — verified against the real
codebase, not assumed from the reference list alone. Before
implementing anything from this list, still follow the discipline this
project already established: verify the function's exact real-ADX
semantics (search + fetch the real docs page, don't guess from the
name), check for an existing helper to reuse rather than duplicate
logic, write it, build, run the full test suite, verify against real
production scopes, write tests checked against the real docs' own
worked examples, then commit.

Priority tiers are a starting judgment, not a mandate — reconsider
given what's actually needed at the time.

---

## 🟡 Medium priority — natural, low-risk extensions of families already built here

These sit directly alongside code that already exists and works; each
is a small, contained addition following an established pattern.

### Trigonometry — CLOSED 2026-08-17
`sin`, `cos`, `tan`, `asin`, `acos`, `atan`, `atan2`, `degrees`,
`radians` are all implemented in `func_convert.go`, verified against
real ADX worked examples, and share their underlying implementation
with the pre-existing `series_sin`/`series_cos`/etc family (which
previously called `math.Sin`/`math.Cos` directly with no scalar
counterpart) — see `kql_coverage.md` for full detail.

### Statistics — covariance — CLOSED 2026-08-17
`covariance`, `covarianceif`, `covariancep`, `covariancepif` are all
implemented in `aggregation.go`, verified against real ADX's own
worked example, with pairwise null exclusion and an auto-generated
output column name that (unlike `variance`) includes both argument
names — see `kql_coverage.md` for full detail.

### Array functions — CLOSED 2026-08-18
`array_sum`, `array_split`, `array_rotate_left`, `array_rotate_right`,
`array_iff`/`array_iif` are all implemented in `func_dynamic.go`,
verified against real ADX's own worked examples (every function had
at least one, most had 2-4 covering distinct behavior branches — see
`kql_coverage.md`'s Sprint 22 entry for full detail, including one
genuinely undocumented-by-example edge case decided by direct
inference: `array_sum`'s "non-numeric" exclusion is read as also
excluding JSON booleans, not just strings/objects, since the docs'
own phrasing is about JSON type, and no worked example tests a
boolean array element either way).

### Bag/dynamic functions (`func_dynamic.go`)
**`bag_zip`, `bag_set_key`, `bag_pack_columns`, `dynamic_to_json`,
`set_has_element` CLOSED 2026-08-18** — see `kql_coverage.md`'s
Sprint 23 entry for full detail, including `bag_zip`'s own 6-case
worked-example verification (equal-length, missing-value, extra-value,
non-string-key-exclusion, non-array-Values, non-array-Keys) and
`bag_set_key`'s own auto-vivification behavior for a missing
intermediate JSONPath segment. Still open, deliberately deferred as
a genuinely different category of work (schema-introspection, not an
ordinary scalar function — see the note below): `column_names_of`,
`column_ifexists`.

**`column_names_of`/`column_ifexists` scope note (2026-08-18)**: NOT
attempted this session, deliberately. Both need to inspect the row's
own SCHEMA (a list of column names) at the point of evaluation, not
just operate on already-evaluated values the way every other function
in this list does — `column_ifexists(name, default)` specifically
needs to check whether a named column exists in the current tabular
context and fall back to a literal default if not, which is closer in
kind to `inferExprType`'s own schema-dependent needs (or
`project-by-names`' own existing `column_names_of` integration inside
that operator specifically) than a typical value-in-value-out scalar
function. Building this properly means deciding how a schema-aware
function integrates with the general expression evaluator (which
currently receives a `*types.Schema` alongside the row, so the
data IS available — but no existing scalar function reads the schema
at all, only column VALUES), not just adding a `case` to an existing
switch. Revisit as its own scoped unit of work with that design
question settled first, rather than folding it in alongside the five
straightforward additions above.

### Hash functions (`func_net.go`, alongside `hash_md5`/`hash_sha1`/`hash_sha256`)
**`hash`/`hash_xxhash64` CLOSED 2026-08-18** — see `kql_coverage.md`'s
Sprint 24 entry for full detail, including a correction to this list's
own earlier assumption: `hash()` is NOT "generic, algorithm-
selectable" as originally noted here — real ADX's own docs state it
always uses xxhash64 internally (this was caught by reading the real
docs before implementing, not carried forward as assumed). Verified
bit-for-bit against real ADX's own worked examples, including a
non-obvious finding for the datetime case (see the Sprint 24 entry).

**`hash_combine`/`hash_many` scope note (2026-08-18, genuinely
BLOCKED, not just unstarted)**: `hash_combine`'s own real worked
example gives exact input/output values (`hash("Hello")=753694413698530628`,
`hash("World")=1846988464401551951`, `combined=hash_combine(h1,h2)=-1440138333540407281`),
but the combine algorithm producing that third value from the first
two isn't documented anywhere, and isn't in any public source either
— the `microsoft/Kusto-Query-Language` GitHub repo (the only public
Kusto source) is the parser and semantic analyzer only, not the query
execution engine that would contain this function's real
implementation. Five standard candidate combine algorithms were tried
directly against the real worked example and NONE matched: a
boost-style 64-bit golden-ratio combine (both argument orders), a
plain XOR, addition, an FNV-style multiply-and-add, and re-hashing the
16 concatenated bytes of the two inputs (both byte orders) via
xxhash64 again. Shipping a guessed combine algorithm that merely
"produces a plausible-looking int64" would be a real silent-wrong-
answer risk — anyone using `hash_combine`/`hash_many` for
cross-system-comparable deduplication keys would get values that
silently diverge from real ADX's own. `hash_many()`'s own docs state
it applies `hash()` to each argument and then combines the results —
the exact same undocumented combine step, so it's blocked for the
identical reason, not a separate investigation. Revisit only if the
actual combine algorithm becomes available from a source other than
guessing (e.g. a future public disclosure, or a way to query a real
ADX cluster directly to brute-force-fit candidate algorithms against
many more input/output pairs than the one worked example provides).

### Type conversion (`func_net.go`) — CLOSED 2026-08-18
**`toguid`, `tohex`, `gettype` CLOSED** — see `kql_coverage.md`'s
Sprint 25 entry for full detail, including three real, pre-existing
`inferExprType` bugs found and fixed while verifying `tohex`/`gettype`
against real ADX's own worked examples (missing cases for
`parse_json`/`todynamic` and `toguid`, and `toint` incorrectly grouped
with `tolong`). Implemented in `func_net.go`, not `func_convert.go` as
this list originally guessed — placed alongside the existing
`hash`/`new_guid` functions since `toguid`/`tohex`/`gettype` all
depend on the same `inferExprType`-driven static-type-resolution
pattern `hash()` established in Sprint 24, not because of file
organization alone.

**`todecimal` scope note (2026-08-18, genuinely BLOCKED, same root
cause as `parse_version`)**: real ADX's own docs confirm `todecimal()`
returns a genuine `decimal` scalar (128-bit), verified via its own
worked example (`todecimal("123.45678") == decimal(123.45678)`). This
engine has no decimal/big-integer type anywhere in `pkg/types` (the
same gap documented in detail under `parse_version`'s own scope note
above) — revisit both together if a `TypeDecimal` is ever added.

### Base64 / compression — CLOSED 2026-08-18
`base64_decode_toarray`, `base64_encode_fromarray`,
`base64_decode_toguid`, `base64_encode_fromguid`,
`gzip_compress_to_base64_string`, `gzip_decompress_from_base64_string`,
`zlib_compress_to_base64_string`, `zlib_decompress_from_base64_string`
are all implemented in `func_net.go`, verified against real ADX's own
worked examples (see `kql_coverage.md`'s Sprint 26 entry for full
detail) — including a genuine discrepancy found and resolved in
`base64_decode_toguid`'s own doc page (a two-character transposition
in its third guid hex group, cross-checked against
`base64_encode_fromguid`'s own separate, independently-verified worked
example and a direct round-trip test) and real ADX's documented
"returns empty string, not null, on failure" contract for the gzip/
zlib decompress functions, followed exactly rather than defaulted to
this codebase's more common null-on-failure convention.

### String functions — CLOSED 2026-08-18
**`repeat`, `regex_quote`, `replace_strings`, `unicode_codepoints_from_string`/`to_utf8`,
`unicode_codepoints_to_string`, `isascii`, `isutf8` all implemented**
— see `kql_coverage.md`'s Sprint 27 entry for full detail, including a
correction to this list's own earlier assumption: `repeat()` is NOT
"just `strrep` under a different name" — it generates a dynamic ARRAY
of repeated VALUES (any scalar type), unrelated to `strrep()`'s own
string-repetition-with-delimiter behavior. `replace_strings()`'s own
real algorithm (sequential, order-dependent passes with match
protection) was reverse-engineered from its own worked examples, not
assumed from the name — a naive single-scan priority algorithm does
NOT reproduce real ADX's own documented order-reversal example.

### Datetime (`func_datetime.go`, alongside the existing, large datetime family)
`datetime_part` (verify against the real docs — likely a single
function that dispatches to the same logic already backing
`year`/`month`/`day`/etc., worth checking whether it can be a thin
wrapper rather than new logic), `datetime_local_to_utc`,
`datetime_utc_to_local`, `datetime_list_timezones`,
`unixtime_microseconds_todatetime`, `unixtime_nanoseconds_todatetime`

### IPv4 / IPv6 — CLOSED 2026-08-17
`has_any_ipv4`, `has_any_ipv4_prefix`, `has_ipv4_prefix`,
`ipv4_is_in_any_range`, `ipv4_is_match`, `ipv4_range_to_cidr_list`,
`ipv4_netmask_suffix`, `format_ipv4_mask`, `parse_ipv4_mask`, plus the
full IPv6 family (`ipv6_compare`, `ipv6_is_in_range`,
`ipv6_is_in_any_range`, `ipv6_is_match`, `parse_ipv6`,
`parse_ipv6_mask`) are all implemented and verified against real ADX
worked examples — see `kql_coverage.md`'s own entry for full detail,
including 5 real bugs found and fixed in already-implemented IPv4
sibling functions (`has_ipv4`, `format_ipv4`, `ipv4_compare`,
`ipv4_is_private`, `ipv4_is_in_range`) plus one real bug found and
fixed in this session's own new IPv6 code before it was ever
committed (an embedded-prefix offset decision that inspected the
parsed byte value instead of the address's own source text).

### Parse functions (`func_string.go` / `func_net.go` / `func_xml.go`)
**`parse_command_line`, `parse_user_agent`, `indexof_regex`,
`extract_json`/`extractjson`, `parse_xml` CLOSED (2026-08-17/18)** —
see `kql_coverage.md` for full detail on all five, including
`parse_user_agent`'s own reversal note (the earlier deferral below was
based on the actual `ua-parser/uap-core` regex database being assumed
unfetchable; it turned out to be public and fetchable, which removed
the blocker), `indexof_regex`'s own note (a real discrepancy found
between two sections of its own doc page), `extract_json`'s own note
below (a second real doc discrepancy found, plus the "meaningful
difference from parse_json" question actually resolved: it's a real,
general JSONPath walker, not a `parse_json` + dot-access shortcut),
and `parse_xml`'s own note below (a full, directly-documented 7-row
conversion table plus one worked example — this makes it the
category's OWN item entirely unlike `parse_version`, which is
genuinely blocked). Every item on this session's original priority
list is now closed except `parse_version`, which remains genuinely
BLOCKED on a missing decimal/big-integer type — see its own scope note
below.

**`parse_xml`'s own scope note (2026-08-18)**: implemented in a new
dedicated file, `func_xml.go`, as a real XML-to-JSON walker
(`evalParseXML`) following real ADX's own fully-documented 7-row
conversion table exactly (`<e/>`→null, text-only, attribute-only,
attribute+text, distinct children, repeated-children-collapse-to-array,
mixed text+children) — every row verified individually against the
real doc's own examples, plus the doc's own full "duration" worked
example (an XML declaration prolog that must be skipped without
erroring, 7 numeric-looking leaf children, retrieved via both
`d.duration.value` dot-access and `d["duration"]["min"]` bracket-access
exactly as the doc's own example does) checked against its exact
stated values (118.0, 100.0). One deliberate, clearly-flagged
inference: the conversion table itself states every leaf value as a
bare string, but the SAME doc page's own worked example feeds numeric
text (`"118.0"`) and shows the resulting JSON holding a genuine numeric
value, not a quoted string — so leaf element text gets number
coercion (int if it parses as one, else float, else left as string)
when the text is directly an element's own content; this is NOT
applied to attribute values, since no worked example demonstrates
attribute-value coercion either way. Real ADX's own documented 1 MB
(1,048,576 byte) input-length cap and "or null, if invalid" contract
are both implemented exactly, not approximated. New tests
(`pkg/engine/func_parse_xml_test.go`): one dedicated test per
conversion-table row, a 3-element array-collapse extension beyond the
table's own 2-element example, the full duration worked example
end to end, invalid-XML-returns-null, null propagation, a
no-attributes/no-text nested-structure case beyond the table's own 7
rows, and the 1 MB cap enforced exactly at the byte boundary the docs
state.

**`extract_json`/`extractjson`'s own scope note (2026-08-18)**:
implemented as a real, general JSONPath walker (`evalJSONPath` in
`func_dynamic.go`) supporting exactly real ADX's own documented subset
(`jsonpath.md`): `$` root, `.prop` dot access, `['prop']`/`["prop"]`
bracket access, `[n]` 0-based array index — turned out to be
meaningfully different from a `parse_json` + dot-access shortcut after
all, since KQL's own dot-access syntax can't express bracket/index
paths at all. Verified against both the function's own simple worked
example (`extract-json-function.md`) and the full JSONPath subset doc
page's own 11-path worked example against its own JSON document. A
second real intra-page doc discrepancy found while verifying (after
`indexof_regex`'s own, in the same session): `jsonpath.md`'s own last
example path, `"$.Tags[0]"`, doesn't actually resolve against the
SAME page's own shown JSON document, where `Tags` is nested under
`Details`, not root-level — looks like a copy-paste typo (a missing
`Details.` prefix) rather than a real, different resolution rule.
Confirmed by testing the doc's own literal path (returns null, as it
should against that document) alongside the corrected path
(`"$.Details.Tags[0]"`, resolves to `"startup"` as the other 11
examples' own pattern would predict) — both are asserted in the test
suite rather than silently "fixing" the doc's own mistake without
recording it. Not implemented: type conversion is via reusing
`types.ParseValue`, the same optional-3rd-arg pattern already
established by `extract()`.

**`parse_version`'s own scope note (2026-08-18, genuinely BLOCKED, not
just deferred)**: verified real ADX's own worked example
(`parse-version-function.md`) and reverse-derived the exact formula —
`result = part1×10²⁴ + part2×10¹⁶ + part3×10⁸ + part4` (missing
trailing parts treated as 0, each part up to 8 digits / 99999999 max)
— confirmed against all 7 rows of the doc's own worked-example table,
including the largest value, `1,000,000,020,000,000,300,000,004`
(from `1.2.3.4`). That value alone is a real, structural blocker: even
the SIMPLEST real-world case, `parse_version("1")`, evaluates to
10²⁴ — about 5 orders of magnitude past `int64`'s own maximum
(~9.2×10¹⁸) — so there is no version string, however ordinary, this
function could return without overflow using this engine's existing
numeric types. Real ADX returns this as `decimal` (128-bit); this
codebase has NO decimal or big-integer type anywhere in `pkg/types`
(confirmed by direct source search — no `TypeDecimal`, no `math/big`
usage at all). A `float64`-based implementation was considered and
rejected: at the 10²⁴ magnitude the worked examples reach, `float64`'s
own ~15-17 significant decimal digits can't distinguish different
`part4` values (0-99999999) at all once `part1 >= 1` — the exact
silent-wrong-answer risk this project's own CLAUDE.md P0 backlog
category exists to catch, not a small numeric-formatting nuance to
wave through. A string-based decimal encoding (fixed-width
zero-padded digits, so lexicographic comparison stays correct) was
also considered, but the docs' own second worked example
(`parsed_version < parse_version("1.0.0.0")`) depends on real numeric
`<` comparison working against the result, which would need either
new comparison-operator logic for a string-encoded-decimal special
case or a genuine new type threaded through `ParseValue`/
`FormatValue`/the comparison operators/aggregations — a real,
cross-cutting type-system addition, not the "genuinely easy" scope
this item was originally framed under (that framing was accurate for
`indexof_regex`, whose blocker was regex-specific and already solved;
it doesn't transfer to `parse_version`, whose blocker is an entirely
separate, numeric-type gap). Deliberately left unimplemented rather
than shipped with silent precision loss or a half-integrated new type.
Revisit as a dedicated, scoped piece of work (add a real decimal/
`math/big`-backed `TypeDecimal` to `pkg/types`, thread it through the
same surfaces `TypeLong`/`TypeReal` already cover) rather than folded
into a "small function addition" session.

**`indexof_regex`'s own scope note (2026-08-18, superseding the
"verify before implementing" note below since it's now done)**:
implemented and verified exactly against real ADX's own worked example
(`indexof-regex-function.md`), all 5 cases, exact output values (0, 3,
-1, -1, null). A genuine discrepancy was found between two sections of
that SAME doc page while verifying: the Parameters table's prose for
`start` describes a negative value as offsetting the search start from
the end of the string (this reads like boilerplate copied from
`indexof()`'s own doc), but the Returns section states plainly "returns
null if start is less than 0" — and the worked example's own `idx5`
case (`start=-1`) confirms a null result, not an end-relative offset.
Implemented per the worked example (ground truth) over the ambiguous
prose. `occurrence < 0` and `length < -1` also return null per the same
Returns section, though neither is exercised by the worked example's 5
cases — taken as stated since nothing contradicts them.

**`parse_user_agent` scope note — SUPERSEDED 2026-08-18, see
`kql_coverage.md`'s Sprint 15 entry.** (Original 2026-08-17 note,
kept for history: real ADX's own docs state its implementation is
"built on regex checks of the input string against a huge number of
predefined patterns," and at the time this was deliberately deferred
because no worked example with real output values had been found and
the actual pattern database's accessibility hadn't been checked. Both
turned out to be resolvable: the database is public
(`ua-parser/uap-core`'s own `regexes.yaml`), and real ADX's own docs
do include worked examples with exact output values once looked for
directly rather than via a narrower search pass.)

### `_TimeReceived` accessor
`ingestion_time()` — real ADX's own function-form accessor for the
automatic ingest-time column. Already noted as deliberately deferred
when `_TimeReceived` itself was built this session (the column is
reachable only by its real name today) — this is that deferred item,
now cross-referenced here too so it isn't lost between documents.

### Misc
`assert` (real ADX's own query-time assertion function — verify exact
failure-mode semantics before implementing), `jaccard_index`,
`format_bytes`

---

## 🟢 Low priority — niche, narrower audience

### Probability distributions / special math functions
`beta_cdf`, `beta_inv`, `beta_pdf`, `erf`, `erfc`, `gamma`, `loggamma`,
`welch_test` — statistical distribution functions; real, but a narrow
slice of real-world KQL usage for this project's own use cases.

### Bitwise / binary (aggregation-level `binary_all_*` already exist; these are the scalar, single-value forms)
`binary_and`, `binary_or`, `binary_not`, `binary_xor`,
`binary_shift_left`, `binary_shift_right`, `bitset_count_ones`

### Punycode
`punycode_from_string`, `punycode_to_string`,
`punycode_domain_from_string`, `punycode_domain_to_string` — narrow,
internationalized-domain-name use case.

### Aggregations
`buildschema` — infer a minimal schema admitting all values of a
dynamic column (already noted in `kql_coverage.md`).

---

## ⚪ Out of scope — distributed-cluster or sketch-structure specific, already understood, not just overlooked

### Cluster / cursor / principal introspection
`current_cluster_endpoint`, `current_database`, `current_principal`,
`current_principal_details`, `current_principal_is_member_of`,
`cursor_after`, `cursor_before_or_at`, `cursor_current`, `extent_id`,
`extent_tags`, `estimate_data_size` — these are all meaningful only in
a real, multi-tenant, clustered ADX deployment (continuous export
cursors, RBAC principal checks, distributed extent metadata). No
equivalent concept exists in this engine's own, single-node, single-
scope model; not a gap so much as a different architecture.

### Geo
`geo_info_from_ip_address` — part of the much larger geo-function
family (50+ functions) already explicitly out of scope in
`kql_coverage.md`.

### HLL / T-Digest sketch structures
Already listed as low-priority/deferred in `kql_coverage.md`'s own
aggregation-functions section (real value is distributed/incremental
aggregation across queries or materialized views, which matters less
for a single-node engine), restated here for completeness since the
full reference list surfaced several additional entry points beyond
what was already tracked:

- Scalar-context forms: `dcount_hll` (compute an HLL sketch directly,
  scalar), `hll_merge` (scalar form — merging two already-computed HLL
  sketch values; distinct from the AGGREGATION `hll_merge`, which
  combines HLL values across a group of rows — same name, two
  genuinely different real-ADX functions, both missing)
- Aggregation forms: `hll`, `hll_if`, `hll_merge` (aggregation form),
  `percentilew`, `percentilesw` (weighted percentile variants)
- T-Digest family (scalar and aggregation): `merge_tdigest`,
  `percentile_tdigest`, `percentile_array_tdigest`,
  `percentrank_tdigest`, `rank_tdigest`, `tdigest`, `tdigest_merge`

---

## Comparison operators — confirmed complete

Every comparison operator in the reference list provided (`between`,
`!between`, `in`/`in~`/`!in`/`!in~`, the full `contains`/`has`/
`hasprefix`/`hassuffix`/`startswith`/`endswith` family including every
`_cs` case-sensitive and `!`-negated variant, `==`/`=~`/`!=`/`!~`,
`has_all`/`has_any`, `matches regex`) is already implemented and
verified — including a live functional spot-check of `hasprefix_cs`
specifically (not just grep) to confirm case-sensitivity is genuinely
enforced, not just present as a recognized name.
