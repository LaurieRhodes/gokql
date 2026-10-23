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
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/embed"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// DefaultEmbedModel and EmbedEndpoint are the zero-configuration
// Ollama settings. Retained as package-level vars because the existing
// test suite swaps them around an httptest server and expects the
// change to take effect on the next call; the Ollama client itself
// (pkg/embed) takes them as ordinary struct fields.
//
// Prefer Engine.SetEmbedder or GOKQL_EMBED for new code.
var DefaultEmbedModel = "nomic-embed-text"

// EmbedEndpoint is the local Ollama embeddings endpoint.
var EmbedEndpoint = "http://localhost:11434/api/embed"

// embedBackoff is the retry schedule handed to the Ollama client.
// Overridable so tests need not wait out real backoff.
var embedBackoff = []time.Duration{
	200 * time.Millisecond, 500 * time.Millisecond, 1 * time.Second, 2 * time.Second,
}

func evalVectorFunc(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "embed_text":
		if len(fc.Args) < 1 || len(fc.Args) > 2 {
			return nil, true, fmt.Errorf("embed_text requires 1 or 2 arguments (text[, model])")
		}
		textVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if textVal == nil {
			return nil, true, nil
		}
		text := fmt.Sprintf("%v", textVal)

		// Resolve the backend through the seam rather than calling
		// Ollama directly. currentEmbedder() returns the engine's
		// configured embedder, else GOKQL_EMBED, else the historical
		// Ollama default -- so an unconfigured engine behaves exactly
		// as it did before embedder.go existed.
		em := eng.currentEmbedder()

		// An explicit second argument still names a MODEL, not a
		// backend: embed_text(x, "mxbai-embed-large") must keep
		// meaning what it meant. It only makes sense where the
		// backend can switch models per call, which today means
		// Ollama; asking a loaded static model to be a different
		// model mid-query is a category error, so say so rather than
		// silently ignoring the argument and returning vectors from
		// the wrong model.
		if len(fc.Args) == 2 {
			mv, err := evalExpr(eng, fc.Args[1], schema, row)
			if err != nil {
				return nil, true, err
			}
			if mv != nil {
				name := fmt.Sprintf("%v", mv)
				sw, ok := em.(embed.ModelSwitcher)
				if !ok {
					return nil, true, fmt.Errorf(
						"embed_text: backend %q does not support a per-call model argument "+
							"(drop the second argument, or configure that model as the backend)",
						em.Name())
				}
				em2, err := sw.WithModel(name)
				if err != nil {
					return nil, true, fmt.Errorf("embed_text: %w", err)
				}
				em = em2
			}
		}

		vec, err := em.Embed(context.Background(), text)
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
		aVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		bVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if aVal == nil || bVal == nil {
			return nil, true, nil
		}
		av, ok1 := memoFloatArray(eng, fc, 0, aVal)
		bv, ok2 := memoFloatArray(eng, fc, 1, bVal)
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
		aVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		bVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if aVal == nil || bVal == nil {
			return nil, true, nil
		}
		av, ok1 := memoFloatArray(eng, fc, 0, aVal)
		bv, ok2 := memoFloatArray(eng, fc, 1, bVal)
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

// parseFloatArray decodes a dynamic (JSON array) value into []float64.
// Non-numeric elements make the whole parse fail closed (ok=false),
// since a corrupted or mistyped vector should never silently produce a
// wrong similarity score.
func parseFloatArray(val types.Value) ([]float64, bool) {
	// Compact form first: it is the common case for stored vectors and
	// it avoids parseJSONArray's fmt.Sprintf + []interface{} round
	// trip entirely. Falls through to JSON for vectors written before
	// the compact encoding existed, for embed_text()'s own output, and
	// for any array a user supplies inline.
	if s, ok := val.(string); ok {
		if v, ok := decodeCompactVector(s); ok {
			return v, true
		}
		if v, ok := scanJSONFloatArray(s); ok {
			return v, true
		}
	}
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

// scanJSONFloatArray parses a JSON array of plain numbers without
// encoding/json (which decoded every element into an interface and
// dominated similarity ranking over JSON-stored vectors). Anything
// else -- null, strings, nesting, a stray character -- reports
// ok=false and the caller takes the general decoder.
func scanJSONFloatArray(s string) ([]float64, bool) {
	return appendJSONFloats(make([]float64, 0, strings.Count(s, ",")+1), s)
}

// appendJSONFloats is scanJSONFloatArray appending to dst, so a caller
// parsing many vectors can reuse one buffer.
func appendJSONFloats(dst []float64, s string) ([]float64, bool) {
	i, n := 0, len(s)
	skip := func() {
		for i < n && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
			i++
		}
	}
	skip()
	if i >= n || s[i] != '[' {
		return nil, false
	}
	i++
	out := dst
	skip()
	if i < n && s[i] == ']' {
		i++
		skip()
		return out, i == n
	}
	for {
		skip()
		start := i
		for i < n {
			c := s[i]
			if (c >= '0' && c <= '9') || c == '-' || c == '.' || c == 'e' || c == 'E' || c == '+' {
				i++
				continue
			}
			break
		}
		if i == start || s[start] == '+' {
			return nil, false
		}
		f, err := strconv.ParseFloat(s[start:i], 64)
		if err != nil {
			return nil, false
		}
		out = append(out, f)
		skip()
		if i >= n {
			return nil, false
		}
		if s[i] == ']' {
			i++
			skip()
			return out, i == n
		}
		if s[i] != ',' {
			return nil, false
		}
		i++
	}
}

// vecMemo remembers the last vector parsed for one argument of one
// call site, so an argument that is the same on every row (the query
// vector) is decoded once per query rather than once per row.
type vecMemo struct {
	mu sync.Mutex
	s  string
	v  []float64
	ok bool
}

type vecMemoKey struct {
	fc  *parser.FuncCall
	arg int
}

// memoFloatArray is parseFloatArray with a one-entry memo per call
// site and argument. Vectors are only read, never modified, so the
// shared slice is safe.
func memoFloatArray(eng *Engine, fc *parser.FuncCall, arg int, val types.Value) ([]float64, bool) {
	s, isStr := val.(string)
	if !isStr || len(s) < 64 {
		return parseFloatArray(val)
	}
	if eng == nil {
		return parseFloatArray(val)
	}
	m, _ := eng.vecMemos.LoadOrStore(vecMemoKey{fc, arg}, &vecMemo{})
	memo := m.(*vecMemo)
	memo.mu.Lock()
	if memo.v != nil && memo.s == s {
		v, ok := memo.v, memo.ok
		memo.mu.Unlock()
		return v, ok
	}
	memo.mu.Unlock()
	v, ok := parseFloatArray(val)
	if ok {
		memo.mu.Lock()
		memo.s, memo.v, memo.ok = s, v, ok
		memo.mu.Unlock()
	}
	return v, ok
}
