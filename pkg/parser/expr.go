package parser

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// --- Expression Parsing ---

// ParseExpr parses a KQL expression (predicate, computed value).
func ParseExpr(input string) (Expr, error) {
	p := &exprParser{input: strings.TrimSpace(input), pos: 0}
	expr, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	p.skipWhitespace()
	if p.pos < len(p.input) {
		return nil, fmt.Errorf("unexpected trailing text: %q", p.input[p.pos:])
	}
	return expr, nil
}

type exprParser struct {
	input string
	pos   int
	// consumedBareLiteral holds the raw text matched by the most
	// recent tryConsumeBareDateTimeLiteral call.
	consumedBareLiteral string
}

// bareDateTimeRe matches an unquoted date/datetime literal shape:
// 2026-02-28, 2026-02-28T10:30 (no seconds), or
// 2026-02-28T10:30:00[.fff][Z]. Deliberately strict (4-digit year
// first) so it can never be mistaken for an identifier or an
// arithmetic expression.
//
// Seconds are genuinely optional, not an oversight — verified against
// Microsoft's own datetime-data-type docs before fixing: the
// "strongly recommended" ISO 8601 format table explicitly lists
// %Y-%m-%dT%H:%M (e.g. "2014-05-25T08:20") as its own valid, distinct
// format alongside the with-seconds one, not a typo or an
// abbreviation of it. An earlier version of this regex required
// \d{2}:\d{2}:\d{2} unconditionally, so datetime(2014-05-25T08:20)
// failed to parse at all — found while checking whether this codebase
// replicates real Kusto's UTC-only datetime convention, which led to
// fetching the authoritative format list directly rather than relying
// on the smaller, indirect set of formats already known about.
var bareDateTimeRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}([ T]\d{2}:\d{2}(:\d{2}(\.\d+)?)?Z?)?`)

// tryConsumeBareDateTimeLiteral checks whether the input at the current
// position is a bare date/datetime literal immediately followed
// (modulo whitespace) by ')'. If so, consumes it (not the paren) and
// records it in consumedBareLiteral. Requiring the immediate ')' means
// this only ever fires on the exact shape real Kusto treats as a date
// literal in this position — datetime(someColumn) and datetime(a - b)
// are unaffected, since neither starts with four digits, and any
// trailing content before ')' (other than optional time-of-day) makes
// the match fail closed, falling through to normal expression parsing.
// tryConsumeBareJSONLiteral checks whether the input at the current
// position is a JSON array/object literal ([...] or {...}, brace/
// bracket-balanced, respecting quoted strings so commas or brackets
// inside a JSON string don't confuse depth counting) immediately
// followed (modulo whitespace) by ')'. Matches real Kusto's
// dynamic(...) grammar: the content is JSON, not a KQL expression list
// — dynamic([1,2,3]) and dynamic({"a":1}) are literal-defining syntax,
// not a function call over evaluated KQL sub-expressions. On success,
// consumes the literal (not the closing paren) and validates it is
// well-formed JSON, returning a clear parse-time error otherwise
// rather than deferring to a runtime failure.
func (p *exprParser) tryConsumeBareJSONLiteral() (string, error, bool) {
	rest := p.input[p.pos:]
	if len(rest) == 0 || (rest[0] != '[' && rest[0] != '{') {
		return "", nil, false
	}
	open, close := rest[0], byte(']')
	if open == '{' {
		close = '}'
	}
	depth := 0
	inStr := false
	escaped := false
	end := -1
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if inStr {
			if escaped {
				escaped = false
			} else if c == 92 { // backslash
				escaped = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				end = i + 1
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return "", fmt.Errorf("dynamic(...): unterminated %c...%c literal", open, close), true
	}
	matched := rest[:end]
	after := end
	for after < len(rest) && (rest[after] == ' ' || rest[after] == '	') {
		after++
	}
	if after >= len(rest) || rest[after] != ')' {
		// Doesn't immediately close — not the literal form; fall
		// through to normal expression parsing rather than erroring,
		// in case this shape is ever legitimately something else.
		return "", nil, false
	}
	// Normalize single-quoted JSON strings/keys to double-quoted ones
	// BEFORE strict validation — verified as a real, recurring gap
	// during this session's own testing, not a hypothetical: real
	// ADX's own documentation examples for several functions
	// (bag_merge, bag_remove_keys, has_any_index, among others) freely
	// use single quotes inside dynamic({...}) literals
	// (dynamic({'A1':12}), dynamic(['a', 'b'])) as ordinary, valid KQL
	// grammar — strict JSON (RFC 8259, which Go's own encoding/json
	// enforces via json.Valid below) never permits single quotes at
	// all, so a direct copy-paste of those real, documented examples
	// failed here with "not valid JSON" until requoted by hand.
	// Normalizing once, here, at parse time is sufficient: this
	// engine always stores a dynamic value as JSON-encoded text
	// internally from this point forward, so every downstream
	// consumer (parseJSONObject, parseJSONArray, ...) already only
	// ever sees the normalized, valid form — no second fix needed at
	// any of those call sites.
	normalized := normalizeSingleQuotedJSON(matched)
	if !json.Valid([]byte(normalized)) {
		return "", fmt.Errorf("dynamic(%s): not valid JSON", matched), true
	}
	p.pos += end
	return normalized, nil, true
}

// normalizeSingleQuotedJSON rewrites single-quoted strings in s
// (real ADX's own tolerated dynamic(...) literal grammar) into
// properly double-quoted, strict-JSON strings — a single-quoted
// string's own literal double-quote characters are escaped ("), and
// its own escaped single quotes (') are unescaped to a bare ' (no
// longer needing escaping once the outer delimiter is now "). A
// double-quoted string already present in s (and anything outside any
// string at all — {, }, [, ], :, ,, whitespace, bare numbers/
// true/false/null) passes through completely unchanged; this function
// only ever rewrites content it finds strictly BETWEEN a pair of
// single quotes.
func normalizeSingleQuotedJSON(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		switch c {
		case '"':
			// An existing double-quoted string: copy through verbatim,
			// respecting its own backslash escapes, so a " inside it
			// is never mistaken for the string's own closing quote.
			out.WriteByte(c)
			i++
			for i < len(s) {
				out.WriteByte(s[i])
				if s[i] == '\\' && i+1 < len(s) {
					i++
					out.WriteByte(s[i])
					i++
					continue
				}
				if s[i] == '"' {
					i++
					break
				}
				i++
			}
		case '\'':
			// A single-quoted string: re-emit as a double-quoted one,
			// escaping any literal " found inside it and unescaping
			// any \' (no longer needed once the delimiter itself is
			// now ", not ').
			out.WriteByte('"')
			i++
			for i < len(s) {
				if s[i] == '\\' && i+1 < len(s) && s[i+1] == '\'' {
					out.WriteByte('\'')
					i += 2
					continue
				}
				if s[i] == '"' {
					out.WriteString(`\"`)
					i++
					continue
				}
				if s[i] == '\'' {
					i++
					break
				}
				out.WriteByte(s[i])
				i++
			}
			out.WriteByte('"')
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

func (p *exprParser) tryConsumeBareDateTimeLiteral() bool {
	rest := p.input[p.pos:]
	loc := bareDateTimeRe.FindStringIndex(rest)
	if loc == nil || loc[0] != 0 {
		return false
	}
	after := loc[1]
	for after < len(rest) && (rest[after] == ' ' || rest[after] == '	') {
		after++
	}
	if after >= len(rest) || rest[after] != ')' {
		return false
	}
	p.consumedBareLiteral = rest[:loc[1]]
	p.pos += loc[1]
	return true
}

func (p *exprParser) parseOr() (Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}

	for {
		p.skipWhitespace()
		if p.matchKeyword("or") {
			right, err := p.parseAnd()
			if err != nil {
				return nil, err
			}
			left = &BinaryExpr{Left: left, Op: OpOr, Right: right}
		} else {
			break
		}
	}
	return left, nil
}

func (p *exprParser) parseAnd() (Expr, error) {
	left, err := p.parseComparison()
	if err != nil {
		return nil, err
	}

	for {
		p.skipWhitespace()
		if p.matchKeyword("and") {
			right, err := p.parseComparison()
			if err != nil {
				return nil, err
			}
			left = &BinaryExpr{Left: left, Op: OpAnd, Right: right}
		} else {
			break
		}
	}
	return left, nil
}

func (p *exprParser) parseComparison() (Expr, error) {
	left, err := p.parseAddSub()
	if err != nil {
		return nil, err
	}

	p.skipWhitespace()

	// Check for in / !in / in~ / !in~ — need special parenthesized-list parsing
	negated := false
	caseInsensitive := false
	isIn := false
	savedPos := p.pos
	if p.matchKeyword("!in~") {
		negated = true
		caseInsensitive = true
		isIn = true
	} else if p.matchKeyword("!in") {
		negated = true
		isIn = true
	} else if p.matchKeyword("in~") {
		caseInsensitive = true
		isIn = true
	} else if p.matchKeyword("in") {
		negated = false
		isIn = true
	}

	if isIn {
		p.skipWhitespace()
		if p.pos >= len(p.input) || p.input[p.pos] != '(' {
			// Not followed by '(' — backtrack, not an in-expression
			p.pos = savedPos
			isIn = false
		}
	}

	if isIn {
		// Capture the full, raw content between the parens FIRST, via
		// findMatchingParen — which only reads indices, never mutates
		// p.pos except to skip past the whole (...) block at the very
		// end, below. This matters specifically because "X in
		// (subquery)" (real ADX's own tabular-subquery form of in,
		// verified before adopting this shape) is detected by TRYING
		// the ordinary scalar comma-list parse first and falling back
		// only if that fails — capturing the raw text up front means
		// that attempt runs against an ISOLATED, fresh sub-parse (via
		// the public ParseExpr entry point below), which can never
		// leave this outer parser's own p.pos in a partially-advanced,
		// inconsistent state if the scalar attempt fails partway
		// through. Purely additive: every existing, already-working
		// case (a real scalar list, or a single bare identifier
		// treated as a let-bound TableRef) still succeeds at the
		// scalar attempt exactly as before and never reaches the
		// subquery fallback at all.
		openIdx := p.pos
		closeIdx := findMatchingParen(p.input, openIdx)
		if closeIdx < 0 {
			return nil, fmt.Errorf("in list: unterminated (...)")
		}
		inner := strings.TrimSpace(p.input[openIdx+1 : closeIdx])
		p.pos = closeIdx + 1

		inExpr := &InExpr{Column: left, Negated: negated, CaseInsensitive: caseInsensitive}

		if inner == "" {
			return nil, fmt.Errorf("in list: expected at least one value")
		}

		var values []Expr
		scalarOK := true
		for _, part := range splitRespectingParens(inner, ',') {
			part = strings.TrimSpace(part)
			if part == "" {
				scalarOK = false
				break
			}
			val, err := ParseExpr(part)
			if err != nil {
				scalarOK = false
				break
			}
			values = append(values, val)
		}

		if !scalarOK {
			// The ordinary scalar parse failed somewhere — treat the
			// WHOLE content as a single tabular subquery (never
			// comma-split for this path, since a tabular pipeline
			// isn't meant to be split on commas at all; a query like
			// "T | summarize make_list(x)" could legitimately contain
			// a comma inside a function call, which splitRespectingParens
			// already handles correctly by not splitting inside
			// nested parens, but the point stands that this fallback
			// treats inner as one indivisible unit either way).
			inExpr.SubqueryText = inner
			return inExpr, nil
		}

		// Single identifier reference → could be a let-bound table
		if len(values) == 1 {
			if ref, ok := values[0].(*ColumnRef); ok {
				inExpr.TableRef = ref.Name
				return inExpr, nil
			}
		}

		inExpr.Values = values
		return inExpr, nil
	}

	// Check for has_any / has_all — syntax: expr has_any (term1, term2, ...)
	isHasAny := false
	hasAll := false
	hasAnySavedPos := p.pos
	if p.matchKeyword("has_all") {
		hasAll = true
		isHasAny = true
	} else if p.matchKeyword("has_any") {
		isHasAny = true
	}

	if isHasAny {
		p.skipWhitespace()
		if p.pos >= len(p.input) || p.input[p.pos] != '(' {
			p.pos = hasAnySavedPos
			isHasAny = false
		}
	}

	if isHasAny {
		p.pos++ // skip '('
		p.skipWhitespace()

		var values []Expr
		for {
			p.skipWhitespace()
			if p.pos < len(p.input) && p.input[p.pos] == ')' {
				p.pos++
				break
			}
			if len(values) > 0 {
				if p.pos < len(p.input) && p.input[p.pos] == ',' {
					p.pos++
					p.skipWhitespace()
				}
			}
			val, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			values = append(values, val)
		}

		return &HasAnyAllExpr{Column: left, Values: values, All: hasAll}, nil
	}

	// Check for between / !between — syntax: expr between (low .. high)
	isBetween := false
	betweenNegated := false
	betweenSavedPos := p.pos
	if p.matchKeyword("!between") {
		betweenNegated = true
		isBetween = true
	} else if p.matchKeyword("between") {
		isBetween = true
	}

	if isBetween {
		p.skipWhitespace()
		if p.pos >= len(p.input) || p.input[p.pos] != '(' {
			p.pos = betweenSavedPos
			isBetween = false
		}
	}

	if isBetween {
		p.pos++ // skip '('
		p.skipWhitespace()

		low, err := p.parseAddSub()
		if err != nil {
			return nil, fmt.Errorf("between low: %w", err)
		}
		p.skipWhitespace()

		// Expect '..'
		if p.pos+1 >= len(p.input) || p.input[p.pos:p.pos+2] != ".." {
			return nil, fmt.Errorf("expected '..' in between expression")
		}
		p.pos += 2
		p.skipWhitespace()

		high, err := p.parseAddSub()
		if err != nil {
			return nil, fmt.Errorf("between high: %w", err)
		}
		p.skipWhitespace()

		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return nil, fmt.Errorf("expected ')' in between expression")
		}
		p.pos++

		return &BetweenExpr{Expr: left, Low: low, High: high, Negated: betweenNegated}, nil
	}

	// Try other comparison operators
	if op, ok := p.matchComparisonOp(); ok {
		right, err := p.parseAddSub()
		if err != nil {
			return nil, err
		}
		return &BinaryExpr{Left: left, Op: op, Right: right}, nil
	}

	return left, nil
}

func (p *exprParser) matchComparisonOp() (BinaryOp, bool) {
	p.skipWhitespace()

	// Two-character operators first
	if p.pos+1 < len(p.input) {
		two := p.input[p.pos : p.pos+2]
		switch two {
		case "==":
			p.pos += 2
			return OpEQ, true
		case "=~":
			p.pos += 2
			return OpCIEQ, true
		case "!~":
			p.pos += 2
			return OpCINEQ, true
		case "!=":
			p.pos += 2
			return OpNEQ, true
		case "<=":
			p.pos += 2
			return OpLTE, true
		case ">=":
			p.pos += 2
			return OpGTE, true
		}
	}

	// Single-character operators
	if p.pos < len(p.input) {
		switch p.input[p.pos] {
		case '<':
			p.pos++
			return OpLT, true
		case '>':
			p.pos++
			return OpGT, true
		}
	}

	// Keyword operators
	for _, kw := range []struct {
		word string
		op   BinaryOp
	}{
		// Order matters: longer prefixes first to avoid partial matches
		{"!contains_cs", OpNotContainsCS},
		{"!contains", OpNotContains},
		{"contains_cs", OpContainsCS},
		{"contains", OpContains},
		{"!has_cs", OpNotHasCS},
		{"!has", OpNotHas},
		{"has_cs", OpHasCS},
		{"has", OpHas},
		{"!startswith_cs", OpNotStartsWithCS},
		{"!startswith", OpNotStartsWith},
		{"startswith_cs", OpStartsWithCS},
		{"startswith", OpStartsWith},
		{"!endswith_cs", OpNotEndsWithCS},
		{"!endswith", OpNotEndsWith},
		{"endswith_cs", OpEndsWithCS},
		{"endswith", OpEndsWith},
		{"!hasprefix_cs", OpNotHasPrefixCS},
		{"!hasprefix", OpNotHasPrefix},
		{"hasprefix_cs", OpHasPrefixCS},
		{"hasprefix", OpHasPrefix},
		{"!hassuffix_cs", OpNotHasSuffixCS},
		{"!hassuffix", OpNotHasSuffix},
		{"hassuffix_cs", OpHasSuffixCS},
		{"hassuffix", OpHasSuffix},
		{"!like", OpNotLike},
		{"like", OpLike},
		{"matches regex", OpMatchesRegex},
		// in/!in and between/!between handled in parseComparison directly
	} {
		if p.matchKeyword(kw.word) {
			return kw.op, true
		}
	}

	return 0, false
}

func (p *exprParser) parseAddSub() (Expr, error) {
	left, err := p.parseMulDiv()
	if err != nil {
		return nil, err
	}

	for {
		p.skipWhitespace()
		if p.pos < len(p.input) {
			switch p.input[p.pos] {
			case '+':
				p.pos++
				right, err := p.parseMulDiv()
				if err != nil {
					return nil, err
				}
				left = &BinaryExpr{Left: left, Op: OpAdd, Right: right}
				continue
			case '-':
				p.pos++
				right, err := p.parseMulDiv()
				if err != nil {
					return nil, err
				}
				left = &BinaryExpr{Left: left, Op: OpSub, Right: right}
				continue
			}
		}
		break
	}
	return left, nil
}

func (p *exprParser) parseMulDiv() (Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}

	for {
		p.skipWhitespace()
		if p.pos < len(p.input) {
			switch p.input[p.pos] {
			case '*':
				p.pos++
				right, err := p.parseUnary()
				if err != nil {
					return nil, err
				}
				left = &BinaryExpr{Left: left, Op: OpMul, Right: right}
				continue
			case '/':
				p.pos++
				right, err := p.parseUnary()
				if err != nil {
					return nil, err
				}
				left = &BinaryExpr{Left: left, Op: OpDiv, Right: right}
				continue
			case '%':
				p.pos++
				right, err := p.parseUnary()
				if err != nil {
					return nil, err
				}
				left = &BinaryExpr{Left: left, Op: OpMod, Right: right}
				continue
			}
		}
		break
	}
	return left, nil
}

func (p *exprParser) parseUnary() (Expr, error) {
	p.skipWhitespace()

	// not <expr>
	if p.matchKeyword("not") {
		expr, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &UnaryExpr{Op: "not", Expr: expr}, nil
	}

	return p.parsePrimary()
}

func (p *exprParser) parsePrimary() (Expr, error) {
	p.skipWhitespace()

	if p.pos >= len(p.input) {
		return nil, fmt.Errorf("unexpected end of expression")
	}

	ch := p.input[p.pos]

	// Parenthesised expression
	if ch == '(' {
		p.pos++
		expr, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		p.skipWhitespace()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return nil, fmt.Errorf("expected closing )")
		}
		p.pos++
		return expr, nil
	}

	// String literal
	if ch == '"' || ch == '\'' {
		return p.parseString()
	}

	// Verbatim string literal (@"..." or @'...') — added 2026-08-18.
	// Real KQL: the backslash stands for itself (no escape processing
	// at all) inside a verbatim string; the one exception is the
	// enclosing quote character itself, which is escaped by doubling
	// it (e.g. @"say ""hi""" == say "hi"), the same convention CSV
	// uses. Verified against real KQL's own docs before implementing
	// — this was a real, total gap: @"..." previously wasn't
	// recognized at all ("unexpected character: @"), which blocked
	// every regex-bearing KQL example that (like virtually all of
	// Microsoft's own official docs examples for extract/
	// extract_all/replace_regex/parse kind=regex) uses verbatim
	// strings to avoid double-backslash-escaping a regex pattern.
	if ch == '@' && p.pos+1 < len(p.input) && (p.input[p.pos+1] == '"' || p.input[p.pos+1] == '\'') {
		return p.parseVerbatimString()
	}

	// Number literal (including negative)
	if ch == '-' || (ch >= '0' && ch <= '9') {
		return p.parseNumber()
	}

	// Identifier: column name, function call, keyword literal
	if isIdentStart(ch) {
		return p.parseIdentOrFunc()
	}

	return nil, fmt.Errorf("unexpected character: %c", ch)
}

// parseVerbatimString parses a KQL verbatim string literal (@"..." or
// @'...'): backslash is a literal character throughout, and the only
// escape is doubling the enclosing quote character to represent one
// literal instance of it. See the call site above for the fuller
// rationale and verification note.
func (p *exprParser) parseVerbatimString() (Expr, error) {
	p.pos++ // skip '@'
	quote := p.input[p.pos]
	p.pos++
	var sb strings.Builder

	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if ch == quote {
			// Doubled quote == one literal quote character.
			if p.pos+1 < len(p.input) && p.input[p.pos+1] == quote {
				sb.WriteByte(quote)
				p.pos += 2
				continue
			}
			p.pos++
			return &Literal{Value: sb.String(), Type: types.TypeString}, nil
		}
		sb.WriteByte(ch)
		p.pos++
	}
	return nil, fmt.Errorf("unterminated verbatim string literal")
}

func (p *exprParser) parseString() (Expr, error) {
	quote := p.input[p.pos]
	p.pos++
	var sb strings.Builder

	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if ch == '\\' && p.pos+1 < len(p.input) {
			p.pos++
			switch p.input[p.pos] {
			case '"', '\'', '\\':
				sb.WriteByte(p.input[p.pos])
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			default:
				sb.WriteByte('\\')
				sb.WriteByte(p.input[p.pos])
			}
			p.pos++
			continue
		}
		if ch == quote {
			p.pos++
			return &Literal{Value: sb.String(), Type: types.TypeString}, nil
		}
		sb.WriteByte(ch)
		p.pos++
	}
	return nil, fmt.Errorf("unterminated string literal")
}

func (p *exprParser) parseNumber() (Expr, error) {
	start := p.pos
	if p.input[p.pos] == '-' {
		p.pos++
	}
	isFloat := false
	for p.pos < len(p.input) && (p.input[p.pos] >= '0' && p.input[p.pos] <= '9') {
		p.pos++
	}
	if p.pos < len(p.input) && p.input[p.pos] == '.' {
		isFloat = true
		p.pos++
		for p.pos < len(p.input) && (p.input[p.pos] >= '0' && p.input[p.pos] <= '9') {
			p.pos++
		}
	}
	// Scientific notation
	if p.pos < len(p.input) && (p.input[p.pos] == 'e' || p.input[p.pos] == 'E') {
		isFloat = true
		p.pos++
		if p.pos < len(p.input) && (p.input[p.pos] == '+' || p.input[p.pos] == '-') {
			p.pos++
		}
		for p.pos < len(p.input) && (p.input[p.pos] >= '0' && p.input[p.pos] <= '9') {
			p.pos++
		}
	}

	numStr := p.input[start:p.pos]

	// Check for timespan suffix: d, h, m, s, ms, us, tick
	// KQL timespan literals: 1d, 12h, 30m, 45s, 100ms, 10us, 1tick
	if p.pos < len(p.input) && isTimespanSuffix(p.input[p.pos]) {
		suffStart := p.pos
		for p.pos < len(p.input) && isIdentChar(p.input[p.pos]) {
			p.pos++
		}
		suffix := strings.ToLower(p.input[suffStart:p.pos])
		numVal, _ := strconv.ParseFloat(numStr, 64)
		ticks, err := timespanSuffixToTicks(numVal, suffix)
		if err != nil {
			return nil, err
		}
		return &Literal{Value: ticks, Type: types.TypeTimespan}, nil
	}

	if isFloat {
		v, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid float: %q", numStr)
		}
		return &Literal{Value: v, Type: types.TypeReal}, nil
	}
	v, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid integer: %q", numStr)
	}
	return &Literal{Value: v, Type: types.TypeLong}, nil
}

// isTimespanSuffix checks if a character could start a timespan unit suffix.
func isTimespanSuffix(ch byte) bool {
	return ch == 'd' || ch == 'h' || ch == 'm' || ch == 's' || ch == 'u' || ch == 't'
}

// timespanSuffixToTicks converts a numeric value with a timespan suffix to 100ns ticks.
func timespanSuffixToTicks(val float64, suffix string) (int64, error) {
	var nanosPerUnit float64
	switch suffix {
	case "d":
		nanosPerUnit = 24 * 60 * 60 * 1e9
	case "h":
		nanosPerUnit = 60 * 60 * 1e9
	case "m", "min":
		nanosPerUnit = 60 * 1e9
	case "s", "sec":
		nanosPerUnit = 1e9
	case "ms":
		nanosPerUnit = 1e6
	case "us", "microsecond":
		nanosPerUnit = 1e3
	case "tick":
		return int64(val), nil // Already in ticks
	default:
		return 0, fmt.Errorf("unknown timespan suffix: %q", suffix)
	}
	nanos := val * nanosPerUnit
	return int64(nanos / 100), nil // Convert to 100ns ticks
}

func (p *exprParser) parseIdentOrFunc() (Expr, error) {
	start := p.pos
	for p.pos < len(p.input) && isIdentChar(p.input[p.pos]) {
		p.pos++
	}
	name := p.input[start:p.pos]
	lower := strings.ToLower(name)

	// Boolean literals
	if lower == "true" {
		return &Literal{Value: true, Type: types.TypeBool}, nil
	}
	if lower == "false" {
		return &Literal{Value: false, Type: types.TypeBool}, nil
	}

	// Null literal
	if lower == "null" {
		return &Literal{Value: nil, Type: types.TypeString}, nil
	}

	p.skipWhitespace()

	// Function call: name(...)
	if p.pos < len(p.input) && p.input[p.pos] == '(' {
		openParenIdx := p.pos // captured BEFORE skipWhitespace below can move p.pos past it
		p.pos++
		p.skipWhitespace()

		// toscalar(tabular_expression): the argument is a full tabular
		// pipeline (e.g. toscalar(T | summarize max(X))), never a
		// scalar expression this parser's ordinary argument-list
		// logic below could parse at all — checked before that logic
		// runs, same position in this function as the dynamic(...)
		// special case immediately below, for the same reason.
		// Captured as raw, unparsed text (see ToScalarExpr's own doc
		// comment in ast.go for why parsing is deferred), respecting
		// nested parens via findMatchingParen since the tabular
		// expression can itself contain them (e.g. a nested
		// datatable(...) [...] literal). Uses openParenIdx, captured
		// above BEFORE skipWhitespace ran — p.pos-1 here instead would
		// be wrong whenever whitespace follows the opening paren.
		if lower == "toscalar" {
			closeIdx := findMatchingParen(p.input, openParenIdx)
			if closeIdx < 0 {
				return nil, fmt.Errorf("toscalar(...): unterminated argument")
			}
			queryText := strings.TrimSpace(p.input[p.pos:closeIdx])
			if queryText == "" {
				return nil, fmt.Errorf("toscalar(): requires an argument")
			}
			p.pos = closeIdx + 1
			return p.parsePostfixAccess(&ToScalarExpr{QueryText: queryText})
		}

		// dynamic([1,2,3]) / dynamic({"a":1}): real Kusto grammar — the
		// content is a JSON literal, not a KQL expression list. Checked
		// before general arg parsing since '[' and '{' are not valid
		// expression-starting tokens elsewhere in this grammar; without
		// this, dynamic([1,2,3]) errored "unexpected character: [" with
		// no working literal syntax for constructing a dynamic value at
		// all except an already-dynamic-typed source (bare JSON string
		// literals happen to also work, since dynamic values are
		// JSON-encoded strings under the hood, but that was undocumented
		// and not the syntax a model reaches for from KQL training data).
		if lower == "dynamic" {
			if jsonText, jerr, matched := p.tryConsumeBareJSONLiteral(); matched {
				if jerr != nil {
					return nil, jerr
				}
				lit := &Literal{Value: jsonText, Type: types.TypeDynamic}
				p.skipWhitespace()
				if p.pos < len(p.input) && p.input[p.pos] == ')' {
					p.pos++
				} else {
					return nil, fmt.Errorf("expected ) after dynamic(%s", jsonText)
				}
				return p.parsePostfixAccess(lit)
			}
		}

		// typeof(long) / typeof(datetime) / etc — added 2026-08-18,
		// needed to support extract()'s optional 4th typeLiteral
		// argument (real KQL syntax: extract(regex, group, source,
		// typeof(long))). The argument is a bare KQL type name, not a
		// normal expression — "long" used bare here would otherwise
		// be parsed as either a zero-arg function call attempt or an
		// unresolved column reference by the general arg-parsing loop
		// below, the same reason dynamic(...) and toscalar(...) above
		// need their own special-cased grammar rather than falling
		// through to it. Real KQL's typeof() is its own scalar type
		// with richer behavior (it can appear standalone, is used in
		// column-type contexts, etc); this engine only needs to
		// support it as extract()'s 4th argument, so it's represented
		// simply as a string literal holding the canonical type name
		// — sufficient for every real, verified use of it in this
		// engine, but not a general-purpose typeof() implementation.
		if lower == "typeof" {
			typeStart := p.pos
			for p.pos < len(p.input) && isIdentChar(p.input[p.pos]) {
				p.pos++
			}
			typeName := p.input[typeStart:p.pos]
			if typeName == "" {
				return nil, fmt.Errorf("typeof(): expected a type name")
			}
			if _, terr := types.ParseType(typeName); terr != nil {
				return nil, fmt.Errorf("typeof(%s): %w", typeName, terr)
			}
			p.skipWhitespace()
			if p.pos < len(p.input) && p.input[p.pos] == ')' {
				p.pos++
			} else {
				return nil, fmt.Errorf("expected ) after typeof(%s", typeName)
			}
			lit := &Literal{Value: strings.ToLower(typeName), Type: types.TypeString}
			return p.parsePostfixAccess(lit)
		}

		var args []Expr
		if p.pos < len(p.input) && p.input[p.pos] == ')' {
			p.pos++
		} else if (lower == "datetime" || lower == "todatetime") && p.tryConsumeBareDateTimeLiteral() {
			// datetime(2026-02-28) / datetime(2026-02-28 10:30:00): a bare
			// unquoted date shape, matching real Kusto's grammar, where this
			// is a date literal, NOT arithmetic (2026 minus 2 minus 28).
			// Recognized before general expression parsing so it can never
			// silently evaluate as subtraction — found live: previously
			// datetime(2026-02-28) returned the number 1996 with no error.
			lit := p.consumedBareLiteral
			args = append(args, &Literal{Value: lit, Type: types.TypeString})
			p.skipWhitespace()
			if p.pos < len(p.input) && p.input[p.pos] == ')' {
				p.pos++
			} else {
				return nil, fmt.Errorf("expected ) after date literal in %s(%s...)", name, lit)
			}
		} else {
			for {
				arg, err := p.parseOr()
				if err != nil {
					return nil, fmt.Errorf("function %s arg: %w", name, err)
				}
				args = append(args, arg)
				p.skipWhitespace()
				if p.pos < len(p.input) && p.input[p.pos] == ',' {
					p.pos++
					continue
				}
				if p.pos < len(p.input) && p.input[p.pos] == ')' {
					p.pos++
					break
				}
				return nil, fmt.Errorf("expected , or ) in function call %s", name)
			}
		}
		fc := &FuncCall{Name: lower, Args: args}
		return p.parsePostfixAccess(fc)
	}

	// Column reference — check for postfix property access
	expr := Expr(&ColumnRef{Name: name})
	return p.parsePostfixAccess(expr)
}

// parsePostfixAccess parses chains of .property and ["key"] and [index] access.
func (p *exprParser) parsePostfixAccess(expr Expr) (Expr, error) {
	var path []AccessKey

	for {
		// Dot access: .property
		if p.pos < len(p.input) && p.input[p.pos] == '.' {
			p.pos++ // skip '.'
			if p.pos >= len(p.input) || !isIdentStart(p.input[p.pos]) {
				return nil, fmt.Errorf("expected property name after '.'")
			}
			start := p.pos
			for p.pos < len(p.input) && isIdentChar(p.input[p.pos]) {
				p.pos++
			}
			path = append(path, AccessKey{Name: p.input[start:p.pos], Index: -1})
			continue
		}

		// Bracket access: ["key"] or [index]
		if p.pos < len(p.input) && p.input[p.pos] == '[' {
			p.pos++ // skip '['
			p.skipWhitespace()
			if p.pos >= len(p.input) {
				return nil, fmt.Errorf("unexpected end in bracket access")
			}

			if p.input[p.pos] == '"' || p.input[p.pos] == '\'' {
				// String key: ["property"]
				strExpr, err := p.parseString()
				if err != nil {
					return nil, fmt.Errorf("bracket access: %w", err)
				}
				lit, ok := strExpr.(*Literal)
				if !ok {
					return nil, fmt.Errorf("expected string literal in bracket access")
				}
				path = append(path, AccessKey{Name: fmt.Sprintf("%v", lit.Value), Index: -1})
			} else if p.input[p.pos] >= '0' && p.input[p.pos] <= '9' {
				// Numeric index: [0]
				start := p.pos
				for p.pos < len(p.input) && p.input[p.pos] >= '0' && p.input[p.pos] <= '9' {
					p.pos++
				}
				idx, _ := strconv.Atoi(p.input[start:p.pos])
				path = append(path, AccessKey{Name: "", Index: idx})
			} else {
				return nil, fmt.Errorf("expected string or number in bracket access")
			}

			p.skipWhitespace()
			if p.pos >= len(p.input) || p.input[p.pos] != ']' {
				return nil, fmt.Errorf("expected ']' in bracket access")
			}
			p.pos++ // skip ']'
			continue
		}

		break
	}

	if len(path) == 0 {
		return expr, nil
	}
	return &AccessExpr{Object: expr, Path: path}, nil
}

// --- Helper Functions ---

func (p *exprParser) skipWhitespace() {
	for p.pos < len(p.input) && (p.input[p.pos] == ' ' || p.input[p.pos] == '\t') {
		p.pos++
	}
}

func (p *exprParser) matchKeyword(kw string) bool {
	p.skipWhitespace()
	if p.pos+len(kw) > len(p.input) {
		return false
	}
	if strings.ToLower(p.input[p.pos:p.pos+len(kw)]) != kw {
		return false
	}
	// Keyword must be followed by non-ident char or end of input
	end := p.pos + len(kw)
	if end < len(p.input) && isIdentChar(p.input[end]) {
		return false
	}
	p.pos = end
	return true
}

func isIdentStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}

func isIdentChar(ch byte) bool {
	return isIdentStart(ch) || (ch >= '0' && ch <= '9')
}
