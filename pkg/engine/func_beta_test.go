package engine

import (
	"math"
	"strconv"
	"testing"
)

// func_beta_test.go — beta_cdf/beta_inv/beta_pdf/welch_test against the
// reference examples (15 significant digits, as ADX displays) and SciPy.

func TestBetaWelchReference(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{`beta_cdf(0.9, 10.0, 20.0)`, 0.999999999999959},
		{`beta_inv(0.1, 10.0, 20.0)`, 0.226415022388749},
		{`beta_pdf(0.5, 10.0, 20.0)`, 0.746176019310951},
		{`beta_pdf(0.3, 2.5, 3.5)`, 1.8297671231132155},  // SciPy
		{`beta_cdf(0.3, 2.5, 3.5)`, 0.29675298929566646}, // SciPy
		{`beta_inv(0.5, 200, 300)`, 0.39986656271411614}, // SciPy
	}
	for _, c := range cases {
		r := queryResult(t, `print x = `+c.expr)
		got, ok := r.Rows[0][0].(float64)
		if !ok || math.Abs(got-c.want) > 1e-14*math.Max(1, math.Abs(c.want)) {
			t.Errorf("%s = %v, want %v", c.expr, r.Rows[0][0], c.want)
		}
	}
	for _, c := range []struct{ expr, want string }{
		{`beta_cdf(1.5, 10.0, 20.0)`, "NaN"}, {`beta_cdf(-10.0, 10.0, 20.0)`, "NaN"}, {`beta_cdf(0.1, -1.0, 20.0)`, "NaN"},
		{`beta_pdf(1.5, 10.0, 20.0)`, "NaN"}, {`beta_pdf(0.1, -1.0, 20.0)`, "NaN"},
		{`beta_inv(1.5, 10.0, 20.0)`, ""}, {`beta_inv(0.1, -1.0, 20.0)`, "NaN"}, {`beta_cdf(real(null), 1, 2)`, ""},
	} {
		if got := formatCell(queryResult(t, `print x = `+c.expr), 0, 0); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
	r := queryResult(t, `print s1 = dynamic([27.5, 21.0, 19.0, 23.6, 17.0, 17.9, 16.9, 20.1, 21.9, 22.6, 23.1, 19.6, 19.0, 21.7, 21.4]),
	s2 = dynamic([27.1, 22.0, 20.8, 23.4, 23.4, 23.5, 25.8, 22.0, 24.8, 20.2, 21.9, 22.1, 22.9, 20.5, 24.4])
| mv-expand s1 to typeof(double), s2 to typeof(double)
| summarize m1=avg(s1), v1=variance(s1), c1=count(), m2=avg(s2), v2=variance(s2), c2=count()
| project pValue = welch_test(m1, v1, c1, m2, v2, c2)`)
	if got := strconv.FormatFloat(r.Rows[0][0].(float64), 'f', 3, 64); got != "0.021" {
		t.Errorf("welch_test p = %v, want 0.021", r.Rows[0][0])
	}
}
