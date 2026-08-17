package engine

import (
	"encoding/json"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// TestPagePoolStress alternates between two databases with different
// extent/page characteristics across many iterations, specifically to
// exercise pagePaddedPool's buffer-reuse path with varying page sizes
// (a smaller page reusing a buffer left behind by a larger one) many
// times over, checking correctness on every single iteration rather
// than once — the failure mode this guards against (stale bytes in the
// padding region) would only show up intermittently, not necessarily
// on the first call.
func TestPagePoolStress(t *testing.T) {
	smallCat, err := catalog.Open("/tmp/cmpdb")
	if err != nil {
		t.Skipf("benchmark db not present: %v", err)
	}
	bigCat, err := catalog.Open("/tmp/bigdb")
	if err != nil {
		t.Skipf("benchmark db not present: %v", err)
	}
	smallEng := New(smallCat)
	bigEng := New(bigCat)

	smallStmt, err := parser.Parse(`Events | summarize min(Score), max(Score), sum(Score) by Sev`)
	if err != nil {
		t.Fatal(err)
	}
	bigStmt, err := parser.Parse(`Events | where Score == 47.336 | summarize max(Id) by Host`)
	if err != nil {
		t.Fatal(err)
	}

	var wantSmall, wantBig string
	for i := 0; i < 60; i++ {
		var eng *Engine
		var stmt = smallStmt
		if i%2 == 0 {
			eng = smallEng
		} else {
			eng = bigEng
			stmt = bigStmt
		}
		result, err := eng.Execute(stmt)
		if err != nil {
			t.Fatalf("iteration %d: execute failed: %v", i, err)
		}
		b, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("iteration %d: marshal failed: %v", i, err)
		}
		got := string(b)
		if i%2 == 0 {
			if wantSmall == "" {
				wantSmall = got
			} else if got != wantSmall {
				t.Fatalf("iteration %d (small db): result changed across iterations!\nfirst:\n%s\ngot:\n%s", i, wantSmall, got)
			}
		} else {
			if wantBig == "" {
				wantBig = got
			} else if got != wantBig {
				t.Fatalf("iteration %d (big db): result changed across iterations!\nfirst:\n%s\ngot:\n%s", i, wantBig, got)
			}
		}
	}
}

