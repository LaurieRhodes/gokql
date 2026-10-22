package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// The shipped eval set is a source file like any other: if it stops
// parsing, or a case loses its expectations, the harness silently
// measures less than it claims to.
func TestShippedEvalSetIsWellFormed(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/embed-eval.json")
	if err != nil {
		t.Fatal(err)
	}
	var set EvalSet
	if err := json.Unmarshal(raw, &set); err != nil {
		t.Fatalf("eval set does not parse: %v", err)
	}
	if len(set.Cases) < 10 {
		t.Errorf("%d cases; too few to produce a meaningful recall figure", len(set.Cases))
	}
	seen := map[string]bool{}
	for i, c := range set.Cases {
		if strings.TrimSpace(c.Query) == "" {
			t.Errorf("case %d has an empty query", i)
		}
		if len(c.ExpectContains) == 0 {
			t.Errorf("case %d (%q) has no expectations, so it can never fail", i, c.Query)
		}
		for _, e := range c.ExpectContains {
			if strings.TrimSpace(e) == "" {
				t.Errorf("case %d has an empty expectation, which matches everything", i)
			}
		}
		if seen[c.Query] {
			t.Errorf("duplicate query %q double-weights one case in the average", c.Query)
		}
		seen[c.Query] = true
	}
}

func TestIsRelevantMatching(t *testing.T) {
	row := types.Row{"openai.go:c014", "The retry policy distinguishes TRANSIENT failures from permanent ones."}

	cases := []struct {
		name string
		c    EvalCase
		want bool
	}{
		{"substring hit, case-insensitive", EvalCase{ExpectContains: []string{"transient failures"}}, true},
		{"any-of semantics", EvalCase{ExpectContains: []string{"nope", "permanent ones"}}, true},
		{"no match", EvalCase{ExpectContains: []string{"chunking"}}, false},
		{"provenance gate passes", EvalCase{ExpectContains: []string{"retry"}, ExpectProvenance: "openai.go"}, true},
		{"provenance gate blocks", EvalCase{ExpectContains: []string{"retry"}, ExpectProvenance: "ollama.go"}, false},
		{"no expectations never matches", EvalCase{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRelevant(row, 1, 0, tc.c); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// A query containing quotes or backslashes must not break out of the
// KQL string literal it is interpolated into.
func TestKQLEscape(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`plain`, `plain`},
		{`say "hi"`, `say \"hi\"`},
		{`back\slash`, `back\\slash`},
		{"multi\nline", "multi line"},
		{`"); .drop table X //`, `\"); .drop table X //`},
	} {
		if got := kqlEscape(tc.in); got != tc.want {
			t.Errorf("kqlEscape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
