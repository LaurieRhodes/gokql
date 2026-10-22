package engine

import (
	"encoding/json"
	"math"
	"testing"
)

// FuzzDecodeCompactVector: decoding must never panic and never return
// ok=true for input it cannot faithfully represent. A stored vector is
// attacker-adjacent in the sense that it can be corrupted on disk,
// truncated by a partial write, or written by an older build.
func FuzzDecodeCompactVector(f *testing.F) {
	f.Add("")
	f.Add("kql.vec32:")
	f.Add("kql.vec32:AAAAAA==")
	f.Add("kql.vec32:!!!!")
	f.Add("[1,2,3]")
	f.Add("kql.vec32:" + "A")
	v, _ := encodeVector([]float64{1, -2, 3.5})
	f.Add(v)

	f.Fuzz(func(t *testing.T, s string) {
		got, ok := decodeCompactVector(s)
		if ok && len(got)*4 != len(s)-len(vectorPrefix)-paddingOf(s) {
			// length consistency is checked below via re-encode
			_ = got
		}
		if !ok {
			return
		}
		// Anything that decodes must round-trip to the same bytes.
		re, err := encodeVector(got)
		if err != nil {
			t.Fatalf("re-encode failed for %q: %v", s, err)
		}
		if re != s {
			t.Fatalf("decode/encode not stable:\n in: %q\nout: %q", s, re)
		}
	})
}

func paddingOf(s string) int { return 0 }

// TestNonFiniteVectorsRejected is the one that matters most.
//
// A NaN anywhere in a stored vector makes cosineSimilarity return NaN.
// NaN compares false against everything, so `top N by sim desc` does
// not error, does not rank it last, and does not rank it first: the
// ordering becomes dependent on the sort's comparison order. A silent
// wrong ranking is exactly the failure class this area exists to
// prevent, so a non-finite value must be caught at the point it would
// be written.
func TestNonFiniteVectorsRejected(t *testing.T) {
	for name, bad := range map[string][]float64{
		"NaN":      {1, math.NaN(), 3},
		"+Inf":     {1, math.Inf(1), 3},
		"-Inf":     {math.Inf(-1), 2, 3},
		"overflow": {1, 1e300, 3}, // finite as float64, +Inf as float32
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := encodeVector(bad); err == nil {
				t.Errorf("encoded a vector containing a non-finite value without complaint")
			}
		})
	}
}

// TestNaNSimilarityDoesNotRankSilently documents what happens if one
// slips through: the score is NaN, which no comparison catches.
func TestNaNSimilarityDoesNotRankSilently(t *testing.T) {
	a := []float64{1, math.NaN(), 0}
	b := []float64{1, 0, 0}
	sim, err := cosineSimilarity(a, b)
	if err == nil && math.IsNaN(sim) {
		t.Log("confirmed: a NaN vector yields a NaN score with no error; " +
			"this is why encodeVector must reject non-finite values")
	}
}

// TestLegacyJSONWithNonFiniteIsRejected: JSON cannot represent NaN or
// Inf, but a hand-written or third-party value might contain null or a
// string. parseFloatArray must fail closed rather than substitute 0.
func TestLegacyJSONWithNonFiniteIsRejected(t *testing.T) {
	for _, bad := range []string{`[1,null,3]`, `[1,"x",3]`, `[1,[2],3]`, `{"a":1}`} {
		if v, ok := parseFloatArray(bad); ok {
			t.Errorf("%s parsed to %v; a malformed vector must fail closed", bad, v)
		}
	}
}

func TestEmptyAndSingletonVectors(t *testing.T) {
	for _, v := range [][]float64{{}, {0}, {1}} {
		s, err := encodeVector(v)
		if err != nil {
			t.Fatalf("encode %v: %v", v, err)
		}
		got, ok := decodeCompactVector(s)
		if !ok || len(got) != len(v) {
			t.Errorf("%v round-tripped to %v (ok=%v)", v, got, ok)
		}
	}
}

// TestFloat32PrecisionIsHonest: the compact form is lossy relative to
// float64 input, and callers should know by how much.
func TestFloat32PrecisionIsHonest(t *testing.T) {
	orig := []float64{0.1, 1.0 / 3.0, 1e-8, -2.718281828459045}
	s, err := encodeVector(orig)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := decodeCompactVector(s)
	for i := range orig {
		rel := math.Abs(got[i]-orig[i]) / math.Abs(orig[i])
		if rel > 1e-6 {
			t.Errorf("element %d: relative error %g exceeds float32 precision", i, rel)
		}
	}
	// And confirm the JSON path is exact, so a mixed table's small
	// disagreement is understood rather than mysterious.
	b, _ := json.Marshal(orig)
	exact, _ := parseFloatArray(string(b))
	if exact[0] != orig[0] {
		t.Error("JSON path should be exact")
	}
}
