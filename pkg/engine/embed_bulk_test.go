package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestEmbedIntoBatchesCorrectly verifies .embed-into calls Ollama in
// batches (not one request per row) and produces one output row per
// input row, correctly ordered, against a mock server that fails the
// test if it ever receives more than EmbedBatchSize inputs per call.
func TestEmbedIntoBatchesCorrectly(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Input) > EmbedBatchSize {
			t.Errorf("batch size %d exceeds EmbedBatchSize %d", len(req.Input), EmbedBatchSize)
		}
		vecs := make([][]float64, len(req.Input))
		for i, txt := range req.Input {
			vecs[i] = []float64{float64(len(txt)), 0, 0} // encode length so ordering is checkable
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"embeddings": vecs})
	}))
	defer srv.Close()

	oldEndpoint, oldBatch := EmbedEndpoint, EmbedBatchSize
	EmbedEndpoint = srv.URL
	EmbedBatchSize = 3
	defer func() { EmbedEndpoint = oldEndpoint; EmbedBatchSize = oldBatch }()

	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table Docs (Id: string, Text: string)`)
	diskExec(t, eng, `.set-or-append Docs <| datatable(Id:string, Text:string) [
		"a", "one", "b", "two", "c", "three", "d", "four", "e", "five", "f", "six", "g", "seven"]`)

	diskExec(t, eng, `.create table DocEmbeddings (Id: string, Embedding: dynamic, Model: string, Provenance: string)`)
	diskExec(t, eng, `.embed-into DocEmbeddings <| Docs | project Id, Text, Provenance = "test"`)

	if calls != 3 { // 7 rows, batch size 3 -> ceil(7/3) = 3 calls
		t.Errorf("expected 3 batch calls for 7 rows at batch size 3, got %d", calls)
	}

	got := diskQuery(t, eng, `DocEmbeddings | count`)
	expectCell(t, got, 0, 0, "7")

	// Ordering: "three" (5 chars) must map to row "c", not some other row.
	row := diskQuery(t, eng, `DocEmbeddings | where Id == "c" | project n = array_length(Embedding)`)
	// length isn't directly checkable via array_length (always 3 here);
	// verify via the encoded first element instead.
	_ = row
	first := diskQuery(t, eng, `DocEmbeddings | where Id == "c" | project Embedding`)
	if first.Rows[0][0] != `[5,0,0]` {
		t.Errorf("embedding misaligned for row c: got %v, want [5,0,0] (len(\"three\")=5)", first.Rows[0][0])
	}
}

// TestEmbedIntoRequiresIdAndText: the query must project both columns.
func TestEmbedIntoRequiresIdAndText(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table Docs (Id: string, Text: string)`)
	diskExec(t, eng, `.set-or-append Docs <| datatable(Id:string, Text:string) ["a", "x"]`)

	_, err := runStmt(t, eng, `.embed-into E <| Docs | project Id`) // missing Text
	if err == nil {
		t.Fatal("expected an error when Text column is missing")
	}
}

// TestEmbedIntoAllOrNothing: if a batch call fails, nothing is written.
func TestEmbedIntoAllOrNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // permanent failure, no retry
		w.Write([]byte(`{"error":"model not found"}`))
	}))
	defer srv.Close()
	oldEndpoint := EmbedEndpoint
	EmbedEndpoint = srv.URL
	defer func() { EmbedEndpoint = oldEndpoint }()

	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table Docs (Id: string, Text: string)`)
	diskExec(t, eng, `.set-or-append Docs <| datatable(Id:string, Text:string) ["a", "x", "b", "y"]`)

	_, err := runStmt(t, eng, `.embed-into E <| Docs | project Id, Text`)
	if err == nil {
		t.Fatal("expected failure")
	}
	if eng.Catalog.GetTable("E") != nil {
		t.Error("table E should not have been created on a failed embed batch")
	}
}

// TestEmbedIntoSendsNumBatchOption guards a real, live production bug
// found and fixed by a different model (Kimi): Ollama 0.13.1's
// embedding runner loads with a fixed BatchSize (512 tokens) and
// doesn't chunk a single input that exceeds it at all -- it panics
// outright ("caching disabled but unable to fit entire input in a
// batch"), killing the ENTIRE .embed-into batch (all-or-nothing by
// design) deterministically, reproduced live against a real,
// production finding whose Claim text (2820 chars) crossed that
// threshold. Verified via direct testing, not assumed, that neither
// num_ctx, truncate=true, nor the server-side OLLAMA_NUM_BATCH env var
// fix it -- only a per-request options.num_batch override does. This
// test guards that the request body this engine actually sends
// includes that override, so a future refactor can't silently drop it
// and reintroduce the exact failure mode that motivated it.
func TestEmbedIntoSendsNumBatchOption(t *testing.T) {
	var gotOptions map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input   []string               `json:"input"`
			Options map[string]interface{} `json:"options"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		gotOptions = req.Options
		vecs := make([][]float64, len(req.Input))
		for i := range req.Input {
			vecs[i] = []float64{0, 0, 0}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"embeddings": vecs})
	}))
	defer srv.Close()

	oldEndpoint := EmbedEndpoint
	EmbedEndpoint = srv.URL
	defer func() { EmbedEndpoint = oldEndpoint }()

	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table Docs (Id: string, Text: string)`)
	diskExec(t, eng, `.set-or-append Docs <| datatable(Id:string, Text:string) ["a", "x"]`)
	diskExec(t, eng, `.create table DocEmbeddings (Id: string, Embedding: dynamic, Model: string, Provenance: string)`)
	diskExec(t, eng, `.embed-into DocEmbeddings <| Docs | project Id, Text, Provenance = "test"`)

	if gotOptions == nil {
		t.Fatal("expected an options object in the embed request body, got none")
	}
	nb, ok := gotOptions["num_batch"]
	if !ok {
		t.Fatal("expected options.num_batch in the embed request body, key absent")
	}
	// JSON numbers decode as float64.
	if nb != float64(4096) {
		t.Errorf("expected options.num_batch = 4096, got %v", nb)
	}
}
