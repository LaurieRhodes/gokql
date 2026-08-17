package engine

import (
	"fmt"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"	
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// applyJoin implements hash join between the input (left) table and a right-side subquery.
//
// Execution:
//   1. Execute the right-side subquery independently
//   2. Build a hash table keyed on right join columns
//   3. Stream left rows and probe the hash table
//   4. Emit output rows based on join kind
//
// KQL join semantics:
//   - Join key columns from right side are excluded from output (they duplicate left keys)
//   - Non-key right columns that conflict with left names get a "1" suffix
//   - inner: only rows with matches on both sides
//   - leftouter: all left rows; unmatched get nulls for right columns
//   - leftanti: left rows with NO match (only left columns in output)
//   - leftsemi: left rows WITH a match (only left columns in output)
func (e *Engine) applyJoin(left *types.Table, op *parser.JoinOp) (*types.Table, error) {
	// Step 1: Execute right-side subquery
	right, err := e.executeQuery(op.Right)
	if err != nil {
		return nil, fmt.Errorf("join right side: %w", err)
	}

	// Step 2: Resolve join column indices
	leftKeyIdxs := make([]int, len(op.OnClauses))
	rightKeyIdxs := make([]int, len(op.OnClauses))
	for i, clause := range op.OnClauses {
		leftKeyIdxs[i] = left.Schema.ColumnIndex(clause.LeftColumn)
		if leftKeyIdxs[i] < 0 {
			return nil, fmt.Errorf("join: left column %q not found", clause.LeftColumn)
		}
		rightKeyIdxs[i] = right.Schema.ColumnIndex(clause.RightColumn)
		if rightKeyIdxs[i] < 0 {
			return nil, fmt.Errorf("join: right column %q not found", clause.RightColumn)
		}
	}

	// Step 3: Build output schema
	outputSchema, rightOutputIdxs := buildJoinSchema(&left.Schema, &right.Schema, op)

	// Step 4: Build hash table from right side
	// Key: concatenated string of join column values
	// Value: list of right-side row indices
	rightIndex := make(map[string][]int)
	for i, row := range right.Rows {
		key := joinKey(row, rightKeyIdxs)
		rightIndex[key] = append(rightIndex[key], i)
	}

	// Step 5: Probe and emit
	result := types.NewTable(left.Name, outputSchema)

	switch op.Kind {
	case parser.JoinInner:
		for _, leftRow := range left.Rows {
			key := joinKey(leftRow, leftKeyIdxs)
			if matches, ok := rightIndex[key]; ok {
				for _, ri := range matches {
					outRow := buildJoinRow(leftRow, right.Rows[ri], rightOutputIdxs)
					result.AddRow(outRow)
				}
			}
		}

	case parser.JoinInnerUnique:
		// Real ADX's own default (verified against Microsoft's docs
		// before implementing this: "innerunique (default) — Inner
		// join with left side deduplication... All deduplicated rows
		// from the left table that match rows from the right table").
		// Left rows are deduplicated by join key FIRST — keeping the
		// first-encountered row for each distinct key, dropping the
		// rest — and only then joined, inner-style, against every
		// matching right row. This is NOT the same as deduplicating
		// the final output: a left key with 3 duplicate rows and a
		// right key with 2 matches produces 2 output rows here (one
		// left representative x two right matches), not 6 (which
		// plain JoinInner over the same undeduplicated left rows would
		// produce) and not 1 (which full output dedup would produce).
		seenLeftKey := make(map[string]bool, len(left.Rows))
		for _, leftRow := range left.Rows {
			key := joinKey(leftRow, leftKeyIdxs)
			if seenLeftKey[key] {
				continue
			}
			seenLeftKey[key] = true
			if matches, ok := rightIndex[key]; ok {
				for _, ri := range matches {
					outRow := buildJoinRow(leftRow, right.Rows[ri], rightOutputIdxs)
					result.AddRow(outRow)
				}
			}
		}

	case parser.JoinLeftOuter:
		nullRight := make([]types.Value, len(rightOutputIdxs))
		for _, leftRow := range left.Rows {
			key := joinKey(leftRow, leftKeyIdxs)
			if matches, ok := rightIndex[key]; ok {
				for _, ri := range matches {
					outRow := buildJoinRow(leftRow, right.Rows[ri], rightOutputIdxs)
					result.AddRow(outRow)
				}
			} else {
				outRow := make(types.Row, len(leftRow)+len(nullRight))
				copy(outRow, leftRow)
				copy(outRow[len(leftRow):], nullRight)
				result.AddRow(outRow)
			}
		}

	case parser.JoinLeftAnti:
		for _, leftRow := range left.Rows {
			key := joinKey(leftRow, leftKeyIdxs)
			if _, ok := rightIndex[key]; !ok {
				result.AddRow(leftRow)
			}
		}

	case parser.JoinLeftSemi:
		for _, leftRow := range left.Rows {
			key := joinKey(leftRow, leftKeyIdxs)
			if _, ok := rightIndex[key]; ok {
				result.AddRow(leftRow)
			}
		}

	case parser.JoinRightOuter:
		// Track which right rows were matched
		rightMatched := make([]bool, len(right.Rows))
		nullLeft := make([]types.Value, len(left.Schema.Columns))
		for _, leftRow := range left.Rows {
			key := joinKey(leftRow, leftKeyIdxs)
			if matches, ok := rightIndex[key]; ok {
				for _, ri := range matches {
					rightMatched[ri] = true
					outRow := buildJoinRow(leftRow, right.Rows[ri], rightOutputIdxs)
					result.AddRow(outRow)
				}
			}
		}
		// Emit unmatched right rows
		for ri, row := range right.Rows {
			if !rightMatched[ri] {
				outRow := make(types.Row, len(nullLeft)+len(rightOutputIdxs))
				copy(outRow, nullLeft)
				for oi, ri2 := range rightOutputIdxs {
					outRow[len(nullLeft)+oi] = row[ri2]
				}
				result.AddRow(outRow)
			}
		}

	case parser.JoinRightAnti:
		rightMatched := make(map[string]bool)
		for _, leftRow := range left.Rows {
			key := joinKey(leftRow, leftKeyIdxs)
			if _, ok := rightIndex[key]; ok {
				rightMatched[key] = true
			}
		}
		for _, rightRow := range right.Rows {
			key := joinKey(rightRow, rightKeyIdxs)
			if !rightMatched[key] {
				result.AddRow(rightRow)
			}
		}

	case parser.JoinRightSemi:
		rightMatched := make(map[string]bool)
		for _, leftRow := range left.Rows {
			key := joinKey(leftRow, leftKeyIdxs)
			if _, ok := rightIndex[key]; ok {
				rightMatched[key] = true
			}
		}
		// Emit right rows that had a match (deduplicated by key)
		emitted := make(map[string]bool)
		for _, rightRow := range right.Rows {
			key := joinKey(rightRow, rightKeyIdxs)
			if rightMatched[key] && !emitted[key] {
				emitted[key] = true
				result.AddRow(rightRow)
			}
		}

	case parser.JoinFullOuter:
		rightMatched := make([]bool, len(right.Rows))
		nullRight := make([]types.Value, len(rightOutputIdxs))
		nullLeft := make([]types.Value, len(left.Schema.Columns))
		for _, leftRow := range left.Rows {
			key := joinKey(leftRow, leftKeyIdxs)
			if matches, ok := rightIndex[key]; ok {
				for _, ri := range matches {
					rightMatched[ri] = true
					outRow := buildJoinRow(leftRow, right.Rows[ri], rightOutputIdxs)
					result.AddRow(outRow)
				}
			} else {
				outRow := make(types.Row, len(leftRow)+len(nullRight))
				copy(outRow, leftRow)
				copy(outRow[len(leftRow):], nullRight)
				result.AddRow(outRow)
			}
		}
		for ri, row := range right.Rows {
			if !rightMatched[ri] {
				outRow := make(types.Row, len(nullLeft)+len(rightOutputIdxs))
				copy(outRow, nullLeft)
				for oi, ri2 := range rightOutputIdxs {
					outRow[len(nullLeft)+oi] = row[ri2]
				}
				result.AddRow(outRow)
			}
		}

	default:
		return nil, fmt.Errorf("unsupported join kind: %s", op.Kind)
	}

	return result, nil
}

// buildJoinSchema constructs the output schema for a join.
// Returns the schema and the indices of right-side columns to include in output.
//
// KQL rules:
//   - All left columns are included
//   - Right join key columns are excluded (they duplicate left keys)
//   - Right non-key columns are included, with "1" suffix if name conflicts with left
func buildJoinSchema(left, right *types.Schema, op *parser.JoinOp) (types.Schema, []int) {
	// For anti/semi joins, output is just the probe side
	switch op.Kind {
	case parser.JoinLeftAnti, parser.JoinLeftSemi:
		return *left, nil
	case parser.JoinRightAnti, parser.JoinRightSemi:
		return *right, nil
	}

	// Build set of right-side join key column indices to exclude
	rightKeySet := make(map[int]bool)
	for _, clause := range op.OnClauses {
		idx := right.ColumnIndex(clause.RightColumn)
		if idx >= 0 {
			rightKeySet[idx] = true
		}
	}

	// Build left column name set for conflict detection
	leftNames := make(map[string]bool)
	for _, col := range left.Columns {
		leftNames[col.Name] = true
	}

	schema := types.Schema{Columns: make([]types.Column, 0, len(left.Columns)+len(right.Columns))}
	// All left columns
	schema.Columns = append(schema.Columns, left.Columns...)

	// Non-key right columns
	var rightOutputIdxs []int
	for i, col := range right.Columns {
		if rightKeySet[i] {
			continue // Skip join key columns
		}
		name := col.Name
		if leftNames[name] {
			name = name + "1" // KQL conflict resolution
		}
		schema.Columns = append(schema.Columns, types.Column{Name: name, Type: col.Type})
		rightOutputIdxs = append(rightOutputIdxs, i)
	}

	return schema, rightOutputIdxs
}

// joinKey builds a hash key from a row's join column values.
// Values are type-tagged (typedKey) so equal string forms of different
// types ("1" vs long 1) never match across sides.
func joinKey(row types.Row, keyIdxs []int) string {
	if len(keyIdxs) == 1 {
		return typedKey(row[keyIdxs[0]])
	}
	parts := make([]string, len(keyIdxs))
	for i, idx := range keyIdxs {
		parts[i] = typedKey(row[idx])
	}
	return strings.Join(parts, "\x00")
}

// typedKey formats a value as a map key with a Go-type tag so values of
// different types never collide on their string form. Shared by joinKey
// and graphKey. nil gets its own tag (it must not collide with the
// string "<nil>"); note nil keys still match each other, preserving
// existing join behavior.
func typedKey(v types.Value) string {
	if v == nil {
		return "\x00nil"
	}
	return fmt.Sprintf("%T\x01%v", v, v)
}

// buildJoinRow creates an output row from a left row and matching right row.
func buildJoinRow(leftRow, rightRow types.Row, rightOutputIdxs []int) types.Row {
	out := make(types.Row, len(leftRow)+len(rightOutputIdxs))
	copy(out, leftRow)
	for i, ri := range rightOutputIdxs {
		out[len(leftRow)+i] = rightRow[ri]
	}
	return out
}

// applyLookup implements the lookup operator: a simplified enrichment join
// against a dimension table referenced by name. Semantics are those of a join
// with the same kind (default leftouter), so it delegates to applyJoin with
// the table name wrapped as a bare-source subquery. Name resolution against
// let bindings and the catalog happens inside executeQuery.
func (e *Engine) applyLookup(left *types.Table, op *parser.LookupOp) (*types.Table, error) {
	joinOp := &parser.JoinOp{
		Kind:      op.Kind,
		Right:     &parser.Query{Source: op.TableName},
		OnClauses: op.OnClauses,
	}
	result, err := e.applyJoin(left, joinOp)
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	return result, nil
}
