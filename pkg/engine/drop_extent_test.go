package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// drop_extent_test.go — extent ids are unique across tables in a
// catalog database (a flush's data extent and its _Dictionaries
// extent used to share one), and .drop extent never guesses between
// tables.

func TestExtentIDsUniqueAcrossTables(t *testing.T) {
	eng := diskEngineEmpty(t)
	// Low-cardinality string columns are dictionary-encoded, so each
	// flush that introduces new values also writes a _Dictionaries
	// extent; that nested write used to take the same id.
	for i := 0; i < 3; i++ {
		diskExec(t, eng, `.set-or-append A <| range i from 1 to 300 step 1 | extend S = strcat("a`+itoa(i)+`-", i % 3)`)
		diskExec(t, eng, `.set-or-append B <| range i from 1 to 300 step 1 | extend S = strcat("b`+itoa(i)+`-", i % 4)`)
	}
	if len(eng.Catalog.Database.Tables["_Dictionaries"].Extents) == 0 {
		t.Fatal("test setup: expected dictionary extents to be written")
	}
	seen := map[string]string{}
	for name, tbl := range eng.Catalog.Database.Tables {
		for _, ext := range tbl.Extents {
			if prev, dup := seen[ext.ID]; dup {
				t.Errorf("extent id %s used by both %s and %s", ext.ID, prev, name)
			}
			seen[ext.ID] = name
		}
	}
}

func TestDropExtentFromTable(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.set-or-append A <| datatable(S:string)["x"]`)
	diskExec(t, eng, `.set-or-append A <| datatable(S:string)["y"]`)
	id := eng.Catalog.Database.Tables["A"].Extents[0].ID
	diskExec(t, eng, `.drop extent `+id+` from A`)
	r := diskQuery(t, eng, `A`)
	expectRows(t, r, 1)
	expectCell(t, r, 0, 0, "y")
	// String data still decodes after the drop: the shared dictionary
	// was not touched.
	diskExec(t, eng, `.set-or-append A <| datatable(S:string)["z"]`)
	expectRows(t, diskQuery(t, eng, `A | where S in ("y", "z")`), 2)
}

func TestDropExtentAmbiguousIDRefused(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.set-or-append A <| datatable(S:string)["x"]`)
	diskExec(t, eng, `.set-or-append B <| datatable(S:string)["y"]`)
	// Recreate the pre-fix state on disk: B's extent file carries A's
	// id. (Execute rediscovers the directory, so an in-memory edit
	// would not survive to the statement.)
	a := eng.Catalog.Database.Tables["A"].Extents[0].ID
	bPath := eng.Catalog.Database.Tables["B"].Extents[0].FilePath
	dir := eng.Catalog.DatabasePath()
	if err := os.Rename(filepath.Join(dir, bPath), filepath.Join(dir, filepath.Dir(bPath), "B_"+a+".vtx")); err != nil {
		t.Fatal(err)
	}
	stmt, err := parser.Parse(`.drop extent ` + a)
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.Execute(stmt)
	if err == nil || !strings.Contains(err.Error(), "from <Table>") {
		t.Fatalf("err = %v, want an ambiguity error", err)
	}
	expectRows(t, diskQuery(t, eng, `A`), 1)
	expectRows(t, diskQuery(t, eng, `B`), 1)
}
