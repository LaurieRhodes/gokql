package parser

import (
	"encoding/json"
	"fmt"
	"math"
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
var bareDateTimeRe = regexp.MustCompile(`^\d{4} *- *\d{1,2} *- *\d{1,2}([ T]\d{1,2}:\d{2}(:\d{2}(\.\d+)?)?Z?)?`)

// dateDashSpaceRe matches the optional spaces around a date literal's
// dashes: datetime(2017 - 08 - 01).
var dateDashSpaceRe = regexp.MustCompile(` *- *`)

// bareDateRe is the date part of a bare datetime literal.
var bareDateRe = regexp.MustCompile(`^\d{4} *- *\d{1,2} *- *\d{1,2}`)

// tryConsumeBareJSONLiteral checks whether the input at the current
// position is a dynamic literal ([...] or {...}) immediately followed
// (modulo whitespace) by ')': dynamic([1,2,3]), dynamic({"a":1}). The
// content is KQL's dynamic literal grammar (dynamic_literal.go), not a
// KQL expression list. On success it consumes the literal (not the
// closing paren) and returns its JSON; a malformed literal is a parse
// error rather than a runtime failure.
func (p *exprParser) tryConsumeBareJSONLiteral() (string, error, bool) {
	rest := p.input[p.pos:]
	if len(rest) == 0 || (rest[0] != '[' && rest[0] != '{') {
		return "", nil, false
	}
	normalized, end, err := dynamicLiteralToJSON(rest)
	if err != nil {
		return "", fmt.Errorf("dynamic(%s): %v", strings.TrimSpace(rest[:min(len(rest), 200)]), err), true
	}
	after := end
	for after < len(rest) && (rest[after] == ' ' || rest[after] == '\t' || rest[after] == '\n' || rest[after] == '\r') {
		after++
	}
	if after >= len(rest) || rest[after] != ')' {
		// Doesn't immediately close — not the literal form; fall
		// through to normal expression parsing.
		return "", nil, false
	}
	if !json.Valid([]byte(normalized)) {
		return "", fmt.Errorf("dynamic(%s): not valid JSON", rest[:end]), true
	}
	p.pos += end
	return normalized, nil, true
}

// bareTimespanRe is a bare timespan literal inside time(...).
var bareTimespanRe = regexp.MustCompile(`^-?(\d+\.)?\d{1,2}:\d{2}(:\d{2}(\.\d+)?)?`)

func (p *exprParser) tryConsumeBareTimespanLiteral() bool {
	rest := p.input[p.pos:]
	loc := bareTimespanRe.FindStringIndex(rest)
	if loc == nil {
		return false
	}
	after := loc[1]
	for after < len(rest) && (rest[after] == ' ' || rest[after] == '\t') {
		after++
	}
	if after >= len(rest) || rest[after] != ')' {
		return false
	}
	p.consumedBareLiteral = rest[:loc[1]]
	p.pos += loc[1]
	return true
}

// tryConsumeBareDateTimeLiteral checks whether the input at the current
// position is a bare date/datetime literal immediately followed
// (modulo whitespace) by ')'. If so, consumes it (not the paren) and
// records it in consumedBareLiteral. Requiring the immediate ')' means
// this only ever fires on the exact shape real Kusto treats as a date
// literal in this position — datetime(someColumn) and datetime(a - b)
// are unaffected, since neither starts with four digits, and any
// trailing content before ')' (other than optional time-of-day) makes
// the match fail closed, falling through to normal expression parsing.
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
	lit := rest[:loc[1]]
	if date := bareDateRe.FindString(lit); date != "" {
		// Spaces around the date's dashes are allowed:
		// datetime(2017 - 08 - 01).
		lit = dateDashSpaceRe.ReplaceAllString(date, "-") + lit[len(date):]
	}
	p.consumedBareLiteral = lit
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
		// A tabular source of terms: has_any ((T | project x)) or a
		// let-bound table, has_any (T).
		if closeIdx := findMatchingParenAnyQuote(p.input, p.pos); closeIdx > p.pos {
			inner := strings.TrimSpace(p.input[p.pos+1 : closeIdx])
			tabular := false
			if strings.HasPrefix(inner, "(") && findMatchingParenAnyQuote(inner, 0) == len(inner)-1 {
				if _, err := ParseExpr(inner); err != nil {
					tabular = true
					inner = strings.TrimSpace(inner[1 : len(inner)-1])
				}
			} else if _, err := ParseExpr(inner); err != nil && !strings.Contains(inner, ",") {
				if _, qerr := parseQuery(inner); qerr == nil {
					tabular = true
				}
			}
			if tabular {
				p.pos = closeIdx + 1
				return &HasAnyAllExpr{Column: left, All: hasAll, SubqueryText: inner}, nil
			}
		}
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

	// * as an operand: every column (where * has "x").
	if ch == '*' {
		p.pos++
		return &StarExpr{}, nil
	}

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

	// String literals: '...', "...", verbatim @'...' (the backslash
	// stands for itself; a doubled quote escapes the quote), obfuscated
	// h'...', and whitespace-separated sequences of these, which KQL
	// concatenates.
	if p.atStringLiteral() {
		return p.parseStringSequence()
	}

	// Unary minus on a non-literal operand: -(x), -abs(x), -decimal(1.5).
	if ch == '-' {
		j := p.pos + 1
		for j < len(p.input) && (p.input[j] == ' ' || p.input[j] == '\t') {
			j++
		}
		if j < len(p.input) && !(p.input[j] >= '0' && p.input[j] <= '9') && p.input[j] != '.' {
			p.pos = j
			operand, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			return &UnaryExpr{Op: "-", Expr: operand}, nil
		}
	}

	// Number literal (including negative)
	if ch == '-' || (ch >= '0' && ch <= '9') {
		return p.parseNumber()
	}

	// Identifier: column name, function call, keyword literal
	if isIdentStart(ch) {
		return p.parseIdentOrFunc()
	}

	// Quoted identifier: ['name'] or ["name"], for names that are not
	// valid bare identifiers (spaces, keywords).
	if ch == '[' {
		save := p.pos
		p.pos++
		p.skipWhitespace()
		if p.pos < len(p.input) && (p.input[p.pos] == '\'' || p.input[p.pos] == '"') {
			lit, err := p.parseString()
			if err == nil {
				p.skipWhitespace()
				if l, ok := lit.(*Literal); ok && p.pos < len(p.input) && p.input[p.pos] == ']' {
					p.pos++
					return p.parsePostfixAccess(&ColumnRef{Name: fmt.Sprintf("%v", l.Value)})
				}
			}
		}
		p.pos = save
	}

	return nil, fmt.Errorf("unexpected character: %c", ch)
}

// parseVerbatimString parses a KQL verbatim string literal (@"..." or
// @'...'): backslash is a literal character throughout, and the only
// escape is doubling the enclosing quote character to represent one
// literal instance of it. See the call site above for the fuller
// rationale and verification note.
// atStringLiteral reports whether a string literal starts here: '...',
// "...", @'...', or an obfuscated h'...' / H"..." / h@'...' literal
// (obfuscation affects logging only; the value is the plain string).
func (p *exprParser) atStringLiteral() bool {
	i := p.pos
	if i < len(p.input) && (p.input[i] == 'h' || p.input[i] == 'H') {
		i++
	}
	if i < len(p.input) && p.input[i] == '@' {
		i++
	}
	return i < len(p.input) && (p.input[i] == '"' || p.input[i] == '\'')
}

// parseStringLiteral parses one string literal in any of its forms.
func (p *exprParser) parseStringLiteral() (Expr, error) {
	if c := p.input[p.pos]; c == 'h' || c == 'H' {
		p.pos++
	}
	if p.input[p.pos] == '@' {
		return p.parseVerbatimString()
	}
	return p.parseString()
}

// parseStringSequence parses a string literal and any literals that
// follow it separated only by whitespace: KQL concatenates adjacent
// string literals ('a' 'b' is "ab").
func (p *exprParser) parseStringSequence() (Expr, error) {
	first, err := p.parseStringLiteral()
	if err != nil {
		return nil, err
	}
	val := first.(*Literal).Value.(string)
	for {
		save := p.pos
		p.skipWhitespace()
		if p.pos == save || p.pos >= len(p.input) || !p.atStringLiteral() {
			p.pos = save
			break
		}
		next, err := p.parseStringLiteral()
		if err != nil {
			return nil, err
		}
		val += next.(*Literal).Value.(string)
	}
	return &Literal{Value: val, Type: types.TypeString}, nil
}

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
			// Kusto.Language's escapes (KustoFacts.GetStringLiteralValue).
			c := p.input[p.pos]
			switch c {
			case '"', '\'', '\\':
				sb.WriteByte(c)
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			case 'a':
				sb.WriteByte('\a')
			case 'b':
				sb.WriteByte('\b')
			case 'f':
				sb.WriteByte('\f')
			case 'v':
				sb.WriteByte('\v')
			case 'u', 'U', 'x':
				n := map[byte]int{'u': 4, 'U': 8, 'x': 2}[c]
				hexStr := ""
				if p.pos+1+n <= len(p.input) {
					hexStr = p.input[p.pos+1 : p.pos+1+n]
				}
				if v, err := strconv.ParseUint(hexStr, 16, 32); err == nil && len(hexStr) == n {
					sb.WriteRune(rune(v))
					p.pos += n
				} else {
					sb.WriteByte('\\')
					sb.WriteByte(c)
				}
			default:
				if c >= '0' && c <= '7' {
					// Octal: up to three digits.
					v, k := 0, 0
					for k < 3 && p.pos+k < len(p.input) && p.input[p.pos+k] >= '0' && p.input[p.pos+k] <= '7' {
						v = v<<3 + int(p.input[p.pos+k]-'0')
						k++
					}
					sb.WriteRune(rune(v))
					p.pos += k - 1
				} else {
					sb.WriteByte('\\')
					sb.WriteByte(c)
				}
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
	// Hexadecimal long literal: 0x1F.
	if p.pos+2 < len(p.input) && p.input[p.pos] == '0' && (p.input[p.pos+1] == 'x' || p.input[p.pos+1] == 'X') {
		j := p.pos + 2
		for j < len(p.input) && isHexDigit(p.input[j]) {
			j++
		}
		if j > p.pos+2 {
			v, err := strconv.ParseUint(p.input[p.pos+2:j], 16, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid hex literal %q", p.input[p.pos:j])
			}
			p.pos = j
			return &Literal{Value: int64(v), Type: types.TypeLong}, nil
		}
	}
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

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
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
	lower := keywordFold(name)

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
		// Typed literal forms that are not expressions: real(+inf),
		// real(-inf), real(nan) (also double), and an unquoted
		// guid(74be27de-1e4e-49d9-b579-fe0b331d3642) (also uuid,
		// uniqueid).
		if lit, ok := p.trySpecialTypedLiteral(lower, openParenIdx); ok {
			return p.parsePostfixAccess(lit)
		}

		if lower == "toscalar" || lower == "column_names_of" {
			closeIdx := findMatchingParen(p.input, openParenIdx)
			if closeIdx < 0 {
				return nil, fmt.Errorf("%s(...): unterminated argument", lower)
			}
			queryText := strings.TrimSpace(p.input[p.pos:closeIdx])
			if queryText == "" {
				return nil, fmt.Errorf("%s(): requires an argument", lower)
			}
			p.pos = closeIdx + 1
			return p.parsePostfixAccess(&ToScalarExpr{QueryText: queryText, ColumnNames: lower == "column_names_of"})
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
		} else if (lower == "time" || lower == "timespan" || lower == "totimespan") && p.tryConsumeBareTimespanLiteral() {
			// time(00:01:00), time(29.09:00:05.12345): a bare
			// [d.]hh:mm[:ss[.f]] literal, not arithmetic.
			args = append(args, &Literal{Value: p.consumedBareLiteral, Type: types.TypeString})
			p.skipWhitespace()
			if p.pos < len(p.input) && p.input[p.pos] == ')' {
				p.pos++
			} else {
				return nil, fmt.Errorf("expected ) after timespan literal in %s(%s...)", name, p.consumedBareLiteral)
			}
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
			var argNames []string
			for {
				argName := p.tryArgName()
				if argName != "" && argNames == nil {
					argNames = make([]string, len(args), len(args)+1)
				}
				if argNames != nil {
					argNames = append(argNames, argName)
				}
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
			if argNames != nil {
				fc := &FuncCall{Name: lower, RawName: name, Args: args, ArgNames: argNames}
				return p.parsePostfixAccess(fc)
			}
		}
		fc := &FuncCall{Name: lower, RawName: name, Args: args}
		return p.parsePostfixAccess(fc)
	}

	// Column reference — check for postfix property access
	expr := Expr(&ColumnRef{Name: name})
	return p.parsePostfixAccess(expr)
}

// parsePostfixAccess parses chains of .property and ["key"] and [index] access.
var guidLiteralPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (p *exprParser) trySpecialTypedLiteral(lower string, openParenIdx int) (Expr, bool) {
	isReal := lower == "real" || lower == "double"
	isGUID := lower == "guid" || lower == "uuid" || lower == "uniqueid"
	isDecimal := lower == "decimal"
	if !isReal && !isGUID && !isDecimal {
		return nil, false
	}
	closeIdx := findMatchingParen(p.input, openParenIdx)
	if closeIdx < 0 {
		return nil, false
	}
	raw := strings.TrimSpace(p.input[openParenIdx+1 : closeIdx])
	var lit *Literal
	switch {
	case isReal:
		switch strings.ToLower(raw) {
		case "+inf", "inf":
			lit = &Literal{Value: math.Inf(1), Type: types.TypeReal}
		case "-inf":
			lit = &Literal{Value: math.Inf(-1), Type: types.TypeReal}
		case "nan":
			lit = &Literal{Value: math.NaN(), Type: types.TypeReal}
		}
	case isGUID:
		if guidLiteralPattern.MatchString(raw) {
			lit = &Literal{Value: strings.ToLower(raw), Type: types.TypeGUID}
		}
	case isDecimal:
		// decimal(123.45678), decimal(1e5), decimal(null): parsed from
		// the source text, so the value never passes through float64.
		if strings.EqualFold(raw, "null") {
			lit = &Literal{Value: nil, Type: types.TypeDecimal}
		} else if d, ok := types.ParseDecimal(raw); ok {
			lit = &Literal{Value: d, Type: types.TypeDecimal}
		}
	}
	if lit == nil {
		return nil, false
	}
	p.pos = closeIdx + 1
	return lit, true
}

// tryArgName consumes "name =" at the start of a function argument
// (a named argument, f(len=3)) and returns the name, or returns "" and
// consumes nothing. "==", "=~" and "!=" are comparisons, not names.
func (p *exprParser) tryArgName() string {
	save := p.pos
	p.skipWhitespace()
	start := p.pos
	if p.pos >= len(p.input) || !isIdentStart(p.input[p.pos]) {
		p.pos = save
		return ""
	}
	for p.pos < len(p.input) && isIdentChar(p.input[p.pos]) {
		p.pos++
	}
	name := p.input[start:p.pos]
	p.skipWhitespace()
	if p.pos < len(p.input) && p.input[p.pos] == '=' &&
		!(p.pos+1 < len(p.input) && (p.input[p.pos+1] == '=' || p.input[p.pos+1] == '~')) {
		p.pos++
		return name
	}
	p.pos = save
	return ""
}

func (p *exprParser) parsePostfixAccess(expr Expr) (Expr, error) {
	var path []AccessKey

	for {
		// Dot access: .property (".." is between's range, not access)
		if p.pos < len(p.input) && p.input[p.pos] == '.' && !strings.HasPrefix(p.input[p.pos:], "..") {
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
			} else {
				// Any other index: a non-negative integer literal is a
				// plain index; anything else (arr[-1], arr[n - 1],
				// bag[keyColumn]) is evaluated per row.
				ix, err := p.parseOr()
				if err != nil {
					return nil, fmt.Errorf("bracket access: %w", err)
				}
				key := AccessKey{Index: -1, Expr: ix}
				if lit, ok := ix.(*Literal); ok {
					if n, ok := lit.Value.(int64); ok && n >= 0 {
						key = AccessKey{Index: int(n)}
					}
				}
				path = append(path, key)
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
	for p.pos < len(p.input) && (p.input[p.pos] == ' ' || p.input[p.pos] == '\t' || p.input[p.pos] == '\n' || p.input[p.pos] == '\r') {
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
