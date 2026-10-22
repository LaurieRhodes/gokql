package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// column_ifexists_test.go — value, type and name of column_ifexists,
// and that the column it names is read from storage even though it
// appears only as a string.

func TestColumnIfExistsReference(t *testing.T) {
	// Reference: project column_ifexists("Capital", State) gives the
	// State column when Capital does not exist.
	r := queryResult(t, `datatable(State:string, n:long)["WA", 1, "TX", 2]
		| project column_ifexists("Capital", State)`)
	assertColumns(t, columnNames(&r.Schema), "State")
	assertRows(t, r.Rows, [][]interface{}{{"WA"}, {"TX"}})
}

func TestColumnIfExistsPresentAndTypes(t *testing.T) {
	r := queryResult(t, `datatable(State:string, Capital:string, n:long)["WA", "Olympia", 1]
		| project column_ifexists("Capital", State), a = column_ifexists("n", 0),
		  b = column_ifexists("zz", "none"), c = columnifexists("State", "")`)
	assertColumns(t, columnNames(&r.Schema), "Capital", "a", "b", "c")
	assertRows(t, r.Rows, [][]interface{}{{"Olympia", int64(1), "none", "WA"}})
	if r.Schema.Columns[1].Type != types.TypeLong {
		t.Errorf("a type = %v, want long", r.Schema.Columns[1].Type)
	}
}

func TestColumnIfExistsRequiresConstantName(t *testing.T) {
	if _, err := queryErr(`datatable(s:string)["n"] | extend x = column_ifexists(s, 0)`); err == nil {
		t.Error("expected an error for a non-constant column name")
	}
}

// Column projection pushdown must load a column named only inside
// column_ifexists, or the default would always win.
func TestColumnIfExistsReadsColumnFromStorage(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.set-or-append T <| datatable(State:string, Capital:string, n:long)["WA", "Olympia", 1, "TX", "Austin", 2]`)
	r := diskQuery(t, eng, `T | where n > 0 | project c = column_ifexists("Capital", State)`)
	expectRows(t, r, 2)
	expectCell(t, r, 0, 0, "Olympia")
	r = diskQuery(t, eng, `T | summarize n = countif(column_ifexists("Capital", "") == "Austin")`)
	expectCell(t, r, 0, 0, "1")
}
