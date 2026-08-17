package engine

// shareddict.go — database-wide (not per-extent) shared dictionary for
// low-cardinality string columns, stored as ordinary rows in a real
// table (_Dictionaries) rather than a bespoke sidecar file format.
//
// HISTORY. An earlier version of this feature used a hand-rolled
// per-(table,column) file format: one .dict data file, one
// .dict.count commit marker, one .dict.lock file, each triple crash-
// safe via its own truncate-then-atomic-rename protocol. That worked,
// but was flagged directly by real use: a live scope accumulated 144
// sidecar files, most of them useless (a structurally-unique column
// like Id got its own dictionary with zero deduplication value), and
// the ones that WERE useless were invisible to the query engine —
// nothing could ask "which dictionaries have no corresponding active
// table" as a query, which is exactly why a batch of orphaned files
// from a deleted staging table went undetected until manually
// inspected. Rebuilt here as a real table for one direct reason: a
// dictionary entry is structurally just a row (TableName, ColumnName,
// Code, Value), append-only, same shape as every other table in a
// scope — there was never a reason it needed a second, bespoke
// storage implementation duplicating crash-safety logic (truncate-to-
// committed-length, write-to-temp-then-rename) this codebase already
// gets for free from ordinary extent writing.
//
// WHAT CARRIES OVER, WHAT DOESN'T.
//   - Growth model (monotonic, append-only, capped at sharedDictCap,
//     not a sticky per-extent decision) is unchanged — see
//     resolveDictDecisions in vortex_bridge.go.
//   - Cross-writer coordination for code assignment still needs a real
//     lock (two writers racing to extend the same dictionary could
//     both assign the same new code to different values) — still
//     flock-based, but now ONE lock file for the whole scope
//     (_dictionaries.lock) instead of one per table+column: the
//     earlier design's per-column locks bought finer-grained
//     concurrency than this workload's actual write pattern (a
//     session's batch touching several tables/columns at once) needed,
//     at the cost of exactly the file-count proliferation this
//     redesign exists to fix.
//   - Crash safety no longer needs its own protocol at all — writing
//     new dictionary rows goes through ordinary SaveExtent, which is
//     already crash-safe (atomic rename) for every other table.
//   - Same-process staleness detection (a long-lived Engine writing
//     more dictionary entries after already caching an older
//     snapshot — see storage.go's post-write cache refresh) is
//     unchanged and still required, not optional.
//   - Cross-process/cross-engine LIVE staleness detection (re-checking
//     an on-disk commit marker on every single read access) is
//     deliberately NOT carried over. That was cheap against a 4-byte
//     count file; against a real table it would mean re-scanning
//     _Dictionaries on every access, which doesn't stay cheap as a
//     scope grows. Dictionaries now inherit the SAME staleness model
//     every other table in this catalog already has (a long-lived
//     Engine's view is captured at first access and doesn't
//     auto-refresh from a different Engine's concurrent writes without
//     restarting) — a known, already-documented, already-accepted
//     limitation elsewhere in this codebase, not a new one introduced
//     here. If that gap ever gets a general fix, dictionaries benefit
//     automatically rather than needing their own parallel fix.
//
// SELF-REFERENCE. _Dictionaries' own TableName/ColumnName/Value
// columns must NEVER be dictionary-encoded — resolveDictDecisions
// excludes this table by name, unconditionally, before any other
// check. Without that exclusion, writing a new dictionary entry would
// try to dictionary-encode _Dictionaries' own write, which would call
// back into this same machinery (and the same, non-reentrant lock) —
// not a performance concern but a genuine deadlock/infinite-recursion
// risk, verified directly (see shareddict_test.go) rather than assumed
// safe by the exclusion existing.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	vortex "github.com/LaurieRhodes/vortex-go"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// sharedDictCap is the maximum number of distinct values a shared
// dictionary is allowed to grow to before columns fall back to flat
// encoding. Matches the PTypeU16 boundary already used for per-extent
// dictionary code width elsewhere (vortex-go's writeSharedDictColumn),
// so a saturated dictionary's codes still fit in 2 bytes.
const sharedDictCap = 65536

// dictionariesTableName is the one reserved, engine-owned table that
// holds every table+column's dictionary entries for the whole scope.
const dictionariesTableName = "_Dictionaries"

func dictionariesSchema() types.Schema {
	return types.Schema{Columns: []types.Column{
		{Name: "TableName", Type: types.TypeString},
		{Name: "ColumnName", Type: types.TypeString},
		{Name: "Code", Type: types.TypeLong},
		{Name: "Value", Type: types.TypeString},
		{Name: "LastTouchedAt", Type: types.TypeDatetime},
		{Name: "Provenance", Type: types.TypeString},
	}}
}

// sharedDict is the in-memory, loaded view of one table+column's
// database-wide dictionary.
type sharedDict struct {
	values []string
	codeOf map[string]uint32
}

// dictionariesLockPath is the single, scope-wide lock file
// coordinating _Dictionaries code assignment across every table and
// column — not one lock file per table+column (see file-level doc
// comment for why that changed).
func dictionariesLockPath(dbPath string) string {
	return filepath.Join(dbPath, "_dictionaries.lock")
}

// withDictLock runs fn while holding an exclusive, cross-process flock
// on the scope's single dictionaries lock file — a real lock is
// necessary here: extent files avoid this problem via globally-unique
// filenames per writer, and discovery mode has no catalog.json to
// coordinate through either, but _Dictionaries code assignment is a
// genuinely shared, mutable decision every writer must serialize
// against.
func withDictLock(dbPath string, fn func() error) error {
	lockPath := dictionariesLockPath(dbPath)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open dictionaries lock file: %w", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquire dictionaries lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	return fn()
}

// dictionariesActiveExtentFiles globs _Dictionaries' active (non-
// superseded) extent files DIRECTLY from disk, deliberately bypassing
// e.Catalog.GetTable(dictionariesTableName).Extents.
//
// Why bypass the catalog here specifically. catalog.Discover captures
// a table's extent list ONCE, at Engine-creation time, and doesn't
// auto-refresh to see extents a DIFFERENT Engine committed since — a
// known, already-accepted limitation for ordinary querying (a stale
// view of Tasks/Findings just means "restart to see the update").
// Inside extendTableDict's lock, staleness isn't a visibility
// inconvenience — it's a correctness bug: two writers both holding a
// lock meant to serialize code assignment, but each computing
// nextCode from ITS OWN stale, possibly-empty view of _Dictionaries,
// will both assign the same new code to different values. Confirmed
// live before fixing: 4 concurrent writer Engines, each independently
// extending Host's dictionary, collapsed to 1 group in a `summarize by
// `Host` instead of 4 — every writer's dictionary read was seeing an
// empty or stale table, so every writer assigned code 0 to its own
// first-seen value, silently colliding. The old, sidecar-file version
// of this feature never hit this because it read/wrote raw files
// directly, never through the catalog's cached extent list at all —
// this redesign inherited the gap by routing through the query engine,
// so the fix is to route around the catalog specifically for this one
// lock-protected read, matching the same direct-glob pattern
// compact.go's findActiveShellFiles already uses for an analogous
// reason (finding files the catalog doesn't currently know about).
func dictionariesActiveExtentFiles(dbPath string) ([]string, error) {
	var files []string
	for _, pattern := range []string{
		filepath.Join(dbPath, dictionariesTableName+"_*.vtx"),
		filepath.Join(dbPath, "extents", dictionariesTableName+"_*.vtx"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			rel, err := filepath.Rel(dbPath, m)
			if err != nil {
				rel = filepath.Base(m)
			}
			files = append(files, filepath.ToSlash(rel))
		}
	}
	return files, nil
}

// loadTableDict reads every current dictionary entry for one
// table+column from _Dictionaries, scanning its ACTUAL on-disk extent
// files directly (see dictionariesActiveExtentFiles) rather than
// trusting the calling Engine's possibly-stale Catalog view. A
// missing _Dictionaries table (no dictionary has ever been created in
// this scope) or no matching rows is not an error — it returns a
// valid, empty dictionary.
func loadTableDict(e *Engine, tableName, columnName string) (*sharedDict, error) {
	dbPath := e.Catalog.DatabasePath()
	files, err := dictionariesActiveExtentFiles(dbPath)
	if err != nil {
		return nil, fmt.Errorf("glob %s extents: %w", dictionariesTableName, err)
	}
	if len(files) == 0 {
		return &sharedDict{codeOf: make(map[string]uint32)}, nil
	}

	schema := dictionariesSchema()
	type entry struct {
		code  int64
		value string
	}
	var entries []entry
	cols := []string{"TableName", "ColumnName", "Code", "Value"}
	for _, relPath := range files {
		// The direct glob above finds every _Dictionaries_*.vtx file,
		// including the zero-row schema-shell extent persistDiscoverySchema
		// writes when the table is first bootstrapped — the catalog's
		// own extent list would have excluded that automatically, but
		// bypassing the catalog (the whole point of this function, see
		// dictionariesActiveExtentFiles) means doing that filtering
		// here instead. A zero-row Vortex extent genuinely has no data
		// segments for any column, so scanning it for actual rows
		// errors rather than yielding an empty result — ScanExtentInfo
		// already tolerates exactly this (it swallows the same
		// underlying scan failure and reports RowCount: 0), so use it
		// as a cheap pre-check rather than hitting the same error via
		// ScanExtent directly.
		info, err := e.ScanExtentInfo(relPath)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", relPath, err)
		}
		if info.RowCount == 0 {
			continue
		}
		data, err := e.ScanExtent(dictionariesTableName, relPath, &schema, cols, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", dictionariesTableName, err)
		}
		for _, row := range data.Rows {
			if fmt.Sprintf("%v", row[0]) != tableName || fmt.Sprintf("%v", row[1]) != columnName {
				continue
			}
			code, _ := row[2].(int64)
			entries = append(entries, entry{code: code, value: fmt.Sprintf("%v", row[3])})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].code < entries[j].code })

	sd := &sharedDict{
		values: make([]string, 0, len(entries)),
		codeOf: make(map[string]uint32, len(entries)),
	}
	for _, en := range entries {
		// Codes are contiguous 0..N-1 by construction (extendTableDict
		// always assigns len(current)+i for new entries) — indexing
		// directly rather than trusting that blindly, so a corrupt or
		// hand-edited _Dictionaries with a gap fails safely (a wrong
		// value at worst) instead of panicking on an out-of-range code
		// later during decode.
		idx := int(en.code)
		for len(sd.values) <= idx {
			sd.values = append(sd.values, "")
		}
		sd.values[idx] = en.value
		sd.codeOf[en.value] = uint32(en.code)
	}
	return sd, nil
}

// extendTableDict is the single, lock-protected entry point for the
// write path (resolveDictDecisions in vortex_bridge.go): given the
// full set of distinct string values one extent's rows contain for a
// column, it reloads the CURRENT dictionary fresh under the scope's
// exclusive lock (never trusting a snapshot the caller loaded before
// acquiring the lock — another writer may have extended it since),
// determines which values are genuinely new, and either appends and
// commits them (ordinary SaveExtent — already crash-safe) if the
// resulting total stays within sharedDictCap, or leaves the dictionary
// untouched and returns capped=true, meaning this extent should fall
// back to flat encoding for this column.
func extendTableDict(e *Engine, tableName, columnName string, distinctValues map[string]struct{}) (result *sharedDict, capped bool, err error) {
	lockErr := withDictLock(e.Catalog.DatabasePath(), func() error {
		sd, loadErr := loadTableDict(e, tableName, columnName)
		if loadErr != nil {
			return loadErr
		}

		var newValues []string
		newlyIntroducesEmpty := false
		for v := range distinctValues {
			if _, already := sd.codeOf[v]; !already {
				if v == "" {
					newlyIntroducesEmpty = true
					continue // added first, below — not here, order matters
				}
				newValues = append(newValues, v)
			}
		}
		// "" must land at the LOWEST new code whenever it's genuinely
		// new, not wherever map iteration happened to place it —
		// map iteration order is randomized by the Go runtime, so
		// relying on it was the actual bug in an earlier version of
		// this fix: verified live that "" landed at code 1 instead of
		// 0 on a real write, with real rows written as null silently
		// decoding as whatever value legitimately held code 0 instead
		// (the exact corruption this whole mechanism exists to
		// prevent). Prepending explicitly, rather than sorting the
		// whole slice, keeps every OTHER new value's relative order
		// exactly as map iteration produced it — irrelevant for
		// correctness (any order is fine for values other than ""),
		// but keeps this diff minimal and the reasoning about it
		// simple: "" is special-cased, nothing else is.
		if newlyIntroducesEmpty {
			newValues = append([]string{""}, newValues...)
		}

		if len(sd.values)+len(newValues) > sharedDictCap {
			result = sd
			capped = true
			return nil
		}
		if len(newValues) == 0 {
			result = sd
			capped = false
			return nil
		}

		tableDef := e.Catalog.GetTable(dictionariesTableName)
		if tableDef == nil {
			schema := dictionariesSchema()
			if createErr := e.Catalog.CreateTable(dictionariesTableName, schema); createErr != nil {
				return fmt.Errorf("create %s: %w", dictionariesTableName, createErr)
			}
			if persistErr := e.persistDiscoverySchema(dictionariesTableName, schema); persistErr != nil {
				return fmt.Errorf("persist %s schema: %w", dictionariesTableName, persistErr)
			}
			tableDef = e.Catalog.GetTable(dictionariesTableName)
		}

		newSD := &sharedDict{
			values: append(append([]string(nil), sd.values...), newValues...),
			codeOf: make(map[string]uint32, len(sd.values)+len(newValues)),
		}
		for i, v := range sd.values {
			newSD.codeOf[v] = uint32(i)
		}

		now := time.Now().UTC()
		nextCode := int64(len(sd.values))
		newRows := make([]types.Row, 0, len(newValues))
		for i, v := range newValues {
			code := nextCode + int64(i)
			newRows = append(newRows, types.Row{tableName, columnName, code, v, now, "shared-dict auto-assign"})
			newSD.codeOf[v] = uint32(code)
		}

		if _, saveErr := e.flushBatch(dictionariesTableName, tableDef, newRows); saveErr != nil {
			return fmt.Errorf("append dictionary entries: %w", saveErr)
		}

		result = newSD
		capped = false
		return nil
	})
	if lockErr != nil {
		return nil, false, lockErr
	}
	return result, capped, nil
}

// dictCodePType returns the narrowest unsigned integer PType that can
// hold codes for a dictionary of the given size, matching the width
// boundaries vortex-go's own per-extent dictionary encoding already
// uses (writeSharedDictColumn) for consistency.
func dictCodePType(dictSize int) vortex.PType {
	switch {
	case dictSize <= 256:
		return vortex.PTypeU8
	case dictSize <= 65536:
		return vortex.PTypeU16
	default:
		return vortex.PTypeU32
	}
}
