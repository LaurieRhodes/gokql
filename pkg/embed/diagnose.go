package embed

// diagnose.go -- "is my embedding backend actually going to work?"
// answered before a user finds out the hard way, mid-ingest.
//
// The failure modes here are not evenly distributed. Two of them
// (unreachable server, embeddings not enabled) are loud and obvious.
// The third -- wrong pooling -- returns vectors of the correct shape
// with the wrong meaning, produces no error anywhere in the stack, and
// is only detectable by checking that related text outranks unrelated
// text. That check is therefore part of the diagnostic, not an
// optional extra.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// CheckStatus is the outcome of one probe.
type CheckStatus string

const (
	StatusPass CheckStatus = "PASS"
	StatusWarn CheckStatus = "WARN"
	StatusFail CheckStatus = "FAIL"
)

// Check is one diagnostic result. Remedy is what the user should
// actually type, not a restatement of the problem.
type Check struct {
	Name    string
	Status  CheckStatus
	Detail  string
	Remedy  string
	Elapsed time.Duration
}

// Report is the full diagnosis.
type Report struct {
	Backend   string
	Dimension int
	Checks    []Check
}

// OK reports whether every check passed.
func (r *Report) OK() bool {
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			return false
		}
	}
	return true
}

// String renders the report for a terminal.
func (r *Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Embedding backend: %s\n", r.Backend)
	if r.Dimension > 0 {
		fmt.Fprintf(&b, "Vector dimension:  %d\n", r.Dimension)
	}
	b.WriteString("\n")
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "  [%s] %-22s %s\n", c.Status, c.Name, c.Detail)
		if c.Remedy != "" {
			for _, line := range strings.Split(c.Remedy, "\n") {
				fmt.Fprintf(&b, "         %s\n", line)
			}
		}
	}
	return b.String()
}

// Diagnose probes a backend. Cheap: four short texts, one batch call
// and two single calls. Safe to run against a production endpoint.
func Diagnose(ctx context.Context, e Embedder) *Report {
	r := &Report{Backend: e.Name()}
	if r.Backend == "" {
		r.Backend = fmt.Sprintf("%T", e)
	}

	// 1. Reachability. Everything else is meaningless without it, so
	// the remaining checks are skipped rather than reported as
	// cascading failures.
	t0 := time.Now()
	probe, err := e.Embed(ctx, "connectivity probe")
	if err != nil {
		r.Checks = append(r.Checks, Check{
			Name:    "reachable",
			Status:  StatusFail,
			Detail:  err.Error(),
			Remedy:  setupRemedy,
			Elapsed: time.Since(t0),
		})
		return r
	}
	r.Dimension = len(probe)
	r.Checks = append(r.Checks, Check{
		Name: "reachable", Status: StatusPass,
		Detail: fmt.Sprintf("responded in %s, %d dimensions", time.Since(t0).Round(time.Millisecond), len(probe)),
	})

	// 2. Determinism. If identical input gives different vectors,
	// every stored vector is a snapshot rather than a value and
	// re-embedding silently reshuffles rankings.
	if again, err := e.Embed(ctx, "connectivity probe"); err == nil {
		drift := maxAbsDiff(probe, again)
		st, detail := StatusPass, "identical input gives identical vectors"
		if drift > 1e-6 {
			st = StatusWarn
			detail = fmt.Sprintf("identical input drifted by %.3g; stored vectors are snapshots, not values", drift)
		}
		r.Checks = append(r.Checks, Check{Name: "deterministic", Status: st, Detail: detail})
	}

	// 3. Batch/single agreement. A server that pools a batched input
	// differently from a single one makes .embed-into (batched) and
	// embed_text() (single) produce incomparable vectors, so bulk
	// ingest and query-time embedding quietly stop matching.
	if batch, err := e.EmbedBatch(ctx, []string{"connectivity probe", "second input"}); err == nil && len(batch) == 2 {
		sim := cosine(probe, batch[0])
		st, detail := StatusPass, "batched and single vectors agree"
		if sim < 0.9999 {
			st = StatusFail
			detail = fmt.Sprintf("batched vs single cosine %.5f; .embed-into and embed_text() would not be comparable", sim)
		}
		r.Checks = append(r.Checks, Check{Name: "batch consistency", Status: st, Detail: detail})
	}

	// 4. Semantic ordering. The pooling check. Nothing else in the
	// stack can detect a wrong --pooling: the vectors are the right
	// width, they store, they rank, and the ranking is meaningless.
	vecs, err := e.EmbedBatch(ctx, []string{
		"how do I filter rows in a KQL query",
		"use the where operator to filter rows",
		"recipe for slow roasted lamb shoulder",
	})
	if err == nil && len(vecs) == 3 {
		related, unrelated := cosine(vecs[0], vecs[1]), cosine(vecs[0], vecs[2])
		switch {
		case related <= unrelated:
			r.Checks = append(r.Checks, Check{
				Name:   "semantic ordering",
				Status: StatusFail,
				Detail: fmt.Sprintf("related %.3f, unrelated %.3f -- vectors are the right shape and the wrong meaning", related, unrelated),
				Remedy: poolingRemedy,
			})
		case related-unrelated < 0.05:
			r.Checks = append(r.Checks, Check{
				Name:   "semantic ordering",
				Status: StatusWarn,
				Detail: fmt.Sprintf("related %.3f, unrelated %.3f -- separation is weak; check --pooling", related, unrelated),
				Remedy: poolingRemedy,
			})
		default:
			r.Checks = append(r.Checks, Check{
				Name:   "semantic ordering",
				Status: StatusPass,
				Detail: fmt.Sprintf("related %.3f, unrelated %.3f", related, unrelated),
			})
		}
	}

	return r
}

// NotReadyHint is appended to a connect failure anywhere in this
// package, so a user who hits it mid-query gets the next command to
// type rather than a bare dial error.
const NotReadyHint = "hint: run 'gokql -embed-check' to diagnose, or 'gokql -embed-setup' to write a setup script"

const setupRemedy = `No embedding backend is answering. Either:
  - start one:   gokql -embed-setup     (writes a setup script; read it before running)
  - point at one: GOKQL_EMBED="openai:MODEL@http://host:port/v1/embeddings"
  - or use Ollama: ollama serve && ollama pull nomic-embed-text`

const poolingRemedy = `--pooling is almost certainly wrong. It is NOT optional:
unspecified means the server uses whatever the GGUF's converter baked in.
Set it to match how the model was trained (mean for nomic/e5, cls for bge).`

func maxAbsDiff(a, b []float64) float64 {
	if len(a) != len(b) {
		return math.Inf(1)
	}
	var m float64
	for i := range a {
		if d := math.Abs(a[i] - b[i]); d > m {
			m = d
		}
	}
	return m
}

func cosine(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
