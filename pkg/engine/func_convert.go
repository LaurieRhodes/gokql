package engine

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// evalConvertFunc handles type conversion, null-test, and conditional functions.
func evalConvertFunc(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "toint", "tolong", "int", "long":
		// Also serves as this engine's bare int()/long() literal-cast
		// form — real KQL treats a scalar type name as a conversion
		// function identical to its "to"-prefixed counterpart (e.g.
		// long(x) === tolong(x)), the same relationship already
		// established for datetime()/todatetime() elsewhere in this
		// parser. Null-propagation added 2026-08-15 alongside this —
		// a real, separate, pre-existing bug found while wiring up
		// make-series+series_fill_* integration testing:
		// toint(null)/tolong(null) silently returned 0 instead of
		// null, the one inconsistency in this whole function (every
		// sibling conversion in this file, e.g. tobool immediately
		// below, already null-checks correctly) — confirmed live,
		// not assumed, before fixing.
		if fc.Name == "int" || fc.Name == "long" {
			if userFuncOverridesBuiltinName(fc.Name) {
				return nil, false, nil
			}
		}
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
		return types.ToInt64(val), true, nil

	case "todouble", "toreal", "real", "double":
		// Bare real()/double() alias, same reasoning as int/long
		// above — real KQL treats "real" and "double" as synonymous
		// type names, both valid as this same conversion function.
		// Null-propagation fixed 2026-08-15: found live while testing
		// make-series's own documented default=double(null) idiom
		// (see series-fill-*'s own doc comments, func_series.go) --
		// double(null) silently returning 0.0 instead of null breaks
		// that idiom's entire point, since it exists specifically to
		// let a fill function distinguish "genuinely no data" from
		// "a real value of zero."
		if fc.Name == "real" || fc.Name == "double" {
			if userFuncOverridesBuiltinName(fc.Name) {
				return nil, false, nil
			}
		}
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

	case "tobool", "toboolean", "bool":
		if fc.Name == "bool" {
			if userFuncOverridesBuiltinName(fc.Name) {
				return nil, false, nil
			}
		}
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

	// --- Trigonometry ---
	// Verified 2026-08-17 against real ADX docs: sin/cos/tan/asin/
	// acos/atan are all plain radian-in radian-out functions matching
	// Go's math package directly (no unit-conversion quirks). atan2
	// takes (y, x) in that argument order — confirmed against real
	// ADX's own worked example (atan2(1,1) = Pi/4, atan2(0,-1) = Pi,
	// atan2(-1,0) = -Pi/2), same order as Go's math.Atan2(y, x), so no
	// argument-swap needed. degrees()/radians() use the documented
	// degrees = (180/Pi)*radians and radians = (Pi/180)*degrees
	// formulas — verified against real ADX's own worked examples:
	// degrees(pi()/4)=45, degrees(pi()*1.5)=270, degrees(0)=0;
	// radians(90)=1.5707963267949, radians(180)=3.14159265358979,
	// radians(360)=6.28318530717959. Each dispatches to the same
	// trigXxx helper that series_sin/series_cos/etc (func_series.go)
	// now call too, so the scalar and element-wise series forms can
	// never drift apart.
	case "sin", "cos", "tan", "asin", "acos", "atan":
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
		f := types.ToFloat64(val)
		switch fc.Name {
		case "sin":
			return trigSin(f), true, nil
		case "cos":
			return trigCos(f), true, nil
		case "tan":
			return trigTan(f), true, nil
		case "asin":
			return trigAsin(f), true, nil
		case "acos":
			return trigAcos(f), true, nil
		case "atan":
			return trigAtan(f), true, nil
		default:
			return nil, true, fmt.Errorf("unreachable trig case %s", fc.Name)
		}

	case "atan2":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("atan2 requires 2 arguments")
		}
		yVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		xVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if yVal == nil || xVal == nil {
			return nil, true, nil
		}
		return trigAtan2(types.ToFloat64(yVal), types.ToFloat64(xVal)), true, nil

	case "degrees":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("degrees requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return (180 / math.Pi) * types.ToFloat64(val), true, nil

	case "radians":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("radians requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return (math.Pi / 180) * types.ToFloat64(val), true, nil

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

// userFuncOverridesBuiltinName reports whether a `let`-bound scalar
// function named name exists in the current LetContext — checked
// specifically for this file's bare int()/long()/real()/double()/
// bool() aliases (added 2026-08-15) before treating one of those
// names as this engine's own built-in conversion function, so that a
// legitimate, pre-existing user-defined function of the same name
// (e.g. `let double = (x: long) { x * 2 }`, exactly what
// TestUDFBasic already exercised before these aliases existed) keeps
// working exactly as it did before — a real regression this check
// caught and fixed immediately via that existing test, not a
// hypothetical worth guarding against. Deliberately narrow: only the
// newly-added bare names call this, not every conversion function in
// this file, since broadening it to the whole dispatch chain (letting
// ANY user function shadow ANY built-in) is a bigger, separate change
// with its own test implications not audited here.
func userFuncOverridesBuiltinName(name string) bool {
	_, ok := activeLetContext.LookupFunction(name)
	return ok
}

// trigSin/trigCos/trigTan/trigAsin/trigAcos/trigAtan/trigAtan2 are the
// single shared implementations behind both this file's scalar
// sin()/cos()/tan()/asin()/acos()/atan()/atan2() functions and
// func_series.go's element-wise series_sin()/series_cos()/etc — added
// 2026-08-17 so the two families can't drift apart (previously
// series_* called math.Sin/math.Cos/etc directly, with no scalar
// counterpart to share the implementation with at all).
func trigSin(x float64) float64      { return math.Sin(x) }
func trigCos(x float64) float64      { return math.Cos(x) }
func trigTan(x float64) float64      { return math.Tan(x) }
func trigAsin(x float64) float64     { return math.Asin(x) }
func trigAcos(x float64) float64     { return math.Acos(x) }
func trigAtan(x float64) float64     { return math.Atan(x) }
func trigAtan2(y, x float64) float64 { return math.Atan2(y, x) }
