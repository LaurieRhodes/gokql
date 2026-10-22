package engine

// func_math_extra.go — range(), cot, erf, erfc, gamma, loggamma,
// replace (deprecated form of replace_regex) and assert.

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// maxRangeElements is range()'s documented result cap (2^20).
const maxRangeElements = 1 << 20

func evalMathExtraFunc(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "cot", "erf", "erfc", "gamma", "loggamma":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("%s requires 1 argument", fc.Name)
		}
		v, err := evalExpr(fc.Args[0], schema, row)
		if err != nil || v == nil {
			return nil, true, err
		}
		x := types.ToFloat64(v)
		switch fc.Name {
		case "cot":
			return 1 / math.Tan(x), true, nil
		case "erf":
			return math.Erf(x), true, nil
		case "erfc":
			return math.Erfc(x), true, nil
		case "gamma":
			return math.Gamma(x), true, nil
		default: // loggamma: log of the absolute value of gamma
			lg, _ := math.Lgamma(x)
			return lg, true, nil
		}

	case "replace":
		// Deprecated: replace(regex, rewrite, text) is
		// replace_regex(text, regex, rewrite).
		if len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("replace(regex, rewrite, text) requires 3 arguments")
		}
		v, err := evalFunc(&parser.FuncCall{Name: "replace_regex", Args: []parser.Expr{fc.Args[2], fc.Args[0], fc.Args[1]}}, schema, row)
		return v, true, err

	case "assert":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("assert(condition, message) requires 2 arguments")
		}
		return evalAssert(fc, schema, row)

	case "range":
		return evalRangeFunc(fc, schema, row)
	}
	return nil, false, nil
}

func evalAssert(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	c, err := evalExpr(fc.Args[0], schema, row)
	if err != nil {
		return nil, true, err
	}
	if b, ok := c.(bool); ok && b {
		return true, true, nil
	}
	m, err := evalExpr(fc.Args[1], schema, row)
	if err != nil {
		return nil, true, err
	}
	return nil, true, fmt.Errorf("assert() has failed with message: '%v'", m)
}

// evalRangeFunc implements range(start, stop [, step]): start,
// start+step, ... while within stop, for numbers, datetimes and
// timespans (default step 1, or 1h for datetime and timespan). A
// negative step counts down; a zero step gives null.
func evalRangeFunc(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if len(fc.Args) < 2 || len(fc.Args) > 3 {
		return nil, true, fmt.Errorf("range(start, stop [, step]) takes 2 or 3 arguments")
	}
	vals := make([]types.Value, len(fc.Args))
	for i, a := range fc.Args {
		v, err := evalExpr(a, schema, row)
		if err != nil {
			return nil, true, err
		}
		if v == nil {
			return nil, true, nil
		}
		vals[i] = v
	}
	kind := inferExprType(fc.Args[0], schema)
	var out []interface{}
	switch kind {
	case types.TypeDatetime, types.TypeTimespan:
		start, stop := types.ToInt64(vals[0]), types.ToInt64(vals[1])
		step := int64(36000000000) // 1h in ticks
		if len(vals) == 3 {
			step = types.ToInt64(vals[2])
		}
		if kind == types.TypeDatetime {
			step *= 100 // datetime values are nanoseconds
		}
		if step == 0 {
			return nil, true, nil
		}
		for v := start; (step > 0 && v <= stop) || (step < 0 && v >= stop); v += step {
			if len(out) == maxRangeElements {
				break
			}
			if kind == types.TypeDatetime {
				out = append(out, formatDatetimeISO7(v))
			} else {
				out = append(out, types.FormatValue(v, types.TypeTimespan))
			}
		}
	default:
		integral := true
		for i, v := range vals {
			if t := inferExprType(fc.Args[i], schema); t == types.TypeReal {
				integral = false
			}
			if _, ok := v.(float64); ok {
				integral = false
			}
		}
		if integral {
			start, stop := types.ToInt64(vals[0]), types.ToInt64(vals[1])
			step := int64(1)
			if len(vals) == 3 {
				step = types.ToInt64(vals[2])
			}
			if step == 0 {
				return nil, true, nil
			}
			for v := start; (step > 0 && v <= stop) || (step < 0 && v >= stop); v += step {
				if len(out) == maxRangeElements {
					break
				}
				out = append(out, v)
			}
		} else {
			start, stop := types.ToFloat64(vals[0]), types.ToFloat64(vals[1])
			step := 1.0
			if len(vals) == 3 {
				step = types.ToFloat64(vals[2])
			}
			if step == 0 {
				return nil, true, nil
			}
			for k := 0; ; k++ {
				v := start + float64(k)*step
				if (step > 0 && v > stop) || (step < 0 && v < stop) || len(out) == maxRangeElements {
					break
				}
				out = append(out, v)
			}
		}
	}
	if out == nil {
		out = []interface{}{}
	}
	b, _ := json.Marshal(out)
	return string(b), true, nil
}
