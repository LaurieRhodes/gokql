package main

// embed_eval.go -- measure retrieval quality, so a change that makes
// search worse is visible instead of silent.
//
// Every other check in this codebase catches a mechanical failure: an
// unreachable server, a width mismatch, a mixed vector space. None of
// them can tell you whether the thing actually FINDS the right chunk.
// A different model, a different --pooling, a different chunk size or
// a different backend all produce vectors that store and rank without
// error and may retrieve worse. There is no error to catch. The only
// defence is measuring recall against cases a human labelled once.
//
// Relevance is matched on TEXT SUBSTRINGS, not chunk Ids. Ids shift
// whenever a document is edited or re-chunked, so an Id-keyed eval set
// rots on the first doc change and then gets deleted for being noisy.
// A substring that must appear in the retrieved chunk survives all of
// that, and reads as an assertion about meaning rather than about
// storage layout.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/engine"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// EvalSet is the on-disk format. Deliberately plain JSON a human
// maintains by hand: an eval set nobody edits is an eval set nobody
// trusts.
type EvalSet struct {
	// QueryTemplate is the KQL run per case. {{QUERY}} is replaced
	// with the case's query text (KQL-escaped) and {{K}} with the
	// cutoff. It must project Id and Text.
	QueryTemplate string     `json:"query_template"`
	K             int        `json:"k"`
	Cases         []EvalCase `json:"cases"`
}

// EvalCase is one labelled query.
type EvalCase struct {
	Query string `json:"query"`

	// ExpectContains: the retrieved chunk is relevant if its Text
	// contains ANY of these (case-insensitive). Several alternatives
	// are normal -- a question often has more than one right answer in
	// a corpus, and demanding one exact chunk measures luck.
	ExpectContains []string `json:"expect_contains"`

	// ExpectProvenance, when set, additionally requires the chunk to
	// come from this source. Use it when the same phrase appears in
	// several files.
	ExpectProvenance string `json:"expect_provenance,omitempty"`

	Note string `json:"note,omitempty"`
}

const defaultQueryTemplate = `{{TABLE}}
| extend __sim = series_cosine_similarity(Embedding, embed_text("{{QUERY}}"))
| top {{K}} by __sim desc
| join kind=inner ({{CHUNKS}}) on Id
| project Id, Text`

type caseResult struct {
	Case EvalCase
	Rank int // 1-based rank of the first relevant hit; 0 = not found
	Top  string
	Err  error
}

// runEmbedEval executes the eval set and reports recall@1/@3/@K and
// MRR. Exit code is non-zero when recall@K is below the threshold, so
// it can gate a model or backend change in CI.
func runEmbedEval(eng *engine.Engine, path, table, chunks string, threshold float64) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading eval set: %v\n", err)
		return 2
	}
	var set EvalSet
	if err := json.Unmarshal(raw, &set); err != nil {
		fmt.Fprintf(os.Stderr, "parsing eval set: %v\n", err)
		return 2
	}
	if len(set.Cases) == 0 {
		fmt.Fprintln(os.Stderr, "eval set has no cases")
		return 2
	}
	if set.K <= 0 {
		set.K = 5
	}
	tmpl := set.QueryTemplate
	if tmpl == "" {
		tmpl = defaultQueryTemplate
	}

	results := make([]caseResult, 0, len(set.Cases))
	for _, c := range set.Cases {
		results = append(results, evalOne(eng, tmpl, table, chunks, set.K, c))
	}
	return reportEval(results, set.K, threshold)
}

func evalOne(eng *engine.Engine, tmpl, table, chunks string, k int, c EvalCase) caseResult {
	q := strings.NewReplacer(
		"{{TABLE}}", table,
		"{{CHUNKS}}", chunks,
		"{{K}}", fmt.Sprint(k),
		"{{QUERY}}", kqlEscape(c.Query),
	).Replace(tmpl)

	stmt, err := parser.Parse(q)
	if err != nil {
		return caseResult{Case: c, Err: fmt.Errorf("parse: %w", err)}
	}
	tbl, err := eng.Execute(stmt)
	if err != nil {
		return caseResult{Case: c, Err: err}
	}
	if tbl == nil {
		return caseResult{Case: c, Err: fmt.Errorf("query returned no table")}
	}

	textIdx := tbl.Schema.ColumnIndex("Text")
	idIdx := tbl.Schema.ColumnIndex("Id")
	if textIdx < 0 {
		return caseResult{Case: c, Err: fmt.Errorf("query must project a Text column")}
	}

	res := caseResult{Case: c}
	for i, row := range tbl.Rows {
		if i == 0 && idIdx >= 0 && row[idIdx] != nil {
			res.Top = fmt.Sprintf("%v", row[idIdx])
		}
		if isRelevant(row, textIdx, idIdx, c) {
			res.Rank = i + 1
			break
		}
	}
	return res
}

func isRelevant(row types.Row, textIdx, idIdx int, c EvalCase) bool {
	if row[textIdx] == nil {
		return false
	}
	text := strings.ToLower(fmt.Sprintf("%v", row[textIdx]))

	if c.ExpectProvenance != "" && idIdx >= 0 {
		id := strings.ToLower(fmt.Sprintf("%v", row[idIdx]))
		if !strings.Contains(id, strings.ToLower(c.ExpectProvenance)) {
			return false
		}
	}
	for _, want := range c.ExpectContains {
		if strings.Contains(text, strings.ToLower(want)) {
			return true
		}
	}
	return false
}

func reportEval(results []caseResult, k int, threshold float64) int {
	var at1, at3, atK, errs int
	var mrr float64
	for _, r := range results {
		if r.Err != nil {
			errs++
			continue
		}
		switch {
		case r.Rank == 0:
		default:
			mrr += 1 / float64(r.Rank)
			if r.Rank <= 1 {
				at1++
			}
			if r.Rank <= 3 {
				at3++
			}
			if r.Rank <= k {
				atK++
			}
		}
	}
	n := float64(len(results))

	// Failures first: a report you have to scroll through is a report
	// nobody reads past the summary.
	var misses []caseResult
	for _, r := range results {
		if r.Err != nil || r.Rank == 0 {
			misses = append(misses, r)
		}
	}
	sort.SliceStable(misses, func(i, j int) bool { return misses[i].Err != nil && misses[j].Err == nil })

	if len(misses) > 0 {
		fmt.Printf("Misses (%d of %d):\n\n", len(misses), len(results))
		for _, r := range misses {
			fmt.Printf("  %q\n", r.Case.Query)
			if r.Err != nil {
				fmt.Printf("      ERROR: %v\n", r.Err)
			} else {
				fmt.Printf("      expected text containing: %s\n", strings.Join(r.Case.ExpectContains, " | "))
				if r.Top != "" {
					fmt.Printf("      top hit was: %s\n", r.Top)
				}
			}
			if r.Case.Note != "" {
				fmt.Printf("      note: %s\n", r.Case.Note)
			}
			fmt.Println()
		}
	}

	fmt.Printf("Cases:    %d", len(results))
	if errs > 0 {
		fmt.Printf("  (%d errored)", errs)
	}
	fmt.Printf("\nrecall@1: %.3f  (%d)\nrecall@3: %.3f  (%d)\nrecall@%d: %.3f  (%d)\nMRR:      %.3f\n",
		float64(at1)/n, at1, float64(at3)/n, at3, k, float64(atK)/n, atK, mrr/n)

	got := float64(atK) / n
	if got < threshold {
		fmt.Fprintf(os.Stderr, "\nFAIL: recall@%d %.3f is below the threshold %.3f\n", k, got, threshold)
		return 1
	}
	fmt.Printf("\nPASS: recall@%d %.3f meets the threshold %.3f\n", k, got, threshold)
	return 0
}

// kqlEscape makes a Go string safe inside a KQL double-quoted literal.
func kqlEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ", "\r", " ").Replace(s)
}
