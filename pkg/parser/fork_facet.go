package parser

// fork_facet.go — the fork and facet operators, which return several
// result tables:
//
//	T | fork [Name =] (subquery) [Name =] (subquery) ...
//	T | facet by Column [, Column ...] [with (subquery)]

import (
	"fmt"
	"strings"
)

// ForkBranch is one fork leg; Name is "" unless given with Name =.
type ForkBranch struct {
	Name      string
	Operators []Operator
}

type ForkOp struct {
	Branches []ForkBranch
}

func (*ForkOp) operatorNode() {}

type FacetOp struct {
	Columns []string
	With    []Operator // nil when there is no with clause
}

func (*FacetOp) operatorNode() {}

// parseSubPipeline parses a sourceless operator pipeline.
func parseSubPipeline(what, text string) ([]Operator, error) {
	var ops []Operator
	for _, seg := range splitPipe(text) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		op, err := parseOperator(seg)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		ops = append(ops, op)
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("%s: empty subquery", what)
	}
	return ops, nil
}

func parseFork(rest string) (*ForkOp, error) {
	op := &ForkOp{}
	s := strings.TrimSpace(rest)
	for s != "" {
		name := ""
		if s[0] != '(' {
			eq := strings.Index(s, "=")
			paren := strings.Index(s, "(")
			if eq < 0 || paren < 0 || eq > paren {
				return nil, fmt.Errorf("fork: expected [Name =] (subquery), got %q", s)
			}
			name = unquoteIdent(s[:eq])
			if !isValidIdentifier(name) && !strings.HasPrefix(strings.TrimSpace(s[:eq]), "[") {
				return nil, fmt.Errorf("fork: invalid result name %q", strings.TrimSpace(s[:eq]))
			}
			s = strings.TrimSpace(s[eq+1:])
			if s == "" || s[0] != '(' {
				return nil, fmt.Errorf("fork: expected (subquery) after %s =", name)
			}
		}
		end := findMatchingParenAnyQuote(s, 0)
		if end < 0 {
			return nil, fmt.Errorf("fork: unmatched parenthesis")
		}
		ops, err := parseSubPipeline("fork", s[1:end])
		if err != nil {
			return nil, err
		}
		op.Branches = append(op.Branches, ForkBranch{Name: name, Operators: ops})
		s = strings.TrimSpace(s[end+1:])
	}
	if len(op.Branches) == 0 {
		return nil, fmt.Errorf("fork: no subqueries")
	}
	return op, nil
}

func parseFacet(rest string) (*FacetOp, error) {
	s := strings.TrimSpace(rest)
	if !strings.HasPrefix(strings.ToLower(s), "by ") {
		return nil, fmt.Errorf("facet: expected 'by Column [, ...] [with (subquery)]'")
	}
	s = strings.TrimSpace(s[3:])
	op := &FacetOp{}
	cols := s
	// "with (" at the top level starts the with clause
	lower := strings.ToLower(s)
	for i := 0; i+4 <= len(lower); i++ {
		if lower[i:i+4] != "with" || (i > 0 && isIdentChar(lower[i-1])) {
			continue
		}
		after := strings.TrimSpace(s[i+4:])
		if !strings.HasPrefix(after, "(") {
			continue
		}
		end := findMatchingParenAnyQuote(after, 0)
		if end < 0 {
			return nil, fmt.Errorf("facet: unmatched parenthesis in with clause")
		}
		if trailing := strings.TrimSpace(after[end+1:]); trailing != "" {
			return nil, fmt.Errorf("facet: unexpected %q after with clause", trailing)
		}
		ops, err := parseSubPipeline("facet with", after[1:end])
		if err != nil {
			return nil, err
		}
		op.With = ops
		cols = s[:i]
		break
	}
	for _, c := range splitAndTrim(cols, ',') {
		if c = unquoteIdent(c); c != "" {
			op.Columns = append(op.Columns, c)
		}
	}
	if len(op.Columns) == 0 {
		return nil, fmt.Errorf("facet: no columns")
	}
	return op, nil
}

// keywordAt reports whether s starts with keyword kw followed by
// whitespace or "(".
func keywordAt(s, kw string) bool {
	if !strings.HasPrefix(s, kw) || len(s) == len(kw) {
		return false
	}
	switch s[len(kw)] {
	case ' ', '\t', '\n', '\r', '(':
		return true
	}
	return false
}
