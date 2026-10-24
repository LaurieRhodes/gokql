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
		if ch == '(' || ch == '{' {
			depth++
		}
		if (ch == ')' || ch == '}') && depth > 0 {
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
	return expandTopHitters(parts)
}

// expandTopHitters rewrites each `top-hitters N of Expr [by SumExpr]`
// segment as the summarize and top it means (exact counts and sums,
// which the emulator's approximate ones equal on modest data):
//
//	summarize approximate_count_X = count() by X = Expr | top N by approximate_count_X desc
//	summarize approximate_sum_Y = sum(SumExpr) by X = Expr | top N by approximate_sum_Y desc
func expandTopHitters(parts []string) []string {
	var out []string
	for i, seg := range parts {
		t := strings.TrimSpace(seg)
		if !strings.HasPrefix(keywordFold(t), "top-hitters ") {
			if out != nil {
				out = append(out, seg)
			}
			continue
		}
		if out == nil {
			out = append([]string(nil), parts[:i]...)
		}
		rest := strings.TrimSpace(t[len("top-hitters "):])
		of := findKeyword(rest, "of")
		if of < 0 {
			out = append(out, seg) // let parseOperator report it
			continue
		}
		n, body := strings.TrimSpace(rest[:of]), strings.TrimSpace(rest[of+2:])
		ofText, byText := body, ""
		if by := findKeyword(body, "by"); by >= 0 {
			ofText, byText = strings.TrimSpace(body[:by]), strings.TrimSpace(body[by+2:])
		}
		name := ofText
		if x, err := ParseExpr(ofText); err == nil {
			if r, ok := ExprResultName(x); ok && r != "" {
				name = r
			}
		}
		col, agg := "approximate_count_"+name, "count()"
		if byText != "" {
			sumName := byText
			if x, err := ParseExpr(byText); err == nil {
				if r, ok := ExprResultName(x); ok && r != "" {
					sumName = r
				}
			}
			col, agg = "approximate_sum_"+sumName, "sum("+byText+")"
		}
		out = append(out,
			fmt.Sprintf(" summarize ['%s'] = %s by ['%s'] = %s ", col, agg, name, ofText),
			fmt.Sprintf(" top %s by ['%s'] desc", n, col))
	}
	if out == nil {
		return parts
	}
	return out
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

// keywordFold lowercases ASCII letters and maps tabs and line breaks
// to spaces, byte for byte, so indices into the result are indices into
// s and a keyword written " by " also matches across a line break.
func keywordFold(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'A' && c <= 'Z':
			b[i] = c + ('a' - 'A')
		case c == '\t' || c == '\n' || c == '\r':
			b[i] = ' '
		}
	}
	return string(b)
}

// findKeyword finds a case-insensitive keyword surrounded by word
// boundaries, outside parentheses and quotes. Spaces in kw also match
// tabs and line breaks.
func findKeyword(s string, kw string) int {
	lower := keywordFold(s)
	kwLower := keywordFold(kw)

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
	cluster, db, table, ok := SplitQualifiedName(source)
	if !ok || db == "" || table == "" || strings.ContainsAny(table, " \t.()*") {
		return nil
	}
	return &DatabaseTableRef{Alias: db, TableName: table, Cluster: cluster}
}

// ParseDatabaseTableRef is parseDatabaseTableRef for the engine: a
// table name of the form [cluster('C').]database('D').T.
func ParseDatabaseTableRef(source string) *DatabaseTableRef { return parseDatabaseTableRef(source) }

// SplitQualifiedName splits [cluster('C').]database('D').Rest into its
// parts (quotes ' or "); Rest may be a wildcard pattern. ok is false
// when s does not start with cluster( or database(.
func SplitQualifiedName(s string) (cluster, db, rest string, ok bool) {
	s = strings.TrimSpace(s)
	call := func(s, name string) (arg, rest string, ok bool) {
		if !strings.HasPrefix(keywordFold(s), name+"(") {
			return "", "", false
		}
		in := strings.TrimSpace(s[len(name)+1:])
		if in == "" || (in[0] != '\'' && in[0] != '"') {
			return "", "", false
		}
		end := strings.IndexByte(in[1:], in[0])
		if end < 0 {
			return "", "", false
		}
		arg = in[1 : end+1]
		after := strings.TrimSpace(in[end+2:])
		if !strings.HasPrefix(after, ")") {
			return "", "", false
		}
		return arg, strings.TrimSpace(after[1:]), true
	}
	if c, r, ok := call(s, "cluster"); ok {
		if !strings.HasPrefix(r, ".") {
			return "", "", "", false
		}
		cluster, s = c, strings.TrimSpace(r[1:])
		if cluster == "" {
			return "", "", "", false
		}
	}
	d, r, ok := call(s, "database")
	if !ok || d == "" || !strings.HasPrefix(r, ".") {
		return "", "", "", false
	}
	return cluster, d, strings.TrimSpace(r[1:]), true
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

// unquoteIdent returns the name inside a quoted identifier, ['name'] or
// ["name"] (KQL's form for names that are not bare identifiers), and s
// trimmed otherwise.
func unquoteIdent(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 4 || s[0] != '[' || s[len(s)-1] != ']' {
		return s
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if len(inner) < 2 || (inner[0] != '\'' && inner[0] != '"') || inner[len(inner)-1] != inner[0] {
		return s
	}
	ex, err := ParseExpr(inner)
	if err != nil {
		return s
	}
	if lit, ok := ex.(*Literal); ok {
		if v, ok := lit.Value.(string); ok {
			return v
		}
	}
	return s
}

func unquoteIdents(names []string) []string {
	for i, n := range names {
		names[i] = unquoteIdent(n)
	}
	return names
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
	lower := keywordFold(source)
	for _, name := range []string{"csv", "json", "ndjson", "parquet", "vortex", "arrow"} {
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

// normalizeLeadingKeywordWhitespace collapses whatever whitespace run
// (if any) immediately follows a segment's own leading keyword-shaped
// token (letters/digits/hyphens — matching "where", "project-away",
// "make-series", "sample-distinct", and every other operator keyword
// this parser recognizes) down to a single space character.
//
// Every operator/statement dispatch in this parser (parseOperator,
// and the union/print/search special-cases in parseQuery) is written
// as `strings.HasPrefix(lower, "keyword ")` — a literal single-space
// check. That's correct for the overwhelmingly common case (keyword
// immediately followed by its own first argument on the same line),
// but genuinely wrong whenever a query is formatted with a line break
// right after the keyword instead — `where\n  x > 1`, `extend\n  y =
// x+1`, `print\n  r = 1` all failed outright ("unknown operator" /
// "table \"print\\n...\" not found") before this fix, since a literal
// "\n" or "\t" right after the keyword never matches a literal " ".
// Found and fixed 2026-08-17, while investigating what first looked
// like a print-specific quirk and turned out to be systemic across
// essentially every keyword-prefixed check in this parser (confirmed
// live: where and extend have the identical failure) — rather than
// patching each of the 40+ individual `HasPrefix(lower, "keyword ")`
// sites one at a time (a large, repetitive, error-prone sweep), this
// single, shared normalization runs once at each of the two real
// entry points (parseOperator for pipe-segment operators, parseQuery
// for the union/print/search first-segment special-cases) before any
// keyword check happens, so every existing and future keyword-prefix
// check downstream benefits without needing its own fix.
//
// Deliberately conservative: only touches whitespace RIGHT AFTER the
// leading keyword token, nowhere else in the segment — a newline
// inside a later string literal, a later clause, or a nested
// subquery is completely untouched, since this only ever looks at the
// very start of the string before the first non-identifier character.
func normalizeLeadingKeywordWhitespace(s string) string {
	i := 0
	for i < len(s) {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' {
			i++
			continue
		}
		break
	}
	if i == 0 || i >= len(s) {
		return s // no leading keyword-shaped token, or nothing follows it
	}
	j := i
	for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
		j++
	}
	if j == i {
		return s // keyword isn't followed by any whitespace at all
	}
	if j-i == 1 && s[i] == ' ' {
		return s // already exactly a single space — the common case, no allocation needed
	}
	return s[:i] + " " + s[j:]
}
