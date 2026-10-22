package parser

import (
	"fmt"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// parseScan parses "[with_match_id=Name] [declare (ColumnDeclarations)]
// with (step StepName [output=all|last|none] : Condition [=> Column =
// Assignment, ...]; ...)".
func parseScan(s string) (Operator, error) {
	s = strings.TrimSpace(s)
	lower := keywordFold(s)
	op := &ScanOp{}

	if strings.HasPrefix(lower, "with_match_id") {
		rest := strings.TrimSpace(s[len("with_match_id"):])
		if !strings.HasPrefix(rest, "=") {
			return nil, fmt.Errorf("scan: expected with_match_id=Name")
		}
		rest = strings.TrimSpace(rest[1:])
		end := 0
		for end < len(rest) && isIdentChar(rest[end]) {
			end++
		}
		if end == 0 {
			return nil, fmt.Errorf("scan: expected a column name after with_match_id=")
		}
		op.MatchIDColumn = rest[:end]
		s = strings.TrimSpace(rest[end:])
		lower = keywordFold(s)
	}

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
		lower = keywordFold(s)
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

	seen := map[string]bool{}
	for _, part := range splitRespectingParens(body, ';') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		step, err := parseScanStep(part, op.Declares)
		if err != nil {
			return nil, err
		}
		if seen[step.Name] {
			return nil, fmt.Errorf("scan: duplicate step name %q", step.Name)
		}
		seen[step.Name] = true
		op.Steps = append(op.Steps, *step)
	}
	if len(op.Steps) == 0 {
		return nil, fmt.Errorf("scan: expected at least one step")
	}
	return op, nil
}

// parseScanStep parses "step Name [output=all|last|none] : Condition
// [=> Column = Assignment, ...]".
func parseScanStep(stepText string, declares []ScanDeclare) (*ScanStep, error) {
	if !strings.HasPrefix(strings.ToLower(stepText), "step ") {
		return nil, fmt.Errorf("scan: expected 'step StepName : Condition [=> ...]', got %q", stepText)
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
	step := &ScanStep{Name: headFields[0], Output: "all"}
	if !isValidIdentifier(step.Name) {
		return nil, fmt.Errorf("scan: invalid step name %q", step.Name)
	}
	if rest := strings.TrimSpace(head[len(headFields[0]):]); rest != "" {
		if !strings.HasPrefix(strings.ToLower(rest), "output") {
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
		step.Output = val
	}

	condText, assignText := tail, ""
	if arrowIdx := findTopLevelArrow(tail); arrowIdx >= 0 {
		condText = strings.TrimSpace(tail[:arrowIdx])
		assignText = strings.TrimSpace(tail[arrowIdx+2:])
	}
	condExpr, err := ParseExpr(condText)
	if err != nil {
		return nil, fmt.Errorf("scan step %s: condition: %w", step.Name, err)
	}
	step.Condition = condExpr

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
		declared := false
		for _, d := range declares {
			if d.Name == col {
				declared = true
			}
		}
		if !declared {
			return nil, fmt.Errorf("scan step %s: assignment to %q, which is not in declare(...)", step.Name, col)
		}
		aExpr, err := ParseExpr(strings.TrimSpace(part[eqIdx+1:]))
		if err != nil {
			return nil, fmt.Errorf("scan step %s: assignment %q: %w", step.Name, col, err)
		}
		step.Assignments = append(step.Assignments, ScanAssignment{Column: col, Expr: aExpr})
	}
	return step, nil
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
