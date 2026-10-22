package engine

// func_bin.go — bin (alias floor) and bin_at.
//
// bin(value, size) is "the nearest multiple of size below value"; a
// null argument or a size that is not positive gives null (bin
// reference). bin_at(value, size, fixed) aligns the bins to fixed
// instead of zero: fixed + floor((value - fixed) / size) * size.
// Rounding is a floor, so negative values and pre-1970 datetimes go
// down, not toward zero.
//
// Types follow Kusto.Language: datetime and timespan values keep their
// type (size must then be a timespan); numeric results take the widest
// argument type, int promoted to long.

import (
	"fmt"
	"math"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func evalBin(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	want := 2
	if fc.Name == "bin_at" {
		want = 3
	}
	if len(fc.Args) != want {
		return nil, true, fmt.Errorf("%s requires %d arguments", fc.Name, want)
	}
	vals := make([]types.Value, want)
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
	valType := inferExprType(eng, fc.Args[0], schema)
	sizeType := inferExprType(eng, fc.Args[1], schema)

	switch valType {
	case types.TypeDatetime, types.TypeTimespan:
		// datetime values are nanoseconds; timespans are 100 ns ticks.
		size := types.ToInt64(vals[1])
		if sizeType != types.TypeTimespan {
			return nil, true, fmt.Errorf("%s: bin size for a %s value must be a timespan", fc.Name, valType)
		}
		if valType == types.TypeDatetime {
			size *= 100
		}
		if size <= 0 {
			return nil, true, nil
		}
		var fixed int64
		if want == 3 {
			fixed = types.ToInt64(vals[2])
		}
		return fixed + floorDivInt64(types.ToInt64(vals[0])-fixed, size)*size, true, nil
	}

	if binResultType(eng, fc.Args, schema) == types.TypeLong {
		size := types.ToInt64(vals[1])
		if size <= 0 {
			return nil, true, nil
		}
		var fixed int64
		if want == 3 {
			fixed = types.ToInt64(vals[2])
		}
		return fixed + floorDivInt64(types.ToInt64(vals[0])-fixed, size)*size, true, nil
	}
	size := types.ToFloat64(vals[1])
	if !(size > 0) {
		return nil, true, nil
	}
	var fixed float64
	if want == 3 {
		fixed = types.ToFloat64(vals[2])
	}
	return fixed + math.Floor((types.ToFloat64(vals[0])-fixed)/size)*size, true, nil
}

// binResultType is the static type of bin/floor/bin_at.
func binResultType(eng *Engine, args []parser.Expr, schema *types.Schema) types.KQLType {
	if len(args) == 0 {
		return types.TypeLong
	}
	switch t := inferExprType(eng, args[0], schema); t {
	case types.TypeDatetime, types.TypeTimespan, types.TypeDecimal:
		return t
	}
	for _, a := range args {
		if inferExprType(eng, a, schema) == types.TypeReal {
			return types.TypeReal
		}
	}
	return types.TypeLong
}

// floorDivInt64 is integer division rounding toward negative infinity.
func floorDivInt64(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}
