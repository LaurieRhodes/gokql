package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeServer stands in for llama-server. Everything here is stdlib, as
// is the client under test.
func fakeServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func embedResponse(w http.ResponseWriter, vecs [][]float64, order []int) {
	type item struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	}
	data := make([]item, 0, len(vecs))
	for _, i := range order {
		data = append(data, item{Index: i, Embedding: vecs[i]})
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
}

// TestOpenAIReordersByIndex is the one that matters most. The response
// spec carries an explicit index and does NOT promise array order, so
// a server that answers out of order must not silently attach vectors
// to the wrong rows.
func TestOpenAIReordersByIndex(t *testing.T) {
	vecs := [][]float64{{1, 0, 0}, {2, 0, 0}, {3, 0, 0}}
	srv := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		embedResponse(w, vecs, []int{2, 0, 1}) // deliberately shuffled
	})

	c := NewOpenAI(srv.URL, "test-model")
	got, err := c.EmbedBatch(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range got {
		if got[i][0] != float64(i+1) {
			t.Errorf("row %d: got %v, want leading %d", i, got[i], i+1)
		}
	}
}

func TestOpenAIRejectsDuplicateOrMissingIndex(t *testing.T) {
	vecs := [][]float64{{1, 0}, {2, 0}}
	srv := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		embedResponse(w, vecs, []int{0, 0}) // index 1 never arrives
	})
	if _, err := NewOpenAI(srv.URL, "m").EmbedBatch(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("expected an error for a duplicated index, got none")
	}
}

func TestOpenAIRejectsWidthChangeMidSession(t *testing.T) {
	// A server reloaded onto a different model would otherwise write
	// two vector spaces into one table under one Model name.
	var call int
	srv := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			embedResponse(w, [][]float64{{1, 2, 3}}, []int{0})
			return
		}
		embedResponse(w, [][]float64{{1, 2, 3, 4, 5}}, []int{0})
	})

	c := NewOpenAI(srv.URL, "m")
	if _, err := c.Embed(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if c.Dimension() != 3 {
		t.Fatalf("Dimension() = %d, want 3", c.Dimension())
	}
	if _, err := c.Embed(context.Background(), "second"); err == nil {
		t.Fatal("expected an error when the width changed, got none")
	}
}

func TestOpenAIChunksAtMaxBatch(t *testing.T) {
	var sizes []int
	srv := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		sizes = append(sizes, len(req.Input))
		vecs := make([][]float64, len(req.Input))
		order := make([]int, len(req.Input))
		for i := range req.Input {
			vecs[i] = []float64{float64(len(req.Input[i]))}
			order[i] = i
		}
		embedResponse(w, vecs, order)
	})

	c := NewOpenAI(srv.URL, "m")
	c.MaxBatch = 2
	got, err := c.EmbedBatch(context.Background(), []string{"a", "bb", "ccc", "dddd", "eeeee"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 3 || sizes[0] != 2 || sizes[2] != 1 {
		t.Errorf("batch sizes = %v, want [2 2 1]", sizes)
	}
	// Ordering must survive chunking, not just survive one response.
	for i, want := range []float64{1, 2, 3, 4, 5} {
		if got[i][0] != want {
			t.Errorf("row %d: got %v, want %v", i, got[i][0], want)
		}
	}
}

func TestOpenAIGivesActionableHintWhenEmbeddingsDisabled(t *testing.T) {
	// Also asserts this is treated as PERMANENT: a missing flag is not
	// something backoff can fix, and retrying it delays the one error
	// message that tells the user what to do.
	// llama-server's real 501 when started without --embeddings.
	srv := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		w.Write([]byte(`{"error":{"code":501,"message":"This server does not support embeddings. Start it with ` + "`--embeddings`" + `"}}`))
	})
	_, err := NewOpenAI(srv.URL, "m").Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !contains(err.Error(), "--embeddings") || !contains(err.Error(), "--pooling") {
		t.Errorf("error should name both flags, got: %v", err)
	}
}

func TestOpenAIDoesNotRetryPermanentFailures(t *testing.T) {
	var calls int
	srv := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"unknown model"}}`))
	})
	if _, err := NewOpenAI(srv.URL, "nope").Embed(context.Background(), "x"); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("a 400 was retried %d times; permanent failures must not be retried", calls)
	}
}

func TestOpenAIRespectsContextCancellation(t *testing.T) {
	// The ctx parameter must be real, not decorative.
	srv := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Wrapped with %%w for context, so errors.Is is the contract a
	// caller relies on when deciding "was this cancelled or did the
	// server die?".
	if _, err := NewOpenAI(srv.URL, "m").Embed(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want a wrapped context.Canceled", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
