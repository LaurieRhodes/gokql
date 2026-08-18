package parser

import (
	"fmt"
	"strings"
)

// parseParseOperator parses "parse [kind=simple|regex|relaxed] [flags=regexFlags] Column with Pattern..."
func parseParseOperator(s string) (Operator, error) {
	column, kind, flags, fragments, err := parseParsePatternClause("parse", s)
	if err != nil {
		return nil, err
	}
	return &ParseOp{
		Column:   column,
		Kind:     kind,
		Flags:    flags,
		Patterns: fragments,
	}, nil
}

// parseParseWhereOperator parses "parse-where [kind=simple|regex|relaxed] [flags=regexFlags] Column with Pattern..."
// Same clause grammar as parse -- see parseParsePatternClause.
func parseParseWhereOperator(s string) (Operator, error) {
	column, kind, flags, fragments, err := parseParsePatternClause("parse-where", s)
	if err != nil {
		return nil, err
	}
	return &ParseWhereOp{
		Column:   column,
		Kind:     kind,
		Flags:    flags,
		Patterns: fragments,
	}, nil
}

// parseParsePatternClause parses the shared "[kind=...] [flags=...] Column
// with Pattern..." clause used by both parse and parse-where -- factored
// out so the two operators can't silently drift apart on pattern-parsing
// behavior. opName is used only for error messages ("parse" or "parse-where").
//
// flags=regexFlags (real KQL syntax, kind=regex only: U for ungreedy, m for
// multi-line, s for match-newline, i for case-insensitive -- see
// learn.microsoft.com/en-us/kusto/query/parse-operator's own "regexFlags"
// parameter) added 2026-08-18 alongside rewriting kind=regex's whole
// matching model; verified directly against real ADX's own "Regex mode
// with regex flags" worked examples (flags=Ui, flags=s).
func parseParsePatternClause(opName, s string) (column, kind, flags string, fragments []ParseFragment, err error) {
	s = strings.TrimSpace(s)
	kind = "simple"

	// Check for kind=...
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "kind=") {
		eqEnd := strings.IndexByte(s[5:], ' ')
		if eqEnd < 0 {
			return "", "", "", nil, fmt.Errorf("%s: expected column name after kind=...", opName)
		}
		kind = strings.ToLower(s[5 : 5+eqEnd])
		s = strings.TrimSpace(s[5+eqEnd:])
	}

	// Check for flags=... (kind=regex only, but accepted syntactically
	// regardless -- matching real KQL, which defines the parameter as
	// simply ignored/inapplicable for other kinds rather than an error).
	lower = strings.ToLower(s)
	if strings.HasPrefix(lower, "flags=") {
		eqEnd := strings.IndexByte(s[6:], ' ')
		if eqEnd < 0 {
			return "", "", "", nil, fmt.Errorf("%s: expected column name after flags=...", opName)
		}
		flags = s[6 : 6+eqEnd]
		s = strings.TrimSpace(s[6+eqEnd:])
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
		return "", "", "", nil, fmt.Errorf("%s: expected 'with' keyword", opName)
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
			// Double-quoted string literal — added proper escape
			// processing 2026-08-18 (real, pre-existing bug found and
			// fixed): this previously took the raw text between the
			// first two quote characters completely verbatim, with NO
			// backslash-escape processing at all — unlike the primary
			// expression parser's own parseString, which correctly
			// handles \\, \", \', \n, \t. Reproduced live: real ADX's
			// own "Regex mode with regex flags" worked example ends a
			// pattern fragment with "\\)" (KQL source for a literal
			// backslash followed by a close-paren, i.e. a properly
			// regex-escaped ")"); this scanner previously passed the
			// two raw backslash characters straight through
			// unescaped, corrupting the generated kind=regex pattern
			// ("unexpected )" compile error) instead of collapsing
			// them to one literal backslash first. It also mishandled
			// the boundary itself: strings.IndexByte found the FIRST
			// quote character even if it was itself escaped (\"),
			// terminating the literal early. Fixed via
			// scanEscapedLiteral, which mirrors the primary parser's
			// exact escaping rules.
			lit, consumed, serr := scanEscapedLiteral(rem[1:], '"')
			if serr != nil {
				return "", "", "", nil, fmt.Errorf("%s: %w", opName, serr)
			}
			fragments = append(fragments, ParseFragment{Literal: lit})
			rem = rem[1+consumed:]
		} else if rem[0] == '\'' {
			// Single-quoted string literal — same fix as the
			// double-quoted branch above.
			lit, consumed, serr := scanEscapedLiteral(rem[1:], '\'')
			if serr != nil {
				return "", "", "", nil, fmt.Errorf("%s: %w", opName, serr)
			}
			fragments = append(fragments, ParseFragment{Literal: lit})
			rem = rem[1+consumed:]
		} else if rem[0] == '@' && len(rem) > 1 && (rem[1] == '"' || rem[1] == '\'') {
			// Verbatim string literal: @"..." or @'...' -- added
			// 2026-08-18. Real ADX's own kind=regex worked examples
			// use this directly (e.g. `@", totalSlices=\s*\d+\s*.*?
			// sliceNumber="`) specifically to avoid double-escaping
			// backslashes inside a regex-fragment literal; this
			// clause previously had no @ handling at all, so '@'
			// fell through to the bare-identifier branch below and
			// produced a garbage field name starting with '@'.
			// Same escaping rule as the primary expression parser's
			// own parseVerbatimString (expr.go): backslash is
			// literal, the enclosing quote is escaped by doubling it.
			quote := rem[1]
			lit, consumed, verr := scanVerbatimLiteral(rem[2:], quote)
			if verr != nil {
				return "", "", "", nil, fmt.Errorf("%s: %w", opName, verr)
			}
			fragments = append(fragments, ParseFragment{Literal: lit})
			rem = rem[2+consumed:]
		} else if rem[0] == '*' {
			// Wildcard (skip)
			fragments = append(fragments, ParseFragment{Field: "*"})
			rem = rem[1:]
		} else {
			// Field name — read until space, quote, or end
			end := 0
			// Real, pre-existing bug found and fixed 2026-08-18,
			// present since this repository's very first commit:
			// this loop's stop-character set never included ':',
			// so a field with a type annotation like
			// "totalSlices: long" had the colon swallowed straight
			// into the field NAME ("totalSlices:", with the colon
			// baked in), and the type-name text that followed
			// ("long") was then re-parsed by the OUTER loop as an
			// entirely separate, bogus field of its own — the
			// ":Type annotation" handling a few lines below this was
			// consequently dead code, since rem[0] could never
			// actually BE ':' by the time it ran (the field-name loop
			// had already consumed it). Reproduced live before
			// fixing: `parse s with "resourceName=" resourceName
			// ", totalSlices=" totalSlices: long` previously produced
			// a column literally named "totalSlices:" plus a spurious
			// second column named "long". This means col:type syntax
			// across kind=simple/relaxed/regex has never actually
			// worked in this engine until this fix.
			for end < len(rem) && rem[end] != ' ' && rem[end] != '\t' && rem[end] != '"' && rem[end] != '\'' && rem[end] != '*' && rem[end] != ':' {
				end++
			}
			if end == 0 {
				return "", "", "", nil, fmt.Errorf("%s: unexpected character in pattern: %c", opName, rem[0])
			}
			field := rem[:end]
			rem = rem[end:]
			// Optional : Type annotation -- VALUE now retained (real
			// bug found and fixed 2026-08-18: this was parsed and
			// then silently discarded, so col:long/col:date always
			// produced a plain string column with the raw extracted
			// text, never actually converted -- affected kind=simple/
			// relaxed/regex alike, not just the kind=regex work this
			// was found while doing; see applyParseCore in
			// operators.go for the corresponding engine-side fix).
			rem = strings.TrimLeft(rem, " \t")
			typeName := ""
			if len(rem) > 0 && rem[0] == ':' {
				rem = rem[1:]
				rem = strings.TrimLeft(rem, " \t")
				te := 0
				for te < len(rem) && rem[te] != ' ' && rem[te] != '\t' && rem[te] != '"' && rem[te] != '\'' {
					te++
				}
				typeName = rem[:te]
				rem = rem[te:]
			}
			fragments = append(fragments, ParseFragment{Field: field, Type: typeName})
		}
	}

	return column, kind, flags, fragments, nil
}

// scanEscapedLiteral scans the content of a regular (non-verbatim)
// quoted string literal starting at s[0], up to and including the
// closing quote character, applying the same backslash-escape rules
// as the primary expression parser's own parseString (expr.go): \\,
// \", \' collapse to the literal character, \n and \t become newline/
// tab, and any other \X sequence is passed through unchanged (both
// characters kept literally). Returns the unescaped literal text and
// how many bytes of s were consumed (including the closing quote).
// Added 2026-08-18 alongside fixing a real, pre-existing bug where
// this file's own literal scanning never processed escapes at all —
// see the call sites in parseParsePatternClause for the full story.
func scanEscapedLiteral(s string, quote byte) (literal string, consumed int, err error) {
	var sb strings.Builder
	i := 0
	for i < len(s) {
		ch := s[i]
		if ch == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '"', '\'', '\\':
				sb.WriteByte(s[i+1])
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			default:
				sb.WriteByte('\\')
				sb.WriteByte(s[i+1])
			}
			i += 2
			continue
		}
		if ch == quote {
			return sb.String(), i + 1, nil
		}
		sb.WriteByte(ch)
		i++
	}
	return "", 0, fmt.Errorf("unterminated string in pattern")
}

// scanVerbatimLiteral scans the content of a verbatim string literal
// (the text after the opening @" or @', with quote as the delimiter
// character) starting at s[0], returning the unescaped literal text
// and how many bytes of s were consumed (including the closing quote).
// Shared by parseParsePatternClause above; mirrors the primary
// expression parser's own parseVerbatimString (expr.go) exactly.
func scanVerbatimLiteral(s string, quote byte) (literal string, consumed int, err error) {
	var sb strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == quote {
			if i+1 < len(s) && s[i+1] == quote {
				sb.WriteByte(quote)
				i += 2
				continue
			}
			return sb.String(), i + 1, nil
		}
		sb.WriteByte(s[i])
		i++
	}
	return "", 0, fmt.Errorf("unterminated verbatim string in pattern")
}
