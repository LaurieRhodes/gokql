package main

// json_out.go — -mode json / jsonl rendering.
//
// Keys follow the result's column order, not alphabetical order.
// Values are typed by column: numbers and bools as JSON numbers and
// bools; string, guid, datetime, timespan and decimal as strings in the
// same text the table and csv modes print; dynamic embedded as JSON;
// null as null. Non-finite reals are the strings "NaN", "Infinity" and
// "-Infinity", since JSON has no number for them.

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

func appendJSONRow(b *bytes.Buffer, cols []types.Column, row types.Row) {
	b.WriteByte('{')
	for i, col := range cols {
		if i > 0 {
			b.WriteByte(',')
		}
		appendJSONString(b, col.Name)
		b.WriteByte(':')
		var v types.Value
		if i < len(row) {
			v = row[i]
		}
		appendJSONValue(b, v, col.Type)
	}
	b.WriteByte('}')
}

func appendJSONString(b *bytes.Buffer, s string) {
	enc, _ := json.Marshal(s)
	b.Write(enc)
}

func appendJSONFloat(b *bytes.Buffer, f float64) {
	switch {
	case math.IsNaN(f):
		b.WriteString(`"NaN"`)
	case math.IsInf(f, 1):
		b.WriteString(`"Infinity"`)
	case math.IsInf(f, -1):
		b.WriteString(`"-Infinity"`)
	default:
		b.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
	}
}

func appendJSONValue(b *bytes.Buffer, v types.Value, typ types.KQLType) {
	if v == nil {
		b.WriteString("null")
		return
	}
	if _, ok := v.(types.Decimal); ok {
		appendJSONString(b, types.FormatValue(v, typ))
		return
	}
	switch typ {
	case types.TypeString, types.TypeGUID, types.TypeDatetime, types.TypeTimespan:
		appendJSONString(b, types.FormatValue(v, typ))
		return
	case types.TypeDynamic:
		if s, ok := v.(string); ok {
			if json.Valid([]byte(s)) {
				b.WriteString(s)
			} else {
				appendJSONString(b, s)
			}
			return
		}
	}
	switch x := v.(type) {
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int:
		b.WriteString(strconv.Itoa(x))
	case float64:
		appendJSONFloat(b, x)
	case string:
		appendJSONString(b, x)
	default:
		if enc, err := json.Marshal(x); err == nil {
			b.Write(enc)
		} else {
			appendJSONString(b, types.FormatValue(v, typ))
		}
	}
}
