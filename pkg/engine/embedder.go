package engine

// embedder.go -- the engine's side of the embedding seam.
//
// Before this file, embed_text() and .embed-into both reached a
// package-level EmbedEndpoint literal pointing at localhost:11434.
// This replaces that reach with an embed.Embedder lookup, without
// changing what the default configuration does: an engine that sets
// nothing still talks to Ollama at the same address with the same
// retry, keep_alive and num_batch behaviour, so every existing test
// and every existing caller is unaffected.
//
// The Ollama HTTP code itself deliberately stays where it is
// (func_vector.go, embed_bulk.go) for now. Moving it into pkg/embed is
// the obvious follow-up, but it would churn six test files in the same
// change that introduces the seam, and those two things should be
// reviewable separately.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/LaurieRhodes/gokql/pkg/embed"
)

// ollamaEmbedder adapts the existing Ollama HTTP functions to the
// embed.Embedder interface. It reads the package-level EmbedEndpoint
// and DefaultEmbedModel at CALL time, not construction time, because
// the existing test suite swaps those vars around an httptest server
// and expects the change to take effect on the next call.
var _ embed.ModelSwitcher = (*ollamaEmbedder)(nil)

// ollamaEmbedder binds the package-level EmbedEndpoint,
// DefaultEmbedModel, EmbedBatchSize and embedBackoff vars to
// pkg/embed's Ollama client.
//
// The indirection exists because those vars are read at CALL time, not
// construction time: the existing test suite swaps EmbedEndpoint
// around an httptest server and expects the next call to use it.
// Constructing a throwaway client per call is cheap (the client is a
// plain struct over a shared http.Client) and keeps that contract.
type ollamaEmbedder struct {
	model string // empty means "whatever DefaultEmbedModel says right now"
}

func (o *ollamaEmbedder) modelName() string {
	if o.model != "" {
		return o.model
	}
	return DefaultEmbedModel
}

func (o *ollamaEmbedder) client() *embed.Ollama {
	return &embed.Ollama{
		Endpoint: EmbedEndpoint,
		Model:    o.modelName(),
		Batch:    EmbedBatchSize,
		Backoff:  embedBackoff,
	}
}

// Name returns the MODEL identity only, not the backend. The same
// model served by Ollama or by llama-server produces the same vector
// space, so a table embedded before a server move stays comparable
// with one embedded after. This also keeps the stored Model column
// byte-identical to what pre-seam runs wrote.
func (o *ollamaEmbedder) Name() string   { return o.modelName() }
func (o *ollamaEmbedder) Dimension() int { return 0 }

func (o *ollamaEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	return o.client().Embed(ctx, text)
}

func (o *ollamaEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	return o.client().EmbedBatch(ctx, texts)
}

// WithModel is the optional capability embed_text(text, model) needs.
func (o *ollamaEmbedder) WithModel(model string) (embed.Embedder, error) {
	if model == "" {
		return o, nil
	}
	return &ollamaEmbedder{model: model}, nil
}

// --- resolution ---

// SetEmbedder sets the backend for this engine only.
func (e *Engine) SetEmbedder(em embed.Embedder) { e.Embedder = em }

// beginEmbedderScope installs a statement-scoped memo in front of this
// engine's backend for the outermost statement and returns the leave
// function; nested statements share it. The memo is per engine: the
// evaluator reaches it through the engine it is threaded with, so two
// engines with different backends run concurrently without seeing
// each other's. An earlier process-wide ambient refused that case with
// ErrConcurrentEmbedders.
func (e *Engine) beginEmbedderScope() func() {
	if e.stmtEmbedder != nil {
		return func() {}
	}
	e.stmtEmbedder = embed.NewCaching(e.resolveEmbedder())
	return func() { e.stmtEmbedder = nil }
}

// resolveEmbedder returns the embedder for this engine, falling back
// to the environment and finally to the historical Ollama default.
// Never returns nil.
func (e *Engine) resolveEmbedder() embed.Embedder {
	if e != nil && e.Embedder != nil {
		return e.Embedder
	}
	return defaultEmbedder()
}

// currentEmbedder is what the scalar embed_text() path calls: the
// statement's memoised embedder, or, outside a statement (a direct
// evalExpr call in a test), a memo over the engine's backend.
func (e *Engine) currentEmbedder() embed.Embedder {
	if e != nil && e.stmtEmbedder != nil {
		return e.stmtEmbedder
	}
	return embed.NewCaching(e.resolveEmbedder())
}

// defaultEmbedder builds an embedder from GOKQL_EMBED, or the Ollama
// adapter when unset -- preserving today's zero-configuration
// behaviour exactly.
//
// Not cached: the Ollama adapter is stateless and the spec is read
// per call so a test can change the environment mid-run. A backend
// with real load cost (potion) should be constructed once by the host
// and assigned via SetEmbedder instead.
// defaultOllama is the zero-configuration backend, a SINGLETON.
//
// Identity matters here, not just behaviour: beginEmbedderScope
// detects "two different backends live at once" by comparing
// embedder identity, and a constructor returning a fresh instance per
// call made two unconfigured engines look like a conflict. The
// adapter is stateless and reads EmbedEndpoint/DefaultEmbedModel at
// call time, so one shared instance is exactly equivalent to many.
var defaultOllama = &ollamaEmbedder{}

// specEmbedders caches one embedder per GOKQL_EMBED spec, for the same
// identity reason. Keyed by spec so a test changing the environment
// mid-run still gets the right backend.
var (
	specEmbeddersMu sync.Mutex
	specEmbedders   = map[string]embed.Embedder{}
)

// defaultEmbedder builds an embedder from GOKQL_EMBED, or the Ollama
// adapter when unset -- preserving the zero-configuration behaviour
// this extension had before the seam existed.
func defaultEmbedder() embed.Embedder {
	spec := os.Getenv("GOKQL_EMBED")
	if spec == "" {
		return defaultOllama
	}

	specEmbeddersMu.Lock()
	defer specEmbeddersMu.Unlock()
	if em, ok := specEmbedders[spec]; ok {
		return em
	}

	em, err := EmbedderFromSpec(spec)
	if err != nil {
		// A malformed spec must not silently fall back to a different
		// backend and write vectors from the wrong model into a real
		// table. Cache an embedder that fails loudly on first use.
		em = &brokenEmbedder{spec: spec, err: err}
	}
	specEmbedders[spec] = em
	return em
}

// EmbedderFromSpec parses a backend spec string:
//
//	mock                      deterministic, 8 dims, no dependencies
//	mock:384                  deterministic, given width
//	ollama                    localhost:11434, DefaultEmbedModel
//	ollama:mxbai-embed-large  localhost:11434, named model
//	openai                    OpenAI-compatible server, llama-server defaults
//	openai:bge-m3             named model, default endpoint
//	openai:bge-m3@http://host:8080/v1/embeddings
//
// An API key is NEVER accepted here. Specs are logged, echoed in
// errors and visible in process arguments; the key comes from
// GOKQL_EMBED_API_KEY only.
func EmbedderFromSpec(spec string) (embed.Embedder, error) {
	kind, arg, _ := strings.Cut(spec, ":")
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "mock":
		dim := 8
		if arg != "" {
			n, err := strconv.Atoi(arg)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("mock embedder: bad dimension %q", arg)
			}
			dim = n
		}
		return embed.NewMock(dim), nil
	case "ollama":
		return &ollamaEmbedder{model: arg}, nil
	case "openai", "llama-server", "llamacpp":
		// model@endpoint, both optional.
		model, endpoint, _ := strings.Cut(arg, "@")
		if endpoint != "" && !strings.Contains(endpoint, "/") {
			return nil, fmt.Errorf("openai embedder: %q does not look like a URL "+
				"(expected model@http://host:port/v1/embeddings)", endpoint)
		}
		return embed.NewOpenAI(endpoint, model), nil
	default:
		return nil, fmt.Errorf("unknown embedding backend %q (want mock, ollama or openai)", kind)
	}
}

// brokenEmbedder defers a configuration error to first use, so a bad
// GOKQL_EMBED surfaces as a clear error on the query that needed it
// rather than as silently wrong vectors from a fallback backend.
type brokenEmbedder struct {
	spec string
	err  error
}

func (b *brokenEmbedder) Name() string   { return "invalid" }
func (b *brokenEmbedder) Dimension() int { return 0 }
func (b *brokenEmbedder) Embed(context.Context, string) ([]float64, error) {
	return nil, fmt.Errorf("GOKQL_EMBED=%q: %w", b.spec, b.err)
}
func (b *brokenEmbedder) EmbedBatch(context.Context, []string) ([][]float64, error) {
	return nil, fmt.Errorf("GOKQL_EMBED=%q: %w", b.spec, b.err)
}

// context returns the engine's cancellation context, or Background.
//
// A narrow, deliberate alternative to threading a context through
// evalExpr's 319 call sites. Every BULK path (.embed-into today,
// others later) has an *Engine receiver and can therefore be made
// interruptible now, which covers the case that actually matters: an
// ingest over a large corpus that previously could not be stopped at
// all. The per-row scalar path still cannot be cancelled; that needs
// the evaluation-context refactor.
func (e *Engine) context() context.Context {
	if e != nil && e.Ctx != nil {
		return e.Ctx
	}
	return context.Background()
}

// ResolveEmbedderForDiagnostics exposes the configured backend to the
// CLI's -embed-check, without opening the package-level resolution
// machinery to general use. Diagnostics are the one legitimate caller
// that needs the backend without executing a statement.
func ResolveEmbedderForDiagnostics(e *Engine) embed.Embedder {
	return e.resolveEmbedder()
}
