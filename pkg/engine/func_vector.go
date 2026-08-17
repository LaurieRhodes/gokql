package engine

// func_vector.go — vector similarity as native KQL, no external vector
// database.
//
// Mirrors Azure Data Explorer's own documented pattern: a vector is
// just a `dynamic` array of numbers; similarity is a scalar function
// over two arrays (series_cosine_similarity, same name ADX uses, so a
// model's KQL training-data familiarity transfers directly); ranking
// is `top N by similarity`. No separate database, no new storage
// engine, no MCP dependency — the existing dynamic-type and
// columnar-scan machinery does all of it.
//
// The one piece ADX's design doesn't need to answer (Azure already has
// managed embedding endpoints) is where vectors come from locally:
// embed_text() calls a self-hosted Ollama instance already running on
// this machine for other purposes, over the loopback interface, with
// no API key and no new service to stand up.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// EmbedModel is the Ollama model used by embed_text(). Overridable per
// engine instance for testing or for switching embedding models
// without a rebuild.
var DefaultEmbedModel = "nomic-embed-text"

// EmbedEndpoint is the local Ollama embeddings endpoint.
var EmbedEndpoint = "http://localhost:11434/api/embed"

var embedHTTPClient = &http.Client{Timeout: 30 * time.Second}

func evalVectorFunc(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "embed_text":
		if len(fc.Args) < 1 || len(fc.Args) > 2 {
			return nil, true, fmt.Errorf("embed_text requires 1 or 2 arguments (text[, model])")
		}
		textVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if textVal == nil {
			return nil, true, nil
		}
		text := fmt.Sprintf("%v", textVal)
		model := DefaultEmbedModel
		if len(fc.Args) == 2 {
			mv, err := evalExpr(fc.Args[1], schema, row)
			if err != nil {
				return nil, true, err
			}
			if mv != nil {
				model = fmt.Sprintf("%v", mv)
			}
		}
		vec, err := embedText(text, model)
		if err != nil {
			return nil, true, fmt.Errorf("embed_text: %w", err)
		}
		b, err := json.Marshal(vec)
		if err != nil {
			return nil, true, err
		}
		return string(b), true, nil

	case "series_cosine_similarity":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("series_cosine_similarity requires 2 arguments")
		}
		aVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		bVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if aVal == nil || bVal == nil {
			return nil, true, nil
		}
		av, ok1 := parseFloatArray(aVal)
		bv, ok2 := parseFloatArray(bVal)
		if !ok1 || !ok2 {
			return nil, true, nil
		}
		sim, err := cosineSimilarity(av, bv)
		if err != nil {
			return nil, true, err
		}
		return sim, true, nil

	case "series_dot_product":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("series_dot_product requires 2 arguments")
		}
		aVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		bVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if aVal == nil || bVal == nil {
			return nil, true, nil
		}
		av, ok1 := parseFloatArray(aVal)
		bv, ok2 := parseFloatArray(bVal)
		if !ok1 || !ok2 {
			return nil, true, nil
		}
		if len(av) != len(bv) {
			return nil, true, fmt.Errorf("series_dot_product: vector length mismatch (%d vs %d)", len(av), len(bv))
		}
		var dot float64
		for i := range av {
			dot += av[i] * bv[i]
		}
		return dot, true, nil
	}
	return nil, false, nil
}

// embedRetries and embedBackoff govern retry behavior for transient
// Ollama failures. Found live, reproducibly, embedding a 134-chunk
// batch: Ollama's internal embedding subprocess drops the connection
// ("EOF") under sustained back-to-back sequential requests, roughly
// every ~130-150 calls, recovering on its own — not caused by any
// particular input (confirmed: same failure point on retry with
// identical data, and Ollama's own /api/tags stayed healthy
// throughout). This looks like an internal idle/keepalive issue in
// Ollama's embedding runner rather than anything on our side, so the
// robust fix is retrying past it, not avoiding it.
const embedRetries = 4

var embedBackoff = []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1 * time.Second, 2 * time.Second}

// embedText calls the local Ollama embeddings endpoint, retrying
// transient failures (connection drops, 5xx) with backoff. A
// connection failure that persists past all retries surfaces as a
// clear error naming the endpoint, not a silent empty vector.
func embedText(text, model string) ([]float64, error) {
	reqBody, err := json.Marshal(map[string]interface{}{
		"model": model,
		"input": text,
		// Keep the model resident between calls. Found live: batch-
		// embedding 134 chunks failed reproducibly with the model
		// unloading mid-batch (evidenced by a nonzero load_duration
		// on an otherwise-healthy request) — likely evicted under
		// memory pressure from other large local models, then the
		// in-flight request to the old subprocess sees EOF during
		// teardown. Default Ollama keep-alive is short; ask for
		// longer explicitly rather than relying on retry timing to
		// outlast an unpredictable reload.
		"keep_alive": "10m",
		// num_batch (per-request options, NOT the OLLAMA_NUM_BATCH
		// server env var) -- see embed_bulk.go's own, matching fix for
		// the full explanation: confirmed via direct testing by a
		// different model (Kimi) that only this per-request override
		// actually changes the runner's batch size; the server-side
		// env var alone does not. Without it, a single input near or
		// above ~512 tokens panics the runner outright rather than
		// truncating or erroring cleanly.
		"options": map[string]interface{}{"num_batch": 4096},
	})
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt <= embedRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(embedBackoff[(attempt-1)%len(embedBackoff)])
		}
		vec, retryable, err := embedTextOnce(reqBody, model)
		if err == nil {
			return vec, nil
		}
		lastErr = err
		if !retryable {
			return nil, err
		}
	}
	return nil, fmt.Errorf("embed_text: failed after %d attempts: %w", embedRetries+1, lastErr)
}

// embedTextOnce makes a single attempt. retryable distinguishes
// transient failures (connection errors, 5xx — Ollama's own runner
// hiccupping) from permanent ones (model not found, malformed
// response) that retrying cannot fix.
func embedTextOnce(reqBody []byte, model string) (vec []float64, retryable bool, err error) {
	resp, err := embedHTTPClient.Post(EmbedEndpoint, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, true, fmt.Errorf("could not reach Ollama at %s (is it running? try 'ollama serve'): %w", EmbedEndpoint, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}
	if resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("ollama embeddings request failed (status %d): %s", resp.StatusCode, string(body))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("ollama embeddings request failed (status %d): %s", resp.StatusCode, string(body))
	}
	var parsed struct {
		Embeddings [][]float64 `json:"embeddings"`
		Error      string      `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, false, fmt.Errorf("parsing ollama response: %w (body: %s)", err, string(body))
	}
	if parsed.Error != "" {
		return nil, false, fmt.Errorf("ollama error: %s (is model %q pulled? try 'ollama pull %s')", parsed.Error, model, model)
	}
	if len(parsed.Embeddings) == 0 {
		return nil, true, fmt.Errorf("ollama returned no embeddings")
	}
	return parsed.Embeddings[0], false, nil
}

// parseFloatArray decodes a dynamic (JSON array) value into []float64.
// Non-numeric elements make the whole parse fail closed (ok=false),
// since a corrupted or mistyped vector should never silently produce a
// wrong similarity score.
func parseFloatArray(val types.Value) ([]float64, bool) {
	arr, ok := parseJSONArray(val)
	if !ok {
		return nil, false
	}
	out := make([]float64, len(arr))
	for i, el := range arr {
		switch v := el.(type) {
		case float64:
			out[i] = v
		case json.Number:
			f, err := v.Float64()
			if err != nil {
				return nil, false
			}
			out[i] = f
		default:
			return nil, false
		}
	}
	return out, true
}

// cosineSimilarity computes cosine similarity between two vectors.
// Mismatched lengths are a hard error (comparing embeddings from two
// different models, or a truncated vector, must not silently produce
// a plausible-looking wrong number — exactly the class of failure this
// whole project has spent itself catching elsewhere).
func cosineSimilarity(a, b []float64) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("series_cosine_similarity: vector length mismatch (%d vs %d) — "+
			"likely comparing embeddings from two different models", len(a), len(b))
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0, nil
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb)), nil
}
