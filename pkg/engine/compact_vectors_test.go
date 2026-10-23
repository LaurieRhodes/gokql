package engine

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// .compact table T with (vectors=compact): vector columns become the
// compact encoding, everything else (other dynamic columns, ingest
// times) is unchanged, and similarity ranking is the same.
func TestCompactVectors(t *testing.T) {
	eng := discoverEngine(t, t.TempDir())
	diskExec(t, eng, `.create table E (Id: string, Vec: dynamic, Meta: dynamic, Small: dynamic)`)
	for ext := 0; ext < 2; ext++ {
		var rows []string
		for i := 0; i < 30; i++ {
			id := ext*30 + i
			v := make([]float64, 32)
			for k := range v {
				v[k] = math.Sin(float64(id*7 + k))
			}
			js, _ := encodeVectorJSON(v)
			if id == 5 {
				js, _ = encodeVectorCompact(v) // already compact: kept
			}
			vec := `"` + strings.ReplaceAll(js, `"`, `\"`) + `"`
			if id == 9 {
				vec = `dynamic(null)`
			}
			rows = append(rows, fmt.Sprintf(`"e%d",%s,dynamic({"k":%d}),dynamic([1,2,3])`, id, vec, id))
		}
		diskExec(t, eng, `.set-or-append E <| datatable(Id:string, Vec:dynamic, Meta:dynamic, Small:dynamic)[`+strings.Join(rows, ",")+`]`)
	}
	q := `let qv = toscalar(E | where Id == "e3" | project Vec); E | extend s = series_cosine_similarity(Vec, qv) | top 5 by s desc | project Id`
	before := tableCSV(t, diskQuery(t, eng, q))
	times := tableCSV(t, diskQuery(t, eng, `E | project Id, _TimeReceived, Meta, Small | order by Id asc`))

	res := diskQuery(t, eng, `.compact table E with (vectors=compact)`)
	if got := cell(t, res, 0, 7); got != "Vec" {
		t.Fatalf("VectorColumns = %q, want Vec", got)
	}
	if got := tableCSV(t, diskQuery(t, eng, q)); got != before {
		t.Errorf("ranking changed:\nbefore\n%s\nafter\n%s", before, got)
	}
	if got := tableCSV(t, diskQuery(t, eng, `E | project Id, _TimeReceived, Meta, Small | order by Id asc`)); got != times {
		t.Errorf("other columns changed")
	}
	n := diskQuery(t, eng, `E | where tostring(Vec) startswith "kql.vec32:" | count`)
	if got := cell(t, n, 0, 0); got != "59" { // 60 rows, one null
		t.Errorf("compact vectors: %s, want 59", got)
	}
	diskQueryError(t, eng, `.compact table E with (vectors=fast)`)
}

func TestChunkRowsFor(t *testing.T) {
	narrow := make([]types.Row, 100)
	for i := range narrow {
		narrow[i] = types.Row{int64(i), "x"}
	}
	if got := chunkRowsFor(narrow); got != ZoneChunkSize {
		t.Errorf("narrow rows: %d chunk rows, want %d", got, ZoneChunkSize)
	}
	wide := make([]types.Row, 100)
	big := strings.Repeat("0.123456789,", 1300) // ~15 KB, an embedding as JSON
	for i := range wide {
		wide[i] = types.Row{"id", big}
	}
	got := chunkRowsFor(wide)
	if got >= ZoneChunkSize || got < minChunkRows || int64(got)*int64(len(big)) > 2*chunkTargetBytes {
		t.Errorf("wide rows: %d chunk rows", got)
	}
}

// .gc removes superseded files under extents/ as well as the root.
func TestGCExtentsSubdir(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table G (X: long)`)
	diskExec(t, eng, `.set-or-append G <| datatable(X:long)[1]`)
	diskExec(t, eng, `.set-or-append G <| datatable(X:long)[2]`)
	sub := filepath.Join(dir, "extents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(sub, "G_00000000deadbeef.vtx.superseded")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	diskExec(t, eng, `.gc table G`)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("superseded file under extents/ survived .gc")
	}
}
