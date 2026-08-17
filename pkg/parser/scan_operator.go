package parser

import (
	"fmt"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// parseScan parses "[declare (ColumnDeclarations)] with (step StepName
// [output=all|last|none] : Condition => Column = Assignment[, ...] ;)".
// See ScanOp's own doc comment for exactly what this covers (a single
// step; with_match_id and multi-step sequences are rejected with a
// clear error).
func parseScan(s string) (Operator, error) {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)

	if strings.HasPrefix(lower, "with_match_id") {
		return nil, fmt.Errorf("scan: with_match_id is not implemented")
	}

	op := &ScanOp{Output: "all"}

	if strings.HasPrefix(lower, "declare") {
		rest := strings.TrimSpace(s[len("declare"):])
		if !strings.HasPrefix(rest, "(") {
			return nil, fmt.Errorf("scan: expected '(' after declare")
		}
		close := findMatchingParen(rest, 0)
		if close < 0 {
			return nil, fmt.Errorf("scan: unterminated declare(...)")
		}
		decls, err := parseScanDeclares(rest[1:close])
		if err != nil {
			return nil, err
		}
		op.Declares = decls
		s = strings.TrimSpace(rest[close+1:])
		lower = strings.ToLower(s)
	}
	if len(op.Declares) == 0 {
		return nil, fmt.Errorf("scan: declare(...) is required — at least one declared column")
	}

	if !strings.HasPrefix(lower, "with") {
		return nil, fmt.Errorf("scan: expected 'with (...)' step definitions")
	}
	s = strings.TrimSpace(s[len("with"):])
	if !strings.HasPrefix(s, "(") {
		return nil, fmt.Errorf("scan: expected '(' after 'with'")
	}
	close := findMatchingParen(s, 0)
	if close < 0 {
		return nil, fmt.Errorf("scan: unterminated with(...)")
	}
	if close != len(s)-1 {
		return nil, fmt.Errorf("scan: unexpected trailing content after with(...)")
	}
	body := strings.TrimSpace(s[1:close])

	var steps []string
	for _, part := range splitRespectingParens(body, ';') {
		part = strings.TrimSpace(part)
		if part != "" {
			steps = append(steps, part)
		}
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("scan: expected at least one step")
	}
	if len(steps) > 1 {
		return nil, fmt.Errorf("scan: multi-step scan (%d steps) is not implemented — only a single step is supported", len(steps))
	}

	stepText := steps[0]
	stepLower := strings.ToLower(stepText)
	if !strings.HasPrefix(stepLower, "step ") {
		return nil, fmt.Errorf("scan: expected 'step StepName : Condition => ...'")
	}
	stepText = strings.TrimSpace(stepText[len("step "):])

	colonIdx := findTopLevelByte(stepText, ':')
	if colonIdx < 0 {
		return nil, fmt.Errorf("scan: expected ':' after step name")
	}
	head := strings.TrimSpace(stepText[:colonIdx])
	tail := strings.TrimSpace(stepText[colonIdx+1:])

	headFields := strings.Fields(head)
	if len(headFields) == 0 {
		return nil, fmt.Errorf("scan: expected a step name")
	}
	op.StepName = headFields[0]
	if !isValidIdentifier(op.StepName) {
		return nil, fmt.Errorf("scan: invalid step name %q", op.StepName)
	}
	if rest := strings.TrimSpace(head[len(headFields[0]):]); rest != "" {
		restLower := strings.ToLower(rest)
		if !strings.HasPrefix(restLower, "output") {
			return nil, fmt.Errorf("scan: unexpected token %q after step name", rest)
		}
		eqIdx := strings.Index(rest, "=")
		if eqIdx < 0 {
			return nil, fmt.Errorf("scan: expected 'output=all|last|none'")
		}
		val := strings.ToLower(strings.TrimSpace(rest[eqIdx+1:]))
		if val != "all" && val != "last" && val != "none" {
			return nil, fmt.Errorf("scan: output must be 'all', 'last', or 'none', got %q", val)
		}
		op.Output = val
	}

	arrowIdx := findTopLevelArrow(tail)
	if arrowIdx < 0 {
		return nil, fmt.Errorf("scan: expected '=>' after the step condition")
	}
	condText := strings.TrimSpace(tail[:arrowIdx])
	assignText := strings.TrimSpace(tail[arrowIdx+2:])
	assignText = strings.TrimSuffix(strings.TrimSpace(assignText), ";")

	condExpr, err := ParseExpr(condText)
	if err != nil {
		return nil, fmt.Errorf("scan: condition: %w", err)
	}
	op.Condition = condExpr

	for _, part := range splitRespectingParens(assignText, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eqIdx := assignmentEqIndex(part)
		if eqIdx < 0 {
			return nil, fmt.Errorf("scan: expected Column = Assignment, got %q", part)
		}
		col := strings.TrimSpace(part[:eqIdx])
		if !isValidIdentifier(col) {
			return nil, fmt.Errorf("scan: invalid assignment column name %q", col)
		}
		exprText := strings.TrimSpace(part[eqIdx+1:])
		aExpr, err := ParseExpr(exprText)
		if err != nil {
			return nil, fmt.Errorf("scan: assignment %q: %w", col, err)
		}
		op.Assignments = append(op.Assignments, ScanAssignment{Column: col, Expr: aExpr})
	}
	if len(op.Assignments) == 0 {
		return nil, fmt.Errorf("scan: step %q has no assignments", op.StepName)
	}

	return op, nil
}

// parseScanDeclares parses a declare(...) column list:
// Name:Type[=DefaultExpr][, ...].
func parseScanDeclares(s string) ([]ScanDeclare, error) {
	var decls []ScanDeclare
	for _, part := range splitRespectingParens(s, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		colonIdx := strings.Index(part, ":")
		if colonIdx < 0 {
			return nil, fmt.Errorf("scan declare: expected Name:Type, got %q", part)
		}
		name := strings.TrimSpace(part[:colonIdx])
		if !isValidIdentifier(name) {
			return nil, fmt.Errorf("scan declare: invalid column name %q", name)
		}
		rest := strings.TrimSpace(part[colonIdx+1:])

		typeText := rest
		var defaultExpr Expr
		if eqIdx := strings.Index(rest, "="); eqIdx >= 0 {
			typeText = strings.TrimSpace(rest[:eqIdx])
			defText := strings.TrimSpace(rest[eqIdx+1:])
			expr, err := ParseExpr(defText)
			if err != nil {
				return nil, fmt.Errorf("scan declare %q: default value: %w", name, err)
			}
			defaultExpr = expr
		}
		if _, err := types.ParseType(typeText); err != nil {
			return nil, fmt.Errorf("scan declare %q: %w", name, err)
		}
		decls = append(decls, ScanDeclare{Name: name, Type: typeText, Default: defaultExpr})
	}
	return decls, nil
}

// findTopLevelByte finds the first occurrence of ch not inside
// parentheses or quotes.
func findTopLevelByte(s string, ch byte) int {
	depth := 0
	var inQuote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote != 0 {
			if c == inQuote && !precededByOddBackslashes(s, i) {
				inQuote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			inQuote = c
			continue
		}
		if c == '(' {
			depth++
		} else if c == ')' {
			if depth > 0 {
				depth--
			}
		} else if c == ch && depth == 0 {
			return i
		}
	}
	return -1
}

// findTopLevelArrow finds the first "=>" not inside parentheses or quotes.
func findTopLevelArrow(s string) int {
	depth := 0
	var inQuote byte
	for i := 0; i < len(s)-1; i++ {
		c := s[i]
		if inQuote != 0 {
			if c == inQuote && !precededByOddBackslashes(s, i) {
				inQuote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			inQuote = c
			continue
		}
		if c == '(' {
			depth++
		} else if c == ')' {
			if depth > 0 {
				depth--
			}
		} else if depth == 0 && c == '=' && s[i+1] == '>' {
			return i
		}
	}
	return -1
}

