package embed

// cache.go -- memoisation for the scalar embed_text() path.
//
// KQL evaluates scalar functions once per row, by design. For every
// other function in this engine that is fine: they are pure, cheap and
// local. embed_text() is none of those, and the consequence is not
// subtle. The primary documented pattern,
//
//	Embeddings
//	| extend sim = series_cosine_similarity(Embedding, embed_text("my query"))
//	| top 10 by sim desc
//
// has a CONSTANT argument and was issuing one HTTP request per row:
// 10 rows, 10 calls; a 10,000-chunk corpus, 10,000 identical requests
// to embed one query string. Measured, not theorised.
//
// Caching by (model, text) collapses that to one call per distinct
// string per statement. It is not a workaround for a planner that
// should hoist loop-invariant scalars, and the comment in
// embed_bulk.go about that remains true. It is the part of the fix
// that does not require touching the evaluator.
//
// Lifetime is one statement execution, deliberately. A longer-lived
// cache would hold a query's vectors across a backend or model change
// and serve stale vectors from a space the table no longer uses.

import (
	"context"
	"errors"
	"sync"
)

// Caching wraps an Embedder with a per-statement memo. Safe for
// concurrent use: the engine's per-extent scan runs goroutines bounded
// by NumCPU and every one of them can reach a scalar function.
type Caching struct {
	inner Embedder
	memo  sync.Map // "model\x00text" -> *memoEntry
}

type memoEntry struct {
	once sync.Once
	vec  []float64
	err  error
}

// NewCaching wraps e. Wrapping an already-wrapped embedder returns it
// unchanged, so nested Execute calls share one memo rather than
// building a tower of caches.
func NewCaching(e Embedder) Embedder {
	if e == nil {
		return nil
	}
	if c, ok := e.(*Caching); ok {
		return c
	}
	return &Caching{inner: e}
}

// Unwrap exposes the underlying embedder, for callers that need the
// real backend's identity rather than the decorator's.
func (c *Caching) Unwrap() Embedder { return c.inner }

func (c *Caching) Name() string   { return c.inner.Name() }
func (c *Caching) Dimension() int { return c.inner.Dimension() }

func (c *Caching) Embed(ctx context.Context, text string) ([]float64, error) {
	key := c.inner.Name() + "\x00" + text
	v, _ := c.memo.LoadOrStore(key, &memoEntry{})
	entry := v.(*memoEntry)

	// sync.Once, not a plain check-then-set: without it, N scan
	// goroutines reaching the same constant simultaneously each issue
	// their own request, which is most of the bug this file exists to
	// fix, just with a smaller N.
	entry.once.Do(func() {
		entry.vec, entry.err = c.inner.Embed(ctx, text)
	})
	if entry.err != nil {
		// A failure is NOT cached as a permanent verdict beyond this
		// entry: the backend's own retry policy has already run, and
		// the caller gets the real error. But nor is it retried per
		// row, which would turn one unreachable server into N
		// timeouts.
		return nil, entry.err
	}
	// Defensive copy. The engine marshals this to JSON and callers
	// have no reason to expect a shared slice; one in-place mutation
	// would corrupt every subsequent row's vector.
	out := make([]float64, len(entry.vec))
	copy(out, entry.vec)
	return out, nil
}

// EmbedBatch deliberately does NOT consult the memo. The bulk path
// embeds distinct chunk text exactly once by construction, so a cache
// there buys nothing and costs memory proportional to the corpus.
func (c *Caching) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	return c.inner.EmbedBatch(ctx, texts)
}

// WithModel forwards the optional per-call model-switch capability
// through the decorator. Without this, wrapping an Ollama embedder in
// a cache would silently REMOVE its ability to switch models, and
// embed_text(x, "some-model") would start reporting that the backend
// cannot do something it can. The returned sibling gets its own memo;
// keys already carry the model name, but a separate instance keeps the
// two spaces visibly separate.
func (c *Caching) WithModel(model string) (Embedder, error) {
	sw, ok := c.inner.(ModelSwitcher)
	if !ok {
		return nil, ErrNoModelSwitch
	}
	inner, err := sw.WithModel(model)
	if err != nil {
		return nil, err
	}
	return NewCaching(inner), nil
}

// ErrNoModelSwitch is returned by WithModel on a backend whose model
// is fixed at construction.
var ErrNoModelSwitch = errors.New("backend cannot change model per call")

var _ ModelSwitcher = (*Caching)(nil)
