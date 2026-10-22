package engine

// func_binary.go — scalar bitwise functions: binary_and, binary_or,
// binary_xor, binary_not, binary_shift_left, binary_shift_right,
// bitset_count_ones. The aggregation forms (binary_all_*) live in
// aggregation.go.
//
// Semantics follow the ADX reference pages:
//   - operands are 64-bit integers; a null or non-integer operand
//     gives null;
//   - shifts are value << (shift % 64) and value >> (shift % 64),
//     and a negative shift count gives null;
//   - binary_shift_right is arithmetic (sign-propagating), as for a
//     signed 64-bit integer;
//   - bitset_count_ones counts set bits in the 64-bit two's-complement
//     representation, so bitset_count_ones(-1) is 64.

import (
	"fmt"
	"math/bits"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

var binaryFuncArity = map[string]int{
	"binary_and":         2,
	"binary_or":          2,
	"binary_xor":         2,
	"binary_shift_left":  2,
	"binary_shift_right": 2,
	"binary_not":         1,
	"bitset_count_ones":  1,
}

func evalBinaryFunc(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	arity, ok := binaryFuncArity[fc.Name]
	if !ok {
		return nil, false, nil
	}
	if len(fc.Args) != arity {
		return nil, true, fmt.Errorf("%s requires %d argument(s), got %d", fc.Name, arity, len(fc.Args))
	}
	ops := make([]int64, arity)
	for i, a := range fc.Args {
		v, err := evalExpr(eng, a, schema, row)
		if err != nil {
			return nil, true, err
		}
		n, ok := binaryOperand(v)
		if !ok {
			return nil, true, nil
		}
		ops[i] = n
	}
	switch fc.Name {
	case "binary_and":
		return ops[0] & ops[1], true, nil
	case "binary_or":
		return ops[0] | ops[1], true, nil
	case "binary_xor":
		return ops[0] ^ ops[1], true, nil
	case "binary_not":
		return ^ops[0], true, nil
	case "bitset_count_ones":
		return int64(bits.OnesCount64(uint64(ops[0]))), true, nil
	case "binary_shift_left", "binary_shift_right":
		if ops[1] < 0 {
			return nil, true, nil
		}
		s := uint(ops[1] % 64)
		if fc.Name == "binary_shift_left" {
			return ops[0] << s, true, nil
		}
		return ops[0] >> s, true, nil
	}
	return nil, true, fmt.Errorf("unreachable binary function %s", fc.Name)
}

// binaryOperand accepts integer values only. A real is accepted when it
// holds an exact integer, so binary_and(6.0, 3) behaves like (6, 3);
// a fractional real is not a valid bitwise operand and gives null.
func binaryOperand(v types.Value) (int64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false
	case int64:
		return x, true
	case int32:
		return int64(x), true
	case int:
		return int64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case float64:
		if x == float64(int64(x)) {
			return int64(x), true
		}
		return 0, false
	}
	return 0, false
}
