package engine

// string_ops.go — the KQL string-operator family (contains, has,
// startswith, endswith, hasprefix, hassuffix, like, matches regex,
// =~, !~ and their negated and _cs forms), shared by the row engine
// (evalBinaryOp) and the columnar path (columnar_agg.go).
//
// stringOpMatch is the reference definition. compileStringMatcher
// returns the same predicate with the literal's per-call work (case
// folding, regex compilation) done once; string_ops_test.go checks the
// two agree on every operator.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// fmtV is fmt.Sprintf("%v", v) without the allocation and formatting
// pass when v is already a string.
func fmtV(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// isStringOpFamily reports whether op is evaluated by stringOpMatch.
// It is isStringOperator plus =~ and !~, which share the evaluation
// but not the null-as-empty-string rule.
func isStringOpFamily(op parser.BinaryOp) bool {
	return isStringOperator(op) || op == parser.OpCIEQ || op == parser.OpCINEQ
}

// stringOpMatch evaluates a string-family operator on two strings.
func stringOpMatch(op parser.BinaryOp, ls, rs string) (bool, error) {
	switch op {
	case parser.OpContains:
		return strings.Contains(strings.ToLower(ls), strings.ToLower(rs)), nil
	case parser.OpNotContains:
		return !strings.Contains(strings.ToLower(ls), strings.ToLower(rs)), nil
	case parser.OpContainsCS:
		return strings.Contains(ls, rs), nil
	case parser.OpNotContainsCS:
		return !strings.Contains(ls, rs), nil
	case parser.OpHas:
		return hasTerm(ls, rs, false), nil
	case parser.OpNotHas:
		return !hasTerm(ls, rs, false), nil
	case parser.OpHasCS:
		return hasTerm(ls, rs, true), nil
	case parser.OpNotHasCS:
		return !hasTerm(ls, rs, true), nil
	case parser.OpStartsWith:
		return strings.HasPrefix(strings.ToLower(ls), strings.ToLower(rs)), nil
	case parser.OpNotStartsWith:
		return !strings.HasPrefix(strings.ToLower(ls), strings.ToLower(rs)), nil
	case parser.OpStartsWithCS:
		return strings.HasPrefix(ls, rs), nil
	case parser.OpNotStartsWithCS:
		return !strings.HasPrefix(ls, rs), nil
	case parser.OpEndsWith:
		return strings.HasSuffix(strings.ToLower(ls), strings.ToLower(rs)), nil
	case parser.OpNotEndsWith:
		return !strings.HasSuffix(strings.ToLower(ls), strings.ToLower(rs)), nil
	case parser.OpEndsWithCS:
		return strings.HasSuffix(ls, rs), nil
	case parser.OpNotEndsWithCS:
		return !strings.HasSuffix(ls, rs), nil
	case parser.OpMatchesRegex:
		re, err := regexp.Compile(rs)
		if err != nil {
			return false, fmt.Errorf("matches regex: invalid pattern %q: %w", rs, err)
		}
		return re.MatchString(ls), nil
	case parser.OpCIEQ:
		return strings.EqualFold(ls, rs), nil
	case parser.OpCINEQ:
		return !strings.EqualFold(ls, rs), nil
	case parser.OpLike:
		return matchLike(ls, rs, false), nil
	case parser.OpNotLike:
		return !matchLike(ls, rs, false), nil
	case parser.OpHasPrefix:
		return hasTermPrefix(ls, rs, false), nil
	case parser.OpNotHasPrefix:
		return !hasTermPrefix(ls, rs, false), nil
	case parser.OpHasPrefixCS:
		return hasTermPrefix(ls, rs, true), nil
	case parser.OpNotHasPrefixCS:
		return !hasTermPrefix(ls, rs, true), nil
	case parser.OpHasSuffix:
		return hasTermSuffix(ls, rs, false), nil
	case parser.OpNotHasSuffix:
		return !hasTermSuffix(ls, rs, false), nil
	case parser.OpHasSuffixCS:
		return hasTermSuffix(ls, rs, true), nil
	case parser.OpNotHasSuffixCS:
		return !hasTermSuffix(ls, rs, true), nil
	}
	return false, fmt.Errorf("not a string operator: %v", op)
}

// compileStringMatcher binds a string-family operator to its right-hand
// literal. An invalid regex is returned as an error here rather than at
// the first row; callers that must preserve the row engine's
// error-on-first-row behaviour fall back to it on error.
func compileStringMatcher(op parser.BinaryOp, rs string) (func(string) bool, error) {
	lrs := strings.ToLower(rs)
	switch op {
	case parser.OpContains:
		return func(s string) bool { return strings.Contains(strings.ToLower(s), lrs) }, nil
	case parser.OpNotContains:
		return func(s string) bool { return !strings.Contains(strings.ToLower(s), lrs) }, nil
	case parser.OpStartsWith:
		return func(s string) bool { return strings.HasPrefix(strings.ToLower(s), lrs) }, nil
	case parser.OpNotStartsWith:
		return func(s string) bool { return !strings.HasPrefix(strings.ToLower(s), lrs) }, nil
	case parser.OpEndsWith:
		return func(s string) bool { return strings.HasSuffix(strings.ToLower(s), lrs) }, nil
	case parser.OpNotEndsWith:
		return func(s string) bool { return !strings.HasSuffix(strings.ToLower(s), lrs) }, nil
	case parser.OpMatchesRegex:
		re, err := regexp.Compile(rs)
		if err != nil {
			return nil, fmt.Errorf("matches regex: invalid pattern %q: %w", rs, err)
		}
		return re.MatchString, nil
	}
	if !isStringOpFamily(op) {
		return nil, fmt.Errorf("not a string operator: %v", op)
	}
	// Remaining operators have no per-literal work worth hoisting.
	return func(s string) bool {
		ok, _ := stringOpMatch(op, s, rs)
		return ok
	}, nil
}
