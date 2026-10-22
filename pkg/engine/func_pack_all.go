package engine

// func_pack_all.go — pack_all([ignore_null_empty]): a property bag of
// every input column, keys in column order. With ignore_null_empty
// true, null values and empty strings are left out (pack_all
// reference). Called inside extend it sees the extend's input columns,
// not columns the same extend adds.

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func evalPackAll(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if fc.Name != "pack_all" {
		return nil, false, nil
	}
	if len(fc.Args) > 1 {
		return nil, true, fmt.Errorf("pack_all takes at most 1 argument (ignore_null_empty)")
	}
	ignore := false
	if len(fc.Args) == 1 {
		v, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		b, ok := v.(bool)
		if !ok {
			return nil, true, fmt.Errorf("pack_all: ignore_null_empty must be a bool, got %v", v)
		}
		ignore = b
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for i, col := range schema.Columns {
		var v types.Value
		if i < len(row) {
			v = row[i]
		}
		if ignore {
			if s, ok := v.(string); v == nil || (ok && s == "") {
				continue
			}
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		k, _ := json.Marshal(col.Name)
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(columnValueJSON(v, col.Type))
	}
	buf.WriteByte('}')
	return buf.String(), true, nil
}

// columnValueJSON encodes one cell as a JSON value: dynamic cells are
// embedded as the JSON they hold, other types through
// valueForJSONArray (datetime as ISO-8601, numbers as numbers).
func columnValueJSON(v types.Value, t types.KQLType) []byte {
	if v == nil {
		return []byte("null")
	}
	if t == types.TypeDynamic {
		if s, ok := v.(string); ok && json.Valid([]byte(s)) {
			return compactJSON(json.RawMessage(s))
		}
	}
	b, err := json.Marshal(valueForJSONArray(v, t))
	if err != nil {
		return []byte("null")
	}
	return b
}
