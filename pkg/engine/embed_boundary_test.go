package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/embed"
)

// countingMock records how many rows each EmbedBatch call received,
// so chunking arithmetic is observable rather than inferred.
type countingMock struct {
	*embed.Mock
	batches []int
}

func (c *countingMock) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	c.batches = append(c.batches, len(texts))
	return c.Mock.EmbedBatch(ctx, texts)
}

// TestEmbedCommitBoundaries walks row counts around every commit
// boundary. Off-by-ones in chunking do not crash; they drop or
// duplicate the last row, which is invisible until someone notices a
// chunk missing from search results months later.
func TestEmbedCommitBoundaries(t *testing.T) {
	oldCommit := EmbedCommitRows
	defer func() { EmbedCommitRows = oldCommit }()
	EmbedCommitRows = 4

	for _, n := range []int{1, 3, 4, 5, 7, 8, 9, 12, 13} {
		t.Run(fmt.Sprintf("rows=%d", n), func(t *testing.T) {
			eng := diskEngineEmpty(t)
			eng.SetEmbedder(embed.NewMock(8))
			defer eng.SetEmbedder(nil)

			var rows []string
			for i := 0; i < n; i++ {
				rows = append(rows, fmt.Sprintf(`"id%03d", "text %d"`, i, i))
			}
			diskExec(t, eng, `.create table Docs (Id: string, Text: string)`)
			diskExec(t, eng, `.set-or-append Docs <| datatable(Id:string, Text:string) [`+
				strings.Join(rows, ", ")+`]`)
			diskExec(t, eng, `.embed-into E <| Docs | project Id, Text`)

			expectCell(t, diskQuery(t, eng, `E | count`), 0, 0, fmt.Sprintf("%d", n))
			// Every Id present exactly once: no drops, no duplicates.
			expectCell(t, diskQuery(t, eng, `E | summarize dcount(Id)`), 0, 0, fmt.Sprintf("%d", n))
		})
	}
}

// TestResumeAtEveryBoundary: interrupt after each possible number of
// committed chunks and confirm the resume lands on exactly the full
// set, with no row embedded twice and none lost.
func TestResumeAtEveryBoundary(t *testing.T) {
	oldCommit := EmbedCommitRows
	defer func() { EmbedCommitRows = oldCommit }()
	EmbedCommitRows = 3

	const n = 10
	for stopAfter := 1; stopAfter <= 3; stopAfter++ {
		t.Run(fmt.Sprintf("stopAfter=%d", stopAfter), func(t *testing.T) {
			eng := diskEngineEmpty(t)
			var rows []string
			for i := 0; i < n; i++ {
				rows = append(rows, fmt.Sprintf(`"id%03d", "text %d"`, i, i))
			}
			diskExec(t, eng, `.create table Docs (Id: string, Text: string)`)
			diskExec(t, eng, `.set-or-append Docs <| datatable(Id:string, Text:string) [`+
				strings.Join(rows, ", ")+`]`)

			fail := &failAfterN{Mock: embed.NewMock(8), limit: stopAfter}
			eng.SetEmbedder(fail)
			if _, err := runEngine(eng, `.embed-into E <| Docs | project Id, Text`); err == nil {
				t.Fatal("expected the simulated failure")
			}

			eng.SetEmbedder(embed.NewMock(8))
			if _, err := runEngine(eng, `.embed-into E <| Docs | project Id, Text`); err != nil {
				t.Fatalf("resume: %v", err)
			}
			defer eng.SetEmbedder(nil)

			expectCell(t, diskQuery(t, eng, `E | count`), 0, 0, fmt.Sprintf("%d", n))
			expectCell(t, diskQuery(t, eng, `E | summarize dcount(Id)`), 0, 0, fmt.Sprintf("%d", n))
		})
	}
}

// failAfterN succeeds for limit batch calls then fails, simulating a
// backend dying partway through an ingest.
type failAfterN struct {
	*embed.Mock
	limit int
	calls int
}

func (f *failAfterN) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	f.calls++
	if f.calls > f.limit {
		return nil, fmt.Errorf("simulated backend failure on call %d", f.calls)
	}
	return f.Mock.EmbedBatch(ctx, texts)
}
