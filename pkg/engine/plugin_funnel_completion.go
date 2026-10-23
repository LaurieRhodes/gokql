package engine

// plugin_funnel_completion.go — evaluate funnel_sequence_completion(
// IdColumn, TimelineColumn, Start, End, BinSize, StateColumn, Sequence,
// MaxSequenceStepPeriods).
//
// Pinned against the Kusto emulator (engine 2025.07.06). The timeline
// [Start, End] is cut into BinSize windows from Start, each analysed on
// its own events only. In a window, an id reaches step k of a period P
// when, from one of its events with the first state, the next k states
// follow in order within P of that first event (strictly less than P).
// A step may share its predecessor's timestamp but must come after it
// in the input order (events are sorted stably by time). On StormEvents
// one count of the reference example differs by one, presumably from
// the order of tied rows, which ADX does not expose. Each window with at least one first-state
// event gets, per period and step, the distinct count of ids reaching
// it; StepId numbers the steps from 0.

import (
	"fmt"
	"sort"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["funnel_sequence_completion"] = applyFunnelSequenceCompletion
}

func applyFunnelSequenceCompletion(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	const name = "funnel_sequence_completion"
	if len(args) != 8 {
		return nil, fmt.Errorf("%s(IdColumn, TimelineColumn, Start, End, BinSize, StateColumn, Sequence, MaxSequenceStepPeriods) needs 8 arguments", name)
	}
	tIdx, err := pluginColumn(in, name, "timeline", args[1])
	if err != nil {
		return nil, err
	}
	sIdx, err := pluginColumn(in, name, "state", args[5])
	if err != nil {
		return nil, err
	}
	axis := in.Schema.Columns[tIdx].Type
	var sv [3]int64
	for i, a := range args[2:5] {
		v, vt, err := pluginScalar(e, a)
		if err != nil || v == nil {
			return nil, fmt.Errorf("%s: argument %d: invalid value", name, i+3)
		}
		sv[i] = axisInt(v, vt, axis)
	}
	start, end, bin := sv[0], sv[1], sv[2]
	if bin <= 0 {
		return nil, fmt.Errorf("%s: BinSize must be positive", name)
	}
	seq, err := funnelSequenceArg(e, name, args[6])
	if err != nil {
		return nil, err
	}
	pv, _, err := pluginScalar(e, args[7])
	if err != nil || pv == nil {
		return nil, fmt.Errorf("%s: MaxSequenceStepPeriods must be a dynamic array", name)
	}
	parr, ok := parseJSONArray(pv)
	if !ok || len(parr) == 0 {
		return nil, fmt.Errorf("%s: MaxSequenceStepPeriods must be a non-empty dynamic array", name)
	}
	periods := make([]int64, len(parr))
	periodVals := make([]types.Value, len(parr))
	for i, p := range parr {
		var ticks int64
		switch x := p.(type) {
		case string:
			ts, err := types.ParseValue(x, types.TypeTimespan)
			if err != nil {
				return nil, fmt.Errorf("%s: period %q: %v", name, x, err)
			}
			ticks = types.ToInt64(ts)
		default:
			ticks = int64(types.ToFloat64(x))
		}
		periodVals[i] = ticks
		periods[i] = ticks
		if axis == types.TypeDatetime {
			periods[i] = ticks * 100
		}
	}
	f, err := parseFunnelEventsRaw(in, name, args[0], args[1], start, end, sIdx)
	if err != nil {
		return nil, err
	}
	// per window: per id its events
	type winEvents map[string][]funnelEvent
	wins := map[int64]winEvents{}
	for _, id := range f.ids {
		for _, ev := range f.byID[id] {
			k := floorDiv(ev.t-start, bin)
			if wins[k] == nil {
				wins[k] = winEvents{}
			}
			wins[k][id] = append(wins[k][id], ev)
		}
	}
	keys := make([]int64, 0, len(wins))
	for k, w := range wins {
		has := false
		for _, evs := range w {
			for _, ev := range evs {
				if ev.state == seq[0] {
					has = true
				}
			}
		}
		if has {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := &types.Table{Schema: types.Schema{Columns: []types.Column{in.Schema.Columns[tIdx],
		{Name: in.Schema.Columns[sIdx].Name, Type: types.TypeString},
		{Name: "Period", Type: types.TypeTimespan}, {Name: "dcount", Type: types.TypeLong},
		{Name: "StepId", Type: types.TypeLong}}}}
	for _, k := range keys {
		w := wins[k]
		for pi, period := range periods {
			counts := make([]int64, len(seq))
			for _, evs := range w {
				best := 0
				for s, ev := range evs {
					if ev.state != seq[0] {
						continue
					}
					reached, last := 1, ev.t
					for q := s + 1; q < len(evs) && reached < len(seq); q++ {
						if evs[q].t-ev.t >= period {
							break
						}
						if evs[q].t >= last && evs[q].state == seq[reached] {
							reached++
							last = evs[q].t
						}
					}
					if reached > best {
						best = reached
					}
				}
				for j := 0; j < best; j++ {
					counts[j]++
				}
			}
			for j, st := range seq {
				out.Rows = append(out.Rows, types.Row{start + k*bin, st, periodVals[pi], counts[j], int64(j)})
			}
		}
	}
	return out, nil
}

// parseFunnelEventsRaw is parseFunnelEvents without collapsing repeated
// states.
func parseFunnelEventsRaw(in *types.Table, plugin, idArg, tArg string, start, end int64, sIdx int) (*funnelInput, error) {
	f := &funnelInput{start: start, end: end, byID: map[string][]funnelEvent{}, idVal: map[string]types.Value{}}
	var err error
	if f.idCol, err = pluginColumn(in, plugin, "id", idArg); err != nil {
		return nil, err
	}
	if f.tCol, err = pluginColumn(in, plugin, "timeline", tArg); err != nil {
		return nil, err
	}
	f.sCol = sIdx
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
