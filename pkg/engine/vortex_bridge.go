package engine

// vortex_bridge.go bridges KQL types to Vortex arrays and back.
//
// Write path: KQL rows → columnar Vortex Arrays → writer.WriteChunk
// Read path:  Vortex scan → Array → encoding.*Values → KQL rows

import (
	"encoding/binary"
	"fmt"
	"math"

	vortex "github.com/LaurieRhodes/vortex-go"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// --- KQL Type → Vortex DType ---

// kqlTypeToVortexDType maps a single KQL column type to a Vortex DType.
// kqlExtensionIDs map KQL logical types that share a physical storage
// representation onto Vortex Extension dtype identities, so the exact
// KQL type is recoverable from any extent footer (catalog-free schema
// discovery). Plain long/int/real/bool/string stay unwrapped for
// compatibility with existing extents.
const (
	extKQLDatetime = "kql.datetime"
	extKQLTimespan = "kql.timespan"
	extKQLGUID     = "kql.guid"
	extKQLDynamic  = "kql.dynamic"
	// extKQLDictRef marks a column whose on-disk storage is plain
	// integer codes (StorageDType: an unsigned PType) referencing a
	// database-wide shared dictionary (see shareddict.go) rather than
	// vortex-go's own per-extent Dict layout. Decoded by looking up
	// codes in the table+column's shared dictionary file, not by
	// anything vortex-go itself understands — see decodeColumnVec.
	extKQLDictRef = "kql.dictref"
)

func kqlTypeToVortexDType(kt types.KQLType) *vortex.DType {
	switch kt {
	case types.TypeString:
		return &vortex.DType{Kind: vortex.DTypeUtf8}
	case types.TypeGUID:
		return &vortex.DType{Kind: vortex.DTypeExtension, ExtensionID: extKQLGUID,
			StorageDType: &vortex.DType{Kind: vortex.DTypeUtf8}}
	case types.TypeDynamic:
		return &vortex.DType{Kind: vortex.DTypeExtension, ExtensionID: extKQLDynamic,
			StorageDType: &vortex.DType{Kind: vortex.DTypeUtf8}}
	case types.TypeLong:
		return &vortex.DType{Kind: vortex.DTypePrimitive, PType: vortex.PTypeI64}
	case types.TypeInt:
		return &vortex.DType{Kind: vortex.DTypePrimitive, PType: vortex.PTypeI32}
	case types.TypeReal:
		return &vortex.DType{Kind: vortex.DTypePrimitive, PType: vortex.PTypeF64}
	case types.TypeBool:
		return &vortex.DType{Kind: vortex.DTypeBool}
	case types.TypeDatetime:
		return &vortex.DType{Kind: vortex.DTypeExtension, ExtensionID: extKQLDatetime,
			StorageDType: &vortex.DType{Kind: vortex.DTypePrimitive, PType: vortex.PTypeI64}}
	case types.TypeTimespan:
		return &vortex.DType{Kind: vortex.DTypeExtension, ExtensionID: extKQLTimespan,
			StorageDType: &vortex.DType{Kind: vortex.DTypePrimitive, PType: vortex.PTypeI64}}
	default:
		return &vortex.DType{Kind: vortex.DTypeUtf8} // Fallback to string
	}
}

// vortexDTypeToKQL recovers the KQL type from a Vortex dtype: exact
// for kql.* extension identities, physical otherwise (files written
// before extension emission read as long/string — correct at the
// storage level, with only KQL sub-typing lost).
func vortexDTypeToKQL(dt *vortex.DType) types.KQLType {
	if dt == nil {
		return types.TypeString
	}
	if dt.Kind == vortex.DTypeExtension {
		switch dt.ExtensionID {
		case extKQLDatetime:
			return types.TypeDatetime
		case extKQLTimespan:
			return types.TypeTimespan
		case extKQLGUID:
			return types.TypeGUID
		case extKQLDynamic:
			return types.TypeDynamic
		case extKQLDictRef:
			return types.TypeString
		}
		return vortexDTypeToKQL(dt.StorageDType)
	}
	switch dt.Kind {
	case vortex.DTypeUtf8, vortex.DTypeBinary:
		return types.TypeString
	case vortex.DTypeBool:
		return types.TypeBool
	case vortex.DTypePrimitive:
		switch dt.PType {
		case vortex.PTypeI64, vortex.PTypeU64:
			return types.TypeLong
		case vortex.PTypeI32, vortex.PTypeU32, vortex.PTypeI16, vortex.PTypeU16, vortex.PTypeI8, vortex.PTypeU8:
			return types.TypeInt
		case vortex.PTypeF64, vortex.PTypeF32:
			return types.TypeReal
		}
	}
	return types.TypeString
}

// schemaToVortexDType builds a Vortex struct DType from a KQL schema.
func schemaToVortexDType(schema *types.Schema) *vortex.DType {
	fieldNames := make([]string, len(schema.Columns))
	fieldTypes := make([]*vortex.DType, len(schema.Columns))
	for i, col := range schema.Columns {
		fieldNames[i] = col.Name
		fieldTypes[i] = kqlTypeToVortexDType(col.Type)
	}
	return &vortex.DType{
		Kind:       vortex.DTypeStruct,
		FieldNames: fieldNames,
		FieldTypes: fieldTypes,
	}
}

// --- Row-to-Columnar Transposition ---

// rowsToVortexArrays transposes KQL rows into a map of Vortex Arrays,
// one per column, suitable for writer.WriteChunk.
func rowsToVortexArrays(schema *types.Schema, rows []types.Row) (map[string]*vortex.Array, error) {
	n := len(rows)
	if n == 0 {
		return nil, fmt.Errorf("no rows to convert")
	}

	columns := make(map[string]*vortex.Array, len(schema.Columns))

	for colIdx, col := range schema.Columns {
		arr, err := buildColumnArray(col.Type, rows, colIdx, n)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", col.Name, err)
		}
		columns[col.Name] = arr
	}
	return columns, nil
}

// buildColumnArray builds a single Vortex Array from one column across all rows.
func buildColumnArray(kt types.KQLType, rows []types.Row, colIdx, n int) (*vortex.Array, error) {
	switch kt {
	case types.TypeLong, types.TypeDatetime, types.TypeTimespan:
		return buildI64Array(rows, colIdx, n), nil
	case types.TypeInt:
		return buildI32Array(rows, colIdx, n), nil
	case types.TypeReal:
		return buildF64Array(rows, colIdx, n), nil
	case types.TypeBool:
		return buildBoolArray(rows, colIdx, n), nil
	case types.TypeString, types.TypeGUID, types.TypeDynamic:
		return buildStringArray(rows, colIdx, n), nil
	default:
		return buildStringArray(rows, colIdx, n), nil
	}
}

func buildI64Array(rows []types.Row, colIdx, n int) *vortex.Array {
	buf := make([]byte, n*8)
	for i, row := range rows {
		var v int64
		if colIdx < len(row) && row[colIdx] != nil {
			v = types.ToInt64(row[colIdx])
		}
		binary.LittleEndian.PutUint64(buf[i*8:], uint64(v))
	}
	return &vortex.Array{
		DType:   &vortex.DType{Kind: vortex.DTypePrimitive, PType: vortex.PTypeI64},
		Length:  n,
		Buffers: [][]byte{buf},
	}
}

func buildI32Array(rows []types.Row, colIdx, n int) *vortex.Array {
	buf := make([]byte, n*4)
	for i, row := range rows {
		var v int32
		if colIdx < len(row) && row[colIdx] != nil {
			v = int32(types.ToInt64(row[colIdx]))
		}
		binary.LittleEndian.PutUint32(buf[i*4:], uint32(v))
	}
	return &vortex.Array{
		DType:   &vortex.DType{Kind: vortex.DTypePrimitive, PType: vortex.PTypeI32},
		Length:  n,
		Buffers: [][]byte{buf},
	}
}

func buildF64Array(rows []types.Row, colIdx, n int) *vortex.Array {
	buf := make([]byte, n*8)
	for i, row := range rows {
		var v float64
		if colIdx < len(row) && row[colIdx] != nil {
			v = types.ToFloat64(row[colIdx])
		}
		binary.LittleEndian.PutUint64(buf[i*8:], math.Float64bits(v))
	}
	return &vortex.Array{
		DType:   &vortex.DType{Kind: vortex.DTypePrimitive, PType: vortex.PTypeF64},
		Length:  n,
		Buffers: [][]byte{buf},
	}
}

func buildBoolArray(rows []types.Row, colIdx, n int) *vortex.Array {
	// Bool: bit-packed, 1 bit per value, little-endian byte order
	byteLen := (n + 7) / 8
	buf := make([]byte, byteLen)
	for i, row := range rows {
		if colIdx < len(row) && row[colIdx] != nil {
			if b, ok := row[colIdx].(bool); ok && b {
				buf[i/8] |= 1 << uint(i%8)
			}
		}
	}
	return &vortex.Array{
		DType:   &vortex.DType{Kind: vortex.DTypeBool},
		Length:  n,
		Buffers: [][]byte{buf},
	}
}

func buildStringArray(rows []types.Row, colIdx, n int) *vortex.Array {
	// VarBinView encoding: 16 bytes per view
	// Short strings (≤12 bytes): inline in the view
	// Long strings (>12 bytes): prefix + bufIdx + offset into data buffer
	viewsBuf := make([]byte, n*16)
	var dataBuf []byte

	for i, row := range rows {
		var s string
		if colIdx < len(row) && row[colIdx] != nil {
			s = fmt.Sprintf("%v", row[colIdx])
		}

		off := i * 16
		sLen := len(s)
		binary.LittleEndian.PutUint32(viewsBuf[off:], uint32(sLen))

		if sLen <= 12 {
			copy(viewsBuf[off+4:], s)
		} else {
			copy(viewsBuf[off+4:off+8], s[:4])
			binary.LittleEndian.PutUint32(viewsBuf[off+8:], 0) // bufIdx = 0
			binary.LittleEndian.PutUint32(viewsBuf[off+12:], uint32(len(dataBuf)))
			dataBuf = append(dataBuf, s...)
		}
	}

	bufs := [][]byte{viewsBuf}
	if len(dataBuf) > 0 {
		bufs = append(bufs, dataBuf)
	}

	return &vortex.Array{
		DType:   &vortex.DType{Kind: vortex.DTypeUtf8},
		Length:  n,
		Buffers: bufs,
	}
}

// --- Database-wide shared dictionary: write-path integration ---
//
// See shareddict.go for the full design rationale. This section wires
// the per-extent write decision (dict-encode via the shared dictionary,
// or fall back to ordinary flat string storage) into the existing
// KQL-schema -> Vortex-schema and rows -> Vortex-arrays bridge.

// dictDecision is the resolved per-column outcome of checking a shared
// dictionary against one extent's worth of rows: either dict-encode
// (with the dictionary state as it will be AFTER this extent's new
// values are committed, and the code width that state requires), or nil
// meaning this column falls back to ordinary flat string encoding for
// this extent (today's existing high-cardinality behavior).
type dictDecision struct {
	dict      *sharedDict
	codePType vortex.PType
}

// resolveDictDecisions checks every TypeString column against its
// database-wide shared dictionary and decides, once for the whole
// extent, whether to dict-encode it. A column dict-encodes only if its
// shared dictionary's total size (existing entries + this extent's new
// distinct values) stays within sharedDictCap; otherwise it falls back
// to flat encoding for this extent, without extending the dictionary —
// self-stabilizing rather than a one-way sticky decision, since a later
// extent for the same column gets to try again against whatever
// headroom exists at that point.
//
// Only plain types.TypeString columns are considered (not GUID/Dynamic,
// which already have their own extension wrapping and are typically
// either high-cardinality or small enough that the extent-scoped
// dictionary vortex-go already ships is sufficient — scoping this to
// the categorical-column case it was actually measured against).
//
// Extends and commits the on-disk dictionaries for any columns that
// decide to dict-encode BEFORE returning, so the returned decisions are
// immediately usable for array-building — SaveExtent's chunk loop never
// needs to touch the dictionary store itself.
// dictCardinalityRatioThreshold gates dictionary encoding on whether a
// column actually shows repetition within the extent being written —
// staying under sharedDictCap only answers "is this affordable to
// keep as a dictionary," not "does this column benefit from being
// one." A structurally-unique column (a row's own Id, an EdgeId) is
// affordable forever (one distinct value per row, cap is only ever
// reached after tens of thousands of rows) while providing zero
// deduplication the entire time — every dictionary entry is used
// exactly once, and the three bookkeeping files (.dict/.dict.count/
// .dict.lock) it silently accumulates are pure overhead for no
// benefit. Found live: a real scope's dictionaries/ directory held a
// dictionary for every string column including Id and EdgeId, each
// holding one entry per row with no reuse. 0.5 matches the threshold
// vortex-go's own per-extent dictionary encoding already uses for the
// same judgment at a different layer (writer.go's
// finalizeStringColumns) — kept consistent rather than picked fresh.
const dictCardinalityRatioThreshold = 0.5

func resolveDictDecisions(e *Engine, tableName string, schema *types.Schema, rows []types.Row) (map[string]*dictDecision, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	// _Dictionaries' own write must never try to dictionary-encode
	// itself — its TableName/ColumnName/Value columns are all
	// TypeString, and without this exclusion writing a new dictionary
	// entry would recurse back into this same function (and the same,
	// non-reentrant lock) for _Dictionaries' own columns. Checked
	// first, unconditionally, before any cardinality/cap logic.
	if tableName == dictionariesTableName {
		return nil, nil
	}
	decisions := make(map[string]*dictDecision)
	for colIdx, col := range schema.Columns {
		if col.Type != types.TypeString {
			continue
		}

		distinct := make(map[string]struct{})
		nonNull := 0
		for _, row := range rows {
			if colIdx >= len(row) || row[colIdx] == nil {
				continue
			}
			distinct[fmt.Sprintf("%v", row[colIdx])] = struct{}{}
			nonNull++
		}

		// Skip entirely — no dictionary files touched or created at
		// all — for a column that looks structurally unique within
		// THIS write. Not a sticky decision: a later, larger, or more
		// repetitive extent for the same column gets to try again,
		// exactly like the sharedDictCap fallback below already does.
		// A single-row write always looks 100% distinct for every
		// column trivially (one row can't show internal repetition),
		// which is a real, known blind spot of a per-extent local
		// ratio — the fix is bulk writes seeing enough rows at once
		// for genuine repetition to actually show up in the sample,
		// not a smarter (and much more complex) global-frequency
		// tracker for what's meant to be a lightweight per-write check.
		//
		// This check runs BEFORE "" is added below, deliberately: "" is
		// a forced, always-present candidate for null-safety (see
		// below), not a genuine signal about this column's real
		// cardinality, and padding the ratio with it here would make
		// the gate stricter than intended for small/moderate writes
		// (e.g. 3 rows all "open": ratio 1/3 without padding correctly
		// passes; with "" counted, 2/3 incorrectly fails).
		if nonNull > 0 && float64(len(distinct))/float64(nonNull) > dictCardinalityRatioThreshold {
			continue
		}

		// "" is unconditionally a candidate value from here on, even if
		// no row in THIS write contains it — see the file-level note
		// above buildDictRefArray for why this is load-bearing, not
		// cosmetic: it guarantees "" occupies code 0 the very first
		// time this column is ever dict-ref encoded, and (append-only,
		// codes never move once assigned) permanently after that. That
		// in turn is what makes null-as-code-0 correct rather than
		// merely convenient: without it, code 0 could legitimately
		// belong to whatever real value happened to be dictionary
		// entry zero, and a null row would silently decode as THAT
		// value — or, if the dictionary had zero entries at all,
		// panic on an out-of-range lookup. Confirmed live, not
		// hypothetical: a real scope hit exactly this (Edges.Basis
		// left empty/null for structural edges by design) as both
		// symptoms depending on extent layout — confirmed by direct
		// investigation of that incident. Added AFTER the cardinality
		// gate above, not before — see that comment for why the
		// ordering matters.
		distinct[""] = struct{}{}

		// resolveAndExtendSharedDict does the whole reload-under-lock,
		// filter-to-genuinely-new, cap-check, append-and-commit
		// sequence atomically — see shareddict.go. capped==true means
		// this column falls back to flat encoding for this extent
		// only; the dictionary itself is left untouched, so a later
		// extent gets to try again against whatever headroom exists
		// then (self-stabilizing, not a one-way sticky decision).
		newSD, capped, err := extendTableDict(e, tableName, col.Name, distinct)
		if err != nil {
			return nil, fmt.Errorf("resolve shared dictionary for %s.%s: %w", tableName, col.Name, err)
		}
		if capped {
			continue
		}
		decisions[col.Name] = &dictDecision{
			dict:      newSD,
			codePType: dictCodePType(len(newSD.values)),
		}
	}
	if len(decisions) == 0 {
		return nil, nil
	}
	return decisions, nil
}

// schemaToVortexDTypeWithDict is schemaToVortexDType, but columns with
// a resolved dictDecision are written as kql.dictref (integer codes)
// instead of their normal Vortex dtype.
func schemaToVortexDTypeWithDict(schema *types.Schema, decisions map[string]*dictDecision) *vortex.DType {
	if len(decisions) == 0 {
		return schemaToVortexDType(schema)
	}
	fieldNames := make([]string, len(schema.Columns))
	fieldTypes := make([]*vortex.DType, len(schema.Columns))
	for i, col := range schema.Columns {
		fieldNames[i] = col.Name
		if d, ok := decisions[col.Name]; ok {
			fieldTypes[i] = &vortex.DType{
				Kind:        vortex.DTypeExtension,
				ExtensionID: extKQLDictRef,
				StorageDType: &vortex.DType{
					Kind:  vortex.DTypePrimitive,
					PType: d.codePType,
				},
			}
			continue
		}
		fieldTypes[i] = kqlTypeToVortexDType(col.Type)
	}
	return &vortex.DType{
		Kind:       vortex.DTypeStruct,
		FieldNames: fieldNames,
		FieldTypes: fieldTypes,
	}
}

// rowsToVortexArraysWithDict is rowsToVortexArrays, but columns with a
// resolved dictDecision are built as plain integer code arrays
// (buildDictRefArray) instead of buildStringArray.
func rowsToVortexArraysWithDict(schema *types.Schema, rows []types.Row, decisions map[string]*dictDecision) (map[string]*vortex.Array, error) {
	if len(decisions) == 0 {
		return rowsToVortexArrays(schema, rows)
	}
	n := len(rows)
	if n == 0 {
		return nil, fmt.Errorf("no rows to convert")
	}
	columns := make(map[string]*vortex.Array, len(schema.Columns))
	for colIdx, col := range schema.Columns {
		if d, ok := decisions[col.Name]; ok {
			columns[col.Name] = buildDictRefArray(rows, colIdx, n, d.dict, d.codePType)
			continue
		}
		arr, err := buildColumnArray(col.Type, rows, colIdx, n)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", col.Name, err)
		}
		columns[col.Name] = arr
	}
	return columns, nil
}

// buildDictRefArray builds a plain unsigned-integer codes array for a
// dict-encoded column: every value is looked up in dict.codeOf (present
// by construction — resolveDictDecisions already extended and committed
// the dictionary to cover every distinct value in these rows before
// this is called, AND unconditionally includes "" as a candidate,
// guaranteeing it occupies code 0 — see resolveDictDecisions for the
// full reasoning). Null/missing cells encode as code 0, matching this
// codebase's existing null-as-zero convention for numeric columns
// (storage.go's ScanExtent comment: "storage round-trips nil numerics
// as 0"). Unlike an earlier version of this function, code 0 is NOT
// just assumed to be safe here — it's guaranteed to be "" specifically
// by resolveDictDecisions's unconditional inclusion, which is what
// makes this fallback correct rather than merely convenient: without
// that guarantee, code 0 could legitimately belong to whatever real
// value happened to be dictionary entry zero (silent value
// substitution for null rows) or the dictionary could have zero
// entries at all (out-of-range panic in colVec.value) — both
// confirmed live against real data, not hypothetical failure modes.
func buildDictRefArray(rows []types.Row, colIdx, n int, dict *sharedDict, codePType vortex.PType) *vortex.Array {
	codeOf := func(row types.Row) uint32 {
		if colIdx >= len(row) || row[colIdx] == nil {
			return 0
		}
		s := fmt.Sprintf("%v", row[colIdx])
		return dict.codeOf[s]
	}

	switch codePType {
	case vortex.PTypeU8:
		buf := make([]byte, n)
		for i, row := range rows {
			buf[i] = byte(codeOf(row))
		}
		return &vortex.Array{
			DType:   &vortex.DType{Kind: vortex.DTypePrimitive, PType: vortex.PTypeU8},
			Length:  n,
			Buffers: [][]byte{buf},
		}
	case vortex.PTypeU16:
		buf := make([]byte, n*2)
		for i, row := range rows {
			binary.LittleEndian.PutUint16(buf[i*2:], uint16(codeOf(row)))
		}
		return &vortex.Array{
			DType:   &vortex.DType{Kind: vortex.DTypePrimitive, PType: vortex.PTypeU16},
			Length:  n,
			Buffers: [][]byte{buf},
		}
	default:
		buf := make([]byte, n*4)
		for i, row := range rows {
			binary.LittleEndian.PutUint32(buf[i*4:], codeOf(row))
		}
		return &vortex.Array{
			DType:   &vortex.DType{Kind: vortex.DTypePrimitive, PType: vortex.PTypeU32},
			Length:  n,
			Buffers: [][]byte{buf},
		}
	}
}
