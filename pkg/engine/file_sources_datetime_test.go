package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// csv() and json() datetimes are engine datetimes: they compare with
// datetime literals and aggregate (they were time.Time values, which
// broke both).
func TestFileSourceDatetimes(t *testing.T) {
	dir := t.TempDir()
	c := filepath.Join(dir, "a.csv")
	j := filepath.Join(dir, "a.json")
	os.WriteFile(c, []byte("T,v\n2026-10-01T08:00:00Z,1\n2026-10-01T09:00:00Z,2\n"), 0o644)
	os.WriteFile(j, []byte(`[{"T":"2026-10-01T08:00:00Z"},{"T":"2026-10-01T09:00:00Z"}]`), 0o644)
	for _, src := range []string{`csv("` + c + `")`, `json("` + j + `")`} {
		tbl := queryResult(t, src+` | where T > datetime(2026-10-01 08:30) | summarize n = count(), m = max(T)`)
		expectCell(t, tbl, 0, 0, "1")
		expectCell(t, tbl, 0, 1, "2026-10-01T09:00:00Z")
	}
}
