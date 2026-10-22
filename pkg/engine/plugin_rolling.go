package engine

// plugin_rolling.go — windowed evaluate plugins:
//
//	T | evaluate sliding_window_counts(IdColumn, TimelineColumn, Start, End, LookbackWindow, Bin [, dim1, ...])
//
// Bins are aligned to Start (bin_at). The window of a bin is the
// LookbackWindow/Bin bins ending at it, inclusive (LookbackWindow is
// rounded down to a multiple of Bin). For each bin from Start to End
// whose window holds rows: Count (rows) and Dcount (distinct non-null
// Ids). The reference example reproduces exactly. Output columns and
// types are Kusto.Language's: TimelineColumn, dims, Count, Dcount.
//
//	T | evaluate rolling_percentile(ValueColumn, Percentile, IndexColumn, BinSize, BinsPerWindow [, dim1, ...])
//
// Rows are binned with bin(IndexColumn, BinSize); for each bin that has
// rows, the nearest-rank percentile of the values in the BinsPerWindow
// bins ending at it (inclusive). Column rolling_<N>_percentile_<Value>_<P>,
// typed as the value column. Both reference examples reproduce exactly,
// including their order: dims ascending, then bins ascending.
//
// The references do not say whether bins with an empty window are
// reported; they are not. A Bin string (week/month/year) is not
// supported.

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["sliding_window_counts"] = applySlidingWindowCounts
	evaluatePlugins["rolling_percentile"] = applyRollingPercentile
}

// pluginScalar evaluates a constant plugin argument (literals, let
// scalars, constant expressions) and returns it with its static type.
func pluginScalar(text string) (types.Value, types.KQLType, error) {
	expr, err := parser.ParseExpr(strings.TrimSpace(text))
	if err != nil {
		return nil, 0, err
	}
	empty := &types.Schema{}
	v, err := evalExpr(expr, empty, types.Row{})
	if err != nil {
		return nil, 0, err
	}
	return v, inferExprType(expr, empty), nil
}

func pluginColumn(in *types.Table, plugin, which, name string) (int, error) {
	i := in.Schema.ColumnIndex(strings.TrimSpace(name))
	if i < 0 {
		return 0, fmt.Errorf("%s: %s column %q not found", plugin, which, strings.TrimSpace(name))
	}
	return i, nil
}

// dimGroups resolves dimension columns and keys rows by their values.
func dimColumns(in *types.Table, plugin string, names []string) ([]int, error) {
	var idx []int
	for _, n := range names {
		i, err := pluginColumn(in, plugin, "dimension", n)
		if err != nil {
			return nil, err
		}
		idx = append(idx, i)
	}
	return idx, nil
}

func dimKey(r types.Row, dims []int) string {
	parts := make([]string, len(dims))
	for i, d := range dims {
		parts[i] = fmt.Sprintf("%T:%v", r[d], r[d])
	}
	return strings.Join(parts, "\x00")
}

// sortDimRows orders output rows by the dimension values ascending,
// then by the timeline position.
func sortDimRows(rows []types.Row, dimStart, nd int, pos []float64) {
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ra, rb := rows[idx[a]], rows[idx[b]]
		for d := 0; d < nd; d++ {
			if c := compareValues(ra[dimStart+d], rb[dimStart+d]); c != 0 {
				return c < 0
			}
		}
		return pos[idx[a]] < pos[idx[b]]
	})
	sorted := make([]types.Row, len(rows))
	for i, j := range idx {
		sorted[i] = rows[j]
	}
	copy(rows, sorted)
}

func compareValues(a, b types.Value) int {
	if a == nil || b == nil {
		switch {
		case a == nil && b == nil:
			return 0
		case a == nil:
			return -1
		}
		return 1
	}
	if sa, ok := a.(string); ok {
		sb, _ := b.(string)
		return strings.Compare(sa, sb)
	}
	if ba, ok := a.(bool); ok {
		bb, _ := b.(bool)
		switch {
		case ba == bb:
			return 0
		case !ba:
			return -1
		}
		return 1
	}
	fa, fb := types.ToFloat64(a), types.ToFloat64(b)
	switch {
	case fa < fb:
		return -1
	case fa > fb:
		return 1
	}
	return 0
}

func applySlidingWindowCounts(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	const name = "sliding_window_counts"
	if len(args) < 6 {
		return nil, fmt.Errorf("%s(IdColumn, TimelineColumn, Start, End, LookbackWindow, Bin [, dim1, ...]) needs at least 6 arguments", name)
	}
	idCol, err := pluginColumn(in, name, "id", args[0])
	if err != nil {
		return nil, err
	}
	tCol, err := pluginColumn(in, name, "timeline", args[1])
	if err != nil {
		return nil, err
	}
	axis := in.Schema.Columns[tCol].Type
	var sv [4]types.Value
	var st [4]types.KQLType
	for i, a := range args[2:6] {
		v, vt, err := pluginScalar(a)
		if err != nil {
			return nil, fmt.Errorf("%s: argument %d: %w", name, i+3, err)
		}
		if _, isStr := v.(string); isStr || v == nil {
			return nil, fmt.Errorf("%s: argument %d must be a number, datetime or timespan (Bin strings week/month/year are not supported)", name, i+3)
		}
		sv[i], st[i] = v, vt
	}
	b, err := newBinner(axis, sv[0], sv[1], sv[3], st[0], st[1], st[3])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var k int64 // LookbackWindow in bins, rounded down
	if axis == types.TypeReal {
		k = int64(math.Floor(types.ToFloat64(sv[2]) / b.stepf))
	} else {
		k = axisInt(sv[2], st[2], axis) / b.stepi
	}
	if k < 1 {
		k = 1
	}
	dims, err := dimColumns(in, name, args[6:])
	if err != nil {
		return nil, err
	}
	type cell struct {
		dimRow types.Row
		bin    int64
		count  int64
		ids    map[string]bool
	}
	cells := map[string]*cell{}
	for _, r := range in.Rows {
		if r[tCol] == nil {
			continue
		}
		bi := b.index(r[tCol])
		dk := dimKey(r, dims)
		for w := bi; w < bi+k; w++ {
			if !b.inRange(w) {
				continue
			}
			key := dk + "\x01" + strconv.FormatInt(w, 10)
			c := cells[key]
			if c == nil {
				dr := make(types.Row, len(dims))
				for i, d := range dims {
					dr[i] = r[d]
				}
				c = &cell{dimRow: dr, bin: w, ids: map[string]bool{}}
				cells[key] = c
			}
			c.count++
			if r[idCol] != nil {
				c.ids[fmt.Sprintf("%T:%v", r[idCol], r[idCol])] = true
			}
		}
	}
	schema := types.Schema{Columns: []types.Column{in.Schema.Columns[tCol]}}
	for _, d := range dims {
		schema.Columns = append(schema.Columns, in.Schema.Columns[d])
	}
	schema.Columns = append(schema.Columns, types.Column{Name: "Count", Type: types.TypeLong}, types.Column{Name: "Dcount", Type: types.TypeLong})
	out := &types.Table{Schema: schema}
	var pos []float64
	for _, c := range cells {
		row := types.Row{b.value(c.bin)}
		row = append(row, c.dimRow...)
		row = append(row, c.count, int64(len(c.ids)))
		out.Rows = append(out.Rows, row)
		pos = append(pos, float64(c.bin))
	}
	sortDimRows(out.Rows, 1, len(dims), pos)
	return out, nil
}

func applyRollingPercentile(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	const name = "rolling_percentile"
	if len(args) < 5 {
		return nil, fmt.Errorf("%s(ValueColumn, Percentile, IndexColumn, BinSize, BinsPerWindow [, dim1, ...]) needs at least 5 arguments", name)
	}
	vCol, err := pluginColumn(in, name, "value", args[0])
	if err != nil {
		return nil, err
	}
	pv, _, err := pluginScalar(args[1])
	if err != nil || pv == nil {
		return nil, fmt.Errorf("%s: Percentile: must be a number", name)
	}
	pct := types.ToFloat64(pv)
	iCol, err := pluginColumn(in, name, "index", args[2])
	if err != nil {
		return nil, err
	}
	axis := in.Schema.Columns[iCol].Type
	bv, bt, err := pluginScalar(args[3])
	if err != nil || bv == nil {
		return nil, fmt.Errorf("%s: BinSize: must be a number or timespan", name)
	}
	var zero types.Value = int64(0)
	if axis == types.TypeReal {
		zero = 0.0
	}
	// bin(IndexColumn, BinSize): periods aligned at zero
	bn, err := newBinner(axis, zero, zero, bv, axis, axis, bt)
	if err != nil {
		return nil, fmt.Errorf("%s: BinSize: %w", name, err)
	}
	nv, _, err := pluginScalar(args[4])
	if err != nil || nv == nil {
		return nil, fmt.Errorf("%s: BinsPerWindow: must be an integer", name)
	}
	n := int64(types.ToFloat64(nv))
	if n < 1 {
		return nil, fmt.Errorf("%s: BinsPerWindow must be at least 1", name)
	}
	dims, err := dimColumns(in, name, args[5:])
	if err != nil {
		return nil, err
	}

	type group struct {
		dimRow types.Row
		bins   map[int64][]types.Value
	}
	groups := map[string]*group{}
	var order []string
	for _, r := range in.Rows {
		if r[iCol] == nil || r[vCol] == nil {
			continue
		}
		b := bn.index(r[iCol])
		dk := dimKey(r, dims)
		g := groups[dk]
		if g == nil {
			dr := make(types.Row, len(dims))
			for i, d := range dims {
				dr[i] = r[d]
			}
			g = &group{dimRow: dr, bins: map[int64][]types.Value{}}
			groups[dk] = g
			order = append(order, dk)
		}
		v := r[vCol]
		if x, ok := v.(int32); ok {
			v = int64(x)
		}
		g.bins[b] = append(g.bins[b], v)
	}

	valType := in.Schema.Columns[vCol].Type
	if valType == types.TypeInt {
		valType = types.TypeLong
	}
	pctName := strings.ReplaceAll(strconv.FormatFloat(pct, 'f', -1, 64), ".", "_")
	colName := fmt.Sprintf("rolling_%d_percentile_%s_%s", n, in.Schema.Columns[vCol].Name, pctName)
	schema := types.Schema{Columns: []types.Column{in.Schema.Columns[iCol]}}
	for _, d := range dims {
		schema.Columns = append(schema.Columns, in.Schema.Columns[d])
	}
	schema.Columns = append(schema.Columns, types.Column{Name: colName, Type: valType})
	out := &types.Table{Schema: schema}
	var pos []float64
	for _, dk := range order {
		g := groups[dk]
		for b := range g.bins {
			var window []types.Value
			for w := b - n + 1; w <= b; w++ {
				window = append(window, g.bins[w]...)
			}
			row := types.Row{bn.value(b)}
			row = append(row, g.dimRow...)
			row = append(row, nearestRankPercentile(window, pct))
			out.Rows = append(out.Rows, row)
			pos = append(pos, float64(b))
		}
	}
	sortDimRows(out.Rows, 1, len(dims), pos)
	return out, nil
}

// nearestRankPercentile is the smallest value with at least p% of
// vals less than or equal to it (p = 0 is the minimum); nil if empty.
func nearestRankPercentile(vals []types.Value, p float64) types.Value {
	if len(vals) == 0 {
		return nil
	}
	s := append([]types.Value(nil), vals...)
	sort.SliceStable(s, func(i, j int) bool { return types.ToFloat64(s[i]) < types.ToFloat64(s[j]) })
	rank := int(math.Ceil(p / 100 * float64(len(s))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(s) {
		rank = len(s)
	}
	return s[rank-1]
}
