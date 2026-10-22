// Package catalog manages the extent catalog: table schemas, extent metadata,
// and the mapping from table names to Vortex files.
//
// An on-disk database has no metadata file: the directory of
// self-describing .vtx extents is the database, and the catalog is built
// from their footers (discover.go) and kept current with Refresh
// (freshness.go). Engine metadata (shared dictionaries, stored functions,
// materialized views) lives in KQL system tables in the same directory.
// A catalog.json left by older versions is ignored.
//
// NewMemory catalogs hold everything in memory and are not discovered.
package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// Catalog is the in-memory extent catalog backed by JSON persistence.
type Catalog struct {
	mu        sync.RWMutex
	dbPath    string // Root path for the database
	discovery bool   // on-disk database: files are the source of truth (false only for NewMemory)
	Database  *Database

	// lastAllocatedID is the highest extent id handed out by
	// AllocateExtentID in this process (see there).
	lastAllocatedID uint64

	// Change detection for Refresh (freshness.go): the directory
	// stamps last discovered, when they were observed, and the
	// footers read so far.
	dirSeen [2]fileStamp
	seenAt  time.Time
	footers map[string]cachedFooter // path -> footer
}

// Database represents a single database with tables and extents.
type Database struct {
	Name    string            `json:"name"`
	Tables  map[string]*Table `json:"tables"`
	Version int64             `json:"version"` // Incremented on every mutation
}

// Table represents a table definition with schema and extent references.
type Table struct {
	Name    string        `json:"name"`
	Schema  types.Schema  `json:"schema"`
	Extents []ExtentEntry `json:"extents"`
	Created time.Time     `json:"created"`
}

// ExtentEntry is metadata for a single extent (Vortex file).
type ExtentEntry struct {
	ID          string                 `json:"id"`
	FilePath    string                 `json:"file_path"` // Relative to database extents dir
	RowCount    int64                  `json:"row_count"`
	SizeBytes   int64                  `json:"size_bytes"`
	CreatedAt   time.Time              `json:"created_at"`
	ColumnStats map[string]ColumnStats `json:"column_stats,omitempty"`
}

// ColumnStats holds cached min/max statistics for extent-level pruning.
type ColumnStats struct {
	Min       interface{} `json:"min,omitempty"`
	Max       interface{} `json:"max,omitempty"`
	NullCount int64       `json:"null_count,omitempty"`
}

// Open opens the database directory at dbPath, creating it if absent,
// and builds the catalog from its extent files.
func Open(dbPath string) (*Catalog, error) {
	if err := os.MkdirAll(dbPath, 0755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	return Discover(dbPath)
}

// save records a mutation. Nothing is written: on disk, the extent
// files are the metadata, and each was committed by its own atomic
// rename before it was registered here.
func (c *Catalog) save() error {
	c.Database.Version++
	return nil
}

// --- Table Operations ---

// CreateTable creates a new table. Returns error if it already exists.
func (c *Catalog) CreateTable(name string, schema types.Schema) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.Database.Tables[name]; exists {
		return fmt.Errorf("table %q already exists", name)
	}

	c.Database.Tables[name] = &Table{
		Name:    name,
		Schema:  schema,
		Created: time.Now().UTC(),
	}
	return c.save()
}

// CreateMergeTable creates a table if it doesn't exist, or adds missing columns.
func (c *Catalog) CreateMergeTable(name string, schema types.Schema) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	existing, exists := c.Database.Tables[name]
	if !exists {
		c.Database.Tables[name] = &Table{
			Name:    name,
			Schema:  schema,
			Created: time.Now().UTC(),
		}
		return c.save()
	}

	// Merge: add columns that don't exist yet
	for _, newCol := range schema.Columns {
		if existing.Schema.ColumnIndex(newCol.Name) < 0 {
			existing.Schema.Columns = append(existing.Schema.Columns, newCol)
		}
	}
	return c.save()
}

// DropTable removes a table and deletes its extent files.
// DropTable removes a table from catalog state only — it does NOT
// touch any file on disk. It used to: this function directly
// os.Remove'd every row-bearing extent it knew about, completely
// bypassing the engine layer's dropTableComplete (which archives
// files instead of deleting them, specifically so .drop table is
// recoverable rather than permanent). That bypass meant the archive
// fix was incomplete and actively misleading — it appeared to work
// for shell/superseded files while this function still silently,
// permanently destroyed the actual row-bearing data extents through
// its own separate code path. Found via a failing test the very fix
// this comment describes was meant to catch (TestDropTableRemovesEverything:
// "no such file or directory" on the archive rename, because this
// function had already deleted the file moments earlier) — not
// caught by inspection, caught by the test actually running against
// the real sequence of operations.
//
// File-level work (finding every matching file, including ones this
// function's own Extents list doesn't know about — shells,
// .superseded leftovers — and archiving all of them) belongs entirely
// to dropTableComplete now. Two separate concerns (catalog state,
// disk files) each doing only their own job, not one silently
// stepping on the other's responsibility.
func (c *Catalog) DropTable(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.Database.Tables[name]; !exists {
		return fmt.Errorf("table %q does not exist", name)
	}

	delete(c.Database.Tables, name)
	return c.save()
}

// GetTable returns a table definition, or nil if not found.
func (c *Catalog) GetTable(name string) *Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Database.Tables[name]
}

// ListTables returns all user table names — excludes reserved,
// engine-owned system tables (currently just "_Dictionaries", the
// database-wide shared-dictionary store; see engine/shareddict.go).
// A system table is still fully queryable and usable by explicit name
// (GetTable and a direct `_Dictionaries | ...` query both work
// normally) — this only hides it from generic enumeration (.show
// tables, the search operator's "search every table" default,
// anything else that treats ListTables as "the user's tables").
func (c *Catalog) ListTables() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	names := make([]string, 0, len(c.Database.Tables))
	for name := range c.Database.Tables {
		if IsSystemTable(name) {
			continue
		}
		names = append(names, name)
	}
	return names
}

// IsSystemTable reports whether name is a reserved, engine-owned
// table that should be excluded from generic table enumeration.
func IsSystemTable(name string) bool {
	switch name {
	case "_Dictionaries", "_Functions", "_MaterializedViews":
		return true
	default:
		return false
	}
}

// ListAllTables returns every table name, INCLUDING system tables —
// unlike ListTables, which deliberately excludes them from generic
// user-facing enumeration. Exists specifically for maintenance
// operations that need to find every table with extents worth
// compacting, not just the ones a user would want to see listed.
// Found live, not hypothetical: a scope had 232 _Dictionaries extents
// (every other table already correctly compacted to 1) because
// nothing iterating .show tables' output could ever have known
// _Dictionaries existed — it was hidden from exactly the enumeration
// a "compact everything" workflow would naturally use as its source
// of truth. "Remember to also compact _Dictionaries separately" is
// discipline, and had already failed twice by the time this was
// added; a maintenance-specific listing that structurally can't miss
// it is the fix.
func (c *Catalog) ListAllTables() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	names := make([]string, 0, len(c.Database.Tables))
	for name := range c.Database.Tables {
		names = append(names, name)
	}
	return names
}

// --- Extent Operations ---

// AddExtent registers a new extent for a table.
func (c *Catalog) AddExtent(tableName string, entry ExtentEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	table, exists := c.Database.Tables[tableName]
	if !exists {
		return fmt.Errorf("table %q does not exist", tableName)
	}

	table.Extents = append(table.Extents, entry)
	return c.save()
}

// AllocateExtentID returns an in-memory catalog's extent id (8 lowercase hex
// digits) not used by any extent of any table, including the
// _Dictionaries system table, and not handed out before in this
// process.
//
// The id used to be Version+1. A flush writes its data extent and its
// _Dictionaries extent before either registration advances Version,
// so the two received the same id; .drop extent, which resolves an id
// across tables, could then remove the dictionary extent instead of
// the data extent.
func (c *Catalog) AllocateExtentID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := uint64(c.Database.Version) + 1
	if c.lastAllocatedID >= next {
		next = c.lastAllocatedID + 1
	}
	for _, t := range c.Database.Tables {
		for _, ext := range t.Extents {
			if n, err := strconv.ParseUint(ext.ID, 16, 64); err == nil && n >= next {
				next = n + 1
			}
		}
	}
	c.lastAllocatedID = next
	return fmt.Sprintf("%08x", next)
}

// ExtentTables returns the names of the tables holding an extent with
// the given id (more than one only in databases written before
// AllocateExtentID existed).
func (c *Catalog) ExtentTables(extentID string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []string
	for name, t := range c.Database.Tables {
		for _, ext := range t.Extents {
			if ext.ID == extentID {
				out = append(out, name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// RemoveExtentFrom removes extentID from the named table only.
func (c *Catalog) RemoveExtentFrom(tableName, extentID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	table, ok := c.Database.Tables[tableName]
	if !ok {
		return fmt.Errorf("table %q does not exist", tableName)
	}
	for i, ext := range table.Extents {
		if ext.ID == extentID {
			table.Extents = append(table.Extents[:i], table.Extents[i+1:]...)
			_ = os.Remove(filepath.Join(c.dbPath, ext.FilePath))
			return c.save()
		}
	}
	return fmt.Errorf("extent %q not found in table %q", extentID, tableName)
}

// RemoveExtent removes an extent by ID from the first table holding
// it. .drop extent uses ExtentTables and RemoveExtentFrom instead, so
// an id shared between tables is never resolved by guessing.
func (c *Catalog) RemoveExtent(extentID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for tableName, table := range c.Database.Tables {
		for i, ext := range table.Extents {
			if ext.ID == extentID {
				// Remove from slice
				table.Extents = append(table.Extents[:i], table.Extents[i+1:]...)

				// Delete file
				fullPath := filepath.Join(c.dbPath, ext.FilePath)
				_ = os.Remove(fullPath)

				if err := c.save(); err != nil {
					return "", err
				}
				return tableName, nil
			}
		}
	}
	return "", fmt.Errorf("extent %q not found", extentID)
}

// ReplaceExtents atomically replaces old extents with new ones for a table.
// Used by compaction.
func (c *Catalog) ReplaceExtents(tableName string, oldIDs []string, newEntries []ExtentEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	table, exists := c.Database.Tables[tableName]
	if !exists {
		return fmt.Errorf("table %q does not exist", tableName)
	}

	// Build set of IDs to remove
	removeSet := make(map[string]bool)
	for _, id := range oldIDs {
		removeSet[id] = true
	}

	// Filter out old extents
	kept := make([]ExtentEntry, 0, len(table.Extents))
	var toDelete []string
	for _, ext := range table.Extents {
		if removeSet[ext.ID] {
			toDelete = append(toDelete, filepath.Join(c.dbPath, ext.FilePath))
		} else {
			kept = append(kept, ext)
		}
	}

	// Add new extents
	table.Extents = append(kept, newEntries...)

	if err := c.save(); err != nil {
		return err
	}

	// Delete old files after catalog is saved
	for _, path := range toDelete {
		_ = os.Remove(path)
	}
	return nil
}

// TableRowCount returns the total row count from catalog metadata.
// Zero-I/O — no files opened.
func (c *Catalog) TableRowCount(tableName string) (int64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	table, exists := c.Database.Tables[tableName]
	if !exists {
		return 0, fmt.Errorf("table %q does not exist", tableName)
	}

	var total int64
	for _, ext := range table.Extents {
		total += ext.RowCount
	}
	return total, nil
}

// ExtentsDir returns the path to the extents directory.
func (c *Catalog) ExtentsDir() string {
	return filepath.Join(c.dbPath, "extents")
}

// DatabasePath returns the root database path.
func (c *Catalog) DatabasePath() string {
	return c.dbPath
}

// NewMemory creates a transient in-memory catalog with no disk persistence.
// Tables and extents exist only for the lifetime of the process.
func NewMemory() *Catalog {
	return &Catalog{
		dbPath: "",
		Database: &Database{
			Name:   ":memory:",
			Tables: make(map[string]*Table),
		},
	}
}

// IsMemory returns true if this catalog has no disk backing.
func (c *Catalog) IsMemory() bool {
	return c.dbPath == ""
}
