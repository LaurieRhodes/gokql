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
	"errors"
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

// activeEmbedder mirrors activeLetContext's existing pattern: scalar
// function evaluation (evalFunc and everything below it) has no Engine
// receiver, so an ambient package-level reference is the only way to
// reach per-engine state from inside embed_text() without threading an
// *Engine through evalExpr's entire call graph.
//
// This carries the same caveat activeLetContext already documents and
// that ExternalResolver's comment warns about: two Engine instances
// embedding concurrently would race here. That is not a new hazard
// introduced by this change -- today's code has the identical problem
// via the package-level EmbedEndpoint, only without a field to fix it
// from. The field on Engine is the durable answer; the ambient is the
// bridge until evalExpr takes a context.
// activeEmbedding is the process-wide, reference-counted embedding
// scope for the scalar embed_text() path.
//
// REFERENCE COUNTED, not save-and-restore. Two earlier versions of
// this were wrong, both found by running the suite rather than by
// reading it:
//
//  1. SetEmbedder published to the ambient directly, so one engine's
//     backend became every later engine's backend for the life of the
//     process. Three unrelated tests passed when they should have
//     failed.
//  2. Execute then saved the previous value on entry and restored it
//     on exit. That is the classic broken pattern under concurrency:
//     engine A captures nil, engine B installs, A finishes and
//     restores nil, and B -- still mid-statement -- silently falls
//     through to the default backend. It failed exactly that way with
//     eight goroutines sharing ONE embedder, which is the supported
//     configuration.
//
// Counting fixes the supported case properly: the scope is installed
// on the first entry and torn down only when the last holder leaves,
// so concurrent engines sharing a backend all see it for their whole
// statement. The unsupported case -- two DIFFERENT backends live at
// once -- is now a loud error instead of a silent wrong backend,
// because there is no correct answer available to a function that
// cannot see which engine called it.
//
// The durable fix remains an evaluation context threaded through
// evalExpr's 319 call sites, which would make this whole variable
// unnecessary along with activeLetContext.
type activeEmbedding struct {
	base  embed.Embedder // identity used to detect a conflicting backend
	em    embed.Embedder // the memo-wrapped embedder actually used
	depth int            // live holders; the scope ends when this hits 0
}

var (
	activeEmbedderMu sync.Mutex
	activeEmbedder   *activeEmbedding
)

// ErrConcurrentEmbedders reports two different embedding backends live
// in one process at the same time.
var ErrConcurrentEmbedders = errors.New(
	"two different embedding backends are active concurrently in this process; " +
		"embed_text() cannot tell which engine called it. Use one embedder per process, " +
		"or serialise the statements that call embed_text()")

// SetEmbedder sets the backend for this engine only. It deliberately
// does NOT publish to the ambient: see activeEmbedding above.
func (e *Engine) SetEmbedder(em embed.Embedder) { e.Embedder = em }

// beginEmbedderScope joins (or opens) the embedding scope for one
// statement and returns the leave function. The error is non-nil only
// for the genuinely ambiguous case described on
// ErrConcurrentEmbedders.
func (e *Engine) beginEmbedderScope() (func(), error) {
	base := e.resolveEmbedder()

	activeEmbedderMu.Lock()
	defer activeEmbedderMu.Unlock()

	if activeEmbedder == nil {
		activeEmbedder = &activeEmbedding{base: base, em: embed.NewCaching(base), depth: 1}
	} else {
		if activeEmbedder.base != base {
			return nil, ErrConcurrentEmbedders
		}
		activeEmbedder.depth++
	}

	return func() {
		activeEmbedderMu.Lock()
		defer activeEmbedderMu.Unlock()
		if activeEmbedder == nil {
			return
		}
		activeEmbedder.depth--
		if activeEmbedder.depth <= 0 {
			activeEmbedder = nil
		}
	}, nil
}

func getActiveEmbedder() embed.Embedder {
	activeEmbedderMu.Lock()
	defer activeEmbedderMu.Unlock()
	if activeEmbedder == nil {
		return nil
	}
	return activeEmbedder.em
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

// currentEmbedder is what the scalar embed_text() path calls. It
// prefers the ambient (set by Execute) and falls back to the default,
// so a direct evalExpr call in a test still works.
func currentEmbedder() embed.Embedder {
	if em := getActiveEmbedder(); em != nil {
		return em
	}
	// No statement-scoped embedder installed (a direct evalExpr call
	// in a test, say). Wrap the default so even this path does not
	// issue one request per row for a constant argument.
	return embed.NewCaching(defaultEmbedder())
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
