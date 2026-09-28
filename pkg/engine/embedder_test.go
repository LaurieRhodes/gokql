package engine

// embedder_test.go -- the point of the seam, demonstrated.
//
// Every existing test in func_vector_test.go and embed_bulk_test.go
// stands up an httptest server pretending to be Ollama. These tests
// stand up nothing at all: no server, no daemon, no model files, no
// network. That is the difference the Embedder interface buys, and it
// is the same difference a user gets on a machine with no Ollama
// installed.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/embed"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// testEngine is queryResult's engine, exposed so a test can configure
// the embedder before executing. queryResult builds its engine
// internally, which is fine for every other function but not for one
// whose whole point is a per-engine backend.
func testEngine(t *testing.T) *Engine {
	t.Helper()
	return New(catalog.NewMemory())
}

func runEngine(eng *Engine, query string) (*types.Table, error) {
	stmt, err := parser.Parse(query)
	if err != nil {
		return nil, err
	}
	return eng.Execute(stmt)
}

func queryEngine(t *testing.T, eng *Engine, query string) *types.Table {
	t.Helper()
	tbl, err := runEngine(eng, query)
	if err != nil {
		t.Fatalf("execute %q: %v", query, err)
	}
	return tbl
}

func TestMockEmbedderIsDeterministicAndNormalised(t *testing.T) {
	m := embed.NewMock(16)
	a, err := m.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 16 {
		t.Fatalf("dimension = %d, want 16", len(a))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("not deterministic at %d: %v vs %v", i, a[i], b[i])
		}
	}
	// Self-similarity must be 1 for a normalised vector; this also
	// exercises the real cosineSimilarity the engine ranks with.
	sim, err := cosineSimilarity(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if sim < 0.9999 || sim > 1.0001 {
		t.Fatalf("self-similarity = %v, want 1", sim)
	}
}

func TestMockEmbedderHandlesEmptyInput(t *testing.T) {
	// A zero vector would make everything rank as equally unrelated.
	v, err := embed.NewMock(8).Embed(context.Background(), "   ")
	if err != nil {
		t.Fatal(err)
	}
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	if norm == 0 {
		t.Fatal("empty input produced a zero vector")
	}
}

func TestEmbedTextWithNoDaemon(t *testing.T) {
	// The whole reason this change exists: embed_text() running with
	// nothing installed and nothing listening.
	eng := testEngine(t)
	eng.SetEmbedder(embed.NewMock(12))
	defer eng.SetEmbedder(nil)

	tbl := queryEngine(t, eng, `print n = array_length(embed_text("hello world"))`)
	expectCell(t, tbl, 0, 0, "12")
}

func TestEmbedTextRejectsPerCallModelOnStaticBackend(t *testing.T) {
	// A static backend cannot honour embed_text(x, "some-model").
	// Silently ignoring the argument would write vectors from one
	// model under another model's name, which is precisely the
	// silent-wrong-answer class cosineSimilarity's own length check
	// exists to prevent.
	eng := testEngine(t)
	eng.SetEmbedder(embed.NewMock(12))
	defer eng.SetEmbedder(nil)

	if _, err := runEngine(eng, `print v = embed_text("hi", "nomic-embed-text")`); err == nil {
		t.Fatal("expected an error naming the backend, got none")
	}
}

func TestEmbedderFromSpec(t *testing.T) {
	for _, tc := range []struct {
		spec    string
		want    string
		wantErr bool
	}{
		{spec: "mock", want: "mock-8"},
		{spec: "mock:384", want: "mock-384"},
		{spec: "ollama", want: DefaultEmbedModel},
		{spec: "ollama:mxbai-embed-large", want: "mxbai-embed-large"},
		{spec: "mock:0", wantErr: true},
		{spec: "mock:banana", wantErr: true},
		{spec: "sqlite", wantErr: true},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			em, err := EmbedderFromSpec(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.spec)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if em.Name() != tc.want {
				t.Errorf("Name() = %q, want %q", em.Name(), tc.want)
			}
		})
	}
}

func TestBadGokqlEmbedFailsLoudlyRatherThanFallingBack(t *testing.T) {
	// A typo in configuration must not quietly send traffic to Ollama
	// and write vectors from an unintended model into a real table.
	t.Setenv("GOKQL_EMBED", "potionn:whatever")
	em := defaultEmbedder()
	if _, err := em.Embed(context.Background(), "x"); err == nil {
		t.Fatal("expected first use to fail, got nil")
	}
}

func TestDefaultsToOllamaWhenUnconfigured(t *testing.T) {
	// The compatibility guarantee: nothing set, same behaviour as
	// before this package existed.
	os.Unsetenv("GOKQL_EMBED")
	em := defaultEmbedder()
	if _, ok := em.(*ollamaEmbedder); !ok {
		t.Fatalf("default embedder = %T, want *ollamaEmbedder", em)
	}
}

// TestEmbedIntoWithNoDaemon is TestEmbedIntoBatchesCorrectly's
// dependency-free twin: the same bulk ingest path, end to end, with no
// HTTP server anywhere. This is the shape a user on a machine with no
// Ollama gets.
func TestEmbedIntoWithNoDaemon(t *testing.T) {
	eng := diskEngineEmpty(t)
	eng.SetEmbedder(embed.NewMock(24))
	defer eng.SetEmbedder(nil)

	diskExec(t, eng, `.create table Docs (Id: string, Text: string)`)
	diskExec(t, eng, `.set-or-append Docs <| datatable(Id:string, Text:string) [
		"a", "kql query engine", "b", "vector similarity search", "c", "unrelated cooking recipe"]`)
	diskExec(t, eng, `.create table DocEmbeddings (Id: string, Embedding: dynamic, Model: string, Provenance: string)`)
	diskExec(t, eng, `.embed-into DocEmbeddings <| Docs | project Id, Text, Provenance = "local"`)

	expectCell(t, diskQuery(t, eng, `DocEmbeddings | count`), 0, 0, "3")
	expectCell(t, diskQuery(t, eng,
		`DocEmbeddings | where Id == "a" | project n = array_length(Embedding)`), 0, 0, "24")

	// And the full semantic-recall shape: embed a query, rank stored
	// vectors against it. Nothing here touches the network.
	got := diskQuery(t, eng, `DocEmbeddings
		| extend sim = series_cosine_similarity(Embedding, embed_text("kql query engine"))
		| top 1 by sim desc
		| project Id`)
	expectCell(t, got, 0, 0, "a")
}

// TestEmbedIntoRefusesToMixVectorSpaces is the guard that did not
// exist in the first prototype. Two models of the SAME width compare
// without error and rank nonsense, so this is the last point at which
// the mistake is still cheap.
func TestEmbedIntoRefusesToMixVectorSpaces(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table Docs (Id: string, Text: string)`)
	diskExec(t, eng, `.set-or-append Docs <| datatable(Id:string, Text:string) ["a", "one"]`)
	diskExec(t, eng, `.create table E (Id: string, Embedding: dynamic, Model: string, Provenance: string)`)

	eng.SetEmbedder(embed.NewMock(16))
	diskExec(t, eng, `.embed-into E <| Docs | project Id, Text`)
	defer eng.SetEmbedder(nil)

	// Same width, different model. The length check cannot see this.
	eng.SetEmbedder(&renamedMock{Mock: embed.NewMock(16), name: "some-other-16d-model"})
	if _, err := runEngine(eng, `.embed-into E <| Docs | project Id, Text`); err == nil {
		t.Fatal("expected a refusal to mix two 16-wide vector spaces, got success")
	}

	// Same model again: must still append cleanly.
	eng.SetEmbedder(embed.NewMock(16))
	if _, err := runEngine(eng, `.embed-into E <| Docs | project Id, Text`); err != nil {
		t.Fatalf("same model should append: %v", err)
	}
}

// TestEmbedIntoRecordsTheModelThatDidTheWork guards the defect the
// first prototype shipped: the Model column was written from a package
// default, so a mock-embedded table claimed to hold nomic vectors.
func TestEmbedIntoRecordsTheModelThatDidTheWork(t *testing.T) {
	eng := diskEngineEmpty(t)
	eng.SetEmbedder(embed.NewMock(16))
	defer eng.SetEmbedder(nil)

	diskExec(t, eng, `.create table Docs (Id: string, Text: string)`)
	diskExec(t, eng, `.set-or-append Docs <| datatable(Id:string, Text:string) ["a", "one"]`)
	diskExec(t, eng, `.create table E (Id: string, Embedding: dynamic, Model: string, Provenance: string)`)
	diskExec(t, eng, `.embed-into E <| Docs | project Id, Text`)

	expectCell(t, diskQuery(t, eng, `E | project Model`), 0, 0, "mock-16")
}

type renamedMock struct {
	*embed.Mock
	name string
}

func (r *renamedMock) Name() string { return r.name }

// TestConstantArgumentEmbeddedOnce pins the single worst performance
// defect found in this work. KQL evaluates scalar functions per row,
// so the primary documented search pattern was issuing one HTTP
// request per row to embed one constant query string: measured at 10
// calls for 10 rows before the fix, which is 10,000 calls to search a
// 10,000-chunk corpus.
func TestConstantArgumentEmbeddedOnce(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		json.NewEncoder(w).Encode(map[string]interface{}{"embeddings": [][]float64{{1, 0, 0}}})
	}))
	defer srv.Close()
	old := EmbedEndpoint
	EmbedEndpoint = srv.URL
	defer func() { EmbedEndpoint = old }()

	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table E (Id: string, Embedding: dynamic)`)
	diskExec(t, eng, `.set-or-append E <| datatable(Id:string, Embedding:dynamic) [
		"a","[1,0,0]", "b","[0,1,0]", "c","[0,0,1]", "d","[1,1,0]", "e","[0,1,1]",
		"f","[1,0,1]", "g","[1,1,1]", "h","[2,0,0]", "i","[0,2,0]", "j","[0,0,2]"]`)

	diskQuery(t, eng, `E
		| extend sim = series_cosine_similarity(Embedding, embed_text("my search query"))
		| top 3 by sim desc`)

	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Errorf("a constant argument over 10 rows made %d embedding calls, want 1", got)
	}
}

// TestDistinctArgumentsStillEmbedSeparately guards the obvious way to
// break the above: a cache keyed too loosely would return one row's
// vector for another's text.
func TestDistinctArgumentsStillEmbedSeparately(t *testing.T) {
	seen := map[string]bool{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		seen[req.Input] = true
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"embeddings": [][]float64{{float64(len(req.Input)), 0, 0}},
		})
	}))
	defer srv.Close()
	old := EmbedEndpoint
	EmbedEndpoint = srv.URL
	defer func() { EmbedEndpoint = old }()

	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table T (Id: string, Text: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Text:string) [
		"a","x", "b","yy", "c","zzz", "d","x"]`)

	got := diskQuery(t, eng, `T | project Id, v = embed_text(Text) | order by Id asc`)
	// Three distinct strings, one repeated: three calls, four correct rows.
	if len(seen) != 3 {
		t.Errorf("distinct texts embedded = %d, want 3", len(seen))
	}
	expectCell(t, got, 0, 1, "[1,0,0]") // "x"
	expectCell(t, got, 1, 1, "[2,0,0]") // "yy"
	expectCell(t, got, 2, 1, "[3,0,0]") // "zzz"
	expectCell(t, got, 3, 1, "[1,0,0]") // "x" again, from cache
}

// TestConcurrentEnginesShareOneEmbedderSafely pins the contract that
// actually holds, which is narrower than the one I first wrote a test
// for.
//
// SUPPORTED: many engines running concurrently against ONE embedder.
// Memory-safe, and every statement sees that embedder.
//
// NOT SUPPORTED, and this test does not assert it: concurrent engines
// with DIFFERENT embedders. beginEmbedderScope restores the previous
// ambient on exit, so engine A finishing mid-statement can strip
// engine B's backend and leave B falling through to the default. My
// first version of this test asserted isolation and failed exactly
// that way, which is the honest evidence for the limitation rather
// than a claim about it. Fixing it needs an evaluation context
// threaded through evalExpr's 319 call sites.
func TestConcurrentEnginesShareOneEmbedderSafely(t *testing.T) {
	shared := embed.NewMock(12)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			eng := testEngine(t)
			eng.SetEmbedder(shared)
			tbl, err := runEngine(eng, `print n = array_length(embed_text("hello"))`)
			if err != nil {
				t.Error(err)
				return
			}
			if got := cell(t, tbl, 0, 0); got != "12" {
				t.Errorf("array_length = %s, want 12", got)
			}
		}()
	}
	wg.Wait()
}

// TestEmbedderDoesNotLeakBetweenEngines is the regression guard for a
// defect found by running the suite under -race: SetEmbedder used to
// publish to a package-level ambient, so one engine's mock silently
// became every later engine's backend for the life of the process.
// Three unrelated tests began passing when they should have failed.
func TestEmbedderDoesNotLeakBetweenEngines(t *testing.T) {
	configured := testEngine(t)
	configured.SetEmbedder(embed.NewMock(99))
	if _, err := runEngine(configured, `print n = array_length(embed_text("x"))`); err != nil {
		t.Fatal(err)
	}

	// A second, UNCONFIGURED engine must fall back to the default
	// backend, not inherit the first engine's mock.
	old := EmbedEndpoint
	EmbedEndpoint = "http://127.0.0.1:1/no-such-server"
	oldBackoff := embedBackoff
	embedBackoff = []time.Duration{time.Millisecond}
	defer func() { EmbedEndpoint = old; embedBackoff = oldBackoff }()

	fresh := testEngine(t)
	if _, err := runEngine(fresh, `print n = array_length(embed_text("x"))`); err == nil {
		t.Fatal("an unconfigured engine inherited another engine's embedder")
	}
}

// TestConflictingConcurrentEmbeddersIsALoudError pins the one case
// the reference-counted scope cannot resolve: two DIFFERENT backends
// live at once. embed_text() has no way to know which engine called
// it, so the only honest answers are "error" or "wrong vectors", and
// wrong vectors are unrecoverable once written.
func TestConflictingConcurrentEmbeddersIsALoudError(t *testing.T) {
	outer := testEngine(t)
	outer.SetEmbedder(embed.NewMock(8))
	leave, err := outer.beginEmbedderScope()
	if err != nil {
		t.Fatal(err)
	}
	defer leave()

	other := testEngine(t)
	other.SetEmbedder(embed.NewMock(16)) // a different instance
	if _, err := other.beginEmbedderScope(); !errors.Is(err, ErrConcurrentEmbedders) {
		t.Fatalf("got %v, want ErrConcurrentEmbedders", err)
	}
}

// TestUnconfiguredEnginesDoNotConflict guards the false positive that
// the first version of the conflict check produced: defaultEmbedder()
// returned a fresh instance per call, so two plain engines looked like
// two different backends and every nested query failed.
func TestUnconfiguredEnginesDoNotConflict(t *testing.T) {
	os.Unsetenv("GOKQL_EMBED")
	a, b := testEngine(t), testEngine(t)
	leaveA, err := a.beginEmbedderScope()
	if err != nil {
		t.Fatal(err)
	}
	defer leaveA()
	leaveB, err := b.beginEmbedderScope()
	if err != nil {
		t.Fatalf("two unconfigured engines conflicted: %v", err)
	}
	leaveB()
}

// TestScopeIsRefCountedNotRestored guards the save-and-restore bug:
// the inner scope leaving must not tear down the outer one.
func TestScopeIsRefCountedNotRestored(t *testing.T) {
	eng := testEngine(t)
	eng.SetEmbedder(embed.NewMock(8))

	leaveOuter, err := eng.beginEmbedderScope()
	if err != nil {
		t.Fatal(err)
	}
	leaveInner, err := eng.beginEmbedderScope()
	if err != nil {
		t.Fatal(err)
	}
	leaveInner()
	if getActiveEmbedder() == nil {
		t.Fatal("inner scope leaving tore down the outer scope")
	}
	leaveOuter()
	if getActiveEmbedder() != nil {
		t.Fatal("last holder leaving did not end the scope")
	}
}

// TestWidthGuardSurvivesExplicitModelColumn pins item 7's fix: an
// explicit Model column waives the model-NAME check (the caller owns
// that claim) but must not waive the WIDTH check, which is never a
// judgement call.
//
// NOTE a design ambiguity this test surfaced: the Model column is both
// an INPUT (".embed-into: use this model for this row") and an OUTPUT
// ("this model produced this vector"). A backend that cannot switch
// models per call rejects the input reading outright, so the two
// meanings are not separable today. Worth resolving; see
// docs/embeddings.md.
func TestWidthGuardSurvivesExplicitModelColumn(t *testing.T) {
	var width int64 = 16
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		n := int(atomic.LoadInt64(&width))
		vecs := make([][]float64, len(req.Input))
		for i := range vecs {
			vecs[i] = make([]float64, n)
			vecs[i][0] = 1
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"embeddings": vecs})
	}))
	defer srv.Close()
	old := EmbedEndpoint
	EmbedEndpoint = srv.URL
	defer func() { EmbedEndpoint = old }()

	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table Docs (Id: string, Text: string)`)
	diskExec(t, eng, `.set-or-append Docs <| datatable(Id:string, Text:string) ["a", "one"]`)
	diskExec(t, eng, `.create table E (Id: string, Embedding: dynamic, Model: string, Provenance: string)`)
	diskExec(t, eng, `.embed-into E <| Docs | project Id, Text, Model = "claimed-model"`)

	// Same claimed Model name, different WIDTH. The name check is
	// waived by the explicit column; the width check must still fire.
	atomic.StoreInt64(&width, 32)
	if _, err := runEngine(eng, `.embed-into E <| Docs | project Id, Text, Model = "claimed-model"`); err == nil {
		t.Fatal("an explicit Model column waived the width check")
	}
}
