package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// evalFunc dispatches a function call to the appropriate category handler.
// Each handler returns (value, handled, error). If handled is false, the
// next handler is tried. This keeps the dispatch O(1) per category while
// allowing each file to own its own switch statement.

func evalFunc(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, error) {
	v, err := evalFuncRaw(eng, fc, schema, row)
	if err != nil || v == nil {
		return v, err
	}
	if t, ok := scalarReturnTypes[fc.Name]; ok {
		return coerceToDeclared(v, t), nil
	}
	return v, nil
}

// coerceToDeclared brings a numeric result into the representation of
// its declared type (int → int32, long → int64, real → float64), so a
// value and the static type inferExprType reports for it agree. Other
// types, and values that would not convert exactly, are unchanged.
func coerceToDeclared(v types.Value, t types.KQLType) types.Value {
	switch t {
	case types.TypeInt:
		switch x := v.(type) {
		case int64:
			if x >= math.MinInt32 && x <= math.MaxInt32 {
				return int32(x)
			}
		case float64:
			if x == math.Trunc(x) && x >= math.MinInt32 && x <= math.MaxInt32 {
				return int32(x)
			}
		}
	case types.TypeLong:
		switch x := v.(type) {
		case int32:
			return int64(x)
		case int:
			return int64(x)
		}
	case types.TypeReal:
		switch x := v.(type) {
		case int64:
			return float64(x)
		case int32:
			return float64(x)
		case int:
			return float64(x)
		}
	}
	return v
}

func evalFuncRaw(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, error) {
	// Try each category in turn. The order doesn't matter for correctness
	// (function names are unique) but we put the most common categories first.
	if v, ok, err := evalStringFunc(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalDatetimeFunc(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalConvertFunc(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalNetFunc(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalNetFuncExtended(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalDynamicFunc(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalConvertUnitsFunc(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalMathExtraFunc(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalColumnIfExists(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalPackAll(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalBinaryFunc(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalVectorFunc(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalSeriesFunc(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalSketchFunc(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalBetaFunc(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalPunycodeFunc(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalSeriesFitFunc(eng, fc, schema, row); ok {
		return v, err
	}
	if v, ok, err := evalUserFunc(eng, fc, schema, row); ok {
		return v, err
	}
	if windowFuncNames[fc.Name] {
		return nil, fmt.Errorf("%s is a window function: use it in extend, project, where or "+
			"serialize over a serialized row set", fc.Name)
	}
	return nil, fmt.Errorf("unsupported function: %s", fc.Name)
}

// udfDepth guards against runaway recursion in user-defined functions.
// KQL does not support recursive UDFs; a generous depth allows legitimate
// nested calls (f calling g calling h) while catching cycles.
// The depth is per engine (Engine.udfDepth).

const maxUDFDepth = 64

// errUDFDepth is returned (unwrapped through call frames) when the depth
// guard trips, so the user sees one clear message rather than a 64-deep
// chain of "function f:" prefixes.
var errUDFDepth = errors.New("UDF call depth exceeded (recursive user-defined functions are not supported)")

// evalUserFunc resolves a call against let-bound user-defined functions.
// Arguments are evaluated in the caller's scope, then the body is evaluated
// in an isolated scope where only the parameters are visible as columns.
// Scalar let bindings remain visible through the engine's let context, matching KQL.
func evalUserFunc(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	fn, ok := eng.lets().LookupFunction(fc.RawName)
	if !ok {
		fn, ok = eng.lets().LookupFunction(fc.Name)
	}
	if !ok {
		return nil, false, nil
	}
	if fn.Tabular {
		return nil, true, fmt.Errorf("function %s returns a table and cannot be used in a scalar expression", fc.Name)
	}
	required := 0
	for _, p := range fn.Params {
		if !p.HasDefault {
			required++
		}
	}
	if len(fc.Args) < required || len(fc.Args) > len(fn.Params) {
		return nil, true, fmt.Errorf("function %s: expected %d to %d arguments, got %d",
			fc.Name, required, len(fn.Params), len(fc.Args))
	}
	// Map arguments to parameters: positional ones in order, named
	// ones by name; unfilled parameters take their defaults.
	argFor := make([]parser.Expr, len(fn.Params))
	next := 0
	for i, a := range fc.Args {
		if fc.ArgNames != nil && fc.ArgNames[i] != "" {
			k := -1
			for j, p := range fn.Params {
				if p.Name == fc.ArgNames[i] {
					k = j
				}
			}
			if k < 0 {
				return nil, true, fmt.Errorf("function %s has no parameter %q", fc.Name, fc.ArgNames[i])
			}
			argFor[k] = a
			continue
		}
		for next < len(argFor) && argFor[next] != nil {
			next++
		}
		if next >= len(argFor) {
			return nil, true, fmt.Errorf("function %s: too many arguments", fc.Name)
		}
		argFor[next] = a
		next++
	}
	if eng.udfDepth >= maxUDFDepth {
		return nil, true, fmt.Errorf("function %s: %w", fc.Name, errUDFDepth)
	}

	// Evaluate arguments in the caller's scope
	paramSchema := types.Schema{Columns: make([]types.Column, len(fn.Params))}
	paramRow := make(types.Row, len(fn.Params))
	for i, param := range fn.Params {
		var val types.Value
		var err error
		if argFor[i] != nil {
			val, err = evalExpr(eng, argFor[i], schema, row)
		} else if !param.HasDefault {
			return nil, true, fmt.Errorf("function %s: missing argument %s", fc.Name, param.Name)
		} else {
			// Omitted trailing argument: the declared default,
			// evaluated without the caller's row.
			val, err = evalExpr(eng, param.Default, &types.Schema{}, types.Row{})
		}
		if err != nil {
			return nil, true, fmt.Errorf("function %s argument %s: %w", fc.Name, param.Name, err)
		}
		paramSchema.Columns[i] = types.Column{Name: param.Name, Type: param.Type}
		paramRow[i] = val
	}

	// Evaluate the body in the isolated parameter scope
	eng.udfDepth++
	val, err := evalExpr(eng, fn.Body, &paramSchema, paramRow)
	eng.udfDepth--
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
func evalAccess(eng *Engine, ae *parser.AccessExpr, schema *types.Schema, row types.Row) (types.Value, error) {
	for _, k := range ae.Path {
		if k.Expr != nil {
			path, err := resolveAccessPath(eng, ae.Path, schema, row)
			if err != nil {
				return nil, err
			}
			ae = &parser.AccessExpr{Object: ae.Object, Path: path}
			break
		}
	}
	if idx, rest := dottedColumnPrefix(ae, schema); idx >= 0 {
		if len(rest) == 0 {
			return row[idx], nil
		}
		return accessJSONPath(row[idx], rest), nil
	}

	val, err := evalExpr(eng, ae.Object, schema, row)
	if err != nil {
		return nil, err
	}
	return accessJSONPath(val, ae.Path), nil
}

// resolveAccessPath evaluates computed keys for this row: an integer
// becomes an index (FromEnd: negative counts from the end), anything
// else a property name. A null key makes the access null.
func resolveAccessPath(eng *Engine, path []parser.AccessKey, schema *types.Schema, row types.Row) ([]parser.AccessKey, error) {
	out := make([]parser.AccessKey, len(path))
	for i, k := range path {
		if k.Expr == nil {
			out[i] = k
			continue
		}
		v, err := evalExpr(eng, k.Expr, schema, row)
		if err != nil {
			return nil, err
		}
		switch x := v.(type) {
		case nil:
			out[i] = parser.AccessKey{Index: math.MinInt32, FromEnd: true}
		case int64:
			out[i] = parser.AccessKey{Index: int(x), FromEnd: true}
		case int32:
			out[i] = parser.AccessKey{Index: int(x), FromEnd: true}
		default:
			out[i] = parser.AccessKey{Name: fmt.Sprintf("%v", x), Index: -1}
		}
	}
	return out, nil
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
			ix := key.Index
			if key.FromEnd && ix < 0 {
				ix += len(obj)
			}
			if ix >= 0 && ix < len(obj) {
				current = obj[ix]
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
