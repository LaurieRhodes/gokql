package engine

// plugin_pivot.go — evaluate pivot and evaluate narrow.
//
//	T | evaluate pivot(PivotColumn [, AggregationFunction] [, Column1, ...]) [: OutputSchema]
//
// Each distinct value of PivotColumn (as a string) becomes a column
// holding AggregationFunction (default count()) over the rows of that
// value and of the row-identifier columns. The identifier columns
// default to every column except the pivot column and the columns the
// aggregation references. Output: identifier columns, then pivoted
// columns in order of first appearance. A cell with no input rows is 0
// for count/countif/dcount/sum/sumif (the reference examples show 0),
// an empty array or bag for make_list/make_set/make_bag, and null
// otherwise. With an OutputSchema, the output is exactly that schema;
// a schema column the data did not produce is filled the same way.
//
//	T | evaluate narrow()
//
// One row per input cell: Row (0-based input row number, long),
// Column (column name), Value (tostring of the cell), in input row
// then column order.

import (
	"fmt"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["pivot"] = applyPivotPlugin
	evaluatePlugins["narrow"] = applyNarrowPlugin
}

func bracketName(n string) string {
	return "['" + strings.ReplaceAll(n, "'", "\\'") + "']"
}

func applyPivotPlugin(e *Engine, input *types.Table, args []string, outSchema *types.Schema) (*types.Table, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return nil, fmt.Errorf("pivot(PivotColumn [, AggregationFunction] [, Column1, ...]) needs a pivot column")
	}
	pivotCol := strings.TrimSpace(args[0])
	if input.Schema.ColumnIndex(pivotCol) < 0 {
		return nil, fmt.Errorf("pivot: column %q not found", pivotCol)
	}
	pivotCol = input.Schema.Columns[input.Schema.ColumnIndex(pivotCol)].Name

	aggText := "count()"
	rest := args[1:]
	aggName := "count"
	aggRefs := map[string]bool{}
	if len(rest) > 0 {
		if ex, err := parser.ParseExpr(strings.TrimSpace(rest[0])); err == nil {
			if fc, ok := ex.(*parser.FuncCall); ok {
				aggText = strings.TrimSpace(rest[0])
				aggName = fc.Name
				collectColumnRefs(ex, aggRefs)
				rest = rest[1:]
			}
		}
	}
	var keys []string
	if len(rest) > 0 {
		for _, a := range rest {
			n := strings.TrimSpace(a)
			i := input.Schema.ColumnIndex(n)
			if i < 0 {
				return nil, fmt.Errorf("pivot: column %q not found", n)
			}
			keys = append(keys, input.Schema.Columns[i].Name)
		}
	} else {
		for _, c := range input.Schema.Columns {
			if c.Name != pivotCol && !aggRefs[c.Name] {
				keys = append(keys, c.Name)
			}
		}
	}

	by := make([]string, 0, len(keys)+1)
	for _, k := range keys {
		by = append(by, bracketName(k))
	}
	by = append(by, "__pivot_value = tostring("+bracketName(pivotCol)+")")
	stmt, err := parser.Parse("__pivot_input | summarize __pivot_agg = " + aggText + " by " + strings.Join(by, ", "))
	if err != nil {
		return nil, fmt.Errorf("pivot: %w", err)
	}
	q, ok := stmt.(*parser.Query)
	if !ok {
		return nil, fmt.Errorf("pivot: internal: unexpected statement %T", stmt)
	}
	agg, err := e.applyPipeline(input, q.Operators)
	if err != nil {
		return nil, fmt.Errorf("pivot: %w", err)
	}
	nk := len(keys)
	aggIdx := agg.Schema.ColumnIndex("__pivot_agg")
	valIdx := agg.Schema.ColumnIndex("__pivot_value")
	aggType := agg.Schema.Columns[aggIdx].Type
	fill := pivotFill(aggName, aggType)

	// Pivot columns in order of first appearance; row groups likewise.
	var pivNames []string
	pivPos := map[string]int{}
	type group struct {
		keys  types.Row
		cells map[int]types.Value
	}
	var groups []*group
	groupPos := map[string]int{}
	for _, r := range agg.Rows {
		pv, _ := r[valIdx].(string)
		p, ok := pivPos[pv]
		if !ok {
			p = len(pivNames)
			pivPos[pv] = p
			pivNames = append(pivNames, pv)
		}
		gk := fmt.Sprintf("%#v", []types.Value(r[:nk]))
		g, ok := groupPos[gk]
		if !ok {
			g = len(groups)
			groupPos[gk] = g
			groups = append(groups, &group{keys: append(types.Row(nil), r[:nk]...), cells: map[int]types.Value{}})
		}
		groups[g].cells[p] = r[aggIdx]
	}

	schema := types.Schema{}
	used := map[string]bool{}
	for i := 0; i < nk; i++ {
		schema.Columns = append(schema.Columns, agg.Schema.Columns[i])
		used[strings.ToLower(agg.Schema.Columns[i].Name)] = true
	}
	for _, pv := range pivNames {
		name := pv
		if name == "" {
			name = "Column1"
		}
		for used[strings.ToLower(name)] {
			name += "1"
		}
		used[strings.ToLower(name)] = true
		schema.Columns = append(schema.Columns, types.Column{Name: name, Type: aggType})
	}
	out := &types.Table{Schema: schema}
	for _, g := range groups {
		row := make(types.Row, 0, len(schema.Columns))
		row = append(row, g.keys...)
		for p := range pivNames {
			if v, ok := g.cells[p]; ok {
				row = append(row, v)
			} else {
				row = append(row, fill)
			}
		}
		out.Rows = append(out.Rows, row)
	}
	if outSchema == nil {
		return out, nil
	}
	return conformToSchema(out, outSchema, func(t types.KQLType) types.Value { return pivotFill(aggName, t) }), nil
}

// pivotFill is the value of a pivoted cell with no input rows.
func pivotFill(aggName string, t types.KQLType) types.Value {
	switch aggName {
	case "count", "countif", "dcount", "dcountif", "sum", "sumif":
		switch t {
		case types.TypeLong:
			return int64(0)
		case types.TypeInt:
			return int32(0)
		case types.TypeReal:
			return float64(0)
		}
	case "make_list", "make_set", "make_list_if", "make_set_if", "make_list_with_nulls":
		return "[]"
	case "make_bag", "make_bag_if":
		return "{}"
	}
	return nil
}

// conformToSchema projects t onto the declared schema, matching
// columns by name (case-insensitive) and converting values to the
// declared type; declared columns t lacks are filled by fill.
func conformToSchema(t *types.Table, s *types.Schema, fill func(types.KQLType) types.Value) *types.Table {
	src := make([]int, len(s.Columns))
	for i, c := range s.Columns {
		src[i] = t.Schema.ColumnIndex(c.Name)
	}
	out := &types.Table{Schema: types.Schema{Columns: append([]types.Column(nil), s.Columns...)}}
	for _, r := range t.Rows {
		row := make(types.Row, len(s.Columns))
		for i, c := range s.Columns {
			if src[i] < 0 {
				row[i] = fill(c.Type)
				continue
			}
			v := r[src[i]]
			if v != nil && t.Schema.Columns[src[i]].Type != c.Type {
				v = coerceToDeclared(v, c.Type)
			}
			row[i] = v
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

func applyNarrowPlugin(e *Engine, input *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	if len(args) > 1 || (len(args) == 1 && strings.TrimSpace(args[0]) != "") {
		return nil, fmt.Errorf("narrow() takes no arguments")
	}
	out := &types.Table{Schema: types.Schema{Columns: []types.Column{
		{Name: "Row", Type: types.TypeLong},
		{Name: "Column", Type: types.TypeString},
		{Name: "Value", Type: types.TypeString},
	}}}
	calls := make([]*parser.FuncCall, len(input.Schema.Columns))
	for j, c := range input.Schema.Columns {
		calls[j] = &parser.FuncCall{Name: "tostring", RawName: "tostring", Args: []parser.Expr{&parser.ColumnRef{Name: c.Name}}}
	}
	for i, r := range input.Rows {
		for j, c := range input.Schema.Columns {
			v, err := evalExpr(e, calls[j], &input.Schema, r)
			if err != nil {
				return nil, fmt.Errorf("narrow: %w", err)
			}
			out.Rows = append(out.Rows, types.Row{int64(i), c.Name, v})
		}
	}
	return out, nil
}
