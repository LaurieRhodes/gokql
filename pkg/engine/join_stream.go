package engine

import (
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// A join whose output feeds a summarize (through row-local operators
// only) need not be materialised: its rows are aggregated in batches
// and the per-batch results merged, as ADX does. A self-join that
// fans out to ~10^8 rows before `summarize count() by k` otherwise
// needs tens of GB.
var (
	streamJoinMinRows = 4 << 20 // join outputs smaller than this materialise as usual
	streamJoinBatch   = 1 << 20 // rows per aggregated batch
)

// mergeAggFn maps an aggregate whose partial results combine with
// another aggregate to that aggregate.
var mergeAggFn = map[string]string{
	"count": "sum", "countif": "sum", "sum": "sum", "sumif": "sum",
	"min": "min", "minif": "min", "max": "max", "maxif": "max",
}

// streamableJoinTail reports whether ops (the operators after a join)
// begin with row-local operators followed by a summarize whose
// aggregates all merge; it returns the summarize's index.
func streamableJoinTail(ops []parser.Operator) (int, bool) {
	for i, op := range ops {
		switch o := op.(type) {
		case *parser.WhereOp, *parser.ProjectOp, *parser.ProjectAwayOp, *parser.ProjectRenameOp, *parser.ProjectKeepOp, *parser.ExtendOp:
		case *parser.SummarizeOp:
			if len(o.Aggregations) == 0 {
				return 0, false
			}
			for _, a := range o.Aggregations {
				if _, ok := mergeAggFn[a.Function]; !ok {
					return 0, false
				}
			}
			return i, true
		default:
			return 0, false
		}
	}
	return 0, false
}

// applyJoinPipeline runs the join ops[0] over left. When the join's
// output is large and feeds a mergeable summarize, the join and the
// operators through that summarize run streamed; consumed is the
// number of operators handled.
func (e *Engine) applyJoinPipeline(left *types.Table, ops []parser.Operator) (result *types.Table, consumed int, err error) {
	join := ops[0].(*parser.JoinOp)
	jp, err := e.prepareJoin(left, join)
	if err != nil {
		return nil, 0, err
	}
	sumIdx, streamable := streamableJoinTail(ops[1:])
	if !streamable || jp.outputRows(left) < streamJoinMinRows {
		result = types.NewTable(left.Name, jp.schema)
		err = jp.emit(left, func(r types.Row) error {
			result.AddRow(r)
			return nil
		})
		return result, 1, err
	}
	tail := ops[1 : sumIdx+2]
	sum := tail[len(tail)-1].(*parser.SummarizeOp)

	var acc *types.Table // concatenated per-batch summaries
	batch := types.NewTable(left.Name, jp.schema)
	// Joined rows come from one buffer reused for every batch: the
	// tail ends in a summarize, whose output rows are its own.
	width := len(jp.schema.Columns)
	buf := make([]types.Value, streamJoinBatch*width)
	used := 0
	jp.alloc = func() types.Row {
		r := buf[used : used+width : used+width]
		used += width
		return r
	}
	batch.Rows = make([]types.Row, 0, streamJoinBatch)
	flush := func() error {
		part, err := e.applyPipeline(batch, tail)
		if err != nil {
			return err
		}
		if acc == nil {
			acc = types.NewTable(part.Name, part.Schema)
		}
		acc.Rows = append(acc.Rows, part.Rows...)
		if len(acc.Rows) >= streamJoinBatch {
			if acc, err = e.mergePartialSummaries(acc, sum); err != nil {
				return err
			}
		}
		batch.Rows = batch.Rows[:0]
		clear(buf[:used])
		used = 0
		return nil
	}
	err = jp.emit(left, func(r types.Row) error {
		batch.AddRow(r)
		if len(batch.Rows) >= streamJoinBatch {
			return flush()
		}
		return nil
	})
	if err == nil && (len(batch.Rows) > 0 || acc == nil) {
		err = flush()
	}
	if err != nil {
		return nil, 0, err
	}
	result, err = e.mergePartialSummaries(acc, sum)
	return result, sumIdx + 2, err
}

// mergePartialSummaries combines summaries of disjoint row sets
// produced by sum: group-by columns first, then one column per
// aggregate, each merged by its mergeAggFn.
func (e *Engine) mergePartialSummaries(parts *types.Table, sum *parser.SummarizeOp) (*types.Table, error) {
	nBy := len(sum.ByExprs)
	merge := &parser.SummarizeOp{}
	for i, c := range parts.Schema.Columns {
		ref := &parser.ColumnRef{Name: c.Name}
		if i < nBy {
			merge.ByExprs = append(merge.ByExprs, parser.ByExpr{Name: c.Name, Expr: ref})
			continue
		}
		fn := mergeAggFn[sum.Aggregations[i-nBy].Function]
		merge.Aggregations = append(merge.Aggregations, parser.Aggregation{Name: c.Name, Function: fn, Args: []parser.Expr{ref}})
	}
	out, err := e.applySummarize(parts, merge)
	if err != nil {
		return nil, err
	}
	// Keep the partial summaries' column types (sum() of an int
	// count is still a long; min of a datetime a datetime).
	for i := range out.Schema.Columns {
		if j := parts.Schema.ColumnIndex(out.Schema.Columns[i].Name); j >= 0 {
			out.Schema.Columns[i].Type = parts.Schema.Columns[j].Type
		}
	}
	return out, nil
}
