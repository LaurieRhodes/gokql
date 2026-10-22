package engine

// func_beta.go — beta_cdf, beta_inv, beta_pdf and welch_test.
//
// beta_cdf is the regularized incomplete beta function I_x(alpha,
// beta) (continued fraction, Numerical Recipes betacf); beta_inv
// inverts it by bisection refined with Newton steps; beta_pdf is the
// density. Out-of-range rules follow the reference pages; for beta_inv
// the reference example is followed where it contradicts its own
// prose (probability > 1 gives null, alpha <= 0 gives NaN).
//
// welch_test is the two-sided p-value of Welch's t-test, with the
// Welch–Satterthwaite degrees of freedom; the reference example (the
// Wikipedia data) gives 0.021.

import (
	"fmt"
	"math"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func evalBetaFunc(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	var n int
	switch fc.Name {
	case "beta_cdf", "beta_inv", "beta_pdf":
		n = 3
	case "welch_test":
		n = 6
	default:
		return nil, false, nil
	}
	if len(fc.Args) != n {
		return nil, true, fmt.Errorf("%s: expected %d arguments, got %d", fc.Name, n, len(fc.Args))
	}
	a := make([]float64, n)
	for i, e := range fc.Args {
		v, err := evalExpr(e, schema, row)
		if err != nil {
			return nil, true, err
		}
		switch x := v.(type) {
		case int64:
			a[i] = float64(x)
		case int32:
			a[i] = float64(x)
		case float64:
			a[i] = x
		default:
			return nil, true, nil // null or nonnumeric
		}
	}
	nan := math.NaN()
	switch fc.Name {
	case "beta_cdf":
		x, al, be := a[0], a[1], a[2]
		if x < 0 || x > 1 || al <= 0 || al > 10000 || be <= 0 || be > 10000 {
			return nan, true, nil
		}
		return regIncBeta(x, al, be), true, nil
	case "beta_pdf":
		x, al, be := a[0], a[1], a[2]
		if x <= 0 || x >= 1 || al <= 0 || be <= 0 {
			return nan, true, nil
		}
		if al+be < 170 {
			return math.Pow(x, al-1) * math.Pow(1-x, be-1) / (math.Gamma(al) / math.Gamma(al+be) * math.Gamma(be)), true, nil
		}
		return math.Exp((al-1)*math.Log(x) + (be-1)*math.Log1p(-x) - lnBeta(al, be)), true, nil
	case "beta_inv":
		p, al, be := a[0], a[1], a[2]
		if al <= 0 || be <= 0 {
			return nan, true, nil
		}
		if p <= 0 || p > 1 {
			return nil, true, nil
		}
		return invRegIncBeta(p, al, be), true, nil
	}
	// welch_test
	m1, v1, c1, m2, v2, c2 := a[0], a[1], a[2], a[3], a[4], a[5]
	s1, s2 := v1/c1, v2/c2
	se2 := s1 + s2
	if c1 < 2 || c2 < 2 || se2 <= 0 {
		return nan, true, nil
	}
	t := (m1 - m2) / math.Sqrt(se2)
	df := se2 * se2 / (s1*s1/(c1-1) + s2*s2/(c2-1))
	return regIncBeta(df/(df+t*t), df/2, 0.5), true, nil
}

// lnBeta is log B(a, b); direct Gamma ratios where they cannot
// overflow are several ulps more accurate than Lgamma differences.
func lnBeta(a, b float64) float64 {
	if a+b < 170 {
		return math.Log(math.Gamma(a) / math.Gamma(a+b) * math.Gamma(b))
	}
	la, _ := math.Lgamma(a)
	lb, _ := math.Lgamma(b)
	lab, _ := math.Lgamma(a + b)
	return la + lb - lab
}

// regIncBeta is I_x(a, b).
func regIncBeta(x, a, b float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	front := math.Exp(a*math.Log(x) + b*math.Log1p(-x) - lnBeta(a, b))
	if x < (a+1)/(a+b+2) {
		return front * betaCF(x, a, b) / a
	}
	return 1 - front*betaCF(1-x, b, a)/b
}

// betaCF evaluates the incomplete beta continued fraction (modified Lentz).
func betaCF(x, a, b float64) float64 {
	const eps, tiny = 1e-16, 1e-300
	qab, qap, qam := a+b, a+1, a-1
	c, d := 1.0, 1-qab*x/qap
	if math.Abs(d) < tiny {
		d = tiny
	}
	d = 1 / d
	h := d
	for m := 1; m <= 10000; m++ {
		fm := float64(m)
		m2 := 2 * fm
		aa := fm * (b - fm) * x / ((qam + m2) * (a + m2))
		d = 1 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		h *= d * c
		aa = -(a + fm) * (qab + fm) * x / ((a + m2) * (qap + m2))
		d = 1 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < eps {
			break
		}
	}
	return h
}

// invRegIncBeta finds x with I_x(a, b) = p.
func invRegIncBeta(p, a, b float64) float64 {
	if p >= 1 {
		return 1
	}
	lo, hi := 0.0, 1.0
	x := 0.5
	for i := 0; i < 200; i++ {
		f := regIncBeta(x, a, b) - p
		if f == 0 {
			return x
		}
		if f < 0 {
			lo = x
		} else {
			hi = x
		}
		// Newton step from the density, kept inside the bracket
		pdf := math.Exp((a-1)*math.Log(x) + (b-1)*math.Log1p(-x) - lnBeta(a, b))
		nx := x - f/pdf
		if pdf <= 0 || math.IsNaN(nx) || nx <= lo || nx >= hi {
			nx = (lo + hi) / 2
		}
		if math.Abs(nx-x) < 1e-17 {
			return nx
		}
		x = nx
	}
	return x
}
