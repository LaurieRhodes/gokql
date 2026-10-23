package engine

// plugin_funnel_sequence.go — evaluate funnel_sequence(IdColumn,
// TimelineColumn, Start, End, MaxSequenceStepWindow, Step, StateColumn,
// Sequence): three tables, prev-sequence-next, prev-sequence and
// sequence-next distinct counts with up to 128 sample ids.
//
// Pinned against the Kusto emulator (engine 2025.07.06). Events outside
// [Start, End] are ignored. Per id the events are taken in time order.
// A match is any choice of events carrying the Sequence's states
// in order, each strictly later than the one before and at most
// MaxSequenceStepWindow after it; other events may lie between. prev is
// the latest event strictly earlier than the match's first step whose
// state is not the first step's, and next the earliest strictly later
// than its last whose state is not the last step's; each is kept only
// within MaxSequenceStepWindow, otherwise "". (Repeated states thus read
// as one: cart, cart, pay gives no "cart" prev.) Rows are keyed by the first
// step's period (Start + k*Step). Not pinned: when a step gap equals
// MaxSequenceStepWindow exactly, ADX sometimes drops a prev or next
// that lies within the window; here it is kept. ADX's row and sample
// order look hash-driven; rows here are ordered by period, prev, next,
// and samples by id.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["funnel_sequence"] = applyFunnelSequence
}

type funnelEvent struct {
	t     int64
	state string
}

// funnelInput parses the arguments shared with funnel_sequence_completion
// up to the timeline, returning per-id event lists inside [Start, End].
type funnelInput struct {
	idCol, tCol, sCol int
	start, end        int64
	byID              map[string][]funnelEvent
	idVal             map[string]types.Value
	ids               []string
}

func parseFunnelEvents(in *types.Table, plugin string, idArg, tArg, sArg string, start, end int64) (*funnelInput, error) {
	f := &funnelInput{start: start, end: end, byID: map[string][]funnelEvent{}, idVal: map[string]types.Value{}}
	var err error
	if f.idCol, err = pluginColumn(in, plugin, "id", idArg); err != nil {
		return nil, err
	}
	if f.tCol, err = pluginColumn(in, plugin, "timeline", tArg); err != nil {
		return nil, err
	}
	if f.sCol, err = pluginColumn(in, plugin, "state", sArg); err != nil {
		return nil, err
	}
	for _, r := range in.Rows {
		if r[f.idCol] == nil || r[f.tCol] == nil {
			continue
		}
		t := types.ToInt64(r[f.tCol])
		if t < start || t > end {
			continue
		}
		id := idKey(r[f.idCol])
		if _, ok := f.byID[id]; !ok {
			f.ids = append(f.ids, id)
			f.idVal[id] = r[f.idCol]
		}
		st := ""
		if r[f.sCol] != nil {
			st = fmt.Sprint(r[f.sCol])
		}
		f.byID[id] = append(f.byID[id], funnelEvent{t, st})
	}
	for _, id := range f.ids {
		ev := f.byID[id]
		sort.SliceStable(ev, func(i, j int) bool { return ev[i].t < ev[j].t })
	}
	return f, nil
}

func funnelSequenceArg(e *Engine, plugin, text string) ([]string, error) {
	v, _, err := pluginScalar(e, text)
	if err != nil || v == nil {
		return nil, fmt.Errorf("%s: Sequence must be a dynamic array of states", plugin)
	}
	arr, ok := parseJSONArray(v)
	if !ok || len(arr) == 0 {
		return nil, fmt.Errorf("%s: Sequence must be a non-empty dynamic array of states", plugin)
	}
	seq := make([]string, len(arr))
	for i, a := range arr {
		seq[i] = fmt.Sprint(a)
	}
	return seq, nil
}

// funnelMatches returns the (first, last) index pairs of ev between
// which seq can be followed with every step strictly later and at most
// window apart.
func funnelMatches(ev []funnelEvent, seq []string, window int64) [][2]int {
	var out [][2]int
	for i0, e0 := range ev {
		if e0.state != seq[0] {
			continue
		}
		front := map[int]bool{i0: true}
		for j := 1; j < len(seq) && len(front) > 0; j++ {
			next := map[int]bool{}
			for p := range front {
				for q := p + 1; q < len(ev); q++ {
					if ev[q].t-ev[p].t > window {
						break
					}
					if ev[q].t > ev[p].t && ev[q].state == seq[j] {
						next[q] = true
					}
				}
			}
			front = next
		}
		lasts := make([]int, 0, len(front))
		for q := range front {
			lasts = append(lasts, q)
		}
		sort.Ints(lasts)
		for _, q := range lasts {
			out = append(out, [2]int{i0, q})
		}
	}
	return out
}

func applyFunnelSequence(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	const name = "funnel_sequence"
	if len(args) != 8 {
		return nil, fmt.Errorf("%s(IdColumn, TimelineColumn, Start, End, MaxSequenceStepWindow, Step, StateColumn, Sequence) needs 8 arguments", name)
	}
	tIdx, err := pluginColumn(in, name, "timeline", args[1])
	if err != nil {
		return nil, err
	}
	axis := in.Schema.Columns[tIdx].Type
	var sv [4]int64
	for i, a := range args[2:6] {
		v, vt, err := pluginScalar(e, a)
		if err != nil || v == nil {
			return nil, fmt.Errorf("%s: argument %d: invalid value", name, i+3)
		}
		sv[i] = axisInt(v, vt, axis)
	}
	start, end, window, step := sv[0], sv[1], sv[2], sv[3]
	if step <= 0 {
		return nil, fmt.Errorf("%s: Step must be positive", name)
	}
	seq, err := funnelSequenceArg(e, name, args[7])
	if err != nil {
		return nil, err
	}
	f, err := parseFunnelEvents(in, name, args[0], args[1], args[6], start, end)
	if err != nil {
		return nil, err
	}
	type key struct {
		bin        int64
		prev, next string
	}
	full, prevOnly, nextOnly := map[key]map[string]bool{}, map[key]map[string]bool{}, map[key]map[string]bool{}
	add := func(m map[key]map[string]bool, k key, id string) {
		if m[k] == nil {
			m[k] = map[string]bool{}
		}
		m[k][id] = true
	}
	for _, id := range f.ids {
		ev := f.byID[id]
		for _, m := range funnelMatches(ev, seq, window) {
			first, last := m[0], m[1]
			prev, next := "", ""
			// prev: the latest strictly earlier event whose state is not
			// the first step's; next: the earliest strictly later one
			// whose state is not the last step's
			p := first - 1
			for p >= 0 && (ev[p].t == ev[first].t || ev[p].state == seq[0]) {
				p--
			}
			if p >= 0 && ev[first].t-ev[p].t <= window {
				prev = ev[p].state
			}
			q := last + 1
			for q < len(ev) && (ev[q].t == ev[last].t || ev[q].state == seq[len(seq)-1]) {
				q++
			}
			if q < len(ev) && ev[q].t-ev[last].t <= window {
				next = ev[q].state
			}
			bin := start + floorDiv(ev[first].t-start, step)*step
			add(full, key{bin, prev, next}, id)
			add(prevOnly, key{bin, prev, ""}, id)
			add(nextOnly, key{bin, "", next}, id)
		}
	}
	tcol := in.Schema.Columns[tIdx]
	samples := func(ids map[string]bool) types.Value {
		ks := make([]string, 0, len(ids))
		for k := range ids {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		if len(ks) > 128 {
			ks = ks[:128]
		}
		vals := make([]string, len(ks))
		for i, k := range ks {
			vals[i] = jsonScalar(f.idVal[k])
		}
		return "[" + strings.Join(vals, ",") + "]"
	}
	build := func(m map[key]map[string]bool, cols []types.Column, cells func(k key) types.Row) *types.Table {
		t := &types.Table{Schema: types.Schema{Columns: cols}}
		ks := make([]key, 0, len(m))
		for k := range m {
			ks = append(ks, k)
		}
		sort.Slice(ks, func(i, j int) bool {
			if ks[i].bin != ks[j].bin {
				return ks[i].bin < ks[j].bin
			}
			if ks[i].prev != ks[j].prev {
				return ks[i].prev < ks[j].prev
			}
			return ks[i].next < ks[j].next
		})
		for _, k := range ks {
			row := append(types.Row{k.bin}, cells(k)...)
			t.Rows = append(t.Rows, append(row, int64(len(m[k])), samples(m[k])))
		}
		return t
	}
	str := func(n string) types.Column { return types.Column{Name: n, Type: types.TypeString} }
	cnt := func(n string) types.Column { return types.Column{Name: n, Type: types.TypeLong} }
	smp := types.Column{Name: "samples", Type: types.TypeDynamic}
	t0 := build(full, []types.Column{tcol, str("prev"), str("next"), cnt("dcount"), smp}, func(k key) types.Row { return types.Row{k.prev, k.next} })
	t1 := build(prevOnly, []types.Column{tcol, str("prev"), cnt("dcount_userId"), smp}, func(k key) types.Row { return types.Row{k.prev} })
	t2 := build(nextOnly, []types.Column{tcol, str("next"), cnt("dcount_userId"), smp}, func(k key) types.Row { return types.Row{k.next} })
	e.extraResults = append(e.extraResults, withName(t1, "Table_1"), withName(t2, "Table_2"))
	return withName(t0, "Table_0"), nil
}

// jsonScalar renders a value as a JSON array element.
func jsonScalar(v types.Value) string {
	switch x := v.(type) {
	case string:
		b := strings.Builder{}
		b.WriteString(`"`)
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(x))
		b.WriteString(`"`)
		return b.String()
	case nil:
		return "null"
	}
	return fmt.Sprint(v)
}
