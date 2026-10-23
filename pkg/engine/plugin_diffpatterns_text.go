package engine

// plugin_diffpatterns_text.go — evaluate diffpatterns_text(TextColumn,
// BooleanCondition [, MinTokens, Threshold, MaxTokens]).
//
// Each text is split on white space and its first MaxTokens (default
// 20) tokens become positional items (position, token). Patterns are
// the closed itemsets over those items (every position on which all of
// a pattern's rows agree is fixed), mined with LCM, that are frequent
// (at least Threshold of either set) and have at least MinTokens fixed
// tokens; a pattern qualifies when its shares of the two sets differ by
// at least Threshold. As in diffpatterns, qualifying patterns are taken
// by the difference, largest first, skipping one that generalises or
// specialises a taken pattern or whose rows overlap a taken pattern's
// by more than half. The output is ordered by Percent_of_True -
// Percent_of_False, descending, as ADX orders it. A pattern runs from
// the first position to its last fixed token, with "*" for each free
// position and one trailing "*" when its texts go on. ADX's own
// selection is not specified; on the reference example this finds the
// same leading patterns.

import (
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["diffpatterns_text"] = applyDiffPatternsText
}

func applyDiffPatternsText(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	const name = "diffpatterns_text"
	if len(args) < 2 || len(args) > 5 {
		return nil, fmt.Errorf("%s(TextColumn, BooleanCondition [, MinTokens, Threshold, MaxTokens])", name)
	}
	col, err := pluginColumn(in, name, "text", strings.Trim(strings.TrimSpace(args[0]), `'"`))
	if err != nil {
		return nil, err
	}
	cond, err := parser.ParseExpr(args[1])
	if err != nil {
		return nil, fmt.Errorf("%s: condition: %w", name, err)
	}
	num := func(i int, def, lo, hi float64, what string) (float64, error) {
		if len(args) <= i || isTildeArg(args[i]) {
			return def, nil
		}
		v, _, err := pluginScalar(e, args[i])
		if err != nil || v == nil {
			return 0, fmt.Errorf("%s: %s: invalid value", name, what)
		}
		f := types.ToFloat64(v)
		if f < lo || f > hi {
			return 0, fmt.Errorf("%s: %s must be between %g and %g", name, what, lo, hi)
		}
		return f, nil
	}
	minTok, err := num(2, 1, 0, 200, "MinTokens")
	if err != nil {
		return nil, err
	}
	threshold, err := num(3, 0.05, 0.015, 1, "Threshold")
	if err != nil {
		return nil, err
	}
	maxTok, err := num(4, 20, 0, 20, "MaxTokens")
	if err != nil {
		return nil, err
	}
	P := int(maxTok)
	if P == 0 {
		P = 20
	}

	schema := types.Schema{Columns: []types.Column{
		{Name: "Count_of_True", Type: types.TypeLong}, {Name: "Count_of_False", Type: types.TypeLong},
		{Name: "Percent_of_True", Type: types.TypeReal}, {Name: "Percent_of_False", Type: types.TypeReal},
		{Name: "Pattern", Type: types.TypeString}}}
	out := &types.Table{Schema: schema}

	// rows: tokens and set
	var toks [][]string
	var full []int // token count before truncation
	var isTrue []bool
	for _, r := range in.Rows {
		v, err := evalExpr(e, cond, &in.Schema, r)
		if err != nil {
			return nil, fmt.Errorf("%s: condition: %w", name, err)
		}
		b, ok := v.(bool)
		if !ok {
			continue
		}
		s, _ := r[col].(string)
		f := strings.Fields(s)
		full = append(full, len(f))
		if len(f) > P {
			f = f[:P]
		}
		toks = append(toks, f)
		isTrue = append(isTrue, b)
	}
	var totT, totF float64
	for _, t := range isTrue {
		if t {
			totT++
		} else {
			totF++
		}
	}
	if totT == 0 || totF == 0 {
		return out, nil
	}
	n := len(toks)
	nw := (n + 63) / 64
	type bitsT = []uint64
	count := func(rows bitsT) (float64, float64) {
		var a, b float64
		for r := 0; r < n; r++ {
			if rows[r>>6]&(1<<(uint(r)&63)) != 0 {
				if isTrue[r] {
					a++
				} else {
					b++
				}
			}
		}
		return a, b
	}
	frequent := func(a, b float64) bool { return a/totT >= threshold || b/totF >= threshold }

	// items in a fixed order: (position, token)
	type item struct {
		pos int
		tok string
	}
	idOf := map[item]int{}
	var items []item
	var itemRows []bitsT
	for r, f := range toks {
		for p, t := range f {
			it := item{p, t}
			id, ok := idOf[it]
			if !ok {
				id = len(items)
				idOf[it] = id
				items = append(items, it)
				itemRows = append(itemRows, make(bitsT, nw))
			}
			itemRows[id][r>>6] |= 1 << (uint(r) & 63)
		}
	}
	// frequent items only, ordered by position then token
	var freq []int
	for id := range items {
		if a, b := count(itemRows[id]); frequent(a, b) {
			freq = append(freq, id)
		}
	}
	sort.Slice(freq, func(i, j int) bool {
		a, b := items[freq[i]], items[freq[j]]
		if a.pos != b.pos {
			return a.pos < b.pos
		}
		return a.tok < b.tok
	})
	rank := map[int]int{}
	for i, id := range freq {
		rank[id] = i
	}
	// closure: the frequent items every row of rows has
	closure := func(rows bitsT) []int {
		first := -1
		for w, x := range rows {
			if x != 0 {
				first = w*64 + bits.TrailingZeros64(x)
				break
			}
		}
		if first < 0 {
			return nil
		}
		var c []int
		for p, t := range toks[first] {
			id, ok := idOf[item{p, t}]
			if !ok {
				continue
			}
			if _, ok := rank[id]; !ok {
				continue
			}
			ir := itemRows[id]
			all := true
			for w := range rows {
				if rows[w]&^ir[w] != 0 {
					all = false
					break
				}
			}
			if all {
				c = append(c, rank[id])
			}
		}
		sort.Ints(c)
		return c
	}
	type pat struct {
		items []int // ranks
		rows  bitsT
		a, b  float64
	}
	var found []*pat
	const maxPatterns = 200000
	var lcm func(p []int, rows bitsT, core int)
	lcm = func(p []int, rows bitsT, core int) {
		if len(found) >= maxPatterns {
			return
		}
		in := map[int]bool{}
		for _, x := range p {
			in[x] = true
		}
		for e := core + 1; e < len(freq); e++ {
			if in[e] {
				continue
			}
			ir := itemRows[freq[e]]
			nr := make(bitsT, nw)
			empty := true
			for w := range nr {
				nr[w] = rows[w] & ir[w]
				if nr[w] != 0 {
					empty = false
				}
			}
			if empty {
				continue
			}
			a, b := count(nr)
			if !frequent(a, b) {
				continue
			}
			q := closure(nr)
			// prefix preservation: q and p agree below e
			ok := true
			for _, x := range q {
				if x < e && !in[x] {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			found = append(found, &pat{items: q, rows: nr, a: a, b: b})
			lcm(q, nr, e)
		}
	}
	allRows := make(bitsT, nw)
	for r := 0; r < n; r++ {
		allRows[r>>6] |= 1 << (uint(r) & 63)
	}
	lcm(closure(allRows), allRows, -1)

	diff := func(p *pat) float64 { return p.a/totT - p.b/totF }
	var cands []*pat
	for _, p := range found {
		if float64(len(p.items)) < minTok || math.Abs(diff(p)) < threshold {
			continue
		}
		cands = append(cands, p)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if di, dj := math.Abs(diff(cands[i])), math.Abs(diff(cands[j])); di != dj {
			return di > dj
		}
		return len(cands[i].items) < len(cands[j].items)
	})
	subset := func(x, y []int) bool { // x ⊆ y
		j := 0
		for _, v := range x {
			for j < len(y) && y[j] < v {
				j++
			}
			if j == len(y) || y[j] != v {
				return false
			}
		}
		return true
	}
	// share of p's rows (y) that t's rows (x) already cover
	jacc := func(x, y bitsT) float64 {
		var i, u int
		for w := range x {
			i += bits.OnesCount64(x[w] & y[w])
			u += bits.OnesCount64(y[w])
		}
		if u == 0 {
			return 0
		}
		return float64(i) / float64(u)
	}
	var taken []*pat
	for _, p := range cands {
		dup := false
		for _, t := range taken {
			if subset(t.items, p.items) || subset(p.items, t.items) || jacc(t.rows, p.rows) > 0.5 {
				dup = true
				break
			}
		}
		if !dup {
			taken = append(taken, p)
		}
	}
	sort.SliceStable(taken, func(i, j int) bool { return diff(taken[i]) > diff(taken[j]) })
	round2 := func(v float64) float64 { return math.Round(v*100) / 100 }
	for _, p := range taken {
		fixed := map[int]string{}
		last := -1
		for _, x := range p.items {
			it := items[freq[x]]
			fixed[it.pos] = it.tok
			if it.pos > last {
				last = it.pos
			}
		}
		var sb []string
		for i := 0; i <= last; i++ {
			if t, ok := fixed[i]; ok {
				sb = append(sb, t)
			} else {
				sb = append(sb, "*")
			}
		}
		if last+1 < P {
			sb = append(sb, "*")
		}
		out.Rows = append(out.Rows, types.Row{int64(p.a), int64(p.b),
			round2(100 * p.a / totT), round2(100 * p.b / totF), strings.Join(sb, " ")})
	}
	return out, nil
}
