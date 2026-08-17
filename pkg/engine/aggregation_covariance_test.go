package engine

import "testing"

// aggregation_covariance_test.go — covariance/covariancep/
// covarianceif/covariancepif, added 2026-08-17 (aggregation.go),
// alongside the already-implemented variance/variancep/varianceif/
// variancepif family. Every worked-example value below is taken
// directly from real ADX's own documentation or hand-computed from
// its stated formula, not invented.

func approxEqualCov(a, b, eps float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}

// TestCovarianceWorkedExample guards real ADX's own exact worked
// example for covariance(): x=[1,2,3,4,5], y=[14,10,17,20,50] -> 20.5
// (sample covariance, n-1 divisor). Also confirms the auto-generated
// output column name uses BOTH argument names ("covariance_x_y"),
// the documented divergence from the single-column
// function_argname[0] convention used by variance/stdev/etc.
func TestCovarianceWorkedExample(t *testing.T) {
	result := queryResult(t, `datatable(x:real, y:real)
		[1.0, 14.0, 2.0, 10.0, 3.0, 17.0, 4.0, 20.0, 5.0, 50.0]
		| summarize covariance(x, y)`)
	if got := result.Schema.Columns[0].Name; got != "covariance_x_y" {
		t.Errorf("auto-generated column name = %q, want %q", got, "covariance_x_y")
	}
	got := result.Rows[0][0].(float64)
	if !approxEqualCov(got, 20.5, 1e-9) {
		t.Errorf("covariance(x,y) = %v, want 20.5", got)
	}
}

// TestCovariancePPopulationDivisor confirms covariancep uses the n
// divisor (population), not n-1 (sample) — same dataset as
// TestCovarianceWorkedExample, hand-computed: numerator 82.0 / n=5 =
// 16.4 (vs covariance's own 82.0 / (n-1)=4 = 20.5).
func TestCovariancePPopulationDivisor(t *testing.T) {
	result := queryResult(t, `datatable(x:real, y:real)
		[1.0, 14.0, 2.0, 10.0, 3.0, 17.0, 4.0, 20.0, 5.0, 50.0]
		| summarize covariancep(x, y)`)
	if got := result.Schema.Columns[0].Name; got != "covariancep_x_y" {
		t.Errorf("auto-generated column name = %q, want %q", got, "covariancep_x_y")
	}
	got := result.Rows[0][0].(float64)
	if !approxEqualCov(got, 16.4, 1e-9) {
		t.Errorf("covariancep(x,y) = %v, want 16.4", got)
	}
}

// TestCovarianceIfAndCovariancePIf confirms the predicate-filtered
// variants match a manual filter-then-covariance/covariancep of the
// same dataset with the x=3 row (keep=false) excluded, hand-computed:
// remaining pairs (1,14),(2,10),(4,20),(5,50), numerator 82.0,
// covarianceif = 82/3 = 27.333..., covariancepif = 82/4 = 20.5.
func TestCovarianceIfAndCovariancePIf(t *testing.T) {
	result := queryResult(t, `datatable(x:real, y:real, keep:bool)
		[1.0, 14.0, true, 2.0, 10.0, true, 3.0, 17.0, false, 4.0, 20.0, true, 5.0, 50.0, true]
		| summarize covarianceif(x, y, keep), covariancepif(x, y, keep)`)
	gotIf := result.Rows[0][0].(float64)
	gotPIf := result.Rows[0][1].(float64)
	if !approxEqualCov(gotIf, 82.0/3.0, 1e-9) {
		t.Errorf("covarianceif = %v, want %v", gotIf, 82.0/3.0)
	}
	if !approxEqualCov(gotPIf, 20.5, 1e-9) {
		t.Errorf("covariancepif = %v, want 20.5", gotPIf)
	}
}

// TestCovariancePairwiseNullExclusion confirms a null on EITHER side
// of a pair excludes the whole pair from the calculation (not just
// the null side), matching real ADX's own documented "null values are
// ignored" rule applied correctly for a two-variable statistic.
// Nulling y at x=3 should yield the identical result to the
// covarianceif test above, which independently excludes that same
// row via a predicate instead of a null.
func TestCovariancePairwiseNullExclusion(t *testing.T) {
	result := queryResult(t, `datatable(x:real, y:real)
		[1.0, 14.0, 2.0, 10.0, 3.0, 17.0, 4.0, 20.0, 5.0, 50.0]
		| extend y2 = iff(x == 3.0, real(null), y)
		| summarize covariance(x, y2)`)
	got := result.Rows[0][0].(float64)
	if !approxEqualCov(got, 82.0/3.0, 1e-9) {
		t.Errorf("covariance with one pair excluded by null = %v, want %v", got, 82.0/3.0)
	}
}

// TestCovarianceInsufficientDataReturnsNull confirms covariance
// (sample, needs >=2 pairs) returns null with fewer than 2 usable
// pairs, and covariancep (population, needs >=1 pair) returns null
// with zero usable pairs — the same threshold convention already
// established for variance/variancep.
func TestCovarianceInsufficientDataReturnsNull(t *testing.T) {
	result := queryResult(t, `datatable(x:real, y:real) [1.0, 14.0]
		| summarize covariance(x, y)`)
	if result.Rows[0][0] != nil {
		t.Errorf("covariance with 1 pair = %v, want nil", result.Rows[0][0])
	}

	result2 := queryResult(t, `datatable(x:real, y:real) []
		| summarize covariancep(x, y)`)
	if len(result2.Rows) != 0 {
		t.Errorf("covariancep over zero input rows with no by-clause = %d rows, want 0 (matching this engine's existing zero-row summarize behavior)", len(result2.Rows))
	}
}

