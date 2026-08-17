package engine

// topn.go — bounded top-N selection for `sort by ... | take N` and
// `top N by ...`. A full sort of M rows to keep N is O(M log M)
// comparisons through interface-typed CompareValues; the bounded heap
// is O(M log N), which at M=1e6, N=10 removes ~99.99% of comparisons.
//
// Tie behavior: the heap is not stable, so rows comparing equal on all
// sort keys may surface in a different order than the stable full sort.
// KQL does not define tie order, and `take` is an arbitrary-subset
// operator, so any heap outcome is a valid answer.

import (
	"container/heap"
	"fmt"
	"sort"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// sortSpec is a resolved order-by clause set.
type sortSpec struct {
	cols []struct {
		idx  int
		typ  types.KQLType
		desc bool
	}
}

func buildSortSpec(schema *types.Schema, op *parser.OrderByOp) (*sortSpec, error) {
	spec := &sortSpec{}
	for _, clause := range op.Clauses {
		idx := schema.ColumnIndex(clause.Column)
		if idx < 0 {
			return nil, fmt.Errorf("order by: column %q not found", clause.Column)
		}
		spec.cols = append(spec.cols, struct {
			idx  int
			typ  types.KQLType
			desc bool
		}{idx, schema.Columns[idx].Type, clause.Desc})
	}
	return spec, nil
}

// less reports whether row a sorts before row b under the spec.
func (s *sortSpec) less(a, b types.Row) bool {
	for _, sc := range s.cols {
		cmp := types.CompareValues(a[sc.idx], b[sc.idx], sc.typ)
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

// rowHeap keeps the N best rows with the WORST at the root, so the
// eviction candidate is always heap[0].
type rowHeap struct {
	rows []types.Row
	spec *sortSpec
}

func (h *rowHeap) Len() int            { return len(h.rows) }
func (h *rowHeap) Less(i, j int) bool  { return h.spec.less(h.rows[j], h.rows[i]) }
func (h *rowHeap) Swap(i, j int)       { h.rows[i], h.rows[j] = h.rows[j], h.rows[i] }
func (h *rowHeap) Push(x interface{})  { h.rows = append(h.rows, x.(types.Row)) }
func (h *rowHeap) Pop() interface{} {
	old := h.rows
	n := len(old)
	x := old[n-1]
	h.rows = old[:n-1]
	return x
}

// applyTopN returns the first n rows of the input as ordered by op,
// using a bounded heap. Falls back to a full stable sort when n covers
// most of the input (the heap only pays off when n << len(rows)).
func (e *Engine) applyTopN(input *types.Table, op *parser.OrderByOp, n int64) (*types.Table, error) {
	if n <= 0 {
		return types.NewTable(input.Name, input.Schema), nil
	}
	if n >= int64(len(input.Rows))/2 {
		ordered, err := e.applyOrderBy(input, op)
		if err != nil {
			return nil, err
		}
		return e.applyTake(ordered, &parser.TakeOp{Count: n})
	}

	spec, err := buildSortSpec(&input.Schema, op)
	if err != nil {
		return nil, err
	}

	h := &rowHeap{spec: spec, rows: make([]types.Row, 0, n)}
	for _, row := range input.Rows {
		if int64(len(h.rows)) < n {
			heap.Push(h, row)
			continue
		}
		// Row beats the current worst: replace the root.
		if spec.less(row, h.rows[0]) {
			h.rows[0] = row
			heap.Fix(h, 0)
		}
	}

	out := make([]types.Row, len(h.rows))
	copy(out, h.rows)
	sort.SliceStable(out, func(i, j int) bool { return spec.less(out[i], out[j]) })

	result := types.NewTable(input.Name, input.Schema)
	result.Rows = out
	return result, nil
}
