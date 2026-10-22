package types

import (
	"encoding/json"
	"strings"
	"testing"
)

func dec(t *testing.T, s string) Decimal {
	t.Helper()
	d, ok := ParseDecimal(s)
	if !ok {
		t.Fatalf("ParseDecimal(%q) failed", s)
	}
	return d
}

func TestParseFormatDecimal(t *testing.T) {
	for in, want := range map[string]string{
		"1.50": "1.50", "1e5": "100000", "-0.001": "-0.001", ".5": "0.5", "5.": "5",
		"+7": "7", "1.5E-3": "0.0015", "000123.4500": "123.4500", "0": "0", "-0": "0",
		"12345678901234567890.123456789": "12345678901234567890.123456789",
	} {
		if got := dec(t, in).String(); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
	for _, bad := range []string{"", "abc", "1.2.3", "1e", "--1", ".", "1e99999999999"} {
		if _, ok := ParseDecimal(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	// 39 integer digits is out of range; a long fraction is rounded.
	if _, ok := ParseDecimal(strings.Repeat("9", 39)); ok {
		t.Error("39-digit integer accepted")
	}
	if got := dec(t, "1."+strings.Repeat("3", 40)).String(); len(strings.ReplaceAll(got, ".", "")) != MaxDecimalDigits {
		t.Errorf("long fraction not limited to %d digits: %s", MaxDecimalDigits, got)
	}
}

func TestDecimalArithmetic(t *testing.T) {
	a, b := dec(t, "0.1"), dec(t, "0.2")
	sum, _ := a.Add(b)
	if sum.Cmp(dec(t, "0.3")) != 0 {
		t.Errorf("0.1 + 0.2 = %s", sum)
	}
	if d, _ := dec(t, "1.50").Sub(dec(t, "2")); d.String() != "-0.50" {
		t.Errorf("1.50 - 2 = %s", d)
	}
	if m, _ := dec(t, "1.5").Mul(dec(t, "-0.25")); m.String() != "-0.375" {
		t.Errorf("1.5 * -0.25 = %s", m)
	}
	if q, _ := dec(t, "1").Quo(dec(t, "3")); q.String() != "0."+strings.Repeat("3", DecimalDivisionScale) {
		t.Errorf("1/3 = %s", q)
	}
	if q, _ := dec(t, "2").Quo(dec(t, "3")); !strings.HasSuffix(q.String(), "67") {
		t.Errorf("2/3 not rounded half away from zero: %s", q)
	}
	if q, _ := dec(t, "10").Quo(dec(t, "4")); q.String() != "2.5" {
		t.Errorf("10/4 = %s", q)
	}
	if _, ok := dec(t, "1").Quo(dec(t, "0")); ok {
		t.Error("division by zero succeeded")
	}
	if r, _ := dec(t, "-7.5").Rem(dec(t, "2")); r.String() != "-1.5" {
		t.Errorf("-7.5 %% 2 = %s", r)
	}
	big := dec(t, strings.Repeat("9", 38))
	if _, ok := big.Add(dec(t, "1")); ok {
		t.Error("overflow past 38 digits succeeded")
	}
	if dec(t, "1.5").Cmp(dec(t, "1.50")) != 0 || dec(t, "-2").Cmp(dec(t, "1")) >= 0 {
		t.Error("Cmp")
	}
	if r := dec(t, "2.345").Round(2); r.String() != "2.35" {
		t.Errorf("round(2.345, 2) = %s", r)
	}
	if r := dec(t, "-2.5").Round(0); r.String() != "-3" {
		t.Errorf("round(-2.5) = %s", r)
	}
	if f, _ := dec(t, "-1.5").Floor(dec(t, "1")); f.Cmp(dec(t, "-2")) != 0 {
		t.Errorf("floor(-1.5, 1) = %s", f)
	}
}

func TestDecimalConversions(t *testing.T) {
	if d, ok := ToDecimal(0.1); !ok || d.String() != "0.1" {
		t.Errorf("ToDecimal(0.1) = %s", d)
	}
	if d, ok := ToDecimal(int64(-42)); !ok || d.String() != "-42" {
		t.Errorf("ToDecimal(-42) = %s", d)
	}
	if _, ok := ToDecimal("x"); ok {
		t.Error("ToDecimal(\"x\") succeeded")
	}
	if i, ok := dec(t, "-1.9").Int64(); !ok || i != -1 {
		t.Errorf("Int64(-1.9) = %d", i)
	}
	if f := dec(t, "1.25").Float64(); f != 1.25 {
		t.Errorf("Float64 = %v", f)
	}
	b, _ := json.Marshal(map[string]interface{}{"d": dec(t, "1234567890.0987654321")})
	if string(b) != `{"d":1234567890.0987654321}` {
		t.Errorf("JSON %s", b)
	}
	if CompareValues(dec(t, "2"), int64(10), TypeDecimal) >= 0 || CompareValues(dec(t, "2"), dec(t, "2.0"), TypeDecimal) != 0 {
		t.Error("CompareValues")
	}
	if v, err := ParseValue("3.50", TypeDecimal); err != nil || FormatValue(v, TypeDecimal) != "3.50" {
		t.Errorf("ParseValue/FormatValue: %v %v", v, err)
	}
}
