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
	clauses  []parser.OrderClause
	n        int64
	consumed int
}

// planColumnarTopN returns a plan when the pipeline prefix is columnar
// top-N, or nil to use the row engine.
func planColumnarTopN(ops []parser.Operator, schema *types.Schema) *colTopNPlan {
	plan := &colTopNPlan{}
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

	switch op := ops[i].(type) {
	case *parser.OrderByOp:
		if i+1 >= len(ops) {
			return nil
		}
		take, ok := ops[i+1].(*parser.TakeOp)
		if !ok {
			return nil
		}
		plan.clauses = op.Clauses
		plan.n = take.Count
		plan.consumed = i + 2
	case *parser.TopOp:
		plan.clauses = []parser.OrderClause{{Column: op.By, Desc: op.Desc}}
		plan.n = op.Count
		plan.consumed = i + 1
	default:
		return nil
	}

	if plan.n <= 0 || plan.n > maxColumnarTopN {
		return nil
	}
	for _, clause := range plan.clauses {
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

	spec, err := buildSortSpec(&projectedSchema, &parser.OrderByOp{Clauses: plan.clauses})
	if err != nil {
		return nil, err
	}

	// Zone pruning from the numeric predicate subset.
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
		fmt.Fprintf(os.Stderr, "[scan] columnar top-N: n=%d, %d sort keys, %d predicates, %d extents parallel\n",
			plan.n, len(plan.clauses), len(plan.preds), len(tableDef.Extents))
	}

	result := types.NewTable("", projectedSchema)
	result.Rows = all
	return result, nil
}
