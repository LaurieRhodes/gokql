package engine

import (
	"testing"
	"time"
)

// A reader in another engine (as in another process: its own lock file
// descriptor) during the window where a compaction has written the
// merged extent but not yet superseded the old ones must wait, and
// then count each row once.
func TestCompactionExcludesReaders(t *testing.T) {
	dir := t.TempDir()
	a := discoverEngine(t, dir)
	diskExec(t, a, `.create table C (X: long)`)
	for i := 0; i < 3; i++ {
		diskExec(t, a, `.set-or-append C <| datatable(X:long)[1, 2, 3]`)
	}
	reader := discoverEngine(t, dir)

	type res struct {
		n   string
		err error
	}
	got := make(chan res, 1)
	finishedEarly := false
	compactBeforeSupersedeHook = func() {
		go func() {
			tbl, err := reader.Execute(mustParseQuery(t, `C | count`))
			if err != nil {
				got <- res{err: err}
				return
			}
			got <- res{n: cell(t, tbl, 0, 0)}
		}()
		select {
		case r := <-got:
			finishedEarly = true
			got <- r
		case <-time.After(300 * time.Millisecond):
		}
	}
	defer func() { compactBeforeSupersedeHook = nil }()

	diskExec(t, a, `.compact table C`)
	if finishedEarly {
		t.Fatal("a reader completed while the compaction held the scope")
	}
	r := <-got
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.n != "9" {
		t.Errorf("reader counted %s rows during compaction, want 9", r.n)
	}
	if n := cell(t, diskQuery(t, reader, `C | count`), 0, 0); n != "9" {
		t.Errorf("after compaction: %s rows, want 9", n)
	}
}
