package engine

// columnar_agg.go — Phase 2 of columnar execution: the
// scan → where → summarize fast path with typed accumulators.
//
// When a pipeline's leading operators are zero or more where clauses
// whose every conjunct is EXACTLY evaluable on typed vectors, followed
// by a summarize whose aggregates and group-by columns are supported,
// the query aggregates directly from decoded chunk vectors: rows are
// never materialized, and boxing happens once per GROUP (for by-values
// and final aggregates), not once per cell.
//
// Unlike Phase 1's conservative chunk filter, selection here must be
// EXACT — there is no downstream WhereOp re-filtering. planColumnarAgg
// therefore rejects the whole pipeline (falling back to the row engine)
// if any conjunct, aggregate, or by-expression is outside the supported
// set. Output replicates applySummarize exactly: same schema
// construction, same first-seen group order, same key formatting, same
// aggregate value types (count → long, sum/avg → real, min/max → the
// column's stored representation).
//
// Supported: where conjuncts of (column op literal) with ==, !=, <,
// <=, >, >= on long/int/real/datetime/timespan (exact-value literals
// only) and ==/!= on string; aggregates count(), sum(c), avg(c),
// min(c), max(c) over numeric/datetime/timespan columns; by over plain
// columns of any storable type.

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	vortex "github.com/LaurieRhodes/vortex-go"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// exactPred is a predicate the columnar path can evaluate with exactly
// the row engine's semantics. Exactly one value pointer is set,
// matching the column's stored representation.
type exactPred struct {
	col    string
	op     parser.BinaryOp
	i64Val *int64
	f64Val *float64
	strVal *string
}

// colAggPlan describes a pipeline prefix the columnar path can execute.
type colAggPlan struct {
	preds     []exactPred
	summarize *parser.SummarizeOp
	aggCols   []string // argument column per aggregate ("" for count)
	byCols    []string
	consumed  int  // operators consumed (wheres + summarize/count)
	isCount   bool // synthesized from a trailing CountOp: empty input yields one row of 0
}

// planColumnarAggregate returns a plan when the pipeline prefix is
// fully executable columnar, or nil to use the row engine.
func planColumnarAggregate(ops []parser.Operator, schema *types.Schema) *colAggPlan {
	plan := &colAggPlan{}
	i := 0
	for ; i < len(ops); i++ {
		where, ok := ops[i].(*parser.WhereOp)
		if !ok {
			break
		}
		if !collectExactPreds(where.Predicate, schema, &plan.preds) {
			return nil
		}
	}
	if i >= len(ops) {
		return nil
	}
	sum, ok := ops[i].(*parser.SummarizeOp)
	if !ok {
		// `where ... | count` is `where ... | summarize count()` with
		// one divergence handled at output: count on empty input emits
		// a single row of 0 (applyCount), where an ungrouped summarize
		// emits no rows. Only worthwhile past a filter — the leading
		// bare `T | count` shape is already answered from metadata.
		if _, isCount := ops[i].(*parser.CountOp); isCount && len(plan.preds) > 0 {
			plan.summarize = &parser.SummarizeOp{
				Aggregations: []parser.Aggregation{{Name: "Count", Function: "count"}},
			}
			plan.aggCols = []string{""}
			plan.isCount = true
			plan.consumed = i + 1
			return plan
		}
		return nil
	}

	for _, agg := range sum.Aggregations {
		switch agg.Function {
		case "count":
			if len(agg.Args) != 0 {
				return nil
			}
			plan.aggCols = append(plan.aggCols, "")
		case "sum", "avg", "min", "max":
			if len(agg.Args) != 1 {
				return nil
			}
			ref, ok := agg.Args[0].(*parser.ColumnRef)
			if !ok {
				return nil
			}
			idx := schema.ColumnIndex(ref.Name)
			if idx < 0 {
				return nil
			}
			switch schema.Columns[idx].Type {
			case types.TypeLong, types.TypeInt, types.TypeReal,
				types.TypeDatetime, types.TypeTimespan:
			default:
				return nil
			}
			plan.aggCols = append(plan.aggCols, ref.Name)
		default:
			return nil
		}
	}
	for _, by := range sum.ByExprs {
		ref, ok := by.Expr.(*parser.ColumnRef)
		if !ok {
			return nil
		}
		if schema.ColumnIndex(ref.Name) < 0 {
			return nil
		}
		plan.byCols = append(plan.byCols, ref.Name)
	}

	plan.summarize = sum
	plan.consumed = i + 1
	return plan
}

// collectExactPreds decomposes a where predicate into exact column
// predicates. Returns false if any conjunct is outside the supported
// set (the whole plan is then abandoned).
func collectExactPreds(expr parser.Expr, schema *types.Schema, out *[]exactPred) bool {
	be, ok := expr.(*parser.BinaryExpr)
	if !ok {
		return false
	}
	if be.Op == parser.OpAnd {
		return collectExactPreds(be.Left, schema, out) &&
			collectExactPreds(be.Right, schema, out)
	}

	col, val, colLeft := extractColAndConst(be, schema)
	if col == nil || val == nil {
		return false
	}
	idx := schema.ColumnIndex(col.Name)
	if idx < 0 {
		return false
	}

	// Normalize operator direction so the column is conceptually on
	// the left (mirrors extractPredicates).
	op := be.Op
	if !colLeft {
		switch op {
		case parser.OpGT:
			op = parser.OpLT
		case parser.OpGTE:
			op = parser.OpLTE
		case parser.OpLT:
			op = parser.OpGT
		case parser.OpLTE:
			op = parser.OpGTE
		}
	}
	switch op {
	case parser.OpEQ, parser.OpNEQ, parser.OpGT, parser.OpGTE, parser.OpLT, parser.OpLTE:
	default:
		return false
	}

	p := exactPred{col: col.Name, op: op}
	switch schema.Columns[idx].Type {
	case types.TypeLong, types.TypeInt, types.TypeDatetime, types.TypeTimespan:
		v, ok := exactInt64(val)
		if !ok {
			return false // e.g. float literal vs int column: row path handles it
		}
		p.i64Val = &v
	case types.TypeReal:
		v, ok := exactFloat64(val)
		if !ok {
			return false
		}
		p.f64Val = &v
	case types.TypeString:
		if op != parser.OpEQ && op != parser.OpNEQ {
			return false
		}
		s, ok := val.(string)
		if !ok {
			return false
		}
		p.strVal = &s
	default:
		return false
	}

	*out = append(*out, p)
	return true
}

func kqlCmpMatches(op parser.BinaryOp, cmp int) bool {
	switch op {
	case parser.OpGT:
		return cmp > 0
	case parser.OpGTE:
		return cmp >= 0
	case parser.OpLT:
		return cmp < 0
	case parser.OpLTE:
		return cmp <= 0
	case parser.OpEQ:
		return cmp == 0
	case parser.OpNEQ:
		return cmp != 0
	}
	return false
}

// evalExactPred narrows sel by one predicate, with exact row-engine
// semantics. A nil (missing) column vector fails every comparison: the
// row engine would see nil cells, and nil fails all binary comparisons
// here (the pred value is never nil, so the nil==nil case cannot arise).
func evalExactPred(p exactPred, c *colVec, sel []bool) {
	if c == nil {
		for i := range sel {
			sel[i] = false
		}
		return
	}
	switch {
	case p.i64Val != nil && c.i64 != nil:
		v := *p.i64Val
		for i, x := range c.i64 {
			if !sel[i] {
				continue
			}
			cmp := 0
			if x < v {
				cmp = -1
			} else if x > v {
				cmp = 1
			}
			if !kqlCmpMatches(p.op, cmp) {
				sel[i] = false
			}
		}
	case p.i64Val != nil && c.i32 != nil:
		v := *p.i64Val
		for i, x := range c.i32 {
			if !sel[i] {
				continue
			}
			cmp := 0
			if int64(x) < v {
				cmp = -1
			} else if int64(x) > v {
				cmp = 1
			}
			if !kqlCmpMatches(p.op, cmp) {
				sel[i] = false
			}
		}
	case p.f64Val != nil && c.f64 != nil:
		v := *p.f64Val
		for i, x := range c.f64 {
			if !sel[i] {
				continue
			}
			cmp := 0
			if x < v {
				cmp = -1
			} else if x > v {
				cmp = 1
			}
			if !kqlCmpMatches(p.op, cmp) {
				sel[i] = false
			}
		}
	case p.strVal != nil && c.dictCodes != nil:
		// Encoded-domain evaluation: resolve the literal to its
		// dictionary code once, then compare codes as integers.
		target := -1
		for code, val := range c.dictVals {
			if val == *p.strVal {
				target = code
				break
			}
		}
		eq := p.op == parser.OpEQ
		if target < 0 {
			// Value absent from this chunk's dictionary: == matches
			// nothing; != matches every row (no nil strings survive
			// storage).
			if eq {
				for i := range sel {
					sel[i] = false
				}
			}
			return
		}
		for i, code := range c.dictCodes {
			if !sel[i] {
				continue
			}
			if (code == target) != eq {
				sel[i] = false
			}
		}
	case p.strVal != nil && c.str != nil:
		v := *p.strVal
		eq := p.op == parser.OpEQ
		for i, x := range c.str {
			if !sel[i] {
				continue
			}
			if (x == v) != eq {
				sel[i] = false
			}
		}
	default:
		// Representation mismatch is impossible by plan construction;
		// fail closed by selecting nothing rather than aggregating
		// wrong data.
		for i := range sel {
			sel[i] = false
		}
	}
}

// aggAccum is one aggregate's state for one group. Which fields are
// meaningful depends on the aggregate function: cnt for count/avg,
// sum for sum/avg, the typed min/max fields (guarded by set) for
// min/max.
type aggAccum struct {
	cnt int64
	sum float64
	i64 int64
	i32 int32
	f64 float64
	set bool
}

// accumulate folds one cell into an accumulator with the row engine's
// semantics (ToFloat64 for sum/avg, typed compare for min/max).
// Storage never yields nil numerics, so no nil-skip is needed; a nil
// vector (column missing from the chunk) contributes nothing, exactly
// as the row engine skips nil cells.
func accumulate(a *aggAccum, fn string, isMax bool, c *colVec, row int) {
	if c == nil {
		return
	}
	switch fn {
	case "sum":
		a.sum += cellFloat(c, row)
	case "avg":
		a.sum += cellFloat(c, row)
		a.cnt++
	case "min", "max":
		switch {
		case c.i64 != nil:
			x := c.i64[row]
			if !a.set || (isMax && x > a.i64) || (!isMax && x < a.i64) {
				a.i64 = x
				a.set = true
			}
		case c.i32 != nil:
			x := c.i32[row]
			if !a.set || (isMax && x > a.i32) || (!isMax && x < a.i32) {
				a.i32 = x
				a.set = true
			}
		case c.f64 != nil:
			x := c.f64[row]
			if !a.set || (isMax && x > a.f64) || (!isMax && x < a.f64) {
				a.f64 = x
				a.set = true
			}
		}
	}
}

func cellFloat(c *colVec, row int) float64 {
	switch {
	case c.i64 != nil:
		return float64(c.i64[row])
	case c.i32 != nil:
		return float64(c.i32[row])
	case c.f64 != nil:
		return c.f64[row]
	}
	return 0
}

// vecKind is a minimal type tag capturing which typed field of a colVec
// is populated (i32 / f64 / other-default-i64). finalizeAccum only ever
// needs to know WHICH accumulator field to read (a.i32 / a.f64 / a.i64)
// for min/max output typing — it never reads the vec's actual decoded
// values. Retaining a full *colVec per extent purely to answer that
// question is why extentAggState used to hold a decoded buffer alive
// per extent; this tag captures the same information at negligible size.
type vecKind int8

const (
	vecKindUnset vecKind = iota
	vecKindI32
	vecKindF64
	vecKindOther
)

func kindOfVec(c *colVec) vecKind {
	switch {
	case c == nil:
		return vecKindUnset
	case c.i32 != nil:
		return vecKindI32
	case c.f64 != nil:
		return vecKindF64
	default:
		return vecKindOther
	}
}

// finalizeAccum converts an accumulator to the output value with the
// row engine's types: count → int64, sum/avg → float64, min/max → the
// stored representation's type (int64 / int32 / float64), driven by the
// representative kind rather than a retained decoded vec.
func finalizeAccum(a *aggAccum, fn string, k vecKind) types.Value {
	switch fn {
	case "count":
		return a.cnt
	case "sum":
		return a.sum
	case "avg":
		if a.cnt == 0 {
			return nil
		}
		return a.sum / float64(a.cnt)
	case "min", "max":
		if !a.set {
			return nil
		}
		switch k {
		case vecKindI32:
			return a.i32
		case vecKindF64:
			return a.f64
		default:
			return a.i64
		}
	}
	return nil
}

// extentAggState is one extent's partial aggregation.
type extentAggState struct {
	groupIdx map[string]int
	keys     []string        // first-seen order within the extent
	byVals   [][]types.Value // per group: boxed by-values
	accums   [][]aggAccum
	repKinds []vecKind // representative type tags for min/max typing (see vecKind)
}

// mergeAccum folds src into dst for one aggregate function.
func mergeAccum(dst, src *aggAccum, fn string, isMax bool) {
	switch fn {
	case "count", "avg":
		dst.cnt += src.cnt
		dst.sum += src.sum
	case "sum":
		dst.sum += src.sum
	case "min", "max":
		if !src.set {
			return
		}
		if !dst.set {
			*dst = *src
			return
		}
		if isMax {
			if src.i64 > dst.i64 {
				dst.i64 = src.i64
			}
			if src.i32 > dst.i32 {
				dst.i32 = src.i32
			}
			if src.f64 > dst.f64 {
				dst.f64 = src.f64
			}
		} else {
			if src.i64 < dst.i64 {
				dst.i64 = src.i64
			}
			if src.i32 < dst.i32 {
				dst.i32 = src.i32
			}
			if src.f64 < dst.f64 {
				dst.f64 = src.f64
			}
		}
	}
}

// aggregateExtent computes one extent's partial aggregation state.
func (e *Engine) aggregateExtent(tableName string, ext catalog.ExtentEntry, plan *colAggPlan, schema *types.Schema,
	scanCols []string, colPos map[string]int, byVecIdx, aggVecIdx []int,
	aggFns []string, aggIsMax []bool, zoneFilter *vortex.RowFilter) (*extentAggState, error) {

	nAggs := len(aggFns)
	st := &extentAggState{
		groupIdx: make(map[string]int),
		repKinds: make([]vecKind, nAggs),
	}
	var keyBuf strings.Builder
	var codeToGroup []int // dict code -> group index; -1 = unseen

	iter, f, _, err := e.openExtentChunks(ext.FilePath, scanCols, zoneFilter)
	if err != nil {
		return nil, fmt.Errorf("scan extent %s: %w", ext.ID, err)
	}
	defer f.Close()

	// predColIdx marks which scanCols positions are needed to evaluate
	// plan.preds. Computed once (not per chunk) since it never changes
	// across chunks/extents for a given plan.
	predColIdx := make([]bool, len(scanCols))
	for _, p := range plan.preds {
		predColIdx[colPos[p.col]] = true
	}

	for iter.Next() {
		chunk := iter.Result()
		vecs := make([]*colVec, len(scanCols))

		decodeOne := func(i int, name string) error {
			arr, ok := chunk.Columns[name]
			if !ok {
				return nil
			}
			vec, decErr := decodeColumnVec(e, tableName, name, arr, schema.Columns[schema.ColumnIndex(name)].Type)
			if decErr != nil {
				return fmt.Errorf("decode column %q from %s: %w", name, ext.FilePath, decErr)
			}
			vecs[i] = vec
			return nil
		}

		// Pass 1: decode only the predicate columns. Both pcodec (float)
		// and FastLanes (int) decode work in fixed-size blocks that must
		// be decompressed sequentially, so zone-level pruning (already
		// applied before this point — see openExtentChunks/zoneFilter)
		// is the only mechanism that can skip decode work for provably
		// non-matching zones. But a zone can survive that coarse min/max
		// check and still contain zero rows that actually satisfy an
		// exact-equality predicate (e.g. a query for one specific value
		// on a wide, densely-populated column, or a rare/sparse exact
		// match) — decoding the predicate column(s) first lets us detect
		// that case and skip every other column's decode for the whole
		// chunk below, which is exactly where the real cost lives.
		for i, name := range scanCols {
			if predColIdx[i] {
				if err := decodeOne(i, name); err != nil {
					return nil, err
				}
			}
		}

		sel := make([]bool, chunk.RowCount)
		for i := range sel {
			sel[i] = true
		}
		for _, p := range plan.preds {
			evalExactPred(p, vecs[colPos[p.col]], sel)
		}

		if len(plan.preds) > 0 {
			anySelected := false
			for _, s := range sel {
				if s {
					anySelected = true
					break
				}
			}
			if !anySelected {
				continue
			}
		}

		// Pass 2: decode the remaining columns. Only reached when at
		// least one row in this chunk survives the predicate (or there
		// were no predicates at all, in which case every chunk reaches
		// here exactly as before this change).
		for i, name := range scanCols {
			if !predColIdx[i] {
				if err := decodeOne(i, name); err != nil {
					return nil, err
				}
			}
		}

		for ai, vi := range aggVecIdx {
			if vi >= 0 && st.repKinds[ai] == vecKindUnset {
				st.repKinds[ai] = kindOfVec(vecs[vi])
			}
		}

		// Group-on-code fast path: a single dictionary-encoded
		// by-column groups by array index — no per-row key building,
		// no per-row hash. The string map is still maintained on
		// group creation (once per group, not per row) so generic
		// chunks and the cross-extent merge stay coherent.
		var byDict *colVec
		if len(byVecIdx) == 1 {
			if v := vecs[byVecIdx[0]]; v != nil && v.dictCodes != nil {
				byDict = v
			}
		}
		if byDict != nil {
			if len(codeToGroup) < len(byDict.dictVals) {
				grown := make([]int, len(byDict.dictVals))
				copy(grown, codeToGroup)
				for i := len(codeToGroup); i < len(grown); i++ {
					grown[i] = -1
				}
				codeToGroup = grown
			}
			for row := 0; row < chunk.RowCount; row++ {
				if !sel[row] {
					continue
				}
				code := byDict.dictCodes[row]
				gi := codeToGroup[code]
				if gi < 0 {
					key := byDict.dictVals[code]
					// The same value may have a different code in an
					// earlier chunk only if dictionaries diverge;
					// groupIdx keeps value-identity authoritative.
					if egi, exists := st.groupIdx[key]; exists {
						gi = egi
					} else {
						gi = len(st.byVals)
						st.groupIdx[key] = gi
						st.keys = append(st.keys, key)
						st.byVals = append(st.byVals, []types.Value{key})
						st.accums = append(st.accums, make([]aggAccum, nAggs))
					}
					codeToGroup[code] = gi
				}
				acc := st.accums[gi]
				for ai := 0; ai < nAggs; ai++ {
					vi := aggVecIdx[ai]
					if vi < 0 {
						acc[ai].cnt++ // count()
						continue
					}
					accumulate(&acc[ai], aggFns[ai], aggIsMax[ai], vecs[vi], row)
				}
			}
			continue
		}

		for row := 0; row < chunk.RowCount; row++ {
			if !sel[row] {
				continue
			}

			keyBuf.Reset()
			for bi, vi := range byVecIdx {
				if bi > 0 {
					keyBuf.WriteByte(0)
				}
				appendKeyPart(&keyBuf, vecs[vi], row)
			}
			key := keyBuf.String()

			gi, exists := st.groupIdx[key]
			if !exists {
				gi = len(st.byVals)
				st.groupIdx[key] = gi
				st.keys = append(st.keys, key)
				bv := make([]types.Value, len(plan.byCols))
				for bi, vi := range byVecIdx {
					if vecs[vi] != nil {
						bv[bi] = vecs[vi].value(row)
					}
				}
				st.byVals = append(st.byVals, bv)
				st.accums = append(st.accums, make([]aggAccum, nAggs))
			}

			acc := st.accums[gi]
			for ai := 0; ai < nAggs; ai++ {
				vi := aggVecIdx[ai]
				if vi < 0 {
					acc[ai].cnt++ // count()
					continue
				}
				accumulate(&acc[ai], aggFns[ai], aggIsMax[ai], vecs[vi], row)
			}
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("scan iteration %s: %w", ext.FilePath, err)
	}
	return st, nil
}

// runColumnarAggregate executes the plan: extents aggregate in parallel
// (bounded by NumCPU) into partial states, which merge in extent order.
// Merging in extent order reproduces the sequential scan's first-seen
// group order exactly — the first occurrence of a key across ordered
// extents is the same group position a sequential scan would assign —
// so output stays byte-identical to the row engine.
func (e *Engine) runColumnarAggregate(tableDef *catalog.Table, plan *colAggPlan) (*types.Table, error) {
	schema := &tableDef.Schema

	// Columns to decode: predicates ∪ aggregate args ∪ by columns.
	var scanCols []string
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			scanCols = append(scanCols, name)
		}
	}
	for _, p := range plan.preds {
		add(p.col)
	}
	for _, c := range plan.aggCols {
		add(c)
	}
	for _, c := range plan.byCols {
		add(c)
	}
	if len(scanCols) == 0 {
		// count()-only, no filter, no grouping: decode one column to
		// drive row counts.
		if len(schema.Columns) == 0 {
			return nil, fmt.Errorf("columnar aggregate: table has no columns")
		}
		add(schema.Columns[0].Name)
	}

	// Zone pruning from the numeric predicate subset (predicates are
	// ANDed, so pruning on any subset stays sound).
	var zonePreds []vortex.ColumnPredicate
	for _, p := range plan.preds {
		switch {
		case p.i64Val != nil:
			zonePreds = append(zonePreds, vortex.ColumnPredicate{Column: p.col, Op: kqlToVortexOp(p.op), Value: *p.i64Val})
		case p.f64Val != nil:
			zonePreds = append(zonePreds, vortex.ColumnPredicate{Column: p.col, Op: kqlToVortexOp(p.op), Value: *p.f64Val})
		}
	}
	var zoneFilter *vortex.RowFilter
	if len(zonePreds) > 0 {
		zoneFilter = vortex.NewRowFilter(zonePreds...)
	}

	colPos := map[string]int{}
	for i, c := range scanCols {
		colPos[c] = i
	}
	byVecIdx := make([]int, len(plan.byCols))
	for i, c := range plan.byCols {
		byVecIdx[i] = colPos[c]
	}
	aggVecIdx := make([]int, len(plan.aggCols))
	for i, c := range plan.aggCols {
		if c == "" {
			aggVecIdx[i] = -1
		} else {
			aggVecIdx[i] = colPos[c]
		}
	}
	aggFns := make([]string, len(plan.summarize.Aggregations))
	aggIsMax := make([]bool, len(plan.summarize.Aggregations))
	for i, agg := range plan.summarize.Aggregations {
		aggFns[i] = agg.Function
		aggIsMax[i] = agg.Function == "max"
	}
	nAggs := len(aggFns)

	// Per-extent partial aggregation, parallel, bounded by NumCPU.
	states := make([]*extentAggState, len(tableDef.Extents))
	errs := make([]error, len(tableDef.Extents))
	workers := runtime.NumCPU()
	if workers > len(tableDef.Extents) {
		workers = len(tableDef.Extents)
	}
	semCh := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range tableDef.Extents {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			semCh <- struct{}{}
			defer func() { <-semCh }()
			states[i], errs[i] = e.aggregateExtent(tableDef.Name, tableDef.Extents[i], plan, schema,
				scanCols, colPos, byVecIdx, aggVecIdx, aggFns, aggIsMax, zoneFilter)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	// Merge partial states in extent order (preserves global
	// first-seen group order).
	groupIdx := make(map[string]int)
	var byVals [][]types.Value
	var accums [][]aggAccum
	aggRepKind := make([]vecKind, nAggs)

	for _, st := range states {
		if st == nil {
			continue
		}
		for ai := range aggRepKind {
			if aggRepKind[ai] == vecKindUnset {
				aggRepKind[ai] = st.repKinds[ai]
			}
		}
		for sgi, key := range st.keys {
			gi, exists := groupIdx[key]
			if !exists {
				gi = len(byVals)
				groupIdx[key] = gi
				byVals = append(byVals, st.byVals[sgi])
				accums = append(accums, st.accums[sgi])
				continue
			}
			for ai := 0; ai < nAggs; ai++ {
				mergeAccum(&accums[gi][ai], &st.accums[sgi][ai], aggFns[ai], aggIsMax[ai])
			}
		}
	}

	if e.Verbose {
		fmt.Fprintf(os.Stderr, "[scan] columnar aggregate: %d groups, %d aggregates, %d predicates, %d extents parallel\n",
			len(byVals), nAggs, len(plan.preds), len(tableDef.Extents))
	}

	// Output: identical schema construction to applySummarize.
	outSchema := types.Schema{}
	for _, agg := range plan.summarize.Aggregations {
		outSchema.Columns = append(outSchema.Columns, types.Column{
			Name: agg.Name,
			Type: inferAggType(agg.Function, agg.Args, schema),
		})
	}
	for _, by := range plan.summarize.ByExprs {
		outSchema.Columns = append(outSchema.Columns, types.Column{
			Name: by.Name,
			Type: inferExprType(by.Expr, schema),
		})
	}

	result := types.NewTable("", outSchema)
	for gi := range byVals {
		row := make(types.Row, len(outSchema.Columns))
		for ai := 0; ai < nAggs; ai++ {
			row[ai] = finalizeAccum(&accums[gi][ai], aggFns[ai], aggRepKind[ai])
		}
		copy(row[nAggs:], byVals[gi])
		result.AddRow(row)
	}
	if plan.isCount && len(byVals) == 0 {
		// applyCount semantics: an empty filtered input still yields
		// one row with Count = 0.
		result.AddRow(types.Row{int64(0)})
	}
	return result, nil
}

func kqlToVortexOp(op parser.BinaryOp) vortex.CompareOp {
	switch op {
	case parser.OpEQ:
		return vortex.OpEQ
	case parser.OpNEQ:
		return vortex.OpNEQ
	case parser.OpGT:
		return vortex.OpGT
	case parser.OpGTE:
		return vortex.OpGTE
	case parser.OpLT:
		return vortex.OpLT
	}
	return vortex.OpLTE
}

// appendKeyPart writes one by-column value in the exact form
// fmt.Sprintf("%v", boxedValue) would produce for that value's type.
func appendKeyPart(b *strings.Builder, c *colVec, row int) {
	if c == nil {
		b.WriteString("<nil>")
		return
	}
	switch {
	case c.dictCodes != nil:
		if row < len(c.dictCodes) {
			b.WriteString(c.dictVals[c.dictCodes[row]])
		} else {
			b.WriteString("<nil>")
		}
	case c.str != nil:
		if row < len(c.str) {
			b.WriteString(c.str[row])
		} else {
			b.WriteString("<nil>")
		}
	case c.i64 != nil:
		b.WriteString(strconv.FormatInt(c.i64[row], 10))
	case c.i32 != nil:
		b.WriteString(strconv.FormatInt(int64(c.i32[row]), 10))
	case c.f64 != nil:
		b.WriteString(strconv.FormatFloat(c.f64[row], 'g', -1, 64))
	case c.b != nil:
		b.WriteString(strconv.FormatBool(c.b[row]))
	}
}
