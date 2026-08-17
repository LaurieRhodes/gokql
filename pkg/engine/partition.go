package engine

// partition.go — the partition operator: verified against real ADX's
// own partition operator docs before implementing. Groups the input
// by ByColumn's distinct values, runs the subquery pipeline
// independently against each group's implicit subtable (matching
// applyMvApply's own e.applyOperator accumulation pattern,
// operators.go — a sourceless subquery pipeline applied against an
// implicit per-row/per-group subtable, not a fresh, separate query),
// and returns the union of every partition's result.
//
// See PartitionOp's own doc comment (ast.go) for the deliberately
// out-of-scope real-ADX capability (the braces "{SubQueryWithSource}"
// explicit-source form) and why.

import (
	"fmt"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func (e *Engine) applyPartition(input *types.Table, op *parser.PartitionOp) (*types.Table, error) {
	colIdx := input.Schema.ColumnIndex(op.ByColumn)
	if colIdx < 0 {
		return nil, fmt.Errorf("partition: column %q not found", op.ByColumn)
	}

	// Group rows by the partition key's formatted value, preserving
	// FIRST-SEEN key order (a stable, deterministic union order across
	// runs) — real ADX's own docs don't specify a particular row
	// order for the unioned result, so first-seen is a reasonable,
	// simple choice, not an attempt to replicate any specific
	// undocumented real-ADX ordering.
	groups := map[string][]types.Row{}
	var keyOrder []string
	for _, row := range input.Rows {
		key := fmt.Sprintf("%v", row[colIdx])
		if _, seen := groups[key]; !seen {
			keyOrder = append(keyOrder, key)
		}
		groups[key] = append(groups[key], row)
	}

	var output *types.Table
	for _, key := range keyOrder {
		sub := types.NewTable(input.Name, input.Schema)
		sub.Rows = groups[key]

		result := sub
		var err error
		for _, subOp := range op.Operators {
			result, err = e.applyOperator(result, subOp)
			if err != nil {
				return nil, fmt.Errorf("partition %s: %w", op.ByColumn, err)
			}
		}

		if output == nil {
			output = types.NewTable(input.Name, result.Schema)
		}
		if len(result.Schema.Columns) != len(output.Schema.Columns) {
			return nil, fmt.Errorf("partition %s: inconsistent subquery schema across partitions", op.ByColumn)
		}
		for _, r := range result.Rows {
			output.AddRow(r)
		}
	}

	if output == nil {
		// No input rows at all: derive schema by running the pipeline
		// against an empty subtable, matching applyMvApply's own
		// identical fallback for the same reason — downstream
		// operators still need to see a valid, correctly-shaped
		// (if empty) schema.
		empty := types.NewTable(input.Name, input.Schema)
		result := empty
		var err error
		for _, subOp := range op.Operators {
			result, err = e.applyOperator(result, subOp)
			if err != nil {
				return nil, fmt.Errorf("partition %s: %w", op.ByColumn, err)
			}
		}
		output = types.NewTable(input.Name, result.Schema)
	}

	return output, nil
}
