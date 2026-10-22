package engine

import (
	"math"
	"strings"
	"testing"
)

// math_extra_test.go — range(), cot/erf/erfc/gamma/loggamma, replace,
// assert, named arguments and case-sensitive user function names.

func TestRangeFunctionReference(t *testing.T) {
	r := queryResult(t, `print a = range(1, 8, 3), b = range(1, 3), c = range(1h, 5h), d = range(5, 1, -2)`)
	assertRows(t, r.Rows, [][]interface{}{{"[1,4,7]", "[1,2,3]",
		`["01:00:00","02:00:00","03:00:00","04:00:00","05:00:00"]`, "[5,3,1]"}})
	r = queryResult(t, `print r = range(datetime(2007-01-01), datetime(2007-12-31), 1d)
		| project n = array_length(r), first = tostring(r[0]), last = tostring(r[364])`)
	assertRows(t, r.Rows, [][]interface{}{{int64(365), "2007-01-01T00:00:00.0000000Z", "2007-12-31T00:00:00.0000000Z"}})
	r = queryResult(t, `print r = range(1,1000000000) | mv-expand r | count`)
	if got := r.Rows[0][0]; got != int64(1048576) {
		t.Errorf("capped count = %v, want 1048576", got)
	}
}

func TestSpecialMathFunctions(t *testing.T) {
	r := queryResult(t, `print a = gamma(5), b = cot(1), c = erf(0.5), d = erfc(0.5), e = loggamma(5)`)
	want := []float64{24, 1 / math.Tan(1), math.Erf(0.5), math.Erfc(0.5), math.Log(24)}
	for i, w := range want {
		if g, _ := r.Rows[0][i].(float64); math.Abs(g-w) > 1e-12 {
			t.Errorf("col %d = %v, want %v", i, r.Rows[0][i], w)
		}
	}
	r = queryResult(t, `print x = replace(@"b", "X", "abc")`)
	assertRows(t, r.Rows, [][]interface{}{{"aXc"}})
}

const checkLength = `let checkLength = (len:long, s:string) {
	assert(len > 0, "Length must be greater than zero") and strlen(s) > len };
	datatable(input:string)['123', '4567']`

func TestAssertReference(t *testing.T) {
	_, err := queryErr(checkLength + ` | where checkLength(len=long(-1), input)`)
	if err == nil || !strings.Contains(err.Error(), "assert() has failed with message: 'Length must be greater than zero'") {
		t.Errorf("err = %v", err)
	}
	r := queryResult(t, checkLength+` | where checkLength(len=3, input)`)
	assertRows(t, r.Rows, [][]interface{}{{"4567"}})
	// Evaluated at analysis: fails even though no row reaches it.
	if _, err := queryErr(`let b=false; print x="Hello" | where b | where assert(b, "Assertion failed")`); err == nil {
		t.Error("expected the assertion to fail with no rows")
	}
}

func TestNamedArgumentsAndFunctionNameCase(t *testing.T) {
	r := queryResult(t, `let MySub = (a:long, b:long) { a - b }; print x = MySub(b=1, a=5), y = MySub(5, 1)`)
	assertRows(t, r.Rows, [][]interface{}{{int64(4), int64(4)}})
}
