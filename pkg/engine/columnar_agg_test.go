package engine

import (
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// aggEngine builds a disk table exercising every supported column kind:
// 300 rows over 2 extents.
//   Id:    0..299 (long)
//   Small: Id % 100 (int)
//   Score: Id * 0.5 (real)
//   Host:  host{Id % 5} (string)
func aggEngine(t *testing.T) *Engine {
	t.Helper()
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table A (Id: long, Small: int, Score: real, Host: string)`)
	for ext := 0; ext < 2; ext++ {
		rows := make([]string, 0, 150)
		for i := 0; i < 150; i++ {
			id := ext*150 + i
			rows = append(rows,
				itoa(id)+","+itoa(id%100)+","+
					types.FormatValue(float64(id)*0.5, types.TypeReal)+",host"+itoa(id%5))
		}
		diskExec(t, eng, ".ingest inline into table A <| "+joinLines(rows))
	}
	return eng
}

// tableCSV renders a result deterministically for comparison.
func tableCSV(t *testing.T, tbl *types.Table) string {
	t.Helper()
	var b strings.Builder
	for _, col := range tbl.Schema.Columns {
		b.WriteString(col.Name + ":" + string(rune('0'+int(col.Type))) + ";")
	}
	b.WriteByte('\n')
	for _, row := range tbl.Rows {
		for i, col := range tbl.Schema.Columns {
			b.WriteString(types.FormatValue(row[i], col.Type))
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// TestColumnarAggregateDifferential runs each query through the
// columnar fast path and through the row engine (forced by a
// planner-defeating `where 1 == 1` identity prefix) and requires
// byte-identical results: schema names, types, group order, values.
func TestColumnarAggregateDifferential(t *testing.T) {
	eng := aggEngine(t)

	queries := []string{
		// aggregates, grouped and global
		`A | summarize C = count() by Host`,
		`A | summarize C = count()`,
		`A | summarize S = sum(Id) by Host`,
		`A | summarize S = sum(Score) by Host`,
		`A | summarize V = avg(Score) by Host`,
		`A | summarize M = min(Id), X = max(Id) by Host`,
		`A | summarize M = min(Score), X = max(Score), C = count(), S = sum(Small) by Host`,
		`A | summarize M = min(Small), X = max(Small) by Host`,
		// filters: numeric ranges, string equality, conjunctions,
		// consecutive wheres, empty result
		`A | where Id > 200 | summarize C = count() by Host`,
		`A | where Id >= 100 and Id < 200 | summarize S = sum(Id) by Host`,
		`A | where Host == "host3" | summarize C = count(), S = sum(Score)`,
		`A | where Host != "host3" | where Score > 100.0 | summarize C = count() by Host`,
		`A | where Id > 5000 | summarize C = count() by Host`,
		// numeric and multi-column group keys
		`A | summarize C = count() by Small`,
		`A | summarize C = count() by Host, Small`,
		`A | summarize S = sum(Id) by Score`,
	}

	for _, q := range queries {
		fast := tableCSV(t, diskQuery(t, eng, q))
		slow := tableCSV(t, diskQuery(t, eng, `A | where 1 == 1 `+q[strings.Index(q, "|"):]))
		if fast != slow {
			t.Errorf("differential mismatch for %q:\ncolumnar:\n%s\nrow engine:\n%s", q, fast, slow)
		}
	}
}

// TestColumnarAggregateValues pins a few absolute results so both
// engines can't drift together.
func TestColumnarAggregateValues(t *testing.T) {
	eng := aggEngine(t)

	// 300 rows, 5 hosts → 60 each.
	tbl := diskQuery(t, eng, `A | summarize C = count() by Host | sort by Host asc`)
	expectRows(t, tbl, 5)
	expectCell(t, tbl, 0, 0, "60")

	// Ids 201..299 → 99 rows.
	tbl = diskQuery(t, eng, `A | where Id > 200 | summarize C = count()`)
	expectCell(t, tbl, 0, 0, "99")

	// sum(Id) for host0 (Ids ≡ 0 mod 5): 0+5+...+295 = 5*(0+..+59) = 8850.
	tbl = diskQuery(t, eng, `A | where Host == "host0" | summarize S = sum(Id)`)
	expectCell(t, tbl, 0, 0, "8850")

	// min/max keep the column's type: max(Score) = 299*0.5 = 149.5.
	tbl = diskQuery(t, eng, `A | summarize X = max(Score)`)
	expectCell(t, tbl, 0, 0, "149.5")
}

// TestColumnarAggregatePlanGate verifies unsupported shapes fall back
// to the row engine and still answer correctly.
func TestColumnarAggregatePlanGate(t *testing.T) {
	eng := aggEngine(t)

	// contains: not an exact predicate → row path.
	tbl := diskQuery(t, eng, `A | where Host contains "st1" | summarize C = count()`)
	expectCell(t, tbl, 0, 0, "60")

	// dcount: unsupported aggregate → row path.
	tbl = diskQuery(t, eng, `A | summarize D = dcount(Host)`)
	expectCell(t, tbl, 0, 0, "5")

	// bin() by-expression → row path.
	tbl = diskQuery(t, eng, `A | summarize C = count() by B = bin(Id, 100) | sort by B asc`)
	expectRows(t, tbl, 3)
	expectCell(t, tbl, 0, 0, "100")

	// float literal against a long column → row path (exactness).
	tbl = diskQuery(t, eng, `A | where Id > 199.5 | summarize C = count()`)
	expectCell(t, tbl, 0, 0, "100")
}
