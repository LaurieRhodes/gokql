package engine

import (
	"fmt"
	"math"
	"strings"
	"strconv"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// evalConvertFunc handles type conversion, null-test, and conditional functions.
func evalConvertFunc(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "toint", "tolong":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("%s requires 1 argument", fc.Name)
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		return types.ToInt64(val), true, nil

	case "todouble", "toreal":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("%s requires 1 argument", fc.Name)
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		return types.ToFloat64(val), true, nil

	case "isnull":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("isnull requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		return val == nil, true, nil

	case "isnotnull":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("isnotnull requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		return val != nil, true, nil

	case "parse_json", "todynamic":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("%s requires 1 argument", fc.Name)
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return fmt.Sprintf("%v", val), true, nil

	case "tostring":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("tostring requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return "", true, nil
		}
		switch v := val.(type) {
		case float64:
			if v == float64(int64(v)) {
				return strconv.FormatInt(int64(v), 10), true, nil
			}
			return strconv.FormatFloat(v, 'f', -1, 64), true, nil
		case int64:
			return strconv.FormatInt(v, 10), true, nil
		case bool:
			if v {
				return "true", true, nil
			}
			return "false", true, nil
		default:
			return fmt.Sprintf("%v", val), true, nil
		}

	// --- Conditional Functions ---

	case "iff", "iif":
		if len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("iff requires 3 arguments")
		}
		predVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		pred, _ := predVal.(bool)
		if pred {
			v, err := evalExpr(fc.Args[1], schema, row)
			return v, true, err
		}
		v, err := evalExpr(fc.Args[2], schema, row)
		return v, true, err

	case "case":
		if len(fc.Args) < 3 || len(fc.Args)%2 == 0 {
			return nil, true, fmt.Errorf("case requires odd number of arguments (pred, val pairs + default)")
		}
		for i := 0; i < len(fc.Args)-1; i += 2 {
			predVal, err := evalExpr(fc.Args[i], schema, row)
			if err != nil {
				return nil, true, err
			}
			if pred, ok := predVal.(bool); ok && pred {
				v, err := evalExpr(fc.Args[i+1], schema, row)
				return v, true, err
			}
		}
		v, err := evalExpr(fc.Args[len(fc.Args)-1], schema, row)
		return v, true, err

	case "coalesce":
		for _, arg := range fc.Args {
			val, err := evalExpr(arg, schema, row)
			if err != nil {
				return nil, true, err
			}
			if val != nil {
				s, ok := val.(string)
				if ok && s == "" {
					continue
				}
				return val, true, nil
			}
		}
		return nil, true, nil

	case "max_of":
		if len(fc.Args) < 2 {
			return nil, true, fmt.Errorf("max_of requires at least 2 arguments")
		}
		var best types.Value
		for _, arg := range fc.Args {
			val, err := evalExpr(arg, schema, row)
			if err != nil {
				return nil, true, err
			}
			if val == nil {
				continue
			}
			if best == nil {
				best = val
				continue
			}
			switch bv := best.(type) {
			case int64:
				if cv, ok := val.(int64); ok && cv > bv {
					best = val
				}
			case float64:
				if cv := types.ToFloat64(val); cv > bv {
					best = val
				}
			case string:
				if cv, ok := val.(string); ok && cv > bv {
					best = val
				}
			}
		}
		return best, true, nil

	case "min_of":
		if len(fc.Args) < 2 {
			return nil, true, fmt.Errorf("min_of requires at least 2 arguments")
		}
		var best types.Value
		for _, arg := range fc.Args {
			val, err := evalExpr(arg, schema, row)
			if err != nil {
				return nil, true, err
			}
			if val == nil {
				continue
			}
			if best == nil {
				best = val
				continue
			}
			switch bv := best.(type) {
			case int64:
				if cv, ok := val.(int64); ok && cv < bv {
					best = val
				}
			case float64:
				if cv := types.ToFloat64(val); cv < bv {
					best = val
				}
			case string:
				if cv, ok := val.(string); ok && cv < bv {
					best = val
				}
			}
		}
		return best, true, nil

	case "tobool", "toboolean":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("%s requires 1 argument", fc.Name)
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		switch v := val.(type) {
		case bool:
			return v, true, nil
		case int64:
			return v != 0, true, nil
		case float64:
			return v != 0, true, nil
		case string:
			switch strings.ToLower(v) {
			case "true", "1":
				return true, true, nil
			case "false", "0", "":
				return false, true, nil
			default:
				return nil, true, nil
			}
		}
		return nil, true, nil

	// --- Math Functions ---

	case "round":
		if len(fc.Args) < 1 || len(fc.Args) > 2 {
			return nil, true, fmt.Errorf("round requires 1 or 2 arguments")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		f := types.ToFloat64(val)
		precision := 0
		if len(fc.Args) == 2 {
			pv, err := evalExpr(fc.Args[1], schema, row)
			if err != nil {
				return nil, true, err
			}
			precision = int(types.ToInt64(pv))
		}
		mult := math.Pow(10, float64(precision))
		return math.Round(f*mult) / mult, true, nil

	case "abs":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("abs requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		switch v := val.(type) {
		case int64:
			if v < 0 {
				return -v, true, nil
			}
			return v, true, nil
		default:
			return math.Abs(types.ToFloat64(val)), true, nil
		}

	case "pow":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("pow requires 2 arguments")
		}
		baseVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		expVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if baseVal == nil || expVal == nil {
			return nil, true, nil
		}
		return math.Pow(types.ToFloat64(baseVal), types.ToFloat64(expVal)), true, nil

	case "sqrt":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("sqrt requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return math.Sqrt(types.ToFloat64(val)), true, nil

	case "log":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("log requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return math.Log(types.ToFloat64(val)), true, nil

	case "log2":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("log2 requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return math.Log2(types.ToFloat64(val)), true, nil

	case "log10":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("log10 requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return math.Log10(types.ToFloat64(val)), true, nil

	case "ceiling":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("ceiling requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return math.Ceil(types.ToFloat64(val)), true, nil

	case "exp":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("exp requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return math.Exp(types.ToFloat64(val)), true, nil

	case "exp2":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("exp2 requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return math.Exp2(types.ToFloat64(val)), true, nil

	case "exp10":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("exp10 requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return math.Pow(10, types.ToFloat64(val)), true, nil

	case "pi":
		return math.Pi, true, nil

	case "sign":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("sign requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		f := types.ToFloat64(val)
		if f > 0 {
			return int64(1), true, nil
		} else if f < 0 {
			return int64(-1), true, nil
		}
		return int64(0), true, nil

	case "isnan":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("isnan requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return math.IsNaN(types.ToFloat64(val)), true, nil

	case "isinf":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("isinf requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return math.IsInf(types.ToFloat64(val), 0), true, nil

	case "isfinite":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("isfinite requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		f := types.ToFloat64(val)
		return !math.IsNaN(f) && !math.IsInf(f, 0), true, nil

	default:
		return nil, false, nil
	}
}
