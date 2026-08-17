package engine

// func_vector_test.go — vector functions: pure math tested directly,
// embed_text tested against a mock Ollama server (httptest) so the
// suite never depends on a live local service to pass.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCosineSimilarity(t *testing.T) {
	cases := []struct {
		name    string
		a, b    string
		want    string // exact string match on formatted output
		wantErr bool
	}{
		{"identical", "[1,0,0]", "[1,0,0]", "1", false},
		{"orthogonal", "[1,0,0]", "[0,1,0]", "0", false},
		{"45deg", "[1,1,0]", "[1,0,0]", "0.7071067811865475", false},
		{"length mismatch", "[1,0,0]", "[1,0]", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := `print sim = series_cosine_similarity("` + tc.a + `", "` + tc.b + `")`
			if tc.wantErr {
				queryError(t, q)
				return
			}
			tbl := queryResult(t, q)
			expectCell(t, tbl, 0, 0, tc.want)
		})
	}
}

func TestDotProduct(t *testing.T) {
	tbl := queryResult(t, `print d = series_dot_product("[1,2,3]", "[4,5,6]")`)
	expectCell(t, tbl, 0, 0, "32")
}

func TestEmbedTextAgainstMockOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Model != "nomic-embed-text" {
			t.Errorf("unexpected model: %s", req.Model)
		}
		// A deterministic fake vector so length/content are checkable.
		json.NewEncoder(w).Encode(map[string]interface{}{
			"embeddings": [][]float64{{0.1, 0.2, 0.3, 0.4}},
		})
	}))
	defer srv.Close()

	oldEndpoint := EmbedEndpoint
	EmbedEndpoint = srv.URL
	defer func() { EmbedEndpoint = oldEndpoint }()

	tbl := queryResult(t, `print n = array_length(embed_text("hello world"))`)
	expectCell(t, tbl, 0, 0, "4")
}

// TestEmbedTextSendsNumBatchOption is func_vector.go's own version of
// embed_bulk_test.go's identically-named guard — see that test's own
// doc comment for the full explanation of the real, live production
// bug this guards against. embed_text() (the scalar, single-input
// function) and .embed-into (the bulk command) go through separate
// request-construction code paths, so each needed the fix, and each
// needed its own test to guard it independently.
func TestEmbedTextSendsNumBatchOption(t *testing.T) {
	var gotOptions map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Options map[string]interface{} `json:"options"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		gotOptions = req.Options
		json.NewEncoder(w).Encode(map[string]interface{}{
			"embeddings": [][]float64{{0.1, 0.2, 0.3, 0.4}},
		})
	}))
	defer srv.Close()

	oldEndpoint := EmbedEndpoint
	EmbedEndpoint = srv.URL
	defer func() { EmbedEndpoint = oldEndpoint }()

	queryResult(t, `print n = array_length(embed_text("hello world"))`)

	if gotOptions == nil {
		t.Fatal("expected an options object in the embed_text request body, got none")
	}
	if nb, ok := gotOptions["num_batch"]; !ok || nb != float64(4096) {
		t.Errorf("expected options.num_batch = 4096, got %v (present: %v)", nb, ok)
	}
}

func TestEmbedTextUnreachableGivesActionableError(t *testing.T) {
	oldEndpoint := EmbedEndpoint
	EmbedEndpoint = "http://127.0.0.1:1/no-such-server" // guaranteed connection refused
	defer func() { EmbedEndpoint = oldEndpoint }()

	queryError(t, `print n = array_length(embed_text("hello"))`)
}

// TestEmbedTextThenSimilarityRoundTrip: the full pattern used for
// ingest-time embedding — .set-or-append computing embed_text(Claim)
// per row — against a mock server, verified end to end.
func TestEmbedTextThenSimilarityRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input string `json:"input"` }
		json.NewDecoder(r.Body).Decode(&req)
		// Distinct fake vectors per distinct input so similarity is
		// checkable: same text -> identical vector -> similarity 1.
		v := []float64{0.1, 0.2, 0.3}
		if req.Input == "different" {
			v = []float64{0.9, 0.1, 0.0}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"embeddings": [][]float64{v}})
	}))
	defer srv.Close()
	oldEndpoint := EmbedEndpoint
	EmbedEndpoint = srv.URL
	defer func() { EmbedEndpoint = oldEndpoint }()

	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table T (Id: string, Text: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Text:string) ["a", "same", "b", "same", "c", "different"]`)
	diskExec(t, eng, `.create table E (Id: string, Embedding: dynamic)`)
	diskExec(t, eng, `.set-or-append E <| T | project Id, Embedding = embed_text(Text)`)

	tbl := diskQuery(t, eng, `
		let qv = embed_text("same");
		E | extend sim = series_cosine_similarity(Embedding, qv) | sort by sim desc | project Id, sim`)
	expectRows(t, tbl, 3)
	expectCell(t, tbl, 0, 1, "1") // "a" and "b" both embedded "same" text -> sim 1 with the query
}

// TestEmbedTextRetriesTransientFailure: a 500/connection-drop on the
// first N attempts must be retried, not surfaced immediately — found
// live: Ollama's embedding runner drops connections under sustained
// sequential load, recovering on its own within a few hundred ms.
func TestEmbedTextRetriesTransientFailure(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"do embedding request: EOF"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"embeddings": [][]float64{{1, 2, 3}}})
	}))
	defer srv.Close()
	oldEndpoint, oldBackoff := EmbedEndpoint, embedBackoff
	EmbedEndpoint = srv.URL
	embedBackoff = []time.Duration{time.Millisecond, time.Millisecond} // fast test
	defer func() { EmbedEndpoint = oldEndpoint; embedBackoff = oldBackoff }()

	tbl := queryResult(t, `print n = array_length(embed_text("hello"))`)
	expectCell(t, tbl, 0, 0, "3")
	if calls != 3 {
		t.Errorf("expected 3 calls (2 failures + 1 success), got %d", calls)
	}
}

// TestEmbedTextPermanentFailureNotRetried: a 4xx / malformed response
// (model not found, etc.) must fail fast, not burn through retries.
func TestEmbedTextPermanentFailureNotRetried(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"model not found"}`))
	}))
	defer srv.Close()
	oldEndpoint := EmbedEndpoint
	EmbedEndpoint = srv.URL
	defer func() { EmbedEndpoint = oldEndpoint }()

	queryError(t, `print n = array_length(embed_text("hello"))`)
	if calls != 1 {
		t.Errorf("expected exactly 1 call for a non-retryable failure, got %d", calls)
	}
}

// TestDynamicLiteralSyntax: dynamic([1,2,3]) / dynamic({"a":1}) —
// real Kusto grammar (JSON literal, not a KQL expression list) —
// previously unsupported entirely ("unsupported function: dynamic").
// Found live during the vector-search work when hand-testing
// series_cosine_similarity, worked around at the time with an
// undocumented bare-JSON-string path; fixed properly as part of the
// backlog pass.
func TestDynamicLiteralSyntax(t *testing.T) {
	tbl := queryResult(t, `print n = array_length(dynamic([1,2,3]))`)
	expectCell(t, tbl, 0, 0, "3")

	tbl = queryResult(t, `print sim = series_cosine_similarity(dynamic([1,0,0]), dynamic([1,0,0]))`)
	expectCell(t, tbl, 0, 0, "1")

	// Object literal, including nested array — bracket-depth tracking
	// must not get confused by the inner [ ] while scanning for the
	// outer { }.
	tbl = queryResult(t, `print n = array_length(dynamic({"a": 1, "b": [1,2,3]}).b)`)
	expectCell(t, tbl, 0, 0, "3")

	// Malformed JSON must error at parse time, not produce a garbage
	// value or defer to a confusing runtime failure.
	queryError(t, `print x = dynamic([1,2,)`)

	// Regression: ordinary function calls unaffected.
	tbl = queryResult(t, `print n = strlen("hello")`)
	expectCell(t, tbl, 0, 0, "5")
}
