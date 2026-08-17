package engine

import (
	"fmt"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// applyParseKV implements | parse-kv Expression as (KeysList) with (...)
// — see ParseKVOp's own doc comment (pkg/parser/ast.go) for exactly
// which of real ADX's three documented modes this covers (specified
// delimiter only). Verified against real ADX's own worked examples
// (parse-kv-operator.md) before writing this, including the escaped-
// quote and greedy-mode examples.
func (e *Engine) applyParseKV(input *types.Table, op *parser.ParseKVOp) (*types.Table, error) {
	// Resolve declared key output types up front.
	colTypes := make([]types.KQLType, len(op.Keys))
	for i, k := range op.Keys {
		if k.Type == "" {
			colTypes[i] = types.TypeString
			continue
		}
		t, err := types.ParseType(k.Type)
		if err != nil {
			return nil, err
		}
		colTypes[i] = t
	}

	// Build output schema: input columns + one column per declared key.
	outCols := make([]types.Column, len(input.Schema.Columns))
	copy(outCols, input.Schema.Columns)
	keyColIdx := make(map[string]int, len(op.Keys)) // key name -> index into outCols
	for i, k := range op.Keys {
		keyColIdx[k.Name] = len(outCols)
		outCols = append(outCols, types.Column{Name: k.Name, Type: colTypes[i]})
	}
	outSchema := types.Schema{Columns: outCols}
	output := types.NewTable(input.Name, outSchema)

	srcIdx := input.Schema.ColumnIndex(op.Column)
	if srcIdx < 0 {
		return nil, fmt.Errorf("parse-kv: column %q not found", op.Column)
	}

	for _, row := range input.Rows {
		srcVal := ""
		if row[srcIdx] != nil {
			srcVal = fmt.Sprintf("%v", row[srcIdx])
		}

		var found map[string]string
		if op.Greedy {
			found = parseKVGreedy(srcVal, op.Keys, op.PairDelimiter, op.KVDelimiter, op.Quotes)
		} else {
			found = parseKVSpecified(srcVal, op.PairDelimiter, op.KVDelimiter, op.Quotes, op.Escape)
		}

		outRow := make(types.Row, len(outCols))
		copy(outRow, row)
		for i, k := range op.Keys {
			raw, ok := found[k.Name]
			if !ok {
				continue // leaves nil — "either null or an empty string
				// depending on the column type" per real docs; this
				// engine always leaves nil, consistent with NULL
				// handling used throughout the rest of the engine.
			}
			val, err := types.ParseValue(raw, colTypes[i])
			if err != nil {
				continue // conversion failure -> leave nil rather than
				// fail the whole query, consistent with this engine's
				// general lenient-null approach elsewhere (e.g. parse's
				// own unmatched-field handling).
			}
			outRow[keyColIdx[k.Name]] = val
		}
		output.AddRow(outRow)
	}

	return output, nil
}

// parseKVSpecified implements the non-greedy "specified delimiter" mode:
// split on pairDelimiter into pairs, each pair split on the FIRST
// occurrence of kvDelimiter, both splits quote-aware (a quoted key or
// value may itself contain delimiter/quote characters) and escape-aware
// (escape+char inside a quoted region is unescaped to the literal char,
// matching the real docs' \" example). Only the first occurrence of a
// given key is kept — "The first appearance of a key is extracted, and
// subsequent values are ignored," per real ADX docs.
func parseKVSpecified(s, pairDelim, kvDelim string, quotes []string, escape string) map[string]string {
	result := make(map[string]string)
	if pairDelim == "" {
		return result
	}
	for _, pair := range splitKVRespectingQuotes(s, pairDelim, quotes, escape) {
		key, val, ok := splitFirstRespectingQuotes(pair, kvDelim, quotes, escape)
		if !ok {
			continue
		}
		key = stripKVDataQuotes(strings.TrimSpace(key), quotes)
		val = stripKVDataQuotes(strings.TrimSpace(val), quotes)
		if _, exists := result[key]; !exists {
			result[key] = val
		}
	}
	return result
}

// parseKVGreedy implements greedy mode: a value runs until the next
// occurrence of pairDelimiter that is immediately followed by one of the
// DECLARED keys plus kvDelimiter, rather than stopping at the first
// pairDelimiter — verified against real ADX's own greedy-mode worked
// example ('name=John Doe phone=555 5555 city=New York' correctly
// yielding name="John Doe", not name="John"). Deliberately simpler than
// parseKVSpecified on quoting: only outer-quote stripping is applied to
// the extracted value, not full escape-aware inner scanning — greedy
// mode's real value is unquoted multi-word values (its own worked
// example has none), and this keeps the two modes' code independently
// readable rather than forcing one shared, harder-to-follow scanner.
func parseKVGreedy(s string, keys []parser.ParseKVKey, pairDelim, kvDelim string, quotes []string) map[string]string {
	result := make(map[string]string)
	if pairDelim == "" || kvDelim == "" {
		return result
	}
	pos := 0
	for pos < len(s) {
		for strings.HasPrefix(s[pos:], pairDelim) {
			pos += len(pairDelim)
		}
		if pos >= len(s) {
			break
		}
		matchedKey := ""
		for _, k := range keys {
			if strings.HasPrefix(s[pos:], k.Name+kvDelim) {
				matchedKey = k.Name
				break
			}
		}
		if matchedKey == "" {
			// Unrecognized token at this position — skip to the next
			// pairDelimiter and keep scanning rather than aborting.
			idx := strings.Index(s[pos:], pairDelim)
			if idx < 0 {
				break
			}
			pos += idx + len(pairDelim)
			continue
		}
		valStart := pos + len(matchedKey) + len(kvDelim)
		valEnd := len(s)
		searchPos := valStart
		for searchPos <= len(s) {
			idx := strings.Index(s[searchPos:], pairDelim)
			if idx < 0 {
				break
			}
			candidate := searchPos + idx
			after := candidate + len(pairDelim)
			nextIsKey := false
			for _, k2 := range keys {
				if strings.HasPrefix(s[after:], k2.Name+kvDelim) {
					nextIsKey = true
					break
				}
			}
			if nextIsKey {
				valEnd = candidate
				break
			}
			searchPos = candidate + len(pairDelim)
		}
		val := stripKVDataQuotes(strings.TrimSpace(s[valStart:valEnd]), quotes)
		if _, exists := result[matchedKey]; !exists {
			result[matchedKey] = val
		}
		pos = valEnd
	}
	return result
}

// splitKVRespectingQuotes splits s on pairDelim, treating any region
// opened by a configured quote char as opaque to pairDelim (a quoted
// value may contain the pair delimiter literally, e.g. a space-quoted
// "connection aborted" in real ADX's own worked example). escape+char
// inside a quoted region is collapsed to the literal char.
func splitKVRespectingQuotes(s, pairDelim string, quotes []string, escape string) []string {
	var parts []string
	var cur strings.Builder
	i := 0
	for i < len(s) {
		if openC, closeC, ok := matchQuoteStart(s[i:], quotes); ok {
			cur.WriteByte(openC)
			i++
			for i < len(s) {
				if escape != "" && strings.HasPrefix(s[i:], escape) && i+len(escape) < len(s) {
					cur.WriteByte(s[i+len(escape)])
					i += len(escape) + 1
					continue
				}
				if s[i] == closeC {
					cur.WriteByte(s[i])
					i++
					break
				}
				cur.WriteByte(s[i])
				i++
			}
			continue
		}
		if pairDelim != "" && strings.HasPrefix(s[i:], pairDelim) {
			parts = append(parts, cur.String())
			cur.Reset()
			i += len(pairDelim)
			continue
		}
		cur.WriteByte(s[i])
		i++
	}
	parts = append(parts, cur.String())
	return parts
}

// splitFirstRespectingQuotes splits pair on the first quote-respecting
// occurrence of kvDelim, returning ok=false if kvDelim never appears
// outside a quoted region.
func splitFirstRespectingQuotes(pair, kvDelim string, quotes []string, escape string) (key, val string, ok bool) {
	i := 0
	for i < len(pair) {
		if openC, closeC, matched := matchQuoteStart(pair[i:], quotes); matched {
			_ = openC
			i++
			for i < len(pair) {
				if escape != "" && strings.HasPrefix(pair[i:], escape) && i+len(escape) < len(pair) {
					i += len(escape) + 1
					continue
				}
				if pair[i] == closeC {
					i++
					break
				}
				i++
			}
			continue
		}
		if kvDelim != "" && strings.HasPrefix(pair[i:], kvDelim) {
			return pair[:i], pair[i+len(kvDelim):], true
		}
		i++
	}
	return "", "", false
}

// matchQuoteStart reports whether s begins with a configured quote's
// opening character, returning that opening char and its matching
// closing char (same char for a 1-char quote entry, the second char for
// a 2-char entry like "()").
func matchQuoteStart(s string, quotes []string) (openC, closeC byte, ok bool) {
	if s == "" {
		return 0, 0, false
	}
	for _, q := range quotes {
		if len(q) == 0 {
			continue
		}
		if s[0] == q[0] {
			if len(q) >= 2 {
				return q[0], q[1], true
			}
			return q[0], q[0], true
		}
	}
	return 0, 0, false
}

// stripKVDataQuotes strips one matching layer of configured quote chars
// from an already-extracted key/value substring (the substring still
// carries its original surrounding quote bytes since the scanners above
// copy them through unchanged).
func stripKVDataQuotes(s string, quotes []string) string {
	if len(s) < 2 {
		return s
	}
	for _, q := range quotes {
		if len(q) == 0 {
			continue
		}
		openC := q[0]
		closeC := q[0]
		if len(q) >= 2 {
			closeC = q[1]
		}
		if s[0] == openC && s[len(s)-1] == closeC {
			return s[1 : len(s)-1]
		}
	}
	return s
}

