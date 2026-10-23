package engine

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// vecEngine: table V(Id, Project, Vec) over three extents; vectors are
// JSON in two extents and the compact encoding in the third, with a
// null and an unparsable vector.
func vecEngine(t *testing.T) *Engine {
	t.Helper()
	eng := discoverEngine(t, t.TempDir())
	diskExec(t, eng, `.create table V (Id: string, Project: string, Vec: dynamic)`)
	id := 0
	for ext := 0; ext < 3; ext++ {
		var rows []string
		for i := 0; i < 40; i++ {
			id++
			// Distinct scores (no ties): KQL leaves the order of ties
			// open, so a tie at the cutoff could legitimately differ.
			f := float64(id)
			v := []float64{math.Sin(f), math.Cos(f * 1.3), 0.25 + f/1000, math.Sin(f*0.7) * 0.5}
			var enc string
			if ext == 2 {
				s, err := encodeVector(v)
				if err != nil {
					t.Fatal(err)
				}
				enc = s
			} else {
				b, _ := encodeVectorJSON(v)
				enc = b
			}
			proj := []string{"a", "b"}[id%2]
			if id == 17 {
				rows = append(rows, fmt.Sprintf(`"r%d","%s",dynamic(null)`, id, proj))
				continue
			}
			if id == 18 {
				rows = append(rows, fmt.Sprintf(`"r%d","%s","not a vector"`, id, proj))
				continue
			}
			rows = append(rows, fmt.Sprintf(`"r%d","%s","%s"`, id, proj, strings.ReplaceAll(enc, `"`, `\"`)))
		}
		diskExec(t, eng, `.set-or-append V <| datatable(Id:string, Project:string, Vec:dynamic)[`+strings.Join(rows, ",")+`]`)
	}
	return eng
}

func TestColumnarSimilarityTopN(t *testing.T) {
	eng := vecEngine(t)
	cases := []string{
		`V | extend sim = series_cosine_similarity(Vec, qv) | top 5 by sim desc | project Id, sim`,
		`V | extend sim = series_cosine_similarity(qv, Vec) | top 5 by sim desc | project Id, sim`,
		`V | where Project == "a" | extend sim = series_cosine_similarity(Vec, qv) | top 4 by sim desc`,
		`V | extend s = series_dot_product(Vec, qv) | sort by s desc | take 6 | project Id, s`,
		`V | extend sim = series_cosine_similarity(Vec, qv) | top 3 by sim asc | project Id, sim`,
	}
	let := `let qv = dynamic([1.0, -1.0, 0.5, 2.0]); `
	for _, c := range cases {
		stmt := strings.Replace(c, "V |", "V | where 1 == 1 |", 1)
		fast := tableCSV(t, diskQuery(t, eng, let+c))
		slow := tableCSV(t, diskQuery(t, eng, let+stmt))
		if fast != slow {
			t.Errorf("%s:\ncolumnar:\n%s\nrow engine:\n%s", c, fast, slow)
		}
	}
	// The retrieval shape takes the columnar path.
	def := eng.Catalog.GetTable("V")
	q := mustParseQuery(t, `V | extend sim = series_cosine_similarity(Vec, dynamic([1.0, 0.0, 0.0, 0.0])) | top 5 by sim desc`)
	if planColumnarTopN(eng, q.Operators, &def.Schema) == nil {
		t.Error("expected a columnar similarity top-N plan")
	}
	// A length mismatch is an error on both paths.
	diskQueryError(t, eng, `V | extend sim = series_cosine_similarity(Vec, dynamic([1.0, 2.0])) | top 3 by sim desc`)
}

func mustParseQuery(t *testing.T, q string) *parser.Query {
	t.Helper()
	stmt, err := parser.Parse(q)
	if err != nil {
		t.Fatal(err)
	}
	pq, ok := stmt.(*parser.Query)
	if !ok {
		t.Fatalf("%s: not a query", q)
	}
	return pq
}
