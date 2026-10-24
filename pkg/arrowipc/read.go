package arrowipc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// field is one Arrow field and the parameters of its type.
type field struct {
	name     string
	tid      byte
	bits     int   // Int, Decimal and Time bit width; FixedSizeBinary byte width
	signed   bool  // Int
	unit     int16 // Date, Time, Timestamp and Duration unit; Float precision
	scale    int32 // Decimal
	size     int32 // FixedSizeList length
	children []*field
	dict     *field // index type of a dictionary-encoded field
	dictID   int64
	meta     map[string]string
}

func parseField(t fbt) (*field, error) {
	f := &field{name: t.str(0), tid: t.u8(2, 0), meta: map[string]string{}}
	for i := 0; i < t.vlen(6); i++ {
		kv := t.vtable(6, i)
		f.meta[kv.str(0)] = kv.str(1)
	}
	tt, _ := t.sub(3)
	switch f.tid {
	case tInt:
		f.bits, f.signed = int(tt.i32(0, 0)), tt.boolean(1, false)
	case tFloat:
		f.unit = tt.i16(0, 0)
	case tDecimal:
		f.bits, f.scale = int(tt.i32(2, 128)), tt.i32(1, 0)
	case tDate:
		f.unit = tt.i16(0, 1)
	case tTime:
		f.unit, f.bits = tt.i16(0, 1), int(tt.i32(1, 32))
	case tTimestamp, tDuration:
		def := int16(0)
		if f.tid == tDuration {
			def = 1
		}
		f.unit = tt.i16(0, def)
	case tFixedSizeBinary:
		f.bits = int(tt.i32(0, 0))
	case tFixedSizeList:
		f.size = tt.i32(0, 0)
	case tNull, tBinary, tUtf8, tBool, tList, tStruct, tMap, tLargeBinary, tLargeUtf8, tLargeList:
	default:
		name := typeNames[f.tid]
		if name == "" {
			name = fmt.Sprintf("type %d", f.tid)
		}
		return nil, fmt.Errorf("column %q: Arrow %s columns are not supported", f.name, name)
	}
	for i := 0; i < t.vlen(5); i++ {
		c, err := parseField(t.vtable(5, i))
		if err != nil {
			return nil, err
		}
		f.children = append(f.children, c)
	}
	if d, ok := t.sub(4); ok {
		f.dictID = d.i64(0, 0)
		f.dict = &field{tid: tInt, bits: 32, signed: true}
		if it, ok := d.sub(1); ok {
			f.dict.bits, f.dict.signed = int(it.i32(0, 0)), it.boolean(1, false)
		}
	}
	return f, nil
}

// kqlType is the KQL type a column of this field reads as.
func (f *field) kqlType() types.KQLType {
	if v, ok := f.meta["kql.type"]; ok {
		if t, err := types.ParseType(v); err == nil {
			return t
		}
	}
	switch f.tid {
	case tInt:
		if f.bits == 32 && f.signed {
			return types.TypeInt
		}
		return types.TypeLong
	case tFloat, tDecimal:
		return types.TypeReal
	case tBool:
		return types.TypeBool
	case tUtf8, tLargeUtf8, tBinary, tLargeBinary:
		return types.TypeString
	case tFixedSizeBinary:
		if f.isUUID() {
			return types.TypeGUID
		}
		return types.TypeString
	case tTimestamp, tDate:
		return types.TypeDatetime
	case tDuration, tTime:
		return types.TypeTimespan
	}
	return types.TypeDynamic
}

func (f *field) isUUID() bool {
	return f.tid == tFixedSizeBinary && f.bits == 16 && f.meta["ARROW:extension:name"] == "arrow.uuid"
}

// column is one decoded array.
type column struct {
	f        *field
	n, nulls int
	valid    []byte
	bufs     [][]byte
	children []*column
	dict     []any // dictionary values, for a dictionary-encoded column
}

type batchLoader struct {
	nodes [][2]int64
	bufs  [][]byte
	ni    int
	bi    int
}

func (l *batchLoader) buf() ([]byte, error) {
	if l.bi >= len(l.bufs) {
		return nil, fmt.Errorf("record batch has too few buffers")
	}
	b := l.bufs[l.bi]
	l.bi++
	return b, nil
}

func (l *batchLoader) load(f *field, idx bool, dicts map[int64][]any) (*column, error) {
	if l.ni >= len(l.nodes) {
		return nil, fmt.Errorf("record batch has too few field nodes")
	}
	nd := l.nodes[l.ni]
	l.ni++
	c := &column{f: f, n: int(nd[0]), nulls: int(nd[1])}
	nbufs, kids := 0, f.children
	switch {
	case idx && f.dict != nil:
		c.dict = dicts[f.dictID]
		nbufs, kids = 1, nil
	case f.tid == tNull:
		return c, nil
	case f.tid == tUtf8, f.tid == tLargeUtf8, f.tid == tBinary, f.tid == tLargeBinary:
		nbufs = 2
	case f.tid == tList, f.tid == tLargeList, f.tid == tMap:
		nbufs = 1
	case f.tid == tStruct, f.tid == tFixedSizeList:
		nbufs = 0
	default:
		nbufs = 1
	}
	var err error
	if c.valid, err = l.buf(); err != nil {
		return nil, err
	}
	for i := 0; i < nbufs; i++ {
		b, err := l.buf()
		if err != nil {
			return nil, err
		}
		c.bufs = append(c.bufs, b)
	}
	for _, k := range kids {
		kc, err := l.load(k, true, dicts)
		if err != nil {
			return nil, err
		}
		c.children = append(c.children, kc)
	}
	return c, nil
}

func (c *column) isNull(i int) bool {
	if c.f.tid == tNull && c.dict == nil && c.f.dict == nil {
		return true
	}
	if c.nulls == 0 || len(c.valid) == 0 {
		return false
	}
	return c.valid[i>>3]&(1<<(uint(i)&7)) == 0
}

func le(b []byte, i, width int) []byte {
	if (i+1)*width > len(b) {
		return make([]byte, width)
	}
	return b[i*width : (i+1)*width]
}

func intAt(b []byte, i, bits int, signed bool) int64 {
	switch bits {
	case 8:
		v := le(b, i, 1)[0]
		if signed {
			return int64(int8(v))
		}
		return int64(v)
	case 16:
		v := binary.LittleEndian.Uint16(le(b, i, 2))
		if signed {
			return int64(int16(v))
		}
		return int64(v)
	case 32:
		v := binary.LittleEndian.Uint32(le(b, i, 4))
		if signed {
			return int64(int32(v))
		}
		return int64(v)
	}
	return int64(binary.LittleEndian.Uint64(le(b, i, 8)))
}

func (c *column) offsets(i int) (int, int) {
	if c.f.tid == tLargeUtf8 || c.f.tid == tLargeBinary || c.f.tid == tLargeList {
		return int(intAt(c.bufs[0], i, 64, true)), int(intAt(c.bufs[0], i+1, 64, true))
	}
	return int(intAt(c.bufs[0], i, 32, true)), int(intAt(c.bufs[0], i+1, 32, true))
}

// decimalText is a decimal's exact digits.
type decimalText string

var unitNanos = [4]int64{1e9, 1e6, 1e3, 1}

// value is row i as a Go value: nil, int64, float64, bool, string,
// decimalText, time.Time, time.Duration, []any or map[string]any.
func (c *column) value(i int) any {
	if c.isNull(i) {
		return nil
	}
	f := c.f
	if c.f.dict != nil && len(c.bufs) == 1 && c.children == nil {
		k := intAt(c.bufs[0], i, f.dict.bits, f.dict.signed)
		if k < 0 || int(k) >= len(c.dict) {
			return nil
		}
		return c.dict[k]
	}
	switch f.tid {
	case tInt:
		return intAt(c.bufs[0], i, f.bits, f.signed)
	case tFloat:
		switch f.unit {
		case 0:
			return halfFloat(binary.LittleEndian.Uint16(le(c.bufs[0], i, 2)))
		case 1:
			return float64(math.Float32frombits(binary.LittleEndian.Uint32(le(c.bufs[0], i, 4))))
		}
		return math.Float64frombits(binary.LittleEndian.Uint64(le(c.bufs[0], i, 8)))
	case tBool:
		return c.bufs[0][i>>3]&(1<<(uint(i)&7)) != 0
	case tDecimal:
		return decimalAt(le(c.bufs[0], i, f.bits/8), f.scale)
	case tDate:
		if f.unit == 0 {
			return time.Unix(intAt(c.bufs[0], i, 32, true)*86400, 0).UTC()
		}
		return time.UnixMilli(intAt(c.bufs[0], i, 64, true)).UTC()
	case tTime:
		return time.Duration(intAt(c.bufs[0], i, f.bits, true) * unitNanos[f.unit&3])
	case tTimestamp:
		return time.Unix(0, intAt(c.bufs[0], i, 64, true)*unitNanos[f.unit&3]).UTC()
	case tDuration:
		return time.Duration(intAt(c.bufs[0], i, 64, true) * unitNanos[f.unit&3])
	case tUtf8, tLargeUtf8, tBinary, tLargeBinary:
		a, b := c.offsets(i)
		if a < 0 || b > len(c.bufs[1]) || a > b {
			return ""
		}
		return string(c.bufs[1][a:b])
	case tFixedSizeBinary:
		b := le(c.bufs[0], i, f.bits)
		if f.isUUID() {
			return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
		}
		return string(b)
	case tList, tLargeList, tFixedSizeList:
		a, b := 0, 0
		if f.tid == tFixedSizeList {
			a, b = i*int(f.size), (i+1)*int(f.size)
		} else {
			a, b = c.offsets(i)
		}
		out := make([]any, 0, b-a)
		for j := a; j < b; j++ {
			out = append(out, c.children[0].value(j))
		}
		return out
	case tStruct:
		out := make(map[string]any, len(c.children))
		for _, k := range c.children {
			out[k.f.name] = k.value(i)
		}
		return out
	case tMap:
		a, b := c.offsets(i)
		out := map[string]any{}
		entries := c.children[0]
		if len(entries.children) < 2 {
			return out
		}
		for j := a; j < b; j++ {
			key := entries.children[0].value(j)
			ks, ok := key.(string)
			if !ok {
				ks = fmt.Sprint(jsonable(key))
			}
			out[ks] = entries.children[1].value(j)
		}
		return out
	}
	return nil
}

func halfFloat(h uint16) float64 {
	exp, frac := int(h>>10&0x1f), float64(h&0x3ff)
	var f float64
	switch exp {
	case 0:
		f = math.Ldexp(frac/1024, -14)
	case 31:
		f = math.Inf(1)
		if frac != 0 {
			f = math.NaN()
		}
	default:
		f = math.Ldexp(1+frac/1024, exp-15)
	}
	if h&0x8000 != 0 {
		f = -f
	}
	return f
}

// decimalAt is a little-endian two's-complement decimal as exact text.
func decimalAt(b []byte, scale int32) decimalText {
	be := make([]byte, len(b))
	for i := range b {
		be[len(b)-1-i] = b[i]
	}
	n := new(big.Int).SetBytes(be)
	if len(be) > 0 && be[0]&0x80 != 0 {
		n.Sub(n, new(big.Int).Lsh(big.NewInt(1), uint(8*len(be))))
	}
	s := n.String()
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	if scale > 0 {
		for len(s) <= int(scale) {
			s = "0" + s
		}
		s = s[:len(s)-int(scale)] + "." + s[len(s)-int(scale):]
	} else if scale < 0 {
		s += strings.Repeat("0", int(-scale))
	}
	if neg {
		s = "-" + s
	}
	return decimalText(s)
}

// jsonable converts a value for a dynamic column: times as ISO 8601
// text, durations as KQL timespans, decimals as numbers.
func jsonable(v any) any {
	switch x := v.(type) {
	case time.Time:
		return x.Format("2006-01-02T15:04:05.9999999Z")
	case time.Duration:
		return types.FormatValue(int64(x)/100, types.TypeTimespan)
	case decimalText:
		return json.Number(x)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return types.FormatValue(x, types.TypeReal)
		}
	case []any:
		for i := range x {
			x[i] = jsonable(x[i])
		}
	case map[string]any:
		for k := range x {
			x[k] = jsonable(x[k])
		}
	}
	return v
}

// kqlValue converts a Go value from value() to a KQL value of type t.
func kqlValue(v any, t types.KQLType) types.Value {
	if v == nil {
		if t == types.TypeString {
			return ""
		}
		return nil
	}
	switch t {
	case types.TypeLong, types.TypeInt:
		var n int64
		switch x := v.(type) {
		case int64:
			n = x
		case float64:
			n = int64(x)
		case bool:
			if x {
				n = 1
			}
		case time.Duration:
			n = int64(x)
		case decimalText:
			f, _ := strconv.ParseFloat(string(x), 64)
			n = int64(f)
		case string:
			p, err := types.ParseValue(x, t)
			if err != nil {
				return nil
			}
			return p
		default:
			return nil
		}
		if t == types.TypeInt {
			return int32(n)
		}
		return n
	case types.TypeReal:
		switch x := v.(type) {
		case float64:
			return x
		case int64:
			return float64(x)
		case decimalText:
			f, _ := strconv.ParseFloat(string(x), 64)
			return f
		case string:
			p, _ := types.ParseValue(x, t)
			return p
		}
		return nil
	case types.TypeBool:
		switch x := v.(type) {
		case bool:
			return x
		case int64:
			return x != 0
		case string:
			p, _ := types.ParseValue(x, t)
			return p
		}
		return nil
	case types.TypeDatetime:
		switch x := v.(type) {
		case time.Time:
			return x.UnixNano()
		case int64:
			return x
		case string:
			p, _ := types.ParseValue(x, t)
			return p
		}
		return nil
	case types.TypeTimespan:
		switch x := v.(type) {
		case time.Duration:
			return int64(x) / 100
		case int64:
			return x
		case string:
			p, _ := types.ParseValue(x, t)
			return p
		}
		return nil
	case types.TypeDecimal:
		var s string
		switch x := v.(type) {
		case decimalText:
			s = string(x)
		case float64:
			s = strconv.FormatFloat(x, 'f', -1, 64)
		case int64:
			s = strconv.FormatInt(x, 10)
		case string:
			s = x
		default:
			return nil
		}
		if d, ok := types.ParseDecimal(s); ok {
			return d
		}
		return nil
	case types.TypeDynamic:
		if s, ok := v.(string); ok {
			return s // gokql's own output: the JSON text
		}
		b, err := json.Marshal(jsonable(v))
		if err != nil {
			return nil
		}
		return string(b)
	}
	// string and guid
	switch x := v.(type) {
	case string:
		return x
	case time.Time, time.Duration, decimalText:
		return fmt.Sprint(jsonable(x))
	case float64:
		return types.FormatValue(x, types.TypeReal)
	case []any, map[string]any:
		b, _ := json.Marshal(jsonable(x))
		return string(b)
	}
	return fmt.Sprint(v)
}

// reader walks the messages of a stream.
type reader struct {
	data   []byte
	pos    int
	end    int
	fields []*field
	dicts  map[int64][]any
	dfield map[int64]*field
	zstd   *zstd.Decoder
}

// next returns the next message and its body, or ok=false at the end.
func (r *reader) next() (msg fbt, body []byte, ok bool, err error) {
	if r.pos+4 > r.end {
		return fbt{}, nil, false, nil
	}
	n := binary.LittleEndian.Uint32(r.data[r.pos:])
	r.pos += 4
	if n == 0xFFFFFFFF {
		if r.pos+4 > r.end {
			return fbt{}, nil, false, nil
		}
		n = binary.LittleEndian.Uint32(r.data[r.pos:])
		r.pos += 4
	}
	if n == 0 {
		return fbt{}, nil, false, nil
	}
	if r.pos+int(n) > r.end {
		return fbt{}, nil, false, fmt.Errorf("truncated message")
	}
	meta := r.data[r.pos : r.pos+int(n)]
	r.pos += int(n)
	msg = fbRoot(meta)
	bl := int(msg.i64(3, 0))
	if bl < 0 || r.pos+bl > len(r.data) {
		return fbt{}, nil, false, fmt.Errorf("truncated message body")
	}
	body = r.data[r.pos : r.pos+bl]
	r.pos += bl
	return msg, body, true, nil
}

// batch decodes a RecordBatch table into top-level columns.
func (r *reader) batch(rb fbt, body []byte, fields []*field) ([]*column, int, error) {
	l := &batchLoader{}
	for i := 0; i < rb.vlen(1); i++ {
		p := rb.vstruct(1, i, 16)
		l.nodes = append(l.nodes, [2]int64{rb.at64(p), rb.at64(p + 8)})
	}
	comp, compressed := rb.sub(3)
	codec := comp.u8(0, 0)
	for i := 0; i < rb.vlen(2); i++ {
		p := rb.vstruct(2, i, 16)
		off, n := rb.at64(p), rb.at64(p+8)
		if off < 0 || n < 0 || off+n > int64(len(body)) {
			return nil, 0, fmt.Errorf("buffer %d is outside the message body", i)
		}
		b := body[off : off+n]
		if compressed && len(b) >= 8 {
			var err error
			if b, err = r.decompress(b, codec); err != nil {
				return nil, 0, err
			}
		}
		l.bufs = append(l.bufs, b)
	}
	cols := make([]*column, len(fields))
	for i, f := range fields {
		c, err := l.load(f, true, r.dicts)
		if err != nil {
			return nil, 0, err
		}
		cols[i] = c
	}
	return cols, int(rb.i64(0, 0)), nil
}

func (r *reader) decompress(b []byte, codec byte) ([]byte, error) {
	size := int64(binary.LittleEndian.Uint64(b))
	if size == -1 {
		return b[8:], nil
	}
	if size < 0 || size > 1<<40 {
		return nil, fmt.Errorf("bad compressed buffer length %d", size)
	}
	if codec == 1 {
		if r.zstd == nil {
			d, err := zstd.NewReader(nil)
			if err != nil {
				return nil, err
			}
			r.zstd = d
		}
		return r.zstd.DecodeAll(b[8:], make([]byte, 0, size))
	}
	out := make([]byte, size)
	zr := lz4.NewReader(bytes.NewReader(b[8:]))
	k, err := zr.Read(out)
	for err == nil && k < len(out) {
		var m int
		m, err = zr.Read(out[k:])
		k += m
	}
	if k < len(out) {
		return nil, fmt.Errorf("lz4 buffer: %v", err)
	}
	return out, nil
}

func collectDictFields(fs []*field, into map[int64]*field) {
	for _, f := range fs {
		if f.dict != nil {
			into[f.dictID] = f
		}
		collectDictFields(f.children, into)
	}
}

// Read reads an Arrow IPC file or stream. The table is named after the
// file.
func Read(path string) (*types.Table, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	if ext := filepath.Ext(name); ext != "" {
		name = name[:len(name)-len(ext)]
	}
	return Decode(data, name)
}

// Decode reads an Arrow IPC file or stream held in memory.
func Decode(data []byte, name string) (t *types.Table, err error) {
	defer func() {
		if p := recover(); p != nil {
			t, err = nil, fmt.Errorf("malformed Arrow data: %v", p)
		}
	}()
	r := &reader{data: data, end: len(data), dicts: map[int64][]any{}, dfield: map[int64]*field{}}
	if bytes.HasPrefix(data, []byte("ARROW1")) {
		r.pos = 8
		if n := len(data); n >= 16 && string(data[n-6:]) == "ARROW1" {
			if fl := int(int32(binary.LittleEndian.Uint32(data[n-10:]))); fl > 0 && n-10-fl >= 8 {
				r.end = n - 10 - fl
			}
		}
	}
	defer func() {
		if r.zstd != nil {
			r.zstd.Close()
		}
	}()
	var out *types.Table
	var kt []types.KQLType
	for {
		msg, body, ok, err := r.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		hdr, has := msg.sub(2)
		if !has {
			continue
		}
		switch msg.u8(1, 0) {
		case hSchema:
			if out != nil {
				continue
			}
			for i := 0; i < hdr.vlen(1); i++ {
				f, err := parseField(hdr.vtable(1, i))
				if err != nil {
					return nil, err
				}
				r.fields = append(r.fields, f)
			}
			collectDictFields(r.fields, r.dfield)
			cols := make([]types.Column, len(r.fields))
			kt = make([]types.KQLType, len(r.fields))
			for i, f := range r.fields {
				kt[i] = f.kqlType()
				cols[i] = types.Column{Name: f.name, Type: kt[i]}
			}
			out = types.NewTable(name, types.Schema{Columns: cols})
		case hDictionaryBatch:
			if out == nil {
				return nil, fmt.Errorf("dictionary before schema")
			}
			id := hdr.i64(0, 0)
			df, known := r.dfield[id]
			if !known {
				return nil, fmt.Errorf("dictionary %d has no field", id)
			}
			rb, _ := hdr.sub(1)
			vf := *df
			vf.dict = nil
			cols, n, err := r.batch(rb, body, []*field{&vf})
			if err != nil {
				return nil, err
			}
			vals := make([]any, n)
			for i := range vals {
				vals[i] = cols[0].value(i)
			}
			if hdr.boolean(2, false) {
				r.dicts[id] = append(r.dicts[id], vals...)
			} else {
				r.dicts[id] = vals
			}
		case hRecordBatch:
			if out == nil {
				return nil, fmt.Errorf("record batch before schema")
			}
			cols, n, err := r.batch(hdr, body, r.fields)
			if err != nil {
				return nil, err
			}
			for i := 0; i < n; i++ {
				row := make(types.Row, len(cols))
				for j, c := range cols {
					row[j] = kqlValue(c.value(i), kt[j])
				}
				out.Rows = append(out.Rows, row)
			}
		}
	}
	if out == nil {
		return nil, fmt.Errorf("not an Arrow IPC file or stream")
	}
	return out, nil
}
