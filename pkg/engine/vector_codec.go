package engine

// vector_codec.go -- compact storage encoding for embedding vectors.
//
// Stored vectors were JSON arrays of float64. Measured on a 768-dim
// corpus: ~15 KB of text per vector, and json.Unmarshal accounting for
// roughly 90% of the time in a similarity ranking (~8,000 rows/s
// single-threaded at 768 dims). The decode path went through
// parseJSONArray, which does fmt.Sprintf("%v", val) to get a string
// and then unmarshals into []interface{} -- a string allocation plus
// an interface box per element, for data that is just numbers.
//
// The compact form is float32 little-endian, base64'd, behind a
// "kql.vec32:" prefix. That mirrors the kql.dictref convention already
// used for shared-dictionary columns, so the value stays a plain
// string in a dynamic column and nothing in the storage or result-
// marshalling layers needs to know about it.
//
// float32 is not a precision compromise worth worrying about here:
// every embedding backend in use produces float32 internally and
// widens it on the way out. Storing float64 preserved a precision the
// data never had.
//
// BACKWARD COMPATIBLE BY DESIGN. decodeVector accepts both forms, so
// tables embedded before this change keep working with no migration,
// and a mixed table (old extents plus new) ranks correctly. There is
// no flag day.

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"strings"
)

// vectorPrefix marks the compact encoding.
const vectorPrefix = "kql.vec32:"

// VectorEncoding selects what .embed-into WRITES. Reading always
// accepts both. "compact" is the default; set "json" to write the old
// form, for a bisect or to hand a table to something that parses the
// column itself.
var VectorEncoding = "compact"

// encodeVector renders a vector in the configured storage form.
func encodeVector(v []float64) (string, error) {
	if VectorEncoding == "json" {
		return encodeVectorJSON(v)
	}
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(float32(f)))
	}
	var sb strings.Builder
	sb.Grow(len(vectorPrefix) + base64.StdEncoding.EncodedLen(len(buf)))
	sb.WriteString(vectorPrefix)
	sb.WriteString(base64.StdEncoding.EncodeToString(buf))
	return sb.String(), nil
}

// decodeCompactVector decodes the compact form. ok is false for
// anything that isn't compact, so callers can fall through to JSON.
func decodeCompactVector(s string) ([]float64, bool) {
	if !strings.HasPrefix(s, vectorPrefix) {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(s[len(vectorPrefix):])
	if err != nil || len(raw)%4 != 0 {
		// A corrupted vector must fail closed rather than decode to a
		// shorter one: a truncated vector of the right shape would
		// produce a plausible similarity score, which is the failure
		// class this whole area exists to prevent.
		return nil, false
	}
	out := make([]float64, len(raw)/4)
	for i := range out {
		out[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:])))
	}
	return out, true
}

// encodeVectorJSON is the legacy form, kept for VectorEncoding="json"
// and for embed_text(), whose result is a transient query-time value
// that callers routinely inspect with array_length and friends.
func encodeVectorJSON(v []float64) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
