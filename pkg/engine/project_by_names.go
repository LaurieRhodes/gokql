package engine

import (
	"encoding/json"
	"fmt"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// applyProjectByNames implements | project-by-names — see
// ProjectByNamesOp's own doc comment (pkg/parser/ast.go) for exactly
// which ColumnSpecifier shapes are recognized. Verified against every
// worked example in real ADX's own docs (project-by-names-operator.md):
// exact names, a wildcard pattern, a dynamic array literal, and
// column_names_of(Source) combined with a dynamic-array parameter
// reference in a lookup+invoke pattern.
func (e *Engine) applyProjectByNames(input *types.Table, op *parser.ProjectByNamesOp) (*types.Table, error) {
	emptySchema := types.Schema{}
	emptyRow := types.Row{}

	// Resolve every specifier, in order, into a flat list of column
	// names/wildcard patterns — "Columns in the result are ordered
	// based on the sequence in which they're specified or matched."
	var patterns []string
	for _, spec := range op.Specifiers {
		if spec.ColumnNamesOfTable != "" {
			// LookupTable walks the parent chain, not just
			// e.letContext's own local map.
			tbl, ok := e.letContext.LookupTable(spec.ColumnNamesOfTable)
			if !ok {
				return nil, fmt.Errorf("project-by-names: column_names_of(%s): no such table binding", spec.ColumnNamesOfTable)
			}
			for _, col := range tbl.Schema.Columns {
				patterns = append(patterns, col.Name)
			}
			continue
		}

		val, err := evalExpr(spec.Expr, &emptySchema, emptyRow)
		if err != nil {
			return nil, fmt.Errorf("project-by-names: %w", err)
		}
		if val == nil {
			continue
		}
		names, err := projectByNamesSpecToPatterns(val)
		if err != nil {
			return nil, fmt.Errorf("project-by-names: %w", err)
		}
		patterns = append(patterns, names...)
	}

	// Expand patterns against the input schema, in pattern order, each
	// wildcard matching input columns in their OWN original order —
	// this is the one real, defining difference from project-keep
	// (which preserves input order regardless of pattern order; see
	// ProjectByNamesOp's own doc comment). Deduplicated: a column
	// matched by an earlier pattern is not repeated for a later one
	// that also matches it (real ADX's own docs don't state this
	// explicitly, but every output column name must be unique, the
	// same constraint this engine already enforces elsewhere).
	seen := make(map[string]bool, len(input.Schema.Columns))
	var newCols []types.Column
	var colMap []int
	for _, pattern := range patterns {
		for i, col := range input.Schema.Columns {
			if seen[col.Name] {
				continue
			}
			if matchesAnyPattern(col.Name, []string{pattern}) {
				seen[col.Name] = true
				newCols = append(newCols, col)
				colMap = append(colMap, i)
			}
		}
	}

	if len(newCols) == 0 {
		return types.NewTable(input.Name, types.Schema{}), nil
	}

	output := types.NewTable(input.Name, types.Schema{Columns: newCols})
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

// projectByNamesSpecToPatterns converts one evaluated ColumnSpecifier
// value into a flat list of column names/wildcard patterns — a plain
// string (an exact name or a wildcard pattern like "C*"), or a
// dynamic (JSON array) value, e.g. from dynamic(["Name","Country"])
// or a let-bound/parameter-bound dynamic array reference.
func projectByNamesSpecToPatterns(val types.Value) ([]string, error) {
	if s, ok := val.(string); ok {
		// Could be a plain name/pattern, OR a dynamic value stored as
		// its JSON string form (this engine's own dynamic-column
		// convention) — try JSON array parsing first, fall back to
		// treating it as a literal name/pattern on any failure.
		var arr []string
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			return arr, nil
		}
		return []string{s}, nil
	}
	return nil, fmt.Errorf("column specifier must be a string or a dynamic array of strings, got %T", val)
}

