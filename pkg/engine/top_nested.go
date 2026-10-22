package engine

// top_nested.go — top-nested: hierarchical top-N.
//
// Each level groups the rows of its parent group by the level's
// expression, aggregates every group, keeps the top N groups by the
// aggregate (descending unless asc; ties keep first-seen order; N
// omitted keeps all) and descends into each kept group. Output rows
// list, per level, the value and its aggregate, in hierarchy order.
//
// with others = C adds, for the groups a level did not keep, one row
// whose value is C and whose aggregate is taken over all of their
// rows. The reference does not define deeper levels under that row;
// they are null here.

import (
	"fmt"
	"sort"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func (e *Engine) applyTopNested(input *types.Table, op *parser.TopNestedOp) (*types.Table, error) {
	var cols []types.Column
	for _, l := range op.Levels {
		cols = append(cols,
			types.Column{Name: l.Name, Type: inferExprType(e, l.Expr, &input.Schema)},
			types.Column{Name: l.Agg.Name, Type: inferAggType(e, l.Agg.Function, l.Agg.Args, &input.Schema)})
	}
	out := types.NewTable(input.Name, types.Schema{Columns: cols})
	rows, err := e.topNestedLevel(input, op.Levels, 0, input.Rows)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out.AddRow(r)
	}
	return out, nil
}

type topNestedGroup struct {
	key  types.Value
	rows []types.Row
	agg  types.Value
}

func (e *Engine) topNestedLevel(input *types.Table, levels []parser.TopNestedLevel, li int, rows []types.Row) ([]types.Row, error) {
	if li == len(levels) {
		return []types.Row{{}}, nil
	}
	lvl := levels[li]
	schema := &input.Schema

	index := map[string]int{}
	var groups []*topNestedGroup
	for _, r := range rows {
		k, err := evalExpr(e, lvl.Expr, schema, r)
		if err != nil {
			return nil, fmt.Errorf("top-nested %s: %w", lvl.Name, err)
		}
		gk := fmt.Sprintf("%T\x00%v", k, k)
		gi, ok := index[gk]
		if !ok {
			gi = len(groups)
			index[gk] = gi
			groups = append(groups, &topNestedGroup{key: k})
		}
		groups[gi].rows = append(groups[gi].rows, r)
	}
	aggType := inferAggType(e, lvl.Agg.Function, lvl.Agg.Args, schema)
	for _, g := range groups {
		v, err := computeAgg(e, lvl.Agg, g.rows, schema)
		if err != nil {
			return nil, fmt.Errorf("top-nested %s: %w", lvl.Agg.Name, err)
		}
		g.agg = v
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i].agg, groups[j].agg
		if a == nil || b == nil {
			return a != nil // nulls last
		}
		c := types.CompareValues(a, b, aggType)
		if lvl.Asc {
			return c < 0
		}
		return c > 0
	})
	kept, rest := groups, []*topNestedGroup(nil)
	if lvl.N > 0 && int64(len(groups)) > lvl.N {
		kept, rest = groups[:lvl.N], groups[lvl.N:]
	}

	deeper := 2 * (len(levels) - li - 1)
	var out []types.Row
	for _, g := range kept {
		subs, err := e.topNestedLevel(input, levels, li+1, g.rows)
		if err != nil {
			return nil, err
		}
		for _, s := range subs {
			out = append(out, append(types.Row{g.key, g.agg}, s...))
		}
	}
	if lvl.HasOthers && len(rest) > 0 {
		var otherRows []types.Row
		for _, g := range rest {
			otherRows = append(otherRows, g.rows...)
		}
		v, err := computeAgg(e, lvl.Agg, otherRows, schema)
		if err != nil {
			return nil, fmt.Errorf("top-nested %s: %w", lvl.Agg.Name, err)
		}
		c, err := evalExpr(e, lvl.Others, &types.Schema{}, types.Row{})
		if err != nil {
			return nil, fmt.Errorf("top-nested others: %w", err)
		}
		out = append(out, append(types.Row{c, v}, make(types.Row, deeper)...))
	}
	return out, nil
}
