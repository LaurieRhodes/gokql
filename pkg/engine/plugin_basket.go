package engine

// plugin_basket.go — evaluate basket([Threshold, WeightColumn,
// MaxDimensions, CustomWildcard, ...]), plus the pattern helpers the
// autocluster and diffpatterns plugins share.
//
// Pinned against the Kusto emulator (engine 2025.07.06) on small
// inputs: an item is a (column, value) pair, with null and "" never
// items. Items whose weight exceeds Threshold x total weight (strictly)
// are frequent; patterns are combinations of frequent items, at most
// one per column, whose weight reaches Threshold x total; a pattern is
// returned only when no pattern with one more item has the same weight
// (closed itemsets). Count is the pattern's weight (rows, or the
// WeightColumn sum), Percent its share of the total. ADX samples the
// initial frequent values on large inputs, so its results near the
// threshold can differ from this exact computation, and its row order
// (hash-driven) is not reproduced: rows here are ordered by Count
// descending, then by fewer constrained columns.
//
// MaxDimensions is accepted; at the sizes probed it did not limit the
// pattern size, and its exact effect is not pinned.

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["basket"] = applyBasket
}

// isTildeArg reports a plugin argument written as the default marker.
func isTildeArg(s string) bool {
	s = strings.TrimSpace(s)
	return s == "~" || s == "'~'" || s == `"~"` || s == ""
}

// patternWildcards parses CustomWildcard arguments: each sets the
// wildcard for columns of its type. Defaults: "" for string, null else.
func patternWildcards(e *Engine, plugin string, args []string) (map[types.KQLType]types.Value, error) {
	wc := map[types.KQLType]types.Value{types.TypeString: ""}
	for _, a := range args {
		if isTildeArg(a) {
			continue
		}
		v, vt, err := pluginScalar(e, a)
		if err != nil {
			return nil, fmt.Errorf("%s: CustomWildcard %q: %v", plugin, strings.TrimSpace(a), err)
		}
		wc[vt] = v
	}
	return wc, nil
}

// itemIndex assigns ids to (column, value) items over the dimension
// columns; rowItems[r] lists row r's item ids by dimension position
// (-1 for a null or "" value).
type itemIndex struct {
	dims     []int
	ids      map[string]int
	col      []int // item -> position in dims
	val      []types.Value
	rowItems [][]int
}

func buildItemIndex(in *types.Table, dims []int) *itemIndex {
	ix := &itemIndex{dims: dims, ids: map[string]int{}}
	ix.rowItems = make([][]int, len(in.Rows))
	for r, row := range in.Rows {
		its := make([]int, len(dims))
		for p, c := range dims {
			v := row[c]
			if v == nil {
				its[p] = -1
				continue
			}
			if s, ok := v.(string); ok && s == "" {
				its[p] = -1
				continue
			}
			k := fmt.Sprintf("%d\x00%T:%v", p, v, v)
			id, ok := ix.ids[k]
			if !ok {
				id = len(ix.col)
				ix.ids[k] = id
				ix.col = append(ix.col, p)
				ix.val = append(ix.val, v)
			}
			its[p] = id
		}
		ix.rowItems[r] = its
	}
	return ix
}

// pattern is a set of item ids, sorted, at most one per dimension.
type pattern []int

func (p pattern) key() string { return fmt.Sprint([]int(p)) }

// matches reports whether row r carries every item of p.
func (ix *itemIndex) matches(p pattern, r int) bool {
	its := ix.rowItems[r]
	for _, id := range p {
		if its[ix.col[id]] != id {
			return false
		}
	}
	return true
}

// patternRow renders p over the input's dimension columns.
func (ix *itemIndex) patternRow(in *types.Table, p pattern, wc map[types.KQLType]types.Value) types.Row {
	row := make(types.Row, len(ix.dims))
	for i, c := range ix.dims {
		row[i] = wc[in.Schema.Columns[c].Type]
	}
	for _, id := range p {
		row[ix.col[id]] = ix.val[id]
	}
	return row
}

// rowWeights returns each row's weight (1, or the weight column).
func rowWeights(in *types.Table, wcol int) []float64 {
	w := make([]float64, len(in.Rows))
	for i, row := range in.Rows {
		w[i] = 1
		if wcol >= 0 {
			w[i] = 0
			if row[wcol] != nil {
				w[i] = types.ToFloat64(row[wcol])
			}
		}
	}
	return w
}

// frequentClosedPatterns runs Apriori: items above minItem, patterns
// reaching minPattern, closed ones kept.
func frequentClosedPatterns(ix *itemIndex, w []float64, minItem, minPattern float64) ([]pattern, map[string]float64) {
	itemW := make([]float64, len(ix.col))
	for r, its := range ix.rowItems {
		for _, id := range its {
			if id >= 0 {
				itemW[id] += w[r]
			}
		}
	}
	support := map[string]float64{}
	var level []pattern
	for id, iw := range itemW {
		if iw > minItem {
			p := pattern{id}
			level = append(level, p)
			support[p.key()] = iw
		}
	}
	sort.Slice(level, func(i, j int) bool { return level[i][0] < level[j][0] })
	var all []pattern
	all = append(all, level...)
	for len(level) > 0 {
		// join k-patterns sharing their first k-1 items, on distinct columns
		seen := map[string]bool{}
		var cands []pattern
		for i := 0; i < len(level); i++ {
			for j := i + 1; j < len(level); j++ {
				a, b := level[i], level[j]
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
				// every k-subset must be frequent (Apriori pruning)
				ok := true
				for drop := range c {
					sub := append(append(pattern{}, c[:drop]...), c[drop+1:]...)
					if _, f := support[sub.key()]; !f {
						ok = false
						break
					}
				}
				if ok {
					seen[c.key()] = true
					cands = append(cands, c)
				}
			}
		}
		cw := make([]float64, len(cands))
		for r := range ix.rowItems {
			for ci, c := range cands {
				if ix.matches(c, r) {
					cw[ci] += w[r]
				}
			}
		}
		level = level[:0:0]
		for ci, c := range cands {
			if cw[ci] >= minPattern {
				level = append(level, c)
				support[c.key()] = cw[ci]
			}
		}
		sort.Slice(level, func(i, j int) bool { return lessPattern(level[i], level[j]) })
		all = append(all, level...)
	}
	// closed: no superset with one more item has the same support
	var closed []pattern
	for _, p := range all {
		sp := support[p.key()]
		isClosed := true
		for _, q := range all {
			if len(q) == len(p)+1 && support[q.key()] == sp && subsetOf(p, q) {
				isClosed = false
				break
			}
		}
		if isClosed {
			closed = append(closed, p)
		}
	}
	return closed, support
}

func conflict(ix *itemIndex, p pattern) bool {
	seen := map[int]bool{}
	for _, id := range p {
		if seen[ix.col[id]] {
			return true
		}
		seen[ix.col[id]] = true
	}
	return false
}

func lessPattern(a, b pattern) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func subsetOf(p, q pattern) bool {
	i := 0
	for _, x := range q {
		if i < len(p) && p[i] == x {
			i++
		}
	}
	return i == len(p)
}

// patternOutput builds SegmentId, Count, Percent and the dimension
// columns for the given patterns.
func patternOutput(in *types.Table, ix *itemIndex, pats []pattern, support map[string]float64, total float64, wc map[types.KQLType]types.Value) *types.Table {
	sort.SliceStable(pats, func(i, j int) bool {
		si, sj := support[pats[i].key()], support[pats[j].key()]
		if si != sj {
			return si > sj
		}
		return len(pats[i]) < len(pats[j])
	})
	schema := types.Schema{Columns: []types.Column{{Name: "SegmentId", Type: types.TypeInt},
		{Name: "Count", Type: types.TypeLong}, {Name: "Percent", Type: types.TypeReal}}}
	for _, c := range ix.dims {
		schema.Columns = append(schema.Columns, in.Schema.Columns[c])
	}
	out := &types.Table{Schema: schema}
	for i, p := range pats {
		s := support[p.key()]
		row := types.Row{int32(i), int64(math.Round(s)), s / total * 100}
		out.Rows = append(out.Rows, append(row, ix.patternRow(in, p, wc)...))
	}
	return out
}

func applyBasket(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	const name = "basket"
	threshold := 0.05
	if len(args) > 0 && !isTildeArg(args[0]) {
		v, _, err := pluginScalar(e, args[0])
		if err != nil || v == nil {
			return nil, fmt.Errorf("%s: Threshold: invalid value", name)
		}
		threshold = types.ToFloat64(v)
	}
	if threshold < 0.015 || threshold > 1 {
		return nil, fmt.Errorf("%s: Threshold must be between 0.015 and 1", name)
	}
	wcol := -1
	if len(args) > 1 && !isTildeArg(args[1]) {
		i, err := pluginColumn(in, name, "weight", strings.Trim(strings.TrimSpace(args[1]), `'"`))
		if err != nil {
			return nil, err
		}
		wcol = i
	}
	if len(args) > 2 && !isTildeArg(args[2]) {
		if v, _, err := pluginScalar(e, args[2]); err != nil || v == nil || types.ToInt64(v) < 1 {
			return nil, fmt.Errorf("%s: MaxDimensions must be a positive integer", name)
		}
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
	w := rowWeights(in, wcol)
	var total float64
	for _, x := range w {
		total += x
	}
	ix := buildItemIndex(in, dims)
	if total <= 0 {
		return patternOutput(in, ix, nil, nil, 1, wc), nil
	}
	min := threshold * total
	pats, support := frequentClosedPatterns(ix, w, min, min)
	return patternOutput(in, ix, pats, support, total, wc), nil
}
