package engine

// plugin_session_count.go — evaluate session_count(IdColumn,
// TimelineColumn, Start, End, Bin, LookBackWindow [, dim...]).
//
// Pinned against the Kusto emulator (engine 2025.07.06): rows with a
// timeline outside [Start, End] (both inclusive) are ignored, history
// before Start included. Per id (and dimension combination) the events
// are taken in time order; an event opens a new session when it is the
// id's first or when the gap to the id's previous event is at least
// LookBackWindow. Each bin(Timeline, Bin) holding at least one event
// gets a row counting the sessions opened in it (0 when every event
// there continued a session); bins with no events get no row.

import (
	"fmt"
	"math"
	"sort"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["session_count"] = applySessionCount
}

func applySessionCount(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	const name = "session_count"
	if len(args) < 6 {
		return nil, fmt.Errorf("%s(IdColumn, TimelineColumn, Start, End, Bin, LookBackWindow [, dim...]) needs at least 6 arguments", name)
	}
	a, err := parseActivityInput(name, in, args, args[6:])
	if err != nil {
		return nil, err
	}
	axis := in.Schema.Columns[a.tCol].Type
	var sv [4]types.Value
	var st [4]types.KQLType
	for i, arg := range args[2:6] {
		v, vt, err := pluginScalar(e, arg)
		if err != nil || v == nil {
			return nil, fmt.Errorf("%s: argument %d: invalid value", name, i+3)
		}
		sv[i], st[i] = v, vt
	}
	// Integer timelines (datetime ns, timespan, long, int) use exact
	// int64 arithmetic; datetime bins align to 0001-01-01 as bin() does.
	var pos func(v types.Value) float64
	var binOf func(v types.Value) types.Value
	if axis == types.TypeReal {
		step := types.ToFloat64(sv[2])
		if step <= 0 {
			return nil, fmt.Errorf("%s: Bin must be positive", name)
		}
		pos = types.ToFloat64
		binOf = func(v types.Value) types.Value { return math.Floor(types.ToFloat64(v)/step) * step }
	} else {
		step := axisInt(sv[2], st[2], axis)
		if step <= 0 {
			return nil, fmt.Errorf("%s: Bin must be positive", name)
		}
		var origin int64
		if axis == types.TypeDatetime {
			origin = datetimeBinOrigin(step)
		}
		pos = func(v types.Value) float64 { return float64(types.ToInt64(v)) }
		binOf = func(v types.Value) types.Value {
			x := types.ToInt64(v)
			b := origin + floorDiv(x-origin, step)*step
			if axis == types.TypeInt {
				return int32(b)
			}
			return b
		}
	}
	lo, hi := sv[0], sv[1]
	var lookback float64
	if axis == types.TypeReal {
		lookback = types.ToFloat64(sv[3])
	} else {
		lookback = float64(axisInt(sv[3], st[3], axis))
		lo, hi = axisInt(sv[0], st[0], axis), axisInt(sv[1], st[1], axis)
	}
	lof, hif := pos(lo), pos(hi)

	out := &types.Table{Schema: a.schema([]types.Column{in.Schema.Columns[a.tCol]}, types.Column{Name: "count_sessions", Type: types.TypeLong})}
	type cell struct {
		bin  types.Value
		dims types.Row
		n    int64
	}
	var cells []*cell
	order, rowsBy := a.groups()
	for _, k := range order {
		rows := rowsBy[k]
		var inRange []types.Row
		for _, r := range rows {
			if p := pos(r[a.tCol]); p >= lof && p <= hif {
				inRange = append(inRange, r)
			}
		}
		sort.SliceStable(inRange, func(i, j int) bool { return pos(inRange[i][a.tCol]) < pos(inRange[j][a.tCol]) })
		last := map[string]float64{}
		byBin := map[string]*cell{}
		var binOrder []string
		for _, r := range inRange {
			b := binOf(r[a.tCol])
			bk := fmt.Sprint(b)
			c := byBin[bk]
			if c == nil {
				c = &cell{bin: b, dims: a.dimRow(r)}
				byBin[bk] = c
				binOrder = append(binOrder, bk)
			}
			if r[a.idCol] == nil {
				continue
			}
			id := idKey(r[a.idCol])
			p := pos(r[a.tCol])
			if prev, seen := last[id]; !seen || p-prev >= lookback {
				c.n++
			}
			last[id] = p
		}
		for _, bk := range binOrder {
			cells = append(cells, byBin[bk])
		}
	}
	sort.SliceStable(cells, func(i, j int) bool { return pos(cells[i].bin) < pos(cells[j].bin) })
	for _, c := range cells {
		row := types.Row{c.bin}
		row = append(row, c.dims...)
		out.Rows = append(out.Rows, append(row, c.n))
	}
	return out, nil
}
