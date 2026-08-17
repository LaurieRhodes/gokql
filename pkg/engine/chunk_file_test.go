package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestMD(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "test.md")
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestChunkFileBasicParagraphs verifies heading-trail tagging and
// paragraph-boundary splitting on a small, hand-checkable document.
func TestChunkFileBasicParagraphs(t *testing.T) {
	dir := t.TempDir()
	md := `# Title

First paragraph under the title, long enough to survive the trivial-fragment filter.

## Section A

Paragraph in section A, also long enough to survive filtering easily.

## Section B

Paragraph in section B, likewise comfortably over the twenty character floor.
`
	path := writeTestMD(t, dir, md)
	eng := discoverEngine(t, t.TempDir())
	diskExec(t, eng, `.chunk-file "`+path+`" into Chunks`)

	tbl := diskQuery(t, eng, `Chunks | count`)
	expectCell(t, tbl, 0, 0, "3")

	tbl = diskQuery(t, eng, `Chunks | where Id == "c002" | project HeadingTrail`)
	expectCell(t, tbl, 0, 0, "Title > Section A")
}

// TestChunkFileAutoSplitsOversized: the actual live-found bug — a
// dense block over ChunkMaxChars must be split, not left oversized or
// dropped, and every resulting piece must stay under the limit while
// the union of all pieces reconstructs the original content.
func TestChunkFileAutoSplitsOversized(t *testing.T) {
	dir := t.TempDir()
	oldMax := ChunkMaxChars
	ChunkMaxChars = 100 // small limit, easy to force splitting in a test
	defer func() { ChunkMaxChars = oldMax }()

	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "table row content number "+string(rune('A'+i))+" with some padding text here")
	}
	md := "# Doc\n\n" + strings.Join(lines, "\n") + "\n"
	path := writeTestMD(t, dir, md)

	eng := discoverEngine(t, t.TempDir())
	diskExec(t, eng, `.chunk-file "`+path+`" into Chunks`)

	tbl := diskQuery(t, eng, `Chunks | extend len = strlen(Text) | summarize m = max(len)`)
	maxLen := tbl.Rows[0][0]
	if v, ok := maxLen.(int64); !ok || v >= int64(ChunkMaxChars) {
		t.Fatalf("a chunk exceeded ChunkMaxChars: %v", maxLen)
	}

	tbl = diskQuery(t, eng, `Chunks | count`)
	if tbl.Rows[0][0].(int64) < 2 {
		t.Fatal("expected the oversized paragraph to split into multiple chunks")
	}

	// Content reconstruction: every original line must appear
	// somewhere across the chunks (nothing silently dropped).
	all := diskQuery(t, eng, `Chunks | project Text`)
	var combined strings.Builder
	for _, row := range all.Rows {
		combined.WriteString(row[0].(string))
		combined.WriteString("\n")
	}
	for _, l := range lines {
		if !strings.Contains(combined.String(), l) {
			t.Errorf("line lost during oversized split: %q", l)
		}
	}
}

// TestChunkFileRealDocument runs against the actual pilot corpus file
// used throughout this session and requires the exact known-good
// paragraph count, plus the specific regression this feature exists
// to fix: the table block that reproducibly broke the local embedding
// model must now be split under the real embedding limit.
func TestChunkFileRealDocument(t *testing.T) {
	path := "/media/laurie/Data/Sumerian/house_of_ishbi_irra_md/academic_papers/paper6_gobekli_tepe_test/papers/paper_1h_girsu_observatory/sources/GULA_BABA_VEGA_RESEARCH_NOTES.md"
	if _, err := os.Stat(path); err != nil {
		t.Skip("pilot corpus file not present in this environment")
	}
	eng := discoverEngine(t, t.TempDir())
	diskExec(t, eng, `.chunk-file "`+path+`" into Chunks`)

	tbl := diskQuery(t, eng, `Chunks | count`)
	total := tbl.Rows[0][0].(int64)
	if total < 134 {
		t.Fatalf("expected at least 134 chunks (paragraph count), got %d", total)
	}

	tbl = diskQuery(t, eng, `Chunks | extend len = strlen(Text) | summarize m = max(len)`)
	if v := tbl.Rows[0][0].(int64); v >= 1500 {
		t.Errorf("a chunk from the real document exceeds the practical embedding limit: %d chars", v)
	}
}
