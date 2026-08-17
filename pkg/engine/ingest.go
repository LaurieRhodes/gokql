package engine

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/catalog"	
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// --- Ingest ---

func (e *Engine) ingestInline(tableName string, data string) (*types.Table, error) {
	table := e.Catalog.GetTable(tableName)
	if table == nil {
		return nil, fmt.Errorf("table %q not found", tableName)
	}

	// Parse CSV data. Validated against dataColumnCount (the schema's
	// REAL, external-data-facing columns), not the full schema column
	// count — a table with an automatic _TimeReceived column has one
	// more schema column than any real CSV could ever supply a field
	// for, since that column is engine-generated, never user data.
	// Rows are still SIZED to the full schema (len(table.Schema.Columns),
	// not len(fields)), leaving the reserved _TimeReceived slot nil for
	// flushBatch's own stampTimeReceived to fill in.
	lines := strings.Split(data, "\n")
	var rows []types.Row
	dataCols := dataColumnCount(table.Schema)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := splitCSVLine(line)
		if len(fields) != dataCols {
			return nil, fmt.Errorf("row has %d fields, expected %d columns: %q",
				len(fields), dataCols, line)
		}

		row := make(types.Row, len(table.Schema.Columns))
		for i, field := range fields {
			val, err := types.ParseValue(field, table.Schema.Columns[i].Type)
			if err != nil {
				return nil, fmt.Errorf("row %d, column %q: %w", len(rows)+1, table.Schema.Columns[i].Name, err)
			}
			row[i] = val
		}
		rows = append(rows, row)
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("no data to ingest")
	}

	// Create extent entry
	extentID := e.newExtentID()
	filePath := e.newExtentPath(tableName, extentID)

	entry := catalog.ExtentEntry{
		ID:        extentID,
		FilePath:  filePath,
		RowCount:  int64(len(rows)),
		CreatedAt: time.Now().UTC(),
	}

	// Build extent data for writing
	extData := types.NewTable(tableName, table.Schema)
	for _, row := range rows {
		extData.AddRow(row)
	}

	// Persist to Vortex file (before catalog, so we can record file size)
	if err := e.SaveExtent(filePath, extData); err != nil {
		return nil, fmt.Errorf("save extent: %w", err)
	}

	// Record actual file size
	entry.SizeBytes = e.ExtentFileSize(filePath)

	// Register in catalog
	if err := e.Catalog.AddExtent(tableName, entry); err != nil {
		return nil, err
	}

	// Return result
	result := types.NewTable("", types.Schema{
		Columns: []types.Column{
			{Name: "Result", Type: types.TypeString},
			{Name: "ExtentId", Type: types.TypeString},
			{Name: "RowsIngested", Type: types.TypeLong},
		},
	})
	result.AddRow(types.Row{
		fmt.Sprintf("Ingested %d rows into %s", len(rows), tableName),
		extentID,
		int64(len(rows)),
	})
	return result, nil
}

func (e *Engine) ingestCSVFile(tableName string, filePath string) (*types.Table, error) {
	tableDef := e.Catalog.GetTable(tableName)
	if tableDef == nil {
		return nil, fmt.Errorf("table %q not found", tableName)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read CSV file: %w", err)
	}

	csvData := string(data)
	lines := strings.Split(csvData, "\n")

	// dataCols excludes a trailing, automatic _TimeReceived column (see
	// timereceived.go) — a real CSV file can never supply a field for
	// an engine-generated column, so every CSV-field-count comparison
	// in this function is against dataCols, not the table's full
	// schema column count. Rows built below are still sized to the
	// FULL schema, leaving that reserved slot nil for flushBatch's own
	// stampTimeReceived to fill in.
	dataCols := dataColumnCount(tableDef.Schema)

	// Column-count mismatch against the table schema is rejected outright
	// rather than silently padded or truncated. Padding manufactures null
	// cells the file never specified; truncating discards data the file
	// did specify — either way ingest would succeed while silently writing
	// wrong data. Found the hard way: a 4-column Edges CSV against a
	// 5-column schema defeated header detection below (the header line no
	// longer column-matched) and the header text itself got ingested as a
	// literal data row.
	var firstFields []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			firstFields = splitCSVLine(trimmed)
			break
		}
	}
	if firstFields != nil && len(firstFields) != dataCols {
		want := make([]string, dataCols)
		for i := 0; i < dataCols; i++ {
			want[i] = tableDef.Schema.Columns[i].Name
		}
		return nil, fmt.Errorf(
			"CSV column count (%d) does not match table %q schema (%d columns: %s) — first line: %q",
			len(firstFields), tableName, dataCols, strings.Join(want, ", "),
			strings.Join(firstFields, ","))
	}

	// Detect if first line is a header by checking if it matches column
	// names — a WEAK signal deliberately (at least half match, not all),
	// since a genuine header with a typo or a differently-cased name
	// should still count as a header for the purpose of "skip this line
	// as data". What changed: this weak signal used to ALSO gate
	// whether the header's declared order got used to remap columns —
	// it didn't; the row-building loop below was purely positional
	// regardless, matching fields[i] to Columns[i] by INDEX even when a
	// header with a genuinely different column order was correctly
	// detected and skipped. Found live, not hypothetical: a table
	// created via .create-merge (whose resulting column order can
	// differ from what a CSV file's own header declares) silently
	// misaligned an entire 866-row ingest this way — every value landed
	// in the wrong column, with header detection correctly determining
	// "yes, skip line 1" and then doing nothing further with what that
	// header actually said.
	//
	// Fixed: a header that weakly matches (the existing heuristic) is
	// now REQUIRED to fully, unambiguously match the table's column set
	// by name (case-insensitive, order-independent) before it's trusted
	// to remap anything — colIdxForField[i] gives the TABLE SCHEMA
	// COLUMN INDEX that CSV field position i belongs to. A header that
	// only partially matches (weakly header-shaped, per the original
	// heuristic, but not a clean permutation of the real column names)
	// is treated as genuinely ambiguous and rejected outright, rather
	// than guessed at — silently guessing wrong is exactly the bug this
	// fix closes. A CSV with no header at all still ingests exactly as
	// before: positionally, unchanged.
	hasHeader := false
	colIdxForField := make([]int, dataCols)
	for i := range colIdxForField {
		colIdxForField[i] = i // identity mapping unless a header overrides it
	}
	if len(lines) > 0 {
		firstFields := splitCSVLine(strings.TrimSpace(lines[0]))
		if len(firstFields) == dataCols {
			matchCount := 0
			for i, field := range firstFields {
				if strings.EqualFold(strings.TrimSpace(field), tableDef.Schema.Columns[i].Name) {
					matchCount++
				}
			}
			if matchCount >= len(firstFields)/2 {
				hasHeader = true
			}
			if hasHeader && matchCount != len(firstFields) {
				remap := make([]int, len(firstFields))
				// used/the inner search scope over ONLY the data
				// columns (tableDef.Schema.Columns[:dataCols]) — a
				// header can never legitimately name the reserved
				// _TimeReceived column, so it's excluded from the
				// match search entirely rather than left reachable as
				// a confusing edge case.
				used := make([]bool, dataCols)
				fullyMatched := true
				for i, field := range firstFields {
					name := strings.TrimSpace(field)
					found := -1
					for ci := 0; ci < dataCols; ci++ {
						if !used[ci] && strings.EqualFold(tableDef.Schema.Columns[ci].Name, name) {
							found = ci
							break
						}
					}
					if found < 0 {
						fullyMatched = false
						break
					}
					used[found] = true
					remap[i] = found
				}
				if !fullyMatched {
					return nil, fmt.Errorf(
						"line 1 looks like a header (%d/%d column names match table %q) but doesn't "+
							"exactly match its column set by name — refusing to guess at a remapping; "+
							"either fix the header to exactly name every column (any order), or remove "+
							"it so the file is ingested purely positionally. Header: %q",
						matchCount, len(firstFields), tableName, strings.Join(firstFields, ","))
				}
				colIdxForField = remap
			}
		}
	}

	startLine := 0
	if hasHeader {
		startLine = 1
	}

	// Validate every data line's field count AND every field's value up
	// front, before any batch is flushed to disk. The file is already
	// fully in memory (read above), so this pass is cheap, and it makes
	// ingest all-or-nothing: without it, a malformed row past the first
	// ~50K-row batch boundary would leave an earlier, valid-looking
	// batch already committed by the time the error surfaced. Value
	// validation was NOT here before — the row-building loop below used
	// to catch a parse failure and silently substitute nil per-cell
	// (nil numerics/datetimes round-trip as their zero value elsewhere
	// in this codebase, so this manifested as e.g. a datetime column
	// silently reading back as epoch — the second half of the same live
	// incident that surfaced the header-remapping bug above: column
	// misalignment fed a non-numeric string into a Seq:long slot, which
	// failed to parse, which this loop then silently zeroed instead of
	// erroring).
	for lineIdx := startLine; lineIdx < len(lines); lineIdx++ {
		line := strings.TrimSpace(lines[lineIdx])
		if line == "" {
			continue
		}
		fields := splitCSVLine(line)
		if len(fields) != dataCols {
			return nil, fmt.Errorf(
				"line %d: row has %d fields, table %q schema expects %d — line content: %q",
				lineIdx+1, len(fields), tableName, dataCols, line)
		}
		for i, field := range fields {
			schemaIdx := colIdxForField[i]
			if _, err := types.ParseValue(field, tableDef.Schema.Columns[schemaIdx].Type); err != nil {
				return nil, fmt.Errorf(
					"line %d: field %d (%q) does not parse as %s for column %q: %w — line content: %q",
					lineIdx+1, i+1, field, tableDef.Schema.Columns[schemaIdx].Type,
					tableDef.Schema.Columns[schemaIdx].Name, err, line)
			}
		}
	}

	// Parse rows in batches for chunked ingestion
	const batchSize = ExtentTargetRows // ~50K rows per extent, with internal 8192-row zones
	var totalIngested int64
	var extentIDs []string

	var currentBatch []types.Row

	for lineIdx := startLine; lineIdx < len(lines); lineIdx++ {
		line := strings.TrimSpace(lines[lineIdx])
		if line == "" {
			continue
		}

		fields := splitCSVLine(line) // field count AND every value already validated above

		// colIdxForField[i] is the TABLE SCHEMA column that CSV field
		// position i belongs to — identity (i.e. positional) unless a
		// header was detected and exactly matched the table's column
		// set by name, in which case it reflects that remapping. Errors
		// are not expected here (every field already parsed successfully
		// in the validation pass above, against the same schema column
		// this same colIdxForField mapping points at) — checked anyway
		// rather than assumed, since a second, silent failure here would
		// be exactly the bug this fix exists to close.
		// Sized to the FULL schema (not len(fields)), leaving the
		// reserved _TimeReceived slot nil for flushBatch's own
		// stampTimeReceived to fill in.
		row := make(types.Row, len(tableDef.Schema.Columns))
		for i, field := range fields {
			schemaIdx := colIdxForField[i]
			val, err := types.ParseValue(field, tableDef.Schema.Columns[schemaIdx].Type)
			if err != nil {
				return nil, fmt.Errorf(
					"line %d: field %d (%q) failed to parse on second pass (should be unreachable — "+
						"already validated once): %w", lineIdx+1, i+1, field, err)
			}
			row[schemaIdx] = val
		}
		currentBatch = append(currentBatch, row)

		// Flush batch when full
		if len(currentBatch) >= batchSize {
			extID, err := e.flushBatch(tableName, tableDef, currentBatch)
			if err != nil {
				return nil, fmt.Errorf("flush batch: %w", err)
			}
			totalIngested += int64(len(currentBatch))
			extentIDs = append(extentIDs, extID)
			currentBatch = nil
		}
	}

	// Flush remaining rows
	if len(currentBatch) > 0 {
		extID, err := e.flushBatch(tableName, tableDef, currentBatch)
		if err != nil {
			return nil, fmt.Errorf("flush final batch: %w", err)
		}
		totalIngested += int64(len(currentBatch))
		extentIDs = append(extentIDs, extID)
	}

	if totalIngested == 0 {
		return nil, fmt.Errorf("no data to ingest from %s", filePath)
	}

	// Return result
	result := types.NewTable("", types.Schema{
		Columns: []types.Column{
			{Name: "Result", Type: types.TypeString},
			{Name: "RowsIngested", Type: types.TypeLong},
			{Name: "Extents", Type: types.TypeLong},
		},
	})
	result.AddRow(types.Row{
		fmt.Sprintf("Ingested %d rows into %s from %s", totalIngested, tableName, filePath),
		totalIngested,
		int64(len(extentIDs)),
	})
	return result, nil
}

// flushBatch writes a batch of rows as a new extent.
func (e *Engine) flushBatch(tableName string, tableDef *catalog.Table, rows []types.Row) (string, error) {
	// Stamp _TimeReceived (see timereceived.go) for any row that
	// doesn't already carry a real value there -- a genuinely NEW
	// write. A row already carrying a non-nil value (compaction's own
	// scan-then-rewrite, an MV merge/recompute) is left untouched, so
	// its real, original write-time value survives unchanged rather
	// than being silently overwritten with THIS call's own time. A
	// no-op for any table without the column at all.
	stampTimeReceived(tableDef.Schema, rows)

	extentID := e.newExtentID()
	filePath := e.newExtentPath(tableName, extentID)

	extData := types.NewTable(tableName, tableDef.Schema)
	for _, row := range rows {
		extData.AddRow(row)
	}

	if err := e.SaveExtent(filePath, extData); err != nil {
		return "", fmt.Errorf("save extent: %w", err)
	}

	entry := catalog.ExtentEntry{
		ID:        extentID,
		FilePath:  filePath,
		RowCount:  int64(len(rows)),
		SizeBytes: e.ExtentFileSize(filePath),
		CreatedAt: time.Now().UTC(),
	}

	if err := e.Catalog.AddExtent(tableName, entry); err != nil {
		return "", err
	}

	return extentID, nil
}

// splitCSVLine splits a CSV line on commas, respecting quoted fields.
func splitCSVLine(line string) []string {
	var fields []string
	var current strings.Builder
	inQuote := false
	braceDepth := 0  // Track {}/[] nesting for JSON dynamic columns
	bracketDepth := 0

	for i := 0; i < len(line); i++ {
		ch := line[i]
		if inQuote {
			// Inside CSV-level quoting (only entered at field boundary)
			if ch == '"' {
				if i+1 < len(line) && line[i+1] == '"' {
					current.WriteByte('"')
					i++
				} else {
					inQuote = false
				}
			} else {
				current.WriteByte(ch)
			}
		} else if braceDepth > 0 || bracketDepth > 0 {
			// Inside JSON — all characters are literal, including quotes
			switch ch {
			case '{':
				braceDepth++
			case '}':
				braceDepth--
			case '[':
				bracketDepth++
			case ']':
				bracketDepth--
			}
			current.WriteByte(ch)
		} else {
			// Top-level field parsing
			switch ch {
			case '"':
				inQuote = true // CSV-level quoting
			case '{':
				braceDepth++
				current.WriteByte(ch)
			case '[':
				bracketDepth++
				current.WriteByte(ch)
			case ',':
				fields = append(fields, current.String())
				current.Reset()
			default:
				current.WriteByte(ch)
			}
		}
	}
	fields = append(fields, current.String())
	return fields
}

// ingestCSVFile reads a CSV file and ingests it into the named table.
// The file must have no header row — columns map positionally to the table schema.
// For files with headers, use ingestCSVFileWithHeader.

// mergeExtents compacts all extents for a table into optimally-sized files.
// Each output file targets ExtentTargetRows rows with ZoneChunkSize-row zones,
// producing zone maps for predicate pushdown.
func (e *Engine) mergeExtents(tableName string) (*types.Table, error) {
	tableDef := e.Catalog.GetTable(tableName)
	if tableDef == nil {
		return nil, fmt.Errorf("table %q not found", tableName)
	}

	if len(tableDef.Extents) <= 1 {
		result := types.NewTable("", types.Schema{
			Columns: []types.Column{{Name: "Result", Type: types.TypeString}},
		})
		result.AddRow(types.Row{fmt.Sprintf("Table %s has %d extent(s), nothing to merge", tableName, len(tableDef.Extents))})
		return result, nil
	}

	// Read all extents into memory
	allCols := tableDef.Schema.ColumnNames()
	var allRows []types.Row

	for _, ext := range tableDef.Extents {
		extResult, err := e.ScanExtent(tableDef.Name, ext.FilePath, &tableDef.Schema, allCols, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("reading extent %s: %w", ext.ID, err)
		}
		allRows = append(allRows, extResult.Rows...)
	}

	// Track old extent IDs for replacement
	oldIDs := make([]string, len(tableDef.Extents))
	for i, ext := range tableDef.Extents {
		oldIDs[i] = ext.ID
	}

	// Write optimally-sized new extents
	var newEntries []catalog.ExtentEntry
	totalRows := len(allRows)

	for start := 0; start < totalRows; start += ExtentTargetRows {
		end := start + ExtentTargetRows
		if end > totalRows {
			end = totalRows
		}

		extentID := e.newExtentID()
		filePath := e.newExtentPath(tableName, extentID)

		extData := types.NewTable(tableName, tableDef.Schema)
		for _, row := range allRows[start:end] {
			extData.AddRow(row)
		}

		if err := e.SaveExtent(filePath, extData); err != nil {
			return nil, fmt.Errorf("writing merged extent: %w", err)
		}

		newEntries = append(newEntries, catalog.ExtentEntry{
			ID:        extentID,
			FilePath:  filePath,
			RowCount:  int64(end - start),
			SizeBytes: e.ExtentFileSize(filePath),
			CreatedAt: time.Now().UTC(),
		})
	}

	// Atomically replace old extents with new ones
	if err := e.Catalog.ReplaceExtents(tableName, oldIDs, newEntries); err != nil {
		return nil, fmt.Errorf("replacing extents: %w", err)
	}

	result := types.NewTable("", types.Schema{
		Columns: []types.Column{
			{Name: "Result", Type: types.TypeString},
			{Name: "OldExtents", Type: types.TypeLong},
			{Name: "NewExtents", Type: types.TypeLong},
			{Name: "TotalRows", Type: types.TypeLong},
		},
	})
	result.AddRow(types.Row{
		fmt.Sprintf("Merged %d extents into %d for table %s", len(oldIDs), len(newEntries), tableName),
		int64(len(oldIDs)),
		int64(len(newEntries)),
		int64(totalRows),
	})
	return result, nil
}
