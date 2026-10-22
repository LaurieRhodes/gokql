package engine

// func_series_fit.go — series_fit_line_dynamic and the bag behind the
// multi-column series_fit_line / series_fit_poly (parser/multi_column.go).
//
// Least-squares polynomial fit of y against x (x defaults to 1..n,
// which the series_fit_line reference example needs: LineFit[0] =
// slope + interception). variance is the sample variance of y;
// rvariance is the residual sum of squares over n-1 (the reference's
// 98.628 and 1.686); rsquare = 1 - SSres/SStot. series_fit_poly's
// coefficients run from the highest power down. Non-numeric elements
// are left out of the fit but the fitted series has the input length.

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func evalSeriesFitFunc(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if fc.Name != "series_fit_line_dynamic" && fc.Name != "__series_fit_poly_dynamic" {
		return nil, false, nil
	}
	if len(fc.Args) == 0 {
		return nil, true, fmt.Errorf("%s: expected a series", fc.Name)
	}
	vals := make([]types.Value, len(fc.Args))
	for i, a := range fc.Args {
		v, err := evalExpr(a, schema, row)
		if err != nil {
			return nil, true, err
		}
		vals[i] = v
	}
	if vals[0] == nil {
		return nil, true, nil
	}
	yArr, ok := parseJSONArray(vals[0])
	if !ok {
		return nil, true, nil
	}
	n := len(yArr)
	xs := make([]float64, n)
	for i := range xs {
		xs[i] = float64(i + 1)
	}
	degree := 1
	if fc.Name == "__series_fit_poly_dynamic" {
		if len(vals) > 1 && vals[1] != nil {
			if xArr, ok := parseJSONArray(vals[1]); ok && len(xArr) == n {
				for i, el := range xArr {
					if f, ok := seriesElementToFloat(el); ok {
						xs[i] = f
					} else {
						xs[i] = math.NaN()
					}
				}
			}
		}
		if len(vals) > 2 && vals[2] != nil {
			degree = int(types.ToFloat64(vals[2]))
		}
		if degree < 1 {
			return nil, true, fmt.Errorf("series_fit_poly: degree must be at least 1")
		}
	}
	var px, py []float64
	for i, el := range yArr {
		if f, ok := seriesElementToFloat(el); ok && !math.IsNaN(xs[i]) {
			px = append(px, xs[i])
			py = append(py, f)
		}
	}
	if len(py) <= degree {
		return nil, true, nil
	}
	coef := polyFit(px, py, degree) // lowest power first
	eval := func(x float64) float64 {
		v := 0.0
		for k := len(coef) - 1; k >= 0; k-- {
			v = v*x + coef[k]
		}
		return v
	}
	var mean float64
	for _, v := range py {
		mean += v
	}
	mean /= float64(len(py))
	var ssTot, ssRes float64
	for i, v := range py {
		ssTot += (v - mean) * (v - mean)
		r := v - eval(px[i])
		ssRes += r * r
	}
	m := float64(len(py) - 1)
	rsq := 1.0
	if ssTot > 0 {
		rsq = 1 - ssRes/ssTot
	}
	fit := make([]float64, n)
	for i := range fit {
		fit[i] = eval(xs[i])
	}
	var bag map[string]interface{}
	if fc.Name == "series_fit_line_dynamic" {
		bag = map[string]interface{}{"rsquare": rsq, "slope": coef[1], "variance": ssTot / m,
			"rvariance": ssRes / m, "interception": coef[0], "line_fit": fit}
	} else {
		hi := make([]float64, len(coef))
		for i := range coef {
			hi[i] = coef[len(coef)-1-i]
		}
		bag = map[string]interface{}{"rsquare": rsq, "coefficients": hi, "variance": ssTot / m,
			"rvariance": ssRes / m, "poly_fit": fit}
	}
	b, _ := json.Marshal(bag)
	return string(b), true, nil
}

// polyFit returns least-squares coefficients c[0] + c[1]x + ... + c[d]x^d,
// solving the Vandermonde system by Householder QR.
func polyFit(x, y []float64, d int) []float64 {
	m, n := len(x), d+1
	a := make([][]float64, m)
	for i := range a {
		a[i] = make([]float64, n)
		p := 1.0
		for j := 0; j < n; j++ {
			a[i][j] = p
			p *= x[i]
		}
	}
	b := append([]float64(nil), y...)
	for k := 0; k < n; k++ {
		var norm float64
		for i := k; i < m; i++ {
			norm += a[i][k] * a[i][k]
		}
		norm = math.Sqrt(norm)
		if norm == 0 {
			continue
		}
		if a[k][k] > 0 {
			norm = -norm
		}
		v := make([]float64, m)
		v[k] = a[k][k] - norm
		for i := k + 1; i < m; i++ {
			v[i] = a[i][k]
		}
		var vv float64
		for i := k; i < m; i++ {
			vv += v[i] * v[i]
		}
		if vv == 0 {
			continue
		}
		apply := func(col func(i int) *float64) {
			var s float64
			for i := k; i < m; i++ {
				s += v[i] * *col(i)
			}
			s = 2 * s / vv
			for i := k; i < m; i++ {
				*col(i) -= s * v[i]
			}
		}
		for j := k; j < n; j++ {
			jj := j
			apply(func(i int) *float64 { return &a[i][jj] })
		}
		apply(func(i int) *float64 { return &b[i] })
	}
	c := make([]float64, n)
	for k := n - 1; k >= 0; k-- {
		s := b[k]
		for j := k + 1; j < n; j++ {
			s -= a[k][j] * c[j]
		}
		if a[k][k] != 0 {
			c[k] = s / a[k][k]
		}
	}
	return c
}
