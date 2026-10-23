package engine

// plugin_activity_engagement.go — evaluate activity_engagement(IdColumn,
// TimelineColumn, [Start, End,] InnerActivityWindow,
// OuterActivityWindow [, dim...]).
//
// Pinned against the Kusto emulator (engine 2025.07.06). With Start and
// End, rows outside [Start, End] (both inclusive) are dropped and the
// inner windows are aligned to Start; without them the windows are
// bin(Timeline, Inner). Each inner window holding activity gets a row:
// the distinct ids in it, the distinct ids with a timeline in
// [window end - Outer, window end), and their ratio. Columns: the
// timeline, the dimensions, dcount_activities_inner,
// dcount_activities_outer, activity_ratio.

import (
	"fmt"
	"sort"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["activity_engagement"] = applyActivityEngagement
}

func applyActivityEngagement(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	const name = "activity_engagement"
	if len(args) < 4 {
		return nil, fmt.Errorf("%s(IdColumn, TimelineColumn, [Start, End,] InnerActivityWindow, OuterActivityWindow [, dim...]) needs at least 4 arguments", name)
	}
	tIdx, err := pluginColumn(in, name, "timeline", args[1])
	if err != nil {
		return nil, err
	}
	axis := in.Schema.Columns[tIdx].Type
	if axis != types.TypeDatetime {
		return nil, fmt.Errorf("%s: the timeline column must be a datetime", name)
	}
	// Start and End are present when the third and fourth arguments
	// are datetimes.
	ranged := false
	if len(args) >= 6 {
		if _, vt, err := pluginScalar(e, args[2]); err == nil && vt == types.TypeDatetime {
			ranged = true
		}
	}
	rest := args[2:]
	var lo, hi int64
	if ranged {
		sv, _, err := pluginScalar(e, args[2])
		if err != nil || sv == nil {
			return nil, fmt.Errorf("%s: Start: invalid value", name)
		}
		ev, _, err := pluginScalar(e, args[3])
		if err != nil || ev == nil {
			return nil, fmt.Errorf("%s: End: invalid value", name)
		}
		lo, hi = types.ToInt64(sv), types.ToInt64(ev)
		rest = args[4:]
	}
	if len(rest) < 2 {
		return nil, fmt.Errorf("%s: InnerActivityWindow and OuterActivityWindow are required", name)
	}
	var win [2]int64
	for i := 0; i < 2; i++ {
		v, vt, err := pluginScalar(e, rest[i])
		if err != nil || v == nil {
			return nil, fmt.Errorf("%s: activity window: invalid value", name)
		}
		win[i] = axisInt(v, vt, axis)
		if win[i] <= 0 {
			return nil, fmt.Errorf("%s: activity windows must be positive", name)
		}
	}
	inner, outer := win[0], win[1]
	a, err := parseActivityInput(name, in, args, rest[2:])
	if err != nil {
		return nil, err
	}
	origin := datetimeBinOrigin(inner)
	if ranged {
		origin = lo
	}
	binOf := func(t int64) int64 { return origin + floorDiv(t-origin, inner)*inner }

	out := &types.Table{Schema: a.schema([]types.Column{in.Schema.Columns[a.tCol]},
		types.Column{Name: "dcount_activities_inner", Type: types.TypeLong},
		types.Column{Name: "dcount_activities_outer", Type: types.TypeLong},
		types.Column{Name: "activity_ratio", Type: types.TypeReal})}
	type ev struct {
		t  int64
		id string
	}
	type cell struct {
		bin  int64
		dims types.Row
		row  types.Row
	}
	var cells []cell
	order, rowsBy := a.groups()
	for _, k := range order {
		var evs []ev
		var dims types.Row
		innerIDs := map[int64]map[string]bool{}
		for _, r := range rowsBy[k] {
			t := types.ToInt64(r[a.tCol])
			if ranged && (t < lo || t > hi) {
				continue
			}
			dims = a.dimRow(r)
			b := binOf(t)
			if innerIDs[b] == nil {
				innerIDs[b] = map[string]bool{}
			}
			if r[a.idCol] == nil {
				continue
			}
			id := idKey(r[a.idCol])
			innerIDs[b][id] = true
			evs = append(evs, ev{t, id})
		}
		sort.Slice(evs, func(i, j int) bool { return evs[i].t < evs[j].t })
		for b, ids := range innerIDs {
			end := b + inner
			from := sort.Search(len(evs), func(i int) bool { return evs[i].t >= end-outer })
			outerIDs := map[string]bool{}
			for i := from; i < len(evs) && evs[i].t < end; i++ {
				outerIDs[evs[i].id] = true
			}
			ratio := 0.0
			if len(outerIDs) > 0 {
				ratio = float64(len(ids)) / float64(len(outerIDs))
			}
			cells = append(cells, cell{b, dims, types.Row{int64(len(ids)), int64(len(outerIDs)), ratio}})
		}
	}
	sort.SliceStable(cells, func(i, j int) bool { return cells[i].bin < cells[j].bin })
	for _, c := range cells {
		row := types.Row{c.bin}
		row = append(row, c.dims...)
		out.Rows = append(out.Rows, append(row, c.row...))
	}
	return out, nil
}
