package engine

// plugin_autocluster.go — evaluate autocluster([SizeWeight,
// WeightColumn, NumSeeds, CustomWildcard, ...]).
//
// ADX bases the plugin on Seed-Expand (Ofer, Eldar, Shalev, Resheff,
// "Algorithms for Telemetry Data Mining using Discrete Attributes",
// ICPRAM 2017). The details here were pinned against the Kusto emulator
// (engine 2025.07.06) by sweeping SizeWeight over 0.01..0.99 on 3-, 4-
// and 5-column inputs and fitting:
//
//   - seeds: the distinct rows, by weight descending (first appearance
//     on a tie), at most NumSeeds (default 25);
//   - each seed is expanded by repeatedly dropping the column whose
//     removal keeps the most weight (the first such column on a tie),
//     every step a candidate (the empty pattern excluded);
//   - a candidate with k of the C columns fixed and weight share s
//     scores s^w * ((k - 0.695)/(C - 0.695))^(1-w), w = SizeWeight
//     (default 0.5); the constant was fitted (0.693..0.698 reproduce
//     every sweep point), it is not documented;
//   - each seed keeps its best candidate; those are taken by score and
//     one is dropped when its rows contain, or are contained in, the
//     rows of one already taken.
//
// Count is the weight, Percent its share; null and "" are not values
// (a seed with one there leaves that column open).
//
// This reproduces every synthetic sweep point and the reference's
// SizeWeight 0.6 example and the default, exactly. On larger real
// inputs ADX applies further steps that are not pinned: the reference's
// 0.2 example returns 5 segments here 12, and with a near-constant
// column ADX keeps nested segments this selection drops. Treat its
// output there as an approximation.

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["autocluster"] = applyAutocluster
}

const autoclusterInfoOffset = 0.695

type bitset []uint64

func (b bitset) set(i int) { b[i>>6] |= 1 << (uint(i) & 63) }
func (b bitset) subsetOf(o bitset) bool {
	for i := range b {
		if b[i]&^o[i] != 0 {
			return false
		}
	}
	return true
}

func applyAutocluster(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	const name = "autocluster"
	sw := 0.5
	if len(args) > 0 && !isTildeArg(args[0]) {
		v, _, err := pluginScalar(e, args[0])
		if err != nil || v == nil {
			return nil, fmt.Errorf("%s: SizeWeight: invalid value", name)
		}
		sw = types.ToFloat64(v)
		if sw <= 0 || sw >= 1 {
			return nil, fmt.Errorf("%s: SizeWeight must be between 0 and 1", name)
		}
	}
	wcol := -1
	if len(args) > 1 && !isTildeArg(args[1]) {
		i, err := pluginColumn(in, name, "weight", strings.Trim(strings.TrimSpace(args[1]), `'"`))
		if err != nil {
			return nil, err
		}
		wcol = i
	}
	numSeeds := 25
	if len(args) > 2 && !isTildeArg(args[2]) {
		v, _, err := pluginScalar(e, args[2])
		if err != nil || v == nil || types.ToInt64(v) < 1 {
			return nil, fmt.Errorf("%s: NumSeeds must be a positive integer", name)
		}
		numSeeds = int(types.ToInt64(v))
	}
	var wargs []string
	if len(args) > 3 {
		wargs = args[3:]
	}
	wc, err := patternWildcards(e, name, wargs)
	if err != nil {
		return nil, err
	}
	var dims []int
	for i := range in.Schema.Columns {
		if i != wcol {
			dims = append(dims, i)
		}
	}
	ix := buildItemIndex(in, dims)
	w := rowWeights(in, wcol)
	var total float64
	for _, x := range w {
		total += x
	}
	schema := types.Schema{Columns: []types.Column{{Name: "SegmentId", Type: types.TypeInt},
		{Name: "Count", Type: types.TypeLong}, {Name: "Percent", Type: types.TypeReal}}}
	for _, c := range dims {
		schema.Columns = append(schema.Columns, in.Schema.Columns[c])
	}
	out := &types.Table{Schema: schema}
	if total <= 0 || len(in.Rows) == 0 {
		return out, nil
	}
	C := float64(len(dims))
	nWords := (len(in.Rows) + 63) / 64

	// a pattern is one item id (or -1) per dimension
	key := func(p []int) string { return fmt.Sprint(p) }
	weightOf := map[string]float64{}
	match := func(p []int, r int) bool {
		its := ix.rowItems[r]
		for d, id := range p {
			if id >= 0 && its[d] != id {
				return false
			}
		}
		return true
	}
	weight := func(p []int) float64 {
		k := key(p)
		if v, ok := weightOf[k]; ok {
			return v
		}
		var s float64
		for r := range ix.rowItems {
			if match(p, r) {
				s += w[r]
			}
		}
		weightOf[k] = s
		return s
	}
	fixed := func(p []int) int {
		n := 0
		for _, id := range p {
			if id >= 0 {
				n++
			}
		}
		return n
	}
	score := func(p []int) float64 {
		k := float64(fixed(p))
		return math.Pow(weight(p)/total, sw) * math.Pow((k-autoclusterInfoOffset)/(C-autoclusterInfoOffset), 1-sw)
	}

	// seeds: distinct rows by weight
	type seed struct {
		p   []int
		w   float64
		pos int
	}
	seedIdx := map[string]int{}
	var seeds []*seed
	for r, its := range ix.rowItems {
		k := key(its)
		if i, ok := seedIdx[k]; ok {
			seeds[i].w += w[r]
			continue
		}
		seedIdx[k] = len(seeds)
		seeds = append(seeds, &seed{append([]int(nil), its...), w[r], len(seeds)})
	}
	sort.SliceStable(seeds, func(i, j int) bool { return seeds[i].w > seeds[j].w })
	if len(seeds) > numSeeds {
		seeds = seeds[:numSeeds]
	}

	type cand struct {
		p     []int
		score float64
		order int
	}
	seenBest := map[string]bool{}
	var bests []cand
	for si, s := range seeds {
		if fixed(s.p) == 0 {
			continue
		}
		cur := append([]int(nil), s.p...)
		best := cand{append([]int(nil), cur...), score(cur), si}
		for fixed(cur) > 1 {
			bd, bw := -1, -1.0
			for d, id := range cur {
				if id < 0 {
					continue
				}
				cur[d] = -1
				if x := weight(cur); x > bw {
					bd, bw = d, x
				}
				cur[d] = id
			}
			cur[bd] = -1
			if sc := score(cur); sc > best.score {
				best = cand{append([]int(nil), cur...), sc, si}
			}
		}
		if k := key(best.p); !seenBest[k] {
			seenBest[k] = true
			bests = append(bests, best)
		}
	}
	sort.SliceStable(bests, func(i, j int) bool { return bests[i].score > bests[j].score })
	rowsOf := func(p []int) bitset {
		b := make(bitset, nWords)
		for r := range ix.rowItems {
			if match(p, r) {
				b.set(r)
			}
		}
		return b
	}
	var taken []bitset
	seg := 0
	for _, c := range bests {
		rs := rowsOf(c.p)
		clash := false
		for _, t := range taken {
			if rs.subsetOf(t) || t.subsetOf(rs) {
				clash = true
				break
			}
		}
		if clash {
			continue
		}
		taken = append(taken, rs)
		var pat pattern
		for _, id := range c.p {
			if id >= 0 {
				pat = append(pat, id)
			}
		}
		x := weight(c.p)
		row := types.Row{int32(seg), int64(math.Round(x)), x / total * 100}
		out.Rows = append(out.Rows, append(row, ix.patternRow(in, pat, wc)...))
		seg++
	}
	return out, nil
}
