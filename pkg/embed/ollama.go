package embed

// ollama.go -- Ollama's native /api/embed backend.
//
// Moved here from pkg/engine (func_vector.go and embed_bulk.go), where
// an HTTP client, a retry policy and a set of hard-won workarounds for
// one vendor's runner were living inside a query engine. Behaviour is
// unchanged: same endpoint, same retry policy, same keep_alive, same
// num_batch override, same error text.
//
// The comments below are the operational findings from the original
// code. They are the most valuable thing in this file and are
// preserved verbatim in substance, because every one of them cost a
// production failure to learn.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Ollama talks to Ollama's native embedding endpoint. Safe for
// concurrent use; all fields are read-only after construction.
type Ollama struct {
	Endpoint string
	Model    string

	// Batch caps inputs per request. Ollama's /api/embed natively
	// accepts an array and returns one embedding per input in order,
	// so this is a real batch, not a loop.
	Batch int

	// Backoff is the retry schedule for transient failures. Found
	// live, reproducibly, embedding a 134-chunk batch: Ollama's
	// internal embedding subprocess drops the connection ("EOF")
	// under sustained back-to-back sequential requests, roughly every
	// ~130-150 calls, recovering on its own. Not caused by any
	// particular input (confirmed: same failure point on retry with
	// identical data, and Ollama's own /api/tags stayed healthy
	// throughout). This looks like an internal idle/keepalive issue
	// in Ollama's embedding runner rather than anything on our side,
	// so the robust fix is retrying past it, not avoiding it.
	Backoff []time.Duration

	Client *http.Client
}

var defaultOllamaBackoff = []time.Duration{
	200 * time.Millisecond, 500 * time.Millisecond, 1 * time.Second, 2 * time.Second,
}

var defaultOllamaClient = &http.Client{Timeout: 30 * time.Second}

func (o *Ollama) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return defaultOllamaClient
}

func (o *Ollama) backoff() []time.Duration {
	if len(o.Backoff) > 0 {
		return o.Backoff
	}
	return defaultOllamaBackoff
}

// Name is the model identity written to the stored Model column, not
// the backend: the same model served from anywhere produces the same
// vector space.
func (o *Ollama) Name() string { return o.Model }

// Dimension is unknown until Ollama answers. The width is a property
// of whichever model happens to be pulled, and asking costs a round
// trip.
func (o *Ollama) Dimension() int { return 0 }

// WithModel returns a sibling for a different model. Ollama can switch
// per call because the model is a field in the request.
func (o *Ollama) WithModel(model string) (Embedder, error) {
	if model == "" || model == o.Model {
		return o, nil
	}
	cp := *o
	cp.Model = model
	return &cp, nil
}

var _ ModelSwitcher = (*Ollama)(nil)

// requestBody builds the /api/embed payload. input is either a string
// (single) or []string (batch); Ollama accepts both on the same
// endpoint.
func (o *Ollama) requestBody(input interface{}) ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"model": o.Model,
		"input": input,
		// Keep the model resident between calls. Found live:
		// batch-embedding 134 chunks failed reproducibly with the
		// model unloading mid-batch (evidenced by a nonzero
		// load_duration on an otherwise-healthy request), likely
		// evicted under memory pressure from other large local
		// models, then the in-flight request to the old subprocess
		// sees EOF during teardown. Default Ollama keep-alive is
		// short; ask for longer explicitly rather than relying on
		// retry timing to outlast an unpredictable reload.
		"keep_alive": "10m",
		// num_batch as a PER-REQUEST option, NOT the OLLAMA_NUM_BATCH
		// server env var: confirmed by direct testing that the runner
		// loads with BatchSize:512 regardless of the server-side env
		// var, and only a per-request override actually changes it.
		// Without this, any input whose token count exceeds the
		// runner's batch size panics the runner outright ("caching
		// disabled but unable to fit entire input in a batch") rather
		// than truncating or erroring cleanly. Reproduced
		// deterministically and bisected to ~512 tokens. 4096 is
		// verified headroom over that threshold, not a guess.
		"options": map[string]interface{}{"num_batch": 4096},
	})
}

func (o *Ollama) Embed(ctx context.Context, text string) ([]float64, error) {
	body, err := o.requestBody(text)
	if err != nil {
		return nil, err
	}
	vecs, err := o.do(ctx, body, 1)
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

func (o *Ollama) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	max := o.Batch
	if max <= 0 {
		max = 20
	}
	out := make([][]float64, 0, len(texts))
	for start := 0; start < len(texts); start += max {
		end := start + max
		if end > len(texts) {
			end = len(texts)
		}
		body, err := o.requestBody(texts[start:end])
		if err != nil {
			return nil, err
		}
		batch, err := o.do(ctx, body, end-start)
		if err != nil {
			return nil, fmt.Errorf("batch %d-%d: %w", start, end, err)
		}
		out = append(out, batch...)
	}
	return out, nil
}

// do issues one request with retries, expecting want embeddings back.
func (o *Ollama) do(ctx context.Context, body []byte, want int) ([][]float64, error) {
	backoff := o.backoff()
	var lastErr error
	for attempt := 0; attempt <= len(backoff); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff[attempt-1]):
			}
		}
		vecs, retryable, err := o.once(ctx, body)
		if err == nil {
			if len(vecs) != want {
				return nil, fmt.Errorf("ollama returned %d embeddings for %d inputs", len(vecs), want)
			}
			return vecs, nil
		}
		lastErr = err
		if !retryable {
			return nil, err
		}
	}
	return nil, fmt.Errorf("failed after %d attempts: %w", len(backoff)+1, lastErr)
}

// once makes a single attempt. retryable distinguishes transient
// failures (connection errors, 5xx -- Ollama's own runner hiccupping)
// from permanent ones (model not found, malformed response) that
// retrying cannot fix.
func (o *Ollama) once(ctx context.Context, body []byte) (vecs [][]float64, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client().Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		return nil, true, fmt.Errorf("could not reach Ollama at %s (is it running? try 'ollama serve'): %w",
			o.Endpoint, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}
	if resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("ollama embeddings request failed (status %d): %s", resp.StatusCode, string(raw))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("ollama embeddings request failed (status %d): %s", resp.StatusCode, string(raw))
	}

	var parsed struct {
		Embeddings [][]float64 `json:"embeddings"`
		Error      string      `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, false, fmt.Errorf("parsing ollama response: %w (body: %s)", err, string(raw))
	}
	if parsed.Error != "" {
		return nil, false, fmt.Errorf("ollama error: %s (is model %q pulled? try 'ollama pull %s')",
			parsed.Error, o.Model, o.Model)
	}
	if len(parsed.Embeddings) == 0 {
		return nil, true, fmt.Errorf("ollama returned no embeddings")
	}
	return parsed.Embeddings, false, nil
}
