package parser

import (
	"fmt"
	"strings"
)

// parseParseKVOperator parses:
//   parse-kv Expression as ( KeysList ) with ( pair_delimiter=..., kv_delimiter=...
//     [, quote=...]* [, escape=...] [, greedy=true] )
//
// Verified against real ADX docs (parse-kv-operator.md) before writing
// this. Only the "specified delimiter" mode is accepted here (a query
// giving neither pair_delimiter nor kv_delimiter, or giving regex=...
// instead, is a real documented alternate form of this operator that
// this engine does not implement — see ParseKVOp's own doc comment).
// Expression is scoped to a bare column name, matching this engine's
// existing ParseOp's own Column-only scope rather than a full expression
// grammar.
func parseParseKVOperator(s string) (Operator, error) {
	s = strings.TrimSpace(s)

	asIdx := findKeyword(s, " as ")
	if asIdx < 0 {
		return nil, fmt.Errorf("parse-kv: expected 'as' keyword")
	}
	column := strings.TrimSpace(s[:asIdx])
	if column == "" || !isValidIdentifier(column) {
		return nil, fmt.Errorf("parse-kv: expected a column name before 'as', got %q", column)
	}
	rest := strings.TrimSpace(s[asIdx+len(" as "):])

	if !strings.HasPrefix(rest, "(") {
		return nil, fmt.Errorf("parse-kv: expected '(' KeysList ')' after 'as'")
	}
	keysClose := findMatchingParenAnyQuote(rest, 0)
	if keysClose < 0 {
		return nil, fmt.Errorf("parse-kv: unterminated KeysList — missing ')'")
	}
	keysListStr := strings.TrimSpace(rest[1:keysClose])
	keys, err := parseKVKeysList(keysListStr)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("parse-kv: KeysList must name at least one key")
	}

	rest = strings.TrimSpace(rest[keysClose+1:])
	lowerRest := strings.ToLower(rest)
	if !strings.HasPrefix(lowerRest, "with") {
		return nil, fmt.Errorf("parse-kv: expected 'with (...)' after KeysList")
	}
	rest = strings.TrimSpace(rest[len("with"):])
	if !strings.HasPrefix(rest, "(") {
		return nil, fmt.Errorf("parse-kv: expected '(' after 'with'")
	}
	optsClose := findMatchingParenAnyQuote(rest, 0)
	if optsClose < 0 || optsClose != len(rest)-1 {
		return nil, fmt.Errorf("parse-kv: unterminated or trailing content after 'with (...)'")
	}
	optsStr := strings.TrimSpace(rest[1:optsClose])

	op := &ParseKVOp{Column: column, Keys: keys}
	sawRegex := false
	sawPairDelim := false
	sawKVDelim := false
	for _, item := range splitAndTrim(optsStr, ',') {
		eqIdx := strings.Index(item, "=")
		if eqIdx < 0 {
			return nil, fmt.Errorf("parse-kv: expected Name=Value in with(...), got %q", item)
		}
		optName := strings.ToLower(strings.TrimSpace(item[:eqIdx]))
		optVal := stripKVQuotes(strings.TrimSpace(item[eqIdx+1:]))
		switch optName {
		case "pair_delimiter":
			op.PairDelimiter = optVal
			sawPairDelim = true
		case "kv_delimiter":
			op.KVDelimiter = optVal
			sawKVDelim = true
		case "quote":
			op.Quotes = append(op.Quotes, optVal)
		case "escape":
			op.Escape = optVal
		case "greedy":
			op.Greedy = strings.EqualFold(optVal, "true")
		case "regex":
			sawRegex = true
		default:
			return nil, fmt.Errorf("parse-kv: unrecognized with(...) option %q", optName)
		}
	}

	if sawRegex {
		return nil, fmt.Errorf("parse-kv: regex=... mode is not implemented — only the pair_delimiter/kv_delimiter (\"specified delimiter\") mode is supported")
	}
	if !sawPairDelim || !sawKVDelim {
		return nil, fmt.Errorf("parse-kv: the \"non-specified delimiter\" mode (no pair_delimiter/kv_delimiter given) is not implemented — pair_delimiter and kv_delimiter are both required here")
	}

	return op, nil
}

// parseKVKeysList parses a parse-kv KeysList: comma-separated
// Name[:Type] or ['quoted name'][:Type] entries.
func parseKVKeysList(s string) ([]ParseKVKey, error) {
	var keys []ParseKVKey
	for _, item := range splitAndTrim(s, ',') {
		var name, typeName string
		if strings.HasPrefix(item, "[") {
			closeIdx := strings.IndexByte(item, ']')
			if closeIdx < 0 {
				return nil, fmt.Errorf("parse-kv: unterminated ['name'] in KeysList: %q", item)
			}
			inner := strings.TrimSpace(item[1:closeIdx])
			inner = stripKVQuotes(inner)
			name = inner
			rest := strings.TrimSpace(item[closeIdx+1:])
			if strings.HasPrefix(rest, ":") {
				typeName = strings.TrimSpace(rest[1:])
			}
		} else {
			colonIdx := strings.Index(item, ":")
			if colonIdx < 0 {
				name = item
			} else {
				name = strings.TrimSpace(item[:colonIdx])
				typeName = strings.TrimSpace(item[colonIdx+1:])
			}
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("parse-kv: empty key name in KeysList")
		}
		keys = append(keys, ParseKVKey{Name: name, Type: typeName})
	}
	return keys, nil
}

// stripKVQuotes strips one layer of surrounding single or double quotes
// from a with(...) option value or a ['bracketed'] key name, e.g. 'x' -> x.
// Not related to ParseKVOp.Quotes (which governs quoting *within the data
// being parsed*, not this operator's own syntax) — kept as a small,
// separate helper to avoid conflating the two.
func stripKVQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}


// findMatchingParenAnyQuote finds the ')' matching the '(' at position
// start, treating BOTH '...' and "..." regions as opaque (paired on
// their own matching quote char, not a single-char-type toggle).
// Needed specifically because findMatchingParen (parser.go) only
// tracks double-quote regions — parse-kv's own with(...) clause
// routinely contains option values that are themselves single-quoted
// strings containing a literal double-quote character (e.g.
// quote='"'), which would otherwise desync a "-only toggle and miscount
// paren depth for everything after it.
func findMatchingParenAnyQuote(s string, start int) int {
	depth := 0
	var inQuote byte
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inQuote != 0 {
			if ch == inQuote && !precededByOddBackslashes(s, i) {
				inQuote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			inQuote = ch
			continue
		}
		if ch == '(' {
			depth++
		} else if ch == ')' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
