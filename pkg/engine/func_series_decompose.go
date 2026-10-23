package engine

// func_series_decompose.go — series_outliers, series_decompose,
// series_decompose_anomalies, series_decompose_forecast and
// series_fit_2lines. Each was pinned against the Kusto emulator
// (engine 2025.07.06); the multi-column forms are expanded by
// parser/multi_column.go into reads of the internal bag functions here.

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func evalSeriesDecomposeFunc(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "series_outliers":
		return evalSeriesOutliers(eng, fc, schema, row)
	case "__series_decompose_dynamic":
		return evalSeriesDecompose(eng, fc, schema, row)
	case "__series_decompose_anomalies_dynamic":
		return evalSeriesDecomposeAnomalies(eng, fc, schema, row)
	case "series_decompose_forecast":
		return evalSeriesDecomposeForecast(eng, fc, schema, row)
	case "series_fit_2lines_dynamic":
		return evalSeriesFit2Lines(eng, fc, schema, row)
	}
	return nil, false, nil
}

// seriesStrict decodes argument i as a numeric array: nulls are NaN;
// ok is false for a non-array or an element that is neither a number
// nor null (ADX returns null for the whole series then).
func seriesStrict(eng *Engine, fc *parser.FuncCall, i int, schema *types.Schema, row types.Row) ([]float64, bool, error) {
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
		if el == nil {
			out[j] = math.NaN()
			continue
		}
		f, ok := seriesElementToFloat(el)
		if !ok {
			return nil, false, nil
		}
		out[j] = f
	}
	return out, true, nil
}

// optArg evaluates argument i, or returns nil when it is absent.
func optArg(eng *Engine, fc *parser.FuncCall, i int, schema *types.Schema, row types.Row) (types.Value, error) {
	if len(fc.Args) <= i {
		return nil, nil
	}
	return evalExpr(eng, fc.Args[i], schema, row)
}

// --- series_outliers -----------------------------------------------

// The normal quantiles ADX uses for the outlier fences: a table at
// percentiles 2, 8, ..., 50, linearly interpolated in between. The
// entries were recovered from emulator scores to 8 decimals; they are
// the standard normal quantiles rounded to that precision.
var outlierQuantKnots = [...]float64{2, 8, 14, 20, 26, 32, 38, 44, 50}
var outlierQuantZ = [...]float64{2.05374891, 1.40507156, 1.08031934, 0.84162123,
	0.64334541, 0.4676988, 0.30548079, 0.15096922, 0}

// outlierQuantile is the (tabulated) standard normal quantile at
// percentile p in [2, 98].
func outlierQuantile(p float64) float64 {
	neg := p <= 50
	if !neg {
		p = 100 - p
	}
	z := 0.0
	for i := 0; i < len(outlierQuantKnots)-1; i++ {
		a, b := outlierQuantKnots[i], outlierQuantKnots[i+1]
		if p >= a && p <= b {
			t := (p - a) / (b - a)
			z = outlierQuantZ[i] + t*(outlierQuantZ[i+1]-outlierQuantZ[i])
			break
		}
	}
	if neg {
		return -z
	}
	return z
}

// percentileInterp is the linearly interpolated percentile of sorted
// values (rank p/100 * (n-1)).
func percentileInterp(sorted []float64, p float64) float64 {
	r := p / 100 * float64(len(sorted)-1)
	i := int(math.Floor(r))
	if i+1 >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	f := r - float64(i)
	return sorted[i] + f*(sorted[i+1]-sorted[i])
}

// outlierScores scores each value against fences derived from a normal
// fit to the lo/hi percentiles: sigma and mu are chosen so the normal
// quantiles at lo and hi land on the data's percentiles, the fences are
// mu -/+ 2.7 sigma, and a value at a fence scores -/+1.5 (linear from
// the percentile). Values between the percentiles score 0, as do NaNs.
// ok is false when fewer than three values are present.
func outlierScores(x []float64, lo, hi float64) ([]float64, bool) {
	return outlierScoresFit(x, len(x), lo, hi)
}

// outlierScoresFit fits the fences to the first m values and scores
// all of them (series_decompose_anomalies with test points).
func outlierScoresFit(x []float64, m int, lo, hi float64) ([]float64, bool) {
	var vals []float64
	for _, v := range x[:m] {
		if !math.IsNaN(v) {
			vals = append(vals, v)
		}
	}
	if len(vals) < 3 {
		return nil, false
	}
	sort.Float64s(vals)
	pl, ph := percentileInterp(vals, lo), percentileInterp(vals, hi)
	zl, zh := outlierQuantile(lo), outlierQuantile(hi)
	out := make([]float64, len(x))
	if ph == pl || zh == zl {
		for i, v := range x {
			switch {
			case math.IsNaN(v):
			case v > ph:
				out[i] = math.Inf(1)
			case v < pl:
				out[i] = math.Inf(-1)
			}
		}
		return out, true
	}
	sigma := (ph - pl) / (zh - zl)
	mu := ph - zh*sigma
	upper, lower := mu+2.7*sigma, mu-2.7*sigma
	for i, v := range x {
		switch {
		case math.IsNaN(v):
		case v > ph:
			out[i] = 1.5 * (v - ph) / (upper - ph)
		case v < pl:
			out[i] = -1.5 * (pl - v) / (pl - lower)
		}
	}
	return out, true
}

// outlierRange returns the percentile pair for kind: tukey is 25/75;
// ctukey takes the given pair (default 10/90).
func outlierRange(kind string, lo, hi types.Value) (float64, float64, error) {
	switch strings.ToLower(kind) {
	case "tukey":
		return 25, 75, nil
	case "", "ctukey":
		l, h := 10.0, 90.0
		if lo != nil {
			l = types.ToFloat64(lo)
		}
		if hi != nil {
			h = types.ToFloat64(hi)
		}
		if l < 2 || l > 98 || h < 2 || h > 98 || l >= h {
			return 0, 0, fmt.Errorf("series_outliers: percentiles must satisfy 2 <= min < max <= 98")
		}
		return l, h, nil
	}
	return 0, 0, fmt.Errorf("series_outliers: kind must be 'tukey' or 'ctukey', got %q", kind)
}

// evalSeriesOutliers is series_outliers(series [, kind [, ignore_val
// [, min_percentile [, max_percentile]]]]).
func evalSeriesOutliers(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if len(fc.Args) < 1 || len(fc.Args) > 5 {
		return nil, true, fmt.Errorf("series_outliers requires (series [, kind [, ignore_val [, min_percentile [, max_percentile]]]])")
	}
	x, ok, err := seriesStrict(eng, fc, 0, schema, row)
	if err != nil || !ok {
		return nil, true, err
	}
	args := make([]types.Value, 5)
	for i := 1; i < len(fc.Args); i++ {
		if args[i], err = evalExpr(eng, fc.Args[i], schema, row); err != nil {
			return nil, true, err
		}
	}
	kind := ""
	if args[1] != nil {
		kind = fmt.Sprintf("%v", args[1])
	}
	lo, hi, err := outlierRange(kind, args[3], args[4])
	if err != nil {
		return nil, true, err
	}
	if args[2] != nil {
		ig := types.ToFloat64(args[2])
		for i, v := range x {
			if v == ig {
				x[i] = math.NaN()
			}
		}
	}
	s, ok := outlierScores(x, lo, hi)
	if !ok {
		return nil, true, nil
	}
	return floatsJSONKeepInf(s), true, nil
}

// floatsJSONKeepInf renders scores; infinities (a zero-width range)
// are written as the strings ADX's dynamic uses.
func floatsJSONKeepInf(xs []float64) types.Value {
	out := make([]interface{}, len(xs))
	for i, x := range xs {
		switch {
		case math.IsNaN(x):
			out[i] = nil
		case math.IsInf(x, 1):
			out[i] = "Infinity"
		case math.IsInf(x, -1):
			out[i] = "-Infinity"
		default:
			out[i] = x
		}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// --- series_decompose ----------------------------------------------

type decomposition struct {
	baseline, seasonal, trend, residual []float64
	trendNone                           bool
	testPoints                          int
}

// decompose splits x into seasonal (median of the training points in
// the same phase), trend (average or least-squares line of the
// deseasonalized training points, extended over the whole series) and
// residual = x - baseline. The training points are all but the last
// testPoints. period 0 means no seasonal component. A null among the
// training points makes the trend (and so baseline and residual) null,
// as ADX does; the seasonal medians skip nulls.
func decompose(x []float64, period int, trend string, testPoints int) (*decomposition, error) {
	n := len(x)
	m := n - testPoints
	if testPoints < 0 || m < 1 {
		return nil, fmt.Errorf("series_decompose: test_points must be between 0 and the series length - 1")
	}
	d := &decomposition{seasonal: make([]float64, n), trend: make([]float64, n),
		baseline: make([]float64, n), residual: make([]float64, n), testPoints: testPoints}
	if period > 0 {
		med := make([]float64, period)
		for pos := 0; pos < period; pos++ {
			var vals []float64
			for i := pos; i < m; i += period {
				if !math.IsNaN(x[i]) {
					vals = append(vals, x[i])
				}
			}
			med[pos] = median(vals)
		}
		for i := range d.seasonal {
			d.seasonal[i] = med[i%period]
		}
	}
	hasNull := false
	for i := 0; i < m; i++ {
		if math.IsNaN(x[i]) {
			hasNull = true
		}
	}
	switch strings.ToLower(trend) {
	case "avg", "":
		var sum float64
		var cnt int
		for i := 0; i < m; i++ {
			if !math.IsNaN(x[i]) {
				sum += x[i] - d.seasonal[i]
				cnt++
			}
		}
		avg := math.NaN()
		if cnt > 0 && !hasNull {
			avg = sum / float64(cnt)
		}
		for i := range d.trend {
			d.trend[i] = avg
		}
	case "linefit":
		var px, py []float64
		for i := 0; i < m; i++ {
			if !math.IsNaN(x[i]) {
				px = append(px, float64(i+1))
				py = append(py, x[i]-d.seasonal[i])
			}
		}
		if len(py) >= 2 && !hasNull {
			c := polyFit(px, py, 1)
			for i := range d.trend {
				d.trend[i] = c[0] + c[1]*float64(i+1)
			}
		} else {
			for i := range d.trend {
				d.trend[i] = math.NaN()
			}
		}
	case "none":
		d.trendNone = true
	default:
		return nil, fmt.Errorf("series_decompose: trend must be 'avg', 'linefit' or 'none', got %q", trend)
	}
	for i := range x {
		d.baseline[i] = d.seasonal[i] + d.trend[i]
		d.residual[i] = x[i] - d.baseline[i]
	}
	return d, nil
}

// decomposeArgs reads series, seasonality, trend, test_points and
// seasonality_threshold from the given argument positions (-1 when the
// function has no such argument) and runs the decomposition.
func decomposeArgs(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row,
	seasonIdx, trendIdx, testIdx, threshIdx int, defTrend string) ([]float64, *decomposition, bool, error) {
	x, ok, err := seriesStrict(eng, fc, 0, schema, row)
	if err != nil || !ok {
		return nil, nil, false, err
	}
	period, trend, testPoints, thresh := int64(-1), defTrend, int64(0), 0.6
	if v, err := optArg(eng, fc, seasonIdx, schema, row); err != nil {
		return nil, nil, false, err
	} else if seasonIdx >= 0 && v != nil {
		period = types.ToInt64(v)
	}
	if v, err := optArg(eng, fc, trendIdx, schema, row); err != nil {
		return nil, nil, false, err
	} else if trendIdx >= 0 && v != nil {
		trend = fmt.Sprintf("%v", v)
	}
	if v, err := optArg(eng, fc, testIdx, schema, row); err != nil {
		return nil, nil, false, err
	} else if testIdx >= 0 && v != nil {
		testPoints = types.ToInt64(v)
	}
	if v, err := optArg(eng, fc, threshIdx, schema, row); err != nil {
		return nil, nil, false, err
	} else if threshIdx >= 0 && v != nil {
		thresh = types.ToFloat64(v)
	}
	if period < 0 {
		p, err := detectSeasonality(x[:max(0, len(x)-int(testPoints))], thresh)
		if err != nil {
			return nil, nil, false, err
		}
		period = int64(p)
	}
	d, err := decompose(x, int(period), trend, int(testPoints))
	if err != nil {
		return nil, nil, false, err
	}
	return x, d, true, nil
}

func decompositionTrendJSON(d *decomposition) interface{} {
	if d.trendNone {
		z := make([]int, len(d.trend))
		return z
	}
	return jsonFloats(d.trend)
}

// jsonFloats renders a series for a bag: NaN as null.
func jsonFloats(xs []float64) []interface{} {
	out := make([]interface{}, len(xs))
	for i, v := range xs {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			out[i] = v
		}
	}
	return out
}

// evalSeriesDecompose is the bag behind series_decompose(series
// [, seasonality [, trend [, test_points [, seasonality_threshold]]]]).
func evalSeriesDecompose(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if len(fc.Args) < 1 || len(fc.Args) > 5 {
		return nil, true, fmt.Errorf("series_decompose requires (series [, seasonality [, trend [, test_points [, seasonality_threshold]]]])")
	}
	_, d, ok, err := decomposeArgs(eng, fc, schema, row, 1, 2, 3, 4, "avg")
	if err != nil || !ok {
		return nil, true, err
	}
	b, _ := json.Marshal(map[string]interface{}{"baseline": jsonFloats(d.baseline),
		"seasonal": jsonFloats(d.seasonal), "trend": decompositionTrendJSON(d), "residual": jsonFloats(d.residual)})
	return string(b), true, nil
}

// evalSeriesDecomposeForecast is series_decompose_forecast(series,
// points [, seasonality [, trend [, seasonality_threshold]]]): the
// baseline of a decomposition trained on all but the last points.
func evalSeriesDecomposeForecast(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if len(fc.Args) < 2 || len(fc.Args) > 5 {
		return nil, true, fmt.Errorf("series_decompose_forecast requires (series, points [, seasonality [, trend [, seasonality_threshold]]])")
	}
	_, d, ok, err := decomposeArgs(eng, fc, schema, row, 2, 3, 1, 4, "linefit")
	if err != nil || !ok {
		return nil, true, err
	}
	return floatsJSON(d.baseline), true, nil
}

// evalSeriesDecomposeAnomalies is the bag behind
// series_decompose_anomalies(series [, threshold [, seasonality
// [, trend [, test_points [, ad_method [, seasonality_threshold]]]]]]):
// series_outliers over the residual; a score above threshold flags 1,
// below -threshold flags -1.
func evalSeriesDecomposeAnomalies(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if len(fc.Args) < 1 || len(fc.Args) > 7 {
		return nil, true, fmt.Errorf("series_decompose_anomalies requires (series [, threshold [, seasonality [, trend [, test_points [, ad_method [, seasonality_threshold]]]]]])")
	}
	_, d, ok, err := decomposeArgs(eng, fc, schema, row, 2, 3, 4, 6, "linefit")
	if err != nil || !ok {
		return nil, true, err
	}
	th := 1.5
	if v, err := optArg(eng, fc, 1, schema, row); err != nil {
		return nil, true, err
	} else if v != nil {
		th = types.ToFloat64(v)
	}
	method := ""
	if v, err := optArg(eng, fc, 5, schema, row); err != nil {
		return nil, true, err
	} else if v != nil {
		method = fmt.Sprintf("%v", v)
	}
	lo, hi, err := outlierRange(method, nil, nil)
	if err != nil {
		return nil, true, err
	}
	score, ok := outlierScoresFit(d.residual, len(d.residual)-d.testPoints, lo, hi)
	if !ok {
		return nil, true, nil
	}
	flags := make([]int, len(score))
	for i, s := range score {
		if s > th {
			flags[i] = 1
		} else if s < -th {
			flags[i] = -1
		}
	}
	b, _ := json.Marshal(map[string]interface{}{"ad_flag": flags, "ad_score": jsonFloats(score),
		"baseline": jsonFloats(d.baseline)})
	return string(b), true, nil
}

// --- series_fit_2lines ---------------------------------------------

type lineStats struct {
	slope, interception, rsquare, variance, rvariance float64
	ssRes                                             float64
}

// fitSegment fits y against x = 1..len(y). variance is the sample
// variance of y, rvariance SSres/(n-1), rsquare 1 - SSres/SStot.
func fitSegment(y []float64) lineStats {
	n := float64(len(y))
	var sx, sy, sxx, sxy float64
	for i, v := range y {
		x := float64(i + 1)
		sx += x
		sy += v
		sxx += x * x
		sxy += x * v
	}
	var st lineStats
	if den := n*sxx - sx*sx; den != 0 {
		st.slope = (n*sxy - sx*sy) / den
	}
	st.interception = (sy - st.slope*sx) / n
	mean := sy / n
	var ssTot float64
	for i, v := range y {
		r := v - (st.interception + st.slope*float64(i+1))
		st.ssRes += r * r
		ssTot += (v - mean) * (v - mean)
	}
	if len(y) > 1 {
		st.variance = ssTot / (n - 1)
		st.rvariance = st.ssRes / (n - 1)
	}
	st.rsquare = 1
	if ssTot > 0 {
		st.rsquare = 1 - st.ssRes/ssTot
	}
	return st
}

// evalSeriesFit2Lines is series_fit_2lines_dynamic(series): the split
// index (the first point of the right line) minimising the total
// squared residual of two independent line fits, each over local
// x = 1..len; the smallest such index wins a tie.
func evalSeriesFit2Lines(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if len(fc.Args) != 1 {
		return nil, true, fmt.Errorf("series_fit_2lines requires one series")
	}
	y, ok, err := seriesStrict(eng, fc, 0, schema, row)
	if err != nil || !ok {
		return nil, true, err
	}
	for _, v := range y {
		if math.IsNaN(v) {
			return nil, true, nil
		}
	}
	n := len(y)
	if n < 2 {
		return nil, true, nil
	}
	if n < 4 {
		return fit2LinesShort(y), true, nil
	}
	lo, hi := 2, n-2
	best, bestSS := -1, math.Inf(1)
	var bl, br lineStats
	for s := lo; s <= hi; s++ {
		l, r := fitSegment(y[:s]), fitSegment(y[s:])
		if ss := l.ssRes + r.ssRes; ss < bestSS-1e-12*math.Max(1, bestSS) || best < 0 {
			best, bestSS, bl, br = s, ss, l, r
		}
	}
	all := fitSegment(y)
	fit := make([]float64, n)
	for i := range fit {
		if i < best {
			fit[i] = bl.interception + bl.slope*float64(i+1)
		} else {
			fit[i] = br.interception + br.slope*float64(i-best+1)
		}
	}
	rsq := 1.0
	if ssTot := all.variance * float64(n-1); ssTot > 0 {
		rsq = 1 - bestSS/ssTot
	}
	seg := func(s lineStats) map[string]interface{} {
		return map[string]interface{}{"slope": s.slope, "interception": s.interception,
			"rsquare": s.rsquare, "variance": s.variance, "rvariance": s.rvariance}
	}
	b, _ := json.Marshal(map[string]interface{}{"split_idx": best, "rsquare": rsq,
		"variance": all.variance, "rvariance": bestSS / float64(n-1),
		"left": seg(bl), "right": seg(br), "line_fit": fit})
	return string(b), true, nil
}

// fit2LinesShort is ADX's result for two or three points: the split
// is n-2, every fit is exact, each line passes through two adjacent
// points with x the 0-based position in the whole series (for two
// points the left line is all zeros), segment variances are 0 and the
// total variance divides by n.
func fit2LinesShort(y []float64) types.Value {
	n := len(y)
	var mean, ss float64
	for _, v := range y {
		mean += v
	}
	mean /= float64(n)
	for _, v := range y {
		ss += (v - mean) * (v - mean)
	}
	through := func(i int) map[string]interface{} {
		slope := y[i+1] - y[i]
		return map[string]interface{}{"slope": slope, "interception": y[i] - slope*float64(i),
			"rsquare": 1.0, "variance": 0.0, "rvariance": 0.0}
	}
	left := map[string]interface{}{"slope": 0.0, "interception": 0.0, "rsquare": 1.0, "variance": 0.0, "rvariance": 0.0}
	right := through(n - 2)
	if n == 3 {
		left = through(0)
	}
	b, _ := json.Marshal(map[string]interface{}{"split_idx": n - 2, "rsquare": 1.0,
		"variance": ss / float64(n), "rvariance": 0.0, "left": left, "right": right, "line_fit": jsonFloats(y)})
	return string(b)
}
