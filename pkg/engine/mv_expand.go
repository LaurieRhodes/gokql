package engine

// mv_expand.go — element expansion shared by mv-expand and mv-apply.
//
// A dynamic value expands as follows (ADX mv-expand reference):
//   - null: one null element;
//   - empty array or property bag: no elements;
//   - array: its elements in order;
//   - property bag: one element per key, in the bag's own key order,
//     either a single-entry bag {"key": value} (bag mode, the default)
//     or a [key, value] pair (array mode);
//   - any other scalar: itself, as one element.
//
// JSON is decoded token by token so that key order inside bags, and
// inside nested values, is preserved exactly as written.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// expandCollection returns the elements v expands to. bagAsArray
// selects [key, value] pairs for property bags. dynamic says v comes
// from a dynamic-typed source, whose scalars are held as JSON text
// (dynamic(7) is "7", dynamic("abc") is "\"abc\"") and are decoded;
// a value of any other static type expands to itself.
func expandCollection(v types.Value, bagAsArray, dynamic bool) []types.Value {
	if v == nil {
		return []types.Value{nil}
	}
	s, isStr := v.(string)
	if !isStr {
		return []types.Value{v}
	}
	if vec, ok := decodeCompactVector(s); ok {
		out := make([]types.Value, len(vec))
		for i, f := range vec {
			out[i] = f
		}
		return out
	}
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || (trimmed[0] != '[' && trimmed[0] != '{') {
		if dynamic && json.Valid([]byte(trimmed)) {
			return []types.Value{rawJSONToValue(json.RawMessage(trimmed))}
		}
		return []types.Value{v}
	}
	if trimmed[0] == '[' {
		var raws []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &raws); err != nil {
			return []types.Value{v}
		}
		out := make([]types.Value, len(raws))
		for i, r := range raws {
			out[i] = rawJSONToValue(r)
		}
		return out
	}
	keys, vals, ok := orderedObject([]byte(trimmed))
	if !ok {
		return []types.Value{v}
	}
	out := make([]types.Value, len(keys))
	for i, k := range keys {
		kb, _ := json.Marshal(k)
		var buf bytes.Buffer
		if bagAsArray {
			buf.WriteByte('[')
			buf.Write(kb)
			buf.WriteByte(',')
			buf.Write(compactJSON(vals[i]))
			buf.WriteByte(']')
		} else {
			buf.WriteByte('{')
			buf.Write(kb)
			buf.WriteByte(':')
			buf.Write(compactJSON(vals[i]))
			buf.WriteByte('}')
		}
		out[i] = buf.String()
	}
	return out
}

// orderedObject decodes a JSON object into its keys and raw values in
// document order.
func orderedObject(b []byte) ([]string, []json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, nil, false
	}
	var keys []string
	var vals []json.RawMessage
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, false
		}
		k, ok := tok.(string)
		if !ok {
			return nil, nil, false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, nil, false
		}
		keys = append(keys, k)
		vals = append(vals, raw)
	}
	return keys, vals, true
}

func compactJSON(raw json.RawMessage) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return buf.Bytes()
}

// rawJSONToValue converts one JSON value to an engine value: nested
// arrays and objects stay as compact JSON text (order preserved),
// integers that fit become long, other numbers real.
func rawJSONToValue(raw json.RawMessage) types.Value {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return nil
	}
	switch t[0] {
	case '{', '[':
		return string(compactJSON(t))
	case '"':
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			return nil
		}
		return s
	case 'n':
		return nil
	case 't':
		return true
	case 'f':
		return false
	}
	num := string(t)
	if !strings.ContainsAny(num, ".eE") {
		if n, err := strconv.ParseInt(num, 10, 64); err == nil {
			return n
		}
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return nil
	}
	return f
}

// castElement applies "to typeof(T)": a cast, not a parse. An element
// whose underlying kind does not conform to T becomes null. Strings do
// conform to datetime, timespan and guid, because JSON has no literal
// for those and dynamic values carry them as strings.
func castElement(v types.Value, t types.KQLType) types.Value {
	if v == nil || t == types.TypeDynamic {
		return v
	}
	switch t {
	case types.TypeString:
		if s, ok := v.(string); ok && !looksLikeJSONContainer(s) {
			return s
		}
		return nil
	case types.TypeLong:
		switch x := v.(type) {
		case int64:
			return x
		case int32:
			return int64(x)
		}
		return nil
	case types.TypeInt:
		switch x := v.(type) {
		case int64:
			if x >= math.MinInt32 && x <= math.MaxInt32 {
				return int32(x)
			}
		case int32:
			return x
		}
		return nil
	case types.TypeReal:
		switch x := v.(type) {
		case float64:
			return x
		case int64:
			return float64(x)
		case int32:
			return float64(x)
		}
		return nil
	case types.TypeBool:
		if b, ok := v.(bool); ok {
			return b
		}
		return nil
	case types.TypeDatetime, types.TypeTimespan, types.TypeGUID:
		s, ok := v.(string)
		if !ok {
			return nil
		}
		out, err := types.ParseValue(s, t)
		if err != nil {
			return nil
		}
		return out
	}
	return v
}

func looksLikeJSONContainer(s string) bool {
	t := strings.TrimSpace(s)
	return t != "" && (t[0] == '[' || t[0] == '{') && json.Valid([]byte(t))
}

// applyMvExpand implements mv-expand. All expanded columns of a row
// are expanded in parallel; the row yields as many output rows as its
// longest collection (capped by limit), shorter ones padded with null.
func (e *Engine) applyMvExpand(input *types.Table, op *parser.MvExpandOp) (*types.Table, error) {
	outSchema := types.Schema{Columns: append([]types.Column(nil), input.Schema.Columns...)}
	targets := make([]int, len(op.Columns))
	for i, col := range op.Columns {
		if idx := outSchema.ColumnIndex(col.Name); idx >= 0 {
			outSchema.Columns[idx].Type = col.Type
			targets[i] = idx
			continue
		}
		if ref, ok := col.Source.(*parser.ColumnRef); ok && ref.Name == col.Name {
			return nil, fmt.Errorf("mv-expand: column %q not found", col.Name)
		}
		outSchema.Columns = append(outSchema.Columns, types.Column{Name: col.Name, Type: col.Type})
		targets[i] = len(outSchema.Columns) - 1
	}
	indexCol := -1
	if op.IndexColumn != "" {
		if outSchema.ColumnIndex(op.IndexColumn) >= 0 {
			return nil, fmt.Errorf("mv-expand: with_itemindex column %q already exists", op.IndexColumn)
		}
		outSchema.Columns = append(outSchema.Columns, types.Column{Name: op.IndexColumn, Type: types.TypeLong})
		indexCol = len(outSchema.Columns) - 1
	}

	bagAsArray := op.BagMode == "array"
	dynamicSrc := make([]bool, len(op.Columns))
	for i, col := range op.Columns {
		dynamicSrc[i] = inferExprType(col.Source, &input.Schema) == types.TypeDynamic
	}
	output := types.NewTable(input.Name, outSchema)
	elems := make([][]types.Value, len(op.Columns))
	for _, row := range input.Rows {
		n := 0
		for i, col := range op.Columns {
			v, err := evalExpr(col.Source, &input.Schema, row)
			if err != nil {
				return nil, fmt.Errorf("mv-expand %q: %w", col.Name, err)
			}
			elems[i] = expandCollection(v, bagAsArray, dynamicSrc[i])
			if len(elems[i]) > n {
				n = len(elems[i])
			}
		}
		if op.Limit > 0 && int64(n) > op.Limit {
			n = int(op.Limit)
		}
		for k := 0; k < n; k++ {
			newRow := make(types.Row, len(outSchema.Columns))
			copy(newRow, row)
			for i, col := range op.Columns {
				var v types.Value
				if k < len(elems[i]) {
					v = castElement(elems[i][k], col.Type)
				}
				newRow[targets[i]] = v
			}
			if indexCol >= 0 {
				newRow[indexCol] = int64(k)
			}
			output.AddRow(newRow)
		}
	}
	return output, nil
}

// expandArrayOnly is mv-apply's expansion: an array yields its
// elements; any other value, a property bag included, is one element
// (mv-apply has no bag expansion; the reference applies mv-expand
// inside the subquery for that). Null gives one null element.
func expandArrayOnly(v types.Value, dynamic bool) []types.Value {
	if s, ok := v.(string); ok {
		if t := strings.TrimSpace(s); len(t) > 0 && t[0] == '{' {
			if json.Valid([]byte(t)) {
				return []types.Value{string(compactJSON(json.RawMessage(t)))}
			}
		}
	}
	return expandCollection(v, false, dynamic)
}

// applyMvApply implements mv-apply: for each input row, expand the
// listed arrays in parallel (padding with null, as mv-expand does)
// into a subtable holding the row's columns, run the subquery on it,
// and concatenate the results.
//
// Each output row is the input row's columns followed by the
// subquery's new columns; an input column the subquery also returns
// takes the subquery's value. So "summarize Top2 = make_list(Arr2)"
// gives index, Arr, Top2 (reference example). An input column that an
// expanded column replaces in place (mv-apply Arr on ...) is carried
// only if the subquery returns it, as the reference describes.
func (e *Engine) applyMvApply(input *types.Table, op *parser.MvApplyOp) (*types.Table, error) {
	subSchema := types.Schema{Columns: append([]types.Column(nil), input.Schema.Columns...)}
	targets := make([]int, len(op.Columns))
	dynamicSrc := make([]bool, len(op.Columns))
	for i, col := range op.Columns {
		dynamicSrc[i] = inferExprType(col.Source, &input.Schema) == types.TypeDynamic
		if idx := subSchema.ColumnIndex(col.Name); idx >= 0 {
			subSchema.Columns[idx].Type = col.Type
			targets[i] = idx
			continue
		}
		if ref, ok := col.Source.(*parser.ColumnRef); ok && input.Schema.ColumnIndex(ref.Name) < 0 {
			return nil, fmt.Errorf("mv-apply: column %q not found", ref.Name)
		}
		subSchema.Columns = append(subSchema.Columns, types.Column{Name: col.Name, Type: col.Type})
		targets[i] = len(subSchema.Columns) - 1
	}
	indexCol := -1
	if op.IndexColumn != "" {
		if subSchema.ColumnIndex(op.IndexColumn) >= 0 {
			return nil, fmt.Errorf("mv-apply: with_itemindex column %q already exists", op.IndexColumn)
		}
		subSchema.Columns = append(subSchema.Columns, types.Column{Name: op.IndexColumn, Type: types.TypeLong})
		indexCol = len(subSchema.Columns) - 1
	}

	runSub := func(sub *types.Table) (*types.Table, error) {
		result := sub
		var err error
		for _, subOp := range op.Operators {
			if result, err = e.applyOperator(result, subOp); err != nil {
				return nil, fmt.Errorf("mv-apply: %w", err)
			}
		}
		return result, nil
	}

	inPlace := map[string]bool{}
	for _, col := range op.Columns {
		if input.Schema.ColumnIndex(col.Name) >= 0 {
			inPlace[col.Name] = true
		}
	}
	// merge builds the output schema from the first result and the
	// per-row combination of input row and result row.
	var outCols []types.Column
	type src struct {
		fromResult bool
		idx        int
	}
	var plan []src
	planFor := func(res types.Schema) error {
		var cols []types.Column
		var p []src
		used := map[string]bool{}
		for i, c := range input.Schema.Columns {
			if ri := res.ColumnIndex(c.Name); ri >= 0 {
				cols = append(cols, res.Columns[ri])
				p = append(p, src{true, ri})
				used[c.Name] = true
			} else if !inPlace[c.Name] {
				cols = append(cols, c)
				p = append(p, src{false, i})
			}
		}
		for ri, c := range res.Columns {
			if !used[c.Name] {
				cols = append(cols, c)
				p = append(p, src{true, ri})
			}
		}
		if outCols == nil {
			outCols, plan = cols, p
			return nil
		}
		if len(cols) != len(outCols) {
			return fmt.Errorf("mv-apply: subquery produced different schemas for different rows")
		}
		for i := range cols {
			if cols[i].Name != outCols[i].Name {
				return fmt.Errorf("mv-apply: subquery produced different schemas for different rows")
			}
		}
		plan = p
		return nil
	}

	var output *types.Table
	elems := make([][]types.Value, len(op.Columns))
	for _, row := range input.Rows {
		n := 0
		for i, col := range op.Columns {
			v, err := evalExpr(col.Source, &input.Schema, row)
			if err != nil {
				return nil, fmt.Errorf("mv-apply %q: %w", col.Name, err)
			}
			elems[i] = expandArrayOnly(v, dynamicSrc[i])
			if len(elems[i]) > n {
				n = len(elems[i])
			}
		}
		if op.Limit > 0 && int64(n) > op.Limit {
			n = int(op.Limit)
		}
		sub := types.NewTable(input.Name, subSchema)
		for k := 0; k < n; k++ {
			subRow := make(types.Row, len(subSchema.Columns))
			copy(subRow, row)
			for i, col := range op.Columns {
				var v types.Value
				if k < len(elems[i]) {
					v = castElement(elems[i][k], col.Type)
				}
				subRow[targets[i]] = v
			}
			if indexCol >= 0 {
				subRow[indexCol] = int64(k)
			}
			sub.AddRow(subRow)
		}
		result, err := runSub(sub)
		if err != nil {
			return nil, err
		}
		if err := planFor(result.Schema); err != nil {
			return nil, err
		}
		if output == nil {
			output = types.NewTable(input.Name, types.Schema{Columns: outCols})
		}
		for _, rr := range result.Rows {
			out := make(types.Row, len(plan))
			for i, p := range plan {
				if p.fromResult {
					out[i] = rr[p.idx]
				} else {
					out[i] = row[p.idx]
				}
			}
			output.AddRow(out)
		}
	}
	if output == nil {
		// No input rows: the subquery over an empty subtable gives the
		// output schema.
		result, err := runSub(types.NewTable(input.Name, subSchema))
		if err != nil {
			return nil, err
		}
		if err := planFor(result.Schema); err != nil {
			return nil, err
		}
		output = types.NewTable(input.Name, types.Schema{Columns: outCols})
	}
	return output, nil
}
