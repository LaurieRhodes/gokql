// Package gql translates GQL (ISO/IEC 39075, as Azure Data Explorer's
// graph query language) and openCypher queries into KQL graph-match
// pipelines. The translation is text to text: Translate returns a KQL
// query over the graph reference that the ordinary parser and engine
// then run, so every graph feature is implemented once, in graph-match.
//
// Exact parity with ADX is not a goal; the aim is a usable subset:
// MATCH / OPTIONAL MATCH with label expressions, inline property maps,
// element WHERE, variable-length edges and path variables; WHERE,
// FILTER, LET, FOR / UNWIND, RETURN [DISTINCT] with aggregates, WITH,
// ORDER BY, OFFSET / SKIP, LIMIT, NEXT and UNION [ALL].
package gql

import (
	"fmt"
	"strings"
	"unicode"
)

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tQuotedIdent // `backtick` identifier: never a keyword
	tString
	tInt
	tFloat
	tParam
	tPunct
)

type token struct {
	kind tokKind
	text string // identifier name, decoded string, number text or punctuation
	pos  int
	end  int
}

func (t token) is(p string) bool { return t.kind == tPunct && t.text == p }

// kw reports whether t is the (case-insensitive) keyword k.
func (t token) kw(k string) bool { return t.kind == tIdent && strings.EqualFold(t.text, k) }

var multiPunct = []string{"<>", "<=", ">=", "!=", "||", "..", "=~"}

func lex(src string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
			continue
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			e := strings.Index(src[i+2:], "*/")
			if e < 0 {
				return nil, fmt.Errorf("gql: unterminated comment")
			}
			i += e + 4
			continue
		}
		start := i
		switch {
		case c == '\'' || c == '"':
			var sb strings.Builder
			i++
			for {
				if i >= len(src) {
					return nil, fmt.Errorf("gql: unterminated string at %d", start)
				}
				ch := src[i]
				if ch == '\\' && i+1 < len(src) {
					i++
					switch src[i] {
					case 'n':
						sb.WriteByte('\n')
					case 't':
						sb.WriteByte('\t')
					case 'r':
						sb.WriteByte('\r')
					default:
						sb.WriteByte(src[i])
					}
					i++
					continue
				}
				if ch == c {
					if i+1 < len(src) && src[i+1] == c { // doubled quote
						sb.WriteByte(c)
						i += 2
						continue
					}
					i++
					break
				}
				sb.WriteByte(ch)
				i++
			}
			toks = append(toks, token{kind: tString, text: sb.String(), pos: start, end: i})
		case c == '`':
			e := strings.IndexByte(src[i+1:], '`')
			if e < 0 {
				return nil, fmt.Errorf("gql: unterminated `identifier`")
			}
			toks = append(toks, token{kind: tQuotedIdent, text: src[i+1 : i+1+e], pos: start, end: i + e + 2})
			i += e + 2
		case c == '$':
			i++
			for i < len(src) && isIdentChar(rune(src[i])) {
				i++
			}
			toks = append(toks, token{kind: tParam, text: src[start+1 : i], pos: start, end: i})
		case c >= '0' && c <= '9':
			for i < len(src) && src[i] >= '0' && src[i] <= '9' {
				i++
			}
			kind := tInt
			if i+1 < len(src) && src[i] == '.' && src[i+1] >= '0' && src[i+1] <= '9' {
				kind = tFloat
				i++
				for i < len(src) && src[i] >= '0' && src[i] <= '9' {
					i++
				}
			}
			if i < len(src) && (src[i] == 'e' || src[i] == 'E') {
				j := i + 1
				if j < len(src) && (src[j] == '+' || src[j] == '-') {
					j++
				}
				if j < len(src) && src[j] >= '0' && src[j] <= '9' {
					kind = tFloat
					i = j
					for i < len(src) && src[i] >= '0' && src[i] <= '9' {
						i++
					}
				}
			}
			toks = append(toks, token{kind: kind, text: src[start:i], pos: start, end: i})
		case isIdentStart(rune(c)):
			for i < len(src) && isIdentChar(rune(src[i])) {
				i++
			}
			toks = append(toks, token{kind: tIdent, text: src[start:i], pos: start, end: i})
		default:
			p := string(c)
			for _, m := range multiPunct {
				if strings.HasPrefix(src[i:], m) {
					p = m
					break
				}
			}
			if !strings.ContainsAny(p[:1], "()[]{},.:;|&!?*+-/%=<>^~") {
				return nil, fmt.Errorf("gql: unexpected character %q at %d", c, i)
			}
			i += len(p)
			toks = append(toks, token{kind: tPunct, text: p, pos: start, end: i})
		}
	}
	toks = append(toks, token{kind: tEOF, pos: len(src), end: len(src)})
	return toks, nil
}

func isIdentStart(r rune) bool { return r == '_' || unicode.IsLetter(r) || r >= 0x80 }
func isIdentChar(r rune) bool  { return isIdentStart(r) || (r >= '0' && r <= '9') }
