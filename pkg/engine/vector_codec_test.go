package engine

import (
	"encoding/json"
	"math"
	"math/rand"
	"testing"
)

func randVec(dim int) []float64 {
	v := make([]float64, dim)
	for i := range v {
		v[i] = float64(float32(rand.NormFloat64()))
	}
	return v
}

func TestCompactRoundTrip(t *testing.T) {
	v := randVec(768)
	s, err := encodeVector(v)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decodeCompactVector(s)
	if !ok {
		t.Fatal("did not decode")
	}
	if len(got) != len(v) {
		t.Fatalf("width %d, want %d", len(got), len(v))
	}
	for i := range v {
		if math.Abs(got[i]-v[i]) > 1e-6 {
			t.Fatalf("element %d: %v != %v", i, got[i], v[i])
		}
	}
}

// TestJSONVectorsStillReadable is the no-migration guarantee: tables
// embedded before the compact encoding must keep working, including a
// table with a mix of old and new extents.
func TestJSONVectorsStillReadable(t *testing.T) {
	v := randVec(64)
	b, _ := json.Marshal(v)
	got, ok := parseFloatArray(string(b))
	if !ok {
		t.Fatal("legacy JSON vector no longer parses")
	}
	if len(got) != 64 {
		t.Fatalf("width %d, want 64", len(got))
	}

	compact, _ := encodeVector(v)
	sim, err := cosineSimilarity(mustParse(t, string(b)), mustParse(t, compact))
	if err != nil {
		t.Fatal(err)
	}
	if sim < 0.99999 {
		t.Errorf("a JSON vector and its compact twin scored %v against each other; "+
			"a mixed-encoding table would rank incorrectly", sim)
	}
}

// TestCorruptCompactFailsClosed: a truncated vector of the right shape
// would produce a plausible similarity score.
func TestCorruptCompactFailsClosed(t *testing.T) {
	for _, bad := range []string{
		vectorPrefix + "not!base64!",
		vectorPrefix + "AAA=", // 2 bytes, not a multiple of 4
	} {
		if _, ok := decodeCompactVector(bad); ok {
			t.Errorf("%q decoded; it must fail closed", bad)
		}
	}
}

// TestArrayLengthSeesThroughEncoding: a storage optimisation is not
// allowed to change the language.
func TestArrayLengthSeesThroughEncoding(t *testing.T) {
	s, _ := encodeVector(randVec(12))
	arr, ok := parseJSONArray(s)
	if !ok || len(arr) != 12 {
		t.Fatalf("generic dynamic path saw %d elements (ok=%v), want 12", len(arr), ok)
	}
}

func mustParse(t *testing.T, s string) []float64 {
	t.Helper()
	v, ok := parseFloatArray(s)
	if !ok {
		t.Fatalf("parse failed: %.40s", s)
	}
	return v
}

// --- the numbers ---

func benchDecode(b *testing.B, dim int, compact bool) {
	v := randVec(dim)
	var stored string
	if compact {
		stored, _ = encodeVector(v)
	} else {
		raw, _ := json.Marshal(v)
		stored = string(raw)
	}
	b.SetBytes(int64(len(stored)))
	b.ReportMetric(float64(len(stored)), "stored_bytes")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := parseFloatArray(stored); !ok {
			b.Fatal("decode failed")
		}
	}
}

func BenchmarkDecodeJSON768(b *testing.B)    { benchDecode(b, 768, false) }
func BenchmarkDecodeCompact768(b *testing.B) { benchDecode(b, 768, true) }
func BenchmarkDecodeJSON384(b *testing.B)    { benchDecode(b, 384, false) }
func BenchmarkDecodeCompact384(b *testing.B) { benchDecode(b, 384, true) }
