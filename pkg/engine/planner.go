package engine

// planner.go analyses a KQL operator pipeline to determine which columns
// are needed from the storage layer, enabling column projection pushdown.
//
// Key insights:
//
//  1. summarize/count/distinct are schema barriers — operators after them
//     work on computed output, not storage columns.
//
//  2. "Full-row" operators (take, order by, top) need all columns ONLY if
//     no narrowing operator (project, summarize, distinct) appears in the
//     pipeline. If a narrowing operator already limits the columns, full-row
//     operators work within that narrowed set.
//
//  3. where/extend add column dependencies but don't narrow — they reference
//     storage columns that must be included in the scan.

import (
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// RequiredColumns analyses a pipeline and returns the minimum set of columns
// that must be read from storage. Returns nil if all columns are needed.
func RequiredColumns(schema *types.Schema, operators []parser.Operator) []string {
	if len(operators) == 0 {
		return nil // Bare table name → all columns
	}

	// First pass: check if any narrowing operator exists before a schema barrier.
	// If not, full-row operators force all columns.
	hasNarrowing := false
	for _, op := range operators {
		switch op.(type) {
		case *parser.ProjectOp, *parser.SummarizeOp, *parser.CountOp, *parser.DistinctOp, *parser.SampleDistinctOp, *parser.JoinOp, *parser.PrintOp:
			hasNarrowing = true
		}
		if isSchemaBarrier(op) {
			break
		}
	}

	refs := &columnCollector{
		schema:       schema,
		columns:      make(map[string]bool),
		needsAll:     false,
		hasNarrowing: hasNarrowing,
	}

	// Second pass: collect column references.
	for _, op := range operators {
		refs.collectOperator(op)
		if refs.needsAll {
			return nil
		}
		if isSchemaBarrier(op) {
			break
		}
	}

	// If no narrowing operator exists (project/summarize/distinct/count),
	// the query outputs all source columns. where/extend filter or add
	// but don't reduce the output schema.
	if !refs.needsAll && !hasNarrowing {
		return nil // All columns needed
	}

	if len(refs.columns) == 0 {
		// Only count/take with no column references — need one column for row iteration
		return []string{schema.Columns[0].Name}
	}

	// Build result preserving schema column order
	result := make([]string, 0, len(refs.columns))
	for _, col := range schema.Columns {
		if refs.columns[col.Name] {
			result = append(result, col.Name)
		}
	}
	return result
}

func isSchemaBarrier(op parser.Operator) bool {
	switch op.(type) {
	case *parser.SummarizeOp, *parser.CountOp, *parser.DistinctOp, *parser.SampleDistinctOp, *parser.JoinOp, *parser.UnionOp, *parser.GetSchemaOp:
		return true
	default:
		return false
	}
}

type columnCollector struct {
	schema       *types.Schema
	columns      map[string]bool
	needsAll     bool
	hasNarrowing bool // True if project/summarize/distinct exists in pipeline
}

func (c *columnCollector) collectOperator(op parser.Operator) {
	switch o := op.(type) {
	case *parser.WhereOp:
		c.collectExpr(o.Predicate)

	case *parser.ProjectOp:
		for _, item := range o.Items {
			if item.Expr != nil {
				c.collectExpr(item.Expr)
			} else {
				c.addColumn(item.Name)
			}
		}

	case *parser.ExtendOp:
		for _, assign := range o.Assignments {
			c.collectExpr(assign.Expr)
		}

	case *parser.TakeOp:
		// take returns full rows. If no narrowing operator limits columns,
		// we need all columns. If a project/summarize already narrows, take
		// works within that narrowed set.
		if !c.hasNarrowing {
			c.needsAll = true
		}

	case *parser.CountOp:
		// count needs no specific columns

	case *parser.DistinctOp:
		for _, name := range o.Columns {
			c.addColumn(name)
		}

	case *parser.SampleDistinctOp:
		c.addColumn(o.Column)

	case *parser.OrderByOp:
		if !c.hasNarrowing {
			c.needsAll = true
		}
		// If there IS a narrowing operator, order by's columns should still be collected
		for _, clause := range o.Clauses {
			c.addColumn(clause.Column)
		}

	case *parser.TopOp:
		if !c.hasNarrowing {
			c.needsAll = true
		}
		c.addColumn(o.By)

	case *parser.SummarizeOp:
		for _, agg := range o.Aggregations {
			for _, arg := range agg.Args {
				// A bare * argument (arg_max/arg_min's real-ADX
				// wildcard, added this session) means "every source
				// column is needed" by definition -- collectExpr has
				// no case for parser.StarExpr at all (a plain marker
				// type, not a column reference collectExpr could ever
				// meaningfully walk into), so without this explicit
				// check the pushdown analysis would silently conclude
				// only agg.Args[0] and the group-by keys were needed.
				// Found live: T | summarize arg_max(Seq, *) by Id
				// against a real, disk-backed table (not an in-memory
				// datatable, which never exercises this pushdown path
				// at all) silently omitted Status -- present in T's
				// own schema, absent from the summarize output
				// entirely -- exactly the same class of gap this
				// session already found and fixed once for
				// HasAnyAllExpr and LookupOp: a new AST node type the
				// planner's column-collection switch had no case for.
				if _, isStar := arg.(*parser.StarExpr); isStar {
					c.needsAll = true
					continue
				}
				c.collectExpr(arg)
			}
		}
		for _, byExpr := range o.ByExprs {
			c.collectExpr(byExpr.Expr)
		}

	case *parser.JoinOp:
		// Join output includes ALL left columns plus non-key right columns,
		// so we always need all left-side columns from storage.
		c.needsAll = true

	case *parser.LookupOp:
		// Same reasoning as JoinOp immediately above — lookup's output
		// also includes all left columns plus right-side ones, so it
		// needs all left-side columns from storage too. Found live:
		// LookupOp had NO case here at all (fell through doing
		// nothing, neither needsAll nor collecting the on-clause's
		// own column references), so `... | lookup T on $left.Col ==
		// $right.Col | count` silently omitted Col from the scan
		// whenever count (or any other narrowing operator) followed —
		// lookup's own where-clause-shaped `on` condition was simply
		// invisible to this analysis. The bare lookup with no
		// downstream narrowing never hit this, since with no pushdown
		// triggered at all every column gets scanned regardless.
		c.needsAll = true

	case *parser.ProjectAwayOp:
		// project-away removes columns, but needs all others
		c.needsAll = true

	case *parser.ProjectRenameOp:
		// project-rename needs all columns (just renames some)
		c.needsAll = true

	case *parser.ParseOp:
		c.columns[o.Column] = true
	case *parser.ParseWhereOp:
		c.columns[o.Column] = true
	case *parser.ParseKVOp:
		c.columns[o.Column] = true
	case *parser.MakeSeriesOp:
		// Conservative: needsAll rather than collecting AxisColumn +
		// aggregation args + by-expressions individually. make-series
		// is a source-narrowing operator with real, if uncommon,
		// arbitrary-expression support in both its aggregation and
		// group-by clauses (real ADX's own docs: "you can provide
		// arbitrary expressions for both the aggregation and grouping
		// expressions") -- correctness here matters far more than the
		// pushdown optimization this would unlock, and needsAll is
		// always safe.
		c.needsAll = true
	case *parser.GetSchemaOp:
		// Needs schema info but not row data — one column suffices
	case *parser.PrintOp:
		// print produces its own output, no source columns needed
	case *parser.RenderOp:
		// render is a visualization hint; needs no columns
	case *parser.MvApplyOp:
		// mv-apply copies all original columns onto expanded rows
		c.needsAll = true
	case *parser.MakeGraphOp:
		// make-graph carries full edge rows into the graph
		c.needsAll = true

	default:
		// Deliberately fail SAFE, not silent, for any operator type
		// this switch doesn't yet recognize by name — responds
		// directly to a structural pattern independently spotted and
		// named by a different model's testing (Kimi) after this same
		// bug class (a new AST node type missing from this exact
		// switch) was independently found and fixed three separate
		// times this session (StarExpr, HasAnyAllExpr, LookupOp),
		// always the same way: silently under-collect required
		// columns, producing wrong data with no error at all, and
		// always invisible to every existing test because every one
		// of them used an in-memory datatable literal, which never
		// exercises this pushdown analysis in the first place.
		//
		// A bare panic (the more obvious fix) was considered and
		// rejected: it only helps if some test happens to exercise the
		// exact new type, same blind spot as the bug class itself.
		// needsAll=true instead changes the FAILURE MODE structurally,
		// for every future case, not just ones a test happens to
		// cover: an operator type this switch doesn't recognize now
		// costs a full-column scan (a real but bounded performance
		// cost) instead of silently dropping data (a correctness bug,
		// unbounded in how wrong the output can be). This can never
		// again be the specific way this bug class manifests, even
		// for an operator type added after this comment is long
		// forgotten.
		c.needsAll = true
	}
}

func (c *columnCollector) collectExpr(expr parser.Expr) {
	switch e := expr.(type) {
	case *parser.ColumnRef:
		c.addColumn(e.Name)
	case *parser.BinaryExpr:
		c.collectExpr(e.Left)
		c.collectExpr(e.Right)
	case *parser.UnaryExpr:
		c.collectExpr(e.Expr)
	case *parser.FuncCall:
		for _, arg := range e.Args {
			c.collectExpr(arg)
		}
	case *parser.Literal:
		// No column reference
	case *parser.InExpr:
		c.collectExpr(e.Column)
		for _, val := range e.Values {
			c.collectExpr(val)
		}
	case *parser.BetweenExpr:
		c.collectExpr(e.Expr)
		c.collectExpr(e.Low)
		c.collectExpr(e.High)
	case *parser.AccessExpr:
		c.collectExpr(e.Object)
	case *parser.HasAnyAllExpr:
		// Found live: `Findings | where Claim has_any (...) | count`
		// failed with "column Claim not found" -- has_any/has_all's
		// Column reference was never discovered here (no case existed
		// for this node type at all), so the storage scan silently
		// omitted Claim from the required-columns set whenever a
		// downstream narrowing operator (count, summarize, project,
		// ...) was present to trigger pushdown at all. The same
		// where clause with no downstream narrowing never hit this,
		// since with no narrowing the scan just reads every column
		// regardless -- which is exactly why this didn't reproduce on
		// a first, simpler test and only showed up in a broader sweep
		// that happened to pipe into count. InExpr, immediately above,
		// already has the identical Column-plus-value-list shape and
		// was already handled correctly -- this was a gap specific to
		// HasAnyAllExpr, not a systemic omission across every
		// expression type (checked: BetweenExpr, InExpr, BinaryExpr,
		// UnaryExpr, AccessExpr were all already present).
		c.collectExpr(e.Column)
		for _, val := range e.Values {
			c.collectExpr(val)
		}

	case *parser.StarExpr:
		// Already handled specially, before ever reaching collectExpr,
		// by SummarizeOp's own case above (a bare * means "every
		// source column", which collectExpr as a per-expression walker
		// has no way to express on its own). Listed here explicitly,
		// as a genuine no-op, so a future direct collectExpr(*StarExpr)
		// call from somewhere else in this codebase doesn't fall into
		// the conservative default below and force an unnecessary
		// full scan for a case that's actually already correctly
		// handled at the call site.

	default:
		// Same reasoning as collectOperator's own default case,
		// immediately above this function in the file — fail safe
		// (scan more than strictly necessary) rather than silently
		// drop columns, for any expression type this switch doesn't
		// yet recognize by name. See that case's own, longer comment
		// for the full context (the same bug class, found and fixed
		// three separate times this session, that this default exists
		// to structurally close off going forward).
		c.needsAll = true
	}
}

func (c *columnCollector) addColumn(name string) {
	if c.schema.ColumnIndex(name) >= 0 {
		c.columns[name] = true
	}
}

// ScanRowLimit determines the maximum number of rows the storage scan
// must produce for the pipeline to be answered correctly. Returns
// (limit, true) when a take/limit operator is reachable through only
// row-preserving 1:1 operators (project variants, extend, parse,
// serialize, render) — no operator before it may drop, add, or reorder
// rows. Returns (0, false) when the scan must read everything.
// KQL's take is an arbitrary-subset operator, so serving it the first N
// scanned rows is valid.
func ScanRowLimit(operators []parser.Operator) (int64, bool) {
	for _, op := range operators {
		switch o := op.(type) {
		case *parser.TakeOp:
			return o.Count, true
		case *parser.ProjectOp, *parser.ExtendOp, *parser.ProjectAwayOp,
			*parser.ProjectRenameOp, *parser.ProjectReorderOp,
			*parser.ProjectKeepOp, *parser.ProjectByNamesOp,
			*parser.SerializeOp, *parser.RenderOp,
			*parser.ParseOp, *parser.ParseKVOp:
			// ProjectByNamesOp is 1:1 row-preserving exactly like
			// ProjectKeepOp, its closest sibling (see
			// applyProjectByNames's own doc comment for the one real
			// difference between them — reordering, not row shape).
			// Safe here even in ProjectByNamesOp's own zero-matching-
			// column edge case (an empty-schema, zero-row result) for
			// the same reason ProjectKeepOp's identical edge case
			// already is: the pushed-down row limit only bounds how
			// many SOURCE rows are scanned, and a zero-column project
			// produces an empty result regardless of that bound, so
			// the optimization stays correct either way.
			// ParseKVOp only extends columns, same 1:1 row shape as
			// ParseOp — safe here. ParseWhereOp is deliberately NOT
			// listed: it drops non-matching rows, so it is not a 1:1
			// row-preserving operator and must fall through to the
			// conservative default below.
			continue
		default:
			return 0, false
		}
	}
	return 0, false
}
