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
	"bytes"
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
	var files []string
	for _, pattern := range []string{"*.vtx", filepath.Join("extents", "*.vtx")} {
		m, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return nil, fmt.Errorf("glob %s: %w", pattern, err)
		}
		files = append(files, m...)
	}
	sort.Strings(files) // deterministic table/extent order

	cat := &Catalog{
		dbPath:    dir,
		discovery: true,
		Database: &Database{
			Name:   filepath.Base(dir),
			Tables: make(map[string]*Table),
		},
	}
	if len(files) == 0 {
		return cat, nil
	}

	type footerInfo struct {
		relPath  string
		table    string
		id       string
		schema   types.Schema
		rowCount int64
		size     int64
		err      error
	}

	infos := make([]footerInfo, len(files))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i, path := range files {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			infos[i] = readExtentFooter(dir, path)
		}(i, path)
	}
	wg.Wait()

	for _, info := range infos {
		if info.err != nil {
			return nil, fmt.Errorf("extent %s: %w", info.relPath, info.err)
		}
		if info.table == "" {
			continue // filename outside the convention: skipped, warned by caller via Verbose if desired
		}
		tbl, ok := cat.Database.Tables[info.table]
		if !ok {
			tbl = &Table{
				Name:    info.table,
				Schema:  info.schema,
				Created: time.Now().UTC(),
			}
			cat.Database.Tables[info.table] = tbl
		} else {
			merged, err := mergeSchemas(tbl.Schema, info.schema)
			if err != nil {
				return nil, fmt.Errorf("table %q: extent %s: %w", info.table, info.relPath, err)
			}
			tbl.Schema = merged
		}
		if info.rowCount > 0 || info.size > 0 {
			tbl.Extents = append(tbl.Extents, ExtentEntry{
				ID:        info.id,
				FilePath:  info.relPath,
				RowCount:  info.rowCount,
				SizeBytes: info.size,
			})
		}
	}

	// Drop schema-only tables' zero-row extents from scan lists while
	// keeping the tables themselves: a zero-row extent exists solely to
	// assert a schema (see CreateTable in discovery mode).
	for _, tbl := range cat.Database.Tables {
		kept := tbl.Extents[:0]
		for _, e := range tbl.Extents {
			if e.RowCount > 0 {
				kept = append(kept, e)
			}
		}
		tbl.Extents = kept
	}

	return cat, nil
}

// readExtentFooter opens one extent file and extracts table name,
// schema, and row count.
func readExtentFooter(dir, path string) (info struct {
	relPath  string
	table    string
	id       string
	schema   types.Schema
	rowCount int64
	size     int64
	err      error
}) {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	info.relPath = filepath.ToSlash(rel)
	info.table, info.id = parseExtentName(filepath.Base(path))
	if info.table == "" {
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		info.err = err
		return
	}
	info.size = int64(len(data))

	vf, err := vortex.Open(bytes.NewReader(data), int64(len(data)))
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
