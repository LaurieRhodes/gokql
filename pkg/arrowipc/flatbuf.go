// Package arrowipc reads and writes the Apache Arrow IPC formats (the
// stream format and the file format, also called Feather v2) for
// gokql's tables, without the Arrow library.
//
// The reader accepts what pyarrow, Polars, DuckDB and arrow-go write:
// every integer and float width, decimals, dates, times, timestamps,
// durations, binary and string (plain, large and dictionary-encoded),
// lists, structs and maps, uncompressed or with LZ4 or Zstandard buffer
// compression. View, union and run-end-encoded types are refused.
//
// The writer writes the stream format with no compression, one Arrow
// type per KQL type (see Write).
//
// The message layouts follow the Arrow format's Schema.fbs, Message.fbs
// and File.fbs; table slots are numbered as in those files.
package arrowipc

import flatbuffers "github.com/google/flatbuffers/go"

// fbt is a flatbuffer table, read by slot number.
type fbt struct{ t flatbuffers.Table }

func fbRoot(buf []byte) fbt {
	return fbt{flatbuffers.Table{Bytes: buf, Pos: flatbuffers.GetUOffsetT(buf)}}
}

func (x fbt) off(slot int) flatbuffers.UOffsetT {
	if x.t.Bytes == nil {
		return 0
	}
	return flatbuffers.UOffsetT(x.t.Offset(flatbuffers.VOffsetT(4 + 2*slot)))
}

func (x fbt) i64(slot int, def int64) int64 {
	if o := x.off(slot); o != 0 {
		return x.t.GetInt64(o + x.t.Pos)
	}
	return def
}

func (x fbt) i32(slot int, def int32) int32 {
	if o := x.off(slot); o != 0 {
		return x.t.GetInt32(o + x.t.Pos)
	}
	return def
}

func (x fbt) i16(slot int, def int16) int16 {
	if o := x.off(slot); o != 0 {
		return x.t.GetInt16(o + x.t.Pos)
	}
	return def
}

func (x fbt) u8(slot int, def byte) byte {
	if o := x.off(slot); o != 0 {
		return x.t.GetByte(o + x.t.Pos)
	}
	return def
}

func (x fbt) boolean(slot int, def bool) bool {
	if o := x.off(slot); o != 0 {
		return x.t.GetBool(o + x.t.Pos)
	}
	return def
}

func (x fbt) str(slot int) string {
	if o := x.off(slot); o != 0 {
		return string(x.t.ByteVector(o + x.t.Pos))
	}
	return ""
}

// sub is the table in a table (or union) slot.
func (x fbt) sub(slot int) (fbt, bool) {
	o := x.off(slot)
	if o == 0 {
		return fbt{}, false
	}
	return fbt{flatbuffers.Table{Bytes: x.t.Bytes, Pos: x.t.Indirect(o + x.t.Pos)}}, true
}

func (x fbt) vlen(slot int) int {
	if o := x.off(slot); o != 0 {
		return x.t.VectorLen(o)
	}
	return 0
}

// vtable is element j of a vector of tables.
func (x fbt) vtable(slot, j int) fbt {
	v := x.t.Vector(x.off(slot)) + flatbuffers.UOffsetT(j*4)
	return fbt{flatbuffers.Table{Bytes: x.t.Bytes, Pos: x.t.Indirect(v)}}
}

// vstruct is the position of element j of a vector of structs.
func (x fbt) vstruct(slot, j, size int) flatbuffers.UOffsetT {
	return x.t.Vector(x.off(slot)) + flatbuffers.UOffsetT(j*size)
}

func (x fbt) at64(pos flatbuffers.UOffsetT) int64 { return x.t.GetInt64(pos) }

// Arrow Type union members (Schema.fbs).
const (
	tNull            = 1
	tInt             = 2
	tFloat           = 3
	tBinary          = 4
	tUtf8            = 5
	tBool            = 6
	tDecimal         = 7
	tDate            = 8
	tTime            = 9
	tTimestamp       = 10
	tInterval        = 11
	tList            = 12
	tStruct          = 13
	tUnion           = 14
	tFixedSizeBinary = 15
	tFixedSizeList   = 16
	tMap             = 17
	tDuration        = 18
	tLargeBinary     = 19
	tLargeUtf8       = 20
	tLargeList       = 21
)

// MessageHeader union members (Message.fbs).
const (
	hSchema          = 1
	hDictionaryBatch = 2
	hRecordBatch     = 3
)

var typeNames = map[byte]string{
	tInterval: "interval", tUnion: "union", 22: "run-end encoded",
	23: "binary view", 24: "string view", 25: "list view", 26: "large list view",
}
