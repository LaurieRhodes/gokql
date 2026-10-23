package engine

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// applyJoin implements hash join between the input (left) table and a right-side subquery.
//
// Execution:
//  1. Execute the right-side subquery independently
//  2. Build a hash table keyed on right join columns
//  3. Stream left rows and probe the hash table
//  4. Emit output rows based on join kind
//
// KQL join semantics (Kusto.Language's result schema):
//   - every right column is kept, the join keys included, after the
//     left columns; a name already taken gets the next free numeric
//     suffix (a → a1), so `T | join (U) on a` has a and a1
//   - unmatched rows of an outer join carry nulls on the missing side,
//     the missing side's keys included
//   - inner: only rows with matches on both sides
//   - leftouter: all left rows; unmatched get nulls for right columns
//   - leftanti: left rows with NO match (only left columns in output)
//   - leftsemi: left rows WITH a match (only left columns in output)
func (e *Engine) applyJoin(left *types.Table, op *parser.JoinOp) (*types.Table, error) {
	jp, err := e.prepareJoin(left, op)
	if err != nil {
		return nil, err
	}
	result := types.NewTable(left.Name, jp.schema)
	err = jp.emit(left, func(r types.Row) error {
		result.AddRow(r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// joinPrep is a join with its right side executed and hashed, ready
// to probe with left rows.
type joinPrep struct {
	op              *parser.JoinOp
	right           *types.Table
	leftKeyIdxs     []int
	rightKeyIdxs    []int
	rightOutputIdxs []int
	rightIndex      map[string][]int
	schema          types.Schema
	// alloc, when set, supplies output rows instead of make.
	alloc func() types.Row
}

func (jp *joinPrep) joined(leftRow, rightRow types.Row) types.Row {
	if jp.alloc != nil {
		return buildJoinRowInto(jp.alloc(), leftRow, rightRow, jp.rightOutputIdxs)
	}
	return buildJoinRow(leftRow, rightRow, jp.rightOutputIdxs)
}

func (e *Engine) prepareJoin(left *types.Table, op *parser.JoinOp) (*joinPrep, error) {
	right, err := e.executeQuery(op.Right)
	if err != nil {
		return nil, fmt.Errorf("join right side: %w", err)
	}
	jp := &joinPrep{op: op, right: right,
		leftKeyIdxs:  make([]int, len(op.OnClauses)),
		rightKeyIdxs: make([]int, len(op.OnClauses)),
	}
	for i, clause := range op.OnClauses {
		jp.leftKeyIdxs[i] = left.Schema.ColumnIndex(clause.LeftColumn)
		if jp.leftKeyIdxs[i] < 0 {
			return nil, fmt.Errorf("join: left column %q not found", clause.LeftColumn)
		}
		jp.rightKeyIdxs[i] = right.Schema.ColumnIndex(clause.RightColumn)
		if jp.rightKeyIdxs[i] < 0 {
			return nil, fmt.Errorf("join: right column %q not found", clause.RightColumn)
		}
	}
	jp.schema, jp.rightOutputIdxs = buildJoinSchema(&left.Schema, &right.Schema, op)
	// Key: concatenated join column values; value: right row indices.
	jp.rightIndex = make(map[string][]int)
	for i, row := range right.Rows {
		key := joinKey(row, jp.rightKeyIdxs)
		jp.rightIndex[key] = append(jp.rightIndex[key], i)
	}
	return jp, nil
}

// outputRows is the number of rows emit will produce for kinds whose
// output can multiply (inner and the outer joins), or -1.
func (jp *joinPrep) outputRows(left *types.Table) int {
	switch jp.op.Kind {
	case parser.JoinInner, parser.JoinLeftOuter, parser.JoinRightOuter, parser.JoinFullOuter:
	default:
		return -1
	}
	n := 0
	for _, leftRow := range left.Rows {
		if m, ok := jp.rightIndex[joinKey(leftRow, jp.leftKeyIdxs)]; ok {
			n += len(m)
		} else if jp.op.Kind == parser.JoinLeftOuter || jp.op.Kind == parser.JoinFullOuter {
			n++
		}
	}
	return n
}

// emit probes the hash table with left's rows and passes each output
// row, in output order, to out.
func (jp *joinPrep) emit(left *types.Table, out func(types.Row) error) error {
	op, right := jp.op, jp.right
	leftKeyIdxs, rightKeyIdxs, rightOutputIdxs, rightIndex := jp.leftKeyIdxs, jp.rightKeyIdxs, jp.rightOutputIdxs, jp.rightIndex
	switch op.Kind {
	case parser.JoinInner:
		for _, leftRow := range left.Rows {
			key := joinKey(leftRow, leftKeyIdxs)
			if matches, ok := rightIndex[key]; ok {
				for _, ri := range matches {
					if err := out(jp.joined(leftRow, right.Rows[ri])); err != nil {
						return err
					}
				}
			}
		}

	case parser.JoinInnerUnique:
		// ADX's default: left rows are deduplicated by join key first
		// (the first row per key is kept), then inner-joined against
		// every matching right row. A left key with 3 rows and 2 right
		// matches gives 2 rows, not 6 and not 1.
		seenLeftKey := make(map[string]bool, len(left.Rows))
		for _, leftRow := range left.Rows {
			key := joinKey(leftRow, leftKeyIdxs)
			if seenLeftKey[key] {
				continue
			}
			seenLeftKey[key] = true
			if matches, ok := rightIndex[key]; ok {
				for _, ri := range matches {
					if err := out(jp.joined(leftRow, right.Rows[ri])); err != nil {
						return err
					}
				}
			}
		}

	case parser.JoinLeftOuter:
		for _, leftRow := range left.Rows {
			key := joinKey(leftRow, leftKeyIdxs)
			if matches, ok := rightIndex[key]; ok {
				for _, ri := range matches {
					if err := out(jp.joined(leftRow, right.Rows[ri])); err != nil {
						return err
					}
				}
			} else {
				outRow := make(types.Row, len(leftRow)+len(rightOutputIdxs))
				copy(outRow, leftRow)
				if err := out(outRow); err != nil {
					return err
				}
			}
		}

	case parser.JoinLeftAnti:
		for _, leftRow := range left.Rows {
			if _, ok := rightIndex[joinKey(leftRow, leftKeyIdxs)]; !ok {
				if err := out(leftRow); err != nil {
					return err
				}
			}
		}

	case parser.JoinLeftSemi:
		for _, leftRow := range left.Rows {
			if _, ok := rightIndex[joinKey(leftRow, leftKeyIdxs)]; ok {
				if err := out(leftRow); err != nil {
					return err
				}
			}
		}

	case parser.JoinRightOuter, parser.JoinFullOuter:
		rightMatched := make([]bool, len(right.Rows))
		nLeft := len(left.Schema.Columns)
		for _, leftRow := range left.Rows {
			key := joinKey(leftRow, leftKeyIdxs)
			if matches, ok := rightIndex[key]; ok {
				for _, ri := range matches {
					rightMatched[ri] = true
					if err := out(jp.joined(leftRow, right.Rows[ri])); err != nil {
						return err
					}
				}
			} else if op.Kind == parser.JoinFullOuter {
				outRow := make(types.Row, len(leftRow)+len(rightOutputIdxs))
				copy(outRow, leftRow)
				if err := out(outRow); err != nil {
					return err
				}
			}
		}
		// Unmatched right rows, with nulls on the left.
		for ri, row := range right.Rows {
			if !rightMatched[ri] {
				outRow := make(types.Row, nLeft+len(rightOutputIdxs))
				for oi, ri2 := range rightOutputIdxs {
					outRow[nLeft+oi] = row[ri2]
				}
				if err := out(outRow); err != nil {
					return err
				}
			}
		}

	case parser.JoinRightAnti, parser.JoinRightSemi:
		rightMatched := make(map[string]bool)
		for _, leftRow := range left.Rows {
			key := joinKey(leftRow, leftKeyIdxs)
			if _, ok := rightIndex[key]; ok {
				rightMatched[key] = true
			}
		}
		// rightsemi keeps every matching right row, as leftsemi keeps
		// every matching left row; rightanti the others.
		want := op.Kind == parser.JoinRightSemi
		for _, rightRow := range right.Rows {
			if rightMatched[joinKey(rightRow, rightKeyIdxs)] == want {
				if err := out(rightRow); err != nil {
					return err
				}
			}
		}

	default:
		return fmt.Errorf("unsupported join kind: %s", op.Kind)
	}
	return nil
}

// buildJoinSchema constructs the output schema for a join and the
// right-side columns to emit (all of them, keys included).
func buildJoinSchema(left, right *types.Schema, op *parser.JoinOp) (types.Schema, []int) {
	// For anti/semi joins, output is just the probe side
	switch op.Kind {
	case parser.JoinLeftAnti, parser.JoinLeftSemi:
		return *left, nil
	case parser.JoinRightAnti, parser.JoinRightSemi:
		return *right, nil
	}

	// A right column whose name a left column has is renamed to the
	// first name+N (N = 1, 2, ...) used by neither input nor an earlier
	// rename, as Kusto.Language does: left (a) with right (k, a, a1)
	// gives a, k, a2, a1.
	leftNames := make(map[string]bool, len(left.Columns))
	taken := make(map[string]bool, len(left.Columns)+len(right.Columns))
	schema := types.Schema{Columns: make([]types.Column, 0, len(left.Columns)+len(right.Columns))}
	for _, col := range left.Columns {
		schema.Columns = append(schema.Columns, col)
		leftNames[col.Name] = true
		taken[col.Name] = true
	}
	for _, col := range right.Columns {
		taken[col.Name] = true
	}
	rightKeys := make(map[int]bool)
	if op.DropRightKeys {
		for _, clause := range op.OnClauses {
			if idx := right.ColumnIndex(clause.RightColumn); idx >= 0 {
				rightKeys[idx] = true
			}
		}
	}
	rightOutputIdxs := make([]int, 0, len(right.Columns))
	for i, col := range right.Columns {
		if rightKeys[i] {
			continue
		}
		name := col.Name
		if leftNames[name] {
			for n := 1; ; n++ {
				cand := fmt.Sprintf("%s%d", col.Name, n)
				if !taken[cand] {
					name = cand
					break
				}
			}
		}
		taken[name] = true
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
	// Fast paths produce exactly what the Sprintf fallback would.
	switch x := v.(type) {
	case nil:
		return "\x00nil"
	case string:
		return "string\x01" + x
	case int64:
		return "int64\x01" + strconv.FormatInt(x, 10)
	case int32:
		return "int32\x01" + strconv.FormatInt(int64(x), 10)
	case int:
		return "int\x01" + strconv.Itoa(x)
	case float64:
		return "float64\x01" + strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		return "bool\x01" + strconv.FormatBool(x)
	}
	return fmt.Sprintf("%T\x01%v", v, v)
}

// buildJoinRow creates an output row from a left row and matching right row.
func buildJoinRow(leftRow, rightRow types.Row, rightOutputIdxs []int) types.Row {
	return buildJoinRowInto(make(types.Row, len(leftRow)+len(rightOutputIdxs)), leftRow, rightRow, rightOutputIdxs)
}

// buildJoinRowInto fills out (len(leftRow)+len(rightOutputIdxs) long).
func buildJoinRowInto(out, leftRow, rightRow types.Row, rightOutputIdxs []int) types.Row {
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
	right := op.Right
	if right == nil {
		right = &parser.Query{Source: op.TableName}
	}
	joinOp := &parser.JoinOp{
		Kind:          op.Kind,
		Right:         right,
		OnClauses:     op.OnClauses,
		DropRightKeys: true,
	}
	result, err := e.applyJoin(left, joinOp)
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	return result, nil
}
