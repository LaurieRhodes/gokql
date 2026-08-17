package engine

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// evalFunc dispatches a function call to the appropriate category handler.
// Each handler returns (value, handled, error). If handled is false, the
// next handler is tried. This keeps the dispatch O(1) per category while
// allowing each file to own its own switch statement.
var windowFuncNames = map[string]bool{"row_number": true, "prev": true, "next": true}

func evalFunc(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, error) {
	// Try each category in turn. The order doesn't matter for correctness
	// (function names are unique) but we put the most common categories first.
	if v, ok, err := evalStringFunc(fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalDatetimeFunc(fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalConvertFunc(fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalNetFunc(fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalNetFuncExtended(fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalDynamicFunc(fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalVectorFunc(fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalSeriesFunc(fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalUserFunc(fc, schema, row); ok {
		return v, err
	}
	if windowFuncNames[fc.Name] {
		return nil, fmt.Errorf("%s is a window function and only works inside serialize "+
			"(e.g. 'serialize rank = %s()'), not extend/project/where", fc.Name, fc.Name)
	}
	return nil, fmt.Errorf("unsupported function: %s", fc.Name)
}

// udfDepth guards against runaway recursion in user-defined functions.
// KQL does not support recursive UDFs; a generous depth allows legitimate
// nested calls (f calling g calling h) while catching cycles.
// NOTE: package-level state assumes single-threaded query execution, like
// activeLetContext. Revisit both if parallel extent scanning lands.
var udfDepth int

const maxUDFDepth = 64

// errUDFDepth is returned (unwrapped through call frames) when the depth
// guard trips, so the user sees one clear message rather than a 64-deep
// chain of "function f:" prefixes.
var errUDFDepth = errors.New("UDF call depth exceeded (recursive user-defined functions are not supported)")

// evalUserFunc resolves a call against let-bound user-defined functions.
// Arguments are evaluated in the caller's scope, then the body is evaluated
// in an isolated scope where only the parameters are visible as columns.
// Scalar let bindings remain visible through activeLetContext, matching KQL.
func evalUserFunc(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	fn, ok := activeLetContext.LookupFunction(fc.Name)
	if !ok {
		return nil, false, nil
	}
	if len(fc.Args) != len(fn.Params) {
		return nil, true, fmt.Errorf("function %s: expected %d arguments, got %d",
			fc.Name, len(fn.Params), len(fc.Args))
	}
	if udfDepth >= maxUDFDepth {
		return nil, true, fmt.Errorf("function %s: %w", fc.Name, errUDFDepth)
	}

	// Evaluate arguments in the caller's scope
	paramSchema := types.Schema{Columns: make([]types.Column, len(fn.Params))}
	paramRow := make(types.Row, len(fn.Params))
	for i, param := range fn.Params {
		val, err := evalExpr(fc.Args[i], schema, row)
		if err != nil {
			return nil, true, fmt.Errorf("function %s argument %s: %w", fc.Name, param.Name, err)
		}
		paramSchema.Columns[i] = types.Column{Name: param.Name, Type: param.Type}
		paramRow[i] = val
	}

	// Evaluate the body in the isolated parameter scope
	udfDepth++
	val, err := evalExpr(fn.Body, &paramSchema, paramRow)
	udfDepth--
	if err != nil {
		if errors.Is(err, errUDFDepth) {
			return nil, true, err // don't stack a prefix per frame
		}
		return nil, true, fmt.Errorf("function %s: %w", fc.Name, err)
	}
	return val, true, nil
}

// evalAccess evaluates property/index access on a JSON value.
// Special case: graph-match binds pattern variables as literal dotted
// column names ("a.Kind"). When the base identifier is not itself a
// column, the longest dotted prefix that names a column resolves as a
// direct column reference and any remaining path descends into that
// column's value with JSON semantics ("e.Tags.sev" → column "e.Tags",
// then ".sev"). Real dynamic columns keep plain JSON-access semantics
// because the base-column check takes precedence.
func evalAccess(ae *parser.AccessExpr, schema *types.Schema, row types.Row) (types.Value, error) {
	if idx, rest := dottedColumnPrefix(ae, schema); idx >= 0 {
		if len(rest) == 0 {
			return row[idx], nil
		}
		return accessJSONPath(row[idx], rest), nil
	}

	val, err := evalExpr(ae.Object, schema, row)
	if err != nil {
		return nil, err
	}
	return accessJSONPath(val, ae.Path), nil
}

// accessJSONPath parses val as JSON and walks the access path through it.
// Any failure (nil value, non-JSON, missing key, out-of-range index,
// descent into a scalar) yields nil, matching KQL dynamic semantics.
func accessJSONPath(val types.Value, path []parser.AccessKey) types.Value {
	if val == nil {
		return nil
	}

	jsonStr := fmt.Sprintf("%v", val)

	var current interface{}
	if err := json.Unmarshal([]byte(jsonStr), &current); err != nil {
		return nil
	}

	for _, key := range path {
		if current == nil {
			return nil
		}
		switch obj := current.(type) {
		case map[string]interface{}:
			if key.Name != "" {
				current = obj[key.Name]
			} else {
				return nil
			}
		case []interface{}:
			if key.Name != "" {
				// Field access on an array maps over its elements
				// (ADX semantics for variable-length graph-match edge
				// variables: e.Rel over the edge list yields the array
				// of each edge's Rel). Missing fields yield null in
				// place, preserving positional alignment with hops.
				mapped := make([]interface{}, len(obj))
				for mi, el := range obj {
					if m, ok := el.(map[string]interface{}); ok {
						mapped[mi] = m[key.Name]
					}
				}
				current = mapped
				continue
			}
			if key.Index >= 0 && key.Index < len(obj) {
				current = obj[key.Index]
			} else {
				return nil
			}
		default:
			return nil
		}
	}

	return jsonToKQLValue(current)
}

// dottedColumnPrefix resolves an AccessExpr against literal dotted column
// names ("a.Kind"). When the base identifier is NOT itself a column, it
// returns the index of the longest dotted prefix that names a column and
// the unconsumed remainder of the access path ("e.Tags.sev" over a schema
// with column "e.Tags" → index of "e.Tags", rest [sev]). Numeric index
// keys end prefix growth (they cannot be part of a column name) but a
// prefix matched before them still resolves ("e.Tags[0]"). Returns
// (-1, nil) when the base is a column or no prefix matches.
func dottedColumnPrefix(ae *parser.AccessExpr, schema *types.Schema) (int, []parser.AccessKey) {
	ref, ok := ae.Object.(*parser.ColumnRef)
	if !ok || schema.ColumnIndex(ref.Name) >= 0 {
		return -1, nil
	}
	bestIdx := -1
	var bestRest []parser.AccessKey
	name := ref.Name
	for i, key := range ae.Path {
		if key.Name == "" {
			break // numeric index — cannot extend a dotted column name
		}
		name += "." + key.Name
		if idx := schema.ColumnIndex(name); idx >= 0 {
			bestIdx = idx
			bestRest = ae.Path[i+1:]
		}
	}
	return bestIdx, bestRest
}

// jsonToKQLValue converts a Go JSON value to a KQL-compatible value.
func jsonToKQLValue(v interface{}) types.Value {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return int64(val)
		}
		return val
	case bool:
		return val
	case map[string]interface{}, []interface{}:
		b, err := json.Marshal(val)
		if err != nil {
			return nil
		}
		return string(b)
	default:
		return fmt.Sprintf("%v", val)
	}
}
