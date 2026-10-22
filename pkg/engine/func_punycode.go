package engine

// func_punycode.go — punycode_from_string, punycode_to_string,
// punycode_domain_to_string, punycode_domain_from_string: RFC 3492
// Punycode, with no IDNA case folding or validation (the references
// keep case and even spaces: "Lê Lợi" encodes to "L Li-gpa4517b").
// Domain forms split labels on ".", "。" (U+3002), "．" (U+FF0E) and
// "｡" (U+FF61), write "." between labels, and add or strip "xn--" on
// labels that need it. A failure gives "" as the references state.

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

const (
	pcBase        = 36
	pcTMin        = 1
	pcTMax        = 26
	pcSkew        = 38
	pcDamp        = 700
	pcInitialBias = 72
	pcInitialN    = 128
)

func pcAdapt(delta, numPoints int, first bool) int {
	if first {
		delta /= pcDamp
	} else {
		delta /= 2
	}
	delta += delta / numPoints
	k := 0
	for delta > ((pcBase-pcTMin)*pcTMax)/2 {
		delta /= pcBase - pcTMin
		k += pcBase
	}
	return k + (pcBase-pcTMin+1)*delta/(delta+pcSkew)
}

func pcDigit(d int) byte {
	if d < 26 {
		return byte('a' + d)
	}
	return byte('0' + d - 26)
}

func pcDecodeDigit(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c-'0') + 26, true
	case c >= 'A' && c <= 'Z':
		return int(c - 'A'), true
	case c >= 'a' && c <= 'z':
		return int(c - 'a'), true
	}
	return 0, false
}

// punycodeEncode is RFC 3492 encoding of s.
func punycodeEncode(s string) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	input := []rune(s)
	var out []byte
	for _, r := range input {
		if r < 0x80 {
			out = append(out, byte(r))
		}
	}
	b := len(out)
	h := b
	if b > 0 {
		out = append(out, '-')
	}
	n, delta, bias := pcInitialN, 0, pcInitialBias
	for h < len(input) {
		m := int(^uint(0) >> 1)
		for _, r := range input {
			if int(r) >= n && int(r) < m {
				m = int(r)
			}
		}
		if (m - n) > (int(^uint32(0)>>1)-delta)/(h+1) {
			return "", false
		}
		delta += (m - n) * (h + 1)
		n = m
		for _, r := range input {
			if int(r) < n {
				delta++
			}
			if int(r) == n {
				q := delta
				for k := pcBase; ; k += pcBase {
					t := k - bias
					if t < pcTMin {
						t = pcTMin
					} else if t > pcTMax {
						t = pcTMax
					}
					if q < t {
						break
					}
					out = append(out, pcDigit(t+(q-t)%(pcBase-t)))
					q = (q - t) / (pcBase - t)
				}
				out = append(out, pcDigit(q))
				bias = pcAdapt(delta, h+1, h == b)
				delta = 0
				h++
			}
		}
		delta++
		n++
	}
	return string(out), true
}

// punycodeDecode is RFC 3492 decoding of s.
func punycodeDecode(s string) (string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return "", false
		}
	}
	var out []rune
	pos := 0
	if d := strings.LastIndexByte(s, '-'); d >= 0 {
		for _, c := range s[:d] {
			out = append(out, c)
		}
		pos = d + 1
	}
	n, i, bias := pcInitialN, 0, pcInitialBias
	for pos < len(s) {
		oldi, w := i, 1
		for k := pcBase; ; k += pcBase {
			if pos >= len(s) {
				return "", false
			}
			digit, ok := pcDecodeDigit(s[pos])
			pos++
			if !ok {
				return "", false
			}
			i += digit * w
			t := k - bias
			if t < pcTMin {
				t = pcTMin
			} else if t > pcTMax {
				t = pcTMax
			}
			if digit < t {
				break
			}
			w *= pcBase - t
			if w > 1<<30 {
				return "", false
			}
		}
		bias = pcAdapt(i-oldi, len(out)+1, oldi == 0)
		n += i / (len(out) + 1)
		i %= len(out) + 1
		if n > utf8.MaxRune {
			return "", false
		}
		out = append(out, 0)
		copy(out[i+1:], out[i:])
		out[i] = rune(n)
		i++
	}
	return string(out), true
}

func splitDomainLabels(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == '.' || r == '。' || r == '．' || r == '｡'
	})
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func evalPunycodeFunc(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "punycode_from_string", "punycode_to_string", "punycode_domain_to_string", "punycode_domain_from_string":
	default:
		return nil, false, nil
	}
	if len(fc.Args) != 1 {
		return nil, true, fmt.Errorf("%s: expected 1 argument", fc.Name)
	}
	v, err := evalExpr(eng, fc.Args[0], schema, row)
	if err != nil {
		return nil, true, err
	}
	s, ok := v.(string)
	if !ok {
		return "", true, nil
	}
	fail := func(r string, ok bool) (types.Value, bool, error) {
		if !ok {
			return "", true, nil
		}
		return r, true, nil
	}
	switch fc.Name {
	case "punycode_from_string":
		return fail(punycodeEncode(s))
	case "punycode_to_string":
		return fail(punycodeDecode(s))
	case "punycode_domain_to_string":
		labels := splitDomainLabels(s)
		for i, l := range labels {
			if isASCII(l) {
				continue
			}
			e, ok := punycodeEncode(l)
			if !ok {
				return "", true, nil
			}
			labels[i] = "xn--" + e
		}
		return strings.Join(labels, "."), true, nil
	}
	labels := splitDomainLabels(s)
	for i, l := range labels {
		if len(l) > 4 && strings.EqualFold(l[:4], "xn--") {
			d, ok := punycodeDecode(l[4:])
			if !ok {
				return "", true, nil
			}
			labels[i] = d
		}
	}
	return strings.Join(labels, "."), true, nil
}
