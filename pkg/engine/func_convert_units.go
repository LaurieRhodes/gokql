package engine

// func_convert_units.go — convert_angle, convert_energy, convert_force,
// convert_length, convert_mass, convert_speed, convert_temperature,
// convert_volume: convert_X(value, from, to). Unit names are matched
// case-insensitively; an unknown unit gives null (reference: "Invalid
// units return null"). Factors are in func_convert_units_table.go.

import (
	"fmt"
	"math"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// unitOverrides replaces generated factors where ADX's documented
// output shows it uses a different value from current UnitsNet.
// AcreFoot: the reference gives convert_volume(1.2, 'CubicMeter',
// 'AcreFoot') = 0.0009728568, which is 1.2 * 0.000810714, the rounded
// from-base factor of older UnitsNet releases; current UnitsNet's exact
// 1233.48183754752 m3 gives 0.00097285583.
func init() {
	convertUnits["convert_volume"]["acrefoot"] = unitAffine{a: 1 / 0.000810714, b: 0}
}

func evalConvertUnitsFunc(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	table, ok := convertUnits[fc.Name]
	if !ok {
		return nil, false, nil
	}
	if len(fc.Args) != 3 {
		return nil, true, fmt.Errorf("%s(value, from, to) requires 3 arguments", fc.Name)
	}
	vals := make([]types.Value, 3)
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
	x := types.ToFloat64(vals[0])
	from := strings.ToLower(fmt.Sprintf("%v", vals[1]))
	to := strings.ToLower(fmt.Sprintf("%v", vals[2]))

	// Angle's Tilt unit is the sine of the angle, not a linear unit
	// (base unit: radian).
	toBase := func(u string, v float64) (float64, bool) {
		if fc.Name == "convert_angle" && u == "tilt" {
			return math.Asin(v), true
		}
		f, ok := table[u]
		return f.a*v + f.b, ok
	}
	fromBase := func(u string, base float64) (float64, bool) {
		if fc.Name == "convert_angle" && u == "tilt" {
			return math.Sin(base), true
		}
		f, ok := table[u]
		if !ok {
			return 0, false
		}
		return (base - f.b) / f.a, true
	}
	base, ok1 := toBase(from, x)
	out, ok2 := fromBase(to, base)
	if !ok1 || !ok2 {
		return nil, true, nil
	}
	return out, true, nil
}
