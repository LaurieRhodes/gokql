package engine

import (
	"strings"
	"testing"
)

// TestColumnarTopNDifferential runs each query through the columnar
// top-N path and through the row engine (forced by a planner-defeating
// `where 1 == 1` identity prefix) and requires byte-identical results.
// Sort keys are tie-free (Id unique, Score = Id*0.5 unique) so the
// heap's unstable tie handling cannot differ from the stable sort.
func TestColumnarTopNDifferential(t *testing.T) {
	eng := aggEngine(t)

	queries := []string{
		`A | sort by Id desc | take 7`,
		`A | sort by Id asc | take 7`,
		`A | sort by Score desc | take 10`,
		`A | top 5 by Score desc`,
		`A | top 5 by Score asc`,
		// filtered
		`A | where Id > 100 | sort by Score desc | take 5`,
		`A | where Host == "host2" | sort by Id asc | take 4`,
		`A | where Host != "host0" | where Score > 50.0 | top 6 by Id desc`,
		// n exceeds matching rows; empty result
		`A | where Id > 290 | sort by Id desc | take 100`,
		`A | where Id > 5000 | sort by Id desc | take 5`,
		// string sort key (host names unique per group boundary? no —
		// ties exist, so pair with a tiebreaker clause)
		`A | sort by Host asc, Id asc | take 8`,
		// post-take operators run on the streamed result
		`A | sort by Id desc | take 5 | project Host, Id`,
		`A | top 5 by Id desc | summarize C = count() by Host`,
	}

	for _, q := range queries {
		fast := tableCSV(t, diskQuery(t, eng, q))
		slow := tableCSV(t, diskQuery(t, eng, `A | where 1 == 1 `+q[strings.Index(q, "|"):]))
		if fast != slow {
			t.Errorf("differential mismatch for %q:\ncolumnar:\n%s\nrow engine:\n%s", q, fast, slow)
		}
	}
}

// TestColumnarTopNValues pins absolute results.
func TestColumnarTopNValues(t *testing.T) {
	eng := aggEngine(t)

	tbl := diskQuery(t, eng, `A | sort by Id desc | take 3 | project Id`)
	expectRows(t, tbl, 3)
	expectCell(t, tbl, 0, 0, "299")
	expectCell(t, tbl, 2, 0, "297")

	// Filter + top: max Id for host1 (Ids ≡ 1 mod 5) is 296.
	tbl = diskQuery(t, eng, `A | where Host == "host1" | top 1 by Id desc | project Id`)
	expectCell(t, tbl, 0, 0, "296")
}

// TestColumnarTopNPlanGate verifies unsupported shapes fall back to
// the row engine and still answer correctly.
func TestColumnarTopNPlanGate(t *testing.T) {
	eng := aggEngine(t)

	// contains predicate → row path.
	tbl := diskQuery(t, eng, `A | where Host contains "st4" | sort by Id desc | take 2 | project Id`)
	expectCell(t, tbl, 0, 0, "299")

	// N over the columnar cap → row path (applyTopN full-sort branch).
	tbl = diskQuery(t, eng, `A | sort by Id asc | take 20000`)
	expectRows(t, tbl, 300)

	// sort without take (full ordered output) → row path.
	tbl = diskQuery(t, eng, `A | where Id < 3 | sort by Id desc`)
	expectRows(t, tbl, 3)
	expectCell(t, tbl, 0, 0, "2")
}
