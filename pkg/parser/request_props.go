package parser

import (
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/gql"
)

// splitRequestProperties strips leading "#crp name=value" lines (Kusto
// Explorer's client request property syntax) from a query, returning
// the properties (names lower-cased) and the remaining text.
func splitRequestProperties(input string) (map[string]string, string) {
	props := map[string]string{}
	rest := input
	for {
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		if !strings.HasPrefix(trimmed, "#crp ") && !strings.HasPrefix(trimmed, "#crp\t") {
			break
		}
		line := trimmed
		next := ""
		if nl := strings.IndexByte(trimmed, '\n'); nl >= 0 {
			line, next = trimmed[:nl], trimmed[nl+1:]
		}
		kv := strings.TrimSpace(line[len("#crp"):])
		if eq := strings.IndexAny(kv, "=:"); eq > 0 {
			v := strings.TrimSpace(kv[eq+1:])
			v = strings.Trim(v, `"'`)
			props[strings.ToLower(strings.TrimSpace(kv[:eq]))] = v
		}
		rest = next
	}
	return props, rest
}

// translateQueryLanguage applies the query_language request property:
// a GQL or openCypher query is translated into KQL over the graph named
// by query_graph_reference.
func translateQueryLanguage(input string) (string, error) {
	if !strings.Contains(input, "#crp") {
		return input, nil
	}
	props, rest := splitRequestProperties(input)
	if len(props) == 0 {
		return input, nil
	}
	return TranslateQueryLanguage(rest, props["query_language"], props["query_graph_reference"])
}

// TranslateQueryLanguage translates query from the named language
// ("gql", "opencypher"; "" or "kql" leave it unchanged) into KQL over
// graphRef.
func TranslateQueryLanguage(query, lang, graphRef string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "", "kql", "csl":
		return query, nil
	case "gql":
		return gql.Translate(query, graphRef, false)
	case "opencypher", "cypher":
		return gql.Translate(query, graphRef, true)
	}
	return "", errUnknownLanguage(lang)
}

type errUnknownLanguage string

func (e errUnknownLanguage) Error() string {
	return "unsupported query_language " + string(e) + " (expected kql, gql or opencypher)"
}
