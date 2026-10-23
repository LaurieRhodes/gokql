package engine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// evalStringFunc handles string manipulation functions.
func evalStringFunc(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "strlen":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("strlen requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return int64(0), true, nil
		}
		// Characters (code points), not bytes.
		return int64(utf8.RuneCountInString(fmt.Sprintf("%v", val))), true, nil

	case "tolower":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("tolower requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return strings.ToLower(fmt.Sprintf("%v", val)), true, nil

	case "toupper":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("toupper requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return strings.ToUpper(fmt.Sprintf("%v", val)), true, nil

	case "strcat":
		var sb strings.Builder
		for _, arg := range fc.Args {
			str, _, err := exprToString(eng, arg, schema, row)
			if err != nil {
				return nil, true, err
			}
			sb.WriteString(str)
		}
		return sb.String(), true, nil

	case "substring":
		if len(fc.Args) < 2 || len(fc.Args) > 3 {
			return nil, true, fmt.Errorf("substring requires 2 or 3 arguments")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		s := fmt.Sprintf("%v", val)
		startVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		start := int(types.ToInt64(startVal))
		if start < 0 {
			start = 0
		}
		if start >= len(s) {
			return "", true, nil
		}
		if len(fc.Args) == 3 {
			lenVal, err := evalExpr(eng, fc.Args[2], schema, row)
			if err != nil {
				return nil, true, err
			}
			length := int(types.ToInt64(lenVal))
			end := start + length
			if end > len(s) {
				end = len(s)
			}
			return s[start:end], true, nil
		}
		return s[start:], true, nil

	case "split":
		if len(fc.Args) != 2 && len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("split requires 2 or 3 arguments")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		delimVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		s := fmt.Sprintf("%v", val)
		delim := fmt.Sprintf("%v", delimVal)
		parts := strings.Split(s, delim)
		if len(fc.Args) == 3 {
			// split(s, d, i): [part i]; -1 is every part, anything
			// else outside the parts null (the emulator).
			iv, err := evalExpr(eng, fc.Args[2], schema, row)
			if err != nil || iv == nil {
				return nil, true, err
			}
			i := types.ToInt64(iv)
			switch {
			case i == -1:
			case i >= 0 && i < int64(len(parts)):
				parts = parts[i : i+1]
			default:
				return nil, true, nil
			}
		}
		b, _ := json.Marshal(parts)
		return string(b), true, nil

	case "extract":
		// extract(regex, captureGroup, source [, typeLiteral])
		//
		// Two real bugs found and fixed 2026-08-18, both reproduced
		// live before fixing:
		//   1. No-match (or out-of-range captureGroup) returned ""
		//      (empty string), but real ADX's own docs are explicit:
		//      "If there's no match, or the type conversion fails:
		//      null." — confirmed live: extract("xyz([0-9]+)", 1, "no
		//      numbers here") returned "" with isnull()==false, not
		//      the documented null.
		//   2. The optional 4th typeLiteral argument (real syntax:
		//      extract(regex, group, source, typeof(long))) was
		//      entirely unsupported — a 4th argument hard-errored.
		//      Added alongside typeof() parser support (expr.go).
		//      Verified against real ADX's own worked example:
		//      extract(@"-(\d{2})-", 1, DateString, typeof(int)) over
		//      "15-12-2024"/"21-07-2023"/"10-03-2022" -> 12/7/3 as
		//      actual int values (via types.ParseValue, the same
		//      conversion helper already trusted elsewhere in this
		//      engine, e.g. datatable literal parsing) — a failed
		//      conversion (or invalid typeLiteral) returns null too,
		//      per the same documented contract.
		if len(fc.Args) < 3 || len(fc.Args) > 4 {
			return nil, true, fmt.Errorf("extract requires 3-4 arguments (regex, captureGroup, source [, typeLiteral])")
		}
		regexVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		groupVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		sourceVal, err := evalExpr(eng, fc.Args[2], schema, row)
		if err != nil {
			return nil, true, err
		}
		if regexVal == nil || sourceVal == nil {
			return nil, true, nil
		}
		re, err := regexp.Compile(fmt.Sprintf("%v", regexVal))
		if err != nil {
			return nil, true, nil
		}
		group := int(types.ToInt64(groupVal))
		matches := re.FindStringSubmatch(fmt.Sprintf("%v", sourceVal))
		if group < 0 || group >= len(matches) {
			return nil, true, nil
		}
		matched := matches[group]
		if len(fc.Args) == 4 {
			typeVal, err := evalExpr(eng, fc.Args[3], schema, row)
			if err != nil {
				return nil, true, err
			}
			if typeVal == nil {
				return nil, true, nil
			}
			kt, terr := types.ParseType(fmt.Sprintf("%v", typeVal))
			if terr != nil {
				return nil, true, nil
			}
			converted, cerr := types.ParseValue(matched, kt)
			if cerr != nil {
				return nil, true, nil
			}
			return converted, true, nil
		}
		return matched, true, nil

	case "extract_all":
		// extract_all(regex [, captureGroups], source)
		//
		// Real bug found and fixed 2026-08-18: this case only ever
		// accepted 2 arguments (regex, source) — the optional
		// captureGroups selector argument real ADX's own docs
		// document was entirely unsupported (a 3-arg call would hard
		// error). It also used re.FindAllString, which returns the
		// WHOLE regex match text, not the capture group's own
		// content — for extract_all(@"a=(\d+)", "a=123") real ADX
		// returns ["123"] (the group content), this returned
		// ["a=123"] (the whole match) instead. Reproduced live before
		// fixing.
		//
		// Rewritten to implement real ADX's own documented shape
		// exactly, verified against all 4 of its own worked examples
		// on the GUID "82b8be2d-dfa7-4bd1-8f63-24ad26d31449": a
		// single-group regex with no captureGroups arg returns a 1-D
		// array of that group's matched values across all matches; a
		// multi-group regex (with or without an explicit captureGroups
		// selector, which may reference groups by 1-based numeric
		// index OR by name) returns a 2-D array, one sub-array per
		// match, in the requested group order; no match at all
		// returns null (not an empty array).
		if len(fc.Args) < 2 || len(fc.Args) > 3 {
			return nil, true, fmt.Errorf("extract_all requires 2-3 arguments")
		}
		regexVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		var captureGroupsVal types.Value
		var sourceVal types.Value
		if len(fc.Args) == 3 {
			captureGroupsVal, err = evalExpr(eng, fc.Args[1], schema, row)
			if err != nil {
				return nil, true, err
			}
			sourceVal, err = evalExpr(eng, fc.Args[2], schema, row)
			if err != nil {
				return nil, true, err
			}
		} else {
			sourceVal, err = evalExpr(eng, fc.Args[1], schema, row)
			if err != nil {
				return nil, true, err
			}
		}
		if regexVal == nil || sourceVal == nil {
			return nil, true, nil
		}
		re, err := regexp.Compile(fmt.Sprintf("%v", regexVal))
		if err != nil {
			return nil, true, nil
		}

		var selected []int
		if captureGroupsVal != nil {
			arr, ok := parseJSONArray(captureGroupsVal)
			if !ok {
				return nil, true, fmt.Errorf("extract_all: captureGroups must be a dynamic array")
			}
			names := re.SubexpNames()
			for _, el := range arr {
				switch v := el.(type) {
				case float64:
					selected = append(selected, int(v))
				case string:
					found := -1
					for i, n := range names {
						if n == v {
							found = i
							break
						}
					}
					if found < 0 {
						return nil, true, fmt.Errorf("extract_all: unknown capture group name %q", v)
					}
					selected = append(selected, found)
				default:
					return nil, true, fmt.Errorf("extract_all: invalid captureGroups element %v", el)
				}
			}
		} else {
			for i := 1; i <= re.NumSubexp(); i++ {
				selected = append(selected, i)
			}
		}
		if len(selected) == 0 {
			return nil, true, nil
		}

		matches := re.FindAllStringSubmatch(fmt.Sprintf("%v", sourceVal), -1)
		if matches == nil {
			return nil, true, nil
		}

		var result interface{}
		if len(selected) == 1 {
			arr := make([]string, 0, len(matches))
			idx := selected[0]
			for _, m := range matches {
				if idx < len(m) {
					arr = append(arr, m[idx])
				} else {
					arr = append(arr, "")
				}
			}
			result = arr
		} else {
			arr := make([][]string, 0, len(matches))
			for _, m := range matches {
				row := make([]string, 0, len(selected))
				for _, idx := range selected {
					if idx < len(m) {
						row = append(row, m[idx])
					} else {
						row = append(row, "")
					}
				}
				arr = append(arr, row)
			}
			result = arr
		}
		b, _ := json.Marshal(result)
		return string(b), true, nil

	case "replace_string":
		if len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("replace_string requires 3 arguments")
		}
		sourceVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		lookupVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		rewriteVal, err := evalExpr(eng, fc.Args[2], schema, row)
		if err != nil {
			return nil, true, err
		}
		if sourceVal == nil {
			return nil, true, nil
		}
		return strings.ReplaceAll(
			fmt.Sprintf("%v", sourceVal),
			fmt.Sprintf("%v", lookupVal),
			fmt.Sprintf("%v", rewriteVal),
		), true, nil

	case "replace_regex":
		// replace_regex(source, lookup_regex, rewrite_pattern)
		//
		// Real bug found and fixed 2026-08-18: this case passed
		// rewrite_pattern straight to Go's regexp.ReplaceAllString,
		// which uses Go's own $0/$1/$2 backreference syntax — but
		// real ADX's own documented syntax for this exact function is
		// \0/\1/\2 (its own worked example is literally
		// replace_regex(str, @'is (\d+)', @'was: \1')). A query
		// copy-pasted straight from Microsoft's own docs previously
		// produced the literal, unsubstituted text "was: \1" instead
		// of "was: 1" — silently wrong, no error at all. Reproduced
		// live before fixing (once @"..."/@'...' verbatim string
		// support, added alongside this fix, made it possible to
		// even write the exact real-docs query in the first place).
		// Fixed via kqlRewritePatternToGo, which also escapes any
		// literal '$' the caller's rewrite_pattern contains (which Go
		// would otherwise try to interpret as ITS OWN backreference
		// syntax) so it passes through literally, matching real KQL's
		// own documented syntax exactly.
		if len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("replace_regex requires 3 arguments")
		}
		sourceVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		regexVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		rewriteVal, err := evalExpr(eng, fc.Args[2], schema, row)
		if err != nil {
			return nil, true, err
		}
		if sourceVal == nil {
			return nil, true, nil
		}
		re, err := regexp.Compile(fmt.Sprintf("%v", regexVal))
		if err != nil {
			return nil, true, nil
		}
		goRewrite := kqlRewritePatternToGo(fmt.Sprintf("%v", rewriteVal))
		return re.ReplaceAllString(fmt.Sprintf("%v", sourceVal), goRewrite), true, nil

	case "regex_quote":
		// regex_quote(string) — verified against real ADX's own docs
		// before implementing, its own worked example checked
		// exactly: regex_quote('(so$me.Te^xt)') ==
		// `\(so\$me\.Te\^xt\)`. Matches Go's own regexp.QuoteMeta
		// exactly, confirmed directly against this exact input rather
		// than assumed from the two functions sharing a similar name
		// — both escape the RE2/PCRE-common special character set
		// (\.+*?()|[]{}^$) identically.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("regex_quote requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return regexp.QuoteMeta(fmt.Sprintf("%v", val)), true, nil

	case "replace_strings":
		// replace_strings(text, lookups, rewrites) — verified against
		// real ADX's own docs before implementing, ALL FOUR of its
		// own worked examples checked with exact values. The actual
		// algorithm is genuinely non-obvious and was reverse-
		// engineered from the worked examples themselves, not
		// assumed from the name: lookups are applied as SEQUENTIAL,
		// ORDER-DEPENDENT full passes over the text (not a single
		// simultaneous priority scan) — lookups[0] is fully applied
		// first (all its own non-overlapping matches found and
		// replaced/protected), THEN lookups[1] is applied, but only
		// against whatever text is still UNPROTECTED (i.e., text that
		// wasn't already consumed by an earlier lookup's own match,
		// whether that match was replaced or kept unchanged due to a
		// non-string rewrite). This "protect already-matched spans
		// from later lookups" rule is what makes real ADX's own two
		// order-reversal worked examples (identical lookups/rewrites,
		// reversed order, giving genuinely different outputs: "This
		// was..." vs "Thwas was...") come out correctly — a naive
		// "try each lookup at each position, first match wins"
		// single-scan algorithm does NOT reproduce both of those
		// examples (confirmed by hand-tracing before choosing this
		// design). Non-string lookup array elements are skipped
		// entirely (never matched); non-string rewrite elements still
		// consume/protect the matched span but leave the original
		// text there unchanged (verified against the docs' own
		// "Nonstring replacement" example).
		if len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("replace_strings requires 3 arguments (text, lookups, rewrites)")
		}
		textVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if textVal == nil {
			return nil, true, nil
		}
		lookupsVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		rewritesVal, err := evalExpr(eng, fc.Args[2], schema, row)
		if err != nil {
			return nil, true, err
		}
		lookupsArr, lOk := parseJSONArray(lookupsVal)
		if !lOk {
			return nil, true, fmt.Errorf("replace_strings: lookups must be a dynamic array")
		}
		rewritesArr, rOk := parseJSONArray(rewritesVal)
		if !rOk {
			return nil, true, fmt.Errorf("replace_strings: rewrites must be a dynamic array")
		}
		return replaceStringsSequential(fmt.Sprintf("%v", textVal), lookupsArr, rewritesArr), true, nil

	case "unicode_codepoints_from_string", "to_utf8":
		// unicode_codepoints_from_string(value) — verified against
		// real ADX's own docs before implementing, all 3 of its own
		// worked examples checked with exact values, including
		// non-ASCII text ("קוסטו - Kusto" -> [1511,1493,1505,1496,
		// 1493,32,45,32,75,117,115,116,111]). Codepoints, not bytes —
		// implemented via Go's own []rune conversion, which already
		// decodes UTF-8 into Unicode codepoints one rune at a time,
		// matching this function's own documented semantics directly.
		// to_utf8() is real ADX's own documented deprecated alias,
		// routed here identically.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("%s requires 1 argument", fc.Name)
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		runes := []rune(fmt.Sprintf("%v", val))
		codepoints := make([]int64, len(runes))
		for i, r := range runes {
			codepoints[i] = int64(r)
		}
		b, _ := json.Marshal(codepoints)
		return string(b), true, nil

	case "unicode_codepoints_to_string":
		// unicode_codepoints_to_string(array) — the documented inverse
		// of unicode_codepoints_from_string(), verified via round trip
		// against real ADX's own third worked example
		// (unicode_codepoints_to_string(unicode_codepoints_from_string(
		// "Kusto")) == "Kusto").
		// Arguments are code points or arrays of them, concatenated:
		// unicode_codepoints_to_string(dynamic([75, 117]), 115).
		if len(fc.Args) < 1 {
			return nil, true, fmt.Errorf("unicode_codepoints_to_string requires at least 1 argument")
		}
		var runes []rune
		for _, a := range fc.Args {
			val, err := evalExpr(eng, a, schema, row)
			if err != nil {
				return nil, true, err
			}
			if val == nil {
				return nil, true, nil
			}
			if inferExprType(eng, a, schema) == types.TypeDynamic {
				arr, ok := parseJSONArray(val)
				if !ok {
					return nil, true, nil
				}
				for _, el := range arr {
					runes = append(runes, rune(int64(types.ToFloat64(el))))
				}
				continue
			}
			runes = append(runes, rune(types.ToInt64(val)))
		}
		return string(runes), true, nil

	case "isascii":
		// isascii(value) — verified against real ADX's own docs:
		// "returns true if the argument is a valid ASCII string" —
		// every byte in [0x00, 0x7F].
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("isascii requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		s := fmt.Sprintf("%v", val)
		for i := 0; i < len(s); i++ {
			if s[i] > 0x7F {
				return false, true, nil
			}
		}
		return true, true, nil

	case "isutf8":
		// isutf8(value) — verified against real ADX's own docs:
		// "returns true if the argument is a valid UTF8 string" —
		// implemented via Go's own utf8.ValidString, the standard
		// library's own UTF-8 validity check.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("isutf8 requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return utf8.ValidString(fmt.Sprintf("%v", val)), true, nil

	case "trim":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("trim requires 2 arguments")
		}
		regexVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		sourceVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if sourceVal == nil {
			return nil, true, nil
		}
		pattern := fmt.Sprintf("%v", regexVal)
		s := fmt.Sprintf("%v", sourceVal)
		reStart, err := regexp.Compile("^(?:" + pattern + ")+")
		if err == nil {
			s = reStart.ReplaceAllString(s, "")
		}
		reEnd, err := regexp.Compile("(?:" + pattern + ")+$")
		if err == nil {
			s = reEnd.ReplaceAllString(s, "")
		}
		return s, true, nil

	case "trim_start":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("trim_start requires 2 arguments")
		}
		regexVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		sourceVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if sourceVal == nil {
			return nil, true, nil
		}
		re, err := regexp.Compile("^(?:" + fmt.Sprintf("%v", regexVal) + ")+")
		if err != nil {
			return fmt.Sprintf("%v", sourceVal), true, nil
		}
		return re.ReplaceAllString(fmt.Sprintf("%v", sourceVal), ""), true, nil

	case "trim_end":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("trim_end requires 2 arguments")
		}
		regexVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		sourceVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if sourceVal == nil {
			return nil, true, nil
		}
		re, err := regexp.Compile("(?:" + fmt.Sprintf("%v", regexVal) + ")+$")
		if err != nil {
			return fmt.Sprintf("%v", sourceVal), true, nil
		}
		return re.ReplaceAllString(fmt.Sprintf("%v", sourceVal), ""), true, nil

	case "around":
		// around(value, center, delta): center-delta <= value <=
		// center+delta; null if any argument is null.
		if len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("around requires 3 arguments")
		}
		var vals [3]types.Value
		for i, a := range fc.Args {
			v, err := evalExpr(eng, a, schema, row)
			if err != nil {
				return nil, true, err
			}
			if v == nil {
				return nil, true, nil
			}
			vals[i] = v
		}
		t := inferExprType(eng, fc.Args[0], schema)
		var x, c, d float64
		if t == types.TypeDatetime && inferExprType(eng, fc.Args[2], schema) == types.TypeTimespan {
			// datetimes are nanoseconds, timespans ticks
			x, c, d = float64(types.ToInt64(vals[0])), float64(types.ToInt64(vals[1])), float64(types.ToInt64(vals[2]))*100
		} else {
			x, c, d = types.ToFloat64(vals[0]), types.ToFloat64(vals[1]), types.ToFloat64(vals[2])
		}
		return x >= c-d && x <= c+d, true, nil

	case "extent_id":
		// The emulator answers the empty guid (and extent_tags() null):
		// gokql's extents carry short ids and no tags.
		return "00000000-0000-0000-0000-000000000000", true, nil

	case "extent_tags":
		return nil, true, nil

	case "current_cluster_endpoint":
		// This cluster's name from gokql.yaml ("localhost",
		// as the emulator answers, when unset). See cluster.go.
		if eng == nil {
			return "localhost", true, nil
		}
		return eng.clusterName(), true, nil

	case "current_principal":
		if eng != nil {
			return eng.principal(), true, nil
		}
		return "<null identity>", true, nil

	case "current_principal_details":
		p := "<null identity>"
		if eng != nil {
			p = eng.principal()
		}
		var buf strings.Builder
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(map[string]interface{}{"Type": "LocalUser", "DisplayName": p, "FQN": p})
		return strings.TrimSpace(buf.String()), true, nil

	case "current_principal_is_member_of":
		// No groups locally: true only when the principal itself is named.
		p := "<null identity>"
		if eng != nil {
			p = eng.principal()
		}
		for _, a := range fc.Args {
			v, err := evalExpr(eng, a, schema, row)
			if err != nil {
				return nil, true, err
			}
			if s, ok := v.(string); ok && strings.EqualFold(s, p) {
				return true, true, nil
			}
		}
		return false, true, nil

	case "current_database":
		if eng != nil && eng.Catalog != nil && eng.Catalog.Database != nil && eng.Catalog.Database.Name != "" {
			return eng.Catalog.Database.Name, true, nil
		}
		return "", true, nil

	case "guid":
		// guid(null); guid(literal) is handled by the parser.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("guid requires 1 argument")
		}
		v, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil || v == nil {
			return nil, true, err
		}
		g := strings.ToLower(strings.Trim(fmt.Sprintf("%v", v), "{} "))
		return g, true, nil

	case "estimate_data_size":
		// estimate_data_size(col, ... | *): bytes by type: 8 for
		// long/real/datetime/timespan, 4 int, 1 bool, 16 guid and
		// decimal, the length for string and dynamic, 0 for null.
		if len(fc.Args) < 1 {
			return nil, true, fmt.Errorf("estimate_data_size requires at least 1 argument")
		}
		var total int64
		size := func(v types.Value, t types.KQLType) int64 {
			if v == nil {
				return 0
			}
			switch t {
			case types.TypeLong, types.TypeReal, types.TypeDatetime, types.TypeTimespan:
				return 8
			case types.TypeInt:
				return 4
			case types.TypeBool:
				return 1
			case types.TypeGUID, types.TypeDecimal:
				return 16
			}
			return int64(len(fmt.Sprintf("%v", v)))
		}
		for _, a := range fc.Args {
			if _, ok := a.(*parser.StarExpr); ok {
				for i, c := range schema.Columns {
					if i < len(row) && c.Name != timeReceivedColumnName {
						total += size(row[i], c.Type)
					}
				}
				continue
			}
			v, err := evalExpr(eng, a, schema, row)
			if err != nil {
				return nil, true, err
			}
			total += size(v, inferExprType(eng, a, schema))
		}
		return total, true, nil

	case "indexof":
		if len(fc.Args) < 2 {
			return nil, true, fmt.Errorf("indexof requires at least 2 arguments")
		}
		sourceVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		lookupVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if sourceVal == nil || lookupVal == nil {
			return nil, true, nil
		}
		s := kqlToString(sourceVal, inferExprType(eng, fc.Args[0], schema))
		lookup := kqlToString(lookupVal, inferExprType(eng, fc.Args[1], schema))
		// indexof(string, match [, start [, length [, occurrence]]]):
		// a negative start counts from the end; length -1 is
		// unlimited (below -1 is null); each further occurrence is
		// searched from just after the previous match over the same
		// length, as the reference example's idx8 (= 9) requires.
		opt := func(i int, def int64) (int64, bool) {
			if len(fc.Args) <= i {
				return def, true
			}
			v, err := evalExpr(eng, fc.Args[i], schema, row)
			if err != nil || v == nil {
				return 0, false
			}
			return types.ToInt64(v), true
		}
		start, ok1 := opt(2, 0)
		length, ok2 := opt(3, -1)
		occ, ok3 := opt(4, 1)
		if !ok1 || !ok2 || !ok3 || length < -1 || occ < 0 {
			return nil, true, nil
		}
		n := int64(len(s))
		if start < 0 {
			start += n
			if start < 0 {
				start = 0
			}
		}
		if start > n {
			return int64(-1), true, nil
		}
		if occ == 0 {
			occ = 1
		}
		// length bounds where a match may start ([pos, pos+length)),
		// not where it ends: indexof("abcdefg", "cde", 1, 2) is 2.
		pos := start
		for k := int64(0); k < occ; k++ {
			idx := strings.Index(s[pos:], lookup)
			if idx < 0 || length >= 0 && int64(idx) >= length {
				return int64(-1), true, nil
			}
			if k == occ-1 {
				return pos + int64(idx), true, nil
			}
			pos += int64(idx) + 1
		}
		return int64(-1), true, nil

	case "indexof_regex":
		// indexof_regex(string, match [, start [, length [, occurrence]]])
		// Verified against real ADX's own docs (indexof-regex-function.md)
		// and its full worked example with exact output values, fetched
		// directly rather than assumed to mirror indexof()'s semantics:
		//   idx1 = indexof_regex("abcabc", @"a.c")                    -> 0
		//   idx2 = indexof_regex("abcabcdefg", @"a.c", 0, 9, 2)       -> 3
		//   idx3 = indexof_regex("abcabc", @"a.c", 1, -1, 2)          -> -1
		//   idx4 = indexof_regex("ababaa", @"a.a", 0, -1, 2)          -> -1
		//   idx5 = indexof_regex("abcabc", @"a|ab", -1)               -> null
		// A genuine discrepancy was found between two sections of the
		// SAME doc page while verifying this: the Parameters table's
		// prose for `start` says "a negative value will offset the
		// starting search position from the end of the string" (this
		// looks like boilerplate copied from indexof()'s own doc), but
		// the Returns section states plainly "returns null if start is
		// less than 0" -- and the worked example's own idx5 case
		// (start=-1) confirms a null result, not an end-relative
		// offset. The worked example (ground truth) wins over the
		// ambiguous/self-contradicting prose: negative start -> null
		// here, no offset-from-end behavior implemented.
		// occurrence < 0 or length < -1 also -> null, per the same
		// Returns section, also unverified against a worked example
		// (none of the 5 cases exercise those two) but taken as stated
		// since nothing contradicts it.
		// Matches are non-overlapping (explicitly documented), found
		// via Go's regexp.FindAllStringIndex, which already returns
		// successive non-overlapping leftmost matches -- the same RE2
		// engine already used throughout this codebase for extract(),
		// extract_all(), replace_regex(), and the parse operator, so no
		// new regex semantics are introduced here.
		if len(fc.Args) < 2 || len(fc.Args) > 5 {
			return nil, true, fmt.Errorf("indexof_regex requires 2-5 arguments (string, match [, start [, length [, occurrence]]])")
		}
		sourceVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		matchVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if sourceVal == nil || matchVal == nil {
			return nil, true, nil
		}
		s := fmt.Sprintf("%v", sourceVal)
		re, err := regexp.Compile(fmt.Sprintf("%v", matchVal))
		if err != nil {
			return nil, true, nil
		}
		start := 0
		if len(fc.Args) >= 3 {
			sv, err := evalExpr(eng, fc.Args[2], schema, row)
			if err != nil {
				return nil, true, err
			}
			start = int(types.ToInt64(sv))
		}
		length := -1
		if len(fc.Args) >= 4 {
			lv, err := evalExpr(eng, fc.Args[3], schema, row)
			if err != nil {
				return nil, true, err
			}
			length = int(types.ToInt64(lv))
		}
		occurrence := 1
		if len(fc.Args) >= 5 {
			ov, err := evalExpr(eng, fc.Args[4], schema, row)
			if err != nil {
				return nil, true, err
			}
			occurrence = int(types.ToInt64(ov))
		}
		if start < 0 || occurrence < 0 || length < -1 {
			return nil, true, nil
		}
		if start >= len(s) {
			return int64(-1), true, nil
		}
		searchEnd := len(s)
		if length != -1 {
			if start+length < searchEnd {
				searchEnd = start + length
			}
		}
		window := s[start:searchEnd]
		matches := re.FindAllStringIndex(window, -1)
		if occurrence == 0 || occurrence > len(matches) {
			return int64(-1), true, nil
		}
		return int64(start + matches[occurrence-1][0]), true, nil

	case "has_any_index":
		// has_any_index(source, values) — verified against real ADX's
		// own has_any_index docs before implementing: "searches the
		// string for items specified in the array and returns the
		// position in the array of the first item found in the
		// string." Reuses hasTerm (eval.go) for the actual per-item
		// matching, the same word-bounded, case-insensitive semantics
		// this engine's own `has` operator already implements
		// correctly elsewhere — not a second, separate matching
		// implementation. Returns -1 if none of the array items are
		// found, or if values is empty, matching real ADX's own
		// documented worked example exactly (verified against every
		// one of its five cases, not just the simplest one).
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("has_any_index requires 2 arguments (source, values)")
		}
		sourceVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		valuesVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if sourceVal == nil {
			return int64(-1), true, nil
		}
		s := fmt.Sprintf("%v", sourceVal)
		arr, ok := parseJSONArray(valuesVal)
		if !ok {
			return int64(-1), true, nil
		}
		for i, item := range arr {
			term := fmt.Sprintf("%v", item)
			if hasTerm(s, term, false) {
				return int64(i), true, nil
			}
		}
		return int64(-1), true, nil

	case "countof":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("countof requires 2 arguments")
		}
		sourceVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		searchVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if sourceVal == nil || searchVal == nil {
			return int64(0), true, nil
		}
		return int64(strings.Count(fmt.Sprintf("%v", sourceVal), fmt.Sprintf("%v", searchVal))), true, nil

	case "reverse":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("reverse requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		runes := []rune(hashInputText(eng, fc.Args[0], schema, val))
		for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
			runes[i], runes[j] = runes[j], runes[i]
		}
		return string(runes), true, nil

	case "isempty":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("isempty requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return true, true, nil
		}
		return fmt.Sprintf("%v", val) == "", true, nil

	case "isnotempty":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("isnotempty requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return false, true, nil
		}
		return fmt.Sprintf("%v", val) != "", true, nil

	case "strcmp":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("strcmp requires 2 arguments")
		}
		v1, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		v2, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		s1 := fmt.Sprintf("%v", v1)
		s2 := fmt.Sprintf("%v", v2)
		return int64(strings.Compare(s1, s2)), true, nil

	case "strrep":
		// strrep(source, count [, delimiter])
		if len(fc.Args) < 2 || len(fc.Args) > 3 {
			return nil, true, fmt.Errorf("strrep requires 2 or 3 arguments")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		countVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		n := int(types.ToInt64(countVal))
		if n <= 0 {
			return "", true, nil
		}
		s := kqlToString(val, inferExprType(eng, fc.Args[0], schema))
		delim := ""
		if len(fc.Args) == 3 {
			dv, err := evalExpr(eng, fc.Args[2], schema, row)
			if err != nil {
				return nil, true, err
			}
			delim = fmt.Sprintf("%v", dv)
		}
		parts := make([]string, n)
		for i := range parts {
			parts[i] = s
		}
		return strings.Join(parts, delim), true, nil

	case "translate":
		// translate(searchList, replacementList, source)
		if len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("translate requires 3 arguments")
		}
		searchVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		replaceVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		sourceVal, err := evalExpr(eng, fc.Args[2], schema, row)
		if err != nil {
			return nil, true, err
		}
		if sourceVal == nil {
			return nil, true, nil
		}
		searchChars := []rune(fmt.Sprintf("%v", searchVal))
		replaceChars := []rune(fmt.Sprintf("%v", replaceVal))
		source := fmt.Sprintf("%v", sourceVal)
		var sb strings.Builder
		for _, ch := range source {
			found := false
			for i, sc := range searchChars {
				if ch == sc {
					if i < len(replaceChars) {
						sb.WriteRune(replaceChars[i])
					}
					// else: character is deleted (no replacement)
					found = true
					break
				}
			}
			if !found {
				sb.WriteRune(ch)
			}
		}
		return sb.String(), true, nil

	case "strcat_delim":
		// strcat_delim(delimiter, arg1, arg2, ...) — scalar version
		if len(fc.Args) < 2 {
			return nil, true, fmt.Errorf("strcat_delim requires at least 2 arguments")
		}
		delimVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		delim := fmt.Sprintf("%v", delimVal)
		var parts []string
		for _, arg := range fc.Args[1:] {
			s, notNull, err := exprToString(eng, arg, schema, row)
			if err != nil {
				return nil, true, err
			}
			if notNull && s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, delim), true, nil

	case "url_decode":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("url_decode requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return urlDecodeDotNet(fmt.Sprintf("%v", val)), true, nil

	case "string_size":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("string_size requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return int64(0), true, nil
		}
		return int64(len([]byte(fmt.Sprintf("%v", val)))), true, nil

	default:
		return nil, false, nil
	}
}

// kqlRewritePatternToGo translates real KQL's replace_regex
// rewrite_pattern backreference syntax (\0, \1, \2, ... for the whole
// match / capture groups) to Go's regexp.ReplaceAllString syntax ($0,
// $1, $2, ...), and escapes any literal '$' in the caller's pattern
// (which Go's own replace syntax would otherwise try to interpret as
// ITS OWN backreference) so it passes through literally — matching
// real KQL's own documented syntax exactly. Added 2026-08-18
// alongside fixing replace_regex's real backreference-syntax bug; see
// that case's own comment in evalStringFunc for the full story.
func kqlRewritePatternToGo(pattern string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '$':
			b.WriteString("$$")
		case c == '\\' && i+1 < len(pattern) && pattern[i+1] >= '0' && pattern[i+1] <= '9':
			j := i + 1
			for j < len(pattern) && pattern[j] >= '0' && pattern[j] <= '9' {
				j++
			}
			b.WriteByte('$')
			b.WriteString(pattern[i+1 : j])
			i = j - 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// strSegment is one piece of replaceStringsSequential's own working
// representation of the text being transformed: either UNPROTECTED
// (still eligible to be matched by a later lookup) or PROTECTED
// (already consumed by an earlier lookup's own match — whether that
// match was replaced with a rewrite or, for a non-string rewrite,
// left as the original matched text — either way it's now off limits
// to every subsequent lookup).
type strSegment struct {
	text      string
	protected bool
}

// replaceStringsSequential implements replace_strings()'s own real,
// reverse-engineered algorithm — see that case's own doc comment in
// evalStringFunc for the full worked-example verification story.
// lookups/rewrites are walked as parallel JSON arrays (via
// parseJSONArray's own []interface{} element type); a non-string
// element in either array is detected via a Go type assertion on
// that raw JSON-decoded value.
func replaceStringsSequential(text string, lookups, rewrites []interface{}) string {
	segments := []strSegment{{text: text, protected: false}}
	for i, lookupRaw := range lookups {
		lookup, isStr := lookupRaw.(string)
		if !isStr || lookup == "" {
			// Non-string lookup elements are ignored entirely (never
			// matched); an empty-string lookup is also skipped as a
			// defensive measure against an infinite/zero-width-match
			// loop, an undocumented edge case.
			continue
		}
		var rewrite string
		rewriteIsStr := false
		if i < len(rewrites) {
			rewrite, rewriteIsStr = rewrites[i].(string)
		}
		var newSegments []strSegment
		for _, seg := range segments {
			if seg.protected {
				newSegments = append(newSegments, seg)
				continue
			}
			remaining := seg.text
			for {
				idx := strings.Index(remaining, lookup)
				if idx < 0 {
					if remaining != "" {
						newSegments = append(newSegments, strSegment{text: remaining, protected: false})
					}
					break
				}
				if idx > 0 {
					newSegments = append(newSegments, strSegment{text: remaining[:idx], protected: false})
				}
				replacement := lookup // non-string rewrite: keep original matched text
				if rewriteIsStr {
					replacement = rewrite
				}
				newSegments = append(newSegments, strSegment{text: replacement, protected: true})
				remaining = remaining[idx+len(lookup):]
			}
		}
		segments = newSegments
	}
	var sb strings.Builder
	for _, seg := range segments {
		sb.WriteString(seg.text)
	}
	return sb.String()
}
