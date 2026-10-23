package parser

import (
	"fmt"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// isExternalDataSource reports whether lower (keyword-folded) starts an
// externaldata / external_data source.
func isExternalDataSource(lower string) bool {
	for _, kw := range []string{"externaldata", "external_data"} {
		if strings.HasPrefix(lower, kw) {
			rest := strings.TrimLeft(lower[len(kw):], " \t\r\n")
			return strings.HasPrefix(rest, "(")
		}
	}
	return false
}

// parseExternalData parses externaldata (Col: type, ...) [uri, ...]
// [with (name=value, ...)].
func parseExternalData(source string) (*TableFunc, error) {
	s := strings.TrimSpace(source)
	kw := "externaldata"
	if strings.HasPrefix(keywordFold(s), "external_data") {
		kw = "external_data"
	}
	s = strings.TrimSpace(s[len(kw):])
	end := findMatchingParen(s, 0)
	if end < 0 {
		return nil, fmt.Errorf("externaldata: unmatched '('")
	}
	tf := &TableFunc{Name: "externaldata", Schema: &types.Schema{}, Props: map[string]string{}}
	for _, def := range splitRespectingParens(s[1:end], ',') {
		def = strings.TrimSpace(def)
		if def == "" {
			continue
		}
		ci := strings.LastIndex(def, ":")
		if ci < 0 {
			return nil, fmt.Errorf("externaldata: expected 'Name: type', got %q", def)
		}
		t, err := types.ParseType(strings.TrimSpace(def[ci+1:]))
		if err != nil {
			return nil, fmt.Errorf("externaldata: column %q: %w", def[:ci], err)
		}
		tf.Schema.Columns = append(tf.Schema.Columns, types.Column{Name: unquoteIdent(def[:ci]), Type: t})
	}
	if len(tf.Schema.Columns) == 0 {
		return nil, fmt.Errorf("externaldata: no columns")
	}
	rest := strings.TrimSpace(s[end+1:])
	if !strings.HasPrefix(rest, "[") {
		return nil, fmt.Errorf("externaldata: expected [ storage URIs ]")
	}
	close := findMatchingBracket(rest, 0)
	if close < 0 {
		return nil, fmt.Errorf("externaldata: unmatched '['")
	}
	for _, u := range splitRespectingParens(rest[1:close], ',') {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if u[0] == 'h' || u[0] == 'H' { // h'...' obfuscated literal
			u = u[1:]
		}
		x, err := ParseExpr(u)
		lit, ok := x.(*Literal)
		if err != nil || !ok {
			return nil, fmt.Errorf("externaldata: expected a string URI, got %s", u)
		}
		str, ok := lit.Value.(string)
		if !ok {
			return nil, fmt.Errorf("externaldata: expected a string URI, got %s", u)
		}
		tf.URIs = append(tf.URIs, str)
	}
	if len(tf.URIs) == 0 {
		return nil, fmt.Errorf("externaldata: no storage URI")
	}
	rest = strings.TrimSpace(rest[close+1:])
	if rest != "" {
		if !strings.HasPrefix(keywordFold(rest), "with") {
			return nil, fmt.Errorf("externaldata: unexpected %q", rest)
		}
		w := strings.TrimSpace(rest[len("with"):])
		if !strings.HasPrefix(w, "(") || !strings.HasSuffix(w, ")") {
			return nil, fmt.Errorf("externaldata: expected with (name=value, ...)")
		}
		props, err := parsePropertyList(w[1 : len(w)-1])
		if err != nil {
			return nil, fmt.Errorf("externaldata: %w", err)
		}
		tf.Props = props
	}
	return tf, nil
}
