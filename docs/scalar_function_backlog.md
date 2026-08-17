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

### Array functions (`func_dynamic.go`, alongside the existing `array_*` family)
`array_sum`, `array_split`, `array_rotate_left`, `array_rotate_right`,
`array_iff`

### Bag/dynamic functions (`func_dynamic.go`)
`bag_zip` (build a bag from a keys array + a values array — the
sibling of `zip()`, already implemented, for the bag-construction
case), `bag_set_key`, `bag_pack_columns`, `dynamic_to_json` (should be
close to trivial — dynamic values are already JSON-encoded strings
internally), `set_has_element` (natural sibling of the already-
implemented `set_union`/`set_intersect`/`set_difference`),
`column_names_of`, `column_ifexists`

### Hash functions (`func_net.go`, alongside `hash_md5`/`hash_sha1`/`hash_sha256`)
`hash` (generic, algorithm-selectable), `hash_combine`, `hash_many`,
`hash_xxhash64`

### Type conversion (`func_convert.go`)
`toguid`, `tohex`, `todecimal`, `gettype` (type introspection — returns
a value's KQL type name as a string; likely near-trivial given this
engine's own `inferValType`/`KQLType` machinery already exists
internally)

### Base64 / compression (`func_net.go`, alongside the existing base64 pair)
`base64_decode_toarray`, `base64_decode_toguid`,
`base64_encode_fromarray`, `base64_encode_fromguid`,
`gzip_compress_to_base64_string`, `gzip_decompress_from_base64_string`,
`zlib_compress_to_base64_string`, `zlib_decompress_from_base64_string`

### String functions (`func_string.go`)
`repeat` (verify this isn't just `strrep` under a different name before
building a second implementation), `regex_quote`, `replace_strings`
(multi-pattern variant of the already-implemented `replace_string`),
`unicode_codepoints_from_string`, `unicode_codepoints_to_string`,
`isascii`, `isutf8`

### Datetime (`func_datetime.go`, alongside the existing, large datetime family)
`datetime_part` (verify against the real docs — likely a single
function that dispatches to the same logic already backing
`year`/`month`/`day`/etc., worth checking whether it can be a thin
wrapper rather than new logic), `datetime_local_to_utc`,
`datetime_utc_to_local`, `datetime_list_timezones`,
`unixtime_microseconds_todatetime`, `unixtime_nanoseconds_todatetime`

### IPv4 / IPv6 (`func_net.go`, alongside the existing ipv4 family)
**IPv4 half CLOSED 2026-08-17**: `has_any_ipv4`, `has_any_ipv4_prefix`,
`has_ipv4_prefix`, `ipv4_is_in_any_range`, `ipv4_is_match`,
`ipv4_range_to_cidr_list`, `ipv4_netmask_suffix`, `format_ipv4_mask`,
`parse_ipv4_mask` are all implemented and verified against real ADX
worked examples — see `kql_coverage.md`'s own entry for full detail,
including 5 real bugs found and fixed in already-implemented sibling
functions (`has_ipv4`, `format_ipv4`, `ipv4_compare`,
`ipv4_is_private`, `ipv4_is_in_range`) along the way. Still open: a
whole IPv6 family this engine has none of yet: `ipv6_compare`,
`ipv6_is_in_range`, `ipv6_is_in_any_range`, `ipv6_is_match`,
`parse_ipv6`, `parse_ipv6_mask`.

### Parse functions (`func_string.go` / `func_net.go`)
**`parse_command_line` CLOSED 2026-08-17** — implements the standard
Win32 CommandLineToArgvW tokenization algorithm, verified against
real ADX's own worked example; see `kql_coverage.md` for detail.
Still open: `parse_user_agent`, `parse_version`, `parse_xml`,
`extract_json` (a single-value shortcut over `parse_json` + dot-access
— verify whether it's meaningfully different before building it as a
new thing), `indexof_regex`.

**`parse_user_agent` scope note (2026-08-17, deliberately deferred,
not attempted)**: real ADX's own docs state its implementation is
"built on regex checks of the input string against a huge number of
predefined patterns" (the underlying reference is the open-source
`ua-parser` project's own regex pattern database, hundreds of
browser/OS/device patterns, not a small closed-form algorithm like
`parse_command_line`'s CommandLineToArgvW). No worked example showing
exact output field values for a real user-agent string was found in
the docs actually fetched this session — only the output *shape*
(Browser: Family/MajorVersion/MinorVersion/Patch; OperatingSystem:
adds PatchMinor; Device fields not enumerated in what was found). A
plausible-but-uncalibrated regex-pattern reimplementation would run
and look reasonable without any confidence it matches real ADX's own
actual classification for anything but the most trivial inputs —
structurally the same risk this project's own `reduce` scope note
already declined to take on for exactly this reason (see
`kql_coverage.md`'s `reduce` note). Deliberately left unimplemented
rather than shipped with unverified precision; revisit only with the
actual `ua-parser` regex database (or a real, multi-example ADX
worked-output table) in hand, not another documentation-only attempt.

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
