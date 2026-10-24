package engine

// vortex_export.go: -mode vortex writes a result as a plain Vortex file
// that other Vortex readers (the Rust reference, Python's vortex
// package, DuckDB's vortex extension) can open.
//
// gokql's own extents tag columns with kql.* extension types and store
// strings as codes into database-wide dictionaries; no other reader
// understands either. The export uses standard types only:
//
//	long, int, real, bool, string  i64, i32, f64, bool, utf8
//	datetime                       vortex.timestamp[ns, tz=UTC]
//	timespan                       i64 nanoseconds (Vortex has no duration type)
//	guid, decimal, dynamic         utf8 (dynamic as JSON text)

import (
	"fmt"
	"io"

	vortex "github.com/LaurieRhodes/vortex-go"
	"github.com/LaurieRhodes/vortex-go/writer"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// vortexTimestampNsUTC is vortex.timestamp's metadata for nanoseconds in
// UTC: the time unit (0 = ns), then the time zone as a u16 length and
// its bytes.
var vortexTimestampNsUTC = []byte{0, 3, 0, 'U', 'T', 'C'}

// WritePortableVortex writes t as a Vortex file using only standard
// Vortex types.
func WritePortableVortex(w io.Writer, t *types.Table) error {
	// columns as stored: timespan as long nanoseconds, the text types as string
	stored := types.Schema{Columns: make([]types.Column, len(t.Schema.Columns))}
	for i, c := range t.Schema.Columns {
		st := c.Type
		switch c.Type {
		case types.TypeTimespan:
			st = types.TypeLong
		case types.TypeGUID, types.TypeDecimal, types.TypeDynamic:
			st = types.TypeString
		}
		stored.Columns[i] = types.Column{Name: c.Name, Type: st}
	}
	schema := schemaToVortexDType(&stored)
	for i, c := range t.Schema.Columns {
		if c.Type == types.TypeDatetime {
			schema.FieldTypes[i] = &vortex.DType{Kind: vortex.DTypeExtension, ExtensionID: "vortex.timestamp",
				ExtMetadata: vortexTimestampNsUTC, Nullable: true,
				StorageDType: &vortex.DType{Kind: vortex.DTypePrimitive, PType: vortex.PTypeI64, Nullable: true}}
		}
	}
	opts := writer.DefaultOptions()
	opts.ReferenceLayout = true // the buffer and extension layout other readers expect
	// DuckDB's vortex extension (as of 1.4) bundles a pcodec that cannot
	// read the current pcodec format, so numbers use bit-packing only.
	opts.EnablePcodec = false
	vw, err := writer.NewWriter(w, schema, opts)
	if err != nil {
		return fmt.Errorf("vortex: %w", err)
	}
	if len(t.Rows) == 0 {
		// other readers need a segment per column, so write one empty chunk
		cols := make(map[string]*vortex.Array, len(stored.Columns))
		for i, c := range stored.Columns {
			arr, err := buildColumnArray(c.Type, nil, i, 0)
			if err != nil {
				return fmt.Errorf("vortex: %w", err)
			}
			cols[c.Name] = arr
		}
		if err := vw.WriteChunk(cols); err != nil {
			return fmt.Errorf("vortex: %w", err)
		}
	}
	chunk := chunkRowsFor(t.Rows)
	for start := 0; start < len(t.Rows); start += chunk {
		end := min(start+chunk, len(t.Rows))
		rows := make([]types.Row, end-start)
		for r := range rows {
			src := t.Rows[start+r]
			row := make(types.Row, len(t.Schema.Columns))
			for i, c := range t.Schema.Columns {
				if i >= len(src) || src[i] == nil {
					continue
				}
				switch c.Type {
				case types.TypeTimespan:
					row[i] = types.ToInt64(src[i]) * 100 // 100 ns ticks to ns
				case types.TypeGUID, types.TypeDecimal, types.TypeDynamic:
					row[i] = types.FormatValue(src[i], c.Type)
				default:
					row[i] = src[i]
				}
			}
			rows[r] = row
		}
		cols, err := rowsToVortexArrays(&stored, rows)
		if err != nil {
			return fmt.Errorf("vortex: %w", err)
		}
		if err := vw.WriteChunk(cols); err != nil {
			return fmt.Errorf("vortex: %w", err)
		}
	}
	return vw.Close()
}
