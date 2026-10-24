package engine

// file_arrow.go: arrow("path") reads an Apache Arrow IPC file (Feather
// v2, .arrow) or IPC stream as a table, and vortex("path") reads one
// Vortex file.
//
// Arrow types map to KQL: integers to long (int32 to int), floats to
// real, bool, strings to string, timestamps and dates to datetime,
// durations to timespan, decimals to real, and anything nested (lists,
// structs, maps) to dynamic. A field whose metadata says
// kql.type=<type>, as gokql's own -mode arrow output writes, is read
// back as that type.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// arrowKQLType is the KQL type for an Arrow field.
func arrowKQLType(f arrow.Field) types.KQLType {
	if v, ok := f.Metadata.GetValue("kql.type"); ok {
		if t, err := types.ParseType(v); err == nil {
			return t
		}
	}
	if d, ok := f.Type.(*arrow.DictionaryType); ok {
		return arrowKQLType(arrow.Field{Type: d.ValueType})
	}
	switch f.Type.ID() {
	case arrow.INT8, arrow.INT16, arrow.INT64, arrow.UINT8, arrow.UINT16, arrow.UINT32, arrow.UINT64:
		return types.TypeLong
	case arrow.INT32:
		return types.TypeInt
	case arrow.FLOAT16, arrow.FLOAT32, arrow.FLOAT64, arrow.DECIMAL128, arrow.DECIMAL256:
		return types.TypeReal
	case arrow.BOOL:
		return types.TypeBool
	case arrow.STRING, arrow.LARGE_STRING, arrow.STRING_VIEW:
		return types.TypeString
	case arrow.TIMESTAMP, arrow.DATE32, arrow.DATE64:
		return types.TypeDatetime
	case arrow.DURATION:
		return types.TypeTimespan
	}
	return types.TypeDynamic
}

// arrowValue is row i of column c as a gokql value of type t.
func arrowValue(c arrow.Array, i int, t types.KQLType) types.Value {
	if c.IsNull(i) {
		if t == types.TypeString {
			return ""
		}
		return nil
	}
	switch a := c.(type) {
	case *array.Int8:
		return int64(a.Value(i))
	case *array.Int16:
		return int64(a.Value(i))
	case *array.Int32:
		if t == types.TypeInt {
			return a.Value(i)
		}
		return int64(a.Value(i))
	case *array.Int64:
		return a.Value(i) // long, or datetime/timespan in gokql's own encoding
	case *array.Uint8:
		return int64(a.Value(i))
	case *array.Uint16:
		return int64(a.Value(i))
	case *array.Uint32:
		return int64(a.Value(i))
	case *array.Uint64:
		return int64(a.Value(i))
	case *array.Float16:
		return float64(a.Value(i).Float32())
	case *array.Float32:
		return float64(a.Value(i))
	case *array.Float64:
		return a.Value(i)
	case *array.Boolean:
		return a.Value(i)
	case *array.String:
		return a.Value(i)
	case *array.LargeString:
		return a.Value(i)
	case *array.Timestamp:
		unit := a.DataType().(*arrow.TimestampType).Unit
		return int64(a.Value(i)) * int64(unit.Multiplier())
	case *array.Date32:
		return a.Value(i).ToTime().UnixNano()
	case *array.Date64:
		return a.Value(i).ToTime().UnixNano()
	case *array.Dictionary:
		return arrowValue(a.Dictionary(), a.GetValueIndex(i), t)
	case *array.Decimal128:
		return a.Value(i).ToFloat64(a.DataType().(*arrow.Decimal128Type).Scale)
	case *array.Decimal256:
		return a.Value(i).ToFloat64(a.DataType().(*arrow.Decimal256Type).Scale)
	case *array.Duration:
		unit := a.DataType().(*arrow.DurationType).Unit
		return int64(a.Value(i)) * int64(unit.Multiplier()) / 100 // timespans are 100 ns ticks
	}
	b, err := json.Marshal(c.GetOneForMarshal(i))
	if err != nil {
		return c.ValueStr(i)
	}
	return string(b)
}

func readArrowFile(path string) (*types.Table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var recs []arrow.Record
	var schema *arrow.Schema
	// the IPC file format (Feather v2), else the IPC stream format
	if fr, err := ipc.NewFileReader(f, ipc.WithAllocator(memory.DefaultAllocator)); err == nil {
		defer fr.Close()
		schema = fr.Schema()
		for i := 0; i < fr.NumRecords(); i++ {
			r, err := fr.Record(i)
			if err != nil {
				return nil, err
			}
			r.Retain()
			recs = append(recs, r)
		}
	} else {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		sr, err := ipc.NewReader(f, ipc.WithAllocator(memory.DefaultAllocator))
		if err != nil {
			return nil, fmt.Errorf("not an Arrow IPC file or stream: %w", err)
		}
		defer sr.Release()
		schema = sr.Schema()
		for sr.Next() {
			r := sr.Record()
			r.Retain()
			recs = append(recs, r)
		}
		if err := sr.Err(); err != nil {
			return nil, err
		}
	}
	defer func() {
		for _, r := range recs {
			r.Release()
		}
	}()
	cols := make([]types.Column, schema.NumFields())
	for i, fld := range schema.Fields() {
		cols[i] = types.Column{Name: fld.Name, Type: arrowKQLType(fld)}
	}
	name := filepath.Base(path)
	if ext := filepath.Ext(name); ext != "" {
		name = name[:len(name)-len(ext)]
	}
	out := types.NewTable(name, types.Schema{Columns: cols})
	for _, r := range recs {
		n := int(r.NumRows())
		for i := 0; i < n; i++ {
			row := make(types.Row, len(cols))
			for c := range cols {
				row[c] = arrowValue(r.Column(c), i, cols[c].Type)
			}
			out.Rows = append(out.Rows, row)
		}
	}
	return out, nil
}

// readVortexFile reads one Vortex file through a temporary
// single-file database, so it is decoded exactly as a stored extent.
// Strings in a database extent are stored against the database's shared
// dictionaries, so the _Dictionaries extents beside the file are used too.
func (e *Engine) readVortexFile(path string) (t *types.Table, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "gokql-vortex-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	// An extent's table name is its file name up to the last "_"; shared
	// dictionaries are keyed by it, so keep the name when it has that shape.
	base := filepath.Base(abs)
	table := strings.TrimSuffix(base, filepath.Ext(base))
	if k := strings.LastIndexByte(table, '_'); k > 0 && filepath.Ext(base) == ".vtx" {
		table = table[:k]
	} else {
		table, base = "V", "V_18e000000000000000000000.vtx"
	}
	if err := os.Symlink(abs, filepath.Join(dir, base)); err != nil {
		return nil, err
	}
	dicts, _ := filepath.Glob(filepath.Join(filepath.Dir(abs), "_Dictionaries_*.vtx"))
	for _, d := range dicts {
		if d == abs {
			continue
		}
		if err := os.Symlink(d, filepath.Join(dir, filepath.Base(d))); err != nil {
			return nil, err
		}
	}
	defer func() {
		if r := recover(); r != nil {
			t, err = nil, fmt.Errorf("cannot decode %s: its string dictionaries are not beside it (%v)", filepath.Base(path), r)
		}
	}()
	cat, err := catalog.Open(dir)
	if err != nil {
		return nil, err
	}
	if cat.GetTable(table) == nil {
		return nil, fmt.Errorf("not a readable Vortex file")
	}
	t, err = e.forNested(cat).executeQuery(&parser.Query{Source: table})
	if err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	if ext := filepath.Ext(name); ext != "" {
		name = name[:len(name)-len(ext)]
	}
	t.Name = name
	return t, nil
}
