package engine

// compact.go — .compact table T (backlog P2 item 10): discovery-mode
// extent compaction, and .gc table T for physical space reclamation.
//
// REVISED DESIGN (2026-08-07). The first version of this file
// physically deleted old extents once the merged replacement was
// written, and had a real, unresolved test failure around that
// deletion step interacting with a long-lived in-process Catalog.
// Rather than debug that further, this rebuilds compaction around a
// pattern real production systems use for exactly this problem:
// Spice AI's Cayenne engine (built on Vortex, the same file format
// this project uses) tracks superseded files in a small separate
// metadata layer and treats physical deletion as a decoupled,
// scheduled background activity — "periodic compaction merges data
// and delete state into fresh snapshots" — rather than something a
// single operation must get atomically right. Real Vortex itself has
// no row/file obsolescence primitive (confirmed against the official
// file-format spec: a minimal, immutable single-file format); this
// kind of supersession always lives one layer up, which is exactly
// where this implementation puts it.
//
// Mechanism: superseding an extent renames it, in place, from
// "T_<id>.vtx" to "T_<id>.vtx.superseded" — a single atomic filesystem
// rename, the same primitive every commit in this codebase already
// uses. Discovery's glob pattern is literally "*.vtx"; a renamed file
// no longer matches it, so exclusion from all future scanning is
// automatic and requires zero changes to the discovery or scan code
// (verified: pkg/catalog/discover.go's glob is exactly "*.vtx").
// Nothing is ever destroyed by .compact itself — a crash or an
// unexpected process exit at any point leaves, at worst, some
// .superseded files sitting harmlessly on disk (wasted space, cleaned
// up by a later .gc pass), never a correctness violation for any
// reader, in this process or any other.
//
// .gc physically removes .superseded files. This is now safe to run
// at any time, with no coordination required, because by the time a
// file has that suffix it is already fully invisible to every normal
// scan path — there is no reader that could possibly be looking at it.
//
// SCHEMA-ONLY SHELLS (2026-08-08). .create table writes a zero-row
// extent purely to assert a table's existence/schema across process
// restarts in discovery mode, which has no catalog.json to remember it
// otherwise (see discovery.go's persistDiscoverySchema). Every table
// therefore starts life with one tiny shell file, permanently — and,
// critically, catalog.Discover deliberately excludes zero-row extents
// from tableDef.Extents (a real extent's own footer already carries
// the same schema, so the shell serves no purpose once real data
// exists), which meant the FIRST version of the compaction-with-
// consolidation logic below only ever merged/superseded row-bearing
// extents and left every table's original shell file behind forever —
// a real, spotted-in-review inconsistency: a "consolidation" operation
// that doesn't consolidate down to the minimum necessary files isn't
// actually consolidating. Fixed: applyCompactTable now also globs for
// a table's active shell file(s) (the same simple TableName_*.vtx
// pattern applyGCTable already uses for .superseded files, just
// without that suffix) and supersedes them alongside the row-bearing
// extents whenever a new extent is written — the new extent, whether
// it holds real rows or itself ends up empty after a Where filter,
// is exactly as self-describing as the shell was, so nothing is lost.

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

const supersededSuffix = ".superseded"

func (e *Engine) applyCompactTable(cmd *parser.CompactTableCmd) (*types.Table, error) {
	if !e.Catalog.IsDiscovery() {
		return nil, fmt.Errorf(".compact is for catalog-free (discovery mode) scopes only — " +
			"catalog-mode databases already have atomic multi-extent merge via .merge table T extents")
	}
	tableDef := e.Catalog.GetTable(cmd.TableName)
	if tableDef == nil {
		return nil, fmt.Errorf("table %q not found", cmd.TableName)
	}

	// Snapshot NOW. Nothing after this point re-reads the extent list.
	// A concurrently-added extent lands after this snapshot is taken
	// and is simply left for the next .compact pass — never touched,
	// never lost, never duplicated by THIS pass.
	snapshot := make([]catalog.ExtentEntry, len(tableDef.Extents))
	copy(snapshot, tableDef.Extents)

	// Find any active (non-superseded) schema-only shell files for this
	// table — present on disk, matching the naming convention, but NOT
	// in tableDef.Extents at all (catalog.Discover excludes zero-row
	// extents from that list on principle, not just as an omission —
	// see the file-level doc comment above). These have zero rows to
	// contribute to the merge, so they're tracked separately from
	// snapshot rather than scanned for data, but they're just as
	// redundant as a row-bearing extent once a new, equally
	// self-describing extent is about to be written, so they get
	// superseded alongside everything else below.
	shellFiles, err := findActiveShellFiles(e.Catalog.DatabasePath(), cmd.TableName, snapshot)
	if err != nil {
		return nil, fmt.Errorf(".compact: finding schema-only shell files: %w", err)
	}

	if compactAfterSnapshotHook != nil {
		compactAfterSnapshotHook()
	}

	// The "nothing to do" short-circuit only applies when there is
	// truly nothing to consolidate: at most one row-bearing extent, no
	// leftover shell files, and no Where clause asking to drop rows.
	// Any one of those makes this pass worth running: a Where clause
	// can still shrink a single extent's row count; a leftover shell
	// file is a permanently-redundant tiny file the moment any
	// row-bearing extent exists, regardless of how many row-bearing
	// extents there are.
	if len(snapshot) <= 1 && len(shellFiles) == 0 && cmd.Where == nil {
		result := types.NewTable("", types.Schema{Columns: []types.Column{
			{Name: "Result", Type: types.TypeString},
			{Name: "ExtentsBefore", Type: types.TypeLong},
			{Name: "ExtentsAfter", Type: types.TypeLong},
		}})
		result.AddRow(types.Row{"already compact", int64(len(snapshot)), int64(len(snapshot))})
		return result, nil
	}
	if len(snapshot) == 0 && len(shellFiles) == 0 {
		result := types.NewTable("", types.Schema{Columns: []types.Column{
			{Name: "Result", Type: types.TypeString},
			{Name: "ExtentsBefore", Type: types.TypeLong},
			{Name: "ExtentsAfter", Type: types.TypeLong},
		}})
		result.AddRow(types.Row{"already compact", int64(0), int64(0)})
		return result, nil
	}

	allCols := make([]string, len(tableDef.Schema.Columns))
	for i, c := range tableDef.Schema.Columns {
		allCols[i] = c.Name
	}

	var allRows []types.Row
	for _, ext := range snapshot {
		data, err := e.ScanExtent(tableDef.Name, ext.FilePath, &tableDef.Schema, allCols, nil, 0)
		if err != nil {
			return nil, fmt.Errorf(".compact: reading extent %s: %w", ext.ID, err)
		}
		allRows = append(allRows, data.Rows...)
	}
	totalScanned := len(allRows)

	// Apply the optional exclusion predicate — same per-row evalExpr
	// machinery as an ordinary | where operator (see operators.go's
	// applyWhere), including whatever let-bound tables are active from
	// an enclosing compound statement (e.g. `let superseded = Edges |
	// where Rel == "supersedes" | project Dst; .compact table Findings
	// where Id !in (superseded)`). Rows for which the predicate errors
	// or evaluates non-true are dropped, exactly as | where treats them.
	if cmd.Where != nil {
		kept := allRows[:0]
		for _, row := range allRows {
			val, err := evalExpr(cmd.Where, &tableDef.Schema, row)
			if err != nil {
				return nil, fmt.Errorf(".compact: where: %w", err)
			}
			if b, ok := val.(bool); ok && b {
				kept = append(kept, row)
			}
		}
		allRows = kept
	}
	rowsDropped := totalScanned - len(allRows)

	// Write the merged (and possibly filtered) extent. Nothing about
	// the old extents changes yet — both old and new are simultaneously
	// valid, complete, and independently correct at this point; a crash
	// here just leaves an extra (harmless, ignorable-by-nobody-since-
	// it's-real-data) extent. A zero-row result (every row for this
	// table was obsolete, or there was never any real data — just a
	// shell being consolidated) writes a valid empty extent — the same
	// mechanism persistDiscoverySchema already uses deliberately, and
	// discovery mode's own scan-list building already knows to skip
	// zero-row extents, so this is not a special case downstream. This
	// new extent takes over the schema-assertion role any old shell
	// file was playing, so those are safe to supersede below too.
	newExtentID, err := e.flushBatch(cmd.TableName, tableDef, allRows)
	if err != nil {
		return nil, fmt.Errorf(".compact: writing merged extent: %w", err)
	}

	// Supersede exactly the snapshotted old extents, PLUS the shell
	// files found above — rename, not delete. Update the in-memory
	// Extents list to match immediately, so this same process/session
	// sees consistent results right away without needing to
	// re-discover. Shell files were never in tableDef.Extents, so only
	// the row-bearing ones need that bookkeeping step.
	superseded := 0
	var renameErrs []error
	supersededIDs := make(map[string]bool, len(snapshot))
	for _, ext := range snapshot {
		fullPath := filepath.Join(e.Catalog.DatabasePath(), ext.FilePath)
		if err := os.Rename(fullPath, fullPath+supersededSuffix); err != nil {
			renameErrs = append(renameErrs, fmt.Errorf("%s: %w", ext.ID, err))
			continue
		}
		supersededIDs[ext.ID] = true
		superseded++
	}
	for _, relPath := range shellFiles {
		fullPath := filepath.Join(e.Catalog.DatabasePath(), relPath)
		if err := os.Rename(fullPath, fullPath+supersededSuffix); err != nil {
			renameErrs = append(renameErrs, fmt.Errorf("shell %s: %w", relPath, err))
			continue
		}
		superseded++
	}
	if len(supersededIDs) > 0 {
		kept := tableDef.Extents[:0]
		for _, ext := range tableDef.Extents {
			if !supersededIDs[ext.ID] {
				kept = append(kept, ext)
			}
		}
		tableDef.Extents = kept
	}

	status := "OK"
	if len(renameErrs) > 0 {
		status = fmt.Sprintf(
			"PARTIAL: merged extent written successfully, but %d old file(s) could not be superseded (%v) — "+
				"those rows now exist in BOTH the old and new extents and WILL BE DOUBLE-COUNTED by scans "+
				"until resolved manually. Run .show table %s extents to find them.",
			len(renameErrs), renameErrs, cmd.TableName)
	}

	result := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "Result", Type: types.TypeString},
		{Name: "NewExtentId", Type: types.TypeString},
		{Name: "ExtentsBefore", Type: types.TypeLong},
		{Name: "ExtentsSuperseded", Type: types.TypeLong},
		{Name: "RowsScanned", Type: types.TypeLong},
		{Name: "RowsDropped", Type: types.TypeLong},
		{Name: "RowsPreserved", Type: types.TypeLong},
	}})
	result.AddRow(types.Row{status, newExtentID, int64(len(snapshot) + len(shellFiles)), int64(superseded),
		int64(totalScanned), int64(rowsDropped), int64(len(allRows))})
	return result, nil
}

// findActiveShellFiles globs for a table's active (non-.superseded)
// .vtx files — the same TableName_*.vtx pattern applyGCTable already
// uses for .superseded files, minus that suffix, checked against both
// locations discovery itself globs (root and extents/) — and returns
// the ones NOT already present in knownExtents (relative path match).
// These are exactly the zero-row schema-only shells catalog.Discover
// deliberately drops from tableDef.Extents: every file matching the
// naming convention that IS in tableDef.Extents is row-bearing and
// already accounted for; anything left over after subtracting those
// has zero rows by construction (a row-bearing file with no other
// explanation for being un-tracked would indicate a catalog bug, not
// a shell file — but treating it as "extra, supersede it" is still
// safe either way, since its rows were already re-scanned as part of
// snapshot if and only if it WAS in tableDef.Extents; this function
// only ever returns files that were not).
func findActiveShellFiles(dbPath, tableName string, knownExtents []catalog.ExtentEntry) ([]string, error) {
	known := make(map[string]bool, len(knownExtents))
	for _, ext := range knownExtents {
		known[filepath.ToSlash(ext.FilePath)] = true
	}

	var extra []string
	for _, pattern := range []string{
		filepath.Join(dbPath, tableName+"_*.vtx"),
		filepath.Join(dbPath, "extents", tableName+"_*.vtx"),
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
			rel = filepath.ToSlash(rel)
			if !known[rel] {
				extra = append(extra, rel)
			}
		}
	}
	return extra, nil
}

// findAllTableFiles globs EVERY .vtx-family file matching a table's
// naming convention — active extents (including zero-row schema
// shells, unlike catalog.GetTable(name).Extents, which deliberately
// excludes those) AND .superseded files left over from a prior
// .compact that was never .gc'd — checked against both locations
// discovery itself globs (root and extents/). Used by .drop table,
// which needs to remove EVERYTHING belonging to a table, not just
// what the catalog happens to be tracking; findActiveShellFiles above
// serves a narrower need (compact only cares about ACTIVE files it
// isn't already tracking) and doesn't cover .superseded ones, so this
// is a distinct function rather than a shared one — reusing it here
// would need extra parameters just to turn OFF behavior compact wants
// on.
func findAllTableFiles(dbPath, tableName string) ([]string, error) {
	var files []string
	for _, pattern := range []string{
		filepath.Join(dbPath, tableName+"_*.vtx"),
		filepath.Join(dbPath, tableName+"_*.vtx"+supersededSuffix),
		filepath.Join(dbPath, "extents", tableName+"_*.vtx"),
		filepath.Join(dbPath, "extents", tableName+"_*.vtx"+supersededSuffix),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		// filepath.Glob returns paths already prefixed with dbPath
		// (it was baked into the pattern above) — convert to
		// dbPath-RELATIVE, matching findActiveShellFiles's established
		// convention, so callers can safely filepath.Join(dbPath, ...)
		// without doubling it. Verified live before this conversion
		// was added: dropTableComplete's os.Remove silently no-op'd on
		// a doubled, nonexistent path (/scope/scope/T_xxx.vtx), and
		// the failure was invisible because os.IsNotExist errors are
		// deliberately ignored there (a missing file is the NORMAL
		// case when a glob pattern's later match already got removed
		// by an earlier one) — exactly the kind of silent failure this
		// whole session has been hunting, this time in my own fix.
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

// dropTableComplete removes a table completely FROM DISCOVERY — every
// file matching its naming convention, not just the row-bearing
// extents Catalog.DropTable already knows about — by MOVING them into
// a per-drop archive subdirectory, not deleting them. Found live, not
// hypothetical, in two separate incidents: first, .drop table reported
// success but left a table's zero-row schema-shell extent behind (the
// same file persistDiscoverySchema writes and catalog.Discover
// deliberately excludes from GetTable(name).Extents), so a fresh
// discovery-mode re-open of the same directory still found the
// "dropped" table with zero rows. That was fixed by making this
// function find and remove every matching file, including
// .superseded ones — correctly closing that bug, but by permanent
// os.Remove, which then caused real, irrecoverable data loss when a
// different session reached for .drop table to fix a single row and
// lost an entire table with no way back.
//
// The actual design error in that first fix: it conflated two
// different requirements into one irreversible operation. "Must not
// be discoverable after drop" (the real bug, and the only thing
// .drop table is actually obligated to guarantee) does NOT require
// "bytes must be permanently destroyed" (a much stronger guarantee
// nothing asked for, and the one that turned an administrative
// command into rm -rf with no undo). Archiving satisfies the first
// requirement exactly as completely as deletion does — moved files
// are outside every path catalog.Discover/findAllTableFiles glob
// (root and extents/ only, never .dropped/), so a fresh discovery
// genuinely does not find the table, matching the original bug fix's
// own stated symptom test precisely — while leaving genuine,
// intentional recoverability for exactly the "wrong command reached
// for" case that caused real damage. This is a deliberate, permanent
// design choice for .drop table specifically, not a change to this
// system's actual immutability guarantee: that guarantee has always
// been about the data layer (corrections via supersedes/chosen-over
// edges, never in-place row updates) protecting against normal
// operations going wrong. It was never meant to extend to protecting
// against an explicit schema-level command doing exactly what it
// says — the gap was that .drop table had no safety net at all, not
// that the data layer's own immutability was somehow violated.
//
// Archived files are NOT automatically cleaned up — that's
// deliberate. A scope accumulating .dropped/ directories over time is
// a real, expected cost of keeping this recoverable; freeing that
// space is a manual, explicit, out-of-band decision for whoever's
// confident they don't need it (rm -rf <scope>/.dropped/<name>/),
// not something this command or any automated maintenance pass
// should ever decide on someone's behalf.
//
// Still deliberately does NOT touch _Dictionaries — a dropped table's
// dictionary entries become orphaned (exactly like the FindingsNew
// staging-table incident this session's earlier _Dictionaries
// redesign was built to make DETECTABLE), not automatically cleaned
// up. _Dictionaries is append-only by design; deleting rows from it
// is a different, more invasive operation than anything else this
// engine does, and isn't necessary for .drop table to be CORRECT —
// the orphaned entries are harmless (wasted space only) and
// discoverable via the same `_Dictionaries | where TableName !in
// (...)` query pattern already established, run explicitly as a
// maintenance pass, not implicitly on every drop.
func (e *Engine) dropTableComplete(tableName string) error {
	dbPath := e.Catalog.DatabasePath()

	// Found via glob BEFORE Catalog.DropTable runs, deliberately: that
	// call updates in-memory catalog state and removes the row-bearing
	// extents it already knows about, and doing the full file glob
	// afterward (as an earlier version of this function did) is still
	// correct since findAllTableFiles doesn't depend on catalog state
	// at all — but archiving needs the full file list regardless of
	// ordering, so this keeps the two concerns (catalog state, disk
	// files) each doing their own job without depending on the other's
	// completion order.
	files, err := findAllTableFiles(dbPath, tableName)
	if err != nil {
		return fmt.Errorf("drop table %q: finding files to archive: %w", tableName, err)
	}

	if err := e.Catalog.DropTable(tableName); err != nil {
		return err
	}

	if len(files) == 0 {
		return nil
	}

	// Same nanosecond+random uniqueness scheme newExtentID already
	// uses — guarantees no collision even if the same table is
	// dropped, recreated, and dropped again within the same directory.
	var r [4]byte
	if _, randErr := rand.Read(r[:]); randErr != nil {
		binary.LittleEndian.PutUint32(r[:], uint32(os.Getpid()))
	}
	archiveDir := filepath.Join(dbPath, ".dropped",
		fmt.Sprintf("%s_%015x%08x", tableName, uint64(time.Now().UnixNano()), binary.LittleEndian.Uint32(r[:])))
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return fmt.Errorf("drop table %q: creating archive directory: %w", tableName, err)
	}

	// No os.IsNotExist exception, deliberately — same reasoning as the
	// deletion-based version of this function had: findAllTableFiles's
	// four glob patterns are mutually exclusive (a file can't match
	// both .vtx and .vtx.superseded, or both root and extents/, for
	// the same literal path), so there's no legitimate scenario where
	// a file this function just found via glob is already gone by the
	// time this loop reaches it.
	var errs []error
	for _, relPath := range files {
		fullPath := filepath.Join(dbPath, relPath)
		archivePath := filepath.Join(archiveDir, filepath.Base(relPath))
		if err := os.Rename(fullPath, archivePath); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", relPath, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("drop table %q: %d file(s) could not be archived to %s: %v", tableName, len(errs), archiveDir, errs)
	}
	return nil
}

// applyGCTable physically removes .superseded files for one table.
// Safe unconditionally: a file only gets this suffix after .compact
// has already renamed it OUT of the "*.vtx" glob that every scan path
// uses, so nothing could possibly still be reading it.
func (e *Engine) applyGCTable(cmd *parser.GCTableCmd) (*types.Table, error) {
	if !e.Catalog.IsDiscovery() {
		return nil, fmt.Errorf(".gc is for catalog-free (discovery mode) scopes only")
	}
	pattern := filepath.Join(e.Catalog.DatabasePath(), cmd.TableName+"_*.vtx"+supersededSuffix)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf(".gc: %w", err)
	}

	removed := 0
	var freedBytes int64
	var errs []error
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil {
			freedBytes += info.Size()
		}
		if err := os.Remove(m); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}

	result := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "Result", Type: types.TypeString},
		{Name: "FilesRemoved", Type: types.TypeLong},
		{Name: "BytesFreed", Type: types.TypeLong},
	}})
	status := "OK"
	if len(errs) > 0 {
		status = fmt.Sprintf("PARTIAL: %d file(s) failed to remove: %v", len(errs), errs)
	}
	result.AddRow(types.Row{status, int64(removed), freedBytes})
	return result, nil
}

// applyCompactDatabase compacts every table in the scope, including
// _Dictionaries and any other system table ListTables/.show tables
// hides from generic enumeration — see Catalog.ListAllTables's doc
// comment for why this must use that unfiltered listing specifically,
// not the one every other user-facing command uses. One row per
// table in the result; a table already at 1 extent is included (as
// "already compact"), not silently skipped, so the output is a
// complete audit of every table's state, not just the ones that
// needed work.
func (e *Engine) applyCompactDatabase(_ *parser.CompactDatabaseCmd) (*types.Table, error) {
	if !e.Catalog.IsDiscovery() {
		return nil, fmt.Errorf(".compact is for catalog-free (discovery mode) scopes only")
	}

	names := e.Catalog.ListAllTables()
	sort.Strings(names)

	result := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "TableName", Type: types.TypeString},
		{Name: "Result", Type: types.TypeString},
		{Name: "ExtentsBefore", Type: types.TypeLong},
	}})

	for _, name := range names {
		perTable, err := e.applyCompactTable(&parser.CompactTableCmd{TableName: name})
		if err != nil {
			result.AddRow(types.Row{name, fmt.Sprintf("error: %v", err), int64(0)})
			continue
		}
		result.AddRow(types.Row{name, normalizedCell(perTable, "Result"), normalizedCell(perTable, "ExtentsBefore")})
	}
	return result, nil
}

// applyGCDatabase physically removes .superseded files for every
// table in the scope, including system tables — same reasoning and
// same unfiltered listing as applyCompactDatabase.
func (e *Engine) applyGCDatabase(_ *parser.GCDatabaseCmd) (*types.Table, error) {
	if !e.Catalog.IsDiscovery() {
		return nil, fmt.Errorf(".gc is for catalog-free (discovery mode) scopes only")
	}

	names := e.Catalog.ListAllTables()
	sort.Strings(names)

	result := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "TableName", Type: types.TypeString},
		{Name: "FilesRemoved", Type: types.TypeLong},
		{Name: "BytesFreed", Type: types.TypeLong},
	}})

	var totalFiles, totalBytes int64
	for _, name := range names {
		perTable, err := e.applyGCTable(&parser.GCTableCmd{TableName: name})
		if err != nil {
			result.AddRow(types.Row{name, int64(0), int64(0)})
			continue
		}
		files, _ := normalizedCell(perTable, "FilesRemoved").(int64)
		bytesFreed, _ := normalizedCell(perTable, "BytesFreed").(int64)
		totalFiles += files
		totalBytes += bytesFreed
		result.AddRow(types.Row{name, files, bytesFreed})
	}
	result.AddRow(types.Row{"TOTAL", totalFiles, totalBytes})
	return result, nil
}

// normalizedCell looks up a column by name in a per-table compact/gc
// result and returns its value from the first (only) row — robust to
// applyCompactTable's two different result shapes (a table already at
// 1 extent returns a shorter "already compact" row than one that
// actually got compacted), since both shapes name their common
// columns (Result, ExtentsBefore) consistently even though they sit
// at different positions.
func normalizedCell(t *types.Table, colName string) interface{} {
	if t == nil || len(t.Rows) == 0 {
		return nil
	}
	idx := t.Schema.ColumnIndex(colName)
	if idx < 0 || idx >= len(t.Rows[0]) {
		return nil
	}
	return t.Rows[0][idx]
}

var compactAfterSnapshotHook func()
