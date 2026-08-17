package engine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// evalStringFunc handles string manipulation functions.
func evalStringFunc(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "strlen":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("strlen requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return int64(0), true, nil
		}
		return int64(len(fmt.Sprintf("%v", val))), true, nil

	case "tolower":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("tolower requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
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
		val, err := evalExpr(fc.Args[0], schema, row)
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
			val, err := evalExpr(arg, schema, row)
			if err != nil {
				return nil, true, err
			}
			if val != nil {
				sb.WriteString(fmt.Sprintf("%v", val))
			}
		}
		return sb.String(), true, nil

	case "substring":
		if len(fc.Args) < 2 || len(fc.Args) > 3 {
			return nil, true, fmt.Errorf("substring requires 2 or 3 arguments")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		s := fmt.Sprintf("%v", val)
		startVal, err := evalExpr(fc.Args[1], schema, row)
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
			lenVal, err := evalExpr(fc.Args[2], schema, row)
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
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("split requires 2 arguments")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		delimVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		s := fmt.Sprintf("%v", val)
		delim := fmt.Sprintf("%v", delimVal)
		parts := strings.Split(s, delim)
		b, _ := json.Marshal(parts)
		return string(b), true, nil

	case "extract":
		if len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("extract requires 3 arguments (regex, captureGroup, source)")
		}
		regexVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		groupVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		sourceVal, err := evalExpr(fc.Args[2], schema, row)
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
		if group >= 0 && group < len(matches) {
			return matches[group], true, nil
		}
		return "", true, nil

	case "extract_all":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("extract_all requires 2 arguments")
		}
		regexVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		sourceVal, err := evalExpr(fc.Args[1], schema, row)
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
		allMatches := re.FindAllString(fmt.Sprintf("%v", sourceVal), -1)
		b, _ := json.Marshal(allMatches)
		return string(b), true, nil

	case "replace_string":
		if len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("replace_string requires 3 arguments")
		}
		sourceVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		lookupVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		rewriteVal, err := evalExpr(fc.Args[2], schema, row)
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
		if len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("replace_regex requires 3 arguments")
		}
		sourceVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		regexVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		rewriteVal, err := evalExpr(fc.Args[2], schema, row)
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
		return re.ReplaceAllString(fmt.Sprintf("%v", sourceVal), fmt.Sprintf("%v", rewriteVal)), true, nil

	case "trim":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("trim requires 2 arguments")
		}
		regexVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		sourceVal, err := evalExpr(fc.Args[1], schema, row)
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
		regexVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		sourceVal, err := evalExpr(fc.Args[1], schema, row)
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
		regexVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		sourceVal, err := evalExpr(fc.Args[1], schema, row)
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

	case "indexof":
		if len(fc.Args) < 2 {
			return nil, true, fmt.Errorf("indexof requires at least 2 arguments")
		}
		sourceVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		lookupVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if sourceVal == nil || lookupVal == nil {
			return nil, true, nil
		}
		s := fmt.Sprintf("%v", sourceVal)
		lookup := fmt.Sprintf("%v", lookupVal)
		start := 0
		if len(fc.Args) >= 3 {
			sv, _ := evalExpr(fc.Args[2], schema, row)
			start = int(types.ToInt64(sv))
		}
		if start >= len(s) {
			return int64(-1), true, nil
		}
		idx := strings.Index(s[start:], lookup)
		if idx < 0 {
			return int64(-1), true, nil
		}
		return int64(start + idx), true, nil

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
		sourceVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		valuesVal, err := evalExpr(fc.Args[1], schema, row)
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
		sourceVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		searchVal, err := evalExpr(fc.Args[1], schema, row)
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
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		runes := []rune(fmt.Sprintf("%v", val))
		for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
			runes[i], runes[j] = runes[j], runes[i]
		}
		return string(runes), true, nil

	case "isempty":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("isempty requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
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
		val, err := evalExpr(fc.Args[0], schema, row)
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
		v1, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		v2, err := evalExpr(fc.Args[1], schema, row)
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
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		countVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		n := int(types.ToInt64(countVal))
		if n <= 0 {
			return "", true, nil
		}
		s := fmt.Sprintf("%v", val)
		delim := ""
		if len(fc.Args) == 3 {
			dv, err := evalExpr(fc.Args[2], schema, row)
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
		searchVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		replaceVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		sourceVal, err := evalExpr(fc.Args[2], schema, row)
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
		delimVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		delim := fmt.Sprintf("%v", delimVal)
		var parts []string
		for _, arg := range fc.Args[1:] {
			val, err := evalExpr(arg, schema, row)
			if err != nil {
				return nil, true, err
			}
			if val != nil {
				s := fmt.Sprintf("%v", val)
				if s != "" {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, delim), true, nil

	case "url_decode":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("url_decode requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		s := fmt.Sprintf("%v", val)
		s = strings.ReplaceAll(s, "%20", " ")
		s = strings.ReplaceAll(s, "%26", "&")
		s = strings.ReplaceAll(s, "%3D", "=")
		s = strings.ReplaceAll(s, "%2B", "+")
		s = strings.ReplaceAll(s, "%25", "%")
		return s, true, nil

	case "string_size":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("string_size requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
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
