package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A server is not a trusted peer. It can be a proxy, a misconfigured
// gateway, a different product on the same port, or a healthy server
// mid-restart. None of these should produce a vector.
func TestOpenAIHostileResponses(t *testing.T) {
	cases := []struct {
		name string
		body string
		code int
	}{
		{"empty body", "", 200},
		{"html error page", "<html><body>502 Bad Gateway</body></html>", 200},
		{"json but not embeddings", `{"result":"ok"}`, 200},
		{"data present but empty array", `{"data":[]}`, 200},
		{"embedding is null", `{"data":[{"index":0,"embedding":null}]}`, 200},
		{"embedding is empty", `{"data":[{"index":0,"embedding":[]}]}`, 200},
		{"embedding is strings", `{"data":[{"index":0,"embedding":["a","b"]}]}`, 200},
		{"negative index", `{"data":[{"index":-1,"embedding":[1,2]}]}`, 200},
		{"index beyond count", `{"data":[{"index":7,"embedding":[1,2]}]}`, 200},
		{"error field set with 200", `{"error":{"message":"model not loaded"}}`, 200},
		{"truncated json", `{"data":[{"index":0,"embed`, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			c := NewOpenAI(srv.URL, "m")
			c.Client = &http.Client{Timeout: 3 * time.Second}
			// No backoff: these are all permanent-or-empty, and a test
			// should not wait out a retry schedule to prove it.
			v, err := c.Embed(context.Background(), "x")
			if err == nil {
				t.Fatalf("accepted a hostile response and returned %d values", len(v))
			}
			if len(v) != 0 {
				t.Errorf("returned %d values alongside an error", len(v))
			}
		})
	}
}

// TestOpenAIMismatchedBatchCount: a server returning the wrong number
// of vectors would otherwise misalign every row after the gap.
func TestOpenAIMismatchedBatchCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,2,3]}]}`) // 1 back for 3 asked
	}))
	defer srv.Close()
	if _, err := NewOpenAI(srv.URL, "m").EmbedBatch(context.Background(), []string{"a", "b", "c"}); err == nil {
		t.Fatal("accepted 1 embedding for 3 inputs")
	}
}

// TestOpenAIRaggedWidthsWithinOneResponse: a response whose vectors
// disagree in width cannot all belong to one model.
func TestOpenAIRaggedWidthsWithinOneResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,2,3]},{"index":1,"embedding":[1,2]}]}`)
	}))
	defer srv.Close()
	_, err := NewOpenAI(srv.URL, "m").EmbedBatch(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("accepted a response with ragged vector widths")
	}
	if !strings.Contains(err.Error(), "width") {
		t.Errorf("error should name the width problem, got: %v", err)
	}
}

// TestOpenAIHugeBatchIsChunked: a caller passing the whole corpus at
// once must not become one enormous request.
func TestOpenAIHugeBatchIsChunked(t *testing.T) {
	var maxSeen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Input) > maxSeen {
			maxSeen = len(req.Input)
		}
		w.Write([]byte(buildResponse(len(req.Input))))
	}))
	defer srv.Close()

	c := NewOpenAI(srv.URL, "m")
	texts := make([]string, 1000)
	for i := range texts {
		texts[i] = "t"
	}
	got, err := c.EmbedBatch(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1000 {
		t.Fatalf("got %d vectors, want 1000", len(got))
	}
	if maxSeen > c.MaxBatch {
		t.Errorf("largest request had %d inputs, exceeding MaxBatch %d", maxSeen, c.MaxBatch)
	}
}

func buildResponse(n int) string {
	var b strings.Builder
	b.WriteString(`{"data":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"index":%d,"embedding":[1,0,0]}`, i)
	}
	b.WriteString(`]}`)
	return b.String()
}

// TestOpenAIEmptyDataIsNotRetried: a 200 with no embeddings is a
// protocol violation, not a hiccup. Retrying it only delays the error
// message that says what is wrong.
func TestOpenAIEmptyDataIsNotRetried(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	start := time.Now()
	if _, err := NewOpenAI(srv.URL, "m").Embed(context.Background(), "x"); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("retried %d times; a malformed 200 must not be retried", calls)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s; a permanent failure should surface immediately", elapsed.Round(time.Millisecond))
	}
}
