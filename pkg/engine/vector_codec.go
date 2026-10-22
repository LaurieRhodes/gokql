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
	"fmt"
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
		f32 := float32(f)
		// Reject non-finite values at the point they would be written.
		//
		// A NaN anywhere in a stored vector makes cosineSimilarity
		// return NaN. NaN compares false against everything, so
		// "top N by sim desc" does not error, does not rank it last,
		// and does not rank it first -- the ordering becomes an
		// artefact of the sort's comparison order. That is a silent
		// wrong answer, and once written it is indistinguishable from
		// a real vector.
		//
		// The check is on the float32 value, not the float64 input:
		// 1e300 is perfectly finite as a float64 and becomes +Inf on
		// narrowing, so checking before the conversion would let it
		// through.
		if math.IsNaN(float64(f32)) || math.IsInf(float64(f32), 0) {
			return "", fmt.Errorf("vector element %d is not finite (%v); "+
				"a non-finite value produces a NaN similarity score that ranks "+
				"unpredictably instead of erroring", i, f)
		}
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f32))
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
	// Strict() rejects non-canonical encodings, where the unused
	// trailing bits of the final base64 character are non-zero. Go's
	// default decoder silently ignores those bits, so "...000000==" and
	// "...00000w==" decode to identical bytes -- two distinct on-disk
	// strings for one vector. Found by FuzzDecodeCompactVector in
	// under a second.
	//
	// It is not a ranking bug (both decode the same), but it means a
	// stored vector has no canonical form, so any byte-level
	// comparison of two Embedding values can report a difference that
	// does not exist. Strict decoding makes the representation
	// one-to-one and rejects that class of corruption outright.
	payload := s[len(vectorPrefix):]

	// Reject anything outside the base64 alphabet BEFORE decoding.
	// Go's decoder skips \r and \n by design (a MIME convenience), so
	// "kql.vec32:\n" decodes happily as a zero-length vector and
	// whitespace anywhere in a payload is silently ignored. Found by
	// FuzzDecodeCompactVector. Same motivation as Strict() below: a
	// stored vector must have exactly one representation.
	for i := 0; i < len(payload); i++ {
		c := payload[i]
		ok := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '+' || c == '/' || c == '='
		if !ok {
			return nil, false
		}
	}

	raw, err := base64.StdEncoding.Strict().DecodeString(payload)
	if err != nil || len(raw)%4 != 0 {
		// A corrupted vector must fail closed rather than decode to a
		// shorter one: a truncated vector of the right shape would
		// produce a plausible similarity score, which is the failure
		// class this whole area exists to prevent.
		return nil, false
	}
	out := make([]float64, len(raw)/4)
	for i := range out {
		f := float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:])))
		if math.IsNaN(f) || math.IsInf(f, 0) {
			// encodeVector cannot produce these, so a non-finite value
			// on disk means corruption. Fail closed for the same
			// reason the length check does: a plausible-looking wrong
			// score is worse than a missing one.
			return nil, false
		}
		out[i] = f
	}
	return out, true
}

// encodeVectorJSON is the legacy form, kept for VectorEncoding="json"
// and for embed_text(), whose result is a transient query-time value
// that callers routinely inspect with array_length and friends.
func encodeVectorJSON(v []float64) (string, error) {
	for i, f := range v {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return "", fmt.Errorf("vector element %d is not finite (%v)", i, f)
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
