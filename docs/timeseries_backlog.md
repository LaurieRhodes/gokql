# Time Series (`make-series` + `series_*`) Backlog

**Status update (2026-08-17): Tier 1 + Tier 2 are now DONE** — 26 of
the 49 `series_*` functions closed in one pass (all of Tier 1's
element-wise arithmetic/comparison/unary-math/trig, plus all 4 of
Tier 2's gap-filling functions). See the "Tier 1"/"Tier 2" sections
below for the exact scope, and `pkg/engine/func_series.go`'s own top
comment for two real findings that corrected this document's own
earlier planning-stage guesses (comparison functions return booleans,
not 1.0/0.0; two different missing-element conventions exist across
the function family, not one). Two real, separate bugs in unrelated
conversion functions were found and fixed along the way, while wiring
up the actual make-series + series_fill_forward integration path end
to end — see `pkg/engine/func_convert.go`'s own updated comments.
23 functions remain: Tier 3 (summary statistics, 6 functions) is the
natural next step; Tiers 4-5 (curve fitting, signal processing) were
already flagged LOW priority for this project's own use cases.

**Status update (2026-08-15): `make-series` itself is now DONE** — see
`docs/kql_coverage.md`'s own Implemented table and Sprint 8 entry for
full verification detail (every value in real ADX's own worked
examples matched exactly, including the bin_at floor-division edge
case and kind=nonempty). The prerequisite this whole document was
gated on is closed; the 49 `series_*` functions below are now
genuinely actionable, not blocked.

A dedicated backlog, separate from `scalar_function_backlog.md`,
because this is a genuinely different shape of gap: one prerequisite
*operator* (`make-series`) that produces the array data every
`series_*` function then operates on, not a flat list of independent
functions. Built 2026-08-14, cross-checked against a full real-ADX
enumeration (make-series + 51 `series_*` names) provided directly.

**Confirmed already implemented**: `series_cosine_similarity`,
`series_dot_product` (`func_vector.go`, built for embedding/vector
similarity work); `make-series` itself (2026-08-15, main syntax only —
see `docs/kql_coverage.md`'s own scope note for exactly which parts of
real ADX's documented grammar this covers); all of Tier 1 (22
functions) and Tier 2 (4 functions) below, closed 2026-08-17.
**Confirmed genuinely missing**: the 23 functions in Tiers 3-5.

---

## `make-series` — the prerequisite, verified against real ADX's own docs

```
T | make-series [MakeSeriesParameters]
    [Column =] Aggregation [default = DefaultValue] [, ...]
    on AxisColumn [from start] [to end] step step
    [by [Column =] GroupExpression [, ...]]
```

Real ADX also documents an alternate syntax
(`on AxisColumn in range(start, stop, step)`) but explicitly
recommends against using it ("We recommend that you use the main
syntax... and not the alternate syntax") — build only the main,
recommended form; don't spend effort on the discouraged alternate one
unless something specific needs it later.

**Semantics, precisely** (this is the part worth getting right,
verified directly rather than guessed from the name):

1. Rows are grouped by (`by` expressions, `bin_at(AxisColumn, step, start)`)
   — note `bin_at`, not `bin` — this determines exactly which bucket
   boundary values land on, and matters for matching real ADX's own
   output exactly.
2. Each named `Aggregation` is computed per group. **Only aggregations
   with a numeric result are supported** — this engine's own
   `summarize` aggregation set already mostly satisfies this; the
   validation step (reject a non-numeric aggregation clearly) is new
   work, not the aggregations themselves.
3. The per-group rows are then collapsed again, grouping by just the
   `by` expressions, turning each aggregate column into a `dynamic`
   array (one element per time bucket, in bucket order) — this part
   can likely reuse this engine's own existing `make_list`-style
   array-building logic, not build array construction from scratch.
4. A bucket with **no matching input rows** gets `DefaultValue` in
   that slot (real ADX's own default is `0`, not null — but real
   ADX's own docs also flag: "specify `default=double(null)` in
   make-series if you intend to use interpolation functions for the
   series" afterward, since the fill functions below need to be able
   to tell a real 0 from a genuinely missing value).
5. Output also includes one array column for `AxisColumn` itself (the
   binned time/numeric axis values), and the `by` columns as ordinary,
   non-array columns.
6. Real ADX caps generated arrays at 1,048,576 (2^20) values — worth a
   similar sanity bound here too, to fail clearly on a pathological
   `step` rather than silently building an enormous array.

**Column naming**: `Column` for an aggregation defaults to "a name
derived from the expression" if not given explicitly — check this
engine's own existing `FunctionName_Arg`-style auto-naming convention
(already used for `summarize`'s own unnamed aggregations, and for
`percentiles`/`percentiles_array`'s real-ADX-matched naming, both
already correctly implemented) before inventing a new naming rule; it
likely already covers this case or needs only a small, targeted
addition, the same pattern already used for `percentiles_array`'s own
naming fix earlier this session.

**Priority: 🟡 MED**, not the 🟢 LOW `kql_coverage.md` currently lists
it as — worth revisiting that once this document exists, since
`make-series`'s own value is now understood to be much larger than a
single operator: it's the enabling prerequisite for the entire
`series_*` family below.

---

## Tier 1 — element-wise math/comparison over one or two arrays — ✅ COMPLETE (2026-08-17)

All of these share one shape: map a scalar operation across the
elements of one or two `dynamic` arrays (of equal length, or
broadcasting a scalar), producing a new `dynamic` array of the same
length. Once the first one is built (with a shared helper for
"iterate two same-length JSON arrays element-wise, applying a scalar
op"), the rest of this tier should each be a small, mechanical
addition reusing that helper — not 22 separate implementations.

**Arithmetic**: `series_add`, `series_subtract`, `series_multiply`,
`series_divide`, `series_pow`

**Comparison** (each real-ADX example returns 1.0/0.0 per element, not
a bool array — verify this exactly before assuming, it's an easy
detail to get backwards): `series_equals`, `series_not_equals`,
`series_less`, `series_less_equals`, `series_greater`,
`series_greater_equals`

**Unary math**: `series_abs`, `series_sign`, `series_ceiling`,
`series_floor`, `series_log`, `series_exp`

**Trig** (each is the array-mapped form of a scalar function already
in `scalar_function_backlog.md`'s own medium-priority trigonometry
section — build the scalar `sin`/`cos`/`tan`/`asin`/`acos`/`atan`
first, then these become thin wrappers over them, not new math):
`series_sin`, `series_cos`, `series_tan`, `series_asin`,
`series_acos`, `series_atan`

---

## Tier 2 — gap-filling — ✅ COMPLETE (2026-08-17)

`series_fill_forward` (use the previous value), `series_fill_backward`
(use the next value), `series_fill_const` (a fixed fill value),
`series_fill_linear` (linear interpolation between the surrounding
real values) — real ADX's own docs are explicit that these are the
intended companion to `make-series default=double(null)`, so building
`make-series` without at least `series_fill_forward`/
`series_fill_const` leaves a real, immediately-felt gap.

---

## Tier 3 — summary statistics over one array (🟡 MED)

`series_stats` (multiple summary stats packed into one dynamic
result), `series_stats_dynamic` (same, but as a proper nested dynamic
object rather than flattened columns — verify the exact real-ADX
difference between these two before assuming), `series_sum`,
`series_product`, `series_magnitude` (Euclidean norm), and
`series_pearson_correlation` (correlation between two arrays — a
natural companion to the aggregation-level `covariance` family already
in `scalar_function_backlog.md`).

---

## Tier 4 — curve fitting (🟢 LOW, moderate-to-high complexity, niche for this project's own use cases)

`series_fit_line`, `series_fit_line_dynamic`, `series_fit_2lines`,
`series_fit_2lines_dynamic`, `series_fit_poly` — least-squares linear
regression, piecewise two-segment fitting, and polynomial fitting.
Real, genuine math (not just plumbing) — Go's standard library has no
built-in least-squares solver, so this would need either a small,
hand-rolled normal-equations solver (fine for `fit_line`/low-degree
`fit_poly`) or a numerical library dependency, worth deciding
deliberately rather than defaulting into.

## Tier 5 — advanced signal processing / decomposition / forecasting / anomaly detection (🟢 LOW, high complexity, narrow value for this project's own use cases)

`series_decompose` (STL-style trend/seasonal/residual decomposition),
`series_decompose_anomalies`, `series_decompose_forecast`,
`series_fft`/`series_ifft` (Fast Fourier Transform and inverse),
`series_fir`/`series_iir` (finite/infinite impulse response digital
filters), `series_seasonal`, `series_periods_detect`,
`series_periods_validate`, `series_outliers` — this is real,
substantial signal-processing and statistics work (FFT
implementation, seasonal decomposition algorithms, anomaly-scoring
heuristics), not incremental additions to something already built.
Genuinely low priority given this project's own stated use cases
(security log analysis, academic archaeoastronomy research) — neither
obviously needs time-series anomaly detection or seasonal forecasting
today. Worth building only if a real, specific need for one of these
comes up, not proactively.

---

## Suggested build order for a future session

1. `make-series` itself (the enabling prerequisite — nothing else in
   this document has value without it)
2. Tier 1 (element-wise) + Tier 2 (fill) together — real ADX's own
   docs treat these as a matched pair (`make-series default=...` +
   the fill functions), and Tier 1's shared element-wise helper is
   reusable machinery worth having early
3. Tier 3 (summary statistics) — small, self-contained, high value
   relative to effort
4. Tiers 4–5 only if a specific, real need for curve fitting or signal
   processing actually comes up — don't build these speculatively
