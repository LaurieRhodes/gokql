//go:build live

package embed

// live_test.go -- validation against a REAL embedding server.
//
// Everything in openai_test.go uses a fake server written alongside
// the client, so it proves the client is self-consistent, not that it
// is correct. This file is the only thing that proves the latter, and
// nothing about the embedding extension should be trusted in anger
// until it has passed.
//
// Run:
//
//	llama-server -m <model>.gguf --embeddings --pooling <explicit> -c <trained-ctx>
//	GOKQL_LIVE_ENDPOINT=http://localhost:8080/v1/embeddings \
//	GOKQL_LIVE_MODEL=<name> \
//	go test -tags live ./pkg/embed/ -v
//
// Behind a build tag so a normal `go test ./...` on a machine with no
// server stays green. A test that is skipped by default is a test
// nobody runs; a test that is tagged is one you run deliberately.

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"
)

func liveClient(t *testing.T) *OpenAI {
	t.Helper()
	ep := os.Getenv("GOKQL_LIVE_ENDPOINT")
	if ep == "" {
		t.Fatal("set GOKQL_LIVE_ENDPOINT (e.g. http://localhost:8080/v1/embeddings)")
	}
	return NewOpenAI(ep, os.Getenv("GOKQL_LIVE_MODEL"))
}

// TestLiveServerAnswers is the smoke test: is anything there, and is
// it configured for embeddings at all.
func TestLiveServerAnswers(t *testing.T) {
	c := liveClient(t)
	v, err := c.Embed(context.Background(), "the quick brown fox")
	if err != nil {
		t.Fatalf("%v", err)
	}
	t.Logf("model=%s dimension=%d", c.Name(), len(v))
	if len(v) == 0 {
		t.Fatal("empty vector")
	}
}

// TestLiveSemanticOrdering is the one that catches a wrong --pooling.
//
// A misconfigured pooling type returns vectors of the correct SHAPE
// with the wrong MEANING, which no shape check can see and which the
// engine will happily store and rank. Related text must score higher
// than unrelated text; if this fails, the flag is wrong, not the
// client.
func TestLiveSemanticOrdering(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()

	vecs, err := c.EmbedBatch(ctx, []string{
		"how do I filter rows in a KQL query",   // 0: query
		"use the where operator to filter rows", // 1: related
		"recipe for slow roasted lamb shoulder", // 2: unrelated
	})
	if err != nil {
		t.Fatal(err)
	}

	related, err := cosine(vecs[0], vecs[1])
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := cosine(vecs[0], vecs[2])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("related=%.4f unrelated=%.4f", related, unrelated)

	if related <= unrelated {
		t.Fatalf("related text scored %.4f, unrelated scored %.4f. "+
			"The vectors are the right shape and the wrong meaning: check --pooling "+
			"(the GGUF default is whatever the converter set, not necessarily how the "+
			"model was trained)", related, unrelated)
	}
	if related-unrelated < 0.05 {
		t.Errorf("separation is only %.4f; usable but suspiciously weak for this pair",
			related-unrelated)
	}
}

// TestLiveDeterminism guards against a server configured with
// nondeterministic behaviour. Two identical inputs must produce
// identical vectors, or every stored vector is a snapshot rather than
// a value and re-embedding will silently reshuffle rankings.
func TestLiveDeterminism(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	a, err := c.Embed(ctx, "determinism check")
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Embed(ctx, "determinism check")
	if err != nil {
		t.Fatal(err)
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-9 {
			t.Fatalf("identical input produced different vectors at %d: %v vs %v", i, a[i], b[i])
		}
	}
}

// TestLiveBatchMatchesSingle is the correctness check that a fake
// server cannot perform: a real server may pad, truncate or pool a
// batched input differently from a single one. If these disagree,
// .embed-into (batched) and embed_text() (single) produce different
// vectors for the same text, and query-time recall silently degrades.
func TestLiveBatchMatchesSingle(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()

	texts := []string{"alpha one", "beta two three", "gamma four five six"}
	batched, err := c.EmbedBatch(ctx, texts)
	if err != nil {
		t.Fatal(err)
	}
	for i, txt := range texts {
		single, err := c.Embed(ctx, txt)
		if err != nil {
			t.Fatal(err)
		}
		sim, err := cosine(batched[i], single)
		if err != nil {
			t.Fatal(err)
		}
		if sim < 0.9999 {
			t.Errorf("text %d: batched and single vectors differ (cosine %.6f). "+
				"Bulk ingest and query-time embedding would not be comparable", i, sim)
		}
	}
}

// TestLiveLongInputDoesNotSilentlyTruncate checks the failure the
// Ollama path hit in production: an input over the runner's limit
// killed the request rather than erroring cleanly. A server with -c
// set below the model's trained length truncates silently instead,
// which is quieter and worse.
func TestLiveLongInputDoesNotSilentlyTruncate(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()

	base := "the operator filters rows by predicate. "
	short := base
	long := ""
	for i := 0; i < 200; i++ {
		long += base
	}

	vShort, err := c.Embed(ctx, short)
	if err != nil {
		t.Fatalf("short input: %v", err)
	}
	vLong, err := c.Embed(ctx, long+" and then discusses an entirely different subject: marine biology")
	if err != nil {
		t.Logf("long input errored (acceptable if it is a CLEAR error): %v", err)
		return
	}
	sim, err := cosine(vShort, vLong)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("short vs long-with-different-tail cosine = %.4f", sim)
	if sim > 0.999 {
		t.Errorf("a much longer input with a different tail produced an almost identical "+
			"vector (%.4f): the server is probably truncating silently. Check -c against "+
			"the model's trained context length", sim)
	}
}

var errLen = errors.New("vector length mismatch")

func cosine(a, b []float64) (float64, error) {
	if len(a) != len(b) {
		return 0, errLen
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
