package engine

import (
	"os"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
)

// A memory catalog keeps its extent files in a private temporary
// directory removed by Close. It used to write them under extents/
// relative to the working directory.
func TestMemoryCatalogWritesNothingInWorkingDir(t *testing.T) {
	wd := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	cat := catalog.NewMemory()
	eng := New(cat)
	diskExec(t, eng, `.create table T (A: long)`)
	diskExec(t, eng, ".ingest inline into table T <| 1\n2")
	expectCell(t, diskQuery(t, eng, `T | summarize S = sum(A)`), 0, 0, "3")

	if entries, _ := os.ReadDir(wd); len(entries) != 0 {
		t.Fatalf("working directory has %d entries, want none (first: %s)", len(entries), entries[0].Name())
	}
	dir := cat.DatabasePath()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("memory directory %s: %v", dir, err)
	}
	if err := cat.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("memory directory %s still exists after Close", dir)
	}
}
