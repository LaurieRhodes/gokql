package engine

// plugin_activity.go — activity evaluate plugins:
//
//	activity_counts_metrics(IdColumn, TimelineColumn, Start, End, Step [, dim...])
//	activity_metrics(IdColumn, TimelineColumn, Start, End, Step [, dim...])
//	active_users_count(IdColumn, TimelineColumn, Start, End, LookbackWindow, Period, ActivePeriodsCount, Bin [, dim...])
//	new_activity_metrics(IdColumn, TimelineColumn, Start, End, Window [, Cohort] [, dim...] [, Lookback])
//
// Periods are aligned to Start (bin_at), or to startofweek/
// startofmonth/startofyear when Step is "week", "month" or "year"; a
// period belongs to the analysis when its start lies in [Start, End].
// Output columns and types are Kusto.Language's. Rows are ordered by
// dimension values, then period.
//
// activity_counts_metrics: per period with rows, count, dcount,
// new_dcount (Ids not seen in an earlier period) and aggregated_dcount
// (distinct Ids from the first period through this one). The reference
// example reproduces exactly.
//
// activity_metrics: per period with rows, compared with the period
// immediately before it: dcount_values, dcount_newvalues (Ids not in
// the previous period), retention_rate = retained / previous dcount,
// churn_rate = 1 - retention_rate; both NaN when the previous period
// is empty (the reference's first row).
//
// active_users_count: an appearance at t is active when the Id appears
// in at least ActivePeriodsCount distinct Periods within
// (t - LookbackWindow, t]; appearances before Start count toward the
// lookback. Per Bin with active appearances, the distinct count of
// active Ids. The exclusive lower bound is what the reference example
// needs (otherwise B's 2018-07-08 appearance is active); it
// reproduces exactly.
//
// new_activity_metrics: for each cohort period (Ids first seen in it,
// ignoring Ids in the Lookback table) and each period from it through
// End: dcount_new_values (Ids in the later period not seen in any
// period up to the cohort's; for the cohort's own period, the cohort
// size), dcount_retained_values, dcount_churn_values, retention_rate,
// churn_rate. Empty cohorts are not reported. Reference example 1
// reproduces exactly.

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["activity_counts_metrics"] = applyActivityCountsMetrics
	evaluatePlugins["activity_metrics"] = applyActivityMetrics
	evaluatePlugins["active_users_count"] = applyActiveUsersCount
	evaluatePlugins["new_activity_metrics"] = applyNewActivityMetrics
}

// periodBinner maps timeline values to period indexes: period k
// starts at Start + k*Step (or the k-th calendar period from the one
// holding Start). Integer timelines (datetime nanoseconds, timespan
// ticks, long, int) use exact int64 arithmetic; float64 cannot hold a
// datetime to the tick.
type periodBinner struct {
	axis          types.KQLType
	isReal        bool
	si, ei, stepi int64
	sf, ef, stepf float64
	calendar      string // "week", "month", "year"
	cal0          time.Time
	kFirst, kLast int64
}

// axisInt converts a scalar to an integer timeline's units (a timespan
// on a datetime timeline is scaled from ticks to nanoseconds).
func axisInt(v types.Value, vt, axis types.KQLType) int64 {
	n := types.ToInt64(v)
	if f, ok := v.(float64); ok {
		n = int64(math.Round(f))
	}
	if axis == types.TypeDatetime && vt == types.TypeTimespan {
		return n * 100
	}
	return n
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func newBinner(axis types.KQLType, start, end, step types.Value, st, et, pt types.KQLType) (*periodBinner, error) {
	b := &periodBinner{axis: axis, isReal: axis == types.TypeReal}
	if s, ok := step.(string); ok {
		s = strings.ToLower(s)
		if s != "week" && s != "month" && s != "year" {
			return nil, fmt.Errorf("step string must be week, month or year")
		}
		if axis != types.TypeDatetime {
			return nil, fmt.Errorf("a %s step needs a datetime timeline", s)
		}
		b.calendar = s
		b.si, b.ei = axisInt(start, st, axis), axisInt(end, et, axis)
		b.cal0 = calendarStart(time.Unix(0, b.si).UTC(), s)
		if b.cal0.UnixNano() < b.si {
			b.kFirst = 1 // the period holding Start began before it
		}
		b.kLast = b.indexInt(b.ei)
		return b, nil
	}
	if b.isReal {
		b.sf, b.ef, b.stepf = types.ToFloat64(start), types.ToFloat64(end), types.ToFloat64(step)
		if b.stepf <= 0 {
			return nil, fmt.Errorf("step must be positive")
		}
		b.kLast = int64(math.Floor((b.ef - b.sf) / b.stepf))
		return b, nil
	}
	b.si, b.ei, b.stepi = axisInt(start, st, axis), axisInt(end, et, axis), axisInt(step, pt, axis)
	if b.stepi <= 0 {
		return nil, fmt.Errorf("step must be positive")
	}
	b.kLast = floorDiv(b.ei-b.si, b.stepi)
	return b, nil
}

func calendarStart(t time.Time, cal string) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	switch cal {
	case "week":
		return d.AddDate(0, 0, -int(d.Weekday()))
	case "month":
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(t.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
}

func (b *periodBinner) indexInt(x int64) int64 {
	if b.calendar == "" {
		return floorDiv(x-b.si, b.stepi)
	}
	c := calendarStart(time.Unix(0, x).UTC(), b.calendar)
	switch b.calendar {
	case "week":
		return int64(math.Round(c.Sub(b.cal0).Hours() / (24 * 7)))
	case "month":
		return int64((c.Year()-b.cal0.Year())*12 + int(c.Month()) - int(b.cal0.Month()))
	}
	return int64(c.Year() - b.cal0.Year())
}

// index is the period holding timeline value v.
func (b *periodBinner) index(v types.Value) int64 {
	if b.isReal {
		return int64(math.Floor((types.ToFloat64(v) - b.sf) / b.stepf))
	}
	return b.indexInt(types.ToInt64(v))
}

// value is the start of period k, as a timeline value.
func (b *periodBinner) value(k int64) types.Value {
	switch {
	case b.isReal:
		return b.sf + float64(k)*b.stepf
	case b.calendar != "":
		var t time.Time
		switch b.calendar {
		case "week":
			t = b.cal0.AddDate(0, 0, 7*int(k))
		case "month":
			t = b.cal0.AddDate(0, int(k), 0)
		default:
			t = b.cal0.AddDate(int(k), 0, 0)
		}
		return t.UnixNano()
	case b.axis == types.TypeInt:
		return int32(b.si + k*b.stepi)
	}
	return b.si + k*b.stepi
}

func (b *periodBinner) inRange(k int64) bool { return k >= b.kFirst && k <= b.kLast }

// newPeriodBinner builds a binner from plugin argument texts.
func newPeriodBinner(plugin string, axis types.KQLType, startArg, endArg, stepArg string) (*periodBinner, error) {
	sv, st, err := pluginScalar(startArg)
	if err != nil || sv == nil {
		return nil, fmt.Errorf("%s: Start: invalid value", plugin)
	}
	ev, et, err := pluginScalar(endArg)
	if err != nil || ev == nil {
		return nil, fmt.Errorf("%s: End: invalid value", plugin)
	}
	pv, pt, err := pluginScalar(stepArg)
	if err != nil || pv == nil {
		return nil, fmt.Errorf("%s: step: invalid value", plugin)
	}
	b, err := newBinner(axis, sv, ev, pv, st, et, pt)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", plugin, err)
	}
	return b, nil
}

func idKey(v types.Value) string { return fmt.Sprintf("%T:%v", v, v) }

// activityInput is the parsed common prefix of the activity plugins.
type activityInput struct {
	in          *types.Table
	idCol, tCol int
	dims        []int
}

func (a *activityInput) groups() (order []string, rowsBy map[string][]types.Row) {
	rowsBy = map[string][]types.Row{}
	for _, r := range a.in.Rows {
		if r[a.tCol] == nil {
			continue
		}
		k := dimKey(r, a.dims)
		if _, ok := rowsBy[k]; !ok {
			order = append(order, k)
		}
		rowsBy[k] = append(rowsBy[k], r)
	}
	return
}

func (a *activityInput) dimRow(r types.Row) types.Row {
	dr := make(types.Row, len(a.dims))
	for i, d := range a.dims {
		dr[i] = r[d]
	}
	return dr
}

func (a *activityInput) schema(lead []types.Column, tail ...types.Column) types.Schema {
	s := types.Schema{Columns: append([]types.Column(nil), lead...)}
	for _, d := range a.dims {
		s.Columns = append(s.Columns, a.in.Schema.Columns[d])
	}
	s.Columns = append(s.Columns, tail...)
	return s
}

func parseActivityInput(plugin string, in *types.Table, args []string, dimArgs []string) (*activityInput, error) {
	idCol, err := pluginColumn(in, plugin, "id", args[0])
	if err != nil {
		return nil, err
	}
	tCol, err := pluginColumn(in, plugin, "timeline", args[1])
	if err != nil {
		return nil, err
	}
	dims, err := dimColumns(in, plugin, dimArgs)
	if err != nil {
		return nil, err
	}
	return &activityInput{in: in, idCol: idCol, tCol: tCol, dims: dims}, nil
}

// periodSets collects, per period in range, the rows' Ids.
func periodSets(a *activityInput, b *periodBinner, rows []types.Row) (map[int64]map[string]bool, map[int64]int64) {
	sets := map[int64]map[string]bool{}
	counts := map[int64]int64{}
	for _, r := range rows {
		l := b.index(r[a.tCol])
		if !b.inRange(l) {
			continue
		}
		counts[l]++
		if sets[l] == nil {
			sets[l] = map[string]bool{}
		}
		if r[a.idCol] != nil {
			sets[l][idKey(r[a.idCol])] = true
		}
	}
	return sets, counts
}

func sortedKeys(m map[int64]map[string]bool) []int64 {
	ks := make([]int64, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
	return ks
}

func applyActivityCountsMetrics(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	const name = "activity_counts_metrics"
	if len(args) < 5 {
		return nil, fmt.Errorf("%s(IdColumn, TimelineColumn, Start, End, Step [, dim...]) needs at least 5 arguments", name)
	}
	a, err := parseActivityInput(name, in, args, args[5:])
	if err != nil {
		return nil, err
	}
	b, err := newPeriodBinner(name, in.Schema.Columns[a.tCol].Type, args[2], args[3], args[4])
	if err != nil {
		return nil, err
	}
	long := func(n string) types.Column { return types.Column{Name: n, Type: types.TypeLong} }
	out := &types.Table{Schema: a.schema([]types.Column{in.Schema.Columns[a.tCol]}, long("count"), long("dcount"), long("new_dcount"), long("aggregated_dcount"))}
	var pos []float64
	order, rowsBy := a.groups()
	for _, k := range order {
		rows := rowsBy[k]
		sets, counts := periodSets(a, b, rows)
		seen := map[string]bool{}
		for _, l := range sortedKeys(sets) {
			var newN int64
			for id := range sets[l] {
				if !seen[id] {
					seen[id] = true
					newN++
				}
			}
			row := types.Row{b.value(l)}
			row = append(row, a.dimRow(rows[0])...)
			row = append(row, counts[l], int64(len(sets[l])), newN, int64(len(seen)))
			out.Rows = append(out.Rows, row)
			pos = append(pos, float64(l))
		}
	}
	sortDimRows(out.Rows, 1, len(a.dims), pos)
	return out, nil
}

func applyActivityMetrics(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	const name = "activity_metrics"
	if len(args) < 5 {
		return nil, fmt.Errorf("%s(IdColumn, TimelineColumn, Start, End, Step [, dim...]) needs at least 5 arguments", name)
	}
	a, err := parseActivityInput(name, in, args, args[5:])
	if err != nil {
		return nil, err
	}
	b, err := newPeriodBinner(name, in.Schema.Columns[a.tCol].Type, args[2], args[3], args[4])
	if err != nil {
		return nil, err
	}
	out := &types.Table{Schema: a.schema([]types.Column{in.Schema.Columns[a.tCol]},
		types.Column{Name: "dcount_values", Type: types.TypeLong}, types.Column{Name: "dcount_newvalues", Type: types.TypeLong},
		types.Column{Name: "retention_rate", Type: types.TypeReal}, types.Column{Name: "churn_rate", Type: types.TypeReal})}
	var pos []float64
	order, rowsBy := a.groups()
	for _, k := range order {
		rows := rowsBy[k]
		sets, _ := periodSets(a, b, rows)
		for _, l := range sortedKeys(sets) {
			cur := sets[l]
			prev := sets[l-1]
			var retained int64
			for id := range cur {
				if prev[id] {
					retained++
				}
			}
			newN := int64(len(cur)) - retained
			retention, churn := math.NaN(), math.NaN()
			if len(prev) > 0 {
				retention = float64(retained) / float64(len(prev))
				churn = 1 - retention
			}
			row := types.Row{b.value(l)}
			row = append(row, a.dimRow(rows[0])...)
			row = append(row, int64(len(cur)), newN, retention, churn)
			out.Rows = append(out.Rows, row)
			pos = append(pos, float64(l))
		}
	}
	sortDimRows(out.Rows, 1, len(a.dims), pos)
	return out, nil
}

func applyActiveUsersCount(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	const name = "active_users_count"
	if len(args) < 8 {
		return nil, fmt.Errorf("%s(IdColumn, TimelineColumn, Start, End, LookbackWindow, Period, ActivePeriodsCount, Bin [, dim...]) needs at least 8 arguments", name)
	}
	a, err := parseActivityInput(name, in, args, args[8:])
	if err != nil {
		return nil, err
	}
	axis := in.Schema.Columns[a.tCol].Type
	b, err := newPeriodBinner(name, axis, args[2], args[3], args[7])
	if err != nil {
		return nil, err
	}
	var sv [3]types.Value
	var stypes [3]types.KQLType
	for i, arg := range args[4:7] {
		v, vt, err := pluginScalar(arg)
		if err != nil || v == nil {
			return nil, fmt.Errorf("%s: argument %d: invalid value", name, i+5)
		}
		sv[i], stypes[i] = v, vt
	}
	need := int(math.Ceil(types.ToFloat64(sv[2])))
	// within reports whether u lies in (t - lookback, t]; periodOf is
	// u's Period index. Exact int64 arithmetic for integer timelines.
	var within func(u, t types.Value) bool
	var periodOf func(u types.Value) int64
	if axis == types.TypeReal {
		lb, per := types.ToFloat64(sv[0]), types.ToFloat64(sv[1])
		if per <= 0 {
			return nil, fmt.Errorf("%s: Period must be positive", name)
		}
		within = func(u, t types.Value) bool {
			uf, tf := types.ToFloat64(u), types.ToFloat64(t)
			return uf > tf-lb && uf <= tf
		}
		periodOf = func(u types.Value) int64 { return int64(math.Floor(types.ToFloat64(u) / per)) }
	} else {
		lb, per := axisInt(sv[0], stypes[0], axis), axisInt(sv[1], stypes[1], axis)
		if per <= 0 {
			return nil, fmt.Errorf("%s: Period must be positive", name)
		}
		within = func(u, t types.Value) bool {
			ui, ti := types.ToInt64(u), types.ToInt64(t)
			return ui > ti-lb && ui <= ti
		}
		periodOf = func(u types.Value) int64 { return floorDiv(types.ToInt64(u), per) }
	}
	out := &types.Table{Schema: a.schema([]types.Column{in.Schema.Columns[a.tCol]}, types.Column{Name: "dcount", Type: types.TypeLong})}
	var pos []float64
	order, rowsBy := a.groups()
	for _, k := range order {
		rows := rowsBy[k]
		byID := map[string][]types.Value{}
		for _, r := range rows {
			if r[a.idCol] == nil {
				continue
			}
			id := idKey(r[a.idCol])
			byID[id] = append(byID[id], r[a.tCol])
		}
		active := map[int64]map[string]bool{}
		for id, ts := range byID {
			for _, t := range ts {
				l := b.index(t)
				if !b.inRange(l) {
					continue
				}
				periods := map[int64]bool{}
				for _, u := range ts {
					if within(u, t) {
						periods[periodOf(u)] = true
					}
				}
				if len(periods) >= need {
					if active[l] == nil {
						active[l] = map[string]bool{}
					}
					active[l][id] = true
				}
			}
		}
		for _, l := range sortedKeys(active) {
			row := types.Row{b.value(l)}
			row = append(row, a.dimRow(rows[0])...)
			row = append(row, int64(len(active[l])))
			out.Rows = append(out.Rows, row)
			pos = append(pos, float64(l))
		}
	}
	sortDimRows(out.Rows, 1, len(a.dims), pos)
	return out, nil
}

func applyNewActivityMetrics(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	const name = "new_activity_metrics"
	if len(args) < 5 {
		return nil, fmt.Errorf("%s(IdColumn, TimelineColumn, Start, End, Window [, Cohort] [, dim...] [, Lookback]) needs at least 5 arguments", name)
	}
	idName := strings.TrimSpace(args[0])
	// Optional arguments: a column of the input is a dimension; a
	// constant is the Cohort; anything else is the Lookback table.
	var dimArgs []string
	var cohortVal types.Value
	var cohortType types.KQLType
	var lookback map[string]bool
	axis := types.KQLType(0)
	if i := in.Schema.ColumnIndex(strings.TrimSpace(args[1])); i >= 0 {
		axis = in.Schema.Columns[i].Type
	}
	for _, arg := range args[5:] {
		arg = strings.TrimSpace(arg)
		if in.Schema.ColumnIndex(arg) >= 0 {
			dimArgs = append(dimArgs, arg)
			continue
		}
		if v, vt, err := pluginScalar(arg); err == nil {
			cohortVal, cohortType = v, vt
			continue
		}
		stmt, err := parser.Parse(arg)
		if err != nil {
			return nil, fmt.Errorf("%s: argument %q is not a column, constant or table: %w", name, arg, err)
		}
		lt, err := e.Execute(stmt)
		if err != nil {
			return nil, fmt.Errorf("%s: Lookback: %w", name, err)
		}
		c := lt.Schema.ColumnIndex(idName)
		if c < 0 {
			if len(lt.Schema.Columns) != 1 {
				return nil, fmt.Errorf("%s: Lookback table has no %q column", name, idName)
			}
			c = 0
		}
		lookback = map[string]bool{}
		for _, r := range lt.Rows {
			if r[c] != nil {
				lookback[idKey(r[c])] = true
			}
		}
	}
	a, err := parseActivityInput(name, in, args, dimArgs)
	if err != nil {
		return nil, err
	}
	b, err := newPeriodBinner(name, axis, args[2], args[3], args[4])
	if err != nil {
		return nil, err
	}
	tc := in.Schema.Columns[a.tCol]
	long := func(n string) types.Column { return types.Column{Name: n, Type: types.TypeLong} }
	real := func(n string) types.Column { return types.Column{Name: n, Type: types.TypeReal} }
	out := &types.Table{Schema: a.schema(
		[]types.Column{{Name: "from_" + tc.Name, Type: tc.Type}, {Name: "to_" + tc.Name, Type: tc.Type}},
		long("dcount_new_values"), long("dcount_retained_values"), long("dcount_churn_values"),
		real("retention_rate"), real("churn_rate"))}
	var periods []int64
	for k := b.kFirst; k <= b.kLast && len(periods) < 1_000_000; k++ {
		periods = append(periods, k)
	}
	cohortK := int64(math.MinInt64)
	if cohortVal != nil {
		cohortK = b.index(axisCohort(cohortVal, cohortType, axis))
	}
	order, rowsBy := a.groups()
	for _, k := range order {
		rows := rowsBy[k]
		sets, _ := periodSets(a, b, rows)
		seenBefore := map[string]bool{} // Ids in periods before the current cohort
		for id := range lookback {
			seenBefore[id] = true
		}
		for fi, from := range periods {
			cohortIDs := map[string]bool{}
			for id := range sets[from] {
				if !seenBefore[id] {
					cohortIDs[id] = true
				}
			}
			throughFrom := map[string]bool{}
			for id := range seenBefore {
				throughFrom[id] = true
			}
			for id := range sets[from] {
				throughFrom[id] = true
			}
			if len(cohortIDs) > 0 && (cohortVal == nil || cohortK == from) {
				n := float64(len(cohortIDs))
				for _, to := range periods[fi:] {
					var newN, retained int64
					if to == from {
						newN = int64(len(cohortIDs))
					} else {
						for id := range sets[to] {
							if !throughFrom[id] {
								newN++
							}
						}
					}
					for id := range cohortIDs {
						if sets[to][id] {
							retained++
						}
					}
					churned := int64(len(cohortIDs)) - retained
					row := types.Row{b.value(from), b.value(to)}
					row = append(row, a.dimRow(rows[0])...)
					row = append(row, newN, retained, churned, float64(retained)/n, float64(churned)/n)
					out.Rows = append(out.Rows, row)
				}
			}
			seenBefore = throughFrom
		}
	}
	sortNewActivity(out.Rows, len(a.dims))
	return out, nil
}

// sortNewActivity orders rows by dimensions, then from, then to.
func sortNewActivity(rows []types.Row, nd int) {
	sort.SliceStable(rows, func(i, j int) bool {
		for d := 0; d < nd; d++ {
			if c := compareValues(rows[i][2+d], rows[j][2+d]); c != 0 {
				return c < 0
			}
		}
		if c := compareValues(rows[i][0], rows[j][0]); c != 0 {
			return c < 0
		}
		return compareValues(rows[i][1], rows[j][1]) < 0
	})
}

// axisCohort converts a Cohort argument to a timeline value.
func axisCohort(v types.Value, vt, axis types.KQLType) types.Value {
	if axis == types.TypeReal {
		return types.ToFloat64(v)
	}
	return axisInt(v, vt, axis)
}
