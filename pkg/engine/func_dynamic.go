package engine

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// evalDynamicFunc handles dynamic, array, and bag functions.
func evalDynamicFunc(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "array_length":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("array_length requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		arr, ok := parseJSONArray(val)
		if !ok {
			return nil, true, nil
		}
		return int64(len(arr)), true, nil

	case "strcat_array":
		// strcat_array(array, delimiter) — join array elements into a
		// string. Elements format per their JSON value.
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("strcat_array requires 2 arguments (array, delimiter)")
		}
		arrVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		delimVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if arrVal == nil {
			return nil, true, nil
		}
		arr, ok := parseJSONArray(arrVal)
		if !ok {
			return nil, true, nil
		}
		delim := ""
		if delimVal != nil {
			delim = fmt.Sprintf("%v", delimVal)
		}
		parts := make([]string, len(arr))
		for i, el := range arr {
			if s, isStr := el.(string); isStr {
				parts[i] = s
			} else {
				parts[i] = fmt.Sprintf("%v", el)
			}
		}
		return strings.Join(parts, delim), true, nil

	case "array_concat":
		// array_concat(arr1, arr2, ...) — concatenate arrays
		var result []interface{}
		for _, arg := range fc.Args {
			val, err := evalExpr(arg, schema, row)
			if err != nil {
				return nil, true, err
			}
			if val == nil {
				continue
			}
			arr, ok := parseJSONArray(val)
			if ok {
				result = append(result, arr...)
			}
		}
		b, _ := json.Marshal(result)
		return string(b), true, nil

	case "array_index_of":
		// array_index_of(array, value) — returns first index or -1
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("array_index_of requires 2 arguments")
		}
		arrVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		searchVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if arrVal == nil {
			return int64(-1), true, nil
		}
		arr, ok := parseJSONArray(arrVal)
		if !ok {
			return int64(-1), true, nil
		}
		searchStr := fmt.Sprintf("%v", searchVal)
		for i, elem := range arr {
			if fmt.Sprintf("%v", elem) == searchStr {
				return int64(i), true, nil
			}
		}
		return int64(-1), true, nil

	case "array_sort_asc":
		if len(fc.Args) < 1 {
			return nil, true, fmt.Errorf("array_sort_asc requires at least 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		return sortJSONArray(val, true)

	case "array_sort_desc":
		if len(fc.Args) < 1 {
			return nil, true, fmt.Errorf("array_sort_desc requires at least 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		return sortJSONArray(val, false)

	case "array_reverse":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("array_reverse requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		arr, ok := parseJSONArray(val)
		if !ok {
			return nil, true, nil
		}
		for i, j := 0, len(arr)-1; i < j; i, j = i+1, j-1 {
			arr[i], arr[j] = arr[j], arr[i]
		}
		b, _ := json.Marshal(arr)
		return string(b), true, nil

	case "array_slice":
		// array_slice(array, start, end) — inclusive slice
		if len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("array_slice requires 3 arguments")
		}
		arrVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if arrVal == nil {
			return nil, true, nil
		}
		arr, ok := parseJSONArray(arrVal)
		if !ok {
			return nil, true, nil
		}
		startVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		endVal, err := evalExpr(fc.Args[2], schema, row)
		if err != nil {
			return nil, true, err
		}
		start := int(types.ToInt64(startVal))
		end := int(types.ToInt64(endVal))
		n := len(arr)
		// Handle negative indices
		if start < 0 {
			start = n + start
		}
		if end < 0 {
			end = n + end
		}
		if start < 0 {
			start = 0
		}
		if end >= n {
			end = n - 1
		}
		if start > end || start >= n {
			b, _ := json.Marshal([]interface{}{})
			return string(b), true, nil
		}
		b, _ := json.Marshal(arr[start : end+1])
		return string(b), true, nil

	case "bag_keys":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("bag_keys requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		obj, ok := parseJSONObject(val)
		if !ok {
			return nil, true, nil
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b, _ := json.Marshal(keys)
		return string(b), true, nil

	case "bag_has_key":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("bag_has_key requires 2 arguments")
		}
		bagVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		keyVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if bagVal == nil {
			return false, true, nil
		}
		obj, ok := parseJSONObject(bagVal)
		if !ok {
			return false, true, nil
		}
		key := fmt.Sprintf("%v", keyVal)
		_, exists := obj[key]
		return exists, true, nil

	case "pack", "bag_pack", "pack_dictionary":
		// pack(key1, val1, key2, val2, ...)
		if len(fc.Args)%2 != 0 {
			return nil, true, fmt.Errorf("pack requires even number of arguments (key, value pairs)")
		}
		result := make(map[string]interface{})
		for i := 0; i < len(fc.Args); i += 2 {
			keyVal, err := evalExpr(fc.Args[i], schema, row)
			if err != nil {
				return nil, true, err
			}
			valVal, err := evalExpr(fc.Args[i+1], schema, row)
			if err != nil {
				return nil, true, err
			}
			key := fmt.Sprintf("%v", keyVal)
			result[key] = valVal
		}
		b, _ := json.Marshal(result)
		return string(b), true, nil

	case "pack_array":
		// pack_array(v1, v2, ...) — create array from values
		result := make([]interface{}, len(fc.Args))
		for i, arg := range fc.Args {
			val, err := evalExpr(arg, schema, row)
			if err != nil {
				return nil, true, err
			}
			result[i] = val
		}
		b, _ := json.Marshal(result)
		return string(b), true, nil

	case "zip":
		// zip(arr1, arr2, ..., arrN) — verified against real ADX's own
		// zip() docs before implementing: accepts 2-16 dynamic arrays,
		// returns an array whose elements are each an array holding
		// the elements of the input arrays at the same index. Output
		// length is the LONGEST input array's length (not the
		// shortest) — confirmed against the real docs' own second
		// worked example (zip(dynamic(["A",1,1.5]), dynamic([{}, "B"])) ->
		// [["A",{}], [1,"B"], [1.5,null]] — 3 tuples for a 3-and-2-
		// element input pair, the shorter array's missing slot filled
		// with null, not truncating to the shorter length).
		if len(fc.Args) < 2 || len(fc.Args) > 16 {
			return nil, true, fmt.Errorf("zip requires between 2 and 16 array arguments")
		}
		arrays := make([][]interface{}, len(fc.Args))
		maxLen := 0
		for i, arg := range fc.Args {
			val, err := evalExpr(arg, schema, row)
			if err != nil {
				return nil, true, err
			}
			arr, ok := parseJSONArray(val)
			if !ok {
				arr = nil
			}
			arrays[i] = arr
			if len(arr) > maxLen {
				maxLen = len(arr)
			}
		}
		result := make([]interface{}, maxLen)
		for i := 0; i < maxLen; i++ {
			tuple := make([]interface{}, len(arrays))
			for j, arr := range arrays {
				if i < len(arr) {
					tuple[j] = arr[i]
				} else {
					tuple[j] = nil
				}
			}
			result[i] = tuple
		}
		b, _ := json.Marshal(result)
		return string(b), true, nil

	case "parse_csv":
		// parse_csv(csv_text) — verified against real ADX's own
		// parse_csv docs before implementing: "splits a given string
		// representing a single record of comma-separated values and
		// returns a string array." Reuses Go's standard encoding/csv
		// (already imported and used elsewhere in this engine's own
		// file_sources.go for real CSV file ingest) rather than
		// hand-rolling comma/quote-escaping logic a second, separate
		// time — real ADX's own documented escaping rule ("embedded
		// line feeds, commas, and quotes may be escaped using the
		// double quotation mark") is exactly RFC4180-style CSV
		// quoting, which encoding/csv already implements correctly.
		//
		// "This function doesn't support multiple records per row
		// (only the first record is taken)" — real ADX's own stated
		// restriction, matched here by reading only the FIRST record
		// csv.Reader produces and discarding the rest, not erroring
		// on a multi-line input.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("parse_csv requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		reader := csv.NewReader(strings.NewReader(fmt.Sprintf("%v", val)))
		reader.FieldsPerRecord = -1 // don't enforce a fixed column count across (unused) subsequent records
		fields, err := reader.Read()
		if err != nil {
			return nil, true, nil // malformed input — null, matching this engine's own tolerant convention elsewhere for a value that doesn't parse as expected, not an error
		}
		result := make([]interface{}, len(fields))
		for i, f := range fields {
			result[i] = f
		}
		b, _ := json.Marshal(result)
		return string(b), true, nil

	case "array_shift_left", "array_shift_right":
		// array_shift_left(array, shift_count [, default_value]) —
		// verified against real ADX's own docs before implementing:
		// shifts elements left by shift_count (a negative shift_count
		// shifts right instead — real ADX's own documented mechanism
		// for the "right" direction WITHOUT a separate function at
		// all), filling vacated slots with default_value (null if
		// omitted, matching the real docs' own first worked example).
		// array_shift_right(array, shift_count, ...) is the same
		// function with the shift direction inverted (shift_count
		// negated) — real ADX documents both names, this engine
		// supports both rather than only the more fundamental
		// left-shift form, matching what was actually asked for.
		if len(fc.Args) < 2 || len(fc.Args) > 3 {
			return nil, true, fmt.Errorf("%s requires 2 or 3 arguments (array, shift_count [, default_value])", fc.Name)
		}
		arrVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		arr, ok := parseJSONArray(arrVal)
		if !ok {
			return nil, true, nil
		}
		shiftVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		shift := types.ToInt64(shiftVal)
		if fc.Name == "array_shift_right" {
			shift = -shift
		}
		var defaultVal interface{}
		if len(fc.Args) == 3 {
			dv, err := evalExpr(fc.Args[2], schema, row)
			if err != nil {
				return nil, true, err
			}
			defaultVal = dv
		}
		n := int64(len(arr))
		result := make([]interface{}, n)
		for i := int64(0); i < n; i++ {
			srcIdx := i + shift
			if srcIdx >= 0 && srcIdx < n {
				result[i] = arr[srcIdx]
			} else {
				result[i] = defaultVal
			}
		}
		b, _ := json.Marshal(result)
		return string(b), true, nil

	case "bag_merge":
		// bag_merge(bag1, bag2, ..., bagN) — verified against real
		// ADX's own bag_merge docs before implementing: 2-64 property
		// bags merged into one; "if a key is present in multiple
		// input bags, the value associated with the key from the
		// LEFTMOST argument takes precedence" — implemented by writing
		// bags into the result in REVERSE argument order, so an
		// earlier (leftward) bag's write always happens LAST and
		// therefore wins any key collision.
		if len(fc.Args) < 2 || len(fc.Args) > 64 {
			return nil, true, fmt.Errorf("bag_merge requires between 2 and 64 bag arguments")
		}
		bags := make([]map[string]interface{}, len(fc.Args))
		for i, arg := range fc.Args {
			val, err := evalExpr(arg, schema, row)
			if err != nil {
				return nil, true, err
			}
			if obj, ok := parseJSONObject(val); ok {
				bags[i] = obj
			}
		}
		result := make(map[string]interface{})
		for i := len(bags) - 1; i >= 0; i-- {
			for k, v := range bags[i] {
				result[k] = v
			}
		}
		b, _ := json.Marshal(result)
		return string(b), true, nil

	case "bag_remove_keys":
		// bag_remove_keys(bag, keys) — verified against real ADX's own
		// docs before implementing: keys is a dynamic array of
		// strings; "the keys are the first level of the property bag.
		// You can specify keys on the nested levels using JSONPath
		// notation" (e.g. "$.key2.prop1" removes prop1 nested inside
		// key2, leaving key2's other properties and every other
		// top-level key untouched) — "array indexing isn't supported"
		// for the nested form, matching real ADX's own stated
		// restriction; this implementation doesn't attempt array
		// traversal for a "$....[N]...." path either.
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("bag_remove_keys requires 2 arguments (bag, keys)")
		}
		bagVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		obj, ok := parseJSONObject(bagVal)
		if !ok {
			return nil, true, nil
		}
		keysVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		keys, ok := parseJSONArray(keysVal)
		if !ok {
			return nil, true, fmt.Errorf("bag_remove_keys: keys must be a dynamic array of strings")
		}
		result := make(map[string]interface{}, len(obj))
		for k, v := range obj {
			result[k] = v
		}
		for _, kRaw := range keys {
			key, isStr := kRaw.(string)
			if !isStr {
				continue
			}
			if strings.HasPrefix(key, "$.") {
				parts := strings.Split(key[2:], ".")
				removeNestedKey(result, parts)
				continue
			}
			delete(result, key)
		}
		b, _ := json.Marshal(result)
		return string(b), true, nil

	case "set_union":
		return setOperation(fc, schema, row, "union")

	case "set_intersect":
		return setOperation(fc, schema, row, "intersect")

	case "set_difference":
		return setOperation(fc, schema, row, "difference")

	case "treepath":
		// treepath(dynamic) — returns array of paths in the JSON object
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("treepath requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		var obj interface{}
		if err := json.Unmarshal([]byte(fmt.Sprintf("%v", val)), &obj); err != nil {
			return nil, true, nil
		}
		paths := collectPaths("$", obj)
		b, _ := json.Marshal(paths)
		return string(b), true, nil

	default:
		return nil, false, nil
	}
}

// --- helpers ---

func parseJSONArray(val types.Value) ([]interface{}, bool) {
	s := fmt.Sprintf("%v", val)
	var arr []interface{}
	if err := json.Unmarshal([]byte(s), &arr); err != nil {
		return nil, false
	}
	return arr, true
}

// removeNestedKey walks obj through parts (a dotted JSONPath, already
// split on '.') and deletes the final key in place — used by
// bag_remove_keys' own "$.key2.prop1" nested-key form. A path segment
// that doesn't lead to a nested object (missing key, or a non-object
// value along the way) is a silent no-op, matching real ADX's own
// tolerant behavior for a JSONPath that doesn't match the actual
// shape of the data.
func removeNestedKey(obj map[string]interface{}, parts []string) {
	if len(parts) == 0 {
		return
	}
	if len(parts) == 1 {
		delete(obj, parts[0])
		return
	}
	next, ok := obj[parts[0]].(map[string]interface{})
	if !ok {
		return
	}
	removeNestedKey(next, parts[1:])
}

func parseJSONObject(val types.Value) (map[string]interface{}, bool) {
	s := fmt.Sprintf("%v", val)
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return nil, false
	}
	return obj, true
}

func sortJSONArray(val types.Value, asc bool) (types.Value, bool, error) {
	if val == nil {
		return nil, true, nil
	}
	arr, ok := parseJSONArray(val)
	if !ok {
		return nil, true, nil
	}
	sort.Slice(arr, func(i, j int) bool {
		si := fmt.Sprintf("%v", arr[i])
		sj := fmt.Sprintf("%v", arr[j])
		if asc {
			return si < sj
		}
		return si > sj
	})
	b, _ := json.Marshal(arr)
	return string(b), true, nil
}

func setOperation(fc *parser.FuncCall, schema *types.Schema, row types.Row, op string) (types.Value, bool, error) {
	if len(fc.Args) < 2 {
		return nil, true, fmt.Errorf("%s requires at least 2 arguments", "set_"+op)
	}
	// Parse first array
	v1, err := evalExpr(fc.Args[0], schema, row)
	if err != nil {
		return nil, true, err
	}
	arr1, _ := parseJSONArray(v1)
	if arr1 == nil {
		arr1 = []interface{}{}
	}

	set1 := make(map[string]interface{})
	for _, item := range arr1 {
		set1[fmt.Sprintf("%v", item)] = item
	}

	for _, arg := range fc.Args[1:] {
		v, err := evalExpr(arg, schema, row)
		if err != nil {
			return nil, true, err
		}
		arr, _ := parseJSONArray(v)
		if arr == nil {
			arr = []interface{}{}
		}
		set2 := make(map[string]interface{})
		for _, item := range arr {
			set2[fmt.Sprintf("%v", item)] = item
		}

		switch op {
		case "union":
			for k, v := range set2 {
				set1[k] = v
			}
		case "intersect":
			newSet := make(map[string]interface{})
			for k, v := range set1 {
				if _, exists := set2[k]; exists {
					newSet[k] = v
				}
			}
			set1 = newSet
		case "difference":
			for k := range set2 {
				delete(set1, k)
			}
		}
	}

	result := make([]interface{}, 0, len(set1))
	for _, v := range set1 {
		result = append(result, v)
	}
	// Sort for deterministic output
	sort.Slice(result, func(i, j int) bool {
		return fmt.Sprintf("%v", result[i]) < fmt.Sprintf("%v", result[j])
	})
	b, _ := json.Marshal(result)
	return string(b), true, nil
}

func collectPaths(prefix string, v interface{}) []string {
	paths := []string{prefix}
	switch obj := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sub := prefix + "['" + k + "']"
			paths = append(paths, collectPaths(sub, obj[k])...)
		}
	case []interface{}:
		for i, elem := range obj {
			sub := fmt.Sprintf("%s[%d]", prefix, i)
			paths = append(paths, collectPaths(sub, elem)...)
		}
	}
	return paths
}

// strcat_delim scalar helper — used if needed
var _ = strings.Join // ensure import
