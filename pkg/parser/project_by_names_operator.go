package parser

import (
	"fmt"
	"strings"
)

// parseProjectByNames parses a comma-separated ColumnSpecifier list —
// see ProjectByNamesOp's own doc comment for exactly which specifier
// shapes are recognized.
func parseProjectByNames(s string) (Operator, error) {
	var specs []ProjectByNamesSpecifier
	for _, item := range splitRespectingParens(s, ',') {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		// column_names_of(TableRef) — a small, special-cased
		// recognized form, checked before the general expression
		// path since "column_names_of" isn't a registered scalar
		// function elsewhere in this engine (it takes a TABLE
		// reference, not a scalar/column argument, which doesn't fit
		// the general expression grammar at all).
		lowerItem := strings.ToLower(item)
		if strings.HasPrefix(lowerItem, "column_names_of(") && strings.HasSuffix(item, ")") {
			tableRef := strings.TrimSpace(item[len("column_names_of(") : len(item)-1])
			if tableRef == "" || !isValidIdentifier(tableRef) {
				return nil, fmt.Errorf("project-by-names: column_names_of(...) expects a bare table reference, got %q", tableRef)
			}
			specs = append(specs, ProjectByNamesSpecifier{ColumnNamesOfTable: tableRef})
			continue
		}

		expr, err := ParseExpr(item)
		if err != nil {
			return nil, fmt.Errorf("project-by-names: %w", err)
		}
		specs = append(specs, ProjectByNamesSpecifier{Expr: expr})
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("project-by-names: no column specifiers given")
	}
	return &ProjectByNamesOp{Specifiers: specs}, nil
}

