package embed

// openai.go -- an OpenAI-compatible /v1/embeddings client, which is
// the lingua franca every ringfenced embedding server already speaks:
// llama.cpp's llama-server (--embeddings), HuggingFace TEI, vLLM,
// Ollama's own /v1 surface, and every hosted provider.
//
// Stdlib only: net/http and encoding/json. That is the point. gokql's
// KQL-parity surface is pure Go with no inference dependencies, and
// the embedding extension is the only part of the engine that talks to
// anything outside the process. Keeping that boundary at an HTTP
// contract, rather than at a linked ML runtime, is what lets the
// parity claim stay true.
//
// The failure domain is the other half of the point. An embedding
// runner that drops connections under sustained load, or panics on an
// over-long input, does so in ITS process. This client sees a
// connection error or a 5xx and retries. It cannot take the query
// engine down with it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultOpenAIEndpoint is llama-server's default address. Chosen over
// Ollama's 11434 because llama-server is the lighter fence: one binary
// plus one GGUF, no model manager, no background updater, and no
// keep-alive eviction to fight.
const DefaultOpenAIEndpoint = "http://localhost:8080/v1/embeddings"

// OpenAI is an embedder backed by an OpenAI-compatible HTTP endpoint.
// Safe for concurrent use: the only mutable state is the learned
// dimension, which is guarded.
type OpenAI struct {
	Endpoint string
	Model    string

	// APIKey is sent as a bearer token when non-empty. Deliberately
	// NOT settable from an embedder spec string: specs get logged, put
	// in error messages, and listed in process arguments. This is
	// populated from the environment only.
	APIKey string

	// MaxBatch caps inputs per request. Servers differ in what they
	// accept and an over-large batch is a 400 or worse, so chunk
	// conservatively rather than discovering the ceiling in
	// production.
	MaxBatch int

	Client *http.Client

	mu  sync.RWMutex
	dim int // learned from the first successful response
}

// NewOpenAI builds a client. An empty endpoint or model falls back to
// the llama-server defaults.
func NewOpenAI(endpoint, model string) *OpenAI {
	if endpoint == "" {
		endpoint = DefaultOpenAIEndpoint
	}
	return &OpenAI{
		Endpoint: endpoint,
		Model:    model,
		APIKey:   os.Getenv("GOKQL_EMBED_API_KEY"),
		MaxBatch: 32,
		Client:   &http.Client{Timeout: 60 * time.Second},
	}
}

// Name is the value written to the stored Model column. It carries the
// model identity ONLY, not the endpoint: two vectors are comparable if
// they came from the same model, regardless of which host served it,
// and a table that recorded the endpoint would spuriously reject a
// corpus embedded before a server move.
func (o *OpenAI) Name() string {
	if o.Model == "" {
		return "openai-compatible"
	}
	return o.Model
}

// Dimension reports the learned width, or 0 before the first
// successful call. The width is a property of the served model and
// asking costs a round trip, so it is learned rather than configured:
// a configured value that disagreed with reality would be worse than
// no value at all.
func (o *OpenAI) Dimension() int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.dim
}

func (o *OpenAI) learnDimension(n int) {
	o.mu.Lock()
	if o.dim == 0 {
		o.dim = n
	}
	o.mu.Unlock()
}

func (o *OpenAI) Embed(ctx context.Context, text string) ([]float64, error) {
	vecs, err := o.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("expected 1 embedding, got %d", len(vecs))
	}
	return vecs[0], nil
}

// EmbedBatch sends the texts as an input array, chunked at MaxBatch,
// and returns one vector per input in INPUT order.
func (o *OpenAI) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	max := o.MaxBatch
	if max <= 0 {
		max = 32
	}
	out := make([][]float64, 0, len(texts))
	for start := 0; start < len(texts); start += max {
		end := start + max
		if end > len(texts) {
			end = len(texts)
		}
		batch, err := o.embedOneRequest(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("inputs %d-%d: %w", start, end, err)
		}
		if len(batch) != end-start {
			return nil, fmt.Errorf("inputs %d-%d: server returned %d embeddings for %d inputs",
				start, end, len(batch), end-start)
		}
		out = append(out, batch...)
	}
	return out, nil
}

// retry policy. Transient means the server hiccupped (connection drop,
// 5xx); permanent means the request is wrong and retrying cannot fix
// it (bad model, embeddings not enabled, malformed body).
var openAIBackoff = []time.Duration{
	200 * time.Millisecond, 500 * time.Millisecond, 1 * time.Second, 2 * time.Second,
}

func (o *OpenAI) embedOneRequest(ctx context.Context, texts []string) ([][]float64, error) {
	body, err := json.Marshal(map[string]interface{}{
		"model": o.Model,
		"input": texts,
	})
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt <= len(openAIBackoff); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(openAIBackoff[attempt-1]):
			}
		}
		vecs, retryable, err := o.embedOnce(ctx, body)
		if err == nil {
			return vecs, nil
		}
		lastErr = err
		if !retryable {
			return nil, err
		}
	}
	return nil, fmt.Errorf("failed after %d attempts: %w", len(openAIBackoff)+1, lastErr)
}

func (o *OpenAI) embedOnce(ctx context.Context, body []byte) (vecs [][]float64, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}

	resp, err := o.Client.Do(req)
	if err != nil {
		// Distinguish "you cancelled" from "the server is down", or a
		// cancelled ingest reports itself as an unreachable endpoint.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		return nil, true, fmt.Errorf("could not reach embedding server at %s: %w", o.Endpoint, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}

	// 501 is in the 5xx range but is NOT transient: llama-server
	// answers 501 when it was started without --embeddings, and no
	// amount of backoff makes a flag appear. Found by test, not by
	// reading -- the naive "5xx means retry" rule burned four retries
	// and ~3.7s before surfacing a configuration error that was
	// knowable on the first response.
	if resp.StatusCode == http.StatusNotImplemented {
		return nil, false, fmt.Errorf("embedding request rejected (status %d): %s%s",
			resp.StatusCode, snippet(raw), hint(resp.StatusCode, raw))
	}
	// Some 5xx are configuration errors, not hiccups, and retrying
	// them just delays the message that says what to fix. Found live
	// against llama-server: an oversized input returns 500 with
	// "input (N tokens) is too large to process. increase the physical
	// batch size (current batch size: 512)". That is a --ubatch-size
	// setting, not a transient fault, and the naive 5xx rule burned
	// four retries and ~3.7s before surfacing it. Same shape as the
	// 501 case above.
	if resp.StatusCode >= 500 && isPermanent5xx(raw) {
		return nil, false, fmt.Errorf("embedding request rejected (status %d): %s%s",
			resp.StatusCode, snippet(raw), hint(resp.StatusCode, raw))
	}
	if resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("embedding server error (status %d): %s", resp.StatusCode, snippet(raw))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("embedding request rejected (status %d): %s%s",
			resp.StatusCode, snippet(raw), hint(resp.StatusCode, raw))
	}

	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Model string `json:"model"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, false, fmt.Errorf("parsing response: %w (body: %s)", err, snippet(raw))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, false, fmt.Errorf("embedding server: %s", parsed.Error.Message)
	}
	if len(parsed.Data) == 0 {
		return nil, true, fmt.Errorf("embedding server returned no data")
	}

	// The response carries an explicit index per object and the spec
	// does NOT promise array order matches input order. Sorting by
	// index is the difference between correct vectors and vectors
	// silently attached to the wrong rows, which is unfixable after
	// ingest and invisible at query time.
	sort.Slice(parsed.Data, func(i, j int) bool { return parsed.Data[i].Index < parsed.Data[j].Index })
	for i, d := range parsed.Data {
		if d.Index != i {
			return nil, false, fmt.Errorf("response index %d missing or duplicated (got %d at position %d)",
				i, d.Index, i)
		}
	}

	out := make([][]float64, len(parsed.Data))
	for i, d := range parsed.Data {
		if len(d.Embedding) == 0 {
			return nil, false, fmt.Errorf("embedding %d is empty", i)
		}
		out[i] = d.Embedding
	}

	// Width must be consistent within a response and across the
	// client's life. A server reloaded onto a different model mid-run
	// would otherwise write two incompatible vector spaces into one
	// table under one Model name.
	width := len(out[0])
	for i, v := range out {
		if len(v) != width {
			return nil, false, fmt.Errorf("embedding %d has width %d, expected %d", i, len(v), width)
		}
	}
	if known := o.Dimension(); known != 0 && known != width {
		return nil, false, fmt.Errorf(
			"embedding server changed vector width mid-session (%d, was %d) -- "+
				"the served model appears to have changed; stop and re-embed rather than mixing spaces",
			width, known)
	}
	o.learnDimension(width)

	return out, false, nil
}

// isPermanent5xx recognises 5xx bodies that describe a configuration
// problem rather than a fault. Deliberately narrow: anything not
// matched here keeps the retry behaviour, because misclassifying a
// real hiccup as permanent would reintroduce the connection-drop
// failures the retry policy exists for.
func isPermanent5xx(body []byte) bool {
	b := strings.ToLower(string(body))
	return strings.Contains(b, "too large to process") ||
		strings.Contains(b, "does not support embeddings")
}

// hint turns the failure modes a first-time user actually hits
// into instructions rather than status codes.
func hint(status int, body []byte) string {
	b := strings.ToLower(string(body))
	switch {
	case strings.Contains(b, "does not support embeddings"), status == http.StatusNotImplemented:
		return "\nhint: llama-server must be started with --embeddings (and an explicit --pooling " +
			"matching how the model was trained; the GGUF default is set by whoever converted it)"
	case strings.Contains(b, "too large to process"):
		return "\nhint: the input exceeds the server's PHYSICAL batch size. Restart with " +
			"--ubatch-size >= your largest chunk in tokens (and -c at least as large). " +
			"gokql's .chunk-file default of 1500 chars is roughly 375 tokens"
	case status == http.StatusNotFound:
		return "\nhint: check the endpoint path -- it should end in /v1/embeddings"
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return "\nhint: set GOKQL_EMBED_API_KEY if this endpoint requires a token"
	}
	return ""
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
