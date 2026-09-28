package engine

// embed_bulk.go — .embed-into T <| query: bulk embedding via Ollama's
// native batch input support (backlog P2 item 12).
//
// KQL's scalar-function evaluation model calls embed_text() once per
// row inside `project`, by design — the same as every other function,
// and not something worth special-casing in the shared evaluator for
// one function's sake. Ollama's /api/embed endpoint, checked directly,
// natively accepts an array of inputs and returns one embedding per
// input in order in a single HTTP round trip. This command is the
// isolated bridge between the two: it runs the query once, batches
// the resulting Text values through Ollama's real batch API (default
// 20 per call — conservative, matches the batch size that ran
// reliably during the 134-chunk pilot embedding run), and writes the
// result in one flushBatch. All-or-nothing, like every other ingest
// path in this codebase: any batch failure aborts before any row is
// written, not partway through.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/embed"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// EmbedBatchSize caps how many texts go in one Ollama request.
// Overridable for testing.
var EmbedBatchSize = 20

func (e *Engine) applyEmbedInto(cmd *parser.EmbedIntoCmd) (*types.Table, error) {
	src, err := e.executeQuery(cmd.Query)
	if err != nil {
		return nil, fmt.Errorf(".embed-into: evaluating query: %w", err)
	}
	if len(src.Rows) == 0 {
		return nil, fmt.Errorf(".embed-into: query returned no rows")
	}

	idIdx := src.Schema.ColumnIndex("Id")
	textIdx := src.Schema.ColumnIndex("Text")
	modelIdx := src.Schema.ColumnIndex("Model")
	provIdx := src.Schema.ColumnIndex("Provenance")
	if idIdx < 0 || textIdx < 0 {
		return nil, fmt.Errorf(".embed-into: query must project Id and Text columns (got: %v)",
			columnNames(&src.Schema))
	}

	defaultModel := DefaultEmbedModel
	texts := make([]string, len(src.Rows))
	for i, row := range src.Rows {
		if row[textIdx] == nil {
			return nil, fmt.Errorf(".embed-into: row %d (Id=%v) has a null Text value", i, row[idIdx])
		}
		texts[i] = fmt.Sprintf("%v", row[textIdx])
	}

	// Batch through the configured backend. All-or-nothing: collect
	// every embedding before writing anything, matching every other
	// ingest path here.
	//
	// Batching is the BACKEND's concern now, not this command's. For
	// Ollama it is still one HTTP call per EmbedBatchSize inputs via
	// the native array endpoint; for an in-process backend there is no
	// round trip to amortise and EmbedBatch is a loop. Either way this
	// command asks for n vectors and gets n vectors, in order.
	em := e.resolveEmbedder()

	// A per-row Model column still overrides the backend's own model,
	// where the backend can do that. Rows are grouped so a table
	// mixing models is still one call per contiguous run rather than
	// one per row.
	embeddings, err := embedAllRows(em, texts, src, modelIdx, defaultModel)
	if err != nil {
		return nil, fmt.Errorf(".embed-into: %w", err)
	}

	// Vector-space guard. Everything below this point is about the one
	// failure mode that ingest cannot recover from and queries cannot
	// see: mixing two vector spaces in one table. A length mismatch
	// already errors at query time in cosineSimilarity, but two
	// DIFFERENT models of the SAME width (nomic vs any other 768-wide
	// model; potion-base-32M vs potion-retrieval-32M) produce vectors
	// that compare happily and rank nonsense. Catching it here, before
	// anything is written, is the only place it is still cheap.
	if err := checkUniformWidth(embeddings); err != nil {
		return nil, fmt.Errorf(".embed-into: %w", err)
	}
	if err := e.checkVectorSpace(cmd.TableName, e.Catalog.GetTable(cmd.TableName), em, modelIdx, embeddings); err != nil {
		return nil, fmt.Errorf(".embed-into: %w", err)
	}

	// Any column in the source query beyond the four this command
	// understands (Id, Text, Model, Provenance) is passed through
	// verbatim into the output schema and every row — e.g. a Project
	// column, so a filter-before-rank semantic-recall query can filter
	// on it without a join back to the source table. Text itself is
	// never in the output; it's consumed as the embedding input only,
	// matching the existing behavior for the four fixed columns.
	var passthroughCols []types.Column
	for _, c := range src.Schema.Columns {
		switch c.Name {
		case "Id", "Text", "Model", "Provenance":
			continue
		default:
			passthroughCols = append(passthroughCols, c)
		}
	}

	outCols := []types.Column{{Name: "Id", Type: types.TypeString}}
	outCols = append(outCols, passthroughCols...)
	outCols = append(outCols,
		types.Column{Name: "Embedding", Type: types.TypeDynamic},
		types.Column{Name: "Model", Type: types.TypeString},
		types.Column{Name: "Provenance", Type: types.TypeString},
	)
	outSchema := types.Schema{Columns: outCols}

	tableDef := e.Catalog.GetTable(cmd.TableName)
	if tableDef == nil {
		if err := e.Catalog.CreateTable(cmd.TableName, outSchema); err != nil {
			return nil, err
		}
		if err := e.persistDiscoverySchema(cmd.TableName, outSchema); err != nil {
			return nil, err
		}
		tableDef = e.Catalog.GetTable(cmd.TableName)
	} else if err := schemaAppendCompatible(&tableDef.Schema, &outSchema); err != nil {
		return nil, fmt.Errorf(".embed-into %q: %w", cmd.TableName, err)
	}

	rows := make([]types.Row, len(src.Rows))
	for i, row := range src.Rows {
		// The Model column records WHICH MODEL PRODUCED THIS VECTOR,
		// not which model was requested and not a package default.
		// Two vectors of the same width from different models compare
		// without error and rank nonsense, so this value is the only
		// thing standing between a backend swap and silently wrong
		// recall. It comes from the backend that did the work.
		model := em.Name()
		if modelIdx >= 0 && row[modelIdx] != nil {
			model = fmt.Sprintf("%v", row[modelIdx])
		}
		prov := ""
		if provIdx >= 0 && row[provIdx] != nil {
			prov = fmt.Sprintf("%v", row[provIdx])
		}
		vecJSON, err := json.Marshal(embeddings[i])
		if err != nil {
			return nil, err
		}
		// Map onto the TARGET table's column order (may differ from
		// outSchema's if the table was pre-created), matching
		// .set-or-append's column-order independence. Any column
		// that isn't one of the four this command understands is a
		// passthrough column (e.g. Project) — copied straight from
		// the matching column in the SOURCE query's row, by name.
		out := make(types.Row, len(tableDef.Schema.Columns))
		for ti, tc := range tableDef.Schema.Columns {
			switch tc.Name {
			case "Id":
				out[ti] = fmt.Sprintf("%v", row[idIdx])
			case "Embedding":
				out[ti] = string(vecJSON)
			case "Model":
				out[ti] = model
			case "Provenance":
				out[ti] = prov
			default:
				if srcIdx := src.Schema.ColumnIndex(tc.Name); srcIdx >= 0 {
					out[ti] = row[srcIdx]
				}
			}
		}
		rows[i] = out
	}

	extID, err := e.flushBatch(cmd.TableName, tableDef, rows)
	if err != nil {
		return nil, fmt.Errorf(".embed-into: %w", err)
	}

	result := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "Result", Type: types.TypeString},
		{Name: "ExtentId", Type: types.TypeString},
		{Name: "RowsEmbedded", Type: types.TypeLong},
		{Name: "BatchCalls", Type: types.TypeLong},
	}})
	batchCalls := (len(texts) + EmbedBatchSize - 1) / EmbedBatchSize
	result.AddRow(types.Row{"OK", extID, int64(len(rows)), int64(batchCalls)})
	return result, nil
}

func columnNames(s *types.Schema) []string {
	names := make([]string, len(s.Columns))
	for i, c := range s.Columns {
		names[i] = c.Name
	}
	return names
}

// embedTextsBatch calls Ollama's /api/embed with an array input,
// retrying transient failures with the same policy as embedText.
func embedTextsBatch(texts []string, model string) ([][]float64, error) {
	reqBody, err := json.Marshal(map[string]interface{}{
		"model":      model,
		"input":      texts,
		"keep_alive": "10m",
		// num_batch (per-request options, NOT the OLLAMA_NUM_BATCH
		// server env var -- confirmed via direct testing before
		// relying on this, by a different model, Kimi, working
		// through a real production failure: the runner loads with
		// BatchSize:512 regardless of the server-side env var, only a
		// per-request options.num_batch override actually changes it).
		// Ollama's single-input batching doesn't chunk a long input at
		// all (ollama 0.13.1): any text whose token count exceeds the
		// runner's batch size panics outright ("caching disabled but
		// unable to fit entire input in a batch"), not a graceful
		// truncation or a clear application-level error -- confirmed
		// live, reproduced deterministically, bisected to ~512 tokens
		// (long single findings text exceeding that threshold killed
		// an ENTIRE .embed-into batch, since that command is
		// all-or-nothing by design). 4096 is a generous, verified-
		// working headroom over the found ~512-token failure
		// threshold, not a guess.
		"options": map[string]interface{}{"num_batch": 4096},
	})
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt <= embedRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(embedBackoff[(attempt-1)%len(embedBackoff)])
		}
		vecs, retryable, err := embedTextsBatchOnce(reqBody)
		if err == nil {
			return vecs, nil
		}
		lastErr = err
		if !retryable {
			return nil, err
		}
	}
	return nil, fmt.Errorf("embed batch: failed after %d attempts: %w", embedRetries+1, lastErr)
}

func embedTextsBatchOnce(reqBody []byte) (vecs [][]float64, retryable bool, err error) {
	resp, err := embedHTTPClient.Post(EmbedEndpoint, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, true, fmt.Errorf("could not reach Ollama at %s: %w", EmbedEndpoint, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}
	if resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("ollama batch embed failed (status %d): %s", resp.StatusCode, string(body))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("ollama batch embed failed (status %d): %s", resp.StatusCode, string(body))
	}
	var parsed struct {
		Embeddings [][]float64 `json:"embeddings"`
		Error      string      `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, false, fmt.Errorf("parsing ollama batch response: %w", err)
	}
	if parsed.Error != "" {
		return nil, false, fmt.Errorf("ollama error: %s", parsed.Error)
	}
	return parsed.Embeddings, false, nil
}

// embedAllRows produces one vector per text, honouring a per-row Model
// column where present and where the backend supports switching.
// Contiguous runs of the same model go through in a single EmbedBatch
// call, so the common case (no Model column at all, or one constant
// value) is exactly one batched pass.
func embedAllRows(em embed.Embedder, texts []string, src *types.Table,
	modelIdx int, defaultModel string) ([][]float64, error) {

	rowModel := func(i int) string {
		if modelIdx >= 0 && src.Rows[i][modelIdx] != nil {
			return fmt.Sprintf("%v", src.Rows[i][modelIdx])
		}
		return ""
	}

	out := make([][]float64, len(texts))
	for start := 0; start < len(texts); {
		want := rowModel(start)
		end := start + 1
		for end < len(texts) && rowModel(end) == want {
			end++
		}

		target := em
		if want != "" && want != defaultModel {
			sw, ok := em.(embed.ModelSwitcher)
			if !ok {
				return nil, fmt.Errorf("row %d requests model %q but backend %q cannot switch models per call",
					start, want, em.Name())
			}
			t, err := sw.WithModel(want)
			if err != nil {
				return nil, err
			}
			target = t
		}

		batch, err := target.EmbedBatch(context.Background(), texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("rows %d-%d: %w", start, end, err)
		}
		if len(batch) != end-start {
			return nil, fmt.Errorf("rows %d-%d: backend returned %d embeddings for %d inputs",
				start, end, len(batch), end-start)
		}
		copy(out[start:end], batch)
		start = end
	}
	return out, nil
}

// checkUniformWidth rejects a run whose own vectors disagree, which
// means the backend changed model underneath us mid-ingest.
func checkUniformWidth(vecs [][]float64) error {
	if len(vecs) == 0 {
		return nil
	}
	w := len(vecs[0])
	if w == 0 {
		return fmt.Errorf("backend returned an empty vector for row 0")
	}
	for i, v := range vecs {
		if len(v) != w {
			return fmt.Errorf("backend returned inconsistent vector widths within one run "+
				"(row 0 is %d wide, row %d is %d) -- the served model appears to have changed mid-ingest",
				w, i, len(v))
		}
	}
	return nil
}

// checkVectorSpace refuses to append vectors from one model to a table
// already holding another's. Reads a single existing row rather than
// scanning: identity is uniform per table by construction, so one row
// answers the question.
//
// Bypassable on purpose by projecting an explicit Model column, since
// a caller who has said which model each row uses has made the claim
// deliberately and owns it.
func (e *Engine) checkVectorSpace(name string, tableDef *catalog.Table, em embed.Embedder,
	modelIdx int, vecs [][]float64) error {

	if len(vecs) == 0 || tableDef == nil || len(tableDef.Extents) == 0 {
		return nil
	}
	if tableDef.Schema.ColumnIndex("Model") < 0 || tableDef.Schema.ColumnIndex("Embedding") < 0 {
		return nil
	}

	existing, err := e.ScanExtent(name, tableDef.Extents[0].FilePath, &tableDef.Schema,
		[]string{"Model", "Embedding"}, nil, 1)
	if err != nil || existing == nil || len(existing.Rows) == 0 {
		// A table we cannot read a sample from is not a reason to
		// block an ingest; the read path will surface the real problem.
		return nil
	}

	mIdx := existing.Schema.ColumnIndex("Model")
	eIdx := existing.Schema.ColumnIndex("Embedding")
	if mIdx < 0 || eIdx < 0 {
		return nil
	}

	// The MODEL-NAME check is skipped when the query projects its own
	// Model column: a caller who states the model per row has made
	// that claim deliberately and owns it.
	//
	// The WIDTH check below is NOT skipped. An earlier version of this
	// guard returned early on any explicit Model column and so skipped
	// both, which meant projecting a Model column disabled the one
	// check that is never a judgement call: vectors of two different
	// widths cannot belong to one table under any labelling, and the
	// caller cannot have meant it.
	if modelIdx < 0 && existing.Rows[0][mIdx] != nil {
		was := fmt.Sprintf("%v", existing.Rows[0][mIdx])
		now := em.Name()
		if was != "" && was != now {
			return fmt.Errorf("table %q already holds vectors from model %q but this run produced %q; "+
				"appending would mix two vector spaces that rank against each other without error. "+
				"Re-embed the table, or write to a new one",
				name, was, now)
		}
	}

	if existing.Rows[0][eIdx] != nil {
		if prev, ok := parseFloatArray(existing.Rows[0][eIdx]); ok && len(prev) > 0 {
			if len(prev) != len(vecs[0]) {
				return fmt.Errorf("table %q holds %d-wide vectors but this run produced %d-wide; "+
					"re-embed the table, or write to a new one", name, len(prev), len(vecs[0]))
			}
		}
	}
	return nil
}
