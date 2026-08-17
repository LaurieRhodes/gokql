package engine

// storage.go implements extent data persistence using Vortex files.
//
// Write path: KQL rows → columnar arrays → vortex-go writer → .vtx file
// Read path:  .vtx file → vortex.Open → Scan(projected columns) → KQL rows
//
// Architecture:
//   - Each extent file contains multiple chunks (zones) of ~8192 rows each
//   - Zone maps store per-zone min/max statistics for predicate pushdown
//   - Optimal extent size is ~50,000 rows (6 zones per file)
//   - Column projection pushes into the scan layer: unrequested columns
//     are never read from disk, never decoded, never allocated
//   - Zone pruning pushes predicates into the scan layer: zones whose
//     min/max statistics prove no rows can match are skipped entirely

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	vortex "github.com/LaurieRhodes/vortex-go"
	"github.com/LaurieRhodes/vortex-go/encoding"
	"github.com/LaurieRhodes/vortex-go/writer"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// ZoneChunkSize is the number of rows per chunk (zone) within a Vortex file.
// Each chunk gets its own min/max zone map statistics, enabling intra-file
// pruning. This matches the Rust Vortex default zone_len of 8192.
const ZoneChunkSize = 8192

// ExtentTargetRows is the target number of rows per extent file.
// At 12 zones of 8192 rows each, this minimizes worst-case peak query
// RSS across a moderate-selectivity group-by, a zone-prunable range
// filter, and a sparse exact-match query (335MB, vs 453MB at the old
// 6-zone/49152 default and 1.6GB at 1-zone/8192) on a 25M-row,
// 5000-distinct-Host synthetic benchmark. Chosen for containerized
// deployment where a fixed memory ceiling isn't set yet, so worst-case
// RSS across query shapes was optimized for over disk size or best-case
// query time (both are close to flat across 49152-262144; the RSS curve
// is U-shaped and this sits near its minimum, not at either extreme).
// See extent-size sweep results for full methodology/data.
const ExtentTargetRows = 98304 // 12 * 8192

// openExtentChunks opens an extent's Vortex file and returns a chunk
// iterator with column projection and optional zone-level pruning.
// Shared by ScanExtent (row materialization) and the columnar
// aggregation path (columnar_agg.go).
// openExtentChunks opens filePath and starts a scan for columns —
// filtered down, per-file, to whichever of them ACTUALLY exist in
// THIS specific file's own stored schema (via vf.ColumnNames(), a
// cheap, metadata-only read, no scan needed) before ever calling
// vf.Scan(). This matters concretely for .alter-merge table (see
// alter_table.go): vf.Scan() itself hard-errors ("column ... not
// found in schema") if asked for a column absent from the FILE's own
// physical schema, which is exactly the situation for every extent
// written before a later .alter-merge table added a new column to the
// table's LOGICAL schema -- found live, not hypothetical, retrofitting
// _TimeReceived onto an existing table and then trying to read it back
// out. The per-chunk "schema evolution" fallback already present
// further down in ScanExtent (leaving an absent column's cells nil)
// was ALREADY correctly written for exactly this case, but was
// structurally unreachable: it only runs once an iterator has already
// been successfully created, and vf.Scan() itself was rejecting the
// request before that point for any column missing from this specific
// file. Returns the FILTERED column list alongside the iterator so
// ScanExtent knows which of the originally-requested columns to treat
// as "genuinely absent from this file" versus "present, decode
// normally".
func (e *Engine) openExtentChunks(filePath string, columns []string, filter *vortex.RowFilter) (*vortex.ScanIterator, *os.File, []string, error) {
	fullPath := filepath.Join(e.Catalog.DatabasePath(), filePath)

	f, err := os.Open(fullPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open file %s: %w", filePath, err)
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, nil, fmt.Errorf("stat file %s: %w", filePath, err)
	}

	vf, err := vortex.Open(f, stat.Size())
	if err != nil {
		f.Close()
		return nil, nil, nil, fmt.Errorf("open vortex file %s: %w", filePath, err)
	}

	presentInFile := make(map[string]bool, len(vf.ColumnNames()))
	for _, n := range vf.ColumnNames() {
		presentInFile[n] = true
	}
	filteredColumns := make([]string, 0, len(columns))
	for _, c := range columns {
		if presentInFile[c] {
			filteredColumns = append(filteredColumns, c)
		}
	}

	scanOpts := vortex.ScanOptions{
		Columns: filteredColumns,
	}
	if filter != nil && len(filter.Predicates) > 0 {
		scanOpts.RowFilter = filter
	}

	iter, err := vf.Scan(scanOpts)
	if err != nil {
		f.Close()
		return nil, nil, nil, fmt.Errorf("scan %s: %w", filePath, err)
	}
	return iter, f, filteredColumns, nil
}

// ScanExtent opens a Vortex file and scans it with column projection and
// optional zone-level predicate pushdown. Only the requested columns are
// decoded; all others are skipped entirely. When a RowFilter is provided,
// zones whose min/max statistics prove no rows can match are skipped without
// reading data. maxRows > 0 stops chunk iteration once at least that many
// rows have been produced (the result may overshoot to a chunk boundary;
// callers truncate downstream).
//
// This is the core read path — every query goes through here. It is safe
// to call concurrently: all state is per-call, and a shared RowFilter is
// only read during scanning.
func (e *Engine) ScanExtent(tableName, filePath string, tableSchema *types.Schema, columns []string, filter *vortex.RowFilter, maxRows int64) (*types.Table, error) {
	iter, f, _, err := e.openExtentChunks(filePath, columns, filter)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Build projected schema — only the columns we're scanning
	projectedSchema := buildProjectedSchema(tableSchema, columns)

	// Log zone pruning stats in verbose mode
	if e.Verbose {
		pruned, total := iter.ZonePruningStats()
		if total > 0 {
			fmt.Fprintf(os.Stderr, "[scan] file=%s zones=%d pruned=%d\n", filePath, total, pruned)
		}
	}

	table := types.NewTable("", projectedSchema)
	var rowsFiltered int64

	for iter.Next() {
		if maxRows > 0 && int64(len(table.Rows)) >= maxRows {
			break // take/limit early-exit: enough rows produced
		}
		chunk := iter.Result()

		// Decode each projected column into its unboxed typed vector.
		// Boxing is deferred until after row selection, so filtered-out
		// rows never allocate.
		colVecs := make([]*colVec, len(columns))
		vecsByName := make(map[string]*colVec, len(columns))
		for i, colName := range columns {
			arr, ok := chunk.Columns[colName]
			if !ok {
				continue // column not in file (schema evolution): stays nil → null cells
			}
			vec, err := decodeColumnVec(e, tableName, colName, arr, projectedSchema.Columns[i].Type)
			if err != nil {
				return nil, fmt.Errorf("decode column %q from %s: %w", colName, filePath, err)
			}
			colVecs[i] = vec
			vecsByName[colName] = vec
		}

		// Row-level predicate filtering on the typed vectors (same
		// predicate set as zone pruning, applied within surviving
		// zones). Conservative: may over-include, never excludes a
		// match; the pipeline's WhereOp re-filters downstream.
		sel, selCount, _ := selectChunkRows(filter, vecsByName, chunk.RowCount)
		rowsFiltered += int64(chunk.RowCount - selCount)
		if selCount == 0 {
			continue // no candidate rows: skip transposition entirely
		}

		// Transpose only selected rows, boxing exactly once per
		// surviving cell.
		for rowIdx := 0; rowIdx < chunk.RowCount; rowIdx++ {
			if sel != nil && !sel[rowIdx] {
				continue
			}
			row := make(types.Row, len(columns))
			for colIdx, vec := range colVecs {
				if vec != nil {
					row[colIdx] = vec.value(rowIdx)
				}
			}
			table.AddRow(row)
		}
	}

	if e.Verbose && rowsFiltered > 0 {
		fmt.Fprintf(os.Stderr, "[scan] file=%s rows filtered pre-transpose: %d\n", filePath, rowsFiltered)
	}

	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("scan iteration %s: %w", filePath, err)
	}

	return table, nil
}

// ScanExtentInfo returns metadata about a Vortex file without reading data.
// Used for extent-level statistics and pruning decisions.
type ExtentInfo struct {
	RowCount    int
	ColumnCount int
	FileSize    int64
	Schema      *vortex.DType
}

func (e *Engine) ScanExtentInfo(filePath string) (*ExtentInfo, error) {
	fullPath := filepath.Join(e.Catalog.DatabasePath(), filePath)

	f, err := os.Open(fullPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}

	vf, err := vortex.Open(f, stat.Size())
	if err != nil {
		return nil, err
	}

	dt := vf.DType()
	colCount := 0
	if dt != nil {
		colCount = len(dt.FieldNames)
	}

	// Count rows by scanning with a single column
	totalRows := 0
	if colCount > 0 {
		iter, err := vf.Scan(vortex.ScanOptions{Columns: dt.FieldNames[:1]})
		if err == nil {
			for iter.Next() {
				totalRows += iter.Result().RowCount
			}
		}
	}

	return &ExtentInfo{
		RowCount:    totalRows,
		ColumnCount: colCount,
		FileSize:    stat.Size(),
		Schema:      dt,
	}, nil
}

// buildProjectedSchema creates a schema containing only the requested columns,
// preserving the order of the requested column list.
func buildProjectedSchema(tableSchema *types.Schema, columns []string) types.Schema {
	projected := types.Schema{
		Columns: make([]types.Column, len(columns)),
	}
	for i, name := range columns {
		idx := tableSchema.ColumnIndex(name)
		if idx >= 0 {
			projected.Columns[i] = tableSchema.Columns[idx]
		} else {
			// Column not in table schema — shouldn't happen, but handle gracefully
			projected.Columns[i] = types.Column{Name: name, Type: types.TypeString}
		}
	}
	return projected
}

// SaveExtent writes extent data to a Vortex file at the given relative path.
// The data is written as multiple ZoneChunkSize-row chunks within a single file,
// each chunk becoming a zone with min/max statistics for predicate pushdown.
func (e *Engine) SaveExtent(relPath string, data *types.Table) error {
	fullPath := filepath.Join(e.Catalog.DatabasePath(), relPath)

	// Resolve database-wide shared-dictionary decisions once for the
	// WHOLE extent (all chunks), against every distinct value this
	// extent's rows contain — see shareddict.go and
	// vortex_bridge.go's "write-path integration" section. A nil/empty
	// result (no TypeString columns, or none stay within the cap) means
	// every column falls back to schemaToVortexDType/rowsToVortexArrays
	// exactly as before this feature existed.
	dictDecisions, err := resolveDictDecisions(e, data.Name, &data.Schema, data.Rows)
	if err != nil {
		return fmt.Errorf("resolve shared dictionary decisions: %w", err)
	}
	// Update THIS engine's own read cache immediately, even though this
	// call is on the write path. Without this, a long-lived Engine that
	// writes more extents for a table+column AFTER it has already
	// cached that dictionary from an earlier read (e.g. ingest, query,
	// ingest more, query again — a single-process, single-Engine
	// sequence, not a cross-process race) would keep resolving new
	// extents' codes against its own stale, too-small cached snapshot
	// and panic on an out-of-range code. Cross-process staleness (a
	// DIFFERENT Engine/process having extended the dictionary) is a
	// separate, accepted limitation documented in the Engine struct's
	// dictCache comment — this fixes the same-process case, which is
	// not optional.
	for colName, d := range dictDecisions {
		e.dictCacheMu.Lock()
		e.dictCache[data.Name+"."+colName] = d.dict
		e.dictCacheMu.Unlock()
	}

	// Build Vortex schema from KQL schema
	vortexSchema := schemaToVortexDTypeWithDict(&data.Schema, dictDecisions)

	// Write Vortex file with multi-chunk zones
	var buf bytes.Buffer
	opts := writer.DefaultOptions() // zone maps enabled by default
	w, err := writer.NewWriter(&buf, vortexSchema, opts)
	if err != nil {
		return fmt.Errorf("create vortex writer: %w", err)
	}

	// Write rows in ZoneChunkSize-row chunks for zone map generation
	totalRows := len(data.Rows)
	for start := 0; start < totalRows; start += ZoneChunkSize {
		end := start + ZoneChunkSize
		if end > totalRows {
			end = totalRows
		}
		chunkRows := data.Rows[start:end]

		columns, err := rowsToVortexArraysWithDict(&data.Schema, chunkRows, dictDecisions)
		if err != nil {
			return fmt.Errorf("transpose chunk [%d:%d]: %w", start, end, err)
		}

		if err := w.WriteChunk(columns); err != nil {
			return fmt.Errorf("write chunk [%d:%d]: %w", start, end, err)
		}
	}

	if err := w.Close(); err != nil {
		return fmt.Errorf("close vortex writer: %w", err)
	}

	// Commit by atomic rename: a crash mid-write leaves a temp file,
	// never a truncated extent. In discovery mode the rename IS the
	// transaction — the file becoming visible registers it.
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return fmt.Errorf("create extent directory: %w", err)
	}
	tmpPath := fullPath + ".tmp"
	if err := os.WriteFile(tmpPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	if err := os.Rename(tmpPath, fullPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("commit file: %w", err)
	}

	return nil
}

// findTableForExtent finds which table contains the given extent ID.
func (e *Engine) findTableForExtent(extentID string) *catalog.Table {
	for _, tableName := range e.Catalog.ListTables() {
		table := e.Catalog.GetTable(tableName)
		for _, ext := range table.Extents {
			if ext.ID == extentID {
				return table
			}
		}
	}
	return nil
}

// ExtentFileSize returns the on-disk size of an extent's Vortex file.
func (e *Engine) ExtentFileSize(filePath string) int64 {
	fullPath := filepath.Join(e.Catalog.DatabasePath(), filePath)
	info, err := os.Stat(fullPath)
	if err != nil {
		return 0
	}
	return info.Size()
}

// Ensure encoding package is linked (registers decoder).
var _ = encoding.StringValues
