// Package engine executes KQL statements against the catalog and storage layer.
//
// The execution model:
//   1. Resolve table in catalog (or let-bound table)
//   2. Per-extent: scan with column projection + zone map filter pushdown
//   3. Apply remaining operators (where, extend, summarize, join, order, take)
//   4. Return result table
package engine

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"

	vortex "github.com/LaurieRhodes/vortex-go"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// LetContext holds let-bound names during compound statement execution.
type LetContext struct {
	Scalars   map[string]types.Value         // Scalar let bindings: name → value
	Functions map[string]*parser.FunctionDef // UDF let bindings: name → definition
	Tables    map[string]*types.Table        // Tabular let bindings: name → result table

	// Parent is the enclosing scope's own LetContext, if any — nil for
	// a top-level context. Added 2026-08-17 to fix a real, live
	// lexical-scoping gap: executeCompound previously installed a
	// completely ISOLATED fresh context for every nested compound
	// statement (e.g. a stored function's tabular argument with its
	// own further `let` bindings, "(let y = 1; MyTable | where x >
	// y)"), with no way at all for that inner scope to see an
	// outer-scope name like MyTable — confirmed live via
	// TestLetBoundTableWithOwnLetsAsTabularArgument's own comment
	// (stored_functions_test.go), which had documented this as a
	// known, deliberately-unfixed limitation when the PrecomputedTable
	// fix (Sprint 12) closed the adjacent, simpler bug. Fixed via a
	// genuine parent-chain lookup (LookupScalar/LookupTable/
	// LookupFunction below) rather than a copy-down snapshot at
	// context-creation time — the chain stays live, so it also
	// correctly reflects any change to an outer binding made after
	// the inner context was created, which a one-time copy would not.
	Parent *LetContext
}

// LookupScalar checks this context's own Scalars map, falling back to
// the parent chain (if any) when the name isn't found locally — the
// standard lexical-scoping shadowing rule: an inner `let x = ...`
// shadows an outer one with the same name, but an outer binding
// remains visible to any inner scope that doesn't redeclare it.
func (ctx *LetContext) LookupScalar(name string) (types.Value, bool) {
	if ctx == nil {
		return nil, false
	}
	if v, ok := ctx.Scalars[name]; ok {
		return v, true
	}
	return ctx.Parent.LookupScalar(name)
}

// LookupTable is LookupScalar's own tabular-let counterpart.
func (ctx *LetContext) LookupTable(name string) (*types.Table, bool) {
	if ctx == nil {
		return nil, false
	}
	if t, ok := ctx.Tables[name]; ok {
		return t, true
	}
	return ctx.Parent.LookupTable(name)
}

// LookupFunction is LookupScalar's own UDF-let counterpart.
func (ctx *LetContext) LookupFunction(name string) (*parser.FunctionDef, bool) {
	if ctx == nil {
		return nil, false
	}
	if fn, ok := ctx.Functions[name]; ok {
		return fn, true
	}
	return ctx.Parent.LookupFunction(name)
}

// activeLetContext is the package-level reference to the current let context.
// It exists because expression evaluation (evalExpr and the func_*.go
// handlers) is package-level and cannot see the Engine; e.letContext and
// this variable are kept in lockstep exclusively via Engine.setLetContext.
// SINGLE-THREADED INVARIANT: one query executes at a time. This is safe
// for Sprint 6 parallel extent scanning — the scan path (storage.go,
// vortex_bridge.go, filter.go) touches no eval machinery and no package
// state; only operator evaluation (serial, post-scan) reads this.
// Threading a context through all ~224 evalExpr call sites is deferred
// until concurrent query execution is actually needed.
var activeLetContext *LetContext

// Engine is the query execution engine.
type Engine struct {
	Catalog    *catalog.Catalog
	letContext *LetContext // Active let bindings (set during compound execution)
	Verbose    bool

	// ExternalResolver is nil by default -- purely additive, zero
	// impact on this engine's own behavior unless a host application
	// sets it. Deliberately a field on Engine itself, NOT package-level
	// state: this codebase's own concurrency model allows multiple,
	// independent Engine instances to run truly concurrently (see
	// TestDiscoveryConcurrentIngestStress), and a shared, package-level
	// mutable reference for this would risk the exact class of bug this
	// engine already found and fixed three separate times in other
	// features this same session (activeEngine for toscalar(),
	// bindStoredFunctionArgs for scalar parameters, executeCompound's
	// own letContext) -- one goroutine's resolver call landing on a
	// DIFFERENT goroutine's Engine. A per-instance field avoids that
	// whole bug class by construction; each host-created Engine simply
	// carries its own, independent resolver. See
	// ExternalFunctionResolver's own doc comment for the full
	// motivation and contract.
	ExternalResolver ExternalFunctionResolver

	// dictCache holds lazily-loaded shared dictionaries (see
	// shareddict.go), keyed by "table.column". aggregateExtent/
	// topNExtent/ScanExtent all run per-extent scans concurrently
	// (goroutines bounded by NumCPU), so this is guarded by
	// dictCacheMu rather than assumed single-threaded like the rest of
	// Engine.
	//
	// Cached once per Engine instance, on first access — SAME
	// staleness model as every other table in this catalog (a
	// long-lived Engine's view is captured at first touch and doesn't
	// auto-refresh from a DIFFERENT Engine's concurrent writes without
	// restarting; a known, already-accepted limitation elsewhere in
	// this codebase, not new here). This is deliberately weaker than
	// an earlier version of this cache, which re-validated against an
	// on-disk commit marker on every single access — cheap when
	// dictionaries were sidecar files (a few bytes to check), no
	// longer cheap now that _Dictionaries is a real table that would
	// need re-scanning to check. What's NOT weakened: SaveExtent's
	// post-write refresh of THIS engine's own cache (storage.go) is
	// still required, not optional — without it, a long-lived Engine
	// that writes more dictionary entries after already caching an
	// older snapshot (ingest, query, ingest more, query again, all on
	// one Engine — no concurrency involved at all) would resolve new
	// extents' codes against its own stale cache and panic on an
	// out-of-range code, a real bug this codebase already hit once.
	dictCache   map[string]*sharedDict
	dictCacheMu sync.RWMutex

	// resolvingFunctions tracks stored-function names currently being
	// resolved in the CURRENT call chain (functions.go) — a simple
	// stack-as-set, checked before each resolution and popped after,
	// to detect A-calls-B-calls-A recursion. Real ADX explicitly
	// disallows function recursion (verified against its own .create
	// function docs, not assumed); text-substitution resolution makes
	// an unguarded cycle a genuine infinite-recursion risk here, not
	// just a real-ADX policy choice to replicate for conformance's own
	// sake. Not a sync.Map/mutex-guarded structure — deliberately,
	// since a single Engine value only ever resolves one call chain at
	// a time (this isn't shared across the concurrent per-extent scan
	// goroutines dictCache's comment describes; it's plain sequential
	// call-stack tracking within one query's execution).
	resolvingFunctions map[string]bool
}

func New(cat *catalog.Catalog) *Engine {
	return &Engine{
		Catalog:            cat,
		dictCache:          make(map[string]*sharedDict),
		resolvingFunctions: make(map[string]bool),
	}
}

// getSharedDict returns the (lazily loaded, cached) shared dictionary
// for a table+column, used to resolve kql.dictref-encoded columns at
// read time. Cached once per Engine instance on first access — see
// the dictCache field comment for the staleness model this implies.
func (e *Engine) getSharedDict(table, column string) (*sharedDict, error) {
	key := table + "." + column

	e.dictCacheMu.RLock()
	sd, ok := e.dictCache[key]
	e.dictCacheMu.RUnlock()
	if ok {
		return sd, nil
	}

	e.dictCacheMu.Lock()
	defer e.dictCacheMu.Unlock()
	// Re-check under write lock: another goroutine may have already
	// populated this while we were waiting.
	if sd, ok := e.dictCache[key]; ok {
		return sd, nil
	}
	sd, err := loadTableDict(e, table, column)
	if err != nil {
		return nil, err
	}
	e.dictCache[key] = sd
	return sd, nil
}

// Execute runs a parsed statement and returns the result table (if any).
func (e *Engine) Execute(stmt parser.Statement) (*types.Table, error) {
	switch s := stmt.(type) {
	case *parser.CompoundStatement:
		return e.executeCompound(s)
	case *parser.Query:
		return e.executeQuery(s)
	case *parser.CreateTableCmd:
		schema := e.withTimeReceivedColumn(s.Schema, s.NoTimeReceived)
		if err := e.Catalog.CreateTable(s.TableName, schema); err != nil {
			return nil, err
		}
		return nil, e.persistDiscoverySchema(s.TableName, schema)
	case *parser.AlterMergeTableCmd:
		return e.applyAlterMergeTable(s)
	case *parser.CreateMergeTableCmd:
		// CreateMergeTableCmd has no NoTimeReceived field of its own
		// (a distinct, existing command this session didn't extend) —
		// the scope-wide default alone decides for a merge-table
		// create, matching the same rule .create table follows when
		// its own per-table property is unset.
		schema := e.withTimeReceivedColumn(s.Schema, false)
		if err := e.Catalog.CreateMergeTable(s.TableName, schema); err != nil {
			return nil, err
		}
		return nil, e.persistDiscoverySchema(s.TableName, e.Catalog.GetTable(s.TableName).Schema)
	case *parser.DropTableCmd:
		return nil, e.dropTableComplete(s.TableName)
	case *parser.ShowTablesCmd:
		return e.showTables()
	case *parser.ShowTableExtentsCmd:
		return e.showTableExtents(s.TableName)
	case *parser.ShowDatabaseCmd:
		return e.showDatabase()
	case *parser.PipedCommand:
		result, err := e.Execute(s.Inner)
		if err != nil {
			return nil, err
		}
		return e.applyPipeline(result, s.Operators)
	case *parser.HelpCmd:
		return e.showHelp()
	case *parser.CompactTableCmd:
		return e.applyCompactTable(s)
	case *parser.GCTableCmd:
		return e.applyGCTable(s)
	case *parser.CompactDatabaseCmd:
		return e.applyCompactDatabase(s)
	case *parser.GCDatabaseCmd:
		return e.applyGCDatabase(s)
	case *parser.CreateFunctionCmd:
		return e.applyCreateFunction(s)
	case *parser.ShowFunctionsCmd:
		return e.applyShowFunctions()
	case *parser.ShowFunctionCmd:
		return e.applyShowFunction(s)
	case *parser.DropFunctionCmd:
		return e.applyDropFunction(s)
	case *parser.CreateMaterializedViewCmd:
		return e.applyCreateMaterializedView(s)
	case *parser.ShowMaterializedViewsCmd:
		return e.applyShowMaterializedViews()
	case *parser.DropMaterializedViewCmd:
		return e.applyDropMaterializedView(s)
	case *parser.ChunkFileCmd:
		return e.applyChunkFile(s)
	case *parser.EmbedIntoCmd:
		return e.applyEmbedInto(s)
	case *parser.SetCmd:
		return e.setCreate(s)
	case *parser.SetOrAppendCmd:
		return e.setOrAppend(s)
	case *parser.IngestInlineCmd:
		return e.ingestInline(s.TableName, s.Data)
	case *parser.IngestCSVCmd:
		return e.ingestCSVFile(s.TableName, s.FilePath)
	case *parser.MergeExtentsCmd:
		if e.Catalog.IsDiscovery() {
			return nil, fmt.Errorf(".merge is not supported in catalog-free mode: extent replacement needs an atomic multi-file commit, which only a catalog provides. Supersede rows instead of merging extents, or open the database via a catalog")
		}
		return e.mergeExtents(s.TableName)
	case *parser.DropExtentCmd:
		tableName, err := e.Catalog.RemoveExtent(s.ExtentID)
		if err != nil {
			return nil, err
		}
		result := types.NewTable("", types.Schema{
			Columns: []types.Column{{Name: "Result", Type: types.TypeString}},
		})
		result.AddRow(types.Row{fmt.Sprintf("Dropped extent %s from table %s", s.ExtentID, tableName)})
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported statement type: %T", stmt)
	}
}

// --- Query Execution ---

// setLetContext installs (or clears, with nil) the active let context on
// both the engine and the package-level activeLetContext in lockstep.
// All context transitions MUST go through this so the two references
// never diverge; executeCompound pairs it with defer so error paths
// cannot leak bindings into subsequent statements.
func (e *Engine) setLetContext(ctx *LetContext) {
	e.letContext = ctx
	activeLetContext = ctx
}

// executeCompound evaluates let bindings in order, then executes the final statement.
func (e *Engine) executeCompound(cs *parser.CompoundStatement) (*types.Table, error) {
	ctx := &LetContext{
		Scalars:   make(map[string]types.Value),
		Functions: make(map[string]*parser.FunctionDef),
		Tables:    make(map[string]*types.Table),
		// Parent = the enclosing scope's own context, if any — see
		// LetContext's own doc comment for the real, live scoping bug
		// this fixes. e.letContext here is still whatever was active
		// BEFORE this call installs ctx below (prevCtx, captured just
		// after this literal), so using it directly as Parent is
		// correct and doesn't need its own separate variable.
		Parent: e.letContext,
	}

	// Install the context once; it is mutated in place as bindings
	// evaluate, so earlier lets are visible to later ones.
	//
	// The deferred restore covers every exit path, including mid-loop
	// errors -- but restores the PRIOR context (whatever was active
	// before this call), not unconditionally nil. An earlier version
	// always cleared to nil, which was correct for a top-level call
	// (where the prior value already IS nil) but a real, live bug for
	// a NESTED one: executeCompound can call itself recursively (a
	// stored function's tabular argument whose own text has its own
	// let binding, e.g. MyFilter((let x = 10; T | where Y >= x), 9) --
	// see stored_functions.go's bindStoredFunctionArgs and this
	// function's own new CompoundStatement case just above). The
	// INNER call's defer firing unconditionally-to-nil wiped out the
	// OUTER function-level context (which still held v, the scalar
	// parameter) partway through evaluating the outer context's own
	// let bindings, well before the outer call's own defer had a
	// chance to run -- surfacing as "where: column v not found" for a
	// parameter that WAS correctly bound moments earlier, simply
	// erased by the inner call's own cleanup. Same "save the prior
	// value, don't just clear" principle already applied twice this
	// session (activeEngine, bindStoredFunctionArgs) for the identical
	// class of nested-call bug.
	prevCtx := e.letContext
	e.setLetContext(ctx)
	defer e.setLetContext(prevCtx)

	// Evaluate each let binding
	for _, let := range cs.Lets {
		switch v := let.Value.(type) {
		case *parser.CompoundStatement:
			// A tabular let bound to a value that ITSELF has its own
			// let bindings — e.g. a stored function's tabular
			// argument whose text is "(let x = 10; T | where Y >= x)"
			// (bindStoredFunctionArgs, stored_functions.go). Routed
			// through e.Execute (the general entry point, which
			// already correctly dispatches a CompoundStatement to
			// THIS same function, recursively) rather than
			// e.executeQuery directly, since executeQuery only
			// accepts a bare *parser.Query and has no let-handling of
			// its own at all. Found live, not hypothetical: a real
			// gap surfaced during systematic review of tabular
			// stored-function parameters — this case previously
			// didn't exist, so a CompoundStatement stored here fell
			// through with no matching case, silently doing nothing
			// (let.Value's evaluation was simply skipped).
			result, err := e.Execute(v)
			if err != nil {
				return nil, fmt.Errorf("let %s: %w", let.Name, err)
			}
			ctx.Tables[let.Name] = result

		case *parser.Query:
			// Tabular let: execute the query and store result
			result, err := e.executeQuery(v)
			if err != nil {
				return nil, fmt.Errorf("let %s: %w", let.Name, err)
			}
			ctx.Tables[let.Name] = result

		case *parser.PrecomputedTable:
			// A tabular let already bound to a computed result — see
			// PrecomputedTable's own doc comment (ast.go) for why
			// this exists (bindStoredFunctionArgs' own fix for a real,
			// live bug: a tabular argument referencing a caller-scope
			// table must be captured while the caller's own
			// LetContext is still current, not re-resolved later
			// against this function's freshly-installed one).
			ctx.Tables[let.Name] = v.Table

		case *parser.ScalarExpr:
			// Scalar let: evaluate the expression. substituteToScalars
			// runs first (see its own doc comment, eval.go, for why —
			// evalExpr itself has no case for ToScalarExpr at all, by
			// design) so `let x = toscalar(T | summarize max(Y))` — the
			// single most common real-world toscalar() pattern per
			// Microsoft's own docs — resolves correctly.
			// Use a dummy single-row table for evaluation context
			rewritten, err := substituteToScalars(e, v.Expr)
			if err != nil {
				return nil, fmt.Errorf("let %s: %w", let.Name, err)
			}
			val, err := evalExpr(rewritten, &types.Schema{}, types.Row{})
			if err != nil {
				return nil, fmt.Errorf("let %s: %w", let.Name, err)
			}
			ctx.Scalars[let.Name] = val

		case *parser.FunctionDef:
			// UDF: register for call resolution during expression evaluation
			ctx.Functions[let.Name] = v

		default:
			return nil, fmt.Errorf("let %s: unsupported value type %T", let.Name, let.Value)
		}
	}

	// Execute final statement with full context
	return e.Execute(cs.Final)
}

// executeQuery is a thin wrapper over executeQueryRaw that applies the
// _TimeReceived visibility rule (see timereceived.go) uniformly to
// every result, regardless of which internal path (generic scan,
// columnar aggregate/top-N fast paths, federation, a stored function
// or materialized view's own internal query execution) actually
// produced it -- a single interception point rather than threading
// this concern through every individual return statement inside
// executeQueryRaw, several of which exist for genuinely different
// fast-path reasons.
//
// Safe for internal, recursive callers too (federation.go,
// stored_functions.go, materialized_views.go, mv_maintenance.go all
// call e.executeQuery / remoteEng.executeQuery for their own internal
// purposes): any internal computation that actually NEEDS
// _TimeReceived does so via a query that explicitly references it by
// name (e.g. a materialized view's own arg_max(_TimeReceived, *) by
// Id), which the explicit-reference exemption below already covers --
// nothing that genuinely depends on the column loses access to it.
// Lower-level, non-executeQuery paths (compact.go's own ScanExtent
// calls, mv_maintenance.go's readMaterializedTableRaw) are
// deliberately NOT wrapped here at all -- those are storage-level
// operations that must always see every real column regardless of
// this display-layer rule, never query-result-shaped output a user
// would see.
func (e *Engine) executeQuery(q *parser.Query) (*types.Table, error) {
	// Save/restore letContext around a bare (non-compound) query, but
	// ONLY when this query's own pipeline actually contains an AsOp --
	// applyAs (operators.go) is the only thing that can lazily create
	// and install a LetContext mid-pipeline for a query with no `let`
	// statements at all, and unconditionally wrapping EVERY query here
	// (an earlier version of this fix did exactly that) turns out to
	// be a real, live data race: e.setLetContext writes the package-
	// level activeLetContext (see its own doc comment for exactly why
	// that's shared, package-level state), and TestToScalarConcurrentEnginesUseCorrectOwnEngine
	// runs many independent Engine instances truly concurrently on
	// separate goroutines -- go test -race caught the unconditional
	// version immediately, since every one of those goroutines' bare
	// (no-`as`, no-`let`) queries would ALSO have written that shared
	// variable via this same deferred call, even though nothing about
	// their own execution needed to. Checking for AsOp up front keeps
	// this scoped to exactly the queries that need it -- the vast
	// majority of queries (no `as` at all) never touch activeLetContext
	// here, identical to this function's behavior before `as` existed.
	if queryNeedsLetContextRestore(q.Operators) {
		prevCtx := e.letContext
		defer e.setLetContext(prevCtx)
	}

	result, err := e.executeQueryRaw(q)
	if err != nil {
		return nil, err
	}
	return hideTimeReceivedUnlessExplicit(result, q.Operators), nil
}

// queryNeedsLetContextRestore reports whether any operator in the
// pipeline can lazily create/mutate e.letContext mid-pipeline (AsOp,
// InvokeOp — see executeQuery's own doc comment for why this check
// exists). Named for what it guards rather than for AsOp specifically
// since InvokeOp (2026-08-15) shares the exact same lazy-context-
// creation shape applyAs already established (see applyInvoke's own
// doc comment) and needs the identical protection.
func queryNeedsLetContextRestore(operators []parser.Operator) bool {
	for _, op := range operators {
		switch op.(type) {
		case *parser.AsOp, *parser.InvokeOp:
			return true
		}
	}
	return false
}

func (e *Engine) executeQueryRaw(q *parser.Query) (*types.Table, error) {
	// Step 1: Resolve source table (check let context first, then catalog).
	// LookupTable walks the parent chain, not just e.letContext's own
	// local map — see LetContext's own doc comment for why.
	if letTable, ok := e.letContext.LookupTable(q.Source); ok {
		// Source is a let-bound tabular result — apply operators directly
		result := types.NewTable(q.Source, letTable.Schema)
		result.Rows = append(result.Rows, letTable.Rows...)
		return e.applyPipeline(result, q.Operators)
	}

	// Table-valued function source (csv, json, ndjson, vortex)
	if q.SourceFunc != nil {
		return e.executeTableFunc(q)
	}

	// Cross-database (filesystem-federated) source — resolveFederatedTable
	// does the whole alias resolution, remote-engine open, and scan,
	// pushing down as many of q.Operators as it safely can (see
	// federation.go's splitPushableFederationOps for exactly which
	// ones and why) and reporting back whichever ones weren't pushed.
	// Only those REMAINING operators run through the normal local
	// pipeline below — pipeline stages have no notion of which engine
	// produced their input table, so this only needs to intervene at
	// the source and hand off what's left, not thread pushdown
	// awareness through every operator itself.
	if q.SourceDB != nil {
		result, remaining, err := e.resolveFederatedTable(q.SourceDB, q.Operators)
		if err != nil {
			return nil, err
		}
		return e.applyPipeline(result, remaining)
	}

	// Stored (persisted, tabular) function call — see
	// stored_functions.go's resolveStoredFunction. Same shape as the
	// federation case immediately above: resolve at the source, then
	// hand off to the normal pipeline unchanged.
	if q.SourceFuncCall != nil {
		result, err := e.resolveStoredFunction(q.SourceFuncCall)
		if err != nil {
			return nil, err
		}
		return e.applyPipeline(result, q.Operators)
	}

	// If q.Source is a materialized view currently mid-merge (see
	// mv_maintenance.go), wait for that merge to finish BEFORE
	// resolving the table definition below — not after, which was a
	// real, live ordering bug caught during testing: an earlier
	// version placed this wait AFTER tableDef was already fetched,
	// so even though the wait correctly blocked until the merge
	// finished, tableDef itself still pointed at the STALE, pre-merge
	// catalog.Table object (merging replaces a materialized view's
	// table via dropTableComplete + a fresh CreateTable under the same
	// name, installing a genuinely different object in the catalog's
	// map — waiting doesn't retroactively refresh a pointer captured
	// before the wait). The read would then try to scan extents that
	// dropTableComplete had already archived, failing with "no such
	// file or directory" — not a data race the Go race detector could
	// have caught (no concurrent access to the SAME memory without
	// synchronization — the bug was a stale value already safely
	// copied out before the synchronization point existed at all), so
	// only surfaced via a real, disk-backed multi-write test, not -race
	// or inspection.
	e.waitForMaterializedView(q.Source)

	tableDef := e.Catalog.GetTable(q.Source)
	if tableDef == nil {
		// Check if first operator is PrintOp (no source table needed)
		if q.Source == "" && len(q.Operators) > 0 {
			if _, ok := q.Operators[0].(*parser.PrintOp); ok {
				result, err := e.applyPrint(q.Operators[0].(*parser.PrintOp))
				if err != nil {
					return nil, err
				}
				// Apply remaining pipeline operators after print
				return e.applyPipeline(result, q.Operators[1:])
			}
			// Check for DataTableOp
			if dt, ok := q.Operators[0].(*parser.DataTableOp); ok {
				result, err := e.applyDataTable(dt)
				if err != nil {
					return nil, err
				}
				return e.applyPipeline(result, q.Operators[1:])
			}
			// Check for RangeOp
			if ro, ok := q.Operators[0].(*parser.RangeOp); ok {
				result, err := e.applyRange(ro)
				if err != nil {
					return nil, err
				}
				return e.applyPipeline(result, q.Operators[1:])
			}
			if so, ok := q.Operators[0].(*parser.SearchOp); ok {
				result, err := e.applySearch(so)
				if err != nil {
					return nil, err
				}
				return e.applyPipeline(result, q.Operators[1:])
			}
			if fo, ok := q.Operators[0].(*parser.FindOp); ok {
				result, err := e.applyFind(fo)
				if err != nil {
					return nil, err
				}
				return e.applyPipeline(result, q.Operators[1:])
			}
		}
		return nil, fmt.Errorf("table %q not found", q.Source)
	}

	// Metadata fast path: `T | count` as the first operator needs no
	// scan — the catalog already holds per-extent row counts (set at
	// ingest/merge). Only the leading position qualifies: any earlier
	// operator could error or change row count, which the shortcut
	// would mask.
	if len(q.Operators) > 0 {
		if _, ok := q.Operators[0].(*parser.CountOp); ok {
			var total int64
			for _, ext := range tableDef.Extents {
				total += ext.RowCount
			}
			if e.Verbose {
				fmt.Fprintf(os.Stderr, "[scan] count answered from catalog metadata (%d extents)\n", len(tableDef.Extents))
			}
			result := types.NewTable("", types.Schema{
				Columns: []types.Column{{Name: "Count", Type: types.TypeLong}},
			})
			result.AddRow(types.Row{total})
			return e.applyPipeline(result, q.Operators[1:])
		}
	}

	// Columnar aggregation fast path: scan → where → summarize with
	// exact vector predicates and typed accumulators — rows are never
	// materialized. Falls back to the row engine when any predicate,
	// aggregate, or by-expression is outside the supported set.
	if plan := planColumnarAggregate(q.Operators, &tableDef.Schema); plan != nil {
		result, err := e.runColumnarAggregate(tableDef, plan)
		if err != nil {
			return nil, err
		}
		return e.applyPipeline(result, q.Operators[plan.consumed:])
	}

	// Columnar top-N fast path: scan → where → sort|take (or top) with
	// per-extent bounded heaps over typed vectors. Only heap-resident
	// rows are boxed; peak memory is one chunk plus N rows per extent.
	if plan := planColumnarTopN(q.Operators, &tableDef.Schema); plan != nil {
		result, err := e.runColumnarTopN(tableDef, plan, q.Operators)
		if err != nil {
			return nil, err
		}
		return e.applyPipeline(result, q.Operators[plan.consumed:])
	}

	// Step 2-3: pushdown-projected, zone-pruned scan — factored into
	// scanTableProjected specifically so resolveFederatedTable
	// (federation.go) can reuse the exact same pushdown analysis
	// against a REMOTE table, not just this local one. See that
	// function's own doc comment for why: without this, federation
	// always pulled a remote table's full contents regardless of what
	// the local pipeline did with it afterward, a known, documented
	// limitation until this reuse closed it.
	result, err := e.scanTableProjected(tableDef, q.Source, q.Operators)
	if err != nil {
		return nil, err
	}

	// Step 4: Apply operators in pipeline order against projected schema
	return e.applyPipeline(result, q.Operators)
}

// scanExtents fills result with rows from every extent of tableDef.
//
// Three strategies, chosen from the pipeline shape:
//   - row limit (take/limit reachable through 1:1 operators): sequential
//     scan stopping as soon as enough rows are collected — later extents
//     are never opened, and chunk iteration inside an extent stops early
//   - multiple extents, no limit: extents scan concurrently (bounded by
//     NumCPU); per-extent results are merged in extent order so output
//     is deterministic and identical to the sequential scan
//   - single extent, no limit: direct scan, no goroutine overhead
// scanTableProjected runs the pushdown-projected, zone-pruned scan
// shared by both a local table read (executeQuery, above) and a
// federated (cross-scope) table read (federation.go's
// resolveFederatedTable) — extracted here specifically so the SAME
// pushdown analysis (RequiredColumns, extractZoneFilter) applies to
// both, rather than federation reimplementing (or worse, forgetting
// to reimplement) it separately. sourceName is used only for the
// verbose diagnostic and the result table's own Name field; tableDef
// and operators may belong to a DIFFERENT engine than e itself calls
// this on (federation.go calls remoteEng.scanTableProjected(...), not
// e.scanTableProjected(...), so scanExtents runs against the remote
// engine's own catalog/extent files, not the local one's).
func (e *Engine) scanTableProjected(tableDef *catalog.Table, sourceName string, operators []parser.Operator) (*types.Table, error) {
	projectedCols := RequiredColumns(&tableDef.Schema, operators)

	scanCols := projectedCols
	if scanCols == nil {
		scanCols = tableDef.Schema.ColumnNames()
	}

	projectedSchema := buildProjectedSchema(&tableDef.Schema, scanCols)
	zoneFilter := extractZoneFilter(operators, &tableDef.Schema)

	if e.Verbose {
		total := len(tableDef.Schema.Columns)
		scanned := len(scanCols)
		skipped := total - scanned
		fmt.Fprintf(os.Stderr, "[scan] table=%s extents=%d columns=%d/%d (skipping %d)\n",
			sourceName, len(tableDef.Extents), scanned, total, skipped)
		fmt.Fprintf(os.Stderr, "[scan] projected: %v\n", scanCols)
		if zoneFilter != nil {
			fmt.Fprintf(os.Stderr, "[scan] zone filter: %s\n", zoneFilter)
		}
	}

	result := types.NewTable(sourceName, projectedSchema)
	if err := e.scanExtents(result, tableDef, scanCols, zoneFilter, operators); err != nil {
		return nil, err
	}
	return result, nil
}

func (e *Engine) scanExtents(result *types.Table, tableDef *catalog.Table, scanCols []string, zoneFilter *vortex.RowFilter, operators []parser.Operator) error {
	scanLimit, hasLimit := ScanRowLimit(operators)

	if hasLimit {
		if e.Verbose {
			fmt.Fprintf(os.Stderr, "[scan] row limit %d: sequential early-exit scan\n", scanLimit)
		}
		for _, ext := range tableDef.Extents {
			remaining := scanLimit - int64(len(result.Rows))
			if remaining <= 0 {
				break
			}
			extResult, err := e.ScanExtent(tableDef.Name, ext.FilePath, &tableDef.Schema, scanCols, zoneFilter, remaining)
			if err != nil {
				return fmt.Errorf("scan extent %s: %w", ext.ID, err)
			}
			for _, row := range extResult.Rows {
				result.AddRow(row)
			}
		}
		return nil
	}

	if len(tableDef.Extents) > 1 {
		workers := runtime.NumCPU()
		if workers > len(tableDef.Extents) {
			workers = len(tableDef.Extents)
		}
		if e.Verbose {
			fmt.Fprintf(os.Stderr, "[scan] parallel scan: %d extents, %d workers\n",
				len(tableDef.Extents), workers)
		}
		results := make([]*types.Table, len(tableDef.Extents))
		errs := make([]error, len(tableDef.Extents))
		sem := make(chan struct{}, workers)
		var wg sync.WaitGroup
		for i := range tableDef.Extents {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				ext := tableDef.Extents[i]
				results[i], errs[i] = e.ScanExtent(tableDef.Name, ext.FilePath, &tableDef.Schema, scanCols, zoneFilter, 0)
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				return fmt.Errorf("scan extent %s: %w", tableDef.Extents[i].ID, err)
			}
		}
		for _, extResult := range results {
			for _, row := range extResult.Rows {
				result.AddRow(row)
			}
		}
		return nil
	}

	for _, ext := range tableDef.Extents {
		extResult, err := e.ScanExtent(tableDef.Name, ext.FilePath, &tableDef.Schema, scanCols, zoneFilter, 0)
		if err != nil {
			return fmt.Errorf("scan extent %s: %w", ext.ID, err)
		}
		for _, row := range extResult.Rows {
			result.AddRow(row)
		}
	}
	return nil
}

func (e *Engine) applyOperator(input *types.Table, op parser.Operator) (*types.Table, error) {
	switch o := op.(type) {
	case *parser.WhereOp:
		return e.applyWhere(input, o)
	case *parser.ProjectOp:
		return e.applyProject(input, o)
	case *parser.ExtendOp:
		return e.applyExtend(input, o)
	case *parser.TakeOp:
		return e.applyTake(input, o)
	case *parser.SampleOp:
		return e.applySample(input, o)
	case *parser.CountOp:
		return e.applyCount(input)
	case *parser.DistinctOp:
		return e.applyDistinct(input, o)
	case *parser.SampleDistinctOp:
		return e.applySampleDistinct(input, o)
	case *parser.OrderByOp:
		return e.applyOrderBy(input, o)
	case *parser.TopOp:
		return e.applyTop(input, o)
	case *parser.SummarizeOp:
		return e.applySummarize(input, o)
	case *parser.JoinOp:
		return e.applyJoin(input, o)
	case *parser.LookupOp:
		return e.applyLookup(input, o)
	case *parser.RenderOp:
		// Visualization hint only; no effect on tabular results
		return input, nil
	case *parser.UnionOp:
		return e.applyUnion(input, o)
	case *parser.MvExpandOp:
		return e.applyMvExpand(input, o)
	case *parser.MvApplyOp:
		return e.applyMvApply(input, o)
	case *parser.PartitionOp:
		return e.applyPartition(input, o)
	case *parser.ProjectAwayOp:
		return e.applyProjectAway(input, o)
	case *parser.ProjectRenameOp:
		return e.applyProjectRename(input, o)
	case *parser.ProjectReorderOp:
		return e.applyProjectReorder(input, o)
	case *parser.ProjectKeepOp:
		return e.applyProjectKeep(input, o)
	case *parser.ProjectByNamesOp:
		return e.applyProjectByNames(input, o)
	case *parser.SerializeOp:
		return e.applySerialize(input, o)
	case *parser.PrintOp:
		return e.applyPrint(o)
	case *parser.DataTableOp:
		return e.applyDataTable(o)
	case *parser.RangeOp:
		return e.applyRange(o)
	case *parser.ParseOp:
		return e.applyParse(input, o)
	case *parser.ParseWhereOp:
		return e.applyParseWhere(input, o)
	case *parser.ParseKVOp:
		return e.applyParseKV(input, o)
	case *parser.MakeSeriesOp:
		return e.applyMakeSeries(input, o)
	case *parser.GetSchemaOp:
		return e.applyGetSchema(input)
	case *parser.AsOp:
		return e.applyAs(input, o)
	case *parser.ScanOp:
		return e.applyScan(input, o)
	case *parser.InvokeOp:
		return e.applyInvoke(input, o)
	case *parser.EvaluateOp:
		return e.applyEvaluate(input, o)
	default:
		return nil, fmt.Errorf("unsupported operator: %T", op)
	}
}

// rowKey generates a string key for grouping/dedup.
func rowKey(row types.Row, indices []int, schema *types.Schema) string {
	parts := make([]string, len(indices))
	for i, idx := range indices {
		parts[i] = types.FormatValue(row[idx], schema.Columns[idx].Type)
	}
	return strings.Join(parts, "\x00")
}
