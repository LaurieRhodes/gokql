package engine

// func_series_signal.go — series_fir, series_fft / series_ifft (two
// columns each, expanded by parser/multi_column.go from the internal
// __series_fft_dynamic / __series_ifft_dynamic bags) and
// series_seasonal with an explicit period.

import (
	"encoding/json"
	"fmt"
	"math"
	"math/cmplx"
	"sort"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// seriesFloats evaluates argument i as a numeric array; non-numeric
// and null elements are NaN. ok is false for a non-array value.
func seriesFloats(eng *Engine, fc *parser.FuncCall, i int, schema *types.Schema, row types.Row) ([]float64, bool, error) {
	v, err := evalExpr(eng, fc.Args[i], schema, row)
	if err != nil || v == nil {
		return nil, false, err
	}
	arr, ok := parseJSONArray(v)
	if !ok {
		return nil, false, nil
	}
	out := make([]float64, len(arr))
	for j, el := range arr {
		if f, ok := seriesElementToFloat(el); ok {
			out[j] = f
		} else {
			out[j] = math.NaN()
		}
	}
	return out, true, nil
}

func optBoolArg(eng *Engine, fc *parser.FuncCall, i int, def bool, schema *types.Schema, row types.Row) (bool, error) {
	if len(fc.Args) <= i {
		return def, nil
	}
	v, err := evalExpr(eng, fc.Args[i], schema, row)
	if err != nil {
		return false, err
	}
	if b, ok := v.(bool); ok {
		return b, nil
	}
	return def, nil
}

func floatsJSON(xs []float64) types.Value {
	out := make([]interface{}, len(xs))
	for i, x := range xs {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			out[i] = nil
		} else {
			out[i] = x
		}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// evalSeriesFir is series_fir(series, filter [, normalize [, center]]):
// y[i] = sum_k filter[k] * x[i-k] (x outside the series is 0), the
// window shifted by (len(filter)-1)/2 when center is true, and the
// filter divided by its sum when normalize is true (the default; a
// filter with negative values must then not be normalized: null).
func evalSeriesFir(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if len(fc.Args) < 2 || len(fc.Args) > 4 {
		return nil, true, fmt.Errorf("series_fir requires (series, filter [, normalize [, center]])")
	}
	x, ok, err := seriesFloats(eng, fc, 0, schema, row)
	if err != nil || !ok {
		return nil, true, err
	}
	filter, ok, err := seriesFloats(eng, fc, 1, schema, row)
	if err != nil || !ok || len(filter) == 0 {
		return nil, true, err
	}
	normalize, err := optBoolArg(eng, fc, 2, true, schema, row)
	if err != nil {
		return nil, true, err
	}
	center, err := optBoolArg(eng, fc, 3, false, schema, row)
	if err != nil {
		return nil, true, err
	}
	if normalize {
		sum := 0.0
		for _, c := range filter {
			if c < 0 {
				return nil, true, nil
			}
			sum += c
		}
		if sum == 0 {
			return nil, true, nil
		}
		for i := range filter {
			filter[i] /= sum
		}
	}
	shift := 0
	if center {
		shift = (len(filter) - 1) / 2
	}
	y := make([]float64, len(x))
	for i := range x {
		acc := 0.0
		for k, c := range filter {
			j := i - k + shift
			if j >= 0 && j < len(x) && !math.IsNaN(x[j]) {
				acc += c * x[j]
			}
		}
		y[i] = acc
	}
	return floatsJSON(y), true, nil
}

// evalSeriesFFTBag is the bag {real, imag} behind series_fft and
// series_ifft (x_real [, x_imag]). The inverse divides by the length.
func evalSeriesFFTBag(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row, inverse bool) (types.Value, bool, error) {
	if len(fc.Args) < 1 || len(fc.Args) > 2 {
		return nil, true, fmt.Errorf("series_fft requires (real [, imaginary])")
	}
	re, ok, err := seriesFloats(eng, fc, 0, schema, row)
	if err != nil || !ok {
		return nil, true, err
	}
	im := make([]float64, len(re))
	if len(fc.Args) == 2 {
		v, ok, err := seriesFloats(eng, fc, 1, schema, row)
		if err != nil || !ok {
			return nil, true, err
		}
		if len(v) != len(re) {
			return nil, true, nil
		}
		im = v
	}
	n := len(re)
	in := make([]complex128, n)
	for i := range re {
		r, m := re[i], im[i]
		if math.IsNaN(r) {
			r = 0
		}
		if math.IsNaN(m) {
			m = 0
		}
		in[i] = complex(r, m)
	}
	out := dft(in, inverse)
	oRe, oIm := make([]float64, n), make([]float64, n)
	for i, c := range out {
		if inverse {
			c /= complex(float64(n), 0)
		}
		oRe[i], oIm[i] = real(c), imag(c)
	}
	bag := fmt.Sprintf(`{"real":%s,"imag":%s}`, floatsJSON(oRe), floatsJSON(oIm))
	return bag, true, nil
}

// dft is a radix-2 FFT for power-of-two lengths and a direct DFT
// otherwise (exp(-2πi kn/N) forward, exp(+2πi kn/N) inverse, unscaled).
func dft(x []complex128, inverse bool) []complex128 {
	n := len(x)
	sign := -1.0
	if inverse {
		sign = 1
	}
	if n <= 1 {
		return append([]complex128{}, x...)
	}
	if n&(n-1) == 0 {
		even := make([]complex128, n/2)
		odd := make([]complex128, n/2)
		for i := 0; i < n/2; i++ {
			even[i], odd[i] = x[2*i], x[2*i+1]
		}
		e, o := dft(even, inverse), dft(odd, inverse)
		out := make([]complex128, n)
		for k := 0; k < n/2; k++ {
			t := cmplx.Rect(1, sign*2*math.Pi*float64(k)/float64(n)) * o[k]
			out[k] = e[k] + t
			out[k+n/2] = e[k] - t
		}
		return out
	}
	out := make([]complex128, n)
	for k := 0; k < n; k++ {
		var acc complex128
		for j := 0; j < n; j++ {
			acc += x[j] * cmplx.Rect(1, sign*2*math.Pi*float64(k*j%n)/float64(n))
		}
		out[k] = acc
	}
	return out
}

// evalSeriesSeasonal is series_seasonal(series, period): each position
// is the median of the values at that position across the periods.
// Automatic period detection (period omitted or -1) needs
// series_periods_detect, which is not implemented.
func evalSeriesSeasonal(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if len(fc.Args) < 1 || len(fc.Args) > 2 {
		return nil, true, fmt.Errorf("series_seasonal requires (series [, period])")
	}
	x, ok, err := seriesFloats(eng, fc, 0, schema, row)
	if err != nil || !ok {
		return nil, true, err
	}
	period := int64(-1)
	if len(fc.Args) == 2 {
		v, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if v != nil {
			period = types.ToInt64(v)
		}
	}
	if period <= 0 {
		return nil, true, fmt.Errorf("series_seasonal: automatic period detection (series_periods_detect) is not implemented; pass the period")
	}
	p := int(period)
	med := make([]float64, p)
	for pos := 0; pos < p; pos++ {
		var vals []float64
		for i := pos; i < len(x); i += p {
			if !math.IsNaN(x[i]) {
				vals = append(vals, x[i])
			}
		}
		med[pos] = median(vals)
	}
	y := make([]float64, len(x))
	for i := range y {
		y[i] = med[i%p]
	}
	return floatsJSON(y), true, nil
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := append([]float64{}, v...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}
