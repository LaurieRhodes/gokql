package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// BenchmarkFilteredGroupBy profiles the decode-bound shape against the
// 1M-row benchmark database in /tmp/cmpdb (created by the DuckDB
// scoreboard harness; skip when absent).
func BenchmarkFilteredGroupBy(b *testing.B) {
	cat, err := catalog.Open("/tmp/cmpdb")
	if err != nil {
		b.Skipf("benchmark db not present: %v", err)
	}
	eng := New(cat)
	stmt, err := parser.Parse(`Events | where Sev == 5 | summarize S = sum(Score), M = max(Id) by Host`)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := eng.Execute(stmt); err != nil {
			b.Fatal(err)
		}
	}
}
