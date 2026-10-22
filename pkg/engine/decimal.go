package engine

// decimal.go — engine support for the decimal type (types.Decimal):
// parse_version(), and the return types of the decimal functions,
// which the generated func_signatures.go excludes (it predates
// TypeDecimal).

import (
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	scalarReturnTypes["todecimal"] = types.TypeDecimal
	scalarReturnTypes["parse_version"] = types.TypeDecimal
}

// parseVersion implements parse_version(): one to four dot-separated
// parts of up to eight digits each, missing parts trailing (1.0 is
// 1.0.0.0), combined as p1×10^24 + p2×10^16 + p3×10^8 + p4 so versions
// compare as numbers. Anything else is not a version.
func parseVersion(s string) (types.Decimal, bool) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) < 1 || len(parts) > 4 {
		return types.Decimal{}, false
	}
	var digits strings.Builder
	for i := 0; i < 4; i++ {
		p := "0"
		if i < len(parts) {
			p = parts[i]
			if p == "" || len(p) > 8 {
				return types.Decimal{}, false
			}
			for _, r := range p {
				if r < '0' || r > '9' {
					return types.Decimal{}, false
				}
			}
		}
		digits.WriteString(strings.Repeat("0", 8-len(p)) + p)
	}
	return types.ParseDecimal(digits.String())
}

// decimalSumAvg is sum/avg (and their -if forms, cond non-nil) over a
// decimal expression: exact, decimal-typed, as Kusto.Language types
// them. Null when out of range or, for avg, with no values.
func decimalSumAvg(eng *Engine, rows []types.Row, arg, cond parser.Expr, schema *types.Schema, avg bool) (types.Value, error) {
	total := types.DecimalFromInt64(0)
	n := int64(0)
	for _, row := range rows {
		if cond != nil {
			p, err := evalExpr(eng, cond, schema, row)
			if b, ok := p.(bool); err != nil || !ok || !b {
				continue
			}
		}
		v, err := evalExpr(eng, arg, schema, row)
		if err != nil || v == nil {
			continue
		}
		d, ok := types.ToDecimal(v)
		if !ok {
			continue
		}
		if total, ok = total.Add(d); !ok {
			return nil, nil
		}
		n++
	}
	if !avg {
		return total, nil
	}
	if n == 0 {
		return nil, nil
	}
	q, ok := total.Quo(types.DecimalFromInt64(n))
	if !ok {
		return nil, nil
	}
	return q, nil
}

// evalDecimalMath evaluates abs, round, ceiling, floor and bin over a
// decimal first argument exactly, keeping the decimal type (as
// Kusto.Language types them). Other arguments take the real paths.
func evalDecimalMath(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "abs", "round", "ceiling", "floor", "bin":
	default:
		return nil, false, nil
	}
	if len(fc.Args) == 0 || inferExprType(eng, fc.Args[0], schema) != types.TypeDecimal {
		return nil, false, nil
	}
	v, err := evalExpr(eng, fc.Args[0], schema, row)
	if err != nil {
		return nil, true, err
	}
	x, ok := v.(types.Decimal)
	if !ok {
		return nil, false, nil // null or not actually decimal: real path
	}
	arg := func(i int) (types.Value, error) { return evalExpr(eng, fc.Args[i], schema, row) }
	switch fc.Name {
	case "abs":
		return x.Abs(), true, nil
	case "round":
		n := int64(0)
		if len(fc.Args) == 2 {
			pv, err := arg(1)
			if err != nil || pv == nil {
				return nil, true, err
			}
			n = types.ToInt64(pv)
		}
		if n < 0 {
			return nil, false, nil // negative precision: real path
		}
		return x.Round(int32(n)), true, nil
	case "ceiling":
		r, ok := x.Neg().Floor(types.DecimalFromInt64(1))
		if !ok {
			return nil, true, nil
		}
		return r.Neg(), true, nil
	default: // floor, bin
		if len(fc.Args) != 2 {
			return nil, false, nil
		}
		sv, err := arg(1)
		if err != nil || sv == nil {
			return nil, true, err
		}
		step, ok := types.ToDecimal(sv)
		if !ok {
			return nil, true, nil
		}
		r, ok := x.Floor(step)
		if !ok {
			return nil, true, nil // non-positive step gives null, as for reals
		}
		return r, true, nil
	}
}
