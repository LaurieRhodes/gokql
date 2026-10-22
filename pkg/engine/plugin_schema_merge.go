package engine

// plugin_schema_merge.go — evaluate schema_merge([PreserveOrder]).
//
// The input is one or more getschema results stacked (usually by
// union); ordinals are per schema. Columns are merged in schema order: a column not seen before is
// inserted at its own ordinal (or appended when that is past the end),
// which reproduces all three reference examples. With PreserveOrder
// (default true), a column seen before must have the ordinal it had in
// the first schema it appeared in; otherwise it is reported as
// ColumnOrdinal -1 with ERROR(columns are out of order), after the
// valid columns, as in the reference. Types reduce int → long → real;
// any other mismatch is an ERROR row the same way (the reference does
// not give the reduction rules or that message's text).

import (
	"fmt"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["schema_merge"] = applySchemaMerge
}

func applySchemaMerge(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	preserve := true
	if len(args) > 1 {
		return nil, fmt.Errorf("schema_merge([PreserveOrder]) takes at most one argument")
	}
	if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
		a := strings.TrimSpace(args[0])
		if eq := strings.Index(a, "="); eq >= 0 {
			if !strings.EqualFold(strings.TrimSpace(a[:eq]), "PreserveOrder") {
				return nil, fmt.Errorf("schema_merge: unknown argument %q", strings.TrimSpace(a[:eq]))
			}
			a = strings.TrimSpace(a[eq+1:])
		}
		v, _, err := pluginScalar(a)
		b, ok := v.(bool)
		if err != nil || !ok {
			return nil, fmt.Errorf("schema_merge: PreserveOrder must be true or false")
		}
		preserve = b
	}
	nameCol, err := pluginColumn(in, "schema_merge", "getschema", "ColumnName")
	if err != nil {
		return nil, err
	}
	ordCol, err := pluginColumn(in, "schema_merge", "getschema", "ColumnOrdinal")
	if err != nil {
		return nil, err
	}
	typeCol, err := pluginColumn(in, "schema_merge", "getschema", "ColumnType")
	if err != nil {
		return nil, err
	}

	type col struct {
		name     string
		typ      types.KQLType
		firstOrd int64
		errMsg   string
	}
	var merged []*col
	byName := map[string]*col{}
	for _, r := range in.Rows {
		name, _ := r[nameCol].(string)
		ord := int64(types.ToFloat64(r[ordCol]))
		ts, _ := r[typeCol].(string)
		t, err := types.ParseType(ts)
		if err != nil {
			return nil, fmt.Errorf("schema_merge: column %q: %w", name, err)
		}
		c := byName[name]
		if c == nil {
			c = &col{name: name, typ: t, firstOrd: ord}
			byName[name] = c
			pos := int(ord)
			if pos < 0 || pos > len(merged) {
				pos = len(merged)
			}
			merged = append(merged, nil)
			copy(merged[pos+1:], merged[pos:])
			merged[pos] = c
			continue
		}
		if c.errMsg != "" {
			continue
		}
		if preserve && ord != c.firstOrd {
			c.errMsg = "columns are out of order"
			continue
		}
		if rt, ok := reduceSchemaType(c.typ, t); ok {
			c.typ = rt
		} else {
			c.errMsg = fmt.Sprintf("types %s and %s cannot be reduced", c.typ, t)
		}
	}

	out := &types.Table{Schema: types.Schema{Columns: []types.Column{
		{Name: "ColumnName", Type: types.TypeString},
		{Name: "ColumnOrdinal", Type: types.TypeLong},
		{Name: "DataType", Type: types.TypeString},
		{Name: "ColumnType", Type: types.TypeString},
	}}}
	n := int64(0)
	var bad []*col
	for _, c := range merged {
		if c.errMsg != "" {
			bad = append(bad, c)
			continue
		}
		out.Rows = append(out.Rows, types.Row{c.name, n, c.typ.DotNetTypeName(), c.typ.String()})
		n++
	}
	for _, c := range bad {
		msg := "ERROR(" + c.errMsg + ")"
		out.Rows = append(out.Rows, types.Row{c.name, int64(-1), "ERROR(unknown CSL type:" + msg + ")", msg})
	}
	return out, nil
}

// reduceSchemaType is the common type of a and b: equal types, or the
// wider of two numeric types (int < long < real).
func reduceSchemaType(a, b types.KQLType) (types.KQLType, bool) {
	if a == b {
		return a, true
	}
	rank := map[types.KQLType]int{types.TypeInt: 1, types.TypeLong: 2, types.TypeReal: 3}
	ra, rb := rank[a], rank[b]
	if ra == 0 || rb == 0 {
		return 0, false
	}
	if ra > rb {
		return a, true
	}
	return b, true
}
