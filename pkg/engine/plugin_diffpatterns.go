package engine

// plugin_diffpatterns.go — evaluate diffpatterns(SplitColumn,
// SplitValueA, SplitValueB [, WeightColumn, Threshold, MaxDimensions,
// CustomWildcard, ...]).
//
// ADX's algorithm (a contrast form of Seed-Expand, Ofer et al. 2017
// section 6) is not specified in enough detail to reproduce, so this is
// a direct formulation of what the plugin reports: rows split into set
// A (tostring(SplitColumn) == SplitValueA) and set B; patterns are
// combinations of (column, value) items over the other columns, at most
// one per column and MaxDimensions in all (null and "" are not values);
// a pattern qualifies when its share of A and its share of B differ by
// at least Threshold (default 0.05); patterns selecting the same rows
// are represented by the one with fewest items. Qualifying patterns
// are taken by the difference, largest first, skipping one that
// generalises or specialises a taken pattern (its items are a subset or
// superset) or whose rows overlap a taken pattern's by more than half
// (Jaccard), so the result is a short list of distinct explanations. Percentages are rounded to
// two decimals, as ADX reports them. The output columns are ADX's.

import (
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["diffpatterns"] = applyDiffPatterns
}

func applyDiffPatterns(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	const name = "diffpatterns"
	if len(args) < 3 {
		return nil, fmt.Errorf("%s(SplitColumn, SplitValueA, SplitValueB [, WeightColumn, Threshold, MaxDimensions, CustomWildcard...]) needs at least 3 arguments", name)
	}
	split, err := pluginColumn(in, name, "split", strings.Trim(strings.TrimSpace(args[0]), `'"`))
	if err != nil {
		return nil, err
	}
	splitVal := func(text string) (string, error) {
		v, _, err := pluginScalar(e, text)
		if err != nil || v == nil {
			return "", fmt.Errorf("%s: split values must be constants", name)
		}
		return fmt.Sprint(v), nil
	}
	va, err := splitVal(args[1])
	if err != nil {
		return nil, err
	}
	vb, err := splitVal(args[2])
	if err != nil {
		return nil, err
	}
	wcol := -1
	if len(args) > 3 && !isTildeArg(args[3]) {
		if wcol, err = pluginColumn(in, name, "weight", strings.Trim(strings.TrimSpace(args[3]), `'"`)); err != nil {
			return nil, err
		}
	}
	threshold := 0.05
	if len(args) > 4 && !isTildeArg(args[4]) {
		v, _, err := pluginScalar(e, args[4])
		if err != nil || v == nil {
			return nil, fmt.Errorf("%s: Threshold: invalid value", name)
		}
		threshold = types.ToFloat64(v)
	}
	if threshold < 0.015 || threshold > 1 {
		return nil, fmt.Errorf("%s: Threshold must be between 0.015 and 1", name)
	}
	maxDims := math.MaxInt
	if len(args) > 5 && !isTildeArg(args[5]) {
		v, _, err := pluginScalar(e, args[5])
		if err != nil || v == nil || types.ToInt64(v) < 1 {
			return nil, fmt.Errorf("%s: MaxDimensions must be a positive integer", name)
		}
		maxDims = int(types.ToInt64(v))
	}
	var wargs []string
	if len(args) > 6 {
		wargs = args[6:]
	}
	wc, err := patternWildcards(e, name, wargs)
	if err != nil {
		return nil, err
	}

	// keep only the rows of the two sets
	sub := &types.Table{Schema: in.Schema}
	var inA []bool
	for _, r := range in.Rows {
		if r[split] == nil {
			continue
		}
		s := fmt.Sprint(r[split])
		if s == va || s == vb {
			sub.Rows = append(sub.Rows, r)
			inA = append(inA, s == va)
		}
	}
	var dims []int
	for i := range in.Schema.Columns {
		if i != split && i != wcol {
			dims = append(dims, i)
		}
	}
	ix := buildItemIndex(sub, dims)
	w := rowWeights(sub, wcol)
	var totA, totB float64
	for r, a := range inA {
		if a {
			totA += w[r]
		} else {
			totB += w[r]
		}
	}
	schema := types.Schema{Columns: []types.Column{{Name: "SegmentId", Type: types.TypeInt},
		{Name: "CountA", Type: types.TypeLong}, {Name: "CountB", Type: types.TypeLong},
		{Name: "PercentA", Type: types.TypeReal}, {Name: "PercentB", Type: types.TypeReal},
		{Name: "PercentDiffAB", Type: types.TypeReal}}}
	for _, c := range dims {
		schema.Columns = append(schema.Columns, in.Schema.Columns[c])
	}
	out := &types.Table{Schema: schema}
	if totA <= 0 || totB <= 0 {
		return out, nil
	}
	share := func(a, b float64) (float64, float64) { return a / totA, b / totB }

	// counts per item in each set
	nItems := len(ix.col)
	itemA, itemB := make([]float64, nItems), make([]float64, nItems)
	for r, its := range ix.rowItems {
		for _, id := range its {
			if id < 0 {
				continue
			}
			if inA[r] {
				itemA[id] += w[r]
			} else {
				itemB[id] += w[r]
			}
		}
	}
	type pat struct {
		items  pattern
		a, b   float64
		rows   bitset
		closed bool
	}
	nWords := (len(sub.Rows) + 63) / 64
	frequent := func(a, b float64) bool {
		pa, pb := share(a, b)
		return pa >= threshold || pb >= threshold
	}
	var level []*pat
	all := map[string]*pat{}
	for id := 0; id < nItems; id++ {
		if !frequent(itemA[id], itemB[id]) {
			continue
		}
		p := &pat{items: pattern{id}, a: itemA[id], b: itemB[id], rows: make(bitset, nWords)}
		for r, its := range ix.rowItems {
			if its[ix.col[id]] == id {
				p.rows.set(r)
			}
		}
		level = append(level, p)
		all[p.items.key()] = p
	}
	sort.Slice(level, func(i, j int) bool { return level[i].items[0] < level[j].items[0] })
	var found []*pat
	found = append(found, level...)
	for size := 2; size <= maxDims && len(level) > 1; size++ {
		var next []*pat
		seen := map[string]bool{}
		for i := 0; i < len(level); i++ {
			for j := i + 1; j < len(level); j++ {
				a, b := level[i].items, level[j].items
				k := len(a)
				same := true
				for t := 0; t < k-1; t++ {
					if a[t] != b[t] {
						same = false
						break
					}
				}
				if !same || ix.col[a[k-1]] == ix.col[b[k-1]] {
					continue
				}
				c := append(append(pattern{}, a...), b[k-1])
				sort.Ints(c)
				if conflict(ix, c) || seen[c.key()] {
					continue
				}
				seen[c.key()] = true
				ok := true
				for drop := range c {
					s := append(append(pattern{}, c[:drop]...), c[drop+1:]...)
					if all[s.key()] == nil {
						ok = false
						break
					}
				}
				if !ok {
					continue
				}
				rows := make(bitset, nWords)
				var ca, cb float64
				for wi := range rows {
					rows[wi] = level[i].rows[wi] & level[j].rows[wi]
				}
				for r := range sub.Rows {
					if rows[r>>6]&(1<<(uint(r)&63)) == 0 {
						continue
					}
					if inA[r] {
						ca += w[r]
					} else {
						cb += w[r]
					}
				}
				if !frequent(ca, cb) {
					continue
				}
				p := &pat{items: c, a: ca, b: cb, rows: rows}
				next = append(next, p)
				all[c.key()] = p
			}
		}
		sort.Slice(next, func(i, j int) bool { return lessPattern(next[i].items, next[j].items) })
		found = append(found, next...)
		level = next
	}
	// different enough; of patterns selecting the same rows, the one
	// with fewest items stands for them
	byRows := map[string]*pat{}
	var order []string
	for _, p := range found {
		pa, pb := share(p.a, p.b)
		if math.Abs(pa-pb) < threshold {
			continue
		}
		k := fmt.Sprint(p.rows)
		if q, ok := byRows[k]; !ok {
			byRows[k] = p
			order = append(order, k)
		} else if len(p.items) < len(q.items) {
			byRows[k] = p
		}
	}
	cands := make([]*pat, 0, len(order))
	for _, k := range order {
		cands = append(cands, byRows[k])
	}
	diff := func(p *pat) float64 {
		pa, pb := share(p.a, p.b)
		return math.Abs(pa - pb)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if di, dj := diff(cands[i]), diff(cands[j]); di != dj {
			return di > dj
		}
		return len(cands[i].items) < len(cands[j].items)
	})
	jaccard := func(x, y bitset) float64 {
		var in, un int
		for i := range x {
			in += bits.OnesCount64(x[i] & y[i])
			un += bits.OnesCount64(x[i] | y[i])
		}
		if un == 0 {
			return 0
		}
		return float64(in) / float64(un)
	}
	var taken []*pat
	round2 := func(v float64) float64 { return math.Round(v*100) / 100 }
	for _, p := range cands {
		dup := false
		for _, t := range taken {
			if subsetOf(t.items, p.items) || subsetOf(p.items, t.items) || jaccard(p.rows, t.rows) > 0.5 {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		taken = append(taken, p)
		pa, pb := share(p.a, p.b)
		row := types.Row{int32(len(taken) - 1), int64(math.Round(p.a)), int64(math.Round(p.b)),
			round2(100 * pa), round2(100 * pb), round2(100 * math.Abs(pa-pb))}
		out.Rows = append(out.Rows, append(row, ix.patternRow(sub, p.items, wc)...))
	}
	return out, nil
}
