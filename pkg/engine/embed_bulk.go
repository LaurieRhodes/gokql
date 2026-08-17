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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

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

	// Batch through Ollama. All-or-nothing: collect every embedding
	// before writing anything, matching every other ingest path here.
	embeddings := make([][]float64, len(texts))
	for start := 0; start < len(texts); start += EmbedBatchSize {
		end := start + EmbedBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		model := defaultModel
		if modelIdx >= 0 && src.Rows[start][modelIdx] != nil {
			model = fmt.Sprintf("%v", src.Rows[start][modelIdx])
		}
		batch, err := embedTextsBatch(texts[start:end], model)
		if err != nil {
			return nil, fmt.Errorf(".embed-into: batch %d-%d: %w", start, end, err)
		}
		if len(batch) != end-start {
			return nil, fmt.Errorf(".embed-into: batch %d-%d: ollama returned %d embeddings for %d inputs",
				start, end, len(batch), end-start)
		}
		copy(embeddings[start:end], batch)
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
		model := defaultModel
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
