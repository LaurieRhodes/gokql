package engine

import (
	"fmt"
	"sync"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// Engines in one process run statements concurrently (the HTTP server
// builds one per request). Let bindings used to live in a process-wide
// variable read by the evaluator: concurrent statements crashed the
// process (fatal: concurrent map read and map write) or resolved a
// name against another statement's bindings. The evaluator now carries
// its engine.
func TestConcurrentEnginesIsolateLetBindings(t *testing.T) {
	const workers, perWorker = 8, 150
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []string
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			eng := New(catalog.NewMemory())
			defer eng.Catalog.Close()
			for i := 0; i < perWorker; i++ {
				want := g*1000 + i
				// x is a let binding here; in odd workers a let
				// function too, so UDF lookup and depth are exercised.
				q := fmt.Sprintf("let x = %d; range r from 1 to 200 step 1 | extend v = x | summarize max(v)", want)
				if g%2 == 1 {
					q = fmt.Sprintf("let x = %d; let f = (a: long) { a + x - x }; range r from 1 to 200 step 1 | extend v = f(x) | summarize max(v)", want)
				}
				stmt, err := parser.Parse(q)
				if err != nil {
					t.Error(err)
					return
				}
				res, err := eng.Execute(stmt)
				got := "error"
				if err == nil && len(res.Rows) == 1 {
					got = fmt.Sprint(res.Rows[0][0])
				}
				if got != fmt.Sprint(want) {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("worker %d: want %d, got %s (%v)", g, want, got, err))
					mu.Unlock()
				}
			}
		}(g)
	}
	wg.Wait()
	if len(failures) > 0 {
		t.Fatalf("%d of %d statements wrong; first: %s", len(failures), workers*perWorker, failures[0])
	}
}

// A statement referencing an undefined name, running beside one that
// binds that name, gets "not found" rather than the other statement's
// value.
func TestConcurrentLetDoesNotLeakIntoOtherEngines(t *testing.T) {
	var wg sync.WaitGroup
	stop := make(chan struct{})
	binder := New(catalog.NewMemory())
	defer binder.Catalog.Close()
	wg.Add(1)
	go func() {
		defer wg.Done()
		stmt, _ := parser.Parse(`let leaked = 999; range k from 1 to 2000 step 1 | extend v = leaked | summarize max(v)`)
		for {
			select {
			case <-stop:
				return
			default:
				binder.Execute(stmt)
			}
		}
	}()
	reader := New(catalog.NewMemory())
	defer reader.Catalog.Close()
	stmt, _ := parser.Parse(`range r from 1 to 2000 step 1 | extend v = leaked | summarize max(v)`)
	leaks := 0
	for i := 0; i < 400; i++ {
		if _, err := reader.Execute(stmt); err == nil {
			leaks++
		}
	}
	close(stop)
	wg.Wait()
	if leaks > 0 {
		t.Fatalf("%d of 400 statements resolved another engine's let binding", leaks)
	}
}
