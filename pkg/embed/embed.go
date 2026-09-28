// Package embed defines the backend-agnostic contract for turning text
// into vectors, so the engine's vector functions (embed_text,
// .embed-into) depend on a behaviour, not on Ollama.
//
// The engine previously reached a hardcoded http://localhost:11434
// literal from inside a scalar-function evaluator. That worked on one
// dev machine and nowhere else: no environment override, no way to
// test without standing up a daemon, and no way to run at all on a
// host where Ollama isn't installed. This package is the seam that
// fixes all three.
//
// Deliberately NOT in pkg/engine: an embedding backend is a host
// concern, and a host embedding this engine as a Go module (see
// ExternalFunctionResolver's own doc comment for the established
// precedent) should be able to supply its own without importing the
// query engine's internals.
package embed

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strconv"
	"strings"
)

// Embedder turns text into vectors. Implementations must be safe for
// concurrent use: this engine's per-extent scan already runs goroutines
// bounded by NumCPU, and an embedder reached from a scalar function is
// reachable from inside that scan.
//
// Vectors are []float64 to match what the engine already stores and
// what series_cosine_similarity already consumes. float32 would halve
// storage and roughly halve the JSON-decode cost that dominates
// ranking queries, but that's a storage-format change and belongs with
// one, not smuggled in here.
type Embedder interface {
	// Embed returns a vector for a single text.
	Embed(ctx context.Context, text string) ([]float64, error)

	// EmbedBatch returns one vector per input, in input order, and
	// must return exactly len(texts) vectors or an error. Callers
	// rely on positional correspondence; a backend that cannot
	// preserve order must not implement this by reordering.
	EmbedBatch(ctx context.Context, texts []string) ([][]float64, error)

	// Dimension reports the vector width, or 0 if the backend can't
	// know before its first call. Used to reject cross-model
	// comparisons before they silently produce a plausible-looking
	// wrong similarity score.
	Dimension() int

	// Name identifies the model for the stored Model column. Two
	// different models of the SAME width produce vectors that compare
	// without error and rank nonsense, so this value is a correctness
	// input, not a label.
	Name() string
}

// ErrNoEmbedder is returned when a vector function is called on an
// engine with no embedder configured and no default available.
var ErrNoEmbedder = errors.New("no embedding backend configured")

// ModelSwitcher is the optional capability embed_text(text, model)
// needs: a backend that can serve a different model on the next call.
// An HTTP backend has it (the model is a field in the request); a
// backend with fixed loaded weights does not.
//
// A NAMED interface, not an inline anonymous one. The duck-typed
// version of this was asserted in two separate files, and any drift in
// the signature would have failed the assertion silently, reporting
// that a backend cannot switch models when it can. Implementations
// assert conformance at compile time (see ollamaEmbedder in the engine
// package) so drift is a build error instead.
type ModelSwitcher interface {
	Embedder
	WithModel(model string) (Embedder, error)
}

// EmbedBatchVia implements EmbedBatch by looping over Embed. For
// backends with no native batch API (every in-process one, since
// there's no round trip to amortise), this is the whole
// implementation.
func EmbedBatchVia(ctx context.Context, e Embedder, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i, t := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		v, err := e.Embed(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("text %d of %d: %w", i, len(texts), err)
		}
		out[i] = v
	}
	return out, nil
}

// --- Mock ---

// Mock is a deterministic, dependency-free embedder: a hashed
// bag-of-words projected onto the unit sphere. Semantically worthless,
// but stable across runs and processes, which is exactly what a test
// needs. Its existence means the whole vector test suite can run with
// no daemon, no model files, and no network.
//
// Name() carries the "mock" prefix so a vector accidentally written to
// a real table by a misconfigured run is identifiable after the fact
// rather than indistinguishable from a real one.
type Mock struct{ Dim int }

// NewMock returns a Mock of the given width (default 8).
func NewMock(dim int) *Mock {
	if dim <= 0 {
		dim = 8
	}
	return &Mock{Dim: dim}
}

func (m *Mock) Name() string   { return "mock-" + strconv.Itoa(m.Dim) }
func (m *Mock) Dimension() int { return m.Dim }

func (m *Mock) Embed(_ context.Context, text string) ([]float64, error) {
	v := make([]float64, m.Dim)
	for _, tok := range strings.Fields(strings.ToLower(text)) {
		h := fnv.New64a()
		_, _ = h.Write([]byte(tok))
		s := h.Sum64()
		idx := int(s % uint64(m.Dim))
		sign := 1.0
		if s&(1<<63) != 0 {
			sign = -1.0
		}
		v[idx] += sign
	}
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	if norm == 0 {
		// Empty or whitespace-only input. A zero vector would make
		// cosineSimilarity return 0 for everything, which reads as
		// "unrelated" rather than "no input" -- pick a fixed non-zero
		// direction instead so the degenerate case is at least
		// self-consistent.
		v[0] = 1
		return v, nil
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] /= norm
	}
	return v, nil
}

func (m *Mock) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	return EmbedBatchVia(ctx, m, texts)
}
