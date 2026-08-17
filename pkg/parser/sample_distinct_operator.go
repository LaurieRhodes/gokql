package parser

import (
	"fmt"
	"strings"
)

// parseSampleDistinct parses "NumberOfValues of ColumnName" — see
// SampleDistinctOp's own doc comment for the exact semantics
// implemented.
func parseSampleDistinct(s string) (Operator, error) {
	s = strings.TrimSpace(s)
	ofIdx := findKeyword(s, " of ")
	if ofIdx < 0 {
		return nil, fmt.Errorf("sample-distinct: expected 'NumberOfValues of ColumnName'")
	}
	countText := strings.TrimSpace(s[:ofIdx])
	column := strings.TrimSpace(s[ofIdx+len(" of "):])
	if column == "" || !isValidIdentifier(column) {
		return nil, fmt.Errorf("sample-distinct: expected a column name after 'of', got %q", column)
	}
	countExpr, err := ParseExpr(countText)
	if err != nil {
		return nil, fmt.Errorf("sample-distinct: %w", err)
	}
	return &SampleDistinctOp{Count: countExpr, Column: column}, nil
}

