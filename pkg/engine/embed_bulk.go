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
	"context"
	"fmt"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/embed"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// EmbedBatchSize caps how many texts go in one Ollama request.
// Overridable for testing.
var EmbedBatchSize = 20

// EmbedCommitRows is how many rows are embedded and written as one
// durable unit. Smaller means more extents and more resumability;
// larger means fewer, bigger extents.
//
// This is what makes .embed-into resumable. It used to embed the
// ENTIRE query result before writing anything: a failure at row
// 99,000 of 100,000 discarded 99,000 embeddings and there was no way
// to interrupt it either. All-or-nothing is still the rule WITHIN a
// commit chunk, matching every other ingest path here; the change is
// that the unit is now a chunk rather than the whole corpus.
var EmbedCommitRows = 500

func (e *Engine) applyEmbedInto(cmd *parser.EmbedIntoCmd) (*types.Table, error) {
	ctx := e.context()

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

	for i, row := range src.Rows {
		if row[textIdx] == nil {
			return nil, fmt.Errorf(".embed-into: row %d (Id=%v) has a null Text value", i, row[idIdx])
		}
	}

	// Ids must be unique within the source. Found live, embedding this
	// repo's own docs: .chunk-file numbers chunks c001..cNNN PER FILE,
	// so six files produced 281 chunks with only 123 distinct Ids.
	//
	// Duplicate Ids break two things badly and silently. Semantic
	// recall joins the embeddings table back to the source on Id, and
	// a duplicated key turns that join into a partial cross product:
	// a "top 3" came back as three copies of one chunk's score
	// attached to unrelated text. Worse, resumability skips rows whose
	// Id is already present, so re-running after a partial failure
	// would skip genuinely DIFFERENT chunks that merely share an Id,
	// losing them permanently with no error.
	//
	// Refusing up front is the only safe answer: neither problem is
	// visible at query time, and both are unrecoverable once written.
	if dup, n := firstDuplicateId(src, idIdx); dup != "" {
		return nil, fmt.Errorf(".embed-into: source has duplicate Id values "+
			"(%q appears %d times; %d rows, %d distinct Ids). Ids must be unique: "+
			"the embeddings table is joined back on Id, and resuming a partial run "+
			"skips Ids already present. If these came from .chunk-file, note it numbers "+
			"chunks per FILE, so Ids collide across files -- make them unique in the "+
			"query, e.g. | extend Id = strcat(Provenance, \":\", Id)",
			dup, n, len(src.Rows), distinctIdCount(src, idIdx))
	}

	em := e.resolveEmbedder()

	// Resolve the target BEFORE embedding anything. The old order
	// embedded first and resolved the table afterwards, which meant a
	// schema conflict was discovered only after paying for every
	// vector -- and made it impossible to ask the table which rows it
	// already had.
	// Validate the target's schema up front, but do NOT create a
	// missing table yet. TestEmbedIntoAllOrNothing pins the rule that
	// a failed embed leaves no table behind, and resolving early (to
	// read existing Ids) would otherwise have created an empty one
	// before the first vector was fetched. Creation is deferred to
	// the first chunk that actually has rows to write.
	outSchema := embedOutputSchema(src)
	tableDef := e.Catalog.GetTable(cmd.TableName)
	if tableDef != nil {
		if err := schemaAppendCompatible(&tableDef.Schema, &outSchema); err != nil {
			return nil, fmt.Errorf(".embed-into %q: %w", cmd.TableName, err)
		}
	}

	// Model identity is checked BEFORE the skip logic, and
	// independently of it. Resumability introduced a nastier failure
	// than it fixed: with every Id already present, changing the model
	// and re-running skipped every row, wrote nothing, reported OK and
	// silently left the OLD vectors in place. A no-op that looks like
	// a success is worse than an error, so the table's existing model
	// is compared before anything else happens.
	if err := e.checkModelIdentity(cmd.TableName, tableDef, em, modelIdx); err != nil {
		return nil, fmt.Errorf(".embed-into: %w", err)
	}

	// Skip rows already embedded. This is the other half of
	// resumability: after a partial run, re-issuing the SAME command
	// costs only the rows that are missing, and running it twice is a
	// no-op rather than a duplicate.
	done, err := e.existingEmbedIds(cmd.TableName, tableDef)
	if err != nil {
		return nil, fmt.Errorf(".embed-into: reading existing ids: %w", err)
	}
	pending := make([]int, 0, len(src.Rows))
	for i, row := range src.Rows {
		if !done[fmt.Sprintf("%v", row[idIdx])] {
			pending = append(pending, i)
		}
	}
	skipped := len(src.Rows) - len(pending)
	if len(pending) == 0 {
		return embedResultTable("OK (nothing to do)", "", 0, int64(skipped), 0), nil
	}

	commit := EmbedCommitRows
	if commit <= 0 {
		commit = len(pending)
	}

	var (
		lastExtent string
		written    int64
		calls      int64
	)
	for start := 0; start < len(pending); start += commit {
		if err := ctx.Err(); err != nil {
			// Partial progress is durable by design: report what
			// landed rather than pretending the whole thing failed.
			return nil, fmt.Errorf(".embed-into: cancelled after %d of %d rows "+
				"(already-written rows are durable; re-run to resume): %w",
				written, len(pending), err)
		}
		end := start + commit
		if end > len(pending) {
			end = len(pending)
		}
		chunk := pending[start:end]

		texts := make([]string, len(chunk))
		for i, ri := range chunk {
			texts[i] = fmt.Sprintf("%v", src.Rows[ri][textIdx])
		}

		vecs, err := embedAllRows(ctx, em, texts, src, chunk, modelIdx)
		if err != nil {
			return nil, fmt.Errorf(".embed-into: rows %d-%d of %d (%d already written and durable; "+
				"re-run to resume): %w", start, end, len(pending), written, err)
		}
		calls += int64((len(texts) + EmbedBatchSize - 1) / EmbedBatchSize)

		if err := checkUniformWidth(vecs); err != nil {
			return nil, fmt.Errorf(".embed-into: %w", err)
		}
		if err := e.checkVectorWidth(cmd.TableName, e.Catalog.GetTable(cmd.TableName), vecs); err != nil {
			return nil, fmt.Errorf(".embed-into: %w", err)
		}

		if tableDef == nil {
			// First chunk with vectors in hand: now it is safe to
			// create the table.
			if err := e.Catalog.CreateTable(cmd.TableName, outSchema); err != nil {
				return nil, err
			}
			if err := e.persistDiscoverySchema(cmd.TableName, outSchema); err != nil {
				return nil, err
			}
			tableDef = e.Catalog.GetTable(cmd.TableName)
		}

		rows, err := buildEmbedRows(src, chunk, vecs, tableDef, em, idIdx, modelIdx, provIdx)
		if err != nil {
			return nil, fmt.Errorf(".embed-into: %w", err)
		}

		extID, err := e.flushBatch(cmd.TableName, tableDef, rows)
		if err != nil {
			return nil, fmt.Errorf(".embed-into: writing rows %d-%d (%d already written and durable): %w",
				start, end, written, err)
		}
		lastExtent = extID
		written += int64(len(rows))
	}

	return embedResultTable("OK", lastExtent, written, int64(skipped), calls), nil
}

func embedResultTable(result, extentID string, written, skipped, calls int64) *types.Table {
	t := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "Result", Type: types.TypeString},
		{Name: "ExtentId", Type: types.TypeString},
		{Name: "RowsEmbedded", Type: types.TypeLong},
		{Name: "RowsSkipped", Type: types.TypeLong},
		{Name: "BatchCalls", Type: types.TypeLong},
	}})
	t.AddRow(types.Row{result, extentID, written, skipped, calls})
	return t
}

// embedOutputSchema derives the destination schema from the source
// query. Pure: no catalog side effects, so a caller can validate
// compatibility without creating anything.
func embedOutputSchema(src *types.Table) types.Schema {
	// Any column in the source query beyond the four this command
	// understands (Id, Text, Model, Provenance) is passed through
	// verbatim into the output schema and every row -- e.g. a Project
	// column, so a filter-before-rank semantic-recall query can filter
	// on it without a join back to the source table. Text itself is
	// never in the output; it's consumed as the embedding input only.
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
	return types.Schema{Columns: outCols}
}

// existingEmbedIds returns the Ids already present in the target,
// scanning the Id column only.
func (e *Engine) existingEmbedIds(name string, tableDef *catalog.Table) (map[string]bool, error) {
	done := map[string]bool{}
	if tableDef == nil || len(tableDef.Extents) == 0 {
		return done, nil
	}
	if tableDef.Schema.ColumnIndex("Id") < 0 {
		return done, nil
	}
	for _, ext := range tableDef.Extents {
		tbl, err := e.ScanExtent(name, ext.FilePath, &tableDef.Schema, []string{"Id"}, nil, 0)
		if err != nil {
			return nil, err
		}
		if tbl == nil {
			continue
		}
		idIdx := tbl.Schema.ColumnIndex("Id")
		if idIdx < 0 {
			continue
		}
		for _, r := range tbl.Rows {
			if r[idIdx] != nil {
				done[fmt.Sprintf("%v", r[idIdx])] = true
			}
		}
	}
	return done, nil
}

// buildEmbedRows maps source rows and their vectors onto the TARGET
// table's column order (which may differ from the generated schema's
// if the table was pre-created), matching .set-or-append's
// column-order independence.
func buildEmbedRows(src *types.Table, chunk []int, vecs [][]float64,
	tableDef *catalog.Table, em embed.Embedder, idIdx, modelIdx, provIdx int) ([]types.Row, error) {

	rows := make([]types.Row, len(chunk))
	for i, ri := range chunk {
		row := src.Rows[ri]

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
		encoded, err := encodeVector(vecs[i])
		if err != nil {
			return nil, err
		}

		out := make(types.Row, len(tableDef.Schema.Columns))
		for ti, tc := range tableDef.Schema.Columns {
			switch tc.Name {
			case "Id":
				out[ti] = fmt.Sprintf("%v", row[idIdx])
			case "Embedding":
				out[ti] = encoded
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
	return rows, nil
}

func columnNames(s *types.Schema) []string {
	names := make([]string, len(s.Columns))
	for i, c := range s.Columns {
		names[i] = c.Name
	}
	return names
}

// embedAllRows produces one vector per text, honouring a per-row Model
// column where present and where the backend supports switching.
// Contiguous runs of the same model go through in a single EmbedBatch
// call, so the common case (no Model column at all, or one constant
// value) is exactly one batched pass.
func embedAllRows(ctx context.Context, em embed.Embedder, texts []string,
	src *types.Table, chunk []int, modelIdx int) ([][]float64, error) {

	rowModel := func(i int) string {
		if modelIdx >= 0 && src.Rows[chunk[i]][modelIdx] != nil {
			return fmt.Sprintf("%v", src.Rows[chunk[i]][modelIdx])
		}
		return ""
	}

	out := make([][]float64, len(texts))
	for start := 0; start < len(texts); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		want := rowModel(start)
		end := start + 1
		for end < len(texts) && rowModel(end) == want {
			end++
		}

		target := em
		if want != "" && want != em.Name() {
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

		batch, err := target.EmbedBatch(ctx, texts[start:end])
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

// checkModelIdentity refuses to write vectors from one model into a
// table already holding another's. Runs with NO vectors in hand, so it
// can fire even when every row would be skipped as already-embedded.
//
// Waived when the query projects its own Model column: a caller who
// states the model per row has made that claim deliberately and owns
// it.
func (e *Engine) checkModelIdentity(name string, tableDef *catalog.Table,
	em embed.Embedder, modelIdx int) error {

	if modelIdx >= 0 {
		return nil
	}
	row := e.sampleEmbedRow(name, tableDef)
	if row == nil || row.model == "" {
		return nil
	}
	if row.model != em.Name() {
		return fmt.Errorf("table %q already holds vectors from model %q but this run would produce %q; "+
			"appending would mix two vector spaces that rank against each other without error. "+
			"Re-embed the table, or write to a new one",
			name, row.model, em.Name())
	}
	return nil
}

// checkVectorWidth refuses a width change against what the table
// already holds. NOT waived by an explicit Model column: vectors of
// two different widths cannot belong to one table under any
// labelling, so the caller cannot have meant it.
func (e *Engine) checkVectorWidth(name string, tableDef *catalog.Table, vecs [][]float64) error {
	if len(vecs) == 0 {
		return nil
	}
	row := e.sampleEmbedRow(name, tableDef)
	if row == nil || row.width == 0 {
		return nil
	}
	if row.width != len(vecs[0]) {
		return fmt.Errorf("table %q holds %d-wide vectors but this run produced %d-wide; "+
			"re-embed the table, or write to a new one", name, row.width, len(vecs[0]))
	}
	return nil
}

type embedSample struct {
	model string
	width int
}

// sampleEmbedRow reads one existing row. Identity is uniform per table
// by construction, so one row answers both questions; a full scan
// would cost the corpus to learn one fact.
func (e *Engine) sampleEmbedRow(name string, tableDef *catalog.Table) *embedSample {
	if tableDef == nil || len(tableDef.Extents) == 0 {
		return nil
	}
	if tableDef.Schema.ColumnIndex("Model") < 0 || tableDef.Schema.ColumnIndex("Embedding") < 0 {
		return nil
	}
	existing, err := e.ScanExtent(name, tableDef.Extents[0].FilePath, &tableDef.Schema,
		[]string{"Model", "Embedding"}, nil, 1)
	if err != nil || existing == nil || len(existing.Rows) == 0 {
		// A table we cannot sample is not a reason to block an ingest;
		// the read path will surface the real problem.
		return nil
	}
	mIdx := existing.Schema.ColumnIndex("Model")
	eIdx := existing.Schema.ColumnIndex("Embedding")
	if mIdx < 0 || eIdx < 0 {
		return nil
	}
	out := &embedSample{}
	if existing.Rows[0][mIdx] != nil {
		out.model = fmt.Sprintf("%v", existing.Rows[0][mIdx])
	}
	if existing.Rows[0][eIdx] != nil {
		if prev, ok := parseFloatArray(existing.Rows[0][eIdx]); ok {
			out.width = len(prev)
		}
	}
	return out
}

// firstDuplicateId reports the first Id seen more than once, and how
// many times it appears. Returns "" when every Id is unique.
func firstDuplicateId(src *types.Table, idIdx int) (string, int) {
	counts := make(map[string]int, len(src.Rows))
	var firstDup string
	for _, row := range src.Rows {
		id := fmt.Sprintf("%v", row[idIdx])
		counts[id]++
		if counts[id] > 1 && firstDup == "" {
			firstDup = id
		}
	}
	if firstDup == "" {
		return "", 0
	}
	return firstDup, counts[firstDup]
}

func distinctIdCount(src *types.Table, idIdx int) int {
	seen := make(map[string]struct{}, len(src.Rows))
	for _, row := range src.Rows {
		seen[fmt.Sprintf("%v", row[idIdx])] = struct{}{}
	}
	return len(seen)
}
