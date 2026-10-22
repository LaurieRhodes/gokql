package engine

import "testing"

// func_binary_test.go — scalar bitwise functions. The first eight
// cases are the worked examples on the ADX reference pages; the rest
// cover the documented edge cases (negative shift gives null, shift
// count taken modulo 64) and null propagation.
func TestBinaryScalarFunctions(t *testing.T) {
	cases := []struct {
		expr string
		want interface{} // int64, or nil for null
	}{
		{"binary_and(6, 3)", int64(2)},
		{"binary_or(5, 3)", int64(7)},
		{"binary_xor(1, 1)", int64(0)},
		{"binary_xor(1, 2)", int64(3)},
		{"binary_not(100)", int64(-101)},
		{"binary_shift_left(1, 2)", int64(4)},
		{"binary_shift_right(1, 2)", int64(0)},
		{"bitset_count_ones(42)", int64(3)},
		{"binary_shift_left(1, 66)", int64(4)},
		{"binary_shift_right(-8, 1)", int64(-4)},
		{"binary_shift_left(1, -1)", nil},
		{"binary_shift_right(8, -3)", nil},
		{"bitset_count_ones(-1)", int64(64)},
		{"binary_and(long(null), 3)", nil},
		{"binary_and(6.5, 3)", nil},
	}
	for _, c := range cases {
		r := queryResult(t, "print r = "+c.expr)
		got := r.Rows[0][0]
		if got != c.want {
			t.Errorf("%s = %v (%T), want %v (%T)", c.expr, got, got, c.want, c.want)
		}
	}
}

func TestBinaryScalarFunctionTypes(t *testing.T) {
	for _, f := range []string{
		"binary_and(6, 3)", "binary_or(5, 3)", "binary_xor(1, 2)",
		"binary_not(100)", "binary_shift_left(1, 2)",
		"binary_shift_right(1, 2)", "bitset_count_ones(42)",
	} {
		r := queryResult(t, "print r = gettype("+f+")")
		if got := r.Rows[0][0]; got != "long" {
			t.Errorf("gettype(%s) = %v, want long", f, got)
		}
	}
}

// TestBinaryFlagMask is the case these functions exist for in
// practice: testing a flag in a bit field such as UserAccountControl
// (0x0002 = ACCOUNTDISABLE).
func TestBinaryFlagMask(t *testing.T) {
	r := queryResult(t, `datatable(uac:long)[512, 514, 66050]
		| where binary_and(uac, 2) != 0 | count`)
	if got := r.Rows[0][0]; got != int64(2) {
		t.Errorf("disabled accounts = %v, want 2", got)
	}
}
