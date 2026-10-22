package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
)

// After .alter-merge or .create-merge adds a column, positional ingest
// supplies the visible columns in order and _TimeReceived stays last:
// it used to sit before the new column, so a row had to carry a
// placeholder for it and a value meant for the new column landed in
// _TimeReceived.
func TestAlterMergeKeepsTimeReceivedLast(t *testing.T) {
	for _, alter := range []string{
		`.alter-merge table S (Late: string, N: long)`,
		`.create-merge table S (Id: long, Name: string, Late: string, N: long)`,
	} {
		t.Run(alter[:13], func(t *testing.T) {
			dir := t.TempDir()
			eng := discoverEngine(t, dir)
			diskExec(t, eng, `.create table S (Id: long, Name: string)`)
			diskExec(t, eng, ".ingest inline into table S <| 1,a\n2,b")
			diskExec(t, eng, alter)
			diskExec(t, eng, ".ingest inline into table S <| 3,c,late,7")

			for name, e := range map[string]*Engine{"same engine": eng, "fresh": discoverEngine(t, dir)} {
				cols := e.Catalog.GetTable("S").Schema.Columns
				if last := cols[len(cols)-1].Name; last != catalog.TimeReceivedColumn {
					t.Errorf("%s: last column %s, want %s", name, last, catalog.TimeReceivedColumn)
				}
				tbl := diskQuery(t, e, `S | sort by Id asc | project Id, Name, Late, N, HasTR = isnotnull(_TimeReceived)`)
				expectRows(t, tbl, 3)
				expectCell(t, tbl, 2, 2, "late")
				expectCell(t, tbl, 2, 3, "7")
				expectCell(t, tbl, 2, 4, "true")
				expectCell(t, tbl, 0, 4, "true")
				if v := cellVal(t, tbl, 0, 2); v != nil && v != "" {
					t.Errorf("%s: pre-alter row Late = %v, want empty", name, v)
				}
			}
			expectColNames(t, diskQuery(t, eng, `S | getschema | project ColumnName`), "ColumnName")
			gs := diskQuery(t, eng, `S | getschema | project ColumnName`)
			expectRows(t, gs, 4)
			expectCell(t, gs, 3, 0, "N")
		})
	}
}
