package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const chunkDoc = "# T\n\nFirst paragraph, long enough to pass the fragment filter.\n\nSecond paragraph, also long enough to pass the filter.\n"

func writeDoc(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(chunkDoc), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Chunk Ids are <key>:c<NNN>, unique across files in one table, so the
// result embeds without the strcat(Provenance, ...) workaround.
func TestChunkIdsUniqueAcrossFiles(t *testing.T) {
	src := t.TempDir()
	eng := discoverEngine(t, t.TempDir())
	diskExec(t, eng, `.chunk-file "`+writeDoc(t, src, "alpha.md")+`" into Chunks`)
	diskExec(t, eng, `.chunk-file "`+writeDoc(t, src, "beta.txt")+`" into Chunks`)
	tbl := diskQuery(t, eng, `Chunks | summarize n = count(), d = dcount(Id)`)
	expectCell(t, tbl, 0, 0, "4")
	expectCell(t, tbl, 0, 1, "4")
	expectCell(t, diskQuery(t, eng, `Chunks | where Provenance == "beta.txt" | sort by Id asc | take 1 | project Id`), 0, 0, "beta:c001")
}

// The same file chunked twice is refused, writing nothing.
func TestChunkFileTwiceRefused(t *testing.T) {
	src := t.TempDir()
	eng := discoverEngine(t, t.TempDir())
	p := writeDoc(t, src, "alpha.md")
	diskExec(t, eng, `.chunk-file "`+p+`" into Chunks`)
	_, err := runEngine(eng, `.chunk-file "`+p+`" into Chunks`)
	if err == nil || !strings.Contains(err.Error(), "already chunked") {
		t.Fatalf("second chunking: err = %v", err)
	}
	expectCell(t, diskQuery(t, eng, `Chunks | count`), 0, 0, "2")
}

// Two files with one stem collide; idprefix separates them.
func TestChunkFileSameStemNeedsPrefix(t *testing.T) {
	src := t.TempDir()
	eng := discoverEngine(t, t.TempDir())
	diskExec(t, eng, `.chunk-file "`+writeDoc(t, src, "a/notes.md")+`" into Chunks`)
	p2 := writeDoc(t, src, "b/notes.txt")
	_, err := runEngine(eng, `.chunk-file "`+p2+`" into Chunks`)
	if err == nil || !strings.Contains(err.Error(), "idprefix") {
		t.Fatalf("same stem: err = %v", err)
	}
	diskExec(t, eng, `.chunk-file "`+p2+`" into Chunks with (idprefix="b-notes")`)
	expectCell(t, diskQuery(t, eng, `Chunks | summarize d = dcount(Id)`), 0, 0, "4")
	expectCell(t, diskQuery(t, eng, `Chunks | where Id startswith "b-notes:" | count`), 0, 0, "2")
}

// A table written before keyed Ids (bare c001 Ids) is protected from
// re-chunking the same file under new Ids.
func TestChunkFileLegacyIdsProtected(t *testing.T) {
	src := t.TempDir()
	eng := discoverEngine(t, t.TempDir())
	diskExec(t, eng, `.create table Chunks (Id: string, Text: string, HeadingTrail: string, StartLine: long, EndLine: long, Provenance: string)`)
	diskExec(t, eng, ".ingest inline into table Chunks <| c001,old text,T,1,2,alpha.md")
	_, err := runEngine(eng, `.chunk-file "`+writeDoc(t, src, "alpha.md")+`" into Chunks`)
	if err == nil || !strings.Contains(err.Error(), "written before keyed Ids") {
		t.Fatalf("legacy table: err = %v", err)
	}
	// Another file into the same legacy table is fine.
	diskExec(t, eng, `.chunk-file "`+writeDoc(t, src, "beta.md")+`" into Chunks`)
	expectCell(t, diskQuery(t, eng, `Chunks | count`), 0, 0, "3")
}
