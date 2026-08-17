package engine

import (
	"testing"

	vortex "github.com/LaurieRhodes/vortex-go"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// --- selectChunkRows unit tests (pure vector logic) ---

func i64Vec(vals ...int64) *colVec  { return &colVec{kt: types.TypeLong, i64: vals} }
func f64Vec(vals ...float64) *colVec { return &colVec{kt: types.TypeReal, f64: vals} }

func selCount(t *testing.T, filter *vortex.RowFilter, cols map[string]*colVec, n int) (int, int) {
	t.Helper()
	_, count, applied := selectChunkRows(filter, cols, n)
	return count, applied
}

func TestSelectChunkRowsRangesAndEquality(t *testing.T) {
	cols := map[string]*colVec{"X": i64Vec(1, 2, 3, 4, 5)}

	if c, a := selCount(t, vortex.NewRowFilter(vortex.GT("X", int64(3))), cols, 5); c != 2 || a != 1 {
		t.Fatalf("GT 3: got count=%d applied=%d", c, a)
	}
	if c, _ := selCount(t, vortex.NewRowFilter(vortex.GTE("X", int64(3))), cols, 5); c != 3 {
		t.Fatalf("GTE 3: got %d", c)
	}
	if c, _ := selCount(t, vortex.NewRowFilter(vortex.EQ("X", int64(4))), cols, 5); c != 1 {
		t.Fatalf("EQ 4: got %d", c)
	}
	if c, _ := selCount(t, vortex.NewRowFilter(vortex.NEQ("X", int64(4))), cols, 5); c != 4 {
		t.Fatalf("NEQ 4: got %d", c)
	}
	if c, _ := selCount(t, vortex.NewRowFilter(vortex.LT("X", int64(1))), cols, 5); c != 0 {
		t.Fatalf("LT 1 (no match): got %d", c)
	}
}

func TestSelectChunkRowsAndConjunction(t *testing.T) {
	cols := map[string]*colVec{
		"X": i64Vec(1, 2, 3, 4, 5),
		"Y": f64Vec(10, 20, 30, 40, 50),
	}
	f := vortex.NewRowFilter(vortex.GT("X", int64(1)), vortex.LT("Y", 45.0))
	if c, a := selCount(t, f, cols, 5); c != 3 || a != 2 {
		t.Fatalf("X>1 AND Y<45: got count=%d applied=%d (want 3, 2)", c, a)
	}
}

func TestSelectChunkRowsConservativeSkips(t *testing.T) {
	cols := map[string]*colVec{"X": i64Vec(1, 2, 3)}

	// Float literal against an int64 column cannot be compared exactly:
	// the predicate must be skipped (applied=0, all rows kept).
	if c, a := selCount(t, vortex.NewRowFilter(vortex.GT("X", 1.5)), cols, 3); c != 3 || a != 0 {
		t.Fatalf("float-vs-i64 must skip: got count=%d applied=%d", c, a)
	}

	// Unknown column: skipped.
	if c, a := selCount(t, vortex.NewRowFilter(vortex.GT("Missing", int64(1))), cols, 3); c != 3 || a != 0 {
		t.Fatalf("missing column must skip: got count=%d applied=%d", c, a)
	}

	// Int literal against a real column IS exact (within 2^53).
	fcols := map[string]*colVec{"Y": f64Vec(1.0, 2.5, 3.0)}
	if c, a := selCount(t, vortex.NewRowFilter(vortex.GTE("Y", int64(3))), fcols, 3); c != 1 || a != 1 {
		t.Fatalf("int-vs-f64 exact: got count=%d applied=%d", c, a)
	}

	// Nil filter: nil bitmap fast path.
	if sel, c, _ := selectChunkRows(nil, cols, 3); sel != nil || c != 3 {
		t.Fatalf("nil filter fast path broken")
	}
}

// --- integration: chunk filtering + zone extraction scope on disk ---

// filterEngine builds a disk table with two extents:
//   extent 1: Sev = Id (0..99),   Score = Id * 1.0
//   extent 2: Sev = Id (100..199), Score = Id * 1.0
func filterEngine(t *testing.T) *Engine {
	t.Helper()
	eng := diskEngineWith(t, `.create table F (Sev: long, Score: real, Host: string)`, func(ext, i int) string {
		id := ext*100 + i
		return itoa(id) + "," + itoa(id) + ".0,host" + itoa(id%3)
	})
	return eng
}

func itoa(n int) string {
	return types.FormatValue(int64(n), types.TypeLong)
}

// diskEngineWith is a generalized disk fixture: two extents of 100 rows.
func diskEngineWith(t *testing.T, createStmt string, rowFn func(ext, i int) string) *Engine {
	t.Helper()
	eng := diskEngineEmpty(t)
	diskExec(t, eng, createStmt)
	for ext := 0; ext < 2; ext++ {
		rows := make([]string, 0, 100)
		for i := 0; i < 100; i++ {
			rows = append(rows, rowFn(ext, i))
		}
		diskExec(t, eng, ".ingest inline into table F <| "+joinLines(rows))
	}
	return eng
}

func joinLines(rows []string) string {
	out := ""
	for i, r := range rows {
		if i > 0 {
			out += "\n"
		}
		out += r
	}
	return out
}

func diskEngineEmpty(t *testing.T) *Engine {
	t.Helper()
	cat, err := catalog.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	return New(cat)
}

func diskExec(t *testing.T, eng *Engine, q string) *types.Table {
	t.Helper()
	stmt, err := parser.Parse(q)
	if err != nil {
		t.Fatalf("parse %q: %v", q, err)
	}
	result, err := eng.Execute(stmt)
	if err != nil {
		t.Fatalf("execute %q: %v", q, err)
	}
	return result
}

func TestChunkFilterCorrectness(t *testing.T) {
	eng := filterEngine(t)

	cases := []struct {
		q    string
		want string
	}{
		{`F | where Sev > 150 | count`, "49"},
		{`F | where Sev >= 150 | count`, "50"},
		{`F | where Sev == 137 | count`, "1"},
		{`F | where Sev != 137 | count`, "199"},
		{`F | where Sev > 50 and Sev < 60 | count`, "9"},
		{`F | where Score > 150.5 | count`, "49"},
		{`F | where Sev > 50 | where Score < 60.0 | count`, "9"},   // leading consecutive wheres
		{`F | where Sev > 1000 | count`, "0"},                      // nothing matches
		{`F | where Sev > 2.5 | count`, "197"},                     // float-vs-long: conservative skip, where decides
		{`F | where Host == "host1" and Sev > 150 | count`, "17"},  // mixed pushable + unpushable conjuncts
	}
	for _, tc := range cases {
		tbl := diskQuery(t, eng, tc.q)
		expectCell(t, tbl, 0, 0, tc.want)
	}
}

func TestZoneFilterStopsAtFirstNonWhere(t *testing.T) {
	eng := filterEngine(t)

	// project-rename aliases Sev onto Score's data. The where predicate
	// must NOT be pushed against the stored Sev column. Previously the
	// pushed predicate pruned zones by the wrong column's values.
	tbl := diskQuery(t, eng,
		`F | project-rename SevOrig = Sev, Sev = Score | where Sev > 150.5 | count`)
	expectCell(t, tbl, 0, 0, "49")

	// extend shadowing a stored name: same hazard.
	tbl = diskQuery(t, eng,
		`F | extend Combined = Sev * 1000 | where Combined > 150000 | count`)
	expectCell(t, tbl, 0, 0, "49")
}
