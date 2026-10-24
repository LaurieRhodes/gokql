package arrowipc

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// batchRows is the number of rows in each record batch Write emits.
const batchRows = 64 * 1024

// Write writes t as an Arrow IPC stream.
//
// long, int, real, bool and string become int64, int32, float64, bool
// and utf8; datetime becomes timestamp[ns, UTC]; timespan becomes
// duration[ns]; guid, decimal and dynamic become utf8 (dynamic as JSON
// text). Every field carries kql.type=<type> metadata, so Read returns
// the original types.
func Write(w io.Writer, t *types.Table) error {
	cols := t.Schema.Columns
	if err := writeMessage(w, schemaMessage(cols), nil); err != nil {
		return err
	}
	for start := 0; start < len(t.Rows); start += batchRows {
		end := min(start+batchRows, len(t.Rows))
		meta, body, err := recordBatch(cols, t.Rows[start:end])
		if err != nil {
			return err
		}
		if err := writeMessage(w, meta, body); err != nil {
			return err
		}
	}
	_, err := w.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0, 0, 0, 0})
	return err
}

func pad8(n int) int { return (n + 7) &^ 7 }

// writeMessage writes one encapsulated message: continuation marker,
// metadata length, metadata padded to 8 bytes, body.
func writeMessage(w io.Writer, meta, body []byte) error {
	n := pad8(len(meta))
	hdr := make([]byte, 8, 8+n)
	binary.LittleEndian.PutUint32(hdr, 0xFFFFFFFF)
	binary.LittleEndian.PutUint32(hdr[4:], uint32(n))
	hdr = append(hdr, meta...)
	hdr = append(hdr, make([]byte, n-len(meta))...)
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

func arrowTypeOf(b *flatbuffers.Builder, kt types.KQLType) (byte, flatbuffers.UOffsetT) {
	switch kt {
	case types.TypeLong, types.TypeInt:
		b.StartObject(2)
		bits := int32(64)
		if kt == types.TypeInt {
			bits = 32
		}
		b.PrependInt32Slot(0, bits, 0)
		b.PrependBoolSlot(1, true, false)
		return tInt, b.EndObject()
	case types.TypeReal:
		b.StartObject(1)
		b.PrependInt16Slot(0, 2, 0) // DOUBLE
		return tFloat, b.EndObject()
	case types.TypeBool:
		b.StartObject(0)
		return tBool, b.EndObject()
	case types.TypeDatetime:
		tz := b.CreateString("UTC")
		b.StartObject(2)
		b.PrependInt16Slot(0, 3, 0) // NANOSECOND
		b.PrependUOffsetTSlot(1, tz, 0)
		return tTimestamp, b.EndObject()
	case types.TypeTimespan:
		b.StartObject(1)
		b.PrependInt16Slot(0, 3, 1) // NANOSECOND
		return tDuration, b.EndObject()
	}
	b.StartObject(0)
	return tUtf8, b.EndObject()
}

func schemaMessage(cols []types.Column) []byte {
	b := flatbuffers.NewBuilder(1024)
	offs := make([]flatbuffers.UOffsetT, len(cols))
	for i, c := range cols {
		name := b.CreateString(c.Name)
		k, v := b.CreateString("kql.type"), b.CreateString(c.Type.String())
		b.StartObject(2)
		b.PrependUOffsetTSlot(0, k, 0)
		b.PrependUOffsetTSlot(1, v, 0)
		kv := b.EndObject()
		b.StartVector(4, 1, 4)
		b.PrependUOffsetT(kv)
		meta := b.EndVector(1)
		b.StartVector(4, 0, 4)
		children := b.EndVector(0) // readers require the vector, even empty
		tid, typ := arrowTypeOf(b, c.Type)
		b.StartObject(7)
		b.PrependUOffsetTSlot(0, name, 0)
		b.PrependBoolSlot(1, true, false)
		b.PrependByteSlot(2, tid, 0)
		b.PrependUOffsetTSlot(3, typ, 0)
		b.PrependUOffsetTSlot(5, children, 0)
		b.PrependUOffsetTSlot(6, meta, 0)
		offs[i] = b.EndObject()
	}
	b.StartVector(4, len(offs), 4)
	for i := len(offs) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offs[i])
	}
	fields := b.EndVector(len(offs))
	b.StartObject(4)
	b.PrependUOffsetTSlot(1, fields, 0)
	schema := b.EndObject()
	return finishMessage(b, hSchema, schema, 0)
}

func finishMessage(b *flatbuffers.Builder, kind byte, hdr flatbuffers.UOffsetT, bodyLen int64) []byte {
	b.StartObject(5)
	b.PrependInt16Slot(0, 4, 0) // MetadataVersion V5
	b.PrependByteSlot(1, kind, 0)
	b.PrependUOffsetTSlot(2, hdr, 0)
	b.PrependInt64Slot(3, bodyLen, 0)
	b.Finish(b.EndObject())
	return b.FinishedBytes()
}

// recordBatch encodes rows as one RecordBatch message and its body.
func recordBatch(cols []types.Column, rows []types.Row) ([]byte, []byte, error) {
	n := len(rows)
	var body []byte
	var bufs [][2]int64
	nodes := make([][2]int64, len(cols))
	add := func(p []byte) {
		bufs = append(bufs, [2]int64{int64(len(body)), int64(len(p))})
		body = append(body, p...)
		body = append(body, make([]byte, pad8(len(p))-len(p))...)
	}
	for ci, c := range cols {
		valid := make([]byte, (n+7)/8)
		nulls := 0
		get := func(r int) types.Value {
			if ci < len(rows[r]) {
				return rows[r][ci]
			}
			return nil
		}
		for r := 0; r < n; r++ {
			if get(r) == nil {
				nulls++
			} else {
				valid[r>>3] |= 1 << (uint(r) & 7)
			}
		}
		nodes[ci] = [2]int64{int64(n), int64(nulls)}
		if nulls == 0 {
			add(nil)
		} else {
			add(valid)
		}
		switch c.Type {
		case types.TypeLong, types.TypeDatetime, types.TypeTimespan, types.TypeReal:
			vals := make([]byte, 8*n)
			for r := 0; r < n; r++ {
				v := get(r)
				if v == nil {
					continue
				}
				var u uint64
				switch c.Type {
				case types.TypeReal:
					u = math.Float64bits(types.ToFloat64(v))
				case types.TypeTimespan:
					u = uint64(types.ToInt64(v) * 100) // 100 ns ticks to ns
				default:
					u = uint64(types.ToInt64(v))
				}
				binary.LittleEndian.PutUint64(vals[8*r:], u)
			}
			add(vals)
		case types.TypeInt:
			vals := make([]byte, 4*n)
			for r := 0; r < n; r++ {
				if v := get(r); v != nil {
					binary.LittleEndian.PutUint32(vals[4*r:], uint32(int32(types.ToInt64(v))))
				}
			}
			add(vals)
		case types.TypeBool:
			vals := make([]byte, (n+7)/8)
			for r := 0; r < n; r++ {
				if bv, _ := get(r).(bool); bv {
					vals[r>>3] |= 1 << (uint(r) & 7)
				}
			}
			add(vals)
		default:
			offs := make([]byte, 4*(n+1))
			var data []byte
			for r := 0; r < n; r++ {
				if v := get(r); v != nil {
					data = append(data, types.FormatValue(v, c.Type)...)
				}
				if len(data) > math.MaxInt32 {
					return nil, nil, fmt.Errorf("column %q: more than 2 GB of text in one batch", c.Name)
				}
				binary.LittleEndian.PutUint32(offs[4*(r+1):], uint32(len(data)))
			}
			add(offs)
			add(data)
		}
	}
	b := flatbuffers.NewBuilder(256 + 32*len(bufs))
	b.StartVector(16, len(nodes), 8)
	for i := len(nodes) - 1; i >= 0; i-- {
		b.Prep(8, 16)
		b.PrependInt64(nodes[i][1])
		b.PrependInt64(nodes[i][0])
	}
	nv := b.EndVector(len(nodes))
	b.StartVector(16, len(bufs), 8)
	for i := len(bufs) - 1; i >= 0; i-- {
		b.Prep(8, 16)
		b.PrependInt64(bufs[i][1])
		b.PrependInt64(bufs[i][0])
	}
	bv := b.EndVector(len(bufs))
	b.StartObject(5)
	b.PrependInt64Slot(0, int64(n), 0)
	b.PrependUOffsetTSlot(1, nv, 0)
	b.PrependUOffsetTSlot(2, bv, 0)
	rb := b.EndObject()
	return finishMessage(b, hRecordBatch, rb, int64(len(body))), body, nil
}
