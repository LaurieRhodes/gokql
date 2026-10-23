package catalog

// discover.go — catalog-free database discovery.
//
// A directory of self-describing .vtx files is a complete database:
// every extent footer carries its schema, row count, layouts, and zone
// maps, and the filename convention <Table>_<hexid>.vtx carries table
// membership. Discover builds the same in-memory Catalog the JSON
// loader produces by globbing the directory and reading footers in
// parallel, so the engine runs unchanged on top of either.
//
// In discovery mode the files are the source of truth and there is no
// shared mutable metadata: save() is a no-op, and concurrent sessions
// ingesting into the same directory cannot conflict — each writes a
// uniquely named extent via write-to-temp + atomic rename, and the
// rename is the commit. catalog.json never exists; a scope is created
// by writing its first file and destroyed by deleting the directory.
//
// Extents predating extension-dtype emission surface their physical
// schema (datetime reads as long, dynamic as string); extents written
// with kql.* extension identities recover exact KQL types.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	vortex "github.com/LaurieRhodes/vortex-go"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// DiscoveredSchemaFn converts a Vortex file dtype field to a KQL type.
// Injected by the engine package (which owns the dtype↔KQL mapping) to
// avoid an import cycle.
var DiscoveredSchemaFn func(dt *vortex.DType) types.KQLType

// Discover builds a catalog by scanning dir for .vtx extent files
// (directly and under extents/), reading each footer for schema and
// row count. The returned catalog is marked discovery-mode: metadata
// persistence is disabled and the directory contents remain the source
// of truth.
func Discover(dir string) (*Catalog, error) {
	cat := &Catalog{
		dbPath:    dir,
		discovery: true,
	}
	if err := cat.rediscoverLocked(true); err != nil {
		return nil, err
	}
	return cat, nil
}

// footerInfo is what discovery reads from one extent file.
type footerInfo struct {
	relPath  string
	table    string
	id       string
	schema   types.Schema
	rowCount int64
	size     int64
	err      error
}

// cachedFooter is a footer read earlier, valid while the file keeps
// its size and mtime (extent files are immutable once committed).
type cachedFooter struct {
	size  int64
	mtime int64
	info  footerInfo
}

// rediscoverLocked rebuilds the catalog from the directory when either
// directory's stamp moved or has not settled (force skips the check).
// Footers of files seen before are reused, so a refresh reads only new
// files. Caller holds c.mu (or owns c exclusively, as Discover does).
func (c *Catalog) rediscoverLocked(force bool) error {
	dir := c.dbPath
	at := time.Now()
	stamps := [2]fileStamp{statStamp(dir), statStamp(filepath.Join(dir, "extents"))}
	if !force && stamps == c.dirSeen && stamps[0].settled(c.seenAt) &&
		(!stamps[1].ok || stamps[1].settled(c.seenAt)) {
		return nil
	}

	var files []string
	for _, pattern := range []string{"*.vtx", filepath.Join("extents", "*.vtx")} {
		m, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return fmt.Errorf("glob %s: %w", pattern, err)
		}
		files = append(files, m...)
	}
	sort.Strings(files) // deterministic table/extent order

	infos := make([]footerInfo, len(files))
	fresh := make(map[string]cachedFooter, len(files))
	var misses []int
	var missStat []os.FileInfo
	for i, path := range files {
		fi, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				infos[i].table = "" // vanished since the glob: skip
				continue
			}
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if cf, ok := c.footers[path]; ok && cf.size == fi.Size() && cf.mtime == fi.ModTime().UnixNano() {
			infos[i] = cf.info
			fresh[path] = cf
			continue
		}
		misses = append(misses, i)
		missStat = append(missStat, fi)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for _, i := range misses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			infos[i] = readExtentFooter(dir, files[i])
		}(i)
	}
	wg.Wait()
	for k, i := range misses {
		info := infos[i]
		if info.err != nil && !force && os.IsNotExist(info.err) {
			infos[i] = footerInfo{} // removed between stat and read
			continue
		}
		if info.err == nil {
			fresh[files[i]] = cachedFooter{size: missStat[k].Size(), mtime: missStat[k].ModTime().UnixNano(), info: info}
		}
	}

	name := filepath.Base(dir)
	if abs, err := filepath.Abs(dir); err == nil {
		name = filepath.Base(abs) // -db . names the directory, not "."
	}
	db := &Database{
		Name:   name,
		Tables: make(map[string]*Table),
	}
	for _, info := range infos {
		if info.err != nil {
			return fmt.Errorf("extent %s: %w", info.relPath, info.err)
		}
		if info.table == "" {
			continue // filename outside the convention: skipped, warned by caller via Verbose if desired
		}
		tbl, ok := db.Tables[info.table]
		if !ok {
			tbl = &Table{
				Name:    info.table,
				Schema:  info.schema,
				Created: time.Now().UTC(),
			}
			db.Tables[info.table] = tbl
		} else {
			merged, err := mergeSchemas(tbl.Schema, info.schema)
			if err != nil {
				return fmt.Errorf("table %q: extent %s: %w", info.table, info.relPath, err)
			}
			tbl.Schema = timeReceivedLast(merged)
		}
		// Zero-row extents only assert a table's existence and schema
		// (see CreateTable in discovery mode): never scanned.
		if info.rowCount > 0 {
			tbl.Extents = append(tbl.Extents, ExtentEntry{
				ID:        info.id,
				FilePath:  info.relPath,
				RowCount:  info.rowCount,
				SizeBytes: info.size,
			})
		}
	}
	if c.Database != nil {
		db.Version = c.Database.Version + 1
	}
	c.Database = db
	c.footers = fresh
	c.dirSeen, c.seenAt = stamps, at
	return nil
}

// readExtentFooter opens one extent file and extracts table name,
// schema, and row count.
func readExtentFooter(dir, path string) (info footerInfo) {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	info.relPath = filepath.ToSlash(rel)
	info.table, info.id = parseExtentName(filepath.Base(path))
	if info.table == "" {
		return
	}

	// Only the footer is needed: read it in place rather than loading
	// the file (a whole database's extents were read at every start).
	f, err := os.Open(path)
	if err != nil {
		info.err = err
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		info.err = err
		return
	}
	info.size = st.Size()

	vf, err := vortex.Open(f, info.size)
	if err != nil {
		info.err = fmt.Errorf("open vortex: %w", err)
		return
	}
	dt := vf.DType()
	if dt == nil || dt.Kind != vortex.DTypeStruct {
		info.err = fmt.Errorf("file schema is not a struct")
		return
	}
	if DiscoveredSchemaFn == nil {
		info.err = fmt.Errorf("catalog.DiscoveredSchemaFn not registered")
		return
	}
	for i, name := range dt.FieldNames {
		info.schema.Columns = append(info.schema.Columns, types.Column{
			Name: name,
			Type: DiscoveredSchemaFn(dt.FieldTypes[i]),
		})
	}
	if lay := vf.Layout(); lay != nil {
		info.rowCount = int64(lay.RowCount)
	}
	return
}

// parseExtentName splits <Table>_<hexid>.vtx. Table names may contain
// underscores; the id is the final all-hex segment. Files outside the
// convention return an empty table name and are skipped.
func parseExtentName(base string) (table, id string) {
	name := strings.TrimSuffix(base, ".vtx")
	if name == base {
		return "", ""
	}
	i := strings.LastIndexByte(name, '_')
	if i <= 0 || i == len(name)-1 {
		return "", ""
	}
	suffix := name[i+1:]
	for _, c := range suffix {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", ""
		}
	}
	return name[:i], suffix
}

// TimeReceivedColumn is the engine-stamped ingest-time column. A
// table's schema always ends with it: positional ingest (CSV, inline)
// supplies every other column in order and leaves this one to the
// engine. Extents written before a column was added store it earlier
// in the file; scans resolve columns by name, so only the logical
// order matters.
const TimeReceivedColumn = "_TimeReceived"

// timeReceivedLast returns schema with TimeReceivedColumn, if present,
// moved to the end.
func timeReceivedLast(schema types.Schema) types.Schema {
	n := len(schema.Columns)
	i := schema.ColumnIndex(TimeReceivedColumn)
	if i < 0 || i == n-1 {
		return schema
	}
	cols := make([]types.Column, 0, n)
	cols = append(cols, schema.Columns[:i]...)
	cols = append(cols, schema.Columns[i+1:]...)
	cols = append(cols, schema.Columns[i])
	return types.Schema{Columns: cols}
}

// mergeSchemas unions columns across extents of one table (schema
// evolution: later extents may add columns). A column appearing with
// two different types is an error — discovery cannot arbitrate.
func mergeSchemas(a, b types.Schema) (types.Schema, error) {
	merged := a
	index := make(map[string]int, len(a.Columns))
	for i, col := range a.Columns {
		index[col.Name] = i
	}
	for _, col := range b.Columns {
		if i, ok := index[col.Name]; ok {
			if merged.Columns[i].Type != col.Type {
				return types.Schema{}, fmt.Errorf("column %q type conflict: %v vs %v",
					col.Name, merged.Columns[i].Type, col.Type)
			}
			continue
		}
		merged.Columns = append(merged.Columns, col)
	}
	return merged, nil
}

// IsDiscovery reports whether this catalog was built by directory
// discovery (no catalog.json; files are the source of truth).
func (c *Catalog) IsDiscovery() bool {
	return c.discovery
}

// ParseExtentNameForTest exposes the filename convention for tests.
func ParseExtentNameForTest(base string) (table, id string) {
	return parseExtentName(base)
}
