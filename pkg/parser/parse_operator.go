package parser

import (
	"fmt"
	"strings"
)

// parseParseOperator parses "parse [kind=simple|regex|relaxed] Column with Pattern..."
func parseParseOperator(s string) (Operator, error) {
	column, kind, fragments, err := parseParsePatternClause("parse", s)
	if err != nil {
		return nil, err
	}
	return &ParseOp{
		Column:   column,
		Kind:     kind,
		Patterns: fragments,
	}, nil
}

// parseParseWhereOperator parses "parse-where [kind=simple|regex|relaxed] Column with Pattern..."
// Same clause grammar as parse -- see parseParsePatternClause.
func parseParseWhereOperator(s string) (Operator, error) {
	column, kind, fragments, err := parseParsePatternClause("parse-where", s)
	if err != nil {
		return nil, err
	}
	return &ParseWhereOp{
		Column:   column,
		Kind:     kind,
		Patterns: fragments,
	}, nil
}

// parseParsePatternClause parses the shared "[kind=...] Column with Pattern..."
// clause used by both parse and parse-where -- factored out so the two
// operators can't silently drift apart on pattern-parsing behavior.
// opName is used only for error messages ("parse" or "parse-where").
func parseParsePatternClause(opName, s string) (column, kind string, fragments []ParseFragment, err error) {
	s = strings.TrimSpace(s)
	kind = "simple"

	// Check for kind=...
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "kind=") {
		eqEnd := strings.IndexByte(s[5:], ' ')
		if eqEnd < 0 {
			return "", "", nil, fmt.Errorf("%s: expected column name after kind=...", opName)
		}
		kind = strings.ToLower(s[5 : 5+eqEnd])
		s = strings.TrimSpace(s[5+eqEnd:])
	}

	// Find "with" keyword (word boundary)
	withIdx := -1
	lowerS := strings.ToLower(s)
	idx := strings.Index(lowerS, " with ")
	if idx >= 0 {
		withIdx = idx
	} else if strings.HasSuffix(lowerS, " with") {
		withIdx = len(s) - 5
	}
	if withIdx < 0 {
		return "", "", nil, fmt.Errorf("%s: expected 'with' keyword", opName)
	}

	column = strings.TrimSpace(s[:withIdx])
	patternStr := strings.TrimSpace(s[withIdx+5:])
	rem := patternStr
	for len(rem) > 0 {
		rem = strings.TrimLeft(rem, " \t")
		if len(rem) == 0 {
			break
		}

		if rem[0] == '"' {
			// Double-quoted string literal
			end := strings.IndexByte(rem[1:], '"')
			if end < 0 {
				return "", "", nil, fmt.Errorf("%s: unterminated string in pattern", opName)
			}
			lit := rem[1 : 1+end]
			fragments = append(fragments, ParseFragment{Literal: lit})
			rem = rem[2+end:]
		} else if rem[0] == '\'' {
			// Single-quoted string literal
			end := strings.IndexByte(rem[1:], '\'')
			if end < 0 {
				return "", "", nil, fmt.Errorf("%s: unterminated string in pattern", opName)
			}
			lit := rem[1 : 1+end]
			fragments = append(fragments, ParseFragment{Literal: lit})
			rem = rem[2+end:]
		} else if rem[0] == '*' {
			// Wildcard (skip)
			fragments = append(fragments, ParseFragment{Field: "*"})
			rem = rem[1:]
		} else {
			// Field name — read until space, quote, or end
			end := 0
			for end < len(rem) && rem[end] != ' ' && rem[end] != '\t' && rem[end] != '"' && rem[end] != '\'' && rem[end] != '*' {
				end++
			}
			if end == 0 {
				return "", "", nil, fmt.Errorf("%s: unexpected character in pattern: %c", opName, rem[0])
			}
			field := rem[:end]
			rem = rem[end:]
			// Skip optional : Type annotation
			rem = strings.TrimLeft(rem, " \t")
			if len(rem) > 0 && rem[0] == ':' {
				rem = rem[1:]
				rem = strings.TrimLeft(rem, " \t")
				te := 0
				for te < len(rem) && rem[te] != ' ' && rem[te] != '\t' && rem[te] != '"' && rem[te] != '\'' {
					te++
				}
				rem = rem[te:]
			}
			fragments = append(fragments, ParseFragment{Field: field})
		}
	}

	return column, kind, fragments, nil
}
