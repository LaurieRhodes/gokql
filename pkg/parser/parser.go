package parser

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// Parse parses a KQL statement (query or management command).
// Supports multi-statement input with let bindings:
//
//	let name = expr; let name2 = Table | where ...; FinalQuery | ...
//
// unwrapMaterialize returns X for a source that is exactly
// materialize(X).
func unwrapMaterialize(source string) (string, bool) {
	t := strings.TrimSpace(source)
	lower := keywordFold(t)
	if !strings.HasPrefix(lower, "materialize") {
		return "", false
	}
	rest := strings.TrimSpace(t[len("materialize"):])
	if !strings.HasPrefix(rest, "(") {
		return "", false
	}
	closeIdx := matchingClose(rest, 0, '(', ')')
	if closeIdx != len(rest)-1 {
		return "", false
	}
	return strings.TrimSpace(rest[1:closeIdx]), true
}

// parseTopNested parses the comma-separated top-nested clauses.
func parseTopNested(seg string) (*TopNestedOp, error) {
	op := &TopNestedOp{}
	for i, clause := range splitRespectingParens(seg, ',') {
		clause = strings.TrimSpace(clause)
		lower := keywordFold(clause)
		if !strings.HasPrefix(lower, "top-nested") {
			return nil, fmt.Errorf("top-nested clause %d: expected 'top-nested', got %q", i+1, clause)
		}
		rest := strings.TrimSpace(clause[len("top-nested"):])
		var lvl TopNestedLevel

		ofIdx := findKeyword(" "+rest, " of ")
		if ofIdx < 0 {
			return nil, fmt.Errorf("top-nested clause %d: expected 'of'", i+1)
		}
		if nText := strings.TrimSpace((" " + rest)[:ofIdx]); nText != "" {
			n, err := strconv.ParseInt(nText, 10, 64)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("top-nested clause %d: N must be a non-negative integer, got %q", i+1, nText)
			}
			lvl.N = n
		}
		rest = strings.TrimSpace((" " + rest)[ofIdx+len(" of "):])

		byIdx := findKeyword(rest, " by ")
		if byIdx < 0 {
			return nil, fmt.Errorf("top-nested clause %d: expected 'by'", i+1)
		}
		exprPart := strings.TrimSpace(rest[:byIdx])
		aggPart := strings.TrimSpace(rest[byIdx+len(" by "):])

		if w := findKeyword(exprPart, " with others"); w >= 0 {
			tail := strings.TrimSpace(exprPart[w+len(" with others"):])
			if !strings.HasPrefix(tail, "=") {
				return nil, fmt.Errorf("top-nested clause %d: expected 'with others = value'", i+1)
			}
			c, err := ParseExpr(strings.TrimSpace(tail[1:]))
			if err != nil {
				return nil, fmt.Errorf("top-nested clause %d: others value: %w", i+1, err)
			}
			lvl.HasOthers, lvl.Others = true, c
			exprPart = strings.TrimSpace(exprPart[:w])
		}
		if eq := assignmentEqIndex(exprPart); eq > 0 {
			lvl.Name = strings.TrimSpace(exprPart[:eq])
			exprPart = strings.TrimSpace(exprPart[eq+1:])
		}
		e, err := ParseExpr(exprPart)
		if err != nil {
			return nil, fmt.Errorf("top-nested clause %d: %w", i+1, err)
		}
		lvl.Expr = e
		if lvl.Name == "" {
			if ref, ok := e.(*ColumnRef); ok {
				lvl.Name = ref.Name
			} else {
				lvl.Name = fmt.Sprintf("Column%d", i+1)
			}
		}

		al := strings.ToLower(aggPart)
		switch {
		case strings.HasSuffix(al, " asc"):
			lvl.Asc = true
			aggPart = strings.TrimSpace(aggPart[:len(aggPart)-4])
		case strings.HasSuffix(al, " desc"):
			aggPart = strings.TrimSpace(aggPart[:len(aggPart)-5])
		}
		named := assignmentEqIndex(aggPart) > 0
		agg, err := parseAggregation(aggPart)
		if err != nil {
			return nil, fmt.Errorf("top-nested clause %d: %w", i+1, err)
		}
		if !named {
			agg.Name = "aggregated_" + lvl.Name
		}
		lvl.Agg = *agg
		op.Levels = append(op.Levels, lvl)
	}
	if len(op.Levels) == 0 {
		return nil, fmt.Errorf("top-nested: no clauses")
	}
	return op, nil
}

// stripLineComments removes // comments (to the end of the line)
// outside string literals. Quoted text is kept byte for byte: "..."
// and '...' with backslash escapes, and @-prefixed verbatim strings
// where only a doubled quote escapes. .ingest inline is exempt: its
// payload is data, not KQL.
// rewriteMultilineStrings turns each ```...``` multi-line string
// literal into an equivalent escaped "..." literal, so every splitter
// downstream sees an ordinary string (no ; | // inside it can split or
// truncate the statement). Quoted strings and // comments are skipped.
func rewriteMultilineStrings(s string) string {
	if !strings.Contains(s, "```") {
		return s
	}
	var b strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
			b.WriteByte(c)
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			end := strings.IndexByte(s[i:], '\n')
			if end < 0 {
				end = len(s) - i
			}
			b.WriteString(s[i : i+end])
			i += end - 1
		case strings.HasPrefix(s[i:], "```"):
			end := strings.Index(s[i+3:], "```")
			if end < 0 {
				b.WriteString(s[i:])
				return b.String()
			}
			body := s[i+3 : i+3+end]
			b.WriteByte('"')
			for k := 0; k < len(body); k++ {
				switch ch := body[k]; ch {
				case '\\':
					b.WriteString(`\\`)
				case '"':
					b.WriteString(`\"`)
				case '\n':
					b.WriteString(`\n`)
				case '\r':
					b.WriteString(`\r`)
				case '\t':
					b.WriteString(`\t`)
				default:
					b.WriteByte(ch)
				}
			}
			b.WriteByte('"')
			i += 3 + end + 2
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func stripLineComments(s string) string {
	var b strings.Builder
	var quote byte
	verbatim := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			b.WriteByte(ch)
			switch {
			case !verbatim && ch == '\\' && i+1 < len(s):
				i++
				b.WriteByte(s[i])
			case ch == quote:
				if verbatim && i+1 < len(s) && s[i+1] == quote {
					i++
					b.WriteByte(s[i])
				} else {
					quote = 0
				}
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			verbatim = i > 0 && s[i-1] == '@'
			b.WriteByte(ch)
			continue
		}
		if ch == '/' && i+1 < len(s) && s[i+1] == '/' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				b.WriteByte('\n')
			}
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// rewriteBackslashVerbatim rewrites each verbatim literal (@"...",
// @'...', h@"...") in which a backslash touches a quote character, as
// in @"C:\", into the equivalent escaped literal ("C:\\"). Many of the
// statement and argument scanners judge a quote escaped by the
// backslashes before it, which is wrong inside a verbatim literal;
// only these literals can mislead them, so others keep their text.
func rewriteBackslashVerbatim(s string) string {
	if !strings.Contains(s, "@") || !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	changed := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch != '"' && ch != '\'' {
			b.WriteByte(ch)
			continue
		}
		quote := ch
		verbatim := i > 0 && s[i-1] == '@'
		// find the closing quote and the literal's content
		j := i + 1
		var content strings.Builder
		for j < len(s) {
			c := s[j]
			if !verbatim && c == '\\' && j+1 < len(s) {
				content.WriteByte(c)
				content.WriteByte(s[j+1])
				j += 2
				continue
			}
			if c == quote {
				if verbatim && j+1 < len(s) && s[j+1] == quote {
					content.WriteByte(quote) // "" is one quote
					j += 2
					continue
				}
				break
			}
			content.WriteByte(c)
			j++
		}
		text := content.String()
		awkward := verbatim && (strings.Contains(text, "\\"+string(quote)) || strings.HasSuffix(text, "\\") ||
			(strings.Contains(text, "\\") && strings.ContainsRune(text, rune(quote))))
		if !awkward || j >= len(s) {
			b.WriteString(s[i:min(j+1, len(s))])
			i = j
			continue
		}
		// drop the '@' already written, keep an h prefix
		out := b.String()
		b.Reset()
		b.WriteString(out[:len(out)-1])
		b.WriteByte(quote)
		for k := 0; k < len(text); k++ {
			if text[k] == '\\' || text[k] == quote {
				b.WriteByte('\\')
			}
			b.WriteByte(text[k])
		}
		b.WriteByte(quote)
		changed = true
		i = j
	}
	if !changed {
		return s
	}
	return b.String()
}

func Parse(input string) (Statement, error) {
	return ParseWithParameters(input, nil)
}

// ParseWithParameters is Parse with query parameter values (a request's
// properties.Parameters): each overrides the default of the parameter
// of that name in the query's declare query_parameters(...). Values are
// KQL literals; a string parameter may also be given as raw text.
func ParseWithParameters(input string, params map[string]string) (Statement, error) {
	input, err := translateQueryLanguage(input)
	if err != nil {
		return nil, err
	}
	head := keywordFold(strings.TrimSpace(stripLineComments(input)))
	if !strings.HasPrefix(head, ".ingest inline") && !strings.HasPrefix(head, ".create-or-alter graph_model") &&
		!(strings.HasPrefix(head, ".alter graph_model") && strings.Contains(head, " policy ")) {
		// Commands above fence a payload with ```; everywhere else ```
		// opens a multi-line string literal.
		input = rewriteMultilineStrings(input)
	}
	if !strings.HasPrefix(head, ".ingest inline") {
		input = stripLineComments(input)
		input = rewriteBackslashVerbatim(input)
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	// Split on semicolons (respecting strings and parentheses)
	stmts := splitStatements(input)

	// declare pattern statements: their invocations in the other
	// statements are expanded before those are parsed.
	var decls map[string]*patternDecl
	var rest []string
	for _, st := range stmts {
		if isPatternDecl(st) {
			d, err := parsePatternDecl(st)
			if err != nil {
				return nil, err
			}
			if decls == nil {
				decls = map[string]*patternDecl{}
			}
			decls[d.name] = d
			continue
		}
		if decls != nil {
			x, err := expandPatterns(st, decls)
			if err != nil {
				return nil, err
			}
			st = x
		}
		rest = append(rest, st)
	}
	stmts = rest

	// set statements and declare query_parameters(...) come first;
	// a parameter's default binds as a let.
	options := map[string]string{}
	var paramLets []*LetStatement
	for len(stmts) > 0 {
		st := strings.TrimSpace(stmts[0])
		lower := keywordFold(st)
		if strings.HasPrefix(lower, "set ") {
			name, val := strings.TrimSpace(st[4:]), "true"
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name, val = strings.TrimSpace(name[:eq]), strings.TrimSpace(name[eq+1:])
			}
			options[strings.ToLower(name)] = val
			stmts = stmts[1:]
			continue
		}
		if strings.HasPrefix(lower, "declare query_parameters") {
			rest := strings.TrimSpace(st[len("declare query_parameters"):])
			if !strings.HasPrefix(rest, "(") || !strings.HasSuffix(rest, ")") {
				return nil, fmt.Errorf("declare query_parameters: expected (name:type [= default], ...)")
			}
			decl, err := ParseFunctionParams(rest[1 : len(rest)-1])
			if err != nil {
				return nil, fmt.Errorf("declare query_parameters: %w", err)
			}
			for _, p := range decl {
				if v, ok := params[p.Name]; ok {
					x, err := ParseExpr(v)
					if lit, isLit := x.(*Literal); err != nil || (p.Type == types.TypeString && (!isLit || lit.Type != types.TypeString)) {
						if p.Type != types.TypeString {
							return nil, fmt.Errorf("query parameter %s: %q is not a %s literal", p.Name, v, p.Type)
						}
						x = &Literal{Value: v, Type: types.TypeString}
					}
					paramLets = append(paramLets, &LetStatement{Name: p.Name, Value: &ScalarExpr{Expr: x, Raw: v}})
					continue
				}
				if !p.HasDefault {
					return nil, fmt.Errorf("query parameter %s has no value and no default", p.Name)
				}
				paramLets = append(paramLets, &LetStatement{Name: p.Name, Value: &ScalarExpr{Expr: p.Default}})
			}
			stmts = stmts[1:]
			continue
		}
		break
	}
	if len(stmts) == 0 {
		return nil, fmt.Errorf("expected a query after the set/declare statements")
	}
	if len(options) > 0 || len(paramLets) > 0 {
		st, err := parseStatements(stmts)
		if err != nil {
			return nil, err
		}
		cs, ok := st.(*CompoundStatement)
		if !ok {
			cs = &CompoundStatement{Final: st}
		}
		cs.Lets = append(paramLets, cs.Lets...)
		for i := range cs.BatchLetsBefore {
			cs.BatchLetsBefore[i] += len(paramLets)
		}
		cs.Options = options
		return cs, nil
	}
	return parseStatements(stmts)
}

// parseStatements parses split statements: a single statement, or
// lets and one or more statements.
func parseStatements(stmts []string) (Statement, error) {
	// If single statement with no let prefix, parse directly
	if len(stmts) == 1 && !strings.HasPrefix(keywordFold(strings.TrimSpace(stmts[0])), "let ") {
		return parseSingleStatement(stmts[0])
	}

	// Multi-statement: let bindings and one or more statements. Several
	// tabular statements form a query batch, each producing a result;
	// with a control command among them, only the last statement runs
	// (scripts of commands are not batches).
	var lets []*LetStatement
	var finals []string
	var letsBefore []int

	for i, s := range stmts {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		lower := keywordFold(s)
		if strings.HasPrefix(lower, "let ") {
			letStmt, err := parseLetStatement(s)
			if err != nil {
				return nil, fmt.Errorf("let statement %d: %w", i+1, err)
			}
			lets = append(lets, letStmt)
		} else {
			finals = append(finals, s)
			letsBefore = append(letsBefore, len(lets))
		}
	}

	if len(finals) == 0 {
		// All statements are lets with no final query — error
		return nil, fmt.Errorf("expected a query after let statement(s)")
	}
	batch := len(finals) > 1
	for _, f := range finals {
		if strings.HasPrefix(f, ".") {
			batch = false
		}
	}
	if !batch {
		finals = finals[len(finals)-1:]
		letsBefore = letsBefore[len(letsBefore)-1:]
	}

	cs := &CompoundStatement{Lets: lets}
	for k, f := range finals {
		st, err := parseSingleStatement(f)
		if err != nil {
			if len(finals) == 1 {
				return nil, fmt.Errorf("final statement: %w", err)
			}
			return nil, fmt.Errorf("statement %d: %w", k+1, err)
		}
		if k == len(finals)-1 {
			cs.Final = st
		} else {
			cs.Batch = append(cs.Batch, st)
			cs.BatchLetsBefore = append(cs.BatchLetsBefore, letsBefore[k])
		}
	}

	if len(lets) == 0 && len(cs.Batch) == 0 {
		return cs.Final, nil
	}
	return cs, nil
}

// parseSingleStatement parses a non-let statement.
// pipeableSimpleCommandPrefixes are dot-commands with no internal
// query/pipe grammar of their own (no <| clause, no bracket-depth-
// sensitive argument), so splitting the whole input on bare '|' is
// unambiguous and safe. Every OTHER dot-command (.set-or-append,
// .embed-into, .chunk-file, etc.) is deliberately excluded: splitPipe
// does not special-case '<|', so applying it generically would
// silently split a compound command's OWN internal query at the wrong
// point rather than at a trailing pipe-continuation — a real
// correctness risk, not just an unsupported-syntax error. Found live
// while working the backlog: `.show tables | where ...` and
// `.help | where ...` both failed with "unknown command" even though
// nothing about either command's own grammar conflicts with piping.
var pipeableSimpleCommandPrefixes = []string{
	".show tables", ".show database", ".help", ".show table ",
	".show graph_model", ".show graph_snapshot", ".show operations",
}

func parseSingleStatement(input string) (Statement, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}
	if input[0] == '.' {
		lower := keywordFold(input)
		for _, prefix := range pipeableSimpleCommandPrefixes {
			if !strings.HasPrefix(lower, prefix) {
				continue
			}
			parts := splitPipe(input)
			if len(parts) == 1 {
				break // no trailing pipe present; fall through to the normal path
			}
			inner, err := parseCommand(strings.TrimSpace(parts[0]))
			if err != nil {
				return nil, err
			}
			piped := &PipedCommand{Inner: inner}
			for i, seg := range parts[1:] {
				seg = strings.TrimSpace(seg)
				if seg == "" {
					continue
				}
				op, err := parseOperator(seg)
				if err != nil {
					return nil, fmt.Errorf("pipeline operator %d after %s: %w", i+1, strings.Fields(input)[0], err)
				}
				piped.Operators = append(piped.Operators, op)
			}
			return piped, nil
		}
		return parseCommand(input)
	}
	return parseQuery(input)
}

// parseLetStatement parses "let name = value_or_query".
func parseLetStatement(input string) (*LetStatement, error) {
	// Strip "let " prefix
	rest := strings.TrimSpace(input[4:])

	// Find the = sign
	eqIdx := strings.Index(rest, "=")
	if eqIdx < 0 {
		return nil, fmt.Errorf("expected '=' in let statement")
	}

	// Guard against ==
	if eqIdx+1 < len(rest) && rest[eqIdx+1] == '=' {
		return nil, fmt.Errorf("expected '=' in let statement, got '=='")
	}

	name := unquoteBracketName(strings.TrimSpace(rest[:eqIdx]))
	if name == "" {
		return nil, fmt.Errorf("expected name before '=' in let statement")
	}

	valueStr := strings.TrimSpace(rest[eqIdx+1:])
	if valueStr == "" {
		return nil, fmt.Errorf("expected value after '=' in let statement")
	}

	if inner, ok := unwrapMaterialize(valueStr); ok {
		// materialize() only accepts a tabular argument, so a bare
		// name inside it (`let m = materialize(s)`) is a table.
		query, err := parseQuery(inner)
		if err != nil {
			return nil, fmt.Errorf("let %s: %w", name, err)
		}
		return &LetStatement{Name: name, Value: query}, nil
	}

	// view lambda: let V = view () { T | ... }. A parameterless view
	// binds like a tabular let and is additionally visible to union
	// wildcards; a view with parameters is an ordinary function.
	if body, params, ok := splitViewLambda(valueStr); ok {
		if strings.TrimSpace(params) == "" {
			query, err := parseQuery(body)
			if err != nil {
				return nil, fmt.Errorf("let %s: view: %w", name, err)
			}
			return &LetStatement{Name: name, Value: query, IsView: true}, nil
		}
		fn, ok, err := tryParseFunctionDef("(" + params + ") {" + body + "}")
		if !ok || err != nil {
			if err == nil {
				err = fmt.Errorf("malformed view")
			}
			return nil, fmt.Errorf("let %s: view: %w", name, err)
		}
		return &LetStatement{Name: name, Value: fn, IsView: true}, nil
	}

	// User-defined function: let f = (x: long) { x * 2 }
	if fn, ok, err := tryParseFunctionDef(valueStr); ok {
		if err != nil {
			return nil, fmt.Errorf("let %s: %w", name, err)
		}
		return &LetStatement{Name: name, Value: fn}, nil
	}

	// Determine if this is a tabular expression or scalar.
	// Heuristic: if it contains a pipe '|' at depth 0, it's tabular.
	// Also if it looks like a bare table name (identifier with no operators), treat as tabular.
	// datatable and table-valued functions (csv, json, ndjson, parquet, vortex) are
	// always tabular even without a pipe, so route them to the query parser directly.
	// A parenthesised tabular expression, `let x = ( T | where ... );`,
	// is the same expression: unwrap it.
	for strings.HasPrefix(valueStr, "(") && matchingClose(valueStr, 0, '(', ')') == len(valueStr)-1 {
		inner := strings.TrimSpace(valueStr[1 : len(valueStr)-1])
		if !(isTabularSource(inner) || isTabularExpression(inner)) {
			break
		}
		valueStr = inner
	}
	if isTabularSource(valueStr) || isTabularExpression(valueStr) {
		query, err := parseQuery(valueStr)
		if err != nil {
			return nil, fmt.Errorf("let %s: %w", name, err)
		}
		return &LetStatement{Name: name, Value: query}, nil
	}

	// Scalar expression
	expr, err := ParseExpr(valueStr)
	if err != nil {
		return nil, fmt.Errorf("let %s: %w", name, err)
	}
	return &LetStatement{Name: name, Value: &ScalarExpr{Expr: expr, Raw: valueStr}}, nil
}

// unquoteBracketName turns a bracket-quoted identifier (`['a b']`,
// `["a b"]`) into its bare name; other text is returned unchanged.
func unquoteBracketName(s string) string {
	if len(s) >= 4 && s[0] == '[' && s[len(s)-1] == ']' {
		inner := s[1 : len(s)-1]
		if q := inner[0]; (q == '\'' || q == '"') && inner[len(inner)-1] == q && len(inner) >= 2 {
			return inner[1 : len(inner)-1]
		}
	}
	return s
}

// splitViewLambda recognises "view (params) { body }" and returns the
// body and the parameter text.
func splitViewLambda(s string) (body, params string, ok bool) {
	lower := keywordFold(s)
	if !strings.HasPrefix(lower, "view") || len(s) == 4 || isIdentChar(s[4]) {
		return "", "", false
	}
	rest := strings.TrimSpace(s[4:])
	if !strings.HasPrefix(rest, "(") {
		return "", "", false
	}
	closeP := matchingClose(rest, 0, '(', ')')
	if closeP < 0 {
		return "", "", false
	}
	params = rest[1:closeP]
	tail := strings.TrimSpace(rest[closeP+1:])
	if !strings.HasPrefix(tail, "{") {
		return "", "", false
	}
	closeB := matchingClose(tail, 0, '{', '}')
	if closeB != len(tail)-1 {
		return "", "", false
	}
	return strings.TrimSpace(tail[1:closeB]), params, true
}

// matchingClose returns the index of the bracket closing the one at
// s[open], skipping quoted strings, or -1.
func matchingClose(s string, open int, l, r byte) int {
	depth := 0
	var quote byte
	for i := open; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == quote && !precededByOddBackslashes(s, i) {
				quote = 0
			}
			continue
		}
		switch ch {
		case '"', '\'':
			quote = ch
		case l:
			depth++
		case r:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// isIdentifier reports whether s is a plain KQL identifier.
func isIdentifier(s string) bool {
	if s == "" || !isIdentStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isIdentChar(s[i]) {
			return false
		}
	}
	return true
}

// isTabularExpression checks if a string looks like a tabular query (has pipes at depth 0).
func isTabularExpression(s string) bool {
	depth := 0
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inQuote != 0 {
			if ch == inQuote && !precededByOddBackslashes(s, i) {
				inQuote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inQuote = ch
			continue
		}
		if ch == '(' {
			depth++
		}
		if ch == ')' && depth > 0 {
			depth--
		}
		if ch == '|' && depth == 0 {
			return true
		}
	}
	return false
}

// isTabularSource reports whether the value string begins with a construct that
// is inherently tabular: a datatable literal or a table-valued function call
// (csv, json, ndjson, parquet, vortex). These must be parsed as queries even
// when no pipe operator is present.
func isTabularSource(s string) bool {
	lower := keywordFold(strings.TrimSpace(s))
	if strings.HasPrefix(lower, "datatable") {
		rest := lower[len("datatable"):]
		// Word boundary: next non-space char must be '(' so an identifier like
		// "datatables" is not misclassified.
		rest = strings.TrimLeft(rest, " \t")
		return strings.HasPrefix(rest, "(")
	}
	// range columnName from ... — real ADX's own example shows this
	// let-bound directly (let _data = range x from 1 to 100 step 1;),
	// so it needs the same recognition datatable already gets here.
	// Word boundary: next non-space char after "range" must NOT be an
	// identifier character, so a column or table literally named
	// "rangeX" isn't misclassified.
	// range(start, stop, step) is the scalar function, not the operator.
	if strings.HasPrefix(lower, "range") {
		rest := lower[len("range"):]
		if rest == "" || (!isIdentChar(rest[0]) && !strings.HasPrefix(strings.TrimLeft(rest, " \t"), "(")) {
			return true
		}
	}
	// union legs: `let n = union (A), (B);`
	if strings.HasPrefix(lower, "union ") {
		return true
	}
	// let T = print x=1;
	if strings.HasPrefix(lower, "print ") || lower == "print" {
		return true
	}
	// graph("Model" [, ...]) is a graph-valued source.
	if strings.HasPrefix(lower, "graph(") || strings.HasPrefix(lower, "graph (") {
		return true
	}
	return parseTableFunc(strings.TrimSpace(s)) != nil
}

// tryParseFunctionDef detects and parses a UDF lambda of the form
// (name: type, ...) { body-expr }. Returns ok=false when the value does not
// look like a lambda (so let parsing can fall through to tabular/scalar);
// returns ok=true with a non-nil error when it is lambda-shaped but invalid.
func tryParseFunctionDef(s string) (*FunctionDef, bool, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "(") || !strings.HasSuffix(s, "}") {
		return nil, false, nil
	}
	// Find the matching close paren of the parameter list
	depth := 0
	closeIdx := -1
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inQuote != 0 {
			if ch == inQuote && !precededByOddBackslashes(s, i) {
				inQuote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inQuote = ch
			continue
		}
		if ch == '(' {
			depth++
		}
		if ch == ')' {
			depth--
			if depth == 0 {
				closeIdx = i
				break
			}
		}
	}
	if closeIdx < 0 {
		return nil, false, nil
	}
	rest := strings.TrimSpace(s[closeIdx+1:])
	if !strings.HasPrefix(rest, "{") {
		// Parenthesized scalar expression, not a lambda
		return nil, false, nil
	}
	body := strings.TrimSpace(rest[1 : len(rest)-1])
	if body == "" {
		return nil, true, fmt.Errorf("function body is empty")
	}

	// Parse parameters: name: type, name: type, ...
	// Parameters: name:type, name:type = default, and tabular
	// T:(col:type, ...) or T:(*) -- the parser stored functions use.
	paramStr := strings.TrimSpace(s[1:closeIdx])
	params, err := ParseFunctionParams(paramStr)
	if err != nil {
		return nil, true, err
	}

	if lets, expr, ok := parseScalarBodyWithLets(body); ok {
		return &FunctionDef{Params: params, Body: expr, Lets: lets, ParamsText: paramStr, BodyText: body}, true, nil
	}
	if isTabularFunctionBody(body, params) {
		stmt, err := Parse(body)
		if err != nil {
			return nil, true, fmt.Errorf("function body: %w", err)
		}
		if !statementIsTabular(stmt) {
			return nil, true, fmt.Errorf("function body: not a tabular or scalar expression")
		}
		return &FunctionDef{Params: params, Tabular: true, ParamsText: paramStr, BodyText: body}, true, nil
	}
	bodyExpr, err := ParseExpr(body)
	if err != nil {
		return nil, true, fmt.Errorf("function body: %w", err)
	}
	// The body text is kept: a body that only calls a tabular function
	// (let g = (n:long) { f(1, n) }) is resolved as tabular when used as
	// a source.
	return &FunctionDef{Params: params, Body: bodyExpr, ParamsText: paramStr, BodyText: body}, true, nil
}

// parseScalarBodyWithLets recognises a scalar function body with local
// bindings: one or more `let name = scalar;` statements followed by a
// scalar expression (let pi = pi(); iff(x < pi, ...)). A tabular let or
// final statement leaves the body to the tabular path.
func parseScalarBodyWithLets(body string) ([]LocalLet, Expr, bool) {
	stmts := splitStatements(body)
	var parts []string
	for _, st := range stmts {
		if t := strings.TrimSpace(st); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) < 2 {
		return nil, nil, false
	}
	scalar := func(text string) (Expr, bool) {
		if isTabularSource(text) || isTabularExpression(text) || strings.HasPrefix(text, "(") && strings.HasSuffix(text, "}") {
			return nil, false
		}
		x, err := ParseExpr(text)
		if err != nil {
			return nil, false
		}
		return x, true
	}
	var lets []LocalLet
	for _, st := range parts[:len(parts)-1] {
		if !strings.HasPrefix(keywordFold(st), "let ") {
			return nil, nil, false
		}
		rest := strings.TrimSpace(st[len("let "):])
		eq := strings.IndexByte(rest, '=')
		if eq <= 0 || (eq+1 < len(rest) && rest[eq+1] == '=') {
			return nil, nil, false
		}
		name := strings.TrimSpace(rest[:eq])
		if !isIdentifier(name) {
			return nil, nil, false
		}
		x, ok := scalar(strings.TrimSpace(rest[eq+1:]))
		if !ok {
			return nil, nil, false
		}
		lets = append(lets, LocalLet{Name: name, Expr: x})
	}
	last, ok := scalar(parts[len(parts)-1])
	if !ok {
		return nil, nil, false
	}
	return lets, last, true
}

// isTabularFunctionBody decides whether a function body is tabular:
// it has let statements or a top-level pipe, starts with a tabular
// source (datatable, print, range, union, ...), or is a bare name that
// is not one of the function's own scalar parameters (a table).
// IsTabularFunctionBody reports whether a stored function body is a
// tabular expression (rather than a scalar one).
func IsTabularFunctionBody(body, paramsText string) bool {
	params, _ := ParseFunctionParams(paramsText)
	return isTabularFunctionBody(strings.TrimSpace(body), params)
}

func isTabularFunctionBody(body string, params []FunctionParam) bool {
	if isTabularSource(body) || isTabularExpression(body) {
		return true
	}
	if _, _, ok := parseScalarBodyWithLets(body); ok {
		return false
	}
	if len(splitStatements(body)) > 1 {
		return true
	}
	lower := keywordFold(body)
	for _, kw := range []string{"print ", "range ", "union ", "search ", "find "} {
		if strings.HasPrefix(lower, kw) {
			return true
		}
	}
	if isIdentifier(body) {
		for _, p := range params {
			if p.Name == body && !p.IsTabular {
				return false
			}
		}
		return true
	}
	if _, err := ParseExpr(body); err != nil {
		// Not a scalar expression; a tabular call such as Other(1).
		return true
	}
	return false
}

func statementIsTabular(stmt Statement) bool {
	switch s := stmt.(type) {
	case *Query:
		return true
	case *CompoundStatement:
		_, ok := s.Final.(*Query)
		return ok
	}
	return false
}

// splitStatements splits input on semicolons, respecting strings,
// parentheses, AND braces.
//
// depth previously tracked only (...) nesting, not {...} at all —
// found live, not hypothetical (a different model's testing, Kimi):
// .create-or-alter function T() { let s = "open"; Body | where ... }
// silently split on the ; INSIDE the function body's own { } block,
// since that ; sat at depth==0 as far as this function was concerned.
// Two compounding failures resulted from the single wrong split:
// the fragment before the semicolon (".create-or-alter function T() {
// let s = \"open\"") was silently discarded entirely (this function's
// own caller, Parse, treats any non-"let "-prefixed piece as THE
// final statement, so a later piece unconditionally overwrote it,
// with no error at all -- the let binding simply evaporated, later
// surfacing as "column s not found" when the body tried to reference
// it), while the fragment after it ("Body | where ... }") kept the
// function body's own closing brace glued onto its last operator's
// text, surfacing as "unknown operator: \"count }\"" or similar.
// Fixed by tracking { and } under the SAME depth counter as ( and ) —
// for the purpose of "is this ; inside some nested construct at all",
// which bracket kind is nesting doesn't matter, only whether we're
// nested at all.
func splitStatements(s string) []string {
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
		if ch == ';' && depth == 0 {
			parts = append(parts, current.String())
			current.Reset()
			continue
		}
		current.WriteByte(ch)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// --- Management Command Parsing ---

func parseCommand(input string) (Statement, error) {
	lower := keywordFold(input)

	// .show tables
	if lower == ".help" || lower == "help" {
		return &HelpCmd{}, nil
	}

	if lower == ".show tables" {
		return &ShowTablesCmd{}, nil
	}

	if st, ok, err := parseGraphModelCommand(input); ok {
		return st, err
	}

	// .show database
	if lower == ".show database" {
		return &ShowDatabaseCmd{}, nil
	}

	// .show table T extents
	if strings.HasPrefix(lower, ".show table ") && strings.HasSuffix(lower, " extents") {
		name := strings.TrimSpace(input[len(".show table ") : len(input)-len(" extents")])
		if name == "" {
			return nil, fmt.Errorf("expected table name in .show table ... extents")
		}
		return &ShowTableExtentsCmd{TableName: name}, nil
	}

	// .drop extent <guid>
	if strings.HasPrefix(lower, ".drop extent ") {
		// .drop extent ExtentId [from TableName]
		rest := strings.TrimSpace(input[len(".drop extent "):])
		cmd := &DropExtentCmd{ExtentID: rest}
		if i := findKeyword(rest, " from "); i >= 0 {
			cmd.ExtentID = strings.TrimSpace(rest[:i])
			cmd.TableName = strings.TrimSpace(rest[i+len(" from "):])
			if !isIdentifier(cmd.TableName) {
				return nil, fmt.Errorf(".drop extent: invalid table name %q", cmd.TableName)
			}
		}
		return cmd, nil
	}

	// .drop table T
	if strings.HasPrefix(lower, ".drop table ") {
		name := strings.TrimSpace(input[len(".drop table "):])
		return &DropTableCmd{TableName: name}, nil
	}

	// .create-merge table T (...)
	if strings.HasPrefix(lower, ".create-merge table ") {
		return parseCreateTable(input[len(".create-merge table "):], true)
	}

	if strings.HasPrefix(lower, ".alter-merge table ") {
		return parseAlterMergeTable(input[len(".alter-merge table "):])
	}

	// .create table T (...)
	if strings.HasPrefix(lower, ".create table ") {
		return parseCreateTable(input[len(".create table "):], false)
	}

	// .ingest csv into table T from "path"
	if strings.HasPrefix(lower, ".ingest csv into table ") {
		rest := input[len(".ingest csv into table "):]
		fromIdx := strings.Index(keywordFold(rest), " from ")
		if fromIdx < 0 {
			return nil, fmt.Errorf(".ingest csv requires 'from path'")
		}
		tableName := strings.TrimSpace(rest[:fromIdx])
		pathStr := strings.TrimSpace(rest[fromIdx+6:])
		// Strip quotes
		pathStr = strings.Trim(pathStr, "\"'")
		return &IngestCSVCmd{TableName: tableName, FilePath: pathStr}, nil
	}

	// .ingest inline into table T <| data
	if strings.HasPrefix(lower, ".ingest inline into table ") {
		rest := input[len(".ingest inline into table "):]
		idx := strings.Index(rest, "<|")
		if idx < 0 {
			return nil, fmt.Errorf(".ingest inline requires <| delimiter")
		}
		tableName := strings.TrimSpace(rest[:idx])
		data := strings.TrimSpace(rest[idx+2:])
		return &IngestInlineCmd{TableName: tableName, Data: data}, nil
	}

	// .set T <| query
	// .set-or-append T <| query — appends the query's result rows as a
	// new extent. Checked before ".set " since it's a longer, more
	// specific prefix.
	if strings.HasPrefix(lower, ".set-or-append ") {
		rest := input[len(".set-or-append "):]
		idx := strings.Index(rest, "<|")
		if idx < 0 {
			return nil, fmt.Errorf(".set-or-append requires <| delimiter")
		}
		tableName := strings.TrimSpace(rest[:idx])
		queryStr := strings.TrimSpace(rest[idx+2:])
		query, err := parseQuery(queryStr)
		if err != nil {
			return nil, fmt.Errorf(".set-or-append query: %w", err)
		}
		return &SetOrAppendCmd{TableName: tableName, Query: query.(*Query)}, nil
	}

	// .chunk-file "path" into T — reads a markdown file, splits it into
	// paragraph-level chunks (blank-line delimited, heading-trail
	// tagged, oversized blocks auto-split), writes into T. See
	// engine/chunk_file.go.
	if strings.HasPrefix(lower, ".chunk-file ") {
		rest := strings.TrimSpace(input[len(".chunk-file "):])
		if len(rest) == 0 || rest[0] != '"' {
			return nil, fmt.Errorf(".chunk-file: expected a quoted path")
		}
		ep := &exprParser{input: rest}
		pathExpr, err := ep.parseString()
		if err != nil {
			return nil, fmt.Errorf(".chunk-file: parsing path: %w", err)
		}
		pathLit, ok := pathExpr.(*Literal)
		if !ok {
			return nil, fmt.Errorf(".chunk-file: expected a quoted string path")
		}
		path, _ := pathLit.Value.(string)

		remainder := strings.TrimSpace(rest[ep.pos:])
		if !strings.HasPrefix(keywordFold(remainder), "into ") {
			return nil, fmt.Errorf(".chunk-file: expected 'into TableName' after the path, got %q", remainder)
		}
		tableName := strings.TrimSpace(remainder[len("into "):])
		if tableName == "" {
			return nil, fmt.Errorf(".chunk-file: missing table name after 'into'")
		}
		cmd := &ChunkFileCmd{Path: path, TableName: tableName}
		// .chunk-file "path" into T with (idprefix="key")
		if sp := strings.IndexAny(tableName, " \t("); sp > 0 {
			cmd.TableName = tableName[:sp]
			props := strings.TrimSpace(tableName[sp:])
			lp := keywordFold(props)
			if !strings.HasPrefix(lp, "with (") && !strings.HasPrefix(lp, "with(") {
				return nil, fmt.Errorf(".chunk-file into %s: expected with (...) after the table name, got %q", cmd.TableName, props)
			}
			open := strings.Index(props, "(")
			closeIdx := findMatchingParen(props, open)
			if closeIdx < 0 || strings.TrimSpace(props[closeIdx+1:]) != "" {
				return nil, fmt.Errorf(".chunk-file into %s: malformed with (...) clause", cmd.TableName)
			}
			for _, kv := range splitAndTrim(props[open+1:closeIdx], ',') {
				eq := strings.Index(kv, "=")
				if eq < 0 {
					return nil, fmt.Errorf(".chunk-file into %s: malformed property %q, expected name=value", cmd.TableName, kv)
				}
				key := strings.ToLower(strings.TrimSpace(kv[:eq]))
				val := strings.Trim(strings.TrimSpace(kv[eq+1:]), `"'`)
				switch key {
				case "idprefix":
					if val == "" || strings.Contains(val, ":") {
						return nil, fmt.Errorf(".chunk-file into %s: idprefix must be non-empty and contain no ':'", cmd.TableName)
					}
					cmd.IdPrefix = val
				default:
					return nil, fmt.Errorf(".chunk-file into %s: unknown property %q (supported: idprefix)", cmd.TableName, key)
				}
			}
		}
		return cmd, nil
	}

	// .embed-into T <| query — bulk-embeds a Text column via Ollama's
	// batch API in one HTTP round trip per batch instead of one per
	// row, then writes (Id, Embedding, Model, Provenance) in a single
	// flushBatch. See engine/embed_bulk.go.
	if strings.HasPrefix(lower, ".embed-into ") {
		rest := input[len(".embed-into "):]
		idx := strings.Index(rest, "<|")
		if idx < 0 {
			return nil, fmt.Errorf(".embed-into requires <| delimiter")
		}
		target := strings.TrimSpace(rest[:idx])
		queryStr := strings.TrimSpace(rest[idx+2:])
		query, err := parseQuery(queryStr)
		if err != nil {
			return nil, fmt.Errorf(".embed-into query: %w", err)
		}
		cmd := &EmbedIntoCmd{TableName: target, Query: query.(*Query)}
		// .embed-into T with (model="name") <| query
		if sp := strings.IndexAny(target, " \t("); sp > 0 {
			cmd.TableName = target[:sp]
			props := strings.TrimSpace(target[sp:])
			lp := keywordFold(props)
			if !strings.HasPrefix(lp, "with (") && !strings.HasPrefix(lp, "with(") {
				return nil, fmt.Errorf(".embed-into %s: expected with (...) before <|, got %q", cmd.TableName, props)
			}
			open := strings.Index(props, "(")
			closeIdx := findMatchingParen(props, open)
			if closeIdx < 0 || strings.TrimSpace(props[closeIdx+1:]) != "" {
				return nil, fmt.Errorf(".embed-into %s: malformed with (...) clause", cmd.TableName)
			}
			for _, kv := range splitAndTrim(props[open+1:closeIdx], ',') {
				eq := strings.Index(kv, "=")
				if eq < 0 {
					return nil, fmt.Errorf(".embed-into %s: malformed property %q, expected name=value", cmd.TableName, kv)
				}
				key := strings.ToLower(strings.TrimSpace(kv[:eq]))
				val := strings.Trim(strings.TrimSpace(kv[eq+1:]), `"'`)
				switch key {
				case "model":
					cmd.Model = val
				default:
					return nil, fmt.Errorf(".embed-into %s: unknown property %q (supported: model)", cmd.TableName, key)
				}
			}
		}
		return cmd, nil
	}

	if strings.HasPrefix(lower, ".set ") {
		rest := input[len(".set "):]
		idx := strings.Index(rest, "<|")
		if idx < 0 {
			return nil, fmt.Errorf(".set requires <| delimiter")
		}
		tableName := strings.TrimSpace(rest[:idx])
		queryStr := strings.TrimSpace(rest[idx+2:])
		query, err := parseQuery(queryStr)
		if err != nil {
			return nil, fmt.Errorf(".set query: %w", err)
		}
		return &SetCmd{TableName: tableName, Query: query.(*Query)}, nil
	}

	// .compact table T [where <predicate>] — discovery mode only.
	// Merges T's current extents into one, optionally dropping rows
	// the predicate excludes (see CompactTableCmd's doc comment).
	// Genuinely unsafe under concurrent access unlike every other
	// discovery-mode operation in this codebase — see engine/compact.go
	// for why, and run only when the scope is quiescent (no other
	// session reading or writing this table).
	if strings.HasPrefix(lower, ".compact table ") {
		rest := strings.TrimSpace(input[len(".compact table "):])
		if rest == "" {
			return nil, fmt.Errorf(".compact table: missing table name")
		}
		// " where " must be matched case-insensitively but split on the
		// ORIGINAL (not lowercased) rest, so the predicate text itself
		// keeps its original casing (string literals, etc.) — the same
		// approach the pipeline `| where` segment split already uses.
		name := rest
		var whereExpr Expr
		restLower := strings.ToLower(rest)
		if idx := strings.Index(restLower, " where "); idx >= 0 {
			name = strings.TrimSpace(rest[:idx])
			predStr := rest[idx+len(" where "):]
			expr, err := ParseExpr(predStr)
			if err != nil {
				return nil, fmt.Errorf(".compact table: where: %w", err)
			}
			whereExpr = expr
		}
		if name == "" {
			return nil, fmt.Errorf(".compact table: missing table name")
		}
		var opts map[string]string
		if idx := strings.Index(strings.ToLower(name), " with "); idx >= 0 {
			optStr := strings.TrimSpace(name[idx+len(" with "):])
			name = strings.TrimSpace(name[:idx])
			if !strings.HasPrefix(optStr, "(") || !strings.HasSuffix(optStr, ")") {
				return nil, fmt.Errorf(".compact table: expected with (name = value, ...)")
			}
			opts = map[string]string{}
			for _, kv := range strings.Split(optStr[1:len(optStr)-1], ",") {
				eq := strings.IndexByte(kv, '=')
				if eq <= 0 {
					return nil, fmt.Errorf(".compact table: bad option %q", strings.TrimSpace(kv))
				}
				k := strings.ToLower(strings.TrimSpace(kv[:eq]))
				v := strings.Trim(strings.TrimSpace(kv[eq+1:]), `"'`)
				opts[k] = strings.ToLower(v)
			}
			for k, v := range opts {
				if k != "vectors" || v != "compact" {
					return nil, fmt.Errorf(".compact table: unknown option %s=%s (supported: vectors=compact)", k, v)
				}
			}
		}
		return &CompactTableCmd{TableName: name, Where: whereExpr, Options: opts}, nil
	}

	// .gc table T — physically removes .superseded files for T, left
	// behind by .compact. Safe to run at any time (see engine/compact.go).
	if strings.HasPrefix(lower, ".gc table ") {
		name := strings.TrimSpace(input[len(".gc table "):])
		if name == "" {
			return nil, fmt.Errorf(".gc table: missing table name")
		}
		return &GCTableCmd{TableName: name}, nil
	}

	// .compact database / .gc database — every table, INCLUDING
	// _Dictionaries (hidden from .show tables by design, but not from
	// this — see Catalog.ListAllTables's doc comment for why that
	// distinction exists and the real incident that motivated it).
	if lower == ".compact database" {
		return &CompactDatabaseCmd{}, nil
	}
	if lower == ".gc database" {
		return &GCDatabaseCmd{}, nil
	}

	// .merge table T extents
	if strings.HasPrefix(lower, ".merge table ") && strings.HasSuffix(lower, " extents") {
		name := strings.TrimSpace(input[len(".merge table ") : len(input)-len(" extents")])
		if name == "" {
			return nil, fmt.Errorf("expected table name in .merge table ... extents")
		}
		return &MergeExtentsCmd{TableName: name}, nil
	}

	// .create-or-alter function / .create function [ifnotexists] — see
	// CreateFunctionCmd's doc comment. Checked with a space after
	// "function" required, so this doesn't accidentally prefix-match
	// something like a hypothetical future ".create functionality"
	// command.
	if strings.HasPrefix(lower, ".create-or-alter function ") {
		return parseCreateFunction(input[len(".create-or-alter function "):], true, false)
	}
	if strings.HasPrefix(lower, ".create function ifnotexists ") {
		return parseCreateFunction(input[len(".create function ifnotexists "):], false, true)
	}
	if strings.HasPrefix(lower, ".create function ") {
		return parseCreateFunction(input[len(".create function "):], false, false)
	}

	if lower == ".show functions" {
		return &ShowFunctionsCmd{}, nil
	}
	if strings.HasPrefix(lower, ".show function ") {
		name := strings.TrimSpace(input[len(".show function "):])
		// Real ADX allows "with (PropertyName=PropertyValue)" here
		// (e.g. ShowObfuscatedStrings) — not built in this first
		// version; strip and ignore a trailing with(...) clause rather
		// than error, so the common case (just the name) works and an
		// unused property clause is silently a no-op instead of a hard
		// failure over an option this implementation doesn't need yet.
		if idx := strings.Index(keywordFold(name), " with "); idx >= 0 {
			name = strings.TrimSpace(name[:idx])
		}
		if name == "" {
			return nil, fmt.Errorf(".show function: missing function name")
		}
		return &ShowFunctionCmd{Name: name}, nil
	}

	// .drop function Name  |  .drop functions (A, B, C)
	// Real ADX gives these genuinely different semantics (verified,
	// not assumed): singular errors if the function doesn't exist;
	// plural silently tolerates missing ones. ExplicitPlural tracks
	// which FORM was used, not just the resulting name count, since a
	// plural-form call naming exactly one function still gets plural
	// (tolerant) semantics in real ADX.
	if strings.HasPrefix(lower, ".drop function ") {
		name := strings.TrimSpace(input[len(".drop function "):])
		if name == "" {
			return nil, fmt.Errorf(".drop function: missing function name")
		}
		return &DropFunctionCmd{Names: []string{name}, ExplicitPlural: false}, nil
	}
	if strings.HasPrefix(lower, ".drop functions ") {
		rest := strings.TrimSpace(input[len(".drop functions "):])
		rest = strings.TrimPrefix(rest, "(")
		rest = strings.TrimSuffix(rest, ")")
		var names []string
		for _, part := range strings.Split(rest, ",") {
			if n := strings.TrimSpace(part); n != "" {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			return nil, fmt.Errorf(".drop functions: missing function name(s)")
		}
		return &DropFunctionCmd{Names: names, ExplicitPlural: true}, nil
	}

	// .create [ifnotexists] materialized-view [with (...)] Name on table Source { Query }
	if strings.HasPrefix(lower, ".create ifnotexists materialized-view ") {
		return parseCreateMaterializedView(input[len(".create ifnotexists materialized-view "):], true)
	}
	if strings.HasPrefix(lower, ".create materialized-view ") {
		return parseCreateMaterializedView(input[len(".create materialized-view "):], false)
	}
	if lower == ".show materialized-views" {
		return &ShowMaterializedViewsCmd{}, nil
	}
	if strings.HasPrefix(lower, ".drop materialized-view ") {
		name := strings.TrimSpace(input[len(".drop materialized-view "):])
		if name == "" {
			return nil, fmt.Errorf(".drop materialized-view: missing name")
		}
		return &DropMaterializedViewCmd{Name: name}, nil
	}

	return nil, fmt.Errorf("unknown command: %s", input)
}

// parseCreateMaterializedView parses:
//
//	[with (docstring = "...", folder = "...")] Name on table SourceTable { Query }
//
// Verified against real ADX's own .create materialized-view syntax
// before adopting this shape. Query-shape validation (single
// summarize, last operator, only supported aggregate functions) is
// NOT done here — this function stays purely syntactic, matching
// every other command in this parser; see
// engine/materialized_views.go's validateMaterializedViewQuery for the
// semantic checks, which need the source table's real schema and so
// can't run at parse time at all.
func parseCreateMaterializedView(rest string, ifNotExists bool) (Statement, error) {
	rest = strings.TrimSpace(rest)
	cmd := &CreateMaterializedViewCmd{IfNotExists: ifNotExists}

	if strings.HasPrefix(keywordFold(rest), "with (") || strings.HasPrefix(keywordFold(rest), "with(") {
		openIdx := strings.Index(rest, "(")
		closeIdx := findMatchingParen(rest, openIdx)
		if closeIdx < 0 {
			return nil, fmt.Errorf("create materialized-view: unterminated with(...) clause")
		}
		props := rest[openIdx+1 : closeIdx]
		for _, kv := range splitAndTrim(props, ',') {
			eqIdx := strings.Index(kv, "=")
			if eqIdx < 0 {
				return nil, fmt.Errorf("create materialized-view: malformed property %q, expected name=value", kv)
			}
			key := strings.ToLower(strings.TrimSpace(kv[:eqIdx]))
			val := strings.TrimSpace(kv[eqIdx+1:])
			val = strings.Trim(val, `"'`)
			switch key {
			case "docstring":
				cmd.DocString = val
			case "folder":
				cmd.Folder = val
			case "backfill", "effectivedatetime", "lookback", "lookback_column",
				"autoupdateschema", "dimensiontables", "allowmaterializedviewswithoutrowlevelsecurity":
				// Real ADX properties this first version doesn't
				// implement — accepted and silently ignored, matching
				// the same "don't fail purely over an unsupported
				// option" choice already made for .create function's
				// view/skipvalidation properties.
			default:
				return nil, fmt.Errorf("create materialized-view: unknown property %q", key)
			}
		}
		rest = strings.TrimSpace(rest[closeIdx+1:])
	}

	nameEnd := strings.IndexAny(rest, " 	")
	if nameEnd < 0 {
		return nil, fmt.Errorf("create materialized-view: expected Name on table Source { Query }")
	}
	cmd.Name = strings.TrimSpace(rest[:nameEnd])
	if cmd.Name == "" {
		return nil, fmt.Errorf("create materialized-view: missing name")
	}
	rest = strings.TrimSpace(rest[nameEnd:])

	const onTablePrefix = "on table "
	if !strings.HasPrefix(keywordFold(rest), onTablePrefix) {
		return nil, fmt.Errorf("create materialized-view %q: expected 'on table SourceTable'", cmd.Name)
	}
	rest = strings.TrimSpace(rest[len(onTablePrefix):])

	sourceEnd := strings.IndexAny(rest, " 	{")
	if sourceEnd < 0 {
		return nil, fmt.Errorf("create materialized-view %q: expected source table name", cmd.Name)
	}
	cmd.SourceTable = strings.TrimSpace(rest[:sourceEnd])
	if cmd.SourceTable == "" {
		return nil, fmt.Errorf("create materialized-view %q: missing source table name", cmd.Name)
	}
	rest = strings.TrimSpace(rest[sourceEnd:])

	if !strings.HasPrefix(rest, "{") {
		return nil, fmt.Errorf("create materialized-view %q: expected { Query }", cmd.Name)
	}
	closeIdx := findMatchingBrace(rest, 0)
	if closeIdx < 0 {
		return nil, fmt.Errorf("create materialized-view %q: unterminated { Query }", cmd.Name)
	}
	queryText := strings.TrimSpace(rest[1:closeIdx])
	if queryText == "" {
		return nil, fmt.Errorf("create materialized-view %q: empty query", cmd.Name)
	}

	stmt, err := Parse(queryText)
	if err != nil {
		return nil, fmt.Errorf("create materialized-view %q: query does not parse: %w", cmd.Name, err)
	}
	q, ok := stmt.(*Query)
	if !ok {
		return nil, fmt.Errorf("create materialized-view %q: body must be a tabular query, not a management command", cmd.Name)
	}
	cmd.Query = q
	cmd.QueryText = queryText

	return cmd, nil
}

// parseCreateFunction parses the shared shape behind .create-or-alter
// function and .create function [ifnotexists]:
//
//	[with (docstring = "...", folder = "...")] Name() { body }
//
// Parameterless only in this first version — an empty parameter list
// is required; anything else is a clear, immediate error rather than
// silently ignored parameters.
func parseCreateFunction(rest string, orAlter, ifNotExists bool) (Statement, error) {
	rest = strings.TrimSpace(rest)

	cmd := &CreateFunctionCmd{OrAlter: orAlter, IfNotExists: ifNotExists}

	if strings.HasPrefix(keywordFold(rest), "with (") || strings.HasPrefix(keywordFold(rest), "with(") {
		openIdx := strings.Index(rest, "(")
		closeIdx := findMatchingParen(rest, openIdx)
		if closeIdx < 0 {
			return nil, fmt.Errorf("create function: unterminated with(...) clause")
		}
		props := rest[openIdx+1 : closeIdx]
		for _, kv := range splitAndTrim(props, ',') {
			eqIdx := strings.Index(kv, "=")
			if eqIdx < 0 {
				return nil, fmt.Errorf("create function: malformed property %q, expected name=value", kv)
			}
			key := strings.ToLower(strings.TrimSpace(kv[:eqIdx]))
			val := strings.TrimSpace(kv[eqIdx+1:])
			val = strings.Trim(val, `"'`)
			switch key {
			case "docstring":
				cmd.DocString = val
			case "folder":
				cmd.Folder = val
			case "view", "skipvalidation":
				// Real ADX properties this first version doesn't
				// implement — accepted and silently ignored rather
				// than rejected, so a definition copied from a real
				// ADX script doesn't fail purely over an unsupported
				// option it doesn't strictly need here.
			default:
				return nil, fmt.Errorf("create function: unknown property %q", key)
			}
		}
		rest = strings.TrimSpace(rest[closeIdx+1:])
	}

	nameEnd := strings.IndexAny(rest, "( 	")
	if nameEnd < 0 {
		return nil, fmt.Errorf("create function: expected Name() { body }")
	}
	cmd.Name = strings.TrimSpace(rest[:nameEnd])
	if cmd.Name == "" {
		return nil, fmt.Errorf("create function: missing function name")
	}
	rest = strings.TrimSpace(rest[nameEnd:])

	if !strings.HasPrefix(rest, "(") {
		return nil, fmt.Errorf("create function %q: expected (parameters) after function name", cmd.Name)
	}
	paramCloseIdx := findMatchingParen(rest, 0)
	if paramCloseIdx < 0 {
		return nil, fmt.Errorf("create function %q: unterminated parameter list", cmd.Name)
	}
	paramText := rest[1:paramCloseIdx]
	params, paramErr := ParseFunctionParams(paramText)
	if paramErr != nil {
		return nil, fmt.Errorf("create function %q: %w", cmd.Name, paramErr)
	}
	cmd.Parameters = params
	cmd.ParametersText = strings.TrimSpace(paramText)
	rest = strings.TrimSpace(rest[paramCloseIdx+1:])

	if !strings.HasPrefix(rest, "{") {
		return nil, fmt.Errorf("create function %q: expected { body }", cmd.Name)
	}
	closeIdx := findMatchingBrace(rest, 0)
	if closeIdx < 0 {
		return nil, fmt.Errorf("create function %q: unterminated { body }", cmd.Name)
	}
	cmd.Body = strings.TrimSpace(rest[1:closeIdx])
	if cmd.Body == "" {
		return nil, fmt.Errorf("create function %q: empty body", cmd.Name)
	}

	// Validate the body parses NOW, at definition time — matching real
	// ADX's own default (skipvalidation=true opts out; not offered in
	// this first version, so validation always runs). Catches a typo
	// in the definition immediately rather than deferring the failure
	// to whenever the function is first called, possibly much later
	// and in a confusing context.
	if _, err := Parse(cmd.Body); err != nil {
		return nil, fmt.Errorf("create function %q: body does not parse: %w", cmd.Name, err)
	}

	return cmd, nil
}

// findMatchingBrace finds the index of the '}' matching the '{' at
// s[start] (which must itself be '{'), respecting quoted strings so a
// literal brace character inside a string doesn't miscount depth.
// Separate from findMatchingBracket ([/]), deliberately — kept
// distinct rather than generalizing the existing bracket-matcher to
// avoid any risk to its own, already-working callers.
func findMatchingBrace(s string, start int) int {
	depth := 0
	inStr := false
	for i := start; i < len(s); i++ {
		if s[i] == '"' && !precededByOddBackslashes(s, i) {
			inStr = !inStr
		}
		if inStr {
			continue
		}
		if s[i] == '{' {
			depth++
		} else if s[i] == '}' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// parseCreateTable parses "TableName (col1: type1, col2: type2, ...)"
// parseAlterMergeTable parses .alter-merge table T (col1:type1, ...)
// [with (docstring=..., folder=...)] — verified against real ADX's
// own .alter-merge table syntax before adopting this shape. Semantic
// checks (table must already exist, no type change for an existing
// column) happen at execution time (engine/alter_table.go), not
// here — parsing stays purely syntactic, matching every other command
// in this parser.
func parseAlterMergeTable(rest string) (Statement, error) {
	rest = strings.TrimSpace(rest)

	parenIdx := strings.Index(rest, "(")
	if parenIdx < 0 {
		return nil, fmt.Errorf("alter-merge table: expected (column definitions)")
	}
	tableName := strings.TrimSpace(rest[:parenIdx])
	if tableName == "" {
		return nil, fmt.Errorf("alter-merge table: expected table name")
	}

	closeIdx := findMatchingParen(rest, parenIdx)
	if closeIdx < 0 {
		return nil, fmt.Errorf("alter-merge table %q: expected closing ) in column definitions", tableName)
	}
	schema, err := parseColumnDefs(rest[parenIdx+1 : closeIdx])
	if err != nil {
		return nil, err
	}

	cmd := &AlterMergeTableCmd{TableName: tableName, NewColumns: *schema}

	afterCols := strings.TrimSpace(rest[closeIdx+1:])
	lowerAfter := strings.ToLower(afterCols)
	if strings.HasPrefix(lowerAfter, "with (") || strings.HasPrefix(lowerAfter, "with(") {
		wOpenIdx := strings.Index(afterCols, "(")
		wCloseIdx := findMatchingParen(afterCols, wOpenIdx)
		if wCloseIdx < 0 {
			return nil, fmt.Errorf("alter-merge table %q: unterminated with(...) clause", tableName)
		}
		for _, kv := range splitAndTrim(afterCols[wOpenIdx+1:wCloseIdx], ',') {
			eqIdx := strings.Index(kv, "=")
			if eqIdx < 0 {
				return nil, fmt.Errorf("alter-merge table %q: malformed property %q, expected name=value", tableName, kv)
			}
			key := strings.ToLower(strings.TrimSpace(kv[:eqIdx]))
			val := strings.TrimSpace(kv[eqIdx+1:])
			val = strings.Trim(val, `"'`)
			switch key {
			case "docstring":
				cmd.DocString = val
			case "folder":
				cmd.Folder = val
			default:
				return nil, fmt.Errorf("alter-merge table %q: unknown property %q", tableName, key)
			}
		}
	}

	return cmd, nil
}

func parseCreateTable(rest string, isMerge bool) (Statement, error) {
	rest = strings.TrimSpace(rest)

	// Find opening parenthesis
	parenIdx := strings.Index(rest, "(")
	if parenIdx < 0 {
		return nil, fmt.Errorf("expected (column definitions) after table name")
	}

	tableName := strings.TrimSpace(rest[:parenIdx])
	if tableName == "" {
		return nil, fmt.Errorf("expected table name")
	}

	// Find the column-defs' OWN closing parenthesis via matching, not
	// strings.LastIndex — a trailing with(...) clause (added for the
	// notimereceived property) has its own closing paren that would
	// otherwise be mistaken for the column list's, silently absorbing
	// "with (notimereceived=true)" into colDefs as a bogus extra
	// "column".
	closeIdx := findMatchingParen(rest, parenIdx)
	if closeIdx < 0 {
		return nil, fmt.Errorf("expected closing ) in column definitions")
	}

	colDefs := rest[parenIdx+1 : closeIdx]
	schema, err := parseColumnDefs(colDefs)
	if err != nil {
		return nil, err
	}

	// Optional trailing with (...) — currently only notimereceived,
	// but kept as a general key=value block (matching .create function
	// and .create materialized-view's own with(...) shape) rather than
	// a single dedicated flag, so a second table-level property later
	// doesn't need its own new syntax.
	noTimeReceived := false
	afterCols := strings.TrimSpace(rest[closeIdx+1:])
	lowerAfter := strings.ToLower(afterCols)
	if strings.HasPrefix(lowerAfter, "with (") || strings.HasPrefix(lowerAfter, "with(") {
		wOpenIdx := strings.Index(afterCols, "(")
		wCloseIdx := findMatchingParen(afterCols, wOpenIdx)
		if wCloseIdx < 0 {
			return nil, fmt.Errorf("create table %q: unterminated with(...) clause", tableName)
		}
		for _, kv := range splitAndTrim(afterCols[wOpenIdx+1:wCloseIdx], ',') {
			eqIdx := strings.Index(kv, "=")
			if eqIdx < 0 {
				return nil, fmt.Errorf("create table %q: malformed property %q, expected name=value", tableName, kv)
			}
			key := strings.ToLower(strings.TrimSpace(kv[:eqIdx]))
			val := strings.ToLower(strings.TrimSpace(kv[eqIdx+1:]))
			switch key {
			case "notimereceived":
				noTimeReceived = val == "true"
			default:
				return nil, fmt.Errorf("create table %q: unknown property %q", tableName, key)
			}
		}
	}

	if isMerge {
		return &CreateMergeTableCmd{TableName: tableName, Schema: *schema}, nil
	}
	return &CreateTableCmd{TableName: tableName, Schema: *schema, NoTimeReceived: noTimeReceived}, nil
}

// parseColumnDefs parses "col1: type1, col2: type2, ..."
func parseColumnDefs(s string) (*types.Schema, error) {
	parts := splitRespectingParens(s, ',')
	schema := &types.Schema{}

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		colonIdx := strings.LastIndex(part, ":") // type names have no colon; a quoted name may
		if colonIdx < 0 {
			return nil, fmt.Errorf("expected 'name: type' in column definition, got %q", part)
		}

		name := unquoteIdent(part[:colonIdx])
		typStr := strings.TrimSpace(part[colonIdx+1:])

		typ, err := types.ParseType(typStr)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", name, err)
		}

		schema.Columns = append(schema.Columns, types.Column{Name: name, Type: typ})
	}

	if len(schema.Columns) == 0 {
		return nil, fmt.Errorf("no columns defined")
	}
	return schema, nil
}

// --- Query Parsing ---

func parseQuery(input string) (Statement, error) {
	// Split on pipe, respecting strings and parentheses
	segments := splitPipe(input)
	if len(segments) == 0 {
		return nil, fmt.Errorf("empty query")
	}

	// First segment is the table name
	source := strings.TrimSpace(segments[0])
	if source == "" {
		return nil, fmt.Errorf("expected table name")
	}
	source = normalizeLeadingKeywordWhitespace(source)

	// materialize(Expr): a tabular let is already evaluated once and
	// cached, so materialize is the expression itself. Its own
	// operators continue after the inner query's.
	inner, ok := unwrapMaterialize(source)
	if !ok && strings.HasPrefix(source, "(") && matchingClose(source, 0, '(', ')') == len(source)-1 {
		// a parenthesised tabular expression: (T | where ...) | count
		inner, ok = strings.TrimSpace(source[1:len(source)-1]), true
	}
	if ok {
		stmt, err := parseQuery(inner)
		if err != nil {
			return nil, fmt.Errorf("materialize: %w", err)
		}
		q, ok := stmt.(*Query)
		if !ok {
			return nil, fmt.Errorf("materialize: argument must be a tabular expression")
		}
		for i, seg := range segments[1:] {
			seg = strings.TrimSpace(seg)
			if seg == "" {
				continue
			}
			op, err := parseOperator(seg)
			if err != nil {
				return nil, fmt.Errorf("operator %d: %w", i+1, err)
			}
			q.Operators = append(q.Operators, op)
		}
		return q, nil
	}

	// Standalone union: "union T1, T2, T3 | where ..."
	if strings.HasPrefix(keywordFold(source), "union ") {
		return parseStandaloneUnion(source[6:], segments[1:])
	}

	// print expr1, expr2, ...
	if strings.HasPrefix(keywordFold(source), "print ") {
		return parsePrintStatement(source[6:], segments[1:])
	}

	// search "term" or search in (T1, T2) "term"
	if strings.HasPrefix(keywordFold(source), "search ") {
		return parseSearchStatement(source[7:], segments[1:])
	}

	// find [withsource=X] [in (T1, T2)] where Predicate [project ...]
	// or find Predicate [project ...] (no "where" in this shorter form)
	if strings.HasPrefix(keywordFold(source), "find ") {
		return parseFindStatement(source[5:], segments[1:])
	}

	// datatable (Col1: type, Col2: type) [val1, val2, ...]
	if strings.HasPrefix(keywordFold(source), "datatable") {
		return parseDataTableStatement(source, segments[1:])
	}

	// range columnName from start to stop step step — verified against
	// real ADX's own range operator docs before adopting this shape.
	// Word boundary matches isTabularSource's own check just above in
	// this file: the next character after "range" must not be an
	// identifier character, so a real table literally named
	// "rangeEvents" isn't misparsed as this operator.
	if rangeLower := strings.ToLower(source); strings.HasPrefix(rangeLower, "range") &&
		(len(rangeLower) == len("range") || !isIdentChar(rangeLower[len("range")])) {
		return parseRangeStatement(source, segments[1:])
	}

	// Cross-database source: database('alias').TableName — see
	// DatabaseTableRef's doc comment for why this real-ADX-conformant
	// syntax was chosen. Checked before the table-valued-function case
	// below since both are prefix-recognized off the same source
	// string and don't overlap, but ordering them consistently avoids
	// ever having to reason about which check would "win" if that ever
	// changed.
	if dbRef := parseDatabaseTableRef(source); dbRef != nil {
		query := &Query{SourceDB: dbRef}
		for i := 1; i < len(segments); i++ {
			seg := strings.TrimSpace(segments[i])
			if seg == "" {
				continue
			}
			op, err := parseOperator(seg)
			if err != nil {
				return nil, fmt.Errorf("operator %d: %w", i, err)
			}
			query.Operators = append(query.Operators, op)
		}
		return query, nil
	}

	// Table-valued function: csv("path"), json("path"), ndjson("path"), parquet("path"), vortex("path")
	if tf := parseTableFunc(source); tf != nil {
		query := &Query{SourceFunc: tf}
		for i := 1; i < len(segments); i++ {
			seg := strings.TrimSpace(segments[i])
			if seg == "" {
				continue
			}
			op, err := parseOperator(seg)
			if err != nil {
				return nil, fmt.Errorf("operator %d: %w", i, err)
			}
			query.Operators = append(query.Operators, op)
		}
		return query, nil
	}

	// Stored (persisted, tabular) function call: FuncName() or
	// FuncName(arg1, arg2, ...) — checked AFTER the known
	// table-valued-function names above, deliberately, so a stored
	// function can never shadow a built-in name like csv()/json() even
	// if someone defines one with that name; the built-in always wins.
	// parseStoredFunctionCall itself distinguishes "not a call at all"
	// (nil, nil — falls through to the plain Source case below,
	// surfacing later as an ordinary "table not found") from "looked
	// like a call but the argument list itself doesn't parse" (a real
	// error, returned immediately rather than silently falling through
	// to a confusing, unrelated error).
	call, err := parseStoredFunctionCall(source)
	if err != nil {
		return nil, err
	}
	if call != nil {
		query := &Query{SourceFuncCall: call}
		for i := 1; i < len(segments); i++ {
			seg := strings.TrimSpace(segments[i])
			if seg == "" {
				continue
			}
			op, err := parseOperator(seg)
			if err != nil {
				return nil, fmt.Errorf("operator %d: %w", i, err)
			}
			query.Operators = append(query.Operators, op)
		}
		return query, nil
	}

	query := &Query{Source: unquoteBracketName(source)}

	// Remaining segments are operators
	for i := 1; i < len(segments); i++ {
		seg := strings.TrimSpace(segments[i])
		if seg == "" {
			continue
		}

		op, err := parseOperator(seg)
		if err != nil {
			return nil, fmt.Errorf("operator %d: %w", i, err)
		}
		query.Operators = append(query.Operators, op)
	}

	return query, nil
}

func parseOperator(seg string) (Operator, error) {
	seg = normalizeLeadingKeywordWhitespace(seg)
	lower := keywordFold(seg)

	// | count
	if lower == "count" {
		return &CountOp{}, nil
	}

	// | consume [decodeblocks=...]
	if lower == "consume" || strings.HasPrefix(lower, "consume ") {
		return &ConsumeOp{}, nil
	}

	// | search [kind=...] Predicate
	if strings.HasPrefix(lower, "search ") {
		return parseSearchOp(seg[len("search "):], true)
	}

	// | where <predicate>
	if strings.HasPrefix(lower, "where ") {
		predStr := seg[len("where "):]
		expr, err := ParseExpr(predStr)
		if err != nil {
			return nil, fmt.Errorf("where: %w", err)
		}
		return &WhereOp{Predicate: expr}, nil
	}

	// | getschema
	if lower == "getschema" || lower == "get-schema" {
		return &GetSchemaOp{}, nil
	}

	// | as Name — binds a name to the input tabular expression
	if strings.HasPrefix(lower, "as ") {
		// hint.materialized=true caches the bound result; a binding is
		// already evaluated once here, so the hint has no effect.
		name := unquoteBracketName(stripHints(seg[3:]))
		if name == "" || (!isValidIdentifier(name) && name == stripHints(seg[3:])) {
			return nil, fmt.Errorf("as: expected a valid name, got %q", name)
		}
		return &AsOp{Name: name}, nil
	}

	// | scan [declare (...)] with (step Name [output=...] : Cond => Col=Expr[,...];)
	if strings.HasPrefix(lower, "scan ") || strings.HasPrefix(lower, "scan\t") {
		return parseScan(seg[len("scan "):])
	}

	// | invoke FunctionName(args...)
	if strings.HasPrefix(lower, "invoke ") {
		call, err := parseStoredFunctionCall(strings.TrimSpace(seg[len("invoke "):]))
		if err != nil {
			return nil, fmt.Errorf("invoke: %w", err)
		}
		if call == nil {
			return nil, fmt.Errorf("invoke: expected 'FunctionName(args...)'")
		}
		return &InvokeOp{Call: call}, nil
	}

	// | evaluate [evaluateParameters] PluginName(args...) [: (schema)]
	if strings.HasPrefix(lower, "evaluate ") {
		return parseEvaluate(seg[len("evaluate "):])
	}

	// | partition [hint.strategy=X] by Column (SubQuery)
	if strings.HasPrefix(lower, "partition ") {
		return parsePartition(seg[len("partition "):])
	}

	// | fork [Name =] (subquery) ...
	if keywordAt(lower, "fork") {
		return parseFork(seg[len("fork"):])
	}

	// | facet by Column [, ...] [with (subquery)]
	if keywordAt(lower, "facet") {
		return parseFacet(seg[len("facet"):])
	}

	// | mv-apply [Name =] ArrayCol [to typeof(T)] on ( subquery )
	if strings.HasPrefix(lower, "mv-apply ") || strings.HasPrefix(lower, "mvapply ") {
		var rest string
		if strings.HasPrefix(lower, "mv-apply ") {
			rest = seg[len("mv-apply "):]
		} else {
			rest = seg[len("mvapply "):]
		}
		return parseMvApply(rest)
	}

	// | make-graph Source --> Target [with Nodes on NodeId]
	if strings.HasPrefix(lower, "make-graph ") {
		return parseMakeGraph(seg[len("make-graph "):])
	}

	// | graph-to-table nodes|edges [as N] [with_*_id=C] [, ...]
	if lower == "graph-to-table" || strings.HasPrefix(lower, "graph-to-table ") {
		return parseGraphToTable(strings.TrimSpace(seg[len("graph-to-table"):]))
	}

	// | graph-shortest-paths [output=any|all] (a)-[e*1..5]->(b) [where <expr>] project <items>
	if strings.HasPrefix(lower, "graph-shortest-paths ") {
		return parseGraphShortestPaths(seg[len("graph-shortest-paths "):])
	}

	// | graph-mark-components [kind=weak|strong] [with_component_id=Name]
	if lower == "graph-mark-components" || strings.HasPrefix(lower, "graph-mark-components ") {
		return parseGraphMarkComponents(strings.TrimSpace(seg[len("graph-mark-components"):]))
	}

	// | graph-match (a)-[e]->(b) [where <expr>] project <items>
	if strings.HasPrefix(lower, "graph-match ") {
		return parseGraphMatch(seg[len("graph-match "):])
	}

	// | render <visualization> [with (...)]  — metadata only, engine pass-through
	if strings.HasPrefix(lower, "render ") || lower == "render" {
		rest := strings.TrimSpace(seg[len("render"):])
		if rest == "" {
			return nil, fmt.Errorf("render: expected visualization name")
		}
		op := &RenderOp{}
		withIdx := findKeyword(rest, " with ")
		if withIdx >= 0 {
			op.Visualization = strings.TrimSpace(rest[:withIdx])
			withPart := strings.TrimSpace(rest[withIdx+len(" with "):])
			// Strip surrounding parens if present
			if strings.HasPrefix(withPart, "(") && strings.HasSuffix(withPart, ")") {
				withPart = strings.TrimSpace(withPart[1 : len(withPart)-1])
			}
			op.With = withPart
		} else {
			op.Visualization = rest
		}
		if op.Visualization == "" {
			return nil, fmt.Errorf("render: expected visualization name")
		}
		return op, nil
	}

	// | make-series [kind=nonempty] Agg[, ...] on AxisCol [from X] [to Y] step S [by ...]
	if strings.HasPrefix(lower, "make-series ") {
		return parseMakeSeries(seg[len("make-series "):])
	}

	// | parse-where [kind=simple|regex|relaxed] Column with Pattern...
	// Checked before "parse " below since "parse-where " does not start
	// with "parse " (the hyphen means the prefix check for plain parse
	// would never match it anyway, but ordering it first keeps the
	// relationship between the two obvious to a future reader).
	if strings.HasPrefix(lower, "parse-where ") {
		return parseParseWhereOperator(seg[len("parse-where "):])
	}

	// | parse-kv Expression as ( KeysList ) with ( pair_delimiter=..., kv_delimiter=... )
	if strings.HasPrefix(lower, "parse-kv ") {
		return parseParseKVOperator(seg[len("parse-kv "):])
	}

	// | parse [kind=simple|regex|relaxed] Column with Pattern...
	if strings.HasPrefix(lower, "parse ") {
		return parseParseOperator(seg[6:])
	}

	// | project-away col1, col2, ...
	if strings.HasPrefix(lower, "project-away ") || strings.HasPrefix(lower, "projectaway ") {
		var colStr string
		if strings.HasPrefix(lower, "project-away ") {
			colStr = seg[len("project-away "):]
		} else {
			colStr = seg[len("projectaway "):]
		}
		cols := splitAndTrim(colStr, ',')
		if len(cols) == 0 {
			return nil, fmt.Errorf("project-away: no columns specified")
		}
		return &ProjectAwayOp{Columns: unquoteIdents(cols)}, nil
	}

	// | project-rename NewName = OldName, ...
	if strings.HasPrefix(lower, "project-rename ") || strings.HasPrefix(lower, "projectrename ") {
		var renStr string
		if strings.HasPrefix(lower, "project-rename ") {
			renStr = seg[len("project-rename "):]
		} else {
			renStr = seg[len("projectrename "):]
		}
		parts := splitAndTrim(renStr, ',')
		var renames []RenameSpec
		for _, part := range parts {
			eqIdx := strings.Index(part, "=")
			if eqIdx < 0 {
				return nil, fmt.Errorf("project-rename: expected 'NewName = OldName', got %q", part)
			}
			newName := unquoteIdent(part[:eqIdx])
			oldName := unquoteIdent(part[eqIdx+1:])
			renames = append(renames, RenameSpec{NewName: newName, OldName: oldName})
		}
		return &ProjectRenameOp{Renames: renames}, nil
	}

	// | project-reorder col1, col2, ...
	if strings.HasPrefix(lower, "project-reorder ") || strings.HasPrefix(lower, "projectreorder ") {
		var colStr string
		if strings.HasPrefix(lower, "project-reorder ") {
			colStr = seg[len("project-reorder "):]
		} else {
			colStr = seg[len("projectreorder "):]
		}
		cols := splitAndTrim(colStr, ',')
		if len(cols) == 0 {
			return nil, fmt.Errorf("project-reorder: no columns specified")
		}
		return &ProjectReorderOp{Columns: unquoteIdents(cols)}, nil
	}

	// | project-keep col1, col2, ... (supports wildcards like col*)
	if strings.HasPrefix(lower, "project-keep ") || strings.HasPrefix(lower, "projectkeep ") {
		var colStr string
		if strings.HasPrefix(lower, "project-keep ") {
			colStr = seg[len("project-keep "):]
		} else {
			colStr = seg[len("projectkeep "):]
		}
		patterns := splitAndTrim(colStr, ',')
		if len(patterns) == 0 {
			return nil, fmt.Errorf("project-keep: no columns specified")
		}
		return &ProjectKeepOp{Patterns: unquoteIdents(patterns)}, nil
	}

	// | project-by-names ColumnSpecifier[, ...]
	if strings.HasPrefix(lower, "project-by-names ") {
		return parseProjectByNames(seg[len("project-by-names "):])
	}

	// | project col1, NewCol = expr, ...
	if strings.HasPrefix(lower, "project ") {
		items, err := parseProjectItems(seg[len("project "):])
		if err != nil {
			return nil, fmt.Errorf("project: %w", err)
		}
		return &ProjectOp{Items: items}, nil
	}

	// | extend Name = expr, ...
	if strings.HasPrefix(lower, "extend ") {
		assignStr := seg[len("extend "):]
		assignments, err := parseAssignments(assignStr)
		if err != nil {
			return nil, fmt.Errorf("extend: %w", err)
		}
		return &ExtendOp{Assignments: assignments}, nil
	}

	// | take N / | limit N
	if strings.HasPrefix(lower, "take ") || strings.HasPrefix(lower, "limit ") {
		var numStr string
		if strings.HasPrefix(lower, "take ") {
			numStr = strings.TrimSpace(seg[len("take "):])
		} else {
			numStr = strings.TrimSpace(seg[len("limit "):])
		}
		n, err := strconv.ParseInt(numStr, 10, 64)
		if err != nil {
			expr, perr := ParseExpr(numStr)
			if perr != nil || numStr == "" {
				return nil, fmt.Errorf("take: expected a number or scalar expression, got %q", numStr)
			}
			return &TakeOp{Count: -1, CountExpr: expr}, nil
		}
		return &TakeOp{Count: n}, nil
	}

	// | reduce [kind=source] by Expr [with [threshold=T] [, characters="..."]]
	if strings.HasPrefix(lower, "reduce ") {
		return parseReduce(strings.TrimSpace(seg[len("reduce "):]))
	}

	// | sample N
	if strings.HasPrefix(lower, "sample ") {
		numStr := strings.TrimSpace(seg[len("sample "):])
		n, err := strconv.ParseInt(numStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("sample: expected number, got %q", numStr)
		}
		return &SampleOp{Count: n}, nil
	}

	// | serialize [col1 = expr, ...]
	if lower == "serialize" || strings.HasPrefix(lower, "serialize ") {
		rest := ""
		if strings.HasPrefix(lower, "serialize ") {
			rest = strings.TrimSpace(seg[len("serialize "):])
		}
		var cols []Assignment
		if rest != "" {
			var err error
			cols, err = parseAssignments(rest)
			if err != nil {
				return nil, fmt.Errorf("serialize: %w", err)
			}
		}
		return &SerializeOp{Columns: cols}, nil
	}

	// | distinct col1, col2, ...
	if strings.HasPrefix(lower, "distinct ") {
		colStr := seg[len("distinct "):]
		cols := splitAndTrim(colStr, ',')
		return &DistinctOp{Columns: cols}, nil
	}

	// | sample-distinct NumberOfValues of ColumnName
	if strings.HasPrefix(lower, "sample-distinct ") {
		return parseSampleDistinct(seg[len("sample-distinct "):])
	}

	// | top-nested [N] of Expr [with others = C] by Agg [asc|desc], ...
	if strings.HasPrefix(lower, "top-nested ") || lower == "top-nested" {
		return parseTopNested(seg)
	}

	// | top N by col [asc|desc]
	if strings.HasPrefix(lower, "top ") {
		return parseTop(seg[len("top "):])
	}

	// | order by col [asc|desc], ... / | sort by ...
	if strings.HasPrefix(lower, "order by ") || strings.HasPrefix(lower, "sort by ") {
		var clauseStr string
		if strings.HasPrefix(lower, "order by ") {
			clauseStr = seg[len("order by "):]
		} else {
			clauseStr = seg[len("sort by "):]
		}
		return parseOrderBy(clauseStr)
	}

	// | summarize agg() [by col, ...]
	if strings.HasPrefix(lower, "summarize ") {
		return parseSummarize(seg[len("summarize "):])
	}

	// | join kind=X (subquery) on col1[, col2]
	if strings.HasPrefix(lower, "join ") {
		return parseJoin(seg[len("join "):])
	}

	// | lookup [kind=X] TableName on col1[, col2]
	if strings.HasPrefix(lower, "lookup ") {
		return parseLookup(seg[len("lookup "):])
	}

	// | mv-expand Col or | mv-expand NewName = Expr, ...
	if strings.HasPrefix(lower, "mv-expand ") || strings.HasPrefix(lower, "mvexpand ") {
		var rest string
		if strings.HasPrefix(lower, "mv-expand ") {
			rest = seg[len("mv-expand "):]
			return parseMvExpand(rest)
		}
		// Legacy mvexpand: same operator, default row limit 128.
		op, err := parseMvExpand(seg[len("mvexpand "):])
		if err == nil && op.Limit == 0 {
			op.Limit = 128
		}
		return op, err
	}

	// | union T2, T3 or | union (T2 | where ...), (T3 | where ...)
	if strings.HasPrefix(lower, "union ") || lower == "union" {
		rest := ""
		if len(seg) > 6 {
			rest = strings.TrimSpace(seg[6:])
		}
		return parseUnion(rest)
	}

	return nil, fmt.Errorf("unknown operator: %q", seg)
}

// parseUnion parses the piped form, "| union [params] T2, (T3 | ...)".
func parseUnion(s string) (*UnionOp, error) {
	op, rest, err := parseUnionParams(s)
	if err != nil {
		return nil, fmt.Errorf("union: %w", err)
	}
	sources, err := parseUnionSources(rest)
	if err != nil {
		return nil, fmt.Errorf("union: %w", err)
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("union: no tables specified")
	}
	op.Sources = sources
	return op, nil
}

// parseUnionParams consumes the leading Name=Value parameters of a
// union (kind, withsource, isfuzzy, and the hint.concurrency/
// hint.spread execution hints, which have no effect on a single node)
// and returns the operator with them set plus the remaining text.
func parseUnionParams(s string) (*UnionOp, string, error) {
	op := &UnionOp{Kind: "outer"}
	rest := strings.TrimSpace(s)
	for {
		eq := strings.Index(rest, "=")
		if eq <= 0 {
			return op, rest, nil
		}
		key := strings.ToLower(strings.TrimSpace(rest[:eq]))
		if strings.ContainsAny(key, " \t\r\n,()") {
			return op, rest, nil
		}
		after := strings.TrimLeft(rest[eq+1:], " \t\r\n")
		end := strings.IndexAny(after, " \t\r\n")
		if strings.HasPrefix(after, "[") { // ['quoted name'] may contain spaces
			if c := matchingClose(after, 0, '[', ']'); c > 0 {
				end = c + 1
				if end == len(after) {
					end = -1
				}
			}
		}
		if end < 0 {
			return nil, "", fmt.Errorf("parameter %q has no tables after it", key)
		}
		val := after[:end]
		switch key {
		case "kind":
			v := strings.ToLower(val)
			if v != "inner" && v != "outer" {
				return nil, "", fmt.Errorf("kind must be inner or outer, got %q", val)
			}
			op.Kind = v
		case "withsource":
			if q := unquoteIdent(val); q != val {
				val = q
			} else if !isIdentifier(val) {
				return nil, "", fmt.Errorf("withsource: invalid column name %q", val)
			}
			op.WithSource = val
		case "isfuzzy":
			switch strings.ToLower(val) {
			case "true":
				op.IsFuzzy = true
			case "false":
				op.IsFuzzy = false
			default:
				return nil, "", fmt.Errorf("isfuzzy must be true or false, got %q", val)
			}
		case "hint.concurrency", "hint.spread":
			// Distributed-execution hints; accepted and ignored.
		default:
			return op, rest, nil
		}
		rest = strings.TrimSpace(after[end:])
	}
}

// parseStandaloneUnion handles "union [params] T1, T2 | ...". Every leg,
// including the first, goes into the UnionOp so that withsource can
// label it and isfuzzy can drop it.
func parseStandaloneUnion(sourcesPart string, remainingSegments []string) (Statement, error) {
	op, err := parseUnion(sourcesPart)
	if err != nil {
		return nil, err
	}
	op.Standalone = true
	operators := []Operator{op}
	for i, seg := range remainingSegments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		next, err := parseOperator(seg)
		if err != nil {
			return nil, fmt.Errorf("operator %d: %w", i+1, err)
		}
		operators = append(operators, next)
	}
	return &Query{Source: "", Operators: operators}, nil
}

// parseUnionSources parses comma-separated table names and subqueries.
// Each source can be: TableName or (TableName | operators...)
func parseUnionSources(s string) ([]*Query, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}

	var sources []*Query
	// Split on commas at depth 0 (respecting parentheses)
	parts := splitRespectingParens(s, ',')

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Subquery in parentheses: (Table | where ...)
		if part[0] == '(' && part[len(part)-1] == ')' {
			inner := strings.TrimSpace(part[1 : len(part)-1])
			stmt, err := parseQuery(inner)
			if err != nil {
				return nil, fmt.Errorf("subquery %q: %w", inner, err)
			}
			q, ok := stmt.(*Query)
			if !ok {
				return nil, fmt.Errorf("subquery must be a tabular query")
			}
			sources = append(sources, q)
		} else {
			// Plain table name
			sources = append(sources, &Query{Source: part})
		}
	}
	return sources, nil
}

// parseMvExpand parses
//
//	[kind=bag|array] [bagexpansion=bag|array] [with_itemindex=Name]
//	[Name =] Expr [to typeof(T)] [, ...] [limit N]
func parseMvExpand(s string) (*MvExpandOp, error) {
	return parseMvExpandColumns(s, false)
}

// parseMvExpandColumns parses mv-expand's column list. allowUnnamed
// gives an unnamed expression a generated name (mv-apply) instead of
// rejecting it (mv-expand).
func parseMvExpandColumns(s string, allowUnnamed bool) (*MvExpandOp, error) {
	op := &MvExpandOp{BagMode: "bag"}
	rest := strings.TrimSpace(s)
	for {
		eq := strings.Index(rest, "=")
		if eq <= 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(rest[:eq]))
		if key != "kind" && key != "bagexpansion" && key != "with_itemindex" {
			break
		}
		after := strings.TrimLeft(rest[eq+1:], " \t\r\n")
		end := strings.IndexAny(after, " \t\r\n")
		if end < 0 {
			return nil, fmt.Errorf("mv-expand: %s= has no column after it", key)
		}
		val := after[:end]
		switch key {
		case "kind", "bagexpansion":
			v := strings.ToLower(val)
			if v != "bag" && v != "array" {
				return nil, fmt.Errorf("mv-expand: %s must be bag or array, got %q", key, val)
			}
			op.BagMode = v
		case "with_itemindex":
			if !isIdentifier(val) {
				return nil, fmt.Errorf("mv-expand: with_itemindex: invalid column name %q", val)
			}
			op.IndexColumn = val
		}
		rest = strings.TrimSpace(after[end:])
	}

	if i := findKeyword(rest, " limit "); i >= 0 {
		n, err := strconv.ParseInt(strings.TrimSpace(rest[i+len(" limit "):]), 10, 64)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("mv-expand: limit must be a non-negative integer, got %q", strings.TrimSpace(rest[i+len(" limit "):]))
		}
		op.Limit = n
		rest = strings.TrimSpace(rest[:i])
	}

	for _, part := range splitRespectingParens(rest, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		col := MvExpandColumn{Type: types.TypeDynamic}
		if i := findKeyword(part, " to "); i >= 0 {
			typePart := strings.TrimSpace(part[i+len(" to "):])
			lower := keywordFold(typePart)
			if !strings.HasPrefix(lower, "typeof(") || !strings.HasSuffix(typePart, ")") {
				return nil, fmt.Errorf("mv-expand: expected 'to typeof(type)', got %q", typePart)
			}
			t, err := types.ParseType(strings.TrimSpace(typePart[len("typeof(") : len(typePart)-1]))
			if err != nil {
				return nil, fmt.Errorf("mv-expand: %w", err)
			}
			col.Type = t
			part = strings.TrimSpace(part[:i])
		}
		exprStr := part
		if eq := assignmentEqIndex(part); eq > 0 {
			col.Name = strings.TrimSpace(part[:eq])
			exprStr = strings.TrimSpace(part[eq+1:])
		}
		expr, err := ParseExpr(exprStr)
		if err != nil {
			return nil, fmt.Errorf("mv-expand %q: %w", part, err)
		}
		col.Source = expr
		if col.Name == "" {
			ref, ok := expr.(*ColumnRef)
			switch {
			case ok:
				col.Name = ref.Name
			case allowUnnamed:
				col.Name = fmt.Sprintf("Column%d", len(op.Columns)+1)
			default:
				return nil, fmt.Errorf("mv-expand: expression %q needs a name (Name = expression)", exprStr)
			}
		}
		op.Columns = append(op.Columns, col)
	}
	if len(op.Columns) == 0 {
		return nil, fmt.Errorf("mv-expand: no columns specified")
	}
	return op, nil
}

// parseMvApply parses: [Name =] ArrayCol [to typeof(T)] on ( op1 | op2 | ... )
// The parenthesized subquery is a sourceless operator pipeline that runs
// against the per-row expanded table.
func parseMvApply(s string) (*MvApplyOp, error) {
	s = strings.TrimSpace(s)

	onIdx := findKeyword(s, " on ")
	if onIdx < 0 {
		return nil, fmt.Errorf("mv-apply: expected 'on ( subquery )'")
	}
	head := strings.TrimSpace(s[:onIdx])
	rest := strings.TrimSpace(s[onIdx+len(" on "):])

	// The column list has mv-expand's grammar except bag expansion,
	// which mv-apply does not have.
	if l := keywordFold(head); strings.HasPrefix(l, "kind") || strings.HasPrefix(l, "bagexpansion") {
		return nil, fmt.Errorf("mv-apply: bag expansion (kind=/bagexpansion=) is not supported; use mv-expand inside the subquery")
	}
	exp, err := parseMvExpandColumns(head, true)
	if err != nil {
		return nil, fmt.Errorf("mv-apply: %w", err)
	}
	op := &MvApplyOp{Columns: exp.Columns, IndexColumn: exp.IndexColumn, Limit: exp.Limit}

	if len(rest) == 0 || rest[0] != '(' {
		return nil, fmt.Errorf("mv-apply: expected '(' after 'on'")
	}
	depth := 0
	closeIdx := -1
	for i := 0; i < len(rest); i++ {
		if rest[i] == '(' {
			depth++
		} else if rest[i] == ')' {
			depth--
			if depth == 0 {
				closeIdx = i
				break
			}
		}
	}
	if closeIdx < 0 {
		return nil, fmt.Errorf("mv-apply: unmatched parenthesis in subquery")
	}
	if trailing := strings.TrimSpace(rest[closeIdx+1:]); trailing != "" {
		return nil, fmt.Errorf("mv-apply: unexpected trailing content %q", trailing)
	}
	inner := strings.TrimSpace(rest[1:closeIdx])
	if inner == "" {
		return nil, fmt.Errorf("mv-apply: empty subquery")
	}

	// Sourceless operator pipeline: split on pipes, parse each segment
	for _, segPart := range splitPipe(inner) {
		segPart = strings.TrimSpace(segPart)
		if segPart == "" {
			continue
		}
		subOp, err := parseOperator(segPart)
		if err != nil {
			return nil, fmt.Errorf("mv-apply subquery: %w", err)
		}
		op.Operators = append(op.Operators, subOp)
	}
	if len(op.Operators) == 0 {
		return nil, fmt.Errorf("mv-apply: empty subquery")
	}
	return op, nil
}

// stripHints removes leading "hint.name=value" tokens (spaces around
// "=" allowed). Hints steer distributed execution (shuffle, broadcast,
// partition counts, remote placement) and change no results, so a
// single-node engine accepts and ignores them.
func stripHints(s string) string {
	s = strings.TrimSpace(s)
	for {
		fields := strings.Fields(s)
		if len(fields) == 0 || !strings.HasPrefix(keywordFold(fields[0]), "hint.") {
			return s
		}
		tok := fields[0]
		if i := strings.Index(tok, "="); i >= 0 {
			tok = tok[:i]
		}
		s = strings.TrimSpace(s[len(tok):])
		if strings.HasPrefix(s, "=") {
			s = strings.TrimSpace(s[1:])
			valFields := strings.Fields(s)
			if len(valFields) > 0 {
				s = strings.TrimSpace(s[len(valFields[0]):])
			}
		}
	}
}

// parsePartition parses "[hint.strategy=X] [hint.other=Y] by Column
// (SubQuery)" — verified against real ADX's own partition operator
// docs before adopting this shape. See PartitionOp's own doc comment
// (ast.go) for exactly what real-ADX partition capability is
// deliberately out of scope here (the braces "{SubQueryWithSource}"
// legacy-only form) and why — a query using that form gets a clear,
// explicit error here, not silent mis-parsing into something else.
func parsePartition(s string) (Operator, error) {
	s = strings.TrimSpace(s)

	// Skip any leading "hint.xxx=yyy" (or the equally valid, spaced
	// "hint.xxx = yyy" — confirmed as real, valid ADX syntax by one of
	// the docs' own worked examples using exactly that spacing, not
	// assumed) tokens — see PartitionOp's own doc comment for why
	// these are recognized and silently ignored rather than rejected
	// or acted on.
	for {
		fields := strings.Fields(s)
		if len(fields) == 0 {
			return nil, fmt.Errorf("partition: expected 'by Column (SubQuery)'")
		}
		if !strings.HasPrefix(keywordFold(fields[0]), "hint.") {
			break
		}
		// Consume "hint.xxx", then an optional "=" and its value,
		// whether or not either is separated by whitespace from what
		// precedes it.
		s = strings.TrimSpace(s[len(fields[0]):])
		if strings.HasPrefix(s, "=") {
			s = strings.TrimSpace(s[1:])
			valFields := strings.Fields(s)
			if len(valFields) > 0 {
				s = strings.TrimSpace(s[len(valFields[0]):])
			}
		}
	}

	if !strings.HasPrefix(keywordFold(s), "by ") {
		return nil, fmt.Errorf("partition: expected 'by Column (SubQuery)', got %q", s)
	}
	s = strings.TrimSpace(s[len("by "):])

	openIdx := strings.IndexAny(s, "({")
	if openIdx < 0 {
		return nil, fmt.Errorf("partition: expected '(' after the partition column")
	}
	byColumn := strings.TrimSpace(s[:openIdx])
	if byColumn == "" || !isValidIdentifier(byColumn) {
		return nil, fmt.Errorf("partition: expected a valid column name, got %q", byColumn)
	}

	if s[openIdx] == '{' {
		closeB := matchingClose(s, openIdx, '{', '}')
		if closeB < 0 {
			return nil, fmt.Errorf("partition %s: unmatched '{'", byColumn)
		}
		if trailing := strings.TrimSpace(s[closeB+1:]); trailing != "" {
			return nil, fmt.Errorf("partition %s: unexpected trailing content %q", byColumn, trailing)
		}
		q, err := parseQuery(strings.TrimSpace(s[openIdx+1 : closeB]))
		if err != nil {
			return nil, fmt.Errorf("partition %s subquery: %w", byColumn, err)
		}
		query, ok := q.(*Query)
		if !ok {
			return nil, fmt.Errorf("partition %s: the subquery must be a single tabular expression", byColumn)
		}
		return &PartitionOp{ByColumn: byColumn, Source: query}, nil
	}

	closeIdx := findMatchingParen(s, openIdx)
	if closeIdx < 0 {
		return nil, fmt.Errorf("partition %s: unmatched '(' in subquery", byColumn)
	}
	if trailing := strings.TrimSpace(s[closeIdx+1:]); trailing != "" {
		return nil, fmt.Errorf("partition %s: unexpected trailing content %q", byColumn, trailing)
	}
	inner := strings.TrimSpace(s[openIdx+1 : closeIdx])
	if inner == "" {
		return nil, fmt.Errorf("partition %s: empty subquery", byColumn)
	}

	op := &PartitionOp{ByColumn: byColumn}
	for _, segPart := range splitPipe(inner) {
		segPart = strings.TrimSpace(segPart)
		if segPart == "" {
			continue
		}
		subOp, err := parseOperator(segPart)
		if err != nil {
			return nil, fmt.Errorf("partition %s subquery: %w", byColumn, err)
		}
		op.Operators = append(op.Operators, subOp)
	}
	if len(op.Operators) == 0 {
		return nil, fmt.Errorf("partition %s: empty subquery", byColumn)
	}
	return op, nil
}

// parsePrintStatement parses "print Name1 = Expr1, Name2 = Expr2, ..."
// and any subsequent pipeline operators.
func parsePrintStatement(s string, remaining []string) (Statement, error) {
	assignments, err := parseAssignments(s)
	if err != nil {
		return nil, fmt.Errorf("print: %w", err)
	}
	for i := range assignments {
		if assignments[i].Unnamed {
			assignments[i].Name = "" // print_<position>
		}
	}
	query := &Query{
		Source:    "",
		Operators: []Operator{&PrintOp{Expressions: assignments}},
	}
	for i, seg := range remaining {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		op, err := parseOperator(seg)
		if err != nil {
			return nil, fmt.Errorf("operator %d: %w", i+1, err)
		}
		query.Operators = append(query.Operators, op)
	}
	return query, nil
}

// parseDataTableStatement parses: datatable (Col1: type, ...) [val1, val2, ...]
func parseDataTableStatement(source string, remaining []string) (Statement, error) {
	// Strip "datatable" prefix (case-insensitive)
	s := strings.TrimSpace(source[len("datatable"):])

	// Find schema between ( and )
	schemaStart := strings.IndexByte(s, '(')
	if schemaStart < 0 {
		return nil, fmt.Errorf("datatable: expected '(' after datatable")
	}
	schemaEnd := findMatchingParen(s, schemaStart)
	if schemaEnd < 0 {
		return nil, fmt.Errorf("datatable: unmatched '('")
	}
	schemaPart := s[schemaStart+1 : schemaEnd]

	// Parse column definitions: "Name: string, Age: long"
	var schema types.Schema
	for _, colDef := range splitRespectingParens(schemaPart, ',') {
		colDef = strings.TrimSpace(colDef)
		if colDef == "" {
			continue
		}
		ci := strings.LastIndex(colDef, ":") // type names have no colon; a quoted name may
		if ci < 0 {
			return nil, fmt.Errorf("datatable: expected 'ColName: type', got %q", colDef)
		}
		name := unquoteIdent(colDef[:ci])
		typeName := strings.TrimSpace(colDef[ci+1:])
		kt, err := types.ParseType(typeName)
		if err != nil {
			return nil, fmt.Errorf("datatable: unknown type %q for column %q", typeName, name)
		}
		schema.Columns = append(schema.Columns, types.Column{Name: name, Type: kt})
	}

	if len(schema.Columns) == 0 {
		return nil, fmt.Errorf("datatable: no columns defined")
	}

	// Find values between [ and ]
	rest := strings.TrimSpace(s[schemaEnd+1:])
	valStart := strings.IndexByte(rest, '[')
	if valStart < 0 {
		return nil, fmt.Errorf("datatable: expected '[' for values")
	}
	valEnd := findMatchingBracket(rest, valStart)
	if valEnd < 0 {
		return nil, fmt.Errorf("datatable: unmatched '['")
	}
	valuesPart := rest[valStart+1 : valEnd]

	// Split values respecting strings
	values := splitDataTableValues(valuesPart)

	dt := &DataTableOp{Schema: schema, Values: values}
	query := &Query{
		Source:    "",
		Operators: []Operator{dt},
	}

	for i, seg := range remaining {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		op, err := parseOperator(seg)
		if err != nil {
			return nil, fmt.Errorf("operator %d: %w", i+1, err)
		}
		query.Operators = append(query.Operators, op)
	}
	return query, nil
}

// findTopLevelKeyword finds the start index of the first standalone
// occurrence of keyword in s, respecting parens/brackets and quoted
// strings (so a paren-nested or quoted "to"/"step" — e.g. inside a
// string literal or a nested function call argument — is never
// mistaken for the range operator's own from/to/step separators), and
// requiring word boundaries on both sides (so "tomorrow" doesn't match
// a search for "to"). Returns -1 if not found.
func findTopLevelKeyword(s, keyword string) int {
	lower := keywordFold(s)
	kw := strings.ToLower(keyword)
	depth := 0
	inStr := byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inStr != 0 {
			if ch == inStr && !precededByOddBackslashes(s, i) {
				inStr = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inStr = ch
			continue
		}
		if ch == '(' || ch == '[' {
			depth++
		}
		if (ch == ')' || ch == ']') && depth > 0 {
			depth--
		}
		if depth != 0 {
			continue
		}
		if i+len(kw) <= len(lower) && lower[i:i+len(kw)] == kw {
			leftOK := i == 0 || !isIdentChar(s[i-1])
			rightIdx := i + len(kw)
			rightOK := rightIdx == len(s) || !isIdentChar(s[rightIdx])
			if leftOK && rightOK {
				return i
			}
		}
	}
	return -1
}

// parseRangeStatement parses "range columnName from start to stop step
// step" and any subsequent pipeline operators — verified against real
// ADX's own range operator docs before adopting this shape: "This
// operator doesn't take a tabular input... The values can't reference
// the columns of any table." Start/stop/step are parsed as ordinary
// scalar expressions (via ParseExpr), not restricted to bare numeric
// literals, matching real ADX's own documented example computing them
// from function calls (range LastWeek from ago(7d) to now() step 1d).
func parseRangeStatement(source string, remaining []string) (Statement, error) {
	s := strings.TrimSpace(source[len("range"):])

	fromIdx := findTopLevelKeyword(s, "from")
	if fromIdx < 0 {
		return nil, fmt.Errorf("range: expected 'columnName from start to stop step step'")
	}
	colName := strings.TrimSpace(s[:fromIdx])
	if colName == "" || !isValidIdentifier(colName) {
		return nil, fmt.Errorf("range: expected a column name before 'from', got %q", colName)
	}
	rest := s[fromIdx+len("from"):]

	toIdx := findTopLevelKeyword(rest, "to")
	if toIdx < 0 {
		return nil, fmt.Errorf("range %s: expected 'to stop' after 'from start'", colName)
	}
	startText := strings.TrimSpace(rest[:toIdx])
	rest = rest[toIdx+len("to"):]

	stepIdx := findTopLevelKeyword(rest, "step")
	if stepIdx < 0 {
		return nil, fmt.Errorf("range %s: expected 'step step' after 'to stop'", colName)
	}
	stopText := strings.TrimSpace(rest[:stepIdx])
	stepText := strings.TrimSpace(rest[stepIdx+len("step"):])

	if startText == "" || stopText == "" || stepText == "" {
		return nil, fmt.Errorf("range %s: start, stop, and step must all be non-empty expressions", colName)
	}

	start, err := ParseExpr(startText)
	if err != nil {
		return nil, fmt.Errorf("range %s: start %q: %w", colName, startText, err)
	}
	stop, err := ParseExpr(stopText)
	if err != nil {
		return nil, fmt.Errorf("range %s: stop %q: %w", colName, stopText, err)
	}
	step, err := ParseExpr(stepText)
	if err != nil {
		return nil, fmt.Errorf("range %s: step %q: %w", colName, stepText, err)
	}

	query := &Query{
		Source:    "",
		Operators: []Operator{&RangeOp{ColumnName: colName, Start: start, Stop: stop, Step: step}},
	}

	for i, seg := range remaining {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		op, err := parseOperator(seg)
		if err != nil {
			return nil, fmt.Errorf("operator %d: %w", i+1, err)
		}
		query.Operators = append(query.Operators, op)
	}
	return query, nil
}

// parseEvaluate parses "[evaluateParameters] PluginName(args...)
// [: (schema)]" — verified against real ADX's own evaluate operator
// docs before adopting this shape.
//
// evaluateParameters (hint.distribution=..., hint.num_partitions=...,
// real ADX's own distribution hints for a distributed query planner)
// are recognized and silently skipped, not rejected: this engine has
// no distributed execution to hint at all, so a query written for a
// real ADX cluster that happens to include one of these still parses
// and runs correctly here, just without any distribution behavior to
// change (which is the only correct outcome for a single-node
// engine — erroring on a hint that's genuinely meaningless here would
// be pedantic, not helpful).
func parseEvaluate(s string) (Operator, error) {
	s = strings.TrimSpace(s)

	// Skip any leading "hint.xxx=yyy" tokens.
	for {
		fields := strings.Fields(s)
		if len(fields) == 0 {
			return nil, fmt.Errorf("evaluate: expected a plugin name")
		}
		if strings.HasPrefix(keywordFold(fields[0]), "hint.") {
			s = strings.TrimSpace(s[len(fields[0]):])
			continue
		}
		break
	}

	openIdx := strings.Index(s, "(")
	if openIdx < 0 {
		return nil, fmt.Errorf("evaluate: expected 'PluginName(args...)'")
	}
	pluginName := strings.TrimSpace(s[:openIdx])
	if pluginName == "" || !isValidIdentifier(pluginName) {
		return nil, fmt.Errorf("evaluate: expected a valid plugin name, got %q", pluginName)
	}

	closeIdx := findMatchingParen(s, openIdx)
	if closeIdx < 0 {
		return nil, fmt.Errorf("evaluate %s(...): unterminated argument list", pluginName)
	}
	inner := strings.TrimSpace(s[openIdx+1 : closeIdx])

	op := &EvaluateOp{PluginName: pluginName}
	if inner != "" {
		for _, part := range splitRespectingParens(inner, ',') {
			part = strings.TrimSpace(part)
			if part == "" {
				return nil, fmt.Errorf("evaluate %s(...): empty argument in call", pluginName)
			}
			op.ArgTexts = append(op.ArgTexts, part)
		}
	}

	rest := strings.TrimSpace(s[closeIdx+1:])
	if strings.HasPrefix(rest, ":") {
		schemaText := strings.TrimSpace(rest[1:])
		if !strings.HasPrefix(schemaText, "(") || !strings.HasSuffix(schemaText, ")") {
			return nil, fmt.Errorf("evaluate %s: expected '(Name: type, ...)' after ':', got %q", pluginName, schemaText)
		}
		// A "*" entry (bag_unpack(d) : (*, Name:string)) keeps the
		// input's columns; it is recorded as a column named "*".
		var defs []string
		star := false
		for _, d := range splitRespectingParens(schemaText[1:len(schemaText)-1], ',') {
			if strings.TrimSpace(d) == "*" {
				star = true
				continue
			}
			defs = append(defs, d)
		}
		schema := &types.Schema{}
		if len(defs) > 0 {
			sc, err := parseColumnDefs(strings.Join(defs, ","))
			if err != nil {
				return nil, fmt.Errorf("evaluate %s: output schema: %w", pluginName, err)
			}
			schema = sc
		}
		if star {
			schema.Columns = append([]types.Column{{Name: "*", Type: types.TypeDynamic}}, schema.Columns...)
		}
		op.OutputSchema = schema
	} else if rest != "" {
		return nil, fmt.Errorf("evaluate %s: unexpected trailing text %q", pluginName, rest)
	}

	return op, nil
}

// findMatchingParen finds the closing ')' matching the '(' at position start.
func findMatchingParen(s string, start int) int {
	depth := 0
	inStr := false
	for i := start; i < len(s); i++ {
		if s[i] == '"' && !precededByOddBackslashes(s, i) {
			inStr = !inStr
		}
		if inStr {
			continue
		}
		if s[i] == '(' {
			depth++
		} else if s[i] == ')' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// findMatchingBracket finds the closing ']' matching the '[' at position start.
func findMatchingBracket(s string, start int) int {
	depth := 0
	inStr := false
	for i := start; i < len(s); i++ {
		if s[i] == '"' && !precededByOddBackslashes(s, i) {
			inStr = !inStr
		}
		if inStr {
			continue
		}
		if s[i] == '[' {
			depth++
		} else if s[i] == ']' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitDataTableValues splits a comma-separated value list, respecting quoted strings.
func splitDataTableValues(s string) []string {
	// bracketDepth tracks [...] nesting so a bracketed literal — a JSON
	// array value for a dynamic-typed column, e.g. ["x", "y"] — is kept
	// as ONE value token, not split on its internal comma into two.
	//
	// Found live, not hypothetical: without this, datatable(Tags:
	// dynamic) ["a", ["x", "y"], "b", ["z"]] silently misaligned every
	// value from that point on — the comma inside ["x", "y"] split it
	// into two flat tokens ("["x"" and ""y"]"), producing 3 rows
	// instead of 2 with values shifted and mangled (a row with
	// Id=`"y"]` — the literal closing bracket becoming part of an Id),
	// no error anywhere. datatable's OWN operator is genuinely flat in
	// real ADX too (values assigned round-robin to columns, no nested
	// row structure) — but a single dynamic-typed CELL holding a JSON
	// array is a different thing entirely from a nested ROW, and
	// TypeDynamic is already stored as raw JSON text everywhere else in
	// this codebase (embedding vectors are the existing example) — so
	// treating a bracketed value as one token is completing existing
	// behavior, not inventing new syntax.
	// bracketDepth ALSO tracks {...} and (...) nesting, not just [...]
	// as an earlier version of this function did — found live, not
	// hypothetical, the same class of bug already found and fixed once
	// this session in a DIFFERENT function (splitStatements not
	// tracking brace depth): datatable(d:dynamic)
	// [dynamic({"Name": "John", "Age":20})] silently misaligned every
	// value the exact same way the bracket-only fix's own doc comment
	// above describes for [...] — the comma INSIDE the JSON object
	// split it into two flat, truncated, invalid-JSON tokens
	// (`dynamic({"Name": "John"` and `"Age":20})`), no error anywhere,
	// only surfacing downstream as a plugin (evaluate bag_unpack)
	// silently seeing malformed dynamic values and producing an empty
	// result with zero columns. {}/() nesting shares the SAME depth
	// counter as []: for deciding whether a comma here is a genuine
	// value separator, which bracket kind is nesting doesn't matter,
	// only whether nesting is happening at all — the identical
	// reasoning already applied to splitStatements' own fix.
	// Quotes: both '...' and "..." delimit strings, and an @-prefixed
	// (verbatim) string takes no backslash escapes. A comma inside
	// either kind of string is part of the value.
	var values []string
	var current strings.Builder
	inStr := false
	var quote byte
	verbatim := false
	bracketDepth := 0
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inStr {
			current.WriteByte(ch)
			if ch == quote && (verbatim || !precededByOddBackslashes(s, i)) {
				inStr = false
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inStr = true
			quote = ch
			verbatim = i > 0 && (s[i-1] == '@')
			current.WriteByte(ch)
			continue
		}
		if !inStr {
			if ch == '[' || ch == '{' || ch == '(' {
				bracketDepth++
			} else if (ch == ']' || ch == '}' || ch == ')') && bracketDepth > 0 {
				bracketDepth--
			}
		}
		if ch == ',' && !inStr && bracketDepth == 0 {
			v := strings.TrimSpace(current.String())
			if v != "" {
				values = append(values, v)
			}
			current.Reset()
			continue
		}
		current.WriteByte(ch)
	}
	if v := strings.TrimSpace(current.String()); v != "" {
		values = append(values, v)
	}
	return values
}

// parseTop parses "N by Expression [asc|desc]". Real ADX's own top
// operator only ever supports a SINGLE ranking Expression — verified
// directly against Microsoft's own docs before fixing what was here,
// which included a stated equivalence not to be missed: "top 5 by
// name is equivalent to the expression sort by name | take 5 both
// from semantic and performance perspectives." No multi-key form
// exists in real ADX at all (top-nested is the separate, dedicated
// operator for hierarchical ranking needs).
//
// An earlier version of this function used strings.Fields (a bare
// whitespace split) and silently took only parts[2] as the column,
// with no check for anything unexpected following it at all — so
// `top 1 by X desc, Y desc` neither errored nor behaved as a genuine
// two-key sort; it silently parsed as top 1 by X, with "desc," (the
// trailing comma) failing the exact asc/desc string match and falling
// through to top's own default (desc) with no error or warning
// anywhere. Found live via a different model's testing (Kimi), on
// real data: the silently-dropped second key meant top disagreed with
// the semantically equivalent sort by | take on tied first-key rows,
// returning an effectively arbitrary row instead of the documented,
// correct tie-break.
//
// Fixed by explicitly detecting and rejecting a comma in the by
// clause with a clear, actionable error — matching real ADX's own
// genuine restriction with an honest failure instead of silent
// mis-parsing, and pointing at the two real, correct alternatives:
// combine multiple criteria into one ranking expression (e.g.
// coalesce(a, b)), or use sort by (which DOES support multiple keys)
// followed by take.
func parseTop(s string) (*TopOp, error) {
	s = strings.TrimSpace(s)
	byIdx := findKeyword(" "+s, " by ")
	if byIdx < 0 {
		return nil, fmt.Errorf("expected 'top N by expression [asc|desc] [nulls first|last]'")
	}
	countText := strings.TrimSpace((" " + s)[:byIdx])
	rest := strings.TrimSpace((" " + s)[byIdx+len(" by "):])
	if countText == "" || rest == "" {
		return nil, fmt.Errorf("expected 'top N by expression [asc|desc] [nulls first|last]'")
	}
	var countExpr Expr
	n, err := strconv.ParseInt(countText, 10, 64)
	if err != nil {
		e, perr := ParseExpr(countText)
		if perr != nil {
			return nil, fmt.Errorf("top: expected a number or scalar expression, got %q", countText)
		}
		n, countExpr = -1, e
	}
	if len(splitRespectingParens(rest, ',')) > 1 {
		return nil, fmt.Errorf("top: only a single ranking expression is supported (matching real ADX's own restriction) — combine multiple criteria into one expression (e.g. coalesce(a, b)), or use 'sort by col1, col2 | take N' instead, which supports multiple keys")
	}
	cl, err := parseOrderClause(rest)
	if err != nil {
		return nil, fmt.Errorf("top: %w", err)
	}
	return &TopOp{Count: n, By: cl.Column, ByExpr: cl.Expr, Desc: cl.Desc, Nulls: cl.Nulls, CountExpr: countExpr}, nil
}

// parseOrderClause parses `Expr [asc|desc] [nulls first|last]`. A bare
// (or bracket-quoted) column name sets Column; anything else Expr.
func parseOrderClause(part string) (OrderClause, error) {
	cl := OrderClause{Desc: true} // KQL default is desc
	part = strings.TrimSpace(part)
	low := keywordFold(part)
	if strings.HasSuffix(low, " nulls first") {
		cl.Nulls, part = NullsFirst, strings.TrimSpace(part[:len(part)-len(" nulls first")])
	} else if strings.HasSuffix(low, " nulls last") {
		cl.Nulls, part = NullsLast, strings.TrimSpace(part[:len(part)-len(" nulls last")])
	}
	low = keywordFold(part)
	if strings.HasSuffix(low, " asc") {
		cl.Desc, part = false, strings.TrimSpace(part[:len(part)-len(" asc")])
	} else if strings.HasSuffix(low, " desc") {
		part = strings.TrimSpace(part[:len(part)-len(" desc")])
	}
	if part == "" {
		return cl, fmt.Errorf("expected an expression to order by")
	}
	// A quoted identifier, ['$table'] or ["a b"], may hold spaces.
	if (strings.HasPrefix(part, "['") && strings.HasSuffix(part, "']")) ||
		(strings.HasPrefix(part, `["`) && strings.HasSuffix(part, `"]`)) {
		if inner := part[2 : len(part)-2]; !strings.ContainsAny(inner, `'"`) {
			cl.Column = inner
			return cl, nil
		}
	}
	if isPlainIdentifier(part) {
		cl.Column = part
		return cl, nil
	}
	e, err := ParseExpr(part)
	if err != nil {
		return cl, err
	}
	if ref, ok := e.(*ColumnRef); ok {
		cl.Column = ref.Name
		return cl, nil
	}
	cl.Expr = e
	return cl, nil
}

func isPlainIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || r == '$' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func parseOrderBy(s string) (*OrderByOp, error) {
	var clauses []OrderClause
	for _, part := range splitRespectingParens(s, ',') {
		if strings.TrimSpace(part) == "" {
			continue
		}
		cl, err := parseOrderClause(part)
		if err != nil {
			return nil, fmt.Errorf("order by: %w", err)
		}
		clauses = append(clauses, cl)
	}
	return &OrderByOp{Clauses: clauses}, nil
}

func parseSummarize(s string) (*SummarizeOp, error) {
	s = stripHints(s)

	// Split on " by " to separate aggregations from group-by columns.
	// The leading space lets "by x" with no aggregates match too.
	s = " " + s
	spaced := strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
	var aggStr, byStr string
	byIdx := findKeyword(spaced, " by ")
	if byIdx < 0 {
		// sum(x)by a: no space after the closing parenthesis.
		for k := 0; k < len(spaced); {
			i := findKeyword(spaced[k:], "by ")
			if i < 0 {
				break
			}
			if i += k; i > 0 && spaced[i-1] == ')' {
				byIdx = i - 1
				break
			}
			k = i + 1
		}
	}
	if byIdx >= 0 {
		aggStr = strings.TrimSpace(s[:byIdx+1])
		byStr = strings.TrimSpace(s[byIdx+4:])
		if s[byIdx] == ' ' {
			aggStr = strings.TrimSpace(s[:byIdx])
		}
	} else {
		aggStr = s
	}

	// Parse aggregations
	aggParts := splitRespectingParens(aggStr, ',')
	var aggs []Aggregation

	for _, part := range aggParts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		agg, err := parseAggregation(part)
		if err != nil {
			return nil, err
		}
		aggs = append(aggs, *agg)
	}

	if len(aggs) == 0 && strings.TrimSpace(byStr) == "" {
		return nil, fmt.Errorf("summarize: no aggregations specified")
	}
	// Hidden inner-aggregate columns must be unique across items.
	for k := range aggs {
		if aggs[k].Function != PostAggregation {
			continue
		}
		rename := map[string]string{}
		for j := range aggs[k].Inner {
			n := fmt.Sprintf("%s%d_%d", postAggPrefix, k, j)
			rename[aggs[k].Inner[j].Name] = n
			aggs[k].Inner[j].Name = n
		}
		aggs[k].Post = renameColumnRefs(aggs[k].Post, rename)
	}

	// Parse group-by expressions (columns or function calls like bin())
	var byExprs []ByExpr
	if byStr != "" {
		byParts := splitRespectingParens(byStr, ',')
		for _, part := range byParts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}

			// Check for "Name = expr" alias syntax
			var name string
			exprStr := part
			if eqIdx := assignmentEqIndex(part); eqIdx > 0 {
				name = unquoteIdent(part[:eqIdx])
				exprStr = strings.TrimSpace(part[eqIdx+1:])
			}

			expr, err := ParseExpr(exprStr)
			if err != nil {
				return nil, fmt.Errorf("summarize by: %w", err)
			}

			// Auto-derive output column name if not aliased
			if name == "" {
				name = deriveByName(expr)
			}

			byExprs = append(byExprs, ByExpr{Name: name, Expr: expr})
		}
	}

	return &SummarizeOp{Aggregations: aggs, ByExprs: byExprs}, nil
}

// deriveByName extracts a display name from a group-by expression.
// For ColumnRef, returns the column name.
// For bin(Col, step), returns the first arg's column name.
// For other expressions, returns the expression as a string.
func deriveByName(expr Expr) string {
	name, _ := ExprResultName(expr) // "" → ColumnN in the engine
	return name
}

func parseAggregation(s string) (*Aggregation, error) {
	s = strings.TrimSpace(s)
	agg := &Aggregation{}

	// "Name = func(...)": an assignment "=" at the top level, not part
	// of ==, !=, <=, >= or =~ (countif(x >= 3) has no name).
	if eqIdx := assignmentEqIndex(s); eqIdx > 0 {
		agg.Name = unquoteIdent(s[:eqIdx])
		s = strings.TrimSpace(s[eqIdx+1:])
	}

	// Anything other than one aggregate call is a scalar expression over
	// aggregates: count() * 2, toint(sum(x)), sum(x) / count().
	if !isSingleAggregateCall(s) {
		return parsePostAggregation(s, agg.Name)
	}

	// Parse function call: func(args...)
	parenIdx := strings.Index(s, "(")
	if parenIdx < 0 {
		return nil, fmt.Errorf("expected aggregation function call, got %q", s)
	}

	agg.Function = strings.ToLower(strings.TrimSpace(s[:parenIdx]))

	closeIdx := strings.LastIndex(s, ")")
	if closeIdx < 0 {
		return nil, fmt.Errorf("expected closing ) in %q", s)
	}

	argStr := strings.TrimSpace(s[parenIdx+1 : closeIdx])
	if argStr != "" {
		argParts := splitRespectingParens(argStr, ',')
		for _, ap := range argParts {
			ap = strings.TrimSpace(ap)
			if ap == "" {
				continue
			}
			// Bare `*` — real ADX's arg_max/arg_min wildcard ("return
			// all columns"), recognized here rather than in ParseExpr
			// since it's specific to this argument-list context, not a
			// general expression. Checked before ParseExpr, which
			// would otherwise reject `*` outright (it's only valid as
			// a binary multiplication operator, never a standalone
			// token) — found live: arg_max(Version, *) failed to parse
			// at all before this.
			if ap == "*" {
				agg.Args = append(agg.Args, &StarExpr{})
				continue
			}
			expr, err := ParseExpr(ap)
			if err != nil {
				return nil, fmt.Errorf("aggregation arg: %w", err)
			}
			if containsAggregateCall(expr) {
				return nil, fmt.Errorf("summarize: aggregates cannot be nested in %s()", agg.Function)
			}
			agg.Args = append(agg.Args, expr)
		}
	}

	// Auto-generate output name if not specified. arg_max/arg_min are
	// a deliberate exception to the general function_argname[0] rule,
	// verified against real ADX examples before diverging: arg_max(
	// BeginLat, BeginLocation) by State names its output column
	// BeginLocation (the SECOND argument -- what's actually returned),
	// not arg_max_BeginLat. Every other aggregate (max, sum, count,
	// ...) keeps the function_argname[0] convention, since for those
	// the first argument IS what's returned. Found live via a real
	// cross-model conformance report before this was in scope to fix:
	// the old naming meant any hand-written query or stored function
	// projecting the "obvious" column name would silently get nothing
	// (a column that doesn't exist) rather than the intended value.
	if agg.Name == "" {
		agg.AutoNamed = true
	}
	// Generated names from Kusto.Language's result-name kinds (sum(x)
	// → sum_x, make_list(x) → list_x, take_anyif(x, p) → x). covariance
	// keeps both argument names, as ADX's own example shows, and the
	// percentiles family and arg_max/arg_min/take_any are named per
	// output column elsewhere.
	if agg.Name == "" && !strings.HasPrefix(agg.Function, "covariance") && !IsPercentileAgg(agg.Function) {
		if n, ok := AggResultName(&FuncCall{Name: agg.Function, Args: agg.Args}); ok {
			agg.Name = n
			return agg, nil
		}
	}
	if agg.Name == "" && IsPercentileAgg(agg.Function) {
		agg.Name = PercentileAutoName(agg, 0)
		return agg, nil
	}
	if agg.Name == "" {
		nameSourceIdx := 0
		if (agg.Function == "arg_max" || agg.Function == "arg_min") && len(agg.Args) >= 2 {
			nameSourceIdx = 1
		}
		// covariance/covariancep/covarianceif/covariancepif are a
		// second deliberate exception to the single-column
		// function_argname[0] convention, verified against real ADX's
		// own worked example before diverging: summarize
		// covariance(x, y) names its output column "covariance_x_y"
		// -- BOTH argument column names, not just the first -- since
		// covariance is inherently a two-variable statistic, unlike
		// variance/stdev/etc which describe a single column. Added
		// 2026-08-17 alongside implementing the covariance family
		// itself (aggregation.go).
		if (agg.Function == "covariance" || agg.Function == "covariancep" ||
			agg.Function == "covarianceif" || agg.Function == "covariancepif") &&
			len(agg.Args) >= 2 {
			ref0, ok0 := agg.Args[0].(*ColumnRef)
			ref1, ok1 := agg.Args[1].(*ColumnRef)
			if ok0 && ok1 {
				agg.Name = agg.Function + "_" + ref0.Name + "_" + ref1.Name
				return agg, nil
			}
			agg.Name = agg.Function + "_"
			return agg, nil
		}
		// percentiles_array's own auto-name prefix is "percentiles",
		// not the literal function name "percentiles_array" — verified
		// directly against real ADX's own worked example before fixing
		// this, not assumed: summarize percentiles_array(Value, ...)
		// names its output column "percentiles_Value", matching
		// percentiles() itself using "percentile_" (singular) despite
		// its own plural function name — real ADX's column-naming
		// convention for this whole family is independent of each
		// function's own exact literal name.
		namePrefix := agg.Function
		if agg.Function == "percentiles_array" || agg.Function == "percentilesw_array" {
			namePrefix = "percentiles"
		}
		if len(agg.Args) > nameSourceIdx {
			if ref, ok := agg.Args[nameSourceIdx].(*ColumnRef); ok {
				agg.Name = ref.Name
				if nameSourceIdx == 0 {
					agg.Name = namePrefix + "_" + ref.Name
				}
			} else {
				agg.Name = namePrefix + "_"
			}
		} else {
			agg.Name = namePrefix + "_"
		}
	}

	return agg, nil
}

// AssignmentEqIndex is the exported form of assignmentEqIndex, for
// cross-package reuse of the same named-argument detection this
// parser already relies on internally — first needed by the engine
// package's evaluate plugins (evaluate.go's bag_unpack, and any
// future plugin taking named arguments like columnsConflict=... will
// need the identical detection, not a duplicated, potentially
// drifting copy of it).
func AssignmentEqIndex(s string) int { return assignmentEqIndex(s) }

// assignmentEqIndex returns the index of a top-level assignment '=' in s,
// or -1 if s is not an assignment. It skips '==', '!=', '<=', '>=', '=~' and
// any '=' inside quotes or parentheses, so expressions like
// "X = iff(A == 1, \"y\", \"n\")" are detected correctly while a bare
// comparison is not.
func assignmentEqIndex(s string) int {
	depth := 0
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inQuote != 0 {
			if ch == inQuote && !precededByOddBackslashes(s, i) {
				inQuote = 0
			}
			continue
		}
		switch ch {
		case '"', '\'':
			inQuote = ch
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case '=':
			if depth != 0 {
				continue
			}
			// Not an assignment if part of ==, !=, <=, >=, =~
			if i+1 < len(s) && (s[i+1] == '=' || s[i+1] == '~') {
				i++ // skip the pair
				continue
			}
			if i > 0 && (s[i-1] == '=' || s[i-1] == '!' || s[i-1] == '<' || s[i-1] == '>') {
				continue
			}
			return i
		}
	}
	return -1
}

// parseAssignments parses a comma-separated list of "Name = expr" or
// bare "expr" items — shared by print, extend, and project.
//
// Verified against real ADX's own extend/project docs before adding
// bare-expression support: "if ColumnName is omitted, the output
// column name of Expression is automatically generated" — confirmed
// for BOTH operators explicitly, not just print, so this fix belongs
// here, in the shared parsing function, not as a print-specific
// special case. An earlier version rejected any part without a
// top-level '=' outright ("expected 'Name = expr'"), which real ADX
// never required at all. Found live via a different model's testing
// (Kimi): even a query as simple as `print 5` (no toscalar()
// involved) failed the same way, confirming this was a real,
// pre-existing gap, not something introduced by toscalar() work.
//
// Uses assignmentEqIndex (not a naive strings.Index) to find the
// top-level '=' — the same helper join's kind=X parsing and others
// already rely on, which correctly skips ==, !=, <=, >=, =~ and any
// '=' nested inside quotes or parentheses. The previous, ad-hoc
// "guard against == being treated as assignment" check only handled
// the single, exact `==` case; assignmentEqIndex is the more general,
// already-proven fix for the same underlying problem.
//
// A bare expression (no top-level '=' found at all) gets the
// expression's own raw text as its auto-generated name, matching real
// ADX's own convention of using the expression text itself when no
// name is given — not a synthetic placeholder like "print_0", which
// an earlier version of applyPrint's own display fallback used only
// because the parser never allowed a name-less Assignment to reach it
// at all until now.
func parseAssignments(s string) ([]Assignment, error) {
	parts := splitRespectingParens(s, ',')
	var result []Assignment

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		eqIdx := assignmentEqIndex(part)
		if eqIdx < 0 {
			// Bare expression, no name given — real ADX auto-generates
			// one from the expression's own text.
			expr, err := ParseExpr(part)
			if err != nil {
				return nil, fmt.Errorf("%q: %w", part, err)
			}
			if multi := expandMultiColumn("", expr); multi != nil {
				result = append(result, multi...)
				continue
			}
			name, _ := ExprResultName(expr)
			result = append(result, Assignment{Name: name, Expr: expr, Unnamed: true})
			continue
		}

		name := unquoteIdent(part[:eqIdx])
		exprStr := strings.TrimSpace(part[eqIdx+1:])
		expr, err := ParseExpr(exprStr)
		if err != nil {
			return nil, fmt.Errorf("extend %q: %w", name, err)
		}

		if multi := expandMultiColumn(name, expr); multi != nil {
			result = append(result, multi...)
			continue
		}
		result = append(result, Assignment{Name: name, Expr: expr})
	}
	return result, nil
}

// parseJoin parses: kind=X (subquery) on col1[, col2]
// or: kind=X (subquery) on $left.A == $right.B[, ...]
func parseJoin(s string) (*JoinOp, error) {
	s = strings.TrimSpace(s)

	// Parse join kind (optional, defaults to innerunique — real ADX's own
	// default, not okql's own choice; see join.go's applyJoin for the
	// full reasoning). Verified against Microsoft's own docs before
	// changing this: "innerunique (default) — Inner join with left
	// side deduplication." A bare `join` used to default to plain
	// inner instead, silently, with no error — the exact class of
	// trap this whole codebase has spent a lot of effort closing
	// elsewhere, just discovered here via cross-model conformance
	// testing rather than a live incident. Checked blast radius before
	// changing: zero uses of bare (kind-less) join exist anywhere in
	// this repo's own tests or in either memory-scope skill's
	// documented recipes — every one already specifies kind=
	// explicitly, so this is a zero-risk correction, not a breaking
	// change to anything that currently exists.
	kind := JoinInnerUnique
	s = stripHints(s)
	if kindStr, rest, ok := splitKindProperty(s); ok {
		var err error
		kind, err = parseJoinKind(kindStr)
		if err != nil {
			return nil, err
		}
		s = stripHints(rest)
	}

	if len(s) > 0 && s[0] != '(' {
		// A bare right side: join kind=inner T2 on Key, or a function
		// or database(...) reference; it runs to the top-level "on".
		spaced := strings.Map(func(r rune) rune {
			if r == '\t' || r == '\n' || r == '\r' {
				return ' '
			}
			return r
		}, s)
		onIdx := findKeyword(spaced, " on ")
		if onIdx <= 0 {
			return nil, fmt.Errorf("join: expected 'on' clause after the right-hand table")
		}
		s = "(" + strings.TrimSpace(s[:onIdx]) + ") " + strings.TrimSpace(s[onIdx:])
	}

	// Parse (subquery) — find matching parentheses
	if len(s) == 0 || s[0] != '(' {
		return nil, fmt.Errorf("join: expected (subquery)")
	}

	depth := 0
	closeIdx := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '(' {
			depth++
		} else if s[i] == ')' {
			depth--
			if depth == 0 {
				closeIdx = i
				break
			}
		}
	}
	if closeIdx < 0 {
		return nil, fmt.Errorf("join: unmatched parenthesis in subquery")
	}

	subqueryStr := strings.TrimSpace(s[1:closeIdx])
	rest := strings.TrimSpace(s[closeIdx+1:])

	// Parse the subquery as a full query (table | operators)
	subStmt, err := parseQuery(subqueryStr)
	if err != nil {
		return nil, fmt.Errorf("join subquery: %w", err)
	}
	rightQuery, ok := subStmt.(*Query)
	if !ok {
		return nil, fmt.Errorf("join: subquery must be a tabular expression")
	}

	// Parse on clause
	if !strings.HasPrefix(keywordFold(rest), "on ") {
		return nil, fmt.Errorf("join: expected 'on' clause after subquery")
	}
	onStr := strings.TrimSpace(rest[3:])

	clauses, err := parseJoinOnClauses(onStr)
	if err != nil {
		return nil, fmt.Errorf("join on: %w", err)
	}

	return &JoinOp{
		Kind:      kind,
		Right:     rightQuery,
		OnClauses: clauses,
	}, nil
}

// splitKindProperty reads a leading kind = Name (spaces around = are
// optional, and the value ends at the first non-identifier character,
// so kind=inner(T) works), returning the name and the remaining text.
func splitKindProperty(s string) (kind, rest string, ok bool) {
	if !strings.HasPrefix(keywordFold(s), "kind") {
		return "", s, false
	}
	t := strings.TrimLeft(s[len("kind"):], " \t\r\n")
	if !strings.HasPrefix(t, "=") {
		return "", s, false
	}
	t = strings.TrimLeft(t[1:], " \t\r\n")
	end := 0
	for end < len(t) && isIdentChar(t[end]) {
		end++
	}
	if end == 0 {
		return "", s, false
	}
	return t[:end], t[end:], true
}

func parseJoinKind(s string) (JoinKind, error) {
	switch strings.ToLower(s) {
	case "inner":
		return JoinInner, nil
	case "innerunique":
		return JoinInnerUnique, nil
	case "leftouter":
		return JoinLeftOuter, nil
	case "rightouter":
		return JoinRightOuter, nil
	case "fullouter":
		return JoinFullOuter, nil
	case "leftanti", "anti":
		return JoinLeftAnti, nil
	case "leftsemi":
		return JoinLeftSemi, nil
	case "rightanti":
		return JoinRightAnti, nil
	case "rightsemi":
		return JoinRightSemi, nil
	default:
		return 0, fmt.Errorf("unknown join kind: %q", s)
	}
}

// parseJoinOnClauses parses join conditions:
//   - Simple: "Key1, Key2" (same column name on both sides)
//   - Explicit: "$left.A == $right.B, $left.C == $right.D"
func parseJoinOnClauses(s string) ([]JoinCondition, error) {
	parts := splitAndTrim(s, ',')
	var clauses []JoinCondition

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Check for $left.X == $right.Y syntax
		if strings.Contains(part, "$left.") || strings.Contains(part, "$right.") {
			eqIdx := strings.Index(part, "==")
			if eqIdx < 0 {
				return nil, fmt.Errorf("expected == in explicit join condition: %q", part)
			}
			leftExpr := strings.TrimSpace(part[:eqIdx])
			rightExpr := strings.TrimSpace(part[eqIdx+2:])

			leftCol := strings.TrimPrefix(leftExpr, "$left.")
			rightCol := strings.TrimPrefix(rightExpr, "$right.")

			// Handle reversed order: $right.X == $left.Y
			if strings.HasPrefix(leftExpr, "$right.") && strings.HasPrefix(rightExpr, "$left.") {
				leftCol = strings.TrimPrefix(rightExpr, "$left.")
				rightCol = strings.TrimPrefix(leftExpr, "$right.")
			}

			clauses = append(clauses, JoinCondition{LeftColumn: leftCol, RightColumn: rightCol})
		} else {
			// Simple: column name is the same on both sides
			clauses = append(clauses, JoinCondition{LeftColumn: part, RightColumn: part})
		}
	}

	if len(clauses) == 0 {
		return nil, fmt.Errorf("no join conditions specified")
	}
	return clauses, nil
}

// parseProjectItems parses a project item list: col1, NewCol = expr, ...
// Shared by the project operator and graph-match's project clause.
func parseProjectItems(colStr string) ([]ProjectItem, error) {
	parts := splitAndTrim(colStr, ',')
	if len(parts) == 0 {
		return nil, fmt.Errorf("no columns specified")
	}
	items := make([]ProjectItem, 0, len(parts))
	for _, part := range parts {
		eqIdx := assignmentEqIndex(part)
		if eqIdx < 0 {
			// A part with no top-level '=' is either a genuine bare
			// column reference (project X — the common case, kept
			// exactly as before: item.Expr stays nil, resolved by
			// name lookup against the input schema) or an unnamed
			// COMPUTED expression (project x + 1 — real ADX's own,
			// verified-before-adopting rule: "if ColumnName is
			// omitted, the output column name of Expression is
			// automatically generated"). isValidIdentifier
			// distinguishes the two: a real column name can never
			// itself contain an operator, space, or anything else
			// that would also make it a syntactically valid, longer
			// expression -- "X" passes, "x + 1" doesn't. Found live,
			// not hypothetical: parseAssignments (this function's own
			// sibling, shared by print/extend) already fixed to accept
			// a bare expression using this same real ADX rule; project
			// has its OWN, separate parsing function and needed the
			// identical fix applied separately, or `project x + 1`
			// would have kept failing with "column "x + 1" not
			// found" even after that fix landed.
			if isValidIdentifier(part) {
				items = append(items, ProjectItem{Name: part})
				continue
			}
			if u := unquoteIdent(part); u != part {
				items = append(items, ProjectItem{Name: u})
				continue
			}
			expr, err := ParseExpr(part)
			if err != nil {
				return nil, fmt.Errorf("%q: %w", part, err)
			}
			if multi := expandMultiColumn("", expr); multi != nil {
				for _, m := range multi {
					items = append(items, ProjectItem{Name: m.Name, Expr: m.Expr})
				}
				continue
			}
			name, _ := ExprResultName(expr) // "" → ColumnN in the engine
			items = append(items, ProjectItem{Name: name, Expr: expr, Unnamed: true})
			continue
		}
		name := unquoteIdent(part[:eqIdx])
		exprStr := strings.TrimSpace(part[eqIdx+1:])
		if name == "" || exprStr == "" {
			return nil, fmt.Errorf("expected 'Name = expr', got %q", part)
		}
		expr, err := ParseExpr(exprStr)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", name, err)
		}
		if multi := expandMultiColumn(name, expr); multi != nil {
			for _, m := range multi {
				items = append(items, ProjectItem{Name: m.Name, Expr: m.Expr})
			}
			continue
		}
		items = append(items, ProjectItem{Name: name, Expr: expr})
	}
	return items, nil
}

// parseLookup parses: lookup [kind=X] TableName on col1[, col2]
func parseLookup(s string) (*LookupOp, error) {
	s = stripHints(s)
	op := &LookupOp{Kind: JoinLeftOuter} // default kind for lookup

	// Optional kind=X
	if strings.HasPrefix(keywordFold(s), "kind=") {
		eqEnd := strings.Index(s, " ")
		if eqEnd < 0 {
			return nil, fmt.Errorf("lookup: expected table name after kind")
		}
		kindStr := s[len("kind="):eqEnd]
		k, err := parseJoinKind(kindStr)
		if err != nil {
			return nil, fmt.Errorf("lookup: %w", err)
		}
		if k != JoinLeftOuter && k != JoinInner {
			return nil, fmt.Errorf("lookup: kind must be leftouter or inner, got %s", kindStr)
		}
		op.Kind = k
		s = stripHints(s[eqEnd:])
	}

	// Right side: a table name, or a parenthesised table name or
	// subquery. The parenthesised form is bracket-matched, so an "on"
	// inside the subquery (a nested join or lookup) is not mistaken
	// for this lookup's own.
	var right, onPart string
	if strings.HasPrefix(s, "(") {
		closeIdx := matchingClose(s, 0, '(', ')')
		if closeIdx < 0 {
			return nil, fmt.Errorf("lookup: unmatched parenthesis in right side")
		}
		right = strings.TrimSpace(s[1:closeIdx])
		rest := strings.TrimSpace(s[closeIdx+1:])
		if !strings.HasPrefix(keywordFold(rest), "on ") {
			return nil, fmt.Errorf("lookup: expected 'on' clause after right side")
		}
		onPart = strings.TrimSpace(rest[3:])
	} else {
		onIdx := findKeyword(s, " on ")
		if onIdx < 0 {
			return nil, fmt.Errorf("lookup: expected 'on' clause")
		}
		right = strings.TrimSpace(s[:onIdx])
		onPart = strings.TrimSpace(s[onIdx+4:])
	}
	if isIdentifier(right) {
		op.TableName = right
	} else {
		stmt, err := parseQuery(right)
		if err != nil {
			return nil, fmt.Errorf("lookup right side: %w", err)
		}
		q, ok := stmt.(*Query)
		if !ok {
			return nil, fmt.Errorf("lookup: right side must be a tabular expression")
		}
		op.Right = q
	}

	clauses, err := parseJoinOnClauses(onPart)
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	op.OnClauses = clauses

	return op, nil
}

// parseSearchStatement parses the source form
// "search [kind=...] [in (T1, T2, ...)] Predicate" and any subsequent
// pipeline operators (Source: "", first operator carries the payload).
func parseSearchStatement(s string, remaining []string) (Statement, error) {
	op, err := parseSearchOp(s, false)
	if err != nil {
		return nil, err
	}
	query := &Query{Source: "", Operators: []Operator{op}}
	for i, seg := range remaining {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		subOp, err := parseOperator(seg)
		if err != nil {
			return nil, fmt.Errorf("search: pipeline operator %d: %w", i+1, err)
		}
		query.Operators = append(query.Operators, subOp)
	}
	return query, nil
}

// parseSearchOp parses everything after the search keyword.
func parseSearchOp(s string, piped bool) (*SearchOp, error) {
	s = strings.TrimSpace(s)
	op := &SearchOp{Piped: piped}
	lower := keywordFold(s)
	if strings.HasPrefix(lower, "kind") {
		rest := strings.TrimSpace(s[len("kind"):])
		if !strings.HasPrefix(rest, "=") {
			return nil, fmt.Errorf("search: expected kind=...")
		}
		rest = strings.TrimSpace(rest[1:])
		end := strings.IndexAny(rest, " \t")
		if end < 0 {
			end = len(rest)
		}
		switch strings.ToLower(rest[:end]) {
		case "case_sensitive":
			op.CaseSens = true
		case "case_insensitive", "default":
		default:
			return nil, fmt.Errorf("search: unknown kind %q (case_sensitive, case_insensitive, default)", rest[:end])
		}
		s = strings.TrimSpace(rest[end:])
		lower = keywordFold(s)
	}
	if strings.HasPrefix(lower, "in ") || strings.HasPrefix(lower, "in(") {
		if piped {
			return nil, fmt.Errorf("search: in (...) cannot be combined with a piped input")
		}
		rest := strings.TrimSpace(s[2:])
		if !strings.HasPrefix(rest, "(") {
			return nil, fmt.Errorf("search: expected '(' after 'in'")
		}
		closeIdx := strings.IndexByte(rest, ')')
		if closeIdx < 0 {
			return nil, fmt.Errorf("search: unmatched '(' in 'in (...)'")
		}
		for _, t := range strings.Split(rest[1:closeIdx], ",") {
			if t = strings.TrimSpace(t); t != "" {
				op.Tables = append(op.Tables, t)
			}
		}
		s = strings.TrimSpace(rest[closeIdx+1:])
	}
	if s == "" {
		return nil, fmt.Errorf("search: missing predicate")
	}
	pred, err := parseSearchPredicate(s, op.CaseSens)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	op.Predicate = pred
	return op, nil
}

// parseSearchPredicate parses a search predicate: `*` (every row), or
// a boolean expression whose bare string terms mean "any column has
// the term" and whose Col:"term" pairs mean "Col has the term".
func parseSearchPredicate(s string, caseSens bool) (Expr, error) {
	if strings.TrimSpace(s) == "*" {
		return &Literal{Value: true}, nil
	}
	expr, err := ParseExpr(rewriteSearchColumnTerms(s, caseSens))
	if err != nil {
		return nil, err
	}
	return searchTermsToCalls(expr, caseSens), nil
}

// rewriteSearchColumnTerms turns Col:"term" (not valid KQL expression
// syntax) into SearchColFunc(Col, "term", cs), leaving string literals
// untouched.
func rewriteSearchColumnTerms(s string, caseSens bool) string {
	cs := "false"
	if caseSens {
		cs = "true"
	}
	var b strings.Builder
	isIdent := func(c byte) bool {
		return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
	}
	for i := 0; i < len(s); {
		c := s[i]
		if c == '"' || c == '\'' {
			j := skipQuoted(s, i)
			b.WriteString(s[i:j])
			i = j
			continue
		}
		if isIdent(c) && (i == 0 || !isIdent(s[i-1])) {
			j := i
			for j < len(s) && isIdent(s[j]) {
				j++
			}
			k := j
			for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
				k++
			}
			if k < len(s) && s[k] == ':' {
				m := k + 1
				for m < len(s) && (s[m] == ' ' || s[m] == '\t') {
					m++
				}
				if m < len(s) && (s[m] == '"' || s[m] == '\'') {
					end := skipQuoted(s, m)
					b.WriteString(SearchColFunc + "(" + s[i:j] + ", " + s[m:end] + ", " + cs + ")")
					i = end
					continue
				}
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// skipQuoted returns the index just past the string literal starting at
// s[i] (a quote), honouring backslash escapes.
func skipQuoted(s string, i int) int {
	q := s[i]
	j := i + 1
	for j < len(s) {
		if s[j] == '\\' {
			j += 2
			continue
		}
		if s[j] == q {
			return j + 1
		}
		j++
	}
	return len(s)
}

// searchTermsToCalls replaces bare string literals in boolean
// positions (the whole predicate, operands of and/or, the operand of
// not) with SearchAnyFunc calls.
func searchTermsToCalls(expr Expr, caseSens bool) Expr {
	switch e := expr.(type) {
	case *Literal:
		if _, ok := e.Value.(string); ok {
			return &FuncCall{Name: SearchAnyFunc, RawName: SearchAnyFunc, Args: []Expr{e, &Literal{Value: caseSens}}}
		}
	case *BinaryExpr:
		if e.Op == OpAnd || e.Op == OpOr {
			return &BinaryExpr{Left: searchTermsToCalls(e.Left, caseSens), Op: e.Op, Right: searchTermsToCalls(e.Right, caseSens)}
		}
	case *UnaryExpr:
		if e.Op == "not" {
			return &UnaryExpr{Op: e.Op, Expr: searchTermsToCalls(e.Expr, caseSens)}
		}
	case *FuncCall:
		if e.Name == "not" && len(e.Args) == 1 {
			cp := *e
			cp.Args = []Expr{searchTermsToCalls(e.Args[0], caseSens)}
			return &cp
		}
	}
	return expr
}

// parseFindStatement parses find's two real-ADX documented grammar
// forms — verified against Microsoft's own find operator docs before
// adopting this shape, including a real, easy-to-miss distinction:
// "find [withsource=ColumnName] [in (Tables)] where Predicate
// [project-smart | project ...]" REQUIRES the where keyword whenever
// withsource or in(...) is present, but the shorter
// "find Predicate [project ...]" form has NO where keyword at all.
func parseFindStatement(s string, remaining []string) (Statement, error) {
	s = strings.TrimSpace(s)
	op := &FindOp{ProjectSmart: true}

	sawWithsourceOrIn := false

	if strings.HasPrefix(keywordFold(s), "withsource=") {
		rest := s[len("withsource="):]
		fields := strings.Fields(rest)
		if len(fields) == 0 || !isValidIdentifier(fields[0]) {
			return nil, fmt.Errorf("find: expected a column name after 'withsource='")
		}
		op.WithSource = fields[0]
		s = strings.TrimSpace(rest[len(fields[0]):])
		sawWithsourceOrIn = true
	}

	lowerS := keywordFold(s)
	if strings.HasPrefix(lowerS, "in ") || strings.HasPrefix(lowerS, "in(") {
		rest := strings.TrimSpace(s[2:])
		if !strings.HasPrefix(rest, "(") {
			return nil, fmt.Errorf("find: expected '(' after 'in'")
		}
		closeIdx := findMatchingParen(rest, 0)
		if closeIdx < 0 {
			return nil, fmt.Errorf("find: unmatched '(' in 'in (...)'")
		}
		for _, t := range splitRespectingParens(rest[1:closeIdx], ',') {
			t = strings.TrimSpace(t)
			if t != "" {
				op.Tables = append(op.Tables, t)
			}
		}
		s = strings.TrimSpace(rest[closeIdx+1:])
		sawWithsourceOrIn = true
	}

	if sawWithsourceOrIn {
		if !strings.HasPrefix(keywordFold(s), "where ") {
			return nil, fmt.Errorf("find: expected 'where' after withsource=/in(...)")
		}
		s = strings.TrimSpace(s[len("where "):])
	} else if strings.HasPrefix(keywordFold(s), "where ") {
		// Lenient extension: an explicit "where" is also accepted even
		// without withsource=/in(...), a harmless superset of the
		// shorter documented form rather than a strict rejection of it.
		s = strings.TrimSpace(s[len("where "):])
	}

	predText := s
	projectText := ""
	if idx := findTopLevelKeyword(s, "project-smart"); idx >= 0 {
		predText = strings.TrimSpace(s[:idx])
		projectText = strings.TrimSpace(s[idx:])
	} else if idx := findTopLevelKeyword(s, "project"); idx >= 0 {
		predText = strings.TrimSpace(s[:idx])
		projectText = strings.TrimSpace(s[idx:])
	}

	if predText == "" {
		return nil, fmt.Errorf("find: expected a predicate")
	}

	// The "* has term" / bare-term forms search every column of each
	// row rather than evaluate a normal, column-bound boolean
	// expression — not representable as an ordinary Expr (there's no
	// valid ColumnRef for the literal "*" token), so detected and
	// handled specially here rather than via ParseExpr at all.
	lowerPred := strings.ToLower(predText)
	if strings.HasPrefix(lowerPred, "* has ") {
		termExpr, err := ParseExpr(strings.TrimSpace(predText[len("* has "):]))
		if err != nil {
			return nil, fmt.Errorf("find: %w", err)
		}
		lit, ok := termExpr.(*Literal)
		if !ok {
			return nil, fmt.Errorf("find: expected a quoted string literal after '* has'")
		}
		term, ok := lit.Value.(string)
		if !ok {
			return nil, fmt.Errorf("find: expected a string literal after '* has'")
		}
		op.AnyColumnTerm = term
	} else if len(predText) >= 2 && predText[0] == '"' {
		// Bare quoted term with no comparison operator at all
		// (find "Hernandez") — same any-column search as "* has term".
		termExpr, err := ParseExpr(predText)
		if err != nil {
			return nil, fmt.Errorf("find: %w", err)
		}
		if lit, ok := termExpr.(*Literal); ok {
			if term, ok := lit.Value.(string); ok {
				op.AnyColumnTerm = term
			}
		}
		if op.AnyColumnTerm == "" {
			return nil, fmt.Errorf("find: expected a quoted string literal")
		}
	} else {
		predExpr, err := ParseExpr(predText)
		if err != nil {
			return nil, fmt.Errorf("find: predicate: %w", err)
		}
		op.Predicate = predExpr
	}

	if projectText != "" {
		if strings.HasPrefix(keywordFold(projectText), "project-smart") {
			op.ProjectSmart = true
		} else {
			op.ProjectSmart = false
			listText := strings.TrimSpace(projectText[len("project"):])
			for _, part := range splitRespectingParens(listText, ',') {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				if strings.EqualFold(part, "pack_all()") {
					op.PackAll = true
					continue
				}
				name := part
				var colType types.KQLType
				if colonIdx := strings.Index(part, ":"); colonIdx >= 0 {
					name = strings.TrimSpace(part[:colonIdx])
					typeName := strings.TrimSpace(part[colonIdx+1:])
					t, err := types.ParseType(typeName)
					if err != nil {
						return nil, fmt.Errorf("find: project %q: %w", part, err)
					}
					colType = t
				}
				op.ProjectItems = append(op.ProjectItems, FindProjectItem{Name: name, Type: colType})
			}
		}
	}

	query := &Query{Source: "", Operators: []Operator{op}}
	for i, seg := range remaining {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		subOp, err := parseOperator(seg)
		if err != nil {
			return nil, fmt.Errorf("find: pipeline operator %d: %w", i+1, err)
		}
		query.Operators = append(query.Operators, subOp)
	}
	return query, nil
}

// IsPercentileAgg reports whether f is percentile, percentiles,
// percentilew or percentilesw (not the _array forms), which name each
// output column percentile_<Column>_<P>.
func IsPercentileAgg(f string) bool {
	return f == "percentile" || f == "percentiles" || f == "percentilew" || f == "percentilesw"
}

// PercentileAutoName is the generated name of the i-th percentile
// column of agg: percentile_<Column>_<P>, the column empty when the
// value is not a plain column reference, and "." in P written "_"
// (percentile(x, 99.5) is percentile_x_99_5).
func PercentileAutoName(agg *Aggregation, i int) string {
	col := ""
	if len(agg.Args) > 0 {
		if ref, ok := agg.Args[0].(*ColumnRef); ok {
			col = ref.Name
		}
	}
	first := 1
	if agg.Function == "percentilew" || agg.Function == "percentilesw" {
		first = 2
	}
	pct := ""
	if first+i < len(agg.Args) {
		if lit, ok := agg.Args[first+i].(*Literal); ok {
			switch v := lit.Value.(type) {
			case int64:
				pct = strconv.FormatInt(v, 10)
			case float64:
				pct = strings.ReplaceAll(strconv.FormatFloat(v, 'f', -1, 64), ".", "_")
			}
		}
	}
	return "percentile_" + col + "_" + pct
}

// isSingleAggregateCall reports whether s is exactly one call of an
// aggregation function: name(args), the closing paren ending s.
func isSingleAggregateCall(s string) bool {
	open := strings.IndexByte(s, '(')
	if open <= 0 {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(s[:open]))
	if !isIdentifier(name) || !IsAggregateFunction(name) {
		return false
	}
	return matchingClose(s, open, '(', ')') == len(s)-1
}

// parsePostAggregation parses an expression over aggregates: each
// aggregate call becomes a hidden Inner aggregation, referenced from
// Post by its hidden column name.
func parsePostAggregation(s, name string) (*Aggregation, error) {
	expr, err := ParseExpr(s)
	if err != nil {
		return nil, fmt.Errorf("summarize: %w", err)
	}
	agg := &Aggregation{Name: name, Function: PostAggregation}
	var rewrite func(e Expr) (Expr, error)
	rewrite = func(e Expr) (Expr, error) {
		switch x := e.(type) {
		case *FuncCall:
			if isAggregateCall(x) {
				for _, a := range x.Args {
					if containsAggregateCall(a) {
						return nil, fmt.Errorf("summarize: aggregates cannot be nested in %s()", x.Name)
					}
				}
				hidden := fmt.Sprintf("%s%d", postAggPrefix, len(agg.Inner))
				agg.Inner = append(agg.Inner, Aggregation{Name: hidden, Function: x.Name, Args: x.Args})
				return &ColumnRef{Name: hidden}, nil
			}
			c := *x
			c.Args = make([]Expr, len(x.Args))
			for i, a := range x.Args {
				na, err := rewrite(a)
				if err != nil {
					return nil, err
				}
				c.Args[i] = na
			}
			return &c, nil
		case *BinaryExpr:
			l, err := rewrite(x.Left)
			if err != nil {
				return nil, err
			}
			r, err := rewrite(x.Right)
			if err != nil {
				return nil, err
			}
			c := *x
			c.Left, c.Right = l, r
			return &c, nil
		case *UnaryExpr:
			in, err := rewrite(x.Expr)
			if err != nil {
				return nil, err
			}
			c := *x
			c.Expr = in
			return &c, nil
		case *Literal:
			return x, nil
		case *ColumnRef:
			// A name outside any aggregate: only a let scalar or a
			// function parameter is valid here (summarize min(t) -
			// hours*1h), which the parser cannot tell from a column,
			// so it is resolved when the row is evaluated.
			return x, nil
		}
		return nil, fmt.Errorf("summarize: unsupported expression in an aggregation: %s", s)
	}
	post, err := rewrite(expr)
	if err != nil {
		return nil, err
	}
	if len(agg.Inner) == 0 {
		return nil, fmt.Errorf("summarize: %q contains no aggregation function", s)
	}
	agg.Post = post
	if agg.Name == "" {
		agg.AutoNamed = true
		agg.Name, _ = postAggResultName(expr) // "" → ColumnN
	}
	return agg, nil
}

// isAggregateCall reports whether fc is an aggregate call inside an
// expression. hll_merge and tdigest_merge are also scalar functions
// (merging two or more values), so with several arguments they are the
// scalar.
func isAggregateCall(fc *FuncCall) bool {
	if !IsAggregateFunction(fc.Name) {
		return false
	}
	switch fc.Name {
	case "hll_merge", "tdigest_merge", "merge_tdigest":
		return len(fc.Args) <= 1
	}
	return true
}

// containsAggregateCall reports whether e calls an aggregate anywhere.
func containsAggregateCall(e Expr) bool {
	switch x := e.(type) {
	case *FuncCall:
		if isAggregateCall(x) {
			return true
		}
		for _, a := range x.Args {
			if containsAggregateCall(a) {
				return true
			}
		}
	case *BinaryExpr:
		return containsAggregateCall(x.Left) || containsAggregateCall(x.Right)
	case *UnaryExpr:
		return containsAggregateCall(x.Expr)
	}
	return false
}

// postAggResultName names an unnamed expression over aggregates as
// Kusto.Language does: toint(sum(b)) → sum_b; count() * 2 → none.
func postAggResultName(e Expr) (string, bool) {
	fc, ok := e.(*FuncCall)
	if !ok {
		return "", false
	}
	if IsAggregateFunction(fc.Name) {
		return AggResultName(fc)
	}
	rn, ok := scalarResultNames[fc.Name]
	if !ok || len(fc.Args) == 0 {
		return "", false
	}
	arg := func() (string, bool) {
		if inner, ok := fc.Args[0].(*FuncCall); ok && IsAggregateFunction(inner.Name) {
			return AggResultName(inner)
		}
		return exprResultName(fc.Args[0], scalarResultNames)
	}
	switch rn.kind {
	case ResultFirstArgument:
		return arg()
	case ResultOnlyArgument:
		if len(fc.Args) == 1 {
			return arg()
		}
	}
	return "", false
}

// renameColumnRefs returns e with column references renamed per m.
func renameColumnRefs(e Expr, m map[string]string) Expr {
	switch x := e.(type) {
	case *ColumnRef:
		if n, ok := m[x.Name]; ok {
			return &ColumnRef{Name: n}
		}
		return x
	case *FuncCall:
		c := *x
		c.Args = make([]Expr, len(x.Args))
		for i, a := range x.Args {
			c.Args[i] = renameColumnRefs(a, m)
		}
		return &c
	case *BinaryExpr:
		c := *x
		c.Left, c.Right = renameColumnRefs(x.Left, m), renameColumnRefs(x.Right, m)
		return &c
	case *UnaryExpr:
		c := *x
		c.Expr = renameColumnRefs(x.Expr, m)
		return &c
	}
	return e
}

// parseReduce parses the text after `reduce `.
func parseReduce(rest string) (Operator, error) {
	op := &ReduceOp{Threshold: 0.1}
	if l := strings.ToLower(rest); strings.HasPrefix(l, "kind") {
		eq := strings.Index(rest, "=")
		if eq < 0 {
			return nil, fmt.Errorf("reduce: expected kind=source")
		}
		after := strings.TrimSpace(rest[eq+1:])
		f := strings.Fields(after)
		if len(f) == 0 || strings.ToLower(f[0]) != "source" {
			return nil, fmt.Errorf("reduce: the only kind is source")
		}
		op.Source = true
		rest = strings.TrimSpace(after[len(f[0]):])
	}
	if !strings.HasPrefix(strings.ToLower(rest), "by ") {
		return nil, fmt.Errorf("reduce: expected 'by Expr'")
	}
	rest = strings.TrimSpace(rest[3:])
	exprText, with := rest, ""
	if i := findKeyword(rest, "with"); i >= 0 {
		exprText, with = strings.TrimSpace(rest[:i]), strings.TrimSpace(rest[i+len("with"):])
	}
	expr, err := ParseExpr(exprText)
	if err != nil {
		return nil, fmt.Errorf("reduce: %w", err)
	}
	op.By = expr
	for _, part := range splitTopLevelCommas(with) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.Index(part, "=")
		if eq < 0 {
			return nil, fmt.Errorf("reduce: expected name=value in with(...), got %q", part)
		}
		name, val := strings.ToLower(strings.TrimSpace(part[:eq])), strings.TrimSpace(part[eq+1:])
		v, err := ParseExpr(val)
		if err != nil {
			return nil, fmt.Errorf("reduce: %s: %w", name, err)
		}
		lit, ok := v.(*Literal)
		if !ok {
			return nil, fmt.Errorf("reduce: %s must be a constant", name)
		}
		switch name {
		case "threshold":
			f, ok := toFloatLiteral(lit.Value)
			if !ok || f < 0 || f > 1 {
				return nil, fmt.Errorf("reduce: threshold must be between 0 and 1")
			}
			op.Threshold = f
		case "characters":
			cs, ok := lit.Value.(string)
			if !ok {
				return nil, fmt.Errorf("reduce: characters must be a string")
			}
			op.Characters, op.HasChars = cs, true
		default:
			return nil, fmt.Errorf("reduce: unknown option %q (threshold, characters)", name)
		}
	}
	return op, nil
}

func toFloatLiteral(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	}
	return 0, false
}
