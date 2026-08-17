package engine

import (
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"	
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// --- Operator Implementations ---

func (e *Engine) applyWhere(input *types.Table, op *parser.WhereOp) (*types.Table, error) {
	// substituteToScalars runs ONCE here, before the per-row loop, not
	// per row inside it — see its own doc comment (eval.go) for why:
	// this is what makes `T | where X > toscalar(Y | summarize max(Z))`
	// both correct (evaluated once, matching real ADX's own semantics)
	// and safe (a real *Engine already in scope here, never shared
	// package state).
	predicate, err := substituteToScalars(e, op.Predicate)
	if err != nil {
		return nil, fmt.Errorf("where: %w", err)
	}

	result := types.NewTable(input.Name, input.Schema)

	for _, row := range input.Rows {
		val, err := evalExpr(predicate, &input.Schema, row)
		if err != nil {
			return nil, fmt.Errorf("where: %w", err)
		}
		if b, ok := val.(bool); ok && b {
			result.AddRow(row)
		}
	}
	return result, nil
}

func (e *Engine) applyProject(input *types.Table, op *parser.ProjectOp) (*types.Table, error) {
	// substituteToScalars runs ONCE per computed item here, before both
	// the type-inference pass below AND the per-row loop further down
	// — same reasoning, same position in the flow, as applyExtend's own
	// identical wiring immediately above this function.
	items := make([]parser.ProjectItem, len(op.Items))
	for i, item := range op.Items {
		if item.Expr == nil {
			items[i] = item
			continue
		}
		rewritten, err := substituteToScalars(e, item.Expr)
		if err != nil {
			return nil, fmt.Errorf("project %q: %w", item.Name, err)
		}
		items[i] = parser.ProjectItem{Name: item.Name, Expr: rewritten}
	}

	// Build new schema: passthrough columns keep their type, computed
	// columns infer type from the expression (like extend).
	newSchema := types.Schema{}
	srcIdx := make([]int, len(items)) // -1 for computed items

	for i, item := range items {
		if item.Expr == nil {
			idx := input.Schema.ColumnIndex(item.Name)
			if idx < 0 {
				return nil, fmt.Errorf("project: column %q not found", item.Name)
			}
			newSchema.Columns = append(newSchema.Columns, input.Schema.Columns[idx])
			srcIdx[i] = idx
			continue
		}
		newSchema.Columns = append(newSchema.Columns, types.Column{
			Name: item.Name,
			Type: inferExprType(item.Expr, &input.Schema),
		})
		srcIdx[i] = -1
	}

	result := types.NewTable(input.Name, newSchema)
	for _, row := range input.Rows {
		newRow := make(types.Row, len(items))
		for i, item := range items {
			if srcIdx[i] >= 0 {
				newRow[i] = row[srcIdx[i]]
				continue
			}
			val, err := evalExpr(item.Expr, &input.Schema, row)
			if err != nil {
				return nil, fmt.Errorf("project %q: %w", item.Name, err)
			}
			newRow[i] = val
		}
		result.AddRow(newRow)
	}
	return result, nil
}

func (e *Engine) applyExtend(input *types.Table, op *parser.ExtendOp) (*types.Table, error) {
	// Standard KQL semantics: extend Col = expr REPLACES Col in place if
	// it already exists in the input schema, rather than appending a
	// second, same-named column. Previously this always appended,
	// silently producing a duplicate column name — any later reference
	// to that name (project, another extend, output) resolved to the
	// FIRST match, which was the untouched original, so
	// `extend X = case(X == "old", "new", X)` silently kept "old"
	// forever with no error anywhere. Confirmed live via getschema
	// before fixing: the naive implementation produced two columns
	// both literally named X.
	//
	// substituteToScalars runs ONCE per assignment here, before both
	// the type-inference pass below AND the per-row loop further down
	// — see its own doc comment (eval.go) for why. Each assign.Expr is
	// replaced with its substituted form (a plain, real Literal in
	// place of any ToScalarExpr) so inferExprType sees the toscalar()
	// result's OWN real, now-known type, not an inaccurate fallback
	// from an expression type it has no case for at all.
	assignments := make([]parser.Assignment, len(op.Assignments))
	for i, assign := range op.Assignments {
		rewritten, err := substituteToScalars(e, assign.Expr)
		if err != nil {
			return nil, fmt.Errorf("extend %q: %w", assign.Name, err)
		}
		assignments[i] = parser.Assignment{Name: assign.Name, Expr: rewritten}
	}

	// existingIdx maps each assignment to the input-schema column index
	// it replaces, or -1 if it's a genuinely new column appended at the
	// end (matching the previous, still-correct behavior for that case).
	newSchema := types.Schema{Columns: make([]types.Column, len(input.Schema.Columns))}
	copy(newSchema.Columns, input.Schema.Columns)

	existingIdx := make([]int, len(assignments))
	appendCount := 0
	for i, assign := range assignments {
		if idx := newSchema.ColumnIndex(assign.Name); idx >= 0 {
			existingIdx[i] = idx
			newSchema.Columns[idx].Type = inferExprType(assign.Expr, &input.Schema)
		} else {
			existingIdx[i] = -1
			newSchema.Columns = append(newSchema.Columns, types.Column{
				Name: assign.Name,
				Type: inferExprType(assign.Expr, &input.Schema),
			})
			appendCount++
		}
	}

	result := types.NewTable(input.Name, newSchema)
	for _, row := range input.Rows {
		newRow := make(types.Row, len(input.Schema.Columns)+appendCount)
		copy(newRow, row)

		appendPos := len(input.Schema.Columns)
		for i, assign := range assignments {
			// Evaluated against the ORIGINAL row throughout, matching
			// the previous behavior: assignments in the same extend
			// clause see each other's inputs as they were before this
			// extend ran, not sequentially updated mid-clause. This
			// means self-reference (`X = case(X == ..., ..., X)`) reads
			// the pre-extend value of X, which is exactly what's needed
			// for a replace-in-place semantics to make sense.
			val, err := evalExpr(assign.Expr, &input.Schema, row)
			if err != nil {
				return nil, fmt.Errorf("extend %q: %w", assign.Name, err)
			}
			if existingIdx[i] >= 0 {
				newRow[existingIdx[i]] = val
			} else {
				newRow[appendPos] = val
				appendPos++
			}
		}
		result.AddRow(newRow)
	}
	return result, nil
}

func (e *Engine) applyTake(input *types.Table, op *parser.TakeOp) (*types.Table, error) {
	result := types.NewTable(input.Name, input.Schema)
	for i, row := range input.Rows {
		if int64(i) >= op.Count {
			break
		}
		result.AddRow(row)
	}
	return result, nil
}

func (e *Engine) applySample(input *types.Table, op *parser.SampleOp) (*types.Table, error) {
	result := types.NewTable(input.Name, input.Schema)
	n := int(op.Count)
	if n >= len(input.Rows) {
		result.Rows = append(result.Rows, input.Rows...)
		return result, nil
	}
	// Fisher-Yates shuffle on indices, take first n
	indices := make([]int, len(input.Rows))
	for i := range indices {
		indices[i] = i
	}
	for i := len(indices) - 1; i > 0; i-- {
		j := randInt(i + 1)
		indices[i], indices[j] = indices[j], indices[i]
	}
	for _, idx := range indices[:n] {
		result.AddRow(input.Rows[idx])
	}
	return result, nil
}

func (e *Engine) applyCount(input *types.Table) (*types.Table, error) {
	result := types.NewTable("", types.Schema{
		Columns: []types.Column{{Name: "Count", Type: types.TypeLong}},
	})
	result.AddRow(types.Row{int64(len(input.Rows))})
	return result, nil
}

func (e *Engine) applyDistinct(input *types.Table, op *parser.DistinctOp) (*types.Table, error) {
	// Build projection indices
	colIndices := make([]int, len(op.Columns))
	newSchema := types.Schema{}
	for i, colName := range op.Columns {
		idx := input.Schema.ColumnIndex(colName)
		if idx < 0 {
			return nil, fmt.Errorf("distinct: column %q not found", colName)
		}
		colIndices[i] = idx
		newSchema.Columns = append(newSchema.Columns, input.Schema.Columns[idx])
	}

	result := types.NewTable(input.Name, newSchema)
	seen := make(map[string]bool)

	for _, row := range input.Rows {
		// Build key from projected values
		key := rowKey(row, colIndices, &input.Schema)
		if !seen[key] {
			seen[key] = true
			newRow := make(types.Row, len(colIndices))
			for i, idx := range colIndices {
				newRow[i] = row[idx]
			}
			result.AddRow(newRow)
		}
	}
	return result, nil
}

// applySampleDistinct implements | sample-distinct NumberOfValues of
// ColumnName — see SampleDistinctOp's own doc comment (ast.go) for
// exactly what "up to N distinct values" means here (a deterministic
// first-N-encountered scan, stopping early once N distinct values are
// found — real ADX itself only documents "biased, not fair," not any
// specific distribution to match).
func (e *Engine) applySampleDistinct(input *types.Table, op *parser.SampleDistinctOp) (*types.Table, error) {
	colIdx := input.Schema.ColumnIndex(op.Column)
	if colIdx < 0 {
		return nil, fmt.Errorf("sample-distinct: column %q not found", op.Column)
	}
	col := input.Schema.Columns[colIdx]

	emptySchema := types.Schema{}
	emptyRow := types.Row{}
	countVal, err := evalExpr(op.Count, &emptySchema, emptyRow)
	if err != nil {
		return nil, fmt.Errorf("sample-distinct: %w", err)
	}
	n := types.ToInt64(countVal)
	if n < 0 {
		return nil, fmt.Errorf("sample-distinct: NumberOfValues must be non-negative, got %d", n)
	}

	outSchema := types.Schema{Columns: []types.Column{col}}
	output := types.NewTable(input.Name, outSchema)
	if n == 0 {
		return output, nil
	}

	seen := make(map[string]bool)
	for _, row := range input.Rows {
		if int64(len(seen)) >= n {
			break // early exit — the whole point of "distinct" over
			// "fair": once N distinct values are found, stop scanning.
		}
		val := row[colIdx]
		key := types.FormatValue(val, col.Type)
		if seen[key] {
			continue
		}
		seen[key] = true
		output.AddRow(types.Row{val})
	}
	return output, nil
}

func (e *Engine) applyOrderBy(input *types.Table, op *parser.OrderByOp) (*types.Table, error) {
	result := types.NewTable(input.Name, input.Schema)
	result.Rows = make([]types.Row, len(input.Rows))
	copy(result.Rows, input.Rows)

	// Resolve column indices
	type colSort struct {
		idx  int
		typ  types.KQLType
		desc bool
	}
	var sortCols []colSort
	for _, clause := range op.Clauses {
		idx := input.Schema.ColumnIndex(clause.Column)
		if idx < 0 {
			return nil, fmt.Errorf("order by: column %q not found", clause.Column)
		}
		sortCols = append(sortCols, colSort{idx: idx, typ: input.Schema.Columns[idx].Type, desc: clause.Desc})
	}

	sort.SliceStable(result.Rows, func(i, j int) bool {
		for _, sc := range sortCols {
			cmp := types.CompareValues(result.Rows[i][sc.idx], result.Rows[j][sc.idx], sc.typ)
			if cmp == 0 {
				continue
			}
			if sc.desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})

	return result, nil
}

func (e *Engine) applyTop(input *types.Table, op *parser.TopOp) (*types.Table, error) {
	// Bounded top-N heap instead of full sort + take.
	return e.applyTopN(input, &parser.OrderByOp{
		Clauses: []parser.OrderClause{{Column: op.By, Desc: op.Desc}},
	}, op.Count)
}

func (e *Engine) applySummarize(input *types.Table, op *parser.SummarizeOp) (*types.Table, error) {
	// Build output schema: aggregation columns + group-by columns.
	// percentiles() is special — one call expands to N output columns.
	// arg_max(expr, *)/arg_min(expr, *) are the same shape, expanding
	// to every source column instead of a fixed count — see the
	// dedicated branch below.
	type aggSlot struct {
		agg         parser.Aggregation
		pctValues   []float64 // non-nil only for percentiles() expansion
		argStarCols []string  // non-nil only for arg_max/arg_min(expr, *) — source column names, in output order, matching colIndices positionally
		argWantMax  bool      // only meaningful when argStarCols != nil
		colIndices  []int     // output column indices for this aggregation
	}
	outSchema := types.Schema{}
	var slots []aggSlot

	// Group-by names, computed up front so the star-expansion below
	// knows which source columns to EXCLUDE (avoiding a duplicate
	// column when a group-by key is also one of the source table's
	// own columns) — verified against a real, documented example
	// before relying on this exclusion rule: summarize arg_max(Version,
	// *) by Fruit produces columns Fruit, Version, Color -- Fruit
	// appears exactly once, not duplicated, even though Fruit is also
	// one of the source table's own columns that "*" would otherwise
	// include.
	byNames := make(map[string]bool, len(op.ByExprs))
	for _, byExpr := range op.ByExprs {
		byNames[byExpr.Name] = true
	}

	for _, agg := range op.Aggregations {
		if (agg.Function == "arg_max" || agg.Function == "arg_min") &&
			len(agg.Args) == 2 {
			if _, isStar := agg.Args[1].(*parser.StarExpr); isStar {
				// Column order here follows THIS engine's existing,
				// already-established summarize convention
				// (aggregation columns before group-by columns) rather
				// than real ADX's own column order for this specific
				// case (group-by columns first) -- a known, separate,
				// purely cosmetic divergence noted but not fixed here,
				// since column resolution throughout this engine is
				// always by name, never by position, so it doesn't
				// affect correctness. Within the aggregation-columns
				// block itself, source order is preserved (expr's own
				// column first if it's a bare ColumnRef, matching real
				// ADX, then remaining source columns in their original
				// order), just not interleaved with the by-columns the
				// same way real ADX does.
				included := make(map[string]bool, len(input.Schema.Columns))
				for name := range byNames {
					included[name] = true
				}
				var starCols []string
				if ref, ok := agg.Args[0].(*parser.ColumnRef); ok && !included[ref.Name] {
					starCols = append(starCols, ref.Name)
					included[ref.Name] = true
				}
				for _, col := range input.Schema.Columns {
					if !included[col.Name] {
						starCols = append(starCols, col.Name)
						included[col.Name] = true
					}
				}
				var indices []int
				for _, colName := range starCols {
					srcCol := input.Schema.ColumnByName(colName)
					colType := types.TypeDynamic
					if srcCol != nil {
						colType = srcCol.Type
					}
					indices = append(indices, len(outSchema.Columns))
					outSchema.Columns = append(outSchema.Columns, types.Column{Name: colName, Type: colType})
				}
				slots = append(slots, aggSlot{
					agg:         agg,
					argStarCols: starCols,
					argWantMax:  agg.Function == "arg_max",
					colIndices:  indices,
				})
				continue
			}
		}
		if agg.Function == "percentiles" && len(agg.Args) >= 3 {
			// percentiles(expr, p1, p2, ..., pN) → N columns
			var pctVals []float64
			var indices []int
			exprName := aggExprName(agg.Args[0])
			for _, pArg := range agg.Args[1:] {
				pVal, _ := evalExpr(pArg, &input.Schema, types.Row{})
				p := types.ToFloat64(pVal)
				pctVals = append(pctVals, p)
				colName := fmt.Sprintf("percentile_%s_%g", exprName, p)
				if agg.Name != "" && len(agg.Args) == 3 {
					// Single percentile with alias: use the alias
					colName = fmt.Sprintf("percentile_%s_%g", exprName, p)
				}
				indices = append(indices, len(outSchema.Columns))
				outSchema.Columns = append(outSchema.Columns, types.Column{Name: colName, Type: types.TypeReal})
			}
			slots = append(slots, aggSlot{agg: agg, pctValues: pctVals, colIndices: indices})
		} else {
			outType := inferAggType(agg.Function, agg.Args, &input.Schema)
			idx := len(outSchema.Columns)
			outSchema.Columns = append(outSchema.Columns, types.Column{Name: agg.Name, Type: outType})
			slots = append(slots, aggSlot{agg: agg, colIndices: []int{idx}})
		}
	}
	byOffset := len(outSchema.Columns)
	for _, byExpr := range op.ByExprs {
		byType := inferExprType(byExpr.Expr, &input.Schema)
		outSchema.Columns = append(outSchema.Columns, types.Column{Name: byExpr.Name, Type: byType})
	}

	// Group rows by evaluating by-expressions
	type groupEntry struct {
		key    string
		byVals []types.Value // evaluated by-expression values for first row in group
		rows   []types.Row
	}
	groups := make(map[string]*groupEntry)
	groupOrder := make([]string, 0)

	for _, row := range input.Rows {
		// Evaluate all by-expressions for this row
		byVals := make([]types.Value, len(op.ByExprs))
		keyParts := make([]string, len(op.ByExprs))
		for i, byExpr := range op.ByExprs {
			val, err := evalExpr(byExpr.Expr, &input.Schema, row)
			if err != nil {
				return nil, fmt.Errorf("summarize by %s: %w", byExpr.Name, err)
			}
			byVals[i] = val
			keyParts[i] = fmt.Sprintf("%v", val)
		}
		key := strings.Join(keyParts, "\x00")

		if g, exists := groups[key]; exists {
			g.rows = append(g.rows, row)
		} else {
			groupOrder = append(groupOrder, key)
			groups[key] = &groupEntry{key: key, byVals: byVals, rows: []types.Row{row}}
		}
	}

	// Compute aggregations per group
	result := types.NewTable("", outSchema)
	for _, key := range groupOrder {
		g := groups[key]
		outRow := make(types.Row, len(outSchema.Columns))

		// Aggregation values
		for _, slot := range slots {
			if slot.argStarCols != nil {
				// arg_max/arg_min(expr, *) — find the best row once,
				// then pull every star-expanded column's value from
				// it. Null handling (a group where every candidate
				// exprToMax is null still produces a row, via the
				// first row encountered) lives in findArgBestRow
				// itself, shared with the explicit-column form in
				// computeAgg — see that function's doc comment for
				// why, verified against a real documented example.
				bestRow, err := findArgBestRow(slot.agg.Args[0], g.rows, &input.Schema, slot.argWantMax)
				if err != nil {
					return nil, fmt.Errorf("summarize %s: %w", slot.agg.Function, err)
				}
				for i, colName := range slot.argStarCols {
					if bestRow == nil {
						continue
					}
					srcIdx := input.Schema.ColumnIndex(colName)
					if srcIdx >= 0 {
						outRow[slot.colIndices[i]] = bestRow[srcIdx]
					}
				}
			} else if slot.pctValues != nil {
				// percentiles() — compute all requested percentiles
				vals, err := computePercentiles(slot.agg, slot.pctValues, g.rows, &input.Schema)
				if err != nil {
					return nil, fmt.Errorf("summarize percentiles: %w", err)
				}
				for i, idx := range slot.colIndices {
					if i < len(vals) {
						outRow[idx] = vals[i]
					}
				}
			} else {
				val, err := computeAgg(slot.agg, g.rows, &input.Schema)
				if err != nil {
					return nil, fmt.Errorf("summarize %s: %w", slot.agg.Function, err)
				}
				outRow[slot.colIndices[0]] = val
			}
		}

		// Group-by values (evaluated from first row)
		for i := range op.ByExprs {
			outRow[byOffset+i] = g.byVals[i]
		}

		result.AddRow(outRow)
	}

	return result, nil
}

// --- Aggregation ---

// applyUnion stacks rows from additional sources onto the input table.
// Schema is the superset of all input schemas — missing columns filled with nil.
// aggExprName extracts a name for an expression (for auto-naming percentile columns).
func aggExprName(expr parser.Expr) string {
	switch e := expr.(type) {
	case *parser.ColumnRef:
		return e.Name
	default:
		return "expr"
	}
}

func randInt(n int) int {
	return rand.Intn(n)
}

func (e *Engine) applyUnion(input *types.Table, op *parser.UnionOp) (*types.Table, error) {
	// Collect all tables: input + each union source
	tables := []*types.Table{input}

	for _, src := range op.Sources {
		// Check let-bound tables first
		if len(src.Operators) == 0 && e.letContext != nil {
			if letTable, ok := e.letContext.Tables[src.Source]; ok {
				tables = append(tables, letTable)
				continue
			}
		}
		// Execute the source query
		result, err := e.executeQuery(src)
		if err != nil {
			return nil, fmt.Errorf("union source %q: %w", src.Source, err)
		}
		tables = append(tables, result)
	}

	// Build merged schema: superset of all columns preserving order
	mergedSchema := buildUnionSchema(tables)



	// Create output table and map rows from each input
	output := types.NewTable(input.Name, mergedSchema)

	for _, tbl := range tables {
		// Build column index mapping: merged col index → source col index (-1 if missing)
		colMap := make([]int, len(mergedSchema.Columns))
		for i, mc := range mergedSchema.Columns {
			colMap[i] = tbl.Schema.ColumnIndex(mc.Name)
		}

		for _, row := range tbl.Rows {
			newRow := make(types.Row, len(mergedSchema.Columns))
			for i, srcIdx := range colMap {
				if srcIdx >= 0 && srcIdx < len(row) {
					newRow[i] = row[srcIdx]
				}
				// else nil (missing column)
			}
			output.AddRow(newRow)
		}
	}

	return output, nil
}

// buildUnionSchema merges schemas from multiple tables.
// Columns appear in first-seen order; if a column appears in multiple tables
// with different types, the first type wins.
func buildUnionSchema(tables []*types.Table) types.Schema {
	seen := make(map[string]bool)
	var columns []types.Column

	for _, tbl := range tables {
		for _, col := range tbl.Schema.Columns {
			if !seen[col.Name] {
				seen[col.Name] = true
				columns = append(columns, col)
			}
		}
	}

	return types.Schema{Columns: columns}
}


// applyMvExpand flattens array-valued columns into separate rows.
// For each row, the array column is parsed as JSON array, and one output row
// is emitted per element with all other columns duplicated.
// If the value is not an array, the row passes through unchanged.
// If the array is empty, the row is omitted.
func (e *Engine) applyMvExpand(input *types.Table, op *parser.MvExpandOp) (*types.Table, error) {
	// Build output schema — same as input but expanded columns may change type
	outSchema := input.Schema
	// Find column indices for expansion
	type expandSpec struct {
		outColIdx int    // Index in output schema
		srcColIdx int    // Index in input schema (-1 if expression)
		name      string // Output column name
		source    parser.Expr
	}
	var specs []expandSpec

	for _, col := range op.Columns {
		idx := input.Schema.ColumnIndex(col.Name)
		if idx < 0 {
			// Column doesn't exist yet — check if source refers to existing column
			if ref, ok := col.Source.(*parser.ColumnRef); ok {
				srcIdx := input.Schema.ColumnIndex(ref.Name)
				if srcIdx < 0 {
					return nil, fmt.Errorf("mv-expand: column %q not found", ref.Name)
				}
				// Aliased: NewName = ExistingCol — add new column
				outSchema.Columns = append(outSchema.Columns, types.Column{
					Name: col.Name,
					Type: types.TypeDynamic,
				})
				specs = append(specs, expandSpec{
					outColIdx: len(outSchema.Columns) - 1,
					srcColIdx: srcIdx,
					name:      col.Name,
					source:    col.Source,
				})
			} else {
				return nil, fmt.Errorf("mv-expand: column %q not found", col.Name)
			}
		} else {
			// Expanding in-place
			specs = append(specs, expandSpec{
				outColIdx: idx,
				srcColIdx: idx,
				name:      col.Name,
				source:    col.Source,
			})
		}
	}

	output := types.NewTable(input.Name, outSchema)

	for _, row := range input.Rows {
		// Get array values for each expand column
		var arrays [][]interface{}
		maxLen := 0

		for _, spec := range specs {
			val, err := evalExpr(spec.source, &input.Schema, row)
			if err != nil || val == nil {
				arrays = append(arrays, nil)
				continue
			}

			// Parse the value as a JSON array
			elements, _ := parseJSONArray(val)
			arrays = append(arrays, elements)
			if len(elements) > maxLen {
				maxLen = len(elements)
			}
		}

		if maxLen == 0 {
			// No array elements — skip this row (KQL behavior)
			// Unless none of the values are arrays, pass through
			allNil := true
			for _, arr := range arrays {
				if arr != nil {
					allNil = false
					break
				}
			}
			if allNil {
				// Non-array value — pass through as single row
				newRow := make(types.Row, len(outSchema.Columns))
				copy(newRow, row)
				output.AddRow(newRow)
			}
			continue
		}

		// Emit one row per element
		for i := 0; i < maxLen; i++ {
			newRow := make(types.Row, len(outSchema.Columns))
			// Copy base row
			copy(newRow, row)

			// Replace expanded columns with element values
			for specIdx, spec := range specs {
				if specIdx < len(arrays) && arrays[specIdx] != nil && i < len(arrays[specIdx]) {
					newRow[spec.outColIdx] = jsonToKQLValue(arrays[specIdx][i])
				} else {
					newRow[spec.outColIdx] = nil
				}
			}
			output.AddRow(newRow)
		}
	}

	return output, nil
}

// parseJSONArray attempts to parse a value as a JSON array.



// applyProjectAway removes specified columns from the output.

// applyMvApply expands an array column row by row, runs the subquery
// pipeline against each per-row expanded table, and unions the results.
// The expanded table carries all original columns copied onto every element
// row, with the element value in the Name column (added if renamed,
// replacing the source column's values otherwise).
func (e *Engine) applyMvApply(input *types.Table, op *parser.MvApplyOp) (*types.Table, error) {
	srcIdx := input.Schema.ColumnIndex(op.SourceCol)
	if srcIdx < 0 {
		return nil, fmt.Errorf("mv-apply: column %q not found", op.SourceCol)
	}

	// Build the expanded-table schema
	subSchema := types.Schema{Columns: make([]types.Column, len(input.Schema.Columns))}
	copy(subSchema.Columns, input.Schema.Columns)
	elemIdx := srcIdx
	if op.Name != op.SourceCol {
		subSchema.Columns = append(subSchema.Columns, types.Column{
			Name: op.Name,
			Type: op.ElementType,
		})
		elemIdx = len(subSchema.Columns) - 1
	} else {
		subSchema.Columns[srcIdx].Type = op.ElementType
	}

	var output *types.Table

	for _, row := range input.Rows {
		// Expand the array cell into a per-row subtable
		sub := types.NewTable(input.Name, subSchema)
		if row[srcIdx] != nil {
			elements, _ := parseJSONArray(row[srcIdx])
			for _, elem := range elements {
				subRow := make(types.Row, len(subSchema.Columns))
				copy(subRow, row)
				subRow[elemIdx] = jsonToKQLValue(elem)
				sub.AddRow(subRow)
			}
		}

		// Run the subquery pipeline
		result := sub
		var err error
		for _, subOp := range op.Operators {
			result, err = e.applyOperator(result, subOp)
			if err != nil {
				return nil, fmt.Errorf("mv-apply: %w", err)
			}
		}

		if output == nil {
			output = types.NewTable(input.Name, result.Schema)
		}
		if len(result.Schema.Columns) != len(output.Schema.Columns) {
			return nil, fmt.Errorf("mv-apply: inconsistent subquery schema across rows")
		}
		for _, r := range result.Rows {
			output.AddRow(r)
		}
	}

	if output == nil {
		// No input rows: derive schema by running the pipeline on an empty
		// expanded table so downstream operators still see valid columns.
		empty := types.NewTable(input.Name, subSchema)
		result := empty
		var err error
		for _, subOp := range op.Operators {
			result, err = e.applyOperator(result, subOp)
			if err != nil {
				return nil, fmt.Errorf("mv-apply: %w", err)
			}
		}
		output = types.NewTable(input.Name, result.Schema)
	}
	return output, nil
}

func (e *Engine) applyProjectAway(input *types.Table, op *parser.ProjectAwayOp) (*types.Table, error) {
	// Build set of columns to remove
	removeSet := make(map[string]bool)
	for _, col := range op.Columns {
		removeSet[strings.ToLower(col)] = true
	}

	// Build new schema excluding removed columns
	var newCols []types.Column
	var keepIndices []int
	for i, col := range input.Schema.Columns {
		if !removeSet[strings.ToLower(col.Name)] {
			newCols = append(newCols, col)
			keepIndices = append(keepIndices, i)
		}
	}

	if len(newCols) == 0 {
		return nil, fmt.Errorf("project-away: cannot remove all columns")
	}

	outSchema := types.Schema{Columns: newCols}
	output := types.NewTable(input.Name, outSchema)

	for _, row := range input.Rows {
		newRow := make(types.Row, len(keepIndices))
		for j, idx := range keepIndices {
			if idx < len(row) {
				newRow[j] = row[idx]
			}
		}
		output.AddRow(newRow)
	}
	return output, nil
}

// applyProjectRename renames specified columns.
func (e *Engine) applyProjectRename(input *types.Table, op *parser.ProjectRenameOp) (*types.Table, error) {
	// Build rename map
	renameMap := make(map[string]string) // oldName -> newName
	for _, r := range op.Renames {
		renameMap[r.OldName] = r.NewName
	}

	// Build new schema with renames applied
	newCols := make([]types.Column, len(input.Schema.Columns))
	for i, col := range input.Schema.Columns {
		newCols[i] = col
		if newName, ok := renameMap[col.Name]; ok {
			newCols[i].Name = newName
		}
	}

	outSchema := types.Schema{Columns: newCols}
	output := types.NewTable(input.Name, outSchema)
	output.Rows = input.Rows // Rows unchanged, just schema
	return output, nil
}

// applyProjectReorder reorders columns: specified columns first, then remaining in original order.
func (e *Engine) applyProjectReorder(input *types.Table, op *parser.ProjectReorderOp) (*types.Table, error) {
	// Track which columns were explicitly listed
	used := make(map[string]bool)
	var newCols []types.Column
	var colMap []int // maps new index → old index

	// First: add requested columns in order
	for _, name := range op.Columns {
		// Strip asc/desc/granny_asc/granny_desc suffixes (KQL allows these)
		cleanName := strings.TrimSpace(name)
		for _, suffix := range []string{" asc", " desc", " granny_asc", " granny_desc"} {
			cleanName = strings.TrimSuffix(cleanName, suffix)
		}
		cleanName = strings.TrimSpace(cleanName)

		for i, col := range input.Schema.Columns {
			if strings.EqualFold(col.Name, cleanName) {
				newCols = append(newCols, col)
				colMap = append(colMap, i)
				used[col.Name] = true
				break
			}
		}
	}

	// Then: append remaining columns in original order
	for i, col := range input.Schema.Columns {
		if !used[col.Name] {
			newCols = append(newCols, col)
			colMap = append(colMap, i)
		}
	}

	outSchema := types.Schema{Columns: newCols}
	output := types.NewTable(input.Name, outSchema)
	for _, row := range input.Rows {
		newRow := make(types.Row, len(colMap))
		for j, oldIdx := range colMap {
			if oldIdx < len(row) {
				newRow[j] = row[oldIdx]
			}
		}
		output.AddRow(newRow)
	}
	return output, nil
}

// applyProjectKeep keeps only columns matching the given patterns (supports * wildcard).
func (e *Engine) applyProjectKeep(input *types.Table, op *parser.ProjectKeepOp) (*types.Table, error) {
	var newCols []types.Column
	var colMap []int

	for i, col := range input.Schema.Columns {
		if matchesAnyPattern(col.Name, op.Patterns) {
			newCols = append(newCols, col)
			colMap = append(colMap, i)
		}
	}

	if len(newCols) == 0 {
		return types.NewTable(input.Name, types.Schema{}), nil
	}

	outSchema := types.Schema{Columns: newCols}
	output := types.NewTable(input.Name, outSchema)
	for _, row := range input.Rows {
		newRow := make(types.Row, len(colMap))
		for j, oldIdx := range colMap {
			if oldIdx < len(row) {
				newRow[j] = row[oldIdx]
			}
		}
		output.AddRow(newRow)
	}
	return output, nil
}

// matchesAnyPattern checks if a column name matches any of the given patterns.
// Supports * as wildcard at start, end, or both.
func matchesAnyPattern(name string, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		lower := strings.ToLower(name)
		lowerPat := strings.ToLower(pattern)

		if lowerPat == "*" {
			return true
		}
		if strings.HasPrefix(lowerPat, "*") && strings.HasSuffix(lowerPat, "*") {
			if strings.Contains(lower, strings.Trim(lowerPat, "*")) {
				return true
			}
		} else if strings.HasSuffix(lowerPat, "*") {
			if strings.HasPrefix(lower, strings.TrimSuffix(lowerPat, "*")) {
				return true
			}
		} else if strings.HasPrefix(lowerPat, "*") {
			if strings.HasSuffix(lower, strings.TrimPrefix(lowerPat, "*")) {
				return true
			}
		} else if strings.EqualFold(name, pattern) {
			return true
		}
	}
	return false
}

// applySerialize materializes row order and optionally adds window-function columns.
// This enables row_number(), prev(), and next() in the computed columns.
func (e *Engine) applySerialize(input *types.Table, op *parser.SerializeOp) (*types.Table, error) {
	if len(op.Columns) == 0 {
		// Plain serialize — just pass through (rows are already ordered)
		return input, nil
	}

	// Build output schema: input columns + new computed columns
	outSchema := input.Schema
	for _, a := range op.Columns {
		outSchema.Columns = append(outSchema.Columns, types.Column{
			Name: a.Name,
			Type: types.TypeDynamic, // will be refined
		})
	}

	output := types.NewTable(input.Name, outSchema)
	inputColCount := len(input.Schema.Columns)

	for rowIdx, row := range input.Rows {
		outRow := make(types.Row, len(outSchema.Columns))
		copy(outRow, row)

		// Evaluate each computed column with window context
		for i, a := range op.Columns {
			val, err := evalExprWithWindow(a.Expr, &input.Schema, row, rowIdx, input.Rows)
			if err != nil {
				return nil, fmt.Errorf("serialize %s: %w", a.Name, err)
			}
			outRow[inputColCount+i] = val
		}
		output.AddRow(outRow)
	}
	return output, nil
}

// applyPrint evaluates expressions and returns a single-row result.
func (e *Engine) applyPrint(op *parser.PrintOp) (*types.Table, error) {
	// Build schema from expression names
	var cols []types.Column
	for _, assign := range op.Expressions {
		name := assign.Name
		if name == "" {
			name = fmt.Sprintf("print_%d", len(cols))
		}
		cols = append(cols, types.Column{Name: name, Type: types.TypeDynamic})
	}

	outSchema := types.Schema{Columns: cols}
	output := types.NewTable("", outSchema)

	row := make(types.Row, len(op.Expressions))
	emptySchema := types.Schema{}
	emptyRow := types.Row{}
	for i, assign := range op.Expressions {
		// substituteToScalars runs first (see its own doc comment,
		// eval.go) so `print result = toscalar(...)` works directly,
		// not just indirectly via a let binding — found live, via this
		// exact test case, that print had its own, separate evalExpr
		// call path never wired for this at all (applyWhere,
		// applyExtend, applyProject, and the let-scalar-binding path
		// were fixed first; print's own, distinct path was missed
		// until a direct test of it specifically caught the gap).
		rewritten, err := substituteToScalars(e, assign.Expr)
		if err != nil {
			return nil, fmt.Errorf("print: %w", err)
		}
		val, err := evalExpr(rewritten, &emptySchema, emptyRow)
		if err != nil {
			return nil, fmt.Errorf("print: %w", err)
		}
		row[i] = val
		// Infer type from the EXPRESSION, via the same inferExprType
		// project/extend/summarize's by-clause already use — not from
		// the runtime Go value's type, which an earlier version of
		// this code did instead. That distinction matters concretely
		// for datetime and timespan: both are represented internally
		// as a plain int64 (UnixNano / 100ns ticks respectively), so a
		// switch on the Go value alone can never tell "this int64 is a
		// date" from "this int64 is a long" -- it's genuinely
		// ambiguous at the value level, only recoverable from the
		// expression that produced it. Found live: print d =
		// datetime(2026-08-10T15:30:00) was typed as long instead of
		// datetime, so the RFC3339Nano/"Z"-suffixed display formatting
		// (types.go's FormatValue, itself correctly UTC throughout)
		// never even ran -- the value shown was the raw UnixNano
		// number, not a date at all, while the UNDERLYING value was
		// already correct (verified via a round trip through
		// todatetime()). This was a type-inference gap specific to
		// print, not a UTC-handling bug -- every other datetime path
		// in this codebase (literal parsing, now()/ago(), display
		// formatting) was already correctly UTC throughout, checked
		// directly rather than assumed, before concluding this was the
		// one real gap.
		// Uses rewritten, not assign.Expr — the substituted form (a
		// real Literal in place of any ToScalarExpr, with its own
		// actual, now-known type) rather than the original, which
		// inferExprType has no case for at all and would fall back to
		// an inaccurate default type for.
		cols[i].Type = inferExprType(rewritten, &emptySchema)
	}
	output.Schema = types.Schema{Columns: cols}
	output.AddRow(row)
	return output, nil
}

// applyRange generates range's output table — verified against real
// ADX's own range operator docs before implementing this: "start,
// start + step, ... up to and until stop." The output column's type
// matches start's own inferred type (int, long, real, datetime, or
// timespan — the five types real ADX documents as valid here), and
// step is converted to matching units when start/stop are
// datetime/timespan (both stored as int64 -- nanoseconds and 100ns
// ticks respectively, DIFFERENT scales -- via the same toNanos helper
// datetime+timespan arithmetic already uses elsewhere in this engine,
// not a separate, second conversion built for this operator alone).
//
// Both step directions are supported via a single loop condition
// (step > 0 walking up to stop, step < 0 walking down to stop) rather
// than two separate loops -- step == 0 is rejected outright as an
// error, since neither direction would ever terminate.
func (e *Engine) applyRange(op *parser.RangeOp) (*types.Table, error) {
	emptySchema := types.Schema{}
	emptyRow := types.Row{}

	colType := inferExprType(op.Start, &emptySchema)
	if !colType.IsNumeric() {
		return nil, fmt.Errorf("range %s: start must be int, long, real, datetime, or timespan, got %s", op.ColumnName, colType)
	}

	startVal, err := evalExpr(op.Start, &emptySchema, emptyRow)
	if err != nil {
		return nil, fmt.Errorf("range %s: start: %w", op.ColumnName, err)
	}
	stopVal, err := evalExpr(op.Stop, &emptySchema, emptyRow)
	if err != nil {
		return nil, fmt.Errorf("range %s: stop: %w", op.ColumnName, err)
	}
	stepVal, err := evalExpr(op.Step, &emptySchema, emptyRow)
	if err != nil {
		return nil, fmt.Errorf("range %s: step: %w", op.ColumnName, err)
	}

	output := types.NewTable("range", types.Schema{Columns: []types.Column{{Name: op.ColumnName, Type: colType}}})

	if colType == types.TypeReal {
		start := types.ToFloat64(startVal)
		stop := types.ToFloat64(stopVal)
		step := types.ToFloat64(stepVal)
		if step == 0 {
			return nil, fmt.Errorf("range %s: step must not be zero", op.ColumnName)
		}
		for v := start; (step > 0 && v <= stop) || (step < 0 && v >= stop); v += step {
			output.AddRow(types.Row{v})
		}
		return output, nil
	}

	// int, long, datetime, timespan — all int64-backed internally.
	start := types.ToInt64(startVal)
	stop := types.ToInt64(stopVal)
	stepType := inferExprType(op.Step, &emptySchema)
	step := types.ToInt64(stepVal)
	if colType == types.TypeDatetime || colType == types.TypeTimespan {
		// Normalize step to the SAME units as start/stop (nanoseconds)
		// before stepping — start/stop are already in that form for
		// both datetime and timespan columns (see toNanos' own doc
		// comment), but step's OWN inferred type determines whether it
		// needs the *100 tick->nano conversion or is already
		// nanosecond-scaled.
		step = toNanos(step, stepType)
	}
	if step == 0 {
		return nil, fmt.Errorf("range %s: step must not be zero", op.ColumnName)
	}
	for v := start; (step > 0 && v <= stop) || (step < 0 && v >= stop); v += step {
		output.AddRow(types.Row{v})
	}
	return output, nil
}

func (e *Engine) applyDataTable(op *parser.DataTableOp) (*types.Table, error) {
	numCols := len(op.Schema.Columns)
	output := types.NewTable("datatable", op.Schema)

	// Values fill rows left-to-right
	for i := 0; i < len(op.Values); i += numCols {
		row := make(types.Row, numCols)
		for j := 0; j < numCols && i+j < len(op.Values); j++ {
			val, err := convertDataTableValue(op.Values[i+j], op.Schema.Columns[j].Type)
			if err != nil {
				return nil, fmt.Errorf("datatable row %d, col %q: %w",
					i/numCols+1, op.Schema.Columns[j].Name, err)
			}
			row[j] = val
		}
		output.AddRow(row)
	}
	return output, nil
}

// convertDataTableValue converts a raw string token to a typed Go value.
//
// Delegates to types.ParseValue (the same conversion CSV ingest uses)
// after stripping datatable's own quote/escape syntax, rather than
// maintaining a second, parallel per-type conversion table. That
// second implementation had drifted from ParseValue in three ways,
// found while investigating an unrelated schema-design question and
// verified against real behavior before fixing, not assumed:
//   - TypeDatetime returned a raw time.Time instead of ParseValue's
//     UnixNano int64 — every other datetime value in this codebase
//     (CSV ingest, buildI64Array, decodeColumnVec) is an int64, so a
//     time.Time reaching types.ToInt64 hit its untyped default case
//     and silently returned 0. Confirmed live: every datatable literal
//     datetime column silently wrote 1970-01-01 (Unix epoch), with no
//     error anywhere in the pipeline — real data corruption, not a
//     display issue.
//   - TypeTimespan had no case at all, falling to the default branch,
//     which returned the raw, unparsed string instead of ParseValue's
//     correct int64 tick count.
//   - TypeInt was parsed identically to TypeLong (strconv.ParseInt
//     with a 64-bit width) rather than ParseValue's 32-bit-clamped
//     parse — a real but narrower divergence (only manifests on
//     values that overflow int32) than the two silent-corruption bugs
//     above, fixed here as a side effect of removing the duplication
//     rather than patched in isolation.
func convertDataTableValue(raw string, kt types.KQLType) (interface{}, error) {
	// Try ParseExpr + evalExpr first for a function-call-shaped literal
	// -- datetime(...), timespan(...), dynamic({...}), ago(1h), and
	// similar -- the same parse-and-evaluate-as-a-constant pattern
	// evaluate.go's own evalConstExpr already uses for plugin
	// arguments. Deliberately NOT attempted for a plain quoted string
	// token (raw starting with '"' or '\''): a quoted string in a
	// non-string column (e.g. datatable(D:datetime) ["2026-08-01"]) is
	// meant to be COERCED by types.ParseValue below, not evaluated as
	// a bare string-literal expression -- ParseExpr(`"2026-08-01"`)
	// would succeed and return the Go string "2026-08-01" unchanged,
	// silently skipping the datetime coercion entirely. Found and
	// fixed 2026-08-15 in two passes: first broadened unconditionally
	// from the original TypeDynamic-only check to fix a real, separate
	// bug (datetime(...) literals in a datatable failing outright, see
	// below) — that broadening then caused exactly the quoted-string
	// regression just described, caught by TestDatatableDatetimeNotEpochZero
	// failing immediately. This function-call-shape check is the
	// actual fix: narrow enough to leave every quoted-string and bare-
	// numeric/bool case (already handled correctly by ParseValue,
	// unchanged below) alone, while still catching the real gap this
	// was meant to fix — a TypeDatetime column given a real, standard,
	// documented KQL literal that isn't bare text at all. Confirmed
	// directly against Microsoft's own datatable-operator.md worked
	// example, which uses exactly this form
	// (datatable(Date:datetime, ...) [datetime(1910-06-11), ...]):
	// previously, any non-TypeDynamic column skipped straight to the
	// raw-string path below, handing types.ParseValue the literal,
	// still-wrapped text "datetime(2016-12-31T06:00)" -- not a valid
	// datetime string on its own -- instead of evaluating it first.
	looksLikeQuotedString := len(raw) > 0 && (raw[0] == '"' || raw[0] == '\'')
	looksLikeFuncCall := strings.Contains(raw, "(")
	if kt == types.TypeDynamic || (!looksLikeQuotedString && looksLikeFuncCall) {
		if expr, err := parser.ParseExpr(raw); err == nil {
			if val, err := evalExpr(expr, &types.Schema{}, types.Row{}); err == nil {
				return val, nil
			}
		}
	}

	// Strip surrounding quotes for strings
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		raw = raw[1 : len(raw)-1]
		// Unescape
		raw = strings.ReplaceAll(raw, `\"`, `"`)
		raw = strings.ReplaceAll(raw, `\\`, `\`)
	}
	return types.ParseValue(raw, kt)
}

// applyParse extracts fields from a string column using pattern matching.
// Supports kind=simple (default), kind=relaxed, and kind=regex.
func (e *Engine) applyParse(input *types.Table, op *parser.ParseOp) (*types.Table, error) {
	return applyParseCore(input, op.Column, op.Kind, op.Patterns, false)
}

// applyParseWhere implements | parse-where — identical pattern-matching
// to applyParse, but ALWAYS drops a row whose Column value didn't match
// (dropUnmatched=true), for every kind, matching real ADX's own
// documented behavior ("Only successfully parsed strings will be in the
// output"). This is the one real semantic difference from ParseOp,
// whose "simple"/"relaxed" kinds both currently keep the row with the
// new columns left null in this engine (see applyParseCore's own note).
func (e *Engine) applyParseWhere(input *types.Table, op *parser.ParseWhereOp) (*types.Table, error) {
	return applyParseCore(input, op.Column, op.Kind, op.Patterns, true)
}

// applyParseCore is the shared implementation behind both applyParse and
// applyParseWhere — factored out so the two operators' matching logic
// can't silently drift apart; dropUnmatched is the only behavioral knob.
func applyParseCore(input *types.Table, column, kind string, patterns []parser.ParseFragment, dropUnmatched bool) (*types.Table, error) {
	// Determine which new columns are created
	var newCols []string
	var regex *regexp.Regexp

	if kind == "regex" {
		// Single regex pattern with named groups
		if len(patterns) != 1 {
			return nil, fmt.Errorf("parse kind=regex: expected single regex pattern")
		}
		var err error
		regex, err = regexp.Compile(patterns[0].Literal)
		if err != nil {
			return nil, fmt.Errorf("parse kind=regex: %w", err)
		}
		newCols = regex.SubexpNames()[1:] // skip the whole match name ""
	} else {
		// Simple/relaxed: collect field names from pattern
		for _, frag := range patterns {
			if frag.Field != "" && frag.Field != "*" {
				newCols = append(newCols, frag.Field)
			}
		}
	}

	// Build output schema: input columns + new columns
	outCols := make([]types.Column, len(input.Schema.Columns))
	copy(outCols, input.Schema.Columns)
	for _, name := range newCols {
		outCols = append(outCols, types.Column{Name: name, Type: types.TypeString})
	}
	outSchema := types.Schema{Columns: outCols}
	output := types.NewTable(input.Name, outSchema)

	// Find source column index
	srcIdx := -1
	for i, col := range input.Schema.Columns {
		if col.Name == column {
			srcIdx = i
			break
		}
	}
	if srcIdx < 0 {
		return nil, fmt.Errorf("parse: column %q not found", column)
	}

	for _, row := range input.Rows {
		srcVal := ""
		if row[srcIdx] != nil {
			srcVal = fmt.Sprintf("%v", row[srcIdx])
		}

		var extracted []string

		if kind == "regex" {
			match := regex.FindStringSubmatch(srcVal)
			if match != nil {
				extracted = match[1:] // skip full match
			}
		} else {
			extracted = parseSimpleMatch(srcVal, patterns)
		}

		if extracted == nil {
			if !dropUnmatched && (kind == "relaxed" || kind == "simple") {
				// For relaxed: keep row with null new columns
				// For simple in KQL: technically should filter, but relaxed is more useful
				// We follow relaxed semantics for both to avoid data loss
				// (applyParseWhere always sets dropUnmatched=true, so this
				// branch is only reachable from plain ParseOp)
				outRow := make(types.Row, len(outCols))
				copy(outRow, row)
				// new columns remain nil
				output.AddRow(outRow)
			}
			continue
		}

		outRow := make(types.Row, len(outCols))
		copy(outRow, row)
		fieldIdx := 0
		for i, val := range extracted {
			if i < len(newCols) {
				outRow[len(input.Schema.Columns)+fieldIdx] = val
				fieldIdx++
			}
		}
		output.AddRow(outRow)
	}

	return output, nil
}

// parseSimpleMatch matches a string against alternating literal/field patterns.
// Returns extracted field values, or nil if no match.
func parseSimpleMatch(s string, patterns []parser.ParseFragment) []string {
	var fields []string
	pos := 0

	for i, frag := range patterns {
		if frag.Literal != "" {
			// Find literal in remaining string
			idx := strings.Index(s[pos:], frag.Literal)
			if idx < 0 {
				return nil // no match
			}
			// If there was a field capture before this literal, the captured text
			// is from pos to pos+idx
			if i > 0 && patterns[i-1].Field != "" {
				fields = append(fields, s[pos:pos+idx])
			}
			pos += idx + len(frag.Literal)
		} else if frag.Field != "" {
			// Field capture: if this is the last pattern, capture rest of string
			if i == len(patterns)-1 {
				val := s[pos:]
				if frag.Field != "*" {
					fields = append(fields, val)
				}
				pos = len(s)
			}
			// Otherwise, the next literal will determine the boundary
		}
	}

	// If last pattern was a field and we haven't captured it yet
	// (this happens when the first pattern is a field with no leading literal)
	if len(patterns) > 0 && patterns[0].Field != "" && len(fields) == 0 && len(patterns) == 1 {
		fields = append(fields, s[pos:])
	}

	return fields
}

// applyAs implements | as Name -- verified against real ADX docs
// (as-operator.md) before scoping this deliberately narrower than the
// full real-ADX behavior. Binds op.Name to the CURRENT pipeline's
// intermediate result (the table as it stands at this point in the
// pipeline, a pass-through -- as doesn't itself filter, add, or remove
// any rows or columns), registered into e.letContext.Tables exactly
// like a `let Name = (...)` tabular binding would be -- source
// resolution (executeQueryRaw's own "check let context first" step)
// doesn't distinguish how a name got into that map, so a later
// subquery within the SAME query (a join's right side, a union
// source) that references Name by name resolves to this table,
// matching real ADX's own stated purpose ("allows the query to
// reference the value of the tabular expression multiple times").
//
// Deliberately NOT supported, verified as genuinely out of scope
// rather than merely unimplemented:
//   - Cross-statement (semicolon-separated) reference: this engine's
//     CompoundStatement shape is "a list of let bindings then one
//     final statement", not a general list of arbitrary top-level
//     statements, so there's no LATER top-level statement for a name
//     bound mid-pipeline to ever be visible to in the first place.
//   - Real ADX's own withsource=/source_/$table column-naming
//     integration for union/find/search -- a separate, additive piece
//     of real ADX's own `as` behavior that would need wiring through
//     each of those three operators' own already-built machinery.
//
// e.letContext is created lazily here if nil (the common case: a
// one-shot query using `as` with no `let` statements at all) --
// executeQuery's own save/restore wrapper (engine.go) exists
// specifically so this lazy creation can't leak into a later,
// unrelated query run against the same long-lived Engine instance.
func (e *Engine) applyAs(input *types.Table, op *parser.AsOp) (*types.Table, error) {
	if e.letContext == nil {
		e.setLetContext(&LetContext{
			Scalars:   make(map[string]types.Value),
			Functions: make(map[string]*parser.FunctionDef),
			Tables:    make(map[string]*types.Table),
		})
	}
	e.letContext.Tables[op.Name] = input
	return input, nil
}

// applyGetSchema returns a table describing the schema of the input.
func (e *Engine) applyGetSchema(input *types.Table) (*types.Table, error) {
	outSchema := types.Schema{
		Columns: []types.Column{
			{Name: "ColumnName", Type: types.TypeString},
			{Name: "ColumnOrdinal", Type: types.TypeLong},
			{Name: "DataType", Type: types.TypeString},
			{Name: "ColumnType", Type: types.TypeString},
		},
	}
	output := types.NewTable(input.Name, outSchema)

	// _TimeReceived (see timereceived.go) is unconditionally excluded
	// here, not filtered afterward by executeQuery's own
	// hideTimeReceivedUnlessExplicit wrapper -- that wrapper strips a
	// matching COLUMN from a result, but getschema's own output has no
	// column literally named _TimeReceived at all; the name instead
	// appears as a ColumnName VALUE within one of getschema's own rows.
	// Real Log Analytics/ADX's own documented rule ("won't show in the
	// schema view") applies unconditionally here too -- getschema has
	// no operand to "explicitly reference" a specific column's schema
	// row with in the first place, unlike where/project/summarize.
	//
	// Known, stated limitation, not silently glossed over: this hides
	// _TimeReceived from getschema even in the unusual case where an
	// EARLIER stage already explicitly projected it in
	// (T | project _TimeReceived | getschema would still hide it here,
	// even though the actual row DATA from that same pipeline would
	// correctly show it, per hideTimeReceivedUnlessExplicit). Distinguishing
	// "this is the source table's own default _TimeReceived" from
	// "this is a deliberately carried-through _TimeReceived from an
	// explicit project earlier in the pipeline" would need input's
	// schema to carry that provenance, which it doesn't today.
	// Deferred -- functionality first, this exact edge case is rare in
	// practice.
	ordinal := int64(0)
	for _, col := range input.Schema.Columns {
		if col.Name == timeReceivedColumnName {
			continue
		}
		output.AddRow(types.Row{col.Name, ordinal, col.Type.DotNetTypeName(), col.Type.String()})
		ordinal++
	}

	return output, nil
}
