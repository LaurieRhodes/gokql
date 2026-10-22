package embed

import (
	"context"
	"strings"
	"testing"
)

// brokenBackend always fails, standing in for "nothing installed",
// which is the first experience every new user has.
type brokenBackend struct{}

func (brokenBackend) Name() string   { return "broken" }
func (brokenBackend) Dimension() int { return 0 }
func (brokenBackend) Embed(context.Context, string) ([]float64, error) {
	return nil, context.DeadlineExceeded
}
func (brokenBackend) EmbedBatch(context.Context, []string) ([][]float64, error) {
	return nil, context.DeadlineExceeded
}

func TestDiagnoseUnreachableGivesRemedy(t *testing.T) {
	r := Diagnose(context.Background(), brokenBackend{})
	if r.OK() {
		t.Fatal("an unreachable backend reported OK")
	}
	if len(r.Checks) != 1 {
		t.Errorf("got %d checks; later checks must be skipped, not reported as cascading failures", len(r.Checks))
	}
	out := r.String()
	for _, want := range []string{"-embed-setup", "GOKQL_EMBED", "ollama serve"} {
		if !strings.Contains(out, want) {
			t.Errorf("remedy does not mention %q:\n%s", want, out)
		}
	}
}

// TestDiagnoseMockFailsSemanticOrdering documents a property that
// looks like a bug and is not. Mock is a hashed bag-of-words with no
// semantics whatsoever, so related text does NOT outrank unrelated
// text and the pooling check correctly fails it. That is the whole
// point of running the check: it is the only thing in the stack that
// can tell a vector space from a pile of numbers of the right width.
//
// It also means -embed-check is not satisfiable by the test backend,
// which is correct. Mock exists so the suite needs no daemon, not so
// it can pass for a real one.
func TestDiagnoseMockFailsSemanticOrdering(t *testing.T) {
	r := Diagnose(context.Background(), NewMock(64))
	if r.Dimension != 64 {
		t.Errorf("dimension %d, want 64", r.Dimension)
	}
	byName := map[string]CheckStatus{}
	for _, c := range r.Checks {
		byName[c.Name] = c.Status
	}
	for _, n := range []string{"reachable", "deterministic", "batch consistency"} {
		if byName[n] != StatusPass {
			t.Errorf("%s = %s, want PASS", n, byName[n])
		}
	}
	if byName["semantic ordering"] == StatusPass {
		t.Error("a semantically empty backend passed the semantic ordering check; " +
			"the pooling guard is not doing anything")
	}
	if r.OK() {
		t.Error("Diagnose reported OK for a backend that carries no meaning")
	}
}

// constantBackend returns the same vector for every input: the exact
// signature of a wrong --pooling, where vectors are the right shape
// and carry no meaning. Nothing else in the stack can detect this.
type constantBackend struct{}

func (constantBackend) Name() string   { return "constant" }
func (constantBackend) Dimension() int { return 4 }
func (constantBackend) Embed(context.Context, string) ([]float64, error) {
	return []float64{1, 0, 0, 0}, nil
}
func (c constantBackend) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	return EmbedBatchVia(ctx, c, texts)
}

func TestDiagnoseCatchesMeaninglessVectors(t *testing.T) {
	r := Diagnose(context.Background(), constantBackend{})
	out := r.String()
	if !strings.Contains(out, "semantic ordering") {
		t.Fatalf("no semantic ordering check ran:\n%s", out)
	}
	if !strings.Contains(out, "pooling") {
		t.Errorf("a backend with no semantic separation did not point at --pooling:\n%s", out)
	}
}
