package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
)

func BenchmarkSparseExactMatch(b *testing.B) {
	cat, err := catalog.Open("/tmp/bigdb")
	if err != nil {
		b.Skipf("benchmark db not present: %v", err)
	}
	eng := New(cat)
	stmt, err := parser.Parse(`Events | where Score == 47.336 | summarize max(Id) by Host`)
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
