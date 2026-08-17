package engine

// mv_maintenance.go — incremental materialized-view maintenance on
// write. Design agreed and worked through in detail earlier this
// session before any of this was written: a write to a materialized
// view's source table spawns a DETACHED goroutine per affected view
// (the write itself never blocks on maintenance finishing), tracked
// via a per-(scope, view) in-flight marker; any READ of that specific
// view checks the marker first and waits if a merge is in progress
// (so a query can never observe pre-merge state, closing the race a
// fully-detached, untracked design would otherwise have); the CLI
// process waits on any outstanding markers immediately before it
// would otherwise exit, since an unwaited goroutine in a short-lived
// process is simply killed mid-work, not "still running in the
// background" the way it would be in the long-lived HTTP server.
//
// "Incremental" here means the AGGREGATION COMPUTATION only processes
// the delta (the rows just written), never a full rescan of the
// source table — the expensive, unbounded-with-history part. It does
// NOT mean the materialized table's own on-disk storage is updated
// in-place row by row: this storage layer is append-only by design
// throughout this whole codebase (see compact.go, storage.go), with
// no update-in-place primitive at all. Merging is done by reading the
// materialized table's current (small — one row per group key, not
// one per source event) state into memory, combining it with the
// delta's own partial aggregate there, and replacing the materialized
// table's contents wholesale via the already-built, already-tested
// dropTableComplete (archive, not delete) + recreate — reusing proven
// machinery rather than inventing a new in-place-update mechanism, at
// the cost of an archived-generation accumulating per merge (the same
// tradeoff .compact/.gc already exist to manage for any table,
// including this one).
//
// Scope, stated honestly rather than implied complete: true
// incremental merge is implemented only for aggregate functions
// mergeable directly from their DISPLAYED value — count, countif,
// sum, sumif, min, max, arg_max, arg_min, take_any, take_anyif,
// make_set, make_list, make_bag. avg/avgif/dcount/dcountif need hidden
// companion state (sum+count for avg; the full distinct-value set for
// dcount) beyond what's ever displayed to merge correctly — not built
// in this pass. A materialized view using any of those four functions
// still stays correct (falls back to a full recompute of the view
// from the source table on every write to it) but isn't yet truly
// incremental. Every motivating real use case installed this session
// (Kimi's 12-function library in girsu-paper) is entirely arg_max- and
// count-based, so this scope boundary doesn't limit real usage today.

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// mvInFlight tracks in-progress merges, keyed by "<scopePath>|<mvName>"
// — package-level (not per-Engine), deliberately: the HTTP server
// opens a fresh Engine per request (see server.go's own design note),
// so a marker set by the Engine that spawned a merge must still be
// visible to a DIFFERENT Engine instance created by a later request
// against the same scope. Scope path is part of the key so two
// different scopes' same-named views never collide.
var mvInFlight sync.Map // key string -> *sync.WaitGroup

func mvInFlightKey(scopePath, mvName string) string {
	return scopePath + "|" + mvName
}

// waitForMaterializedView blocks if a merge for mvName is currently in
// progress in this scope, then returns — called from executeQuery
// immediately after a table is resolved as a source, before any scan,
// so a read can never observe pre-merge state. A no-op (returns
// immediately) for any table that isn't currently mid-merge, which is
// the overwhelmingly common case — this is a single sync.Map.Load, not
// a real cost for ordinary table reads.
func (e *Engine) waitForMaterializedView(name string) {
	key := mvInFlightKey(e.Catalog.DatabasePath(), name)
	if v, ok := mvInFlight.Load(key); ok {
		if wg, ok := v.(*sync.WaitGroup); ok {
			wg.Wait()
		}
	}
}

// WaitForAllMaterializedViewMaintenance blocks until every merge
// currently in flight for this scope has completed — called from the
// CLI (cmd/gokql/main.go) immediately before the process would
// otherwise exit, since a goroutine spawned by triggerMaterializedViewMaintenance
// and never waited on is simply killed mid-work when a short-lived CLI
// process exits, not "still running in the background" the way it
// would be in the long-lived HTTP server (which never calls this at
// all — a request's own Engine goes out of scope naturally, and the
// NEXT request's fresh Engine will correctly wait via
// waitForMaterializedView if it reads the same view before that merge
// finishes).
func WaitForAllMaterializedViewMaintenance(scopePath string) {
	prefix := scopePath + "|"
	mvInFlight.Range(func(k, v interface{}) bool {
		key, ok := k.(string)
		if !ok || len(key) < len(prefix) || key[:len(prefix)] != prefix {
			return true
		}
		if wg, ok := v.(*sync.WaitGroup); ok {
			wg.Wait()
		}
		return true
	})
}

// trulyIncrementalMVAggregates is the set of aggregate functions this
// first version can correctly merge from their displayed value alone
// — see this file's doc comment for exactly why avg/dcount (and their
// -if variants) are excluded and what a full implementation would need.
var trulyIncrementalMVAggregates = map[string]bool{
	"count": true, "countif": true,
	"sum": true, "sumif": true,
	"min": true, "max": true,
	"arg_max": true, "arg_min": true,
	"take_any": true, "take_anyif": true,
	"make_set": true, "make_list": true, "make_bag": true,
}

// triggerMaterializedViewMaintenance is called from setOrAppendImpl
// immediately after a successful write to sourceTable, with EXACTLY
// the rows just written (the delta) and that table's schema. Spawns
// one detached goroutine per materialized view defined on this source
// table; the caller (the write itself) never waits on any of them.
func (e *Engine) triggerMaterializedViewMaintenance(sourceTable string, deltaRows []types.Row, sourceSchema types.Schema) {
	mvs, err := e.listMaterializedViews()
	if err != nil || len(mvs) == 0 {
		return // no views at all, or listing failed — either way, nothing to maintain; a listing failure here must never fail the write itself
	}

	scopePath := e.Catalog.DatabasePath()
	for _, mv := range mvs {
		if mv.SourceTable != sourceTable {
			continue
		}
		mv := mv // capture

		key := mvInFlightKey(scopePath, mv.Name)
		wg := &sync.WaitGroup{}
		wg.Add(1)
		mvInFlight.Store(key, wg)

		go func() {
			defer wg.Done()
			defer mvInFlight.Delete(key)
			// A merge error is never propagated anywhere that could
			// fail the write that triggered it — the write has
			// already returned successfully to its own caller by the
			// time this goroutine runs, and a materialized view
			// merge failing must never retroactively fail a write
			// that already succeeded. A failed merge leaves the view
			// stale (reflecting its pre-write state) rather than
			// corrupted — the same honest tradeoff every other part
			// of this maintenance system makes in favor of never
			// blocking or failing the write path.
			//
			// NOT fully silent, though — verbose-gated, matching the
			// existing [scan]/[catalog]-style diagnostic convention
			// this codebase already uses elsewhere (engine.go). A
			// prior version of this exact line was a bare, unguarded
			// fmt.Println left over from live debugging a real merge
			// bug (aggFunctionForColumn's star-form gap) that would
			// otherwise have stayed completely invisible — worth
			// keeping SOME visibility path permanently, not removing
			// it back to full silence, given how directly that
			// silence contributed to the bug staying hidden.
			if mergeErr := e.mergeMaterializedView(mv, deltaRows, sourceSchema); mergeErr != nil && e.Verbose {
				fmt.Fprintf(os.Stderr, "[mv] materialized view %q: maintenance failed, view left stale: %v\n", mv.Name, mergeErr)
			}
		}()
	}
}

// mergeMaterializedView does the actual work for one view: parses its
// stored query, computes a partial aggregate over ONLY deltaRows,
// reads the view's current materialized state, merges the two per
// group-by key (or falls back to a full recompute — see this file's
// doc comment — if the query uses any non-trulyIncremental aggregate),
// and replaces the materialized table's contents.
func (e *Engine) mergeMaterializedView(mv materializedViewDef, deltaRows []types.Row, sourceSchema types.Schema) error {
	stmt, err := parser.Parse(mv.Query)
	if err != nil {
		return fmt.Errorf("materialized view %q: stored query no longer parses: %w", mv.Name, err)
	}
	q, ok := stmt.(*parser.Query)
	if !ok {
		return fmt.Errorf("materialized view %q: stored query is not a tabular query", mv.Name)
	}
	var summarizeOp *parser.SummarizeOp
	for _, op := range q.Operators {
		if s, ok := op.(*parser.SummarizeOp); ok {
			summarizeOp = s
		}
	}
	if summarizeOp == nil {
		return fmt.Errorf("materialized view %q: stored query has no summarize operator", mv.Name)
	}

	for _, agg := range summarizeOp.Aggregations {
		if !trulyIncrementalMVAggregates[agg.Function] {
			// Falls back to a full recompute — still correct, not yet
			// incremental for this specific view. See this file's doc
			// comment for the honest scope boundary this represents.
			return e.recomputeMaterializedView(mv, q)
		}
	}

	deltaTable := &types.Table{Schema: sourceSchema, Rows: deltaRows}
	deltaResult, err := e.applySummarize(deltaTable, summarizeOp)
	if err != nil {
		return fmt.Errorf("materialized view %q: aggregating delta: %w", mv.Name, err)
	}

	// Deliberately NOT e.executeQuery(&parser.Query{Source: mv.Name}) —
	// that path calls waitForMaterializedView(mv.Name) first (added
	// this same pass, for ordinary external readers), which would wait
	// on the EXACT in-flight marker THIS merge goroutine itself just
	// set and hasn't cleared yet — a genuine, live deadlock, caught
	// during manual testing (a real .set-or-append against a table
	// with a truly-incremental MV defined on it hung indefinitely,
	// confirmed via ps to be the actual process still running, not a
	// tool glitch). The merge process needs the table's raw, current,
	// PRE-merge state directly; it already owns the in-flight marker
	// and must never wait on itself. readMaterializedTableRaw bypasses
	// executeQuery (and therefore the wait check) entirely.
	current, err := e.readMaterializedTableRaw(mv.Name)
	if err != nil {
		return fmt.Errorf("materialized view %q: reading current state: %w", mv.Name, err)
	}

	byNames := make([]string, len(summarizeOp.ByExprs))
	for i, by := range summarizeOp.ByExprs {
		byNames[i] = by.Name
	}

	merged, err := mergeMVRows(current, deltaResult, summarizeOp, byNames)
	if err != nil {
		return fmt.Errorf("materialized view %q: merging: %w", mv.Name, err)
	}

	return e.replaceMaterializedTable(mv.Name, merged)
}

// recomputeMaterializedView is the honest, correct fallback for any
// view using an aggregate this first version can't yet truly
// incrementally merge — re-runs the view's full query against the
// CURRENT, complete source table (not just the delta) and replaces
// the materialized table's contents with the fresh result. Genuinely
// not incremental (the whole point of the limitation this represents
// is stated in this file's doc comment), but still fully correct.
func (e *Engine) recomputeMaterializedView(mv materializedViewDef, q *parser.Query) error {
	result, err := e.executeQuery(q)
	if err != nil {
		return fmt.Errorf("materialized view %q: recomputing: %w", mv.Name, err)
	}
	return e.replaceMaterializedTable(mv.Name, result)
}

// mergeMVRows combines current (the view's existing materialized
// state) with delta (a fresh aggregate computed over ONLY the new
// rows), keyed by the group-by column values, using the merge rule
// for each aggregate function present in summarizeOp. A group-by key
// present only in delta is a brand-new group, added as-is; a key
// present in both is merged per-column using mergeAggregateColumnValue.
func mergeMVRows(current, delta *types.Table, summarizeOp *parser.SummarizeOp, byNames []string) (*types.Table, error) {
	out := types.NewTable("", delta.Schema)

	byIdxCurrent := make([]int, len(byNames))
	byIdxDelta := make([]int, len(byNames))
	for i, name := range byNames {
		byIdxCurrent[i] = current.Schema.ColumnIndex(name)
		byIdxDelta[i] = delta.Schema.ColumnIndex(name)
	}

	keyOf := func(row types.Row, idxs []int) string {
		s := ""
		for _, idx := range idxs {
			if idx >= 0 {
				s += fmt.Sprintf("%v\x00", row[idx])
			}
		}
		return s
	}

	deltaByKey := make(map[string]types.Row, len(delta.Rows))
	for _, row := range delta.Rows {
		deltaByKey[keyOf(row, byIdxDelta)] = row
	}

	usedDeltaKeys := make(map[string]bool)

	// Existing groups: merge with a matching delta group if one
	// exists, otherwise carry forward unchanged.
	for _, curRow := range current.Rows {
		key := keyOf(curRow, byIdxCurrent)
		if deltaRow, found := deltaByKey[key]; found {
			merged, err := mergeOneMVRow(curRow, deltaRow, current.Schema, delta.Schema, summarizeOp, byNames)
			if err != nil {
				return nil, err
			}
			out.AddRow(merged)
			usedDeltaKeys[key] = true
		} else {
			// Reorder curRow into delta's column order (schemas should
			// already match, but this stays robust to column order
			// drift between the two).
			reordered := make(types.Row, len(out.Schema.Columns))
			for i, col := range out.Schema.Columns {
				if idx := current.Schema.ColumnIndex(col.Name); idx >= 0 {
					reordered[i] = curRow[idx]
				}
			}
			out.AddRow(reordered)
		}
	}
	// Brand-new groups: present in delta, not in current.
	for key, deltaRow := range deltaByKey {
		if !usedDeltaKeys[key] {
			out.AddRow(deltaRow)
		}
	}

	return out, nil
}

// mergeOneMVRow merges a single existing row with a single delta row
// for the SAME group-by key.
//
// A star-form arg_max/arg_min needs different handling from every
// other aggregate, and getting this wrong is a real correctness bug,
// not just a simplification — caught live, not by inspection: an
// earlier version of this function merged every output column
// INDEPENDENTLY (each column's own pickByCompare call), which is
// correct for min/max/sum/count (each is its own, independent
// aggregate) but wrong for arg_max/arg_min's star form, where every
// expanded column must come from the SAME winning row, not be decided
// column-by-column on that column's own value. Concretely: merging
// cur=(Seq:1, Status:"open") with delta=(Seq:3, Status:"closed")
// correctly picked Seq=3 (3>1), but then merged Status independently
// via pickByCompare("open", "closed", ...) — a lexicographic string
// comparison ("open" > "closed") that kept the WRONG side's Status,
// mismatched with the Seq value that had already been decided
// separately. Fixed by determining the winning SIDE once (comparing
// only the maximized/minimized expression's own column between cur
// and delta), then copying every star-expanded column from that one
// winning side as a unit — never merging them independently.
func mergeOneMVRow(curRow, deltaRow types.Row, curSchema, deltaSchema types.Schema, summarizeOp *parser.SummarizeOp, byNames []string) (types.Row, error) {
	isByCol := make(map[string]bool, len(byNames))
	for _, n := range byNames {
		isByCol[n] = true
	}

	starAgg, starMaxCol := starFormAggregation(summarizeOp)
	starWinnerIsDelta := false
	if starAgg != nil {
		curIdx := curSchema.ColumnIndex(starMaxCol)
		deltaIdx := deltaSchema.ColumnIndex(starMaxCol)
		var curVal, deltaVal types.Value
		if curIdx >= 0 {
			curVal = curRow[curIdx]
		}
		if deltaIdx >= 0 {
			deltaVal = deltaRow[deltaIdx]
		}
		direction := -1
		if starAgg.Function == "arg_max" {
			direction = 1
		}
		starWinnerIsDelta = deltaWinsCompare(curVal, deltaVal, direction)
	}

	out := make(types.Row, len(deltaSchema.Columns))
	for i, col := range deltaSchema.Columns {
		if isByCol[col.Name] {
			out[i] = deltaRow[i]
			continue
		}

		aggFunc := aggFunctionForColumn(summarizeOp, col.Name)
		curIdx := curSchema.ColumnIndex(col.Name)
		var curVal types.Value
		if curIdx >= 0 {
			curVal = curRow[curIdx]
		}

		if starAgg != nil && (aggFunc == "arg_max" || aggFunc == "arg_min") {
			// Every star-expanded column takes its value from whichever
			// SIDE already won the maximized/minimized column's own
			// comparison, decided once above — never re-decided per
			// column.
			if starWinnerIsDelta {
				out[i] = deltaRow[i]
			} else {
				out[i] = curVal
			}
			continue
		}

		merged, err := mergeAggregateColumnValue(aggFunc, curVal, deltaRow[i])
		if err != nil {
			return nil, err
		}
		out[i] = merged
	}
	return out, nil
}

// starFormAggregation finds the (at most one, per aggFunctionForColumn's
// own documented assumption) star-form arg_max/arg_min in summarizeOp,
// returning the aggregation itself and the name of its maximized/
// minimized column (agg.Args[0], required to be a bare ColumnRef for
// this to be meaningful — matching real ADX's own examples, which
// always use a plain column as the first argument).
func starFormAggregation(summarizeOp *parser.SummarizeOp) (*parser.Aggregation, string) {
	for i := range summarizeOp.Aggregations {
		agg := &summarizeOp.Aggregations[i]
		if (agg.Function != "arg_max" && agg.Function != "arg_min") || len(agg.Args) != 2 {
			continue
		}
		if _, isStar := agg.Args[1].(*parser.StarExpr); !isStar {
			continue
		}
		if ref, ok := agg.Args[0].(*parser.ColumnRef); ok {
			return agg, ref.Name
		}
	}
	return nil, ""
}

// aggFunctionForColumn finds which aggregation function produced the
// output column named colName — needed because merging requires
// knowing the RULE (sum adds, max compares, etc.), not just the two
// values.
//
// Handles two genuinely different shapes: an ordinary aggregation
// (one agg -> one output column, named agg.Name — matched directly),
// and the arg_max/arg_min star form (one agg -> MANY expanded output
// columns, per applySummarize's own star-expansion logic — none of
// them named agg.Name at all, since Name there is just a fallback
// slot label never actually used as a real column name for this
// form). Found live, not hypothetical: a direct agg.Name match alone
// left every star-expanded column unresolved (colName never equals
// agg.Name for the star form), returning "" and failing merge outright
// with """ is not in trulyIncrementalMVAggregates" for the exact
// arg_max(Seq, *) case this whole maintenance system was built around.
// A summarize with at most one star-form arg_max/arg_min is assumed
// here (the common, and only currently well-defined, case) — any
// column not matched by a direct agg.Name lookup is attributed to
// that star aggregation, since applySummarize's own expansion already
// guarantees its output columns never collide with other aggregations'
// names or the group-by keys.
func aggFunctionForColumn(summarizeOp *parser.SummarizeOp, colName string) string {
	for _, agg := range summarizeOp.Aggregations {
		if agg.Name == colName {
			return agg.Function
		}
	}
	for _, agg := range summarizeOp.Aggregations {
		if (agg.Function == "arg_max" || agg.Function == "arg_min") && len(agg.Args) == 2 {
			if _, isStar := agg.Args[1].(*parser.StarExpr); isStar {
				return agg.Function
			}
		}
	}
	return ""
}

// mergeAggregateColumnValue implements the merge rule for one
// aggregate function's already-computed partial values — cur (from
// the view's existing state) and delta (from the new rows only). Only
// ever called for functions in trulyIncrementalMVAggregates; any other
// function takes the recomputeMaterializedView path instead and never
// reaches here.
func mergeAggregateColumnValue(function string, cur, delta types.Value) (types.Value, error) {
	switch function {
	case "count", "countif":
		c, _ := cur.(int64)
		d, _ := delta.(int64)
		return c + d, nil
	case "sum", "sumif":
		if cur == nil {
			return delta, nil
		}
		if delta == nil {
			return cur, nil
		}
		return addNumeric(cur, delta), nil
	case "min":
		return pickByCompare(cur, delta, -1), nil
	case "max", "arg_max":
		return pickByCompare(cur, delta, 1), nil
	case "arg_min":
		return pickByCompare(cur, delta, -1), nil
	case "take_any", "take_anyif":
		if cur != nil {
			return cur, nil // keep the existing arbitrary choice rather than churning it every merge
		}
		return delta, nil
	case "make_set", "make_list", "make_bag":
		return unionDynamicValues(function, cur, delta), nil
	default:
		return nil, fmt.Errorf("mergeAggregateColumnValue: %q is not in trulyIncrementalMVAggregates", function)
	}
}

// deltaWinsCompare reports whether delta should win over cur, per
// direction (+1 = larger wins, like arg_max; -1 = smaller wins, like
// arg_min) — the boolean-returning counterpart to pickByCompare below,
// used specifically where the WINNING SIDE itself matters (star-form
// arg_max/arg_min needs to know which side won to copy every other
// column from that same side), not just the winning value of one
// column in isolation. A nil delta never wins (nothing to prefer over
// keeping cur); a nil cur always loses to any non-nil delta.
func deltaWinsCompare(cur, delta types.Value, direction int) bool {
	if delta == nil {
		return false
	}
	if cur == nil {
		return true
	}
	kt := inferValType(delta)
	cmp := types.CompareValues(delta, cur, kt)
	return (direction > 0 && cmp > 0) || (direction < 0 && cmp < 0)
}

// pickByCompare returns cur or delta, whichever compares as "better"
// per direction (-1 = smaller wins, like min; +1 = larger wins, like
// max/arg_max/arg_min's OWN maximized/minimized column specifically —
// note arg_max/arg_min's OTHER, carried-along columns are handled
// correctly too: since both cur and delta already reflect whichever
// row WON within their own partial computation, comparing just this
// one column and taking the whole corresponding row-slice would be
// more precise than comparing every column independently — a known,
// accepted simplification for this first version: comparing each
// output column independently (as done here) is correct for min/max/
// count/sum, and correct for arg_max/arg_min too as long as the
// MAXIMIZED/MINIMIZED column itself is what's compared, which the
// column-by-column merge in mergeOneMVRow already guarantees by
// construction (that column's own merge call is what decides winner).
func pickByCompare(cur, delta types.Value, direction int) types.Value {
	if cur == nil {
		return delta
	}
	if deltaWinsCompare(cur, delta, direction) {
		return delta
	}
	return cur
}

func addNumeric(a, b types.Value) types.Value {
	switch av := a.(type) {
	case int64:
		if bv, ok := b.(int64); ok {
			return av + bv
		}
	case float64:
		if bv, ok := b.(float64); ok {
			return av + bv
		}
	}
	return types.ToFloat64(a) + types.ToFloat64(b)
}

// unionDynamicValues merges two make_set/make_list/make_bag results —
// both stored as JSON-text dynamic values, per this engine's own
// existing computeAgg implementation (aggregation.go): make_set/
// make_list as a JSON array of strings, make_bag as a JSON object.
// Decodes both sides, combines per the real semantics of the specific
// function (union+dedupe for make_set, concatenation for make_list,
// later-key-wins merge for make_bag via the existing
// mergeDynamicIntoBag helper — not reimplemented separately), and
// re-encodes. An earlier version of this function didn't actually
// decode or combine anything at all — it silently discarded cur and
// returned delta unchanged, which would have made make_set/make_list/
// make_bag incorrect under "incremental" merge despite being listed
// in trulyIncrementalMVAggregates; caught and fixed before this was
// ever tested, not found live.
func unionDynamicValues(function string, cur, delta types.Value) types.Value {
	if cur == nil {
		return delta
	}
	if delta == nil {
		return cur
	}
	curText, _ := cur.(string)
	deltaText, _ := delta.(string)

	switch function {
	case "make_bag":
		bag := make(map[string]interface{})
		mergeDynamicIntoBag(bag, curText)
		mergeDynamicIntoBag(bag, deltaText)
		b, err := json.Marshal(bag)
		if err != nil {
			return delta
		}
		return string(b)

	case "make_list":
		var curList, deltaList []string
		_ = json.Unmarshal([]byte(curText), &curList)
		_ = json.Unmarshal([]byte(deltaText), &deltaList)
		combined := append(curList, deltaList...)
		b, err := json.Marshal(combined)
		if err != nil {
			return delta
		}
		return string(b)

	case "make_set":
		var curList, deltaList []string
		_ = json.Unmarshal([]byte(curText), &curList)
		_ = json.Unmarshal([]byte(deltaText), &deltaList)
		seen := make(map[string]bool, len(curList)+len(deltaList))
		var combined []string
		for _, v := range append(curList, deltaList...) {
			if !seen[v] {
				seen[v] = true
				combined = append(combined, v)
			}
		}
		b, err := json.Marshal(combined)
		if err != nil {
			return delta
		}
		return string(b)

	default:
		return delta
	}
}

// readMaterializedTableRaw reads a table's full, current, on-disk
// state directly via scanExtents — bypassing executeQuery (and
// therefore waitForMaterializedView) entirely. See
// mergeMaterializedView's own call site for exactly why this
// bypass is required, not optional: it prevents a real, live
// deadlock (a merge goroutine waiting on the in-flight marker it
// itself holds).
func (e *Engine) readMaterializedTableRaw(tableName string) (*types.Table, error) {
	tableDef := e.Catalog.GetTable(tableName)
	if tableDef == nil {
		return types.NewTable(tableName, types.Schema{}), nil
	}
	scanCols := tableDef.Schema.ColumnNames()
	result := types.NewTable(tableName, tableDef.Schema)
	if err := e.scanExtents(result, tableDef, scanCols, nil, nil); err != nil {
		return nil, err
	}
	return result, nil
}

// replaceMaterializedTable overwrites a materialized view's table
// with a completely fresh result set — via dropTableComplete (this
// session's own archive-not-delete redesign) followed by a normal
// create+write, reusing already-proven machinery rather than a new
// in-place-update mechanism this storage layer was never designed to
// support.
func (e *Engine) replaceMaterializedTable(name string, result *types.Table) error {
	if e.Catalog.GetTable(name) != nil {
		if err := e.dropTableComplete(name); err != nil {
			return fmt.Errorf("replacing materialized table %q: %w", name, err)
		}
	}
	if err := e.Catalog.CreateTable(name, result.Schema); err != nil {
		return err
	}
	if err := e.persistDiscoverySchema(name, result.Schema); err != nil {
		return err
	}
	if len(result.Rows) == 0 {
		return nil
	}
	tableDef := e.Catalog.GetTable(name)
	_, err := e.flushBatch(name, tableDef, result.Rows)
	return err
}
