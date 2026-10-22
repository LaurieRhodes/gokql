package types

// decimal.go — the KQL decimal type: a 128-bit decimal number.
//
// A Decimal is an exact base-10 value, coefficient × 10^-scale, kept at
// most MaxDecimalDigits significant digits (SQL decimal precision, the
// largest that fits 128 bits). Addition, subtraction, multiplication
// and comparison are exact; a result needing more digits than that is
// out of range and evaluates to null. Division rounds to
// DecimalDivisionScale fractional digits (half away from zero): the
// reference does not document decimal division precision.
//
// Scale is preserved as written (decimal(1.50) prints 1.50), and
// equality is numeric (decimal(1.5) == decimal(1.50)).

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// MaxDecimalDigits is the precision limit of a decimal value.
const MaxDecimalDigits = 38

// DecimalDivisionScale is the number of fractional digits a decimal
// quotient keeps.
const DecimalDivisionScale = 28

// Decimal is an immutable decimal value. The zero value is 0.
type Decimal struct {
	coef  *big.Int // nil means 0
	scale int32    // digits after the decimal point, >= 0
}

var bigTen = big.NewInt(10)

func (d Decimal) c() *big.Int {
	if d.coef == nil {
		return new(big.Int)
	}
	return d.coef
}

// DecimalFromInt64 returns v as a decimal.
func DecimalFromInt64(v int64) Decimal { return Decimal{coef: big.NewInt(v)} }

// DecimalFromFloat returns v as a decimal via its shortest decimal
// representation (so 0.1 is 0.1, not its binary expansion). NaN and
// infinities have no decimal value.
func DecimalFromFloat(v float64) (Decimal, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return Decimal{}, false
	}
	return ParseDecimal(strconv.FormatFloat(v, 'g', -1, 64))
}

// ParseDecimal parses a decimal literal: optional sign, digits with an
// optional fractional part, optional exponent (1e5, 1.5E-3).
func ParseDecimal(s string) (Decimal, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Decimal{}, false
	}
	exp := int64(0)
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.ParseInt(s[i+1:], 10, 32)
		if err != nil {
			return Decimal{}, false
		}
		exp = e
		s = s[:i]
	}
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg = true
		s = s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	intPart, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i+1:]
	}
	if intPart == "" && frac == "" {
		return Decimal{}, false
	}
	digits := intPart + frac
	for _, r := range digits {
		if r < '0' || r > '9' {
			return Decimal{}, false
		}
	}
	trimmed := strings.TrimLeft(digits, "0")
	if trimmed == "" {
		trimmed = "0"
	}
	coef, ok := new(big.Int).SetString(trimmed, 10)
	if !ok {
		return Decimal{}, false
	}
	scale := int64(len(frac)) - exp
	if scale < 0 {
		coef.Mul(coef, new(big.Int).Exp(bigTen, big.NewInt(-scale), nil))
		scale = 0
	}
	if neg {
		coef.Neg(coef)
	}
	return Decimal{coef: coef, scale: int32(scale)}.checked()
}

// checked drops trailing fractional digits beyond the precision limit
// and reports whether the value is in range.
func (d Decimal) checked() (Decimal, bool) {
	c := d.c()
	digits := len(new(big.Int).Abs(c).String())
	if c.Sign() == 0 {
		digits = 1
	}
	if digits > MaxDecimalDigits {
		excess := digits - MaxDecimalDigits
		if int(d.scale) < excess {
			return Decimal{}, false // integer part too large
		}
		d = d.rescale(d.scale - int32(excess))
	}
	return d, true
}

// rescale returns d at the given scale, rounding half away from zero
// when digits are dropped.
func (d Decimal) rescale(scale int32) Decimal {
	c := new(big.Int).Set(d.c())
	switch {
	case scale > d.scale:
		c.Mul(c, new(big.Int).Exp(bigTen, big.NewInt(int64(scale-d.scale)), nil))
	case scale < d.scale:
		div := new(big.Int).Exp(bigTen, big.NewInt(int64(d.scale-scale)), nil)
		q, r := new(big.Int).QuoRem(c, div, new(big.Int))
		r.Abs(r).Mul(r, big.NewInt(2))
		if r.Cmp(div) >= 0 {
			if c.Sign() < 0 {
				q.Sub(q, big.NewInt(1))
			} else {
				q.Add(q, big.NewInt(1))
			}
		}
		c = q
	}
	return Decimal{coef: c, scale: scale}
}

func align(a, b Decimal) (*big.Int, *big.Int, int32) {
	s := a.scale
	if b.scale > s {
		s = b.scale
	}
	return a.rescale(s).c(), b.rescale(s).c(), s
}

// Cmp compares numerically: -1, 0 or +1.
func (d Decimal) Cmp(o Decimal) int {
	x, y, _ := align(d, o)
	return x.Cmp(y)
}

// Sign returns -1, 0 or +1.
func (d Decimal) Sign() int { return d.c().Sign() }

// Add returns d + o; false when out of range.
func (d Decimal) Add(o Decimal) (Decimal, bool) {
	x, y, s := align(d, o)
	return Decimal{coef: new(big.Int).Add(x, y), scale: s}.checked()
}

// Sub returns d - o; false when out of range.
func (d Decimal) Sub(o Decimal) (Decimal, bool) {
	x, y, s := align(d, o)
	return Decimal{coef: new(big.Int).Sub(x, y), scale: s}.checked()
}

// Mul returns d × o; false when out of range.
func (d Decimal) Mul(o Decimal) (Decimal, bool) {
	return Decimal{coef: new(big.Int).Mul(d.c(), o.c()), scale: d.scale + o.scale}.checked()
}

// Quo returns d / o rounded to DecimalDivisionScale fractional digits;
// false for division by zero or out of range.
func (d Decimal) Quo(o Decimal) (Decimal, bool) {
	if o.Sign() == 0 {
		return Decimal{}, false
	}
	// d/o = (dc × 10^k) / oc × 10^-(ds + k - os); pick k for one guard digit.
	target := int32(DecimalDivisionScale + 1)
	k := target + o.scale - d.scale
	num := new(big.Int).Set(d.c())
	den := new(big.Int).Set(o.c())
	if k >= 0 {
		num.Mul(num, new(big.Int).Exp(bigTen, big.NewInt(int64(k)), nil))
	} else {
		den.Mul(den, new(big.Int).Exp(bigTen, big.NewInt(int64(-k)), nil))
	}
	q := new(big.Int).Quo(num, den)
	return Decimal{coef: q, scale: target}.rescale(DecimalDivisionScale).trimmed().checked()
}

// Rem returns the remainder of truncated division, sign of d.
func (d Decimal) Rem(o Decimal) (Decimal, bool) {
	if o.Sign() == 0 {
		return Decimal{}, false
	}
	x, y, s := align(d, o)
	return Decimal{coef: new(big.Int).Rem(x, y), scale: s}.checked()
}

// Neg returns -d.
func (d Decimal) Neg() Decimal { return Decimal{coef: new(big.Int).Neg(d.c()), scale: d.scale} }

// Abs returns |d|.
func (d Decimal) Abs() Decimal { return Decimal{coef: new(big.Int).Abs(d.c()), scale: d.scale} }

// Round returns d rounded to n fractional digits (half away from zero).
func (d Decimal) Round(n int32) Decimal {
	if n < 0 {
		n = 0
	}
	if n >= d.scale {
		return d
	}
	return d.rescale(n)
}

// Floor returns the largest multiple of step not above d (step > 0).
func (d Decimal) Floor(step Decimal) (Decimal, bool) {
	if step.Sign() <= 0 {
		return Decimal{}, false
	}
	x, y, s := align(d, step)
	q, r := new(big.Int).QuoRem(x, y, new(big.Int))
	if r.Sign() < 0 {
		q.Sub(q, big.NewInt(1))
	}
	return Decimal{coef: q.Mul(q, y), scale: s}.checked()
}

// trimmed drops trailing fractional zeros (used for quotients, whose
// scale is an artefact of the division, not of the inputs).
func (d Decimal) trimmed() Decimal {
	c := new(big.Int).Set(d.c())
	s := d.scale
	r := new(big.Int)
	for s > 0 {
		q, rem := new(big.Int).QuoRem(c, bigTen, r)
		if rem.Sign() != 0 {
			break
		}
		c = q
		s--
	}
	return Decimal{coef: c, scale: s}
}

// Float64 returns the nearest float64.
func (d Decimal) Float64() float64 {
	f, _ := strconv.ParseFloat(d.String(), 64)
	return f
}

// Int64 truncates toward zero; false when out of int64 range.
func (d Decimal) Int64() (int64, bool) {
	q := new(big.Int).Quo(d.c(), new(big.Int).Exp(bigTen, big.NewInt(int64(d.scale)), nil))
	if !q.IsInt64() {
		return 0, false
	}
	return q.Int64(), true
}

// String renders the value at its scale, without exponent.
func (d Decimal) String() string {
	c := d.c()
	neg := c.Sign() < 0
	digits := new(big.Int).Abs(c).String()
	if d.scale > 0 {
		if pad := int(d.scale) + 1 - len(digits); pad > 0 {
			digits = strings.Repeat("0", pad) + digits
		}
		cut := len(digits) - int(d.scale)
		digits = digits[:cut] + "." + digits[cut:]
	}
	if neg {
		return "-" + digits
	}
	return digits
}

// Format implements fmt.Formatter so %v prints the decimal form.
func (d Decimal) Format(f fmt.State, verb rune) { fmt.Fprint(f, d.String()) }

// ToDecimal converts a value to a decimal: numbers exactly (reals via
// their shortest representation), numeric strings by parsing, bools as
// 1 and 0. Anything else has no decimal value.
func ToDecimal(v Value) (Decimal, bool) {
	switch x := v.(type) {
	case Decimal:
		return x, true
	case int64:
		return DecimalFromInt64(x), true
	case int32:
		return DecimalFromInt64(int64(x)), true
	case int:
		return DecimalFromInt64(int64(x)), true
	case float64:
		return DecimalFromFloat(x)
	case float32:
		return DecimalFromFloat(float64(x))
	case bool:
		if x {
			return DecimalFromInt64(1), true
		}
		return DecimalFromInt64(0), true
	case string:
		return ParseDecimal(x)
	}
	return Decimal{}, false
}

// MarshalJSON encodes the value as a JSON number with every digit
// (inside dynamic values and JSON output). The REST server sends
// decimal cells as strings instead, as ADX does (cmd/gokql/server.go).
func (d Decimal) MarshalJSON() ([]byte, error) {
	return []byte(d.String()), nil
}
