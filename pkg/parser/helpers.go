package parser

import (
	"fmt"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// precededByOddBackslashes reports whether the character at s[i] is
// itself escaped -- i.e. preceded by an odd number of consecutive
// backslashes. A naive "s[i-1] == '\\'" check gets this wrong for a
// doubled '\\' immediately before a closing quote (KQL's own escape
// for a single literal backslash, e.g. parse-kv's escape='\\' option):
// the byte right before the quote IS a backslash, but that backslash is
// itself escaped by the one before it, so the quote is NOT escaped and
// should close. Found and fixed while implementing parse-kv (2026-08-15)
// -- real ADX's own escape='\\' worked example was silently corrupting
// splitPipe's segment boundaries (a trailing "| project-away ..." got
// swallowed into the parse-kv segment's own text) before this fix,
// since the same naive check was duplicated across splitPipe,
// splitRespectingParens, and findKeyword below.
func precededByOddBackslashes(s string, i int) bool {
	count := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		count++
	}
	return count%2 == 1
}

// splitPipe splits on | respecting quoted strings.
func splitPipe(s string) []string {
	var parts []string
	var current strings.Builder
	inQuote := byte(0)
	depth := 0

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inQuote != 0 {
			current.WriteByte(ch)
			if ch == inQuote && !precededByOddBackslashes(s, i) {
				inQuote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inQuote = ch
			current.WriteByte(ch)
			continue
		}
		if ch == '(' {
			depth++
		}
		if ch == ')' && depth > 0 {
			depth--
		}
		if ch == '|' && depth == 0 {
			parts = append(parts, current.String())
			current.Reset()
			continue
		}
		current.WriteByte(ch)
	}
	parts = append(parts, current.String())
	return parts
}

// splitRespectingParens splits on sep but not inside parentheses or quotes.
func splitRespectingParens(s string, sep byte) []string {
	var parts []string
	var current strings.Builder
	depth := 0
	inQuote := byte(0)

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inQuote != 0 {
			current.WriteByte(ch)
			if ch == inQuote && !precededByOddBackslashes(s, i) {
				inQuote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inQuote = ch
			current.WriteByte(ch)
			continue
		}
		if ch == '(' {
			depth++
		}
		if ch == ')' && depth > 0 {
			depth--
		}
		if ch == sep && depth == 0 {
			parts = append(parts, current.String())
			current.Reset()
			continue
		}
		current.WriteByte(ch)
	}
	parts = append(parts, current.String())
	return parts
}

// splitAndTrim splits on sep and trims whitespace from each part.
func splitAndTrim(s string, sep byte) []string {
	parts := splitRespectingParens(s, sep)
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// findKeyword finds a case-insensitive keyword surrounded by word boundaries.
func findKeyword(s string, kw string) int {
	lower := strings.ToLower(s)
	kwLower := strings.ToLower(kw)

	idx := 0
	for {
		pos := strings.Index(lower[idx:], kwLower)
		if pos < 0 {
			return -1
		}
		pos += idx

		// Check word boundaries — must not be inside parentheses
		depth := 0
		inQuote := byte(0)
		for i := 0; i < pos; i++ {
			if inQuote != 0 {
				if s[i] == inQuote && !precededByOddBackslashes(s, i) {
					inQuote = 0
				}
				continue
			}
			if s[i] == '"' || s[i] == '\'' {
				inQuote = s[i]
				continue
			}
			if s[i] == '(' {
				depth++
			}
			if s[i] == ')' && depth > 0 {
				depth--
			}
		}
		if depth == 0 && inQuote == 0 {
			return pos
		}
		idx = pos + 1
	}
}

// parseTableFunc checks if source looks like a table-valued function call
// such as csv("path"), json("path"), ndjson("path"), parquet("path"), or
// vortex("path"). Returns nil if not a table function.
// parseDatabaseTableRef recognizes database('alias').TableName as a
// query source — see DatabaseTableRef's doc comment (ast.go) for why
// this specific syntax was chosen (real ADX conformance, repurposed
// for filesystem federation) rather than inventing new syntax.
// Deliberately narrow: the alias must be a quoted string literal (no
// expression evaluation — real ADX itself disallows a subquery result
// here too, "the value can't be the result of subquery evaluation"),
// and the table name is a bare identifier, matching every other table
// reference in this parser.
func parseDatabaseTableRef(source string) *DatabaseTableRef {
	const prefix = "database("
	lower := strings.ToLower(source)
	if !strings.HasPrefix(lower, prefix) {
		return nil
	}
	closeIdx := strings.Index(source, ")")
	if closeIdx < 0 {
		return nil
	}
	inner := strings.TrimSpace(source[len(prefix):closeIdx])
	if len(inner) < 2 {
		return nil
	}
	if !((inner[0] == '"' && inner[len(inner)-1] == '"') ||
		(inner[0] == '\'' && inner[len(inner)-1] == '\'')) {
		return nil
	}
	alias := inner[1 : len(inner)-1]
	if alias == "" {
		return nil
	}

	rest := source[closeIdx+1:]
	if !strings.HasPrefix(rest, ".") {
		return nil
	}
	tableName := strings.TrimSpace(rest[1:])
	if tableName == "" || strings.ContainsAny(tableName, " \t.()") {
		return nil
	}

	return &DatabaseTableRef{Alias: alias, TableName: tableName}
}

// parseStoredFunctionCall recognizes FuncName() — a bare identifier
// followed by empty parens and nothing else — as a candidate stored
// (persisted, tabular) function call. Deliberately conservative:
// non-empty parens (FuncName(arg)) don't match at all here, since
// parameterized stored functions aren't supported in this first
// version (see CreateFunctionCmd's doc comment) — a call with
// arguments falls through to being treated as an ordinary,
// unresolvable table name instead of silently misparsing, which
// surfaces a clear "table not found"-style error rather than quietly
// dropping the arguments.
func parseStoredFunctionCall(source string) (*StoredFunctionCall, error) {
	s := strings.TrimSpace(source)
	openIdx := strings.Index(s, "(")
	if openIdx < 0 || !strings.HasSuffix(s, ")") {
		return nil, nil // not a call at all — not an error, just "doesn't match"
	}
	name := strings.TrimSpace(s[:openIdx])
	if name == "" || !isValidIdentifier(name) {
		return nil, nil
	}
	closeIdx := findMatchingParen(s, openIdx)
	if closeIdx < 0 || closeIdx != len(s)-1 {
		return nil, nil // unbalanced, or trailing text after the closing paren — not a call this recognizes
	}
	inner := strings.TrimSpace(s[openIdx+1 : closeIdx])
	if inner == "" {
		return &StoredFunctionCall{Name: name}, nil // parameterless — ArgTexts stays nil
	}

	// Deliberately just split + trim here, no ParseExpr (or any other
	// parse) call at all — see StoredFunctionCall's own doc comment
	// for exactly why: a tabular argument and a scalar argument need
	// different parsers, and which one applies at a given position
	// isn't knowable here, purely syntactically, without the callee's
	// declared signature.
	var argTexts []string
	for _, argText := range splitRespectingParens(inner, ',') {
		argText = strings.TrimSpace(argText)
		if argText == "" {
			return nil, fmt.Errorf("%s(...): empty argument in call", name)
		}
		argTexts = append(argTexts, argText)
	}
	return &StoredFunctionCall{Name: name, ArgTexts: argTexts}, nil
}

// ParseFunctionParams parses a stored function's parameter-list text
// (e.g. "x: long, y: string = \"default\"") into []FunctionParam.
// Shared, deliberately, between the definition side (parseCreateFunction,
// below) and the resolution side (engine/stored_functions.go's
// resolveStoredFunction) — the parameter list is stored as its own
// raw text, same as Body already is, and re-parsed at call time via
// this exact function, rather than inventing a separate serialization
// format for something that's already valid, round-trippable KQL
// syntax on its own.
func ParseFunctionParams(paramText string) ([]FunctionParam, error) {
	paramText = strings.TrimSpace(paramText)
	if paramText == "" {
		return nil, nil
	}
	var params []FunctionParam
	for _, part := range splitRespectingParens(paramText, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		colonIdx := strings.Index(part, ":")
		if colonIdx < 0 {
			return nil, fmt.Errorf("parameter %q: expected Name: Type", part)
		}
		name := strings.TrimSpace(part[:colonIdx])
		if !isValidIdentifier(name) {
			return nil, fmt.Errorf("parameter %q: invalid parameter name %q", part, name)
		}
		rest := strings.TrimSpace(part[colonIdx+1:])

		// Tabular parameter: Name:(...) — the same syntax as a table
		// definition (column name/type pairs), or a solitary (*)
		// meaning "any tabular schema". Verified against real ADX's
		// own docs before adopting this exact shape. Checked BEFORE
		// the scalar-type branch below, since a leading "(" can never
		// be the start of a valid scalar type name.
		if strings.HasPrefix(rest, "(") {
			if !strings.HasSuffix(rest, ")") {
				return nil, fmt.Errorf("parameter %q: unterminated tabular schema", name)
			}
			schemaText := strings.TrimSpace(rest[1 : len(rest)-1])
			p := FunctionParam{Name: name, IsTabular: true}
			if schemaText == "*" {
				p.IsAnySchema = true
			} else if schemaText != "" {
				for _, colPart := range splitRespectingParens(schemaText, ',') {
					colPart = strings.TrimSpace(colPart)
					if colPart == "" {
						continue
					}
					colColonIdx := strings.Index(colPart, ":")
					if colColonIdx < 0 {
						return nil, fmt.Errorf("parameter %q: tabular schema column %q: expected Name: Type", name, colPart)
					}
					colName := strings.TrimSpace(colPart[:colColonIdx])
					if !isValidIdentifier(colName) {
						return nil, fmt.Errorf("parameter %q: tabular schema column %q: invalid column name %q", name, colPart, colName)
					}
					colType, err := types.ParseType(strings.TrimSpace(colPart[colColonIdx+1:]))
					if err != nil {
						return nil, fmt.Errorf("parameter %q: tabular schema column %q: %w", name, colPart, err)
					}
					p.TabularSchema = append(p.TabularSchema, TabularColumn{Name: colName, Type: colType})
				}
			}
			params = append(params, p)
			continue
		}

		typeText := rest
		var defaultText string
		hasDefault := false
		if eqIdx := strings.Index(rest, "="); eqIdx >= 0 {
			typeText = strings.TrimSpace(rest[:eqIdx])
			defaultText = strings.TrimSpace(rest[eqIdx+1:])
			hasDefault = true
		}

		kqlType, err := types.ParseType(typeText)
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", name, err)
		}

		p := FunctionParam{Name: name, Type: kqlType, HasDefault: hasDefault}
		if hasDefault {
			defExpr, err := ParseExpr(defaultText)
			if err != nil {
				return nil, fmt.Errorf("parameter %q: default value %q: %w", name, defaultText, err)
			}
			p.Default = defExpr
		}
		params = append(params, p)
	}

	// Real ADX rule, verified before enforcing it: "when using both
	// tabular input arguments and scalar input arguments, put all
	// tabular input arguments before the scalar input arguments."
	seenScalar := false
	for _, p := range params {
		if !p.IsTabular {
			seenScalar = true
		} else if seenScalar {
			return nil, fmt.Errorf("parameter %q: tabular parameters must all come before scalar parameters", p.Name)
		}
	}

	return params, nil
}

// isValidIdentifier reports whether s is a bare identifier — letter or
// underscore, then letters/digits/underscores. Shared by the stored-
// function-call recognizer above and (implicitly, by the same shape)
// anywhere else in this file that needs the same check.
func isValidIdentifier(s string) bool {
	for i, r := range s {
		if i == 0 && !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
		if i > 0 && !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func parseTableFunc(source string) *TableFunc {
	lower := strings.ToLower(source)
	for _, name := range []string{"csv", "json", "ndjson", "parquet", "vortex"} {
		prefix := name + "("
		if strings.HasPrefix(lower, prefix) && strings.HasSuffix(strings.TrimSpace(source), ")") {
			// Extract the argument between parens
			inner := source[len(prefix) : len(strings.TrimSpace(source))-1]
			inner = strings.TrimSpace(inner)
			// Strip quotes
			if len(inner) >= 2 {
				if (inner[0] == '"' && inner[len(inner)-1] == '"') ||
					(inner[0] == '\'' && inner[len(inner)-1] == '\'') {
					inner = inner[1 : len(inner)-1]
				}
			}
			if inner == "" {
				return nil
			}
			return &TableFunc{Name: name, Path: inner}
		}
	}
	return nil
}
