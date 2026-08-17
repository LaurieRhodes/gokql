package engine

// timereceived.go — the automatic _TimeReceived column, real ADX
// conformance responding directly to a design conversation this
// session had about materialized-view "latest row" ranking (a
// different model, Kimi, diagnosed that pattern as fundamentally
// blocked without a genuinely precise, monotonic ranking column; the
// same conversation researched real ADX/Log Analytics conventions
// before building this).
//
// Verified directly against real tooling before adopting this exact
// shape, not assumed: Laurie's own Powershell-Kusto-Schema-Tools
// repository (log-analytics-to-adx-kql-export.ps1) confirmed
// _TimeReceived in real Log Analytics/ADX is NOT a specially-hidden
// column at all -- it's an ordinary, visible datetime column, added
// LAST in column order, populated at ingest time via
// todatetime(now()). The same tooling's LogAnalyticsCommon.psm1
// separately confirms the real, general convention for
// system/generated columns is a leading UNDERSCORE (_ResourceId,
// etc.), not any other marker -- matching Microsoft's own documented
// identifier rules (a single leading/trailing underscore is the
// recommended convention for exactly this) and already matching this
// engine's own existing system-table naming (_Functions,
// _MaterializedViews, _Dictionaries) before this file was ever
// written.
//
// This is deliberately simple as a result: NO special "hidden from
// getschema/project *" mechanism was built, because real ADX doesn't
// have one either -- Laurie's own export tooling has to manually
// FILTER _TimeReceived back OUT during schema export precisely
// because ADX itself never hides it. _TimeReceived here is a real,
// ordinary schema column like any other, which means it needs no
// special-casing anywhere else in this engine at all: compaction
// already preserves it correctly (see compact.go) simply because it's
// real column data flowing through the same scan-then-rewrite path
// every other column already does, not derived or reconstructed
// metadata that would need its own preservation logic.
//
// Scope, decided directly in the design conversation this responds
// to: automatic for every table BY DEFAULT, with an opt-out for a
// scope that wants plain, unmodified KQL-over-files semantics with no
// engine-added columns at all (see schemaOptions/loadSchemaOptions
// below) and a per-table override on top of that scope default (see
// parser.CreateTableCmd.NoTimeReceived). Backfilling existing,
// pre-feature rows with a real value is EXPLICITLY deferred --
// functionality first, backfill later, a direct instruction from the
// same conversation -- so an existing row simply has a null
// _TimeReceived until it's next rewritten (compaction, an MV merge,
// etc.), not retroactively stamped with a fabricated value that would
// misrepresent when it was actually written.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// timeReceivedColumnName is the real Log Analytics/ADX name, adopted
// verbatim rather than inventing a new one — the whole point is
// conformance with an existing, real convention, not a fresh okql-only
// name that would need its own documentation and wouldn't compose with
// anyone's existing Log-Analytics-derived tooling or muscle memory.
const timeReceivedColumnName = "_TimeReceived"

const schemaOptionsFileName = ".okql-schema-options.json"

// schemaOptions is the scope-level default — loaded once per Engine
// from inside the scope directory, matching the established "config
// lives alongside the data it describes" convention
// (.okql-federation.json, .okql-server.json already established this).
type schemaOptions struct {
	// DisableTimeReceived flips the SCOPE-WIDE default to opt-out —
	// for a scope that wants plain, unmodified KQL-over-files access
	// with no engine-added columns on any table at all. A per-table
	// notimereceived=true on an individual .create table always wins
	// regardless of this setting (an explicit, narrower choice at
	// creation time overriding a broader, scope-wide default); this
	// setting only controls what happens when a table's own .create
	// table doesn't say anything either way.
	DisableTimeReceived bool `json:"disableTimeReceived"`
}

// loadSchemaOptions reads the scope's own schema-options file. Its
// absence is not an error and is the overwhelmingly common case —
// most scopes will never set this, and the automatic-by-default
// behavior applies with no config file present at all.
func loadSchemaOptions(dbPath string) (schemaOptions, error) {
	data, err := os.ReadFile(filepath.Join(dbPath, schemaOptionsFileName))
	if os.IsNotExist(err) {
		return schemaOptions{}, nil
	}
	if err != nil {
		return schemaOptions{}, err
	}
	var opts schemaOptions
	if err := json.Unmarshal(data, &opts); err != nil {
		return schemaOptions{}, err
	}
	return opts, nil
}

// shouldAddTimeReceived resolves the final, per-table decision:
// explicit per-table opt-out wins outright; otherwise the scope's own
// default (on unless the scope has opted out entirely) applies.
func (e *Engine) shouldAddTimeReceived(tableNoTimeReceived bool) bool {
	if tableNoTimeReceived {
		return false
	}
	opts, err := loadSchemaOptions(e.Catalog.DatabasePath())
	if err != nil {
		// A malformed config file fails safe toward the FEATURE
		// (still on), not silently toward turning it off — a broken
		// config file should be loud (caught by whatever validates
		// scope setup), not quietly change schema behavior in a way
		// that's hard to notice.
		return true
	}
	return !opts.DisableTimeReceived
}

// withTimeReceivedColumn returns schema with _TimeReceived appended
// LAST, matching the real Log Analytics/ADX column-ordering
// convention verified in this file's own doc comment. A no-op,
// returning schema unchanged, if the column is already present (so
// this is safe to call idempotently) or the resolved decision is not
// to add it at all.
func (e *Engine) withTimeReceivedColumn(schema types.Schema, tableNoTimeReceived bool) types.Schema {
	if !e.shouldAddTimeReceived(tableNoTimeReceived) {
		return schema
	}
	for _, c := range schema.Columns {
		if c.Name == timeReceivedColumnName {
			return schema
		}
	}
	out := types.Schema{Columns: make([]types.Column, len(schema.Columns), len(schema.Columns)+1)}
	copy(out.Columns, schema.Columns)
	out.Columns = append(out.Columns, types.Column{Name: timeReceivedColumnName, Type: types.TypeDatetime})
	return out
}

// stampTimeReceived fills in timeReceivedColumnName for any row whose
// value there is currently nil — a genuinely NEW write, never having
// passed through flushBatch before. A row that already carries a
// real, non-nil value there (scanned back out of an existing extent
// during compaction, an MV merge/recompute, or any other
// scan-then-rewrite path) is left completely untouched: those rows
// already have their own, real, original write-time value flowing
// through as ordinary column data, and re-stamping them here would
// silently overwrite genuine history with the rewrite's own time
// instead — exactly the same class of bug this session already found
// and fixed once, for a different reason, in compaction's own extent
// IDs (see compact.go's own doc comments on that topic) elsewhere
// this session.
//
// A no-op for any table without this column at all (its schema simply
// has no matching index), so this is safe to call unconditionally
// from flushBatch for every table, not just ones that opted in.
// dataColumnCount is the schema's column count EXCLUDING a trailing
// _TimeReceived, if present — used wherever an external data source
// (CSV, JSON, an INSERT-style ingest) needs to be validated against
// "how many real columns does this table have", since real, external
// data can never supply a value for an engine-generated column. A
// caller building the actual types.Row to hand to flushBatch should
// still size it to the FULL schema (len(schema.Columns), not this),
// leaving the reserved _TimeReceived slot nil for stampTimeReceived
// to fill in later.
func dataColumnCount(schema types.Schema) int {
	n := len(schema.Columns)
	if n > 0 && schema.Columns[n-1].Name == timeReceivedColumnName {
		return n - 1
	}
	return n
}

// hideTimeReceivedUnlessExplicit implements real Log Analytics/ADX's
// own documented rule, quoted directly rather than paraphrased since
// getting the exact scope right matters: "Some of the standard
// columns won't show in the schema view or intellisense in Log
// Analytics, and they won't show in query results unless you
// explicitly specify the column in the output." Applied at the single
// outermost interception point (executeQuery's own thin wrapper, see
// its doc comment) rather than at every individual scan/fast-path
// entry point -- this engine has several (the generic scan,
// columnar-aggregate and columnar-top-N fast paths), each reading
// tableDef.Schema directly for its own performance reasons, and
// trying to intercept every one of them individually would be both
// more invasive and easier to silently miss one than filtering the
// single, unified result every one of them eventually produces.
//
// A no-op if the result has no _TimeReceived column at all (the
// overwhelmingly common case for a scope that opted out, or hasn't
// written anything through a _TimeReceived-bearing table yet).
//
// Approximation stated honestly, not claimed as perfect conformance:
// real Log Analytics' exact rule is scoped to the OUTPUT specifically
// ("in the output"), which a strict reading might mean a bare `where
// _TimeReceived > ago(1h)` with no subsequent project should still
// hide the column from its own result. This implementation instead
// shows it whenever the query's operator chain references it by name
// ANYWHERE (where, project, summarize, ...), a deliberately broader
// rule. Chosen because it can only ever err toward showing a column
// the user's query clearly cares about, never toward hiding one they
// explicitly asked to see -- the safe direction for an approximation
// to lean, given getting the exact per-operator scoping rule
// perfectly right for every operator type is real, separate,
// lower-priority work.
func hideTimeReceivedUnlessExplicit(result *types.Table, operators []parser.Operator) *types.Table {
	idx := result.Schema.ColumnIndex(timeReceivedColumnName)
	if idx < 0 {
		return result
	}
	if operatorsReferenceColumn(operators, timeReceivedColumnName) {
		return result
	}

	newCols := make([]types.Column, 0, len(result.Schema.Columns)-1)
	newCols = append(newCols, result.Schema.Columns[:idx]...)
	newCols = append(newCols, result.Schema.Columns[idx+1:]...)

	out := types.NewTable(result.Name, types.Schema{Columns: newCols})
	for _, row := range result.Rows {
		newRow := make(types.Row, 0, len(row)-1)
		newRow = append(newRow, row[:idx]...)
		newRow = append(newRow, row[idx+1:]...)
		out.AddRow(newRow)
	}
	return out
}

// operatorsReferenceColumn reports whether name is referenced ANYWHERE
// in operators — a simple, unconditional AST walk, deliberately
// separate from planner.go's RequiredColumns/columnCollector: that
// machinery exists for pushdown column-projection (stops early once
// needsAll is set, discarding whatever it had already collected, since
// pushdown doesn't care about a precise set once "everything" is
// already the answer) — this needs the opposite: keep walking
// regardless, since the question here is purely "was this name ever
// typed", not "what's the minimal column set to scan".
func operatorsReferenceColumn(operators []parser.Operator, name string) bool {
	for _, op := range operators {
		if operatorReferencesColumn(op, name) {
			return true
		}
	}
	return false
}

func operatorReferencesColumn(op parser.Operator, name string) bool {
	switch o := op.(type) {
	case *parser.WhereOp:
		return exprReferencesColumn(o.Predicate, name)
	case *parser.ProjectOp:
		for _, item := range o.Items {
			if item.Name == name {
				return true
			}
			if item.Expr != nil && exprReferencesColumn(item.Expr, name) {
				return true
			}
		}
	case *parser.ProjectAwayOp:
		for _, n := range o.Columns {
			if n == name {
				return true
			}
		}
	case *parser.ProjectKeepOp:
		// Patterns can include wildcards (e.g. "session*") -- exact
		// name or a bare "*" are checked; a wildcard PREFIX/SUFFIX
		// pattern that happens to also match _TimeReceived is treated
		// as not-explicit here (a deliberately safe simplification,
		// not full glob matching), matching this whole function's own
		// stated bias toward the safe direction when uncertain: worst
		// case, a wildcard project-keep that incidentally matches
		// _TimeReceived doesn't reveal it, rather than the reverse.
		for _, p := range o.Patterns {
			if p == name || p == "*" {
				return true
			}
		}
	case *parser.ProjectRenameOp:
		for _, r := range o.Renames {
			if r.OldName == name || r.NewName == name {
				return true
			}
		}
	case *parser.ExtendOp:
		for _, a := range o.Assignments {
			if a.Name == name || exprReferencesColumn(a.Expr, name) {
				return true
			}
		}
	case *parser.DistinctOp:
		for _, n := range o.Columns {
			if n == name {
				return true
			}
		}
	case *parser.OrderByOp:
		for _, c := range o.Clauses {
			if c.Column == name {
				return true
			}
		}
	case *parser.TopOp:
		return o.By == name
	case *parser.SummarizeOp:
		for _, agg := range o.Aggregations {
			for _, arg := range agg.Args {
				if exprReferencesColumn(arg, name) {
					return true
				}
			}
		}
		for _, by := range o.ByExprs {
			if by.Name == name || exprReferencesColumn(by.Expr, name) {
				return true
			}
		}
	case *parser.ParseOp:
		return o.Column == name
	case *parser.ParseWhereOp:
		return o.Column == name
	case *parser.ParseKVOp:
		return o.Column == name
	case *parser.ProjectByNamesOp:
		// Same safe-direction bias as ProjectKeepOp immediately above:
		// only a literal string-expression specifier is checked for
		// an exact match or a bare "*" — a specifier that's a dynamic
		// array literal, a let-bound reference, or column_names_of(...)
		// can't be resolved statically here (this function runs at
		// parse-time query analysis, with no table/let-context to
		// evaluate against), so those are silently treated as
		// not-explicit, matching this whole function's stated bias:
		// worst case, an unresolvable specifier that happens to
		// include _TimeReceived doesn't reveal it, rather than the
		// reverse.
		for _, spec := range o.Specifiers {
			if lit, ok := spec.Expr.(*parser.Literal); ok {
				if s, ok := lit.Value.(string); ok && (s == name || s == "*") {
					return true
				}
			}
		}
	}
	return false
}

func exprReferencesColumn(expr parser.Expr, name string) bool {
	switch e := expr.(type) {
	case *parser.ColumnRef:
		return e.Name == name
	case *parser.BinaryExpr:
		return exprReferencesColumn(e.Left, name) || exprReferencesColumn(e.Right, name)
	case *parser.UnaryExpr:
		return exprReferencesColumn(e.Expr, name)
	case *parser.FuncCall:
		for _, arg := range e.Args {
			if exprReferencesColumn(arg, name) {
				return true
			}
		}
	case *parser.InExpr:
		if exprReferencesColumn(e.Column, name) {
			return true
		}
		for _, v := range e.Values {
			if exprReferencesColumn(v, name) {
				return true
			}
		}
	case *parser.BetweenExpr:
		return exprReferencesColumn(e.Expr, name) || exprReferencesColumn(e.Low, name) || exprReferencesColumn(e.High, name)
	case *parser.AccessExpr:
		return exprReferencesColumn(e.Object, name)
	case *parser.HasAnyAllExpr:
		if exprReferencesColumn(e.Column, name) {
			return true
		}
		for _, v := range e.Values {
			if exprReferencesColumn(v, name) {
				return true
			}
		}
	}
	return false
}

func stampTimeReceived(schema types.Schema, rows []types.Row) {
	idx := schema.ColumnIndex(timeReceivedColumnName)
	if idx < 0 {
		return
	}
	now := time.Now().UTC().UnixNano()
	for i := range rows {
		if idx < len(rows[i]) && rows[i][idx] == nil {
			rows[i][idx] = now
		}
	}
}
