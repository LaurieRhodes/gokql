package engine

// columnar.go — Phase 1 of columnar execution: chunk-level predicate
// filtering on unboxed vectors, before any row materialization.
//
// Vortex hands the scan typed columnar arrays; previously ScanExtent
// boxed every cell into types.Value and transposed the entire chunk to
// rows, leaving the downstream WhereOp to discard most of them one at a
// time. Here the pushable predicates (the same set extractZoneFilter
// derives) are evaluated directly against typed slices ([]int64,
// []float64, ...) to produce a selection, and only selected rows are
// ever boxed and transposed. Chunks with an empty selection are skipped
// entirely.
//
// CORRECTNESS CONTRACT: selection may over-include (rows that fail the
// predicate) but must NEVER exclude a row the predicate matches — the
// WhereOp remains in the pipeline and re-filters exactly. Any
// predicate/column combination this file cannot evaluate exactly is
// skipped (contributing "select all" for that predicate). Nulls need no
// special handling at this layer: storage round-trips nil numerics as 0,
// so the filter sees precisely the values the row engine would.

import (
	"fmt"
	"math"

	vortex "github.com/LaurieRhodes/vortex-go"
	"github.com/LaurieRhodes/vortex-go/encoding"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// colVec is the unboxed decode of one chunk column. Exactly one slice is
// populated, chosen by the column's KQL type. Boxing happens per-element
// via value(), only for rows that survive selection.
type colVec struct {
	kt  types.KQLType
	i64 []int64   // long, datetime, timespan
	i32 []int32   // int
	f64 []float64 // real
	b   []bool    // bool
	str []string  // string, guid, dynamic, fallback (flat encoding)

	// Dictionary-encoded strings stay encoded: per-row codes indexing
	// a small distinct-values table. Operators compute over codes
	// (integer compares, array-indexed grouping) and resolve to
	// strings only at output boundaries. dictVals entries share
	// backing with the decoded dictionary, so value() never copies.
	dictCodes []int
	dictVals  []string
}

// decodeColumnVec decodes a Vortex array into its typed slice form.
//
// e and tableName+colName are used only for the kql.dictref case (a
// database-wide shared-dictionary column — see shareddict.go): they
// resolve which table+column's shared dictionary file to load. Every
// other type ignores them; callers not touching dictref columns may
// pass a nil e (e.g. tests decoding a plain array in isolation).
func decodeColumnVec(e *Engine, tableName, colName string, arr *vortex.Array, kt types.KQLType) (*colVec, error) {
	c := &colVec{kt: kt}
	var err error
	switch kt {
	case types.TypeLong, types.TypeDatetime, types.TypeTimespan:
		c.i64, err = encoding.Int64Values(arr)
	case types.TypeInt:
		c.i32, err = encoding.Int32Values(arr)
	case types.TypeReal:
		c.f64, err = encoding.Float64Values(arr)
	case types.TypeBool:
		c.b, err = encoding.BoolValues(arr)
	default:
		if isDictRefArray(arr) {
			return decodeDictRefColumnVec(e, tableName, colName, arr, kt)
		}
		// Preserve dictionary encoding when present: computing over
		// codes is the point of the columnar layer. Fall back to flat
		// decode otherwise.
		var ok bool
		c.dictCodes, c.dictVals, ok, err = encoding.DictStringParts(arr)
		if err == nil && !ok {
			c.str, err = encoding.StringValues(arr)
		}
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// value boxes element i as a types.Value. Out-of-range indices yield nil
// (ragged chunks from schema evolution).
func (c *colVec) value(i int) types.Value {
	switch {
	case c.i64 != nil:
		if i < len(c.i64) {
			return c.i64[i]
		}
	case c.i32 != nil:
		if i < len(c.i32) {
			return c.i32[i]
		}
	case c.f64 != nil:
		if i < len(c.f64) {
			return c.f64[i]
		}
	case c.b != nil:
		if i < len(c.b) {
			return c.b[i]
		}
	case c.dictCodes != nil:
		if i < len(c.dictCodes) {
			return c.dictVals[c.dictCodes[i]]
		}
	case c.str != nil:
		if i < len(c.str) {
			return c.str[i]
		}
	}
	return nil
}

// exactInt64 converts a predicate value to int64 without loss. Returns
// false for floats and anything else inexact — the caller skips the
// predicate (conservative).
func exactInt64(v interface{}) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case int32:
		return int64(x), true
	case int16:
		return int64(x), true
	case int8:
		return int64(x), true
	case uint8:
		return int64(x), true
	case uint16:
		return int64(x), true
	case uint32:
		return int64(x), true
	case uint64:
		if x <= math.MaxInt64 {
			return int64(x), true
		}
	}
	return 0, false
}

// exactFloat64 converts a predicate value to float64 without loss.
// Integers beyond 2^53 are rejected (inexact in float64).
func exactFloat64(v interface{}) (float64, bool) {
	if f, ok := v.(float64); ok {
		return f, true
	}
	if f, ok := v.(float32); ok {
		return float64(f), true
	}
	if i, ok := exactInt64(v); ok {
		if i >= -(1<<53) && i <= 1<<53 {
			return float64(i), true
		}
	}
	return 0, false
}

func cmpMatches(op vortex.CompareOp, cmp int) bool {
	switch op {
	case vortex.OpGT:
		return cmp > 0
	case vortex.OpGTE:
		return cmp >= 0
	case vortex.OpLT:
		return cmp < 0
	case vortex.OpLTE:
		return cmp <= 0
	case vortex.OpEQ:
		return cmp == 0
	case vortex.OpNEQ:
		return cmp != 0
	}
	return true // unknown op: keep the row (conservative)
}

// applyPredicateVec narrows sel (a bitmap of still-selected rows) by one
// predicate evaluated against its column vector. Returns false when the
// predicate cannot be evaluated exactly, in which case sel is untouched.
func applyPredicateVec(pred vortex.ColumnPredicate, c *colVec, sel []bool) bool {
	switch {
	case c.i64 != nil:
		v, ok := exactInt64(pred.Value)
		if !ok {
			return false
		}
		for i, x := range c.i64 {
			if !sel[i] {
				continue
			}
			cmp := 0
			if x < v {
				cmp = -1
			} else if x > v {
				cmp = 1
			}
			if !cmpMatches(pred.Op, cmp) {
				sel[i] = false
			}
		}
		return true

	case c.i32 != nil:
		v, ok := exactInt64(pred.Value)
		if !ok {
			return false
		}
		for i, x := range c.i32 {
			if !sel[i] {
				continue
			}
			cmp := 0
			if int64(x) < v {
				cmp = -1
			} else if int64(x) > v {
				cmp = 1
			}
			if !cmpMatches(pred.Op, cmp) {
				sel[i] = false
			}
		}
		return true

	case c.f64 != nil:
		v, ok := exactFloat64(pred.Value)
		if !ok {
			return false
		}
		for i, x := range c.f64 {
			if !sel[i] {
				continue
			}
			cmp := 0
			if x < v {
				cmp = -1
			} else if x > v {
				cmp = 1
			}
			if !cmpMatches(pred.Op, cmp) {
				sel[i] = false
			}
		}
		return true
	}
	return false // non-numeric column vector: not evaluable here
}

// selectChunkRows evaluates the filter's predicates (ANDed) against the
// decoded chunk columns. Returns (selection bitmap, selected count,
// applied count). A nil bitmap means every row is selected and no
// predicate work happened (fast path for filterless scans).
func selectChunkRows(filter *vortex.RowFilter, cols map[string]*colVec, rowCount int) ([]bool, int, int) {
	if filter == nil || len(filter.Predicates) == 0 || rowCount == 0 {
		return nil, rowCount, 0
	}

	sel := make([]bool, rowCount)
	for i := range sel {
		sel[i] = true
	}

	applied := 0
	for _, pred := range filter.Predicates {
		c, ok := cols[pred.Column]
		if !ok || c == nil {
			continue // column not decoded in this chunk: skip predicate
		}
		if applyPredicateVec(pred, c, sel) {
			applied++
		}
	}
	if applied == 0 {
		return nil, rowCount, 0
	}

	count := 0
	for _, s := range sel {
		if s {
			count++
		}
	}
	return sel, count, applied
}

// ensure fmt stays imported if diagnostics are added later

// --- Database-wide shared dictionary: read-path integration ---
//
// See shareddict.go for the design and vortex_bridge.go's write-path
// section for how these columns get written. On disk a dictref column
// is nothing but a plain unsigned-integer array (u8/u16/u32) — vortex-go
// itself has no idea it's dictionary-shaped. isDictRefArray/
// decodeDictRefColumnVec are entirely engine-side: decode the integer
// codes normally, then populate colVec's existing dictCodes/dictVals
// fields (the same fields vortex-go's own extent-scoped Dict layout
// populates) from the resolved shared dictionary — every downstream
// consumer (aggregation's group-on-code fast path, filtering, output
// boxing) already works on those fields unchanged.

// isDictRefArray detects a database-wide-shared-dictionary column at
// decode time. It CANNOT check for the kql.dictref Extension marker
// directly: vortex-go's decoder resolves every Extension dtype down to
// its physical StorageDType before an array reaches the caller (see
// encoding.decodeWithContext's extensionAware allowlist — dictref isn't
// in it, deliberately, since it needs no extension-specific decode
// logic, only plain unsigned-integer decode). By the time this runs,
// the schema-level kql.dictref wrapper is already gone.
//
// The signal used instead: decodeColumnVec is only ever called with
// kt == types.TypeString (or GUID/Dynamic, which never use this path)
// for this array, yet the array itself decoded as a plain unsigned
// PRIMITIVE integer, not Utf8/Binary/VarBinView. No other codepath
// produces that combination — an ordinary flat or vortex-go-dict-
// encoded string column always reports Utf8/Binary at this layer — so
// it's an unambiguous, if indirect, marker.
func isDictRefArray(arr *vortex.Array) bool {
	if arr == nil || arr.DType == nil || arr.DType.Kind != vortex.DTypePrimitive {
		return false
	}
	switch arr.DType.PType {
	case vortex.PTypeU8, vortex.PTypeU16, vortex.PTypeU32:
		return true
	default:
		return false
	}
}

func decodeDictRefColumnVec(e *Engine, tableName, colName string, arr *vortex.Array, kt types.KQLType) (*colVec, error) {
	sd, err := e.getSharedDict(tableName, colName)
	if err != nil {
		return nil, fmt.Errorf("resolve shared dictionary for %s.%s: %w", tableName, colName, err)
	}

	// arr.DType at this point is already the resolved PHYSICAL storage
	// type — the kql.dictref Extension wrapper never survives to here
	// (see isDictRefArray's comment), so PType is read directly, not
	// via a StorageDType field that only exists on Extension-kind
	// DTypes and would always be nil on this already-resolved Primitive
	// DType.
	codes := make([]int, arr.Length)
	switch arr.DType.PType {
	case vortex.PTypeU8:
		vals, err := encoding.Uint8Values(arr)
		if err != nil {
			return nil, err
		}
		for i, v := range vals {
			codes[i] = int(v)
		}
	case vortex.PTypeU16:
		vals, err := encoding.Uint16Values(arr)
		if err != nil {
			return nil, err
		}
		for i, v := range vals {
			codes[i] = int(v)
		}
	default:
		vals, err := encoding.Uint32Values(arr)
		if err != nil {
			return nil, err
		}
		for i, v := range vals {
			codes[i] = int(v)
		}
	}

	return &colVec{
		kt:        kt,
		dictCodes: codes,
		dictVals:  sd.values,
	}, nil
}
