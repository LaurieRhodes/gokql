package engine

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// A similarity extend over enough rows runs in parallel; it must give
// the serial result row for row (the serial path is forced by adding a
// function outside the parallel set to the same extend).
func TestParallelSimilarityExtend(t *testing.T) {
	src := `let qv = toscalar(print v = pack_array(1.0, 2.0, 0.5));
		range i from 1 to 20000 step 1
		| extend Embedding = tostring(pack_array(i % 7, (i % 11) * 2.0, 1.0))`
	par := queryResult(t, src+` | extend sim = series_cosine_similarity(Embedding, qv) | summarize s = sum(sim), m = max(sim), c = countif(isnull(sim)), f = take_any(i)`)
	ser := queryResult(t, src+` | extend sim = series_cosine_similarity(Embedding, qv), x = strcat("", i) | summarize s = sum(sim), m = max(sim), c = countif(isnull(sim)), f = take_any(i)`)
	for c := 0; c < 3; c++ {
		if a, b := cell(t, par, 0, c), cell(t, ser, 0, c); a != b {
			t.Errorf("column %d: parallel %s, serial %s", c, a, b)
		}
	}
	// Order is preserved.
	ord := queryResult(t, src+` | extend sim = series_cosine_similarity(Embedding, qv) | take 3 | project i`)
	for r, want := range []string{"1", "2", "3"} {
		if got := cell(t, ord, r, 0); got != want {
			t.Errorf("row %d: i = %s, want %s", r, got, want)
		}
	}
	x, err := parser.ParseExpr(`series_cosine_similarity(Embedding, qv)`)
	if err != nil {
		t.Fatal(err)
	}
	if !parallelExtendable([]parser.Expr{x}) {
		t.Error("expected the similarity extend to be parallel")
	}
	y, _ := parser.ParseExpr(`series_cosine_similarity(Embedding, qv) + row_number()`)
	if parallelExtendable([]parser.Expr{y}) {
		t.Error("row_number must keep the extend serial")
	}
}

// scanJSONFloatArray must agree with encoding/json exactly, and refuse
// (leaving the general decoder to decide) anything that is not a flat
// array of JSON numbers.
func TestScanJSONFloatArray(t *testing.T) {
	good := []string{
		`[]`, ` [ ] `, `[1]`, `[0.027629789,-0.15138465,0.00048631427]`,
		`[1e-7, -2.5E+3 ,3]`, "[\n1,\t2\r]", `[-0, 1.7976931348623157e308]`,
	}
	for _, s := range good {
		got, ok := scanJSONFloatArray(s)
		var want []float64
		if err := json.Unmarshal([]byte(s), &want); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if !ok || len(got) != len(want) {
			t.Fatalf("%s: got %v ok=%v, want %v", s, got, ok, want)
		}
		for i := range got {
			if math.Float64bits(got[i]) != math.Float64bits(want[i]) {
				t.Errorf("%s[%d]: %v != %v", s, i, got[i], want[i])
			}
		}
	}
	for _, s := range []string{`[1,]`, `[,1]`, `[1 2]`, `[null]`, `["1"]`, `[[1]]`, `[+1]`, `[1]x`, `1`, `[1`, `[inf]`, `[0x10]`, `[1e]`} {
		if v, ok := scanJSONFloatArray(s); ok {
			t.Errorf("%s: accepted as %v", s, v)
		}
	}
}
