package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// BenchmarkFullScanGroupBy is a permanent regression benchmark for the
// database-wide shared dictionary (shareddict.go): an unfiltered scan
// forces decode of the Host column across every extent, which is
// exactly the shape that motivated moving the dictionary out of
// per-extent Vortex files and into a single shared, engine-resolved
// artifact. Build /tmp/profdb by ingesting /tmp/bigdb.csv (25M rows,
// 5000-distinct-Host synthetic benchmark — see the extent-size-sweep
// session for the generator recipe) into a fresh database; skips if
// absent, matching this codebase's existing benchmark convention.
func BenchmarkFullScanGroupBy(b *testing.B) {
	cat, err := catalog.Open("/tmp/profdb")
	if err != nil {
		b.Skipf("benchmark db not present: %v", err)
	}
	eng := New(cat)
	stmt, err := parser.Parse(`Events | summarize S = sum(Score), M = max(Id) by Host`)
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
