package engine

// columnar_topn.go — Phase 3 (first slice): streaming columnar top-N.
//
// Pipeline prefixes of the form
//
//	[exact wheres...] | sort by ... | take N        (or | top N by ...)
//
// execute per-extent over decoded chunk vectors with a bounded heap:
// only rows that make the heap are ever boxed, so peak memory is one
// chunk's vectors plus N rows per extent — not the whole table. This
// is the first path where a larger-than-memory table can flow through
// a sort.
//
// The plan gate mirrors columnar_agg.go: every where conjunct must be
// EXACTLY evaluable on vectors (there is no downstream re-filter), all
// sort columns must be plain storable-typed columns, and N must be
// small enough that per-extent heaps stay cheaper than a row scan.
// Anything else falls back to the row engine.
//
// Determinism: extents contribute candidate sets that merge in extent
// order under a stable sort, so output is deterministic for fixed
// data. As with applyTopN, rows comparing equal on all sort keys may
// order differently than the row engine's stable full sort — KQL
// defines no tie order.

import (
	"container/heap"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"

	vortex "github.com/LaurieRhodes/vortex-go"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// maxColumnarTopN bounds N for the columnar path: beyond this,
// per-extent heaps of boxed rows stop being obviously cheaper than the
// row engine's scan.
const maxColumnarTopN = 10000

// colTopNPlan describes a where*/sort/take prefix executable columnar.
type colTopNPlan struct {
	preds    []exactPred
	sim      *simExtend // optional: extend Name = similarity(VecCol, constant)
	clauses  []parser.OrderClause
	n        int64
	consumed int
}

// simExtend is a vector-similarity extend computed on chunk vectors:
// the retrieval shape `extend sim = series_cosine_similarity(Vec, qv)
// | top N by sim`, which otherwise boxes and parses every stored
// vector before keeping N of them.
type simExtend struct {
	name string    // output column
	col  string    // stored vector column
	fn   string    // series_cosine_similarity or series_dot_product
	q    []float64 // the constant vector
}

// planSimilarityExtend accepts a single assignment Name =
// series_cosine_similarity|series_dot_product(VecCol, constant) (either
// argument order) whose constant evaluates to a vector.
func planSimilarityExtend(eng *Engine, as []parser.Assignment, schema *types.Schema) *simExtend {
	if len(as) != 1 || as[0].Name == "" || schema.ColumnIndex(as[0].Name) >= 0 {
		return nil
	}
	fc, ok := as[0].Expr.(*parser.FuncCall)
	if !ok || len(fc.Args) != 2 || (fc.Name != "series_cosine_similarity" && fc.Name != "series_dot_product") {
		return nil
	}
	for vi := 0; vi < 2; vi++ {
		ref, ok := fc.Args[vi].(*parser.ColumnRef)
		if !ok {
			continue
		}
		ci := schema.ColumnIndex(ref.Name)
		if ci < 0 || (schema.Columns[ci].Type != types.TypeDynamic && schema.Columns[ci].Type != types.TypeString) {
			continue
		}
		other, err := substituteToScalars(eng, fc.Args[1-vi])
		if err != nil {
			return nil
		}
		refs := map[string]bool{}
		collectColumnRefs(other, refs)
		for r := range refs {
			if schema.ColumnIndex(r) >= 0 {
				return nil // not a constant
			}
		}
		val, err := evalExpr(eng, other, &types.Schema{}, types.Row{})
		if err != nil || val == nil {
			return nil
		}
		q, ok := parseFloatArray(val)
		if !ok {
			return nil
		}
		return &simExtend{name: as[0].Name, col: ref.Name, fn: fc.Name, q: q}
	}
	return nil
}

// simVec computes the similarity column for one chunk's selected rows.
// Semantics are evalVectorFunc's: a null or unparsable vector gives a
// null score; a length mismatch is an error.
func (s *simExtend) simVec(v *colVec, sel []bool) (*colVec, error) {
	n := len(sel)
	out := &colVec{kt: types.TypeReal, f64: make([]float64, n), valid: make([]bool, n)}
	if v == nil {
		return out, nil
	}
	// Extents differ greatly in size, so per-extent parallelism alone
	// leaves the largest ones on one core: split the chunk's rows too.
	parts := 1
	if n >= 1024 {
		parts = runtime.NumCPU()
		if parts > n/256 {
			parts = n / 256
		}
	}
	if parts <= 1 {
		return out, s.simRange(v, sel, out, 0, n)
	}
	per := (n + parts - 1) / parts
	errs := make([]error, parts)
	var wg sync.WaitGroup
	for p := 0; p < parts; p++ {
		lo, hi := p*per, (p+1)*per
		if hi > n {
			hi = n
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(p, lo, hi int) {
			defer wg.Done()
			errs[p] = s.simRange(v, sel, out, lo, hi)
		}(p, lo, hi)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// simRange scores rows [lo, hi) into out.
func (s *simExtend) simRange(v *colVec, sel []bool, out *colVec, lo, hi int) error {
	var buf []float64
	for row := lo; row < hi; row++ {
		if !sel[row] {
			continue
		}
		var str string
		switch {
		case v.str != nil && row < len(v.str):
			str = v.str[row]
		case v.dictCodes != nil && row < len(v.dictCodes):
			str = v.dictVals[v.dictCodes[row]]
		default:
			continue
		}
		if v.isNull(row) || str == "" {
			continue
		}
		var vec []float64
		var ok bool
		if b, ok2 := appendJSONFloats(buf[:0], str); ok2 {
			buf, vec, ok = b, b, true
		} else {
			vec, ok = parseFloatArray(str)
		}
		if !ok {
			continue
		}
		if s.fn == "series_dot_product" {
			if len(vec) != len(s.q) {
				return fmt.Errorf("series_dot_product: vector length mismatch (%d vs %d)", len(vec), len(s.q))
			}
			var dot float64
			for i := range vec {
				dot += vec[i] * s.q[i]
			}
			out.f64[row], out.valid[row] = dot, true
			continue
		}
		sim, err := cosineSimilarity(vec, s.q)
		if err != nil {
			return err
		}
		out.f64[row], out.valid[row] = sim, true
	}
	return nil
}

// planColumnarTopN returns a plan when the pipeline prefix is columnar
// top-N, or nil to use the row engine.
func planColumnarTopN(eng *Engine, ops []parser.Operator, schema *types.Schema) *colTopNPlan {
	plan := &colTopNPlan{}
	i := 0
	for ; i < len(ops); i++ {
		where, ok := ops[i].(*parser.WhereOp)
		if !ok {
			break
		}
		if !collectExactPreds(eng, where.Predicate, schema, &plan.preds) {
			return nil
		}
	}
	if i >= len(ops) {
		return nil
	}
	if ext, ok := ops[i].(*parser.ExtendOp); ok {
		if plan.sim = planSimilarityExtend(eng, ext.Assignments, schema); plan.sim == nil {
			return nil
		}
		i++
		if i >= len(ops) {
			return nil
		}
	}

	switch op := ops[i].(type) {
	case *parser.OrderByOp:
		if i+1 >= len(ops) {
			return nil
		}
		take, ok := ops[i+1].(*parser.TakeOp)
		if !ok || take.CountExpr != nil {
			return nil
		}
		plan.clauses = op.Clauses
		plan.n = take.Count
		plan.consumed = i + 2
	case *parser.TopOp:
		if op.CountExpr != nil {
			return nil
		}
		plan.clauses = []parser.OrderClause{{Column: op.By, Expr: op.ByExpr, Desc: op.Desc, Nulls: op.Nulls}}
		plan.n = op.Count
		plan.consumed = i + 1
	default:
		return nil
	}

	if plan.n <= 0 || plan.n > maxColumnarTopN {
		return nil
	}
	for _, clause := range plan.clauses {
		if clause.Expr != nil || clause.Nulls != parser.NullsDefault {
			return nil // expression keys and explicit null placement: the row path
		}
		if plan.sim != nil && clause.Column == plan.sim.name {
			continue
		}
		idx := schema.ColumnIndex(clause.Column)
		if idx < 0 {
			return nil
		}
		switch schema.Columns[idx].Type {
		case types.TypeLong, types.TypeInt, types.TypeReal, types.TypeString,
			types.TypeDatetime, types.TypeTimespan, types.TypeBool:
		default:
			return nil
		}
	}
	return plan
}

// vecLess reports whether the (unboxed) row at rowIdx sorts before the
// boxed candidate row under the spec, boxing only the compared clause
// cells (ephemeral, stack-allocated in practice).
func vecLess(spec *sortSpec, clauseVecs []*colVec, rowIdx int, other types.Row) bool {
	for ci, sc := range spec.cols {
		var a types.Value
		if v := clauseVecs[ci]; v != nil {
			a = v.value(rowIdx)
		}
		cmp := types.CompareValues(a, other[sc.idx], sc.typ)
		if cmp == 0 {
			continue
		}
		if sc.desc {
			return cmp > 0
		}
		return cmp < 0
	}
	return false
}

// rowFromVecs boxes one row from chunk vectors (nil vector → nil cell,
// matching the row engine's handling of columns missing from a chunk).
func rowFromVecs(vecs []*colVec, rowIdx int) types.Row {
	row := make(types.Row, len(vecs))
	for i, v := range vecs {
		if v != nil {
			row[i] = v.value(rowIdx)
		}
	}
	return row
}

// topNExtent scans one extent and returns its ≤ n best rows (boxed,
// unsorted).
func (e *Engine) topNExtent(tableName string, ext catalog.ExtentEntry, plan *colTopNPlan, schema *types.Schema,
	scanCols []string, colPos map[string]int, spec *sortSpec,
	zoneFilter *vortex.RowFilter) ([]types.Row, error) {

	clauseVecPos := make([]int, len(plan.clauses))
	for i, clause := range plan.clauses {
		if plan.sim != nil && clause.Column == plan.sim.name {
			clauseVecPos[i] = len(scanCols) // the computed column, after the scanned ones
			continue
		}
		clauseVecPos[i] = colPos[clause.Column]
	}

	h := &rowHeap{spec: spec, rows: make([]types.Row, 0, plan.n)}
	clauseVecs := make([]*colVec, len(plan.clauses))

	// predColIdx marks which scanCols positions are needed to evaluate
	// plan.preds. Computed once, not per chunk (see aggregateExtent's
	// identical two-pass pattern for the full rationale: pcodec/
	// FastLanes decode is sequential within fixed-size blocks, so
	// per-row skip isn't possible, but skipping every non-predicate
	// column's decode entirely for a chunk with zero surviving rows is
	// — and that's common whenever a zone survives coarse zone-map
	// pruning but happens to contain no exact matches).
	predColIdx := make([]bool, len(scanCols))
	for _, p := range plan.preds {
		predColIdx[colPos[p.col]] = true
	}
	memos := make([]strMemo, len(plan.preds))

	iter, f, _, err := e.openExtentChunks(ext.FilePath, scanCols, zoneFilter)
	if err != nil {
		return nil, fmt.Errorf("scan extent %s: %w", ext.ID, err)
	}
	defer f.Close()
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

		// Pass 1: predicate columns only.
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
		for pi, p := range plan.preds {
			evalExactPred(p, vecs[colPos[p.col]], sel, &memos[pi])
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

		// Pass 2: remaining columns, including sort-clause and row
		// materialization columns — only reached once we know at
		// least one row survives (or there were no predicates).
		for i, name := range scanCols {
			if !predColIdx[i] {
				if err := decodeOne(i, name); err != nil {
					return nil, err
				}
			}
		}
		if plan.sim != nil {
			sv, err := plan.sim.simVec(vecs[colPos[plan.sim.col]], sel)
			if err != nil {
				return nil, err
			}
			vecs = append(vecs, sv)
		}
		for i, pos := range clauseVecPos {
			clauseVecs[i] = vecs[pos]
		}

		for row := 0; row < chunk.RowCount; row++ {
			if !sel[row] {
				continue
			}
			if int64(len(h.rows)) < plan.n {
				h.rows = append(h.rows, rowFromVecs(vecs, row))
				if int64(len(h.rows)) == plan.n {
					// Heapify once full.
					heap.Init(h)
				}
				continue
			}
			// Bounded phase: compare against the current worst using
			// vector cells; box only on acceptance.
			if vecLess(spec, clauseVecs, row, h.rows[0]) {
				h.rows[0] = rowFromVecs(vecs, row)
				heap.Fix(h, 0)
			}
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("scan iteration %s: %w", ext.FilePath, err)
	}
	return h.rows, nil
}

// runColumnarTopN executes the plan: extents scan in parallel with
// per-extent bounded heaps; candidates merge in extent order under a
// stable sort; the global top n survive.
func (e *Engine) runColumnarTopN(tableDef *catalog.Table, plan *colTopNPlan, allOps []parser.Operator) (*types.Table, error) {
	schema := &tableDef.Schema

	// Scan the same column set the row engine would, so the output
	// schema is identical.
	scanCols := RequiredColumns(schema, allOps)
	if scanCols == nil {
		scanCols = make([]string, len(schema.Columns))
		for i, c := range schema.Columns {
			scanCols[i] = c.Name
		}
	}
	projectedSchema := buildProjectedSchema(schema, scanCols)
	if plan.sim != nil {
		if _, ok := colPosOf(scanCols, plan.sim.col); !ok {
			return nil, fmt.Errorf("columnar top-N: vector column %q not scanned", plan.sim.col)
		}
		projectedSchema.Columns = append(projectedSchema.Columns, types.Column{Name: plan.sim.name, Type: types.TypeReal})
	}

	spec, err := buildSortSpec(&projectedSchema, &parser.OrderByOp{Clauses: plan.clauses})
	if err != nil {
		return nil, err
	}

	// Zone pruning from the numeric predicate subset.
	var zonePreds []vortex.ColumnPredicate
	for _, p := range plan.preds {
		if p.op == parser.OpNEQ {
			continue // null != v is true; zone stats cover valid values only
		}
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

	// Per-extent candidates, parallel, bounded by NumCPU.
	candidates := make([][]types.Row, len(tableDef.Extents))
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
			candidates[i], errs[i] = e.topNExtent(tableDef.Name, tableDef.Extents[i], plan, schema,
				scanCols, colPos, spec, zoneFilter)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	// Merge in extent order, stable sort, keep n.
	var all []types.Row
	for _, c := range candidates {
		all = append(all, c...)
	}
	sort.SliceStable(all, func(i, j int) bool { return spec.less(all[i], all[j]) })
	if int64(len(all)) > plan.n {
		all = all[:plan.n]
	}

	if e.Verbose {
		fmt.Fprintf(os.Stderr, "[scan] columnar top-N: n=%d, %d sort keys, %d predicates, similarity=%v, %d extents parallel\n",
			plan.n, len(plan.clauses), len(plan.preds), plan.sim != nil, len(tableDef.Extents))
	}

	result := types.NewTable("", projectedSchema)
	result.Rows = all
	return result, nil
}

func colPosOf(cols []string, name string) (int, bool) {
	for i, c := range cols {
		if c == name {
			return i, true
		}
	}
	return -1, false
}
