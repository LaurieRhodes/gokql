package main

// arrow_out.go: -mode arrow writes each result as an Apache Arrow IPC
// stream, for pandas, Polars, DuckDB and other Arrow readers.
//
// long, int, real, bool and string map to Int64, Int32, Float64,
// Boolean and Utf8; datetime to Timestamp(ns, UTC); timespan to
// Duration(ns); guid, decimal and dynamic to Utf8 (dynamic as JSON
// text). Every field carries kql.type=<type> metadata, so arrow("...")
// reads the stream back with the original types.

import (
	"fmt"
	"io"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

func arrowFieldType(t types.KQLType) arrow.DataType {
	switch t {
	case types.TypeLong:
		return arrow.PrimitiveTypes.Int64
	case types.TypeInt:
		return arrow.PrimitiveTypes.Int32
	case types.TypeReal:
		return arrow.PrimitiveTypes.Float64
	case types.TypeBool:
		return arrow.FixedWidthTypes.Boolean
	case types.TypeDatetime:
		return &arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "UTC"}
	case types.TypeTimespan:
		return arrow.FixedWidthTypes.Duration_ns
	}
	return arrow.BinaryTypes.String
}

// writeArrow writes t as one Arrow IPC stream.
func writeArrow(w io.Writer, t *types.Table) error {
	fields := make([]arrow.Field, len(t.Schema.Columns))
	for i, c := range t.Schema.Columns {
		fields[i] = arrow.Field{Name: c.Name, Type: arrowFieldType(c.Type), Nullable: true,
			Metadata: arrow.NewMetadata([]string{"kql.type"}, []string{c.Type.String()})}
	}
	schema := arrow.NewSchema(fields, nil)
	b := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer b.Release()
	for _, row := range t.Rows {
		for i, c := range t.Schema.Columns {
			var v types.Value
			if i < len(row) {
				v = row[i]
			}
			fb := b.Field(i)
			if v == nil {
				fb.AppendNull()
				continue
			}
			switch x := fb.(type) {
			case *array.Int64Builder:
				x.Append(types.ToInt64(v))
			case *array.Int32Builder:
				x.Append(int32(types.ToInt64(v)))
			case *array.Float64Builder:
				x.Append(types.ToFloat64(v))
			case *array.BooleanBuilder:
				bv, _ := v.(bool)
				x.Append(bv)
			case *array.TimestampBuilder:
				x.Append(arrow.Timestamp(types.ToInt64(v)))
			case *array.DurationBuilder:
				x.Append(arrow.Duration(types.ToInt64(v) * 100)) // ticks to ns
			case *array.StringBuilder:
				x.Append(types.FormatValue(v, c.Type))
			default:
				return fmt.Errorf("arrow: unexpected builder %T", fb)
			}
		}
	}
	rec := b.NewRecord()
	defer rec.Release()
	wr := ipc.NewWriter(w, ipc.WithSchema(schema), ipc.WithAllocator(memory.DefaultAllocator))
	if err := wr.Write(rec); err != nil {
		wr.Close()
		return err
	}
	return wr.Close()
}
