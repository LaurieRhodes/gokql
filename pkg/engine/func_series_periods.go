package engine

// func_series_periods.go — series_periods_detect and
// series_periods_validate (and with them seasonality -1 of the
// series_decompose functions).
//
// ADX does not document its scoring. This implementation follows what
// probing the Kusto emulator (engine 2025.07.06) showed, closely but
// not exactly:
//
//   - a period p scores only when its frequency n/p lies within 1.5
//     DFT bins of a spectral peak (a local maximum of the periodogram
//     of the mean-removed series holding at least 10% of the largest
//     peak's power); otherwise 0. ADX gates similarly; on 150 probe
//     rows this gate agrees with its zero/non-zero pattern on most.
//   - the score is (1 + r)/2, r the circular autocorrelation at lag
//     floor(p). When the series length is a multiple of the period this
//     is ADX's value exactly; otherwise ADX is lower by up to a few
//     hundredths (mean difference 0.005 over the probes).
//   - only the four strongest such peaks are used.
//   - periods must lie in [4, n/2]; detect takes, for each peak, the
//     best-scoring integer period within half a bin of it, keeps
//     scores of at least 0.25 (ADX drops a 0.24 harmonic and keeps a
//     0.27 period in the probes), and returns num_periods distinct
//     periods by score, padded with 0. ADX returns the same top period on most
//     probes (sine, sawtooth, off-bin sine, weekly-with-daily series);
//     where several tones mix it sometimes reports fewer periods.
//
// Nulls are replaced by the mean of the other values.

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func evalSeriesPeriodsFunc(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "__series_periods_detect_dynamic":
		return evalSeriesPeriodsDetect(eng, fc, schema, row)
	case "__series_periods_validate_dynamic":
		return evalSeriesPeriodsValidate(eng, fc, schema, row)
	}
	return nil, false, nil
}

// periodAnalyzer holds a series' mean-removed values and spectral peaks.
type periodAnalyzer struct {
	d     []float64
	ss    float64
	peaks []int
}

// maxPeriodPeaks is how many of the strongest spectral peaks may
// carry a period.
const maxPeriodPeaks = 4

func newPeriodAnalyzer(x []float64) *periodAnalyzer {
	n := len(x)
	var sum float64
	var cnt int
	for _, v := range x {
		if !math.IsNaN(v) {
			sum += v
			cnt++
		}
	}
	mean := 0.0
	if cnt > 0 {
		mean = sum / float64(cnt)
	}
	a := &periodAnalyzer{d: make([]float64, n)}
	for i, v := range x {
		if !math.IsNaN(v) {
			a.d[i] = v - mean
		}
		a.ss += a.d[i] * a.d[i]
	}
	// periodogram over bins 1..n/2
	half := n / 2
	pw := make([]float64, half+1)
	for k := 1; k <= half; k++ {
		var re, im float64
		w := 2 * math.Pi * float64(k) / float64(n)
		for t, v := range a.d {
			s, c := math.Sincos(w * float64(t))
			re += v * c
			im -= v * s
		}
		pw[k] = re*re + im*im
	}
	maxP := 0.0
	for k := 1; k <= half; k++ {
		maxP = math.Max(maxP, pw[k])
	}
	if maxP <= 0 {
		return a
	}
	for k := 1; k <= half; k++ {
		if pw[k] < 0.1*maxP || pw[k] < pw[k-1] || (k+1 <= half && pw[k] < pw[k+1]) {
			continue
		}
		a.peaks = append(a.peaks, k)
	}
	// the strongest few peaks only: weak harmonics are not periods
	sort.SliceStable(a.peaks, func(i, j int) bool { return pw[a.peaks[i]] > pw[a.peaks[j]] })
	if len(a.peaks) > maxPeriodPeaks {
		a.peaks = a.peaks[:maxPeriodPeaks]
	}
	return a
}

// acf is the circular autocorrelation at lag k.
func (a *periodAnalyzer) acf(k int) float64 {
	if a.ss == 0 {
		return 0
	}
	n := len(a.d)
	var s float64
	for t, v := range a.d {
		s += v * a.d[(t+k)%n]
	}
	return s / a.ss
}

func (a *periodAnalyzer) gated(p float64) bool {
	f := float64(len(a.d)) / p
	for _, k := range a.peaks {
		if math.Abs(f-float64(k)) < 1.5 {
			return true
		}
	}
	return false
}

// score is the significance of period p in [0, 1].
func (a *periodAnalyzer) score(p float64) float64 {
	n := float64(len(a.d))
	if p < 4 || p > n/2 || !a.gated(p) {
		return 0
	}
	s := (1 + a.acf(int(math.Floor(p)))) / 2
	return math.Max(0, math.Min(1, s))
}

// detect returns up to num (period, score) pairs within [lo, hi].
func (a *periodAnalyzer) detect(lo, hi float64, num int) ([]float64, []float64) {
	n := float64(len(a.d))
	lo, hi = math.Max(lo, 4), math.Min(hi, n/2)
	type cand struct{ p, s float64 }
	best := map[int]cand{}
	for _, k := range a.peaks {
		// search the integer periods within half a bin of the peak, so
		// another tone's harmonic is out of reach
		bandLo, bandHi := n/(float64(k)+0.5), n/(float64(k)-0.5)
		var c cand
		for p := math.Ceil(math.Max(lo, bandLo)); p <= math.Min(hi, bandHi); p++ {
			if sp := a.score(p); sp > c.s {
				c = cand{p, sp}
			}
		}
		if c.s >= 0.25 {
			if old, ok := best[int(c.p)]; !ok || c.s > old.s {
				best[int(c.p)] = c
			}
		}
	}
	cs := make([]cand, 0, len(best))
	for _, c := range best {
		cs = append(cs, c)
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].s != cs[j].s {
			return cs[i].s > cs[j].s
		}
		return cs[i].p < cs[j].p
	})
	ps, ss := make([]float64, num), make([]float64, num)
	for i := 0; i < num && i < len(cs); i++ {
		ps[i], ss[i] = cs[i].p, cs[i].s
	}
	return ps, ss
}

func periodsBag(ps, ss []float64) types.Value {
	b, _ := json.Marshal(map[string]interface{}{"periods": ps, "scores": ss})
	return string(b)
}

// evalSeriesPeriodsDetect is the bag behind series_periods_detect(series,
// min_period, max_period, num_periods).
func evalSeriesPeriodsDetect(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if len(fc.Args) != 4 {
		return nil, true, fmt.Errorf("series_periods_detect requires (series, min_period, max_period, num_periods)")
	}
	x, ok, err := seriesStrict(eng, fc, 0, schema, row)
	if err != nil || !ok {
		return nil, true, err
	}
	var v [3]types.Value
	for i := 1; i <= 3; i++ {
		if v[i-1], err = evalExpr(eng, fc.Args[i], schema, row); err != nil {
			return nil, true, err
		}
		if v[i-1] == nil {
			return nil, true, nil
		}
	}
	num := int(types.ToInt64(v[2]))
	if num < 1 {
		return nil, true, fmt.Errorf("series_periods_detect: num_periods must be positive")
	}
	ps, ss := newPeriodAnalyzer(x).detect(types.ToFloat64(v[0]), types.ToFloat64(v[1]), num)
	return periodsBag(ps, ss), true, nil
}

// evalSeriesPeriodsValidate is the bag behind
// series_periods_validate(series, period1 [, period2 ...]).
func evalSeriesPeriodsValidate(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if len(fc.Args) < 2 || len(fc.Args) > 17 {
		return nil, true, fmt.Errorf("series_periods_validate requires (series, period1 [, ... period16])")
	}
	x, ok, err := seriesStrict(eng, fc, 0, schema, row)
	if err != nil || !ok {
		return nil, true, err
	}
	a := newPeriodAnalyzer(x)
	ps := make([]float64, len(fc.Args)-1)
	ss := make([]float64, len(ps))
	for i := range ps {
		v, err := evalExpr(eng, fc.Args[i+1], schema, row)
		if err != nil {
			return nil, true, err
		}
		ps[i] = types.ToFloat64(v)
		ss[i] = a.score(ps[i])
	}
	return periodsBag(ps, ss), true, nil
}

// detectSeasonality is series_decompose's seasonality -1: the best
// detected period when its score reaches thresh, else 0 (none).
func detectSeasonality(x []float64, thresh float64) (int, error) {
	ps, ss := newPeriodAnalyzer(x).detect(4, float64(len(x))/2, 1)
	if ss[0] >= thresh && ps[0] >= 4 {
		return int(ps[0]), nil
	}
	return 0, nil
}
