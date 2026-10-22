package parser

// dynamic_literal.go — the dynamic(...) literal grammar, converted to
// the JSON text a dynamic value is stored as.
//
//	value   := object | array | string | number | true | false | null
//	         | typed literal | timespan literal
//	object  := '{' [member (',' member)*] [','] '}'
//	member  := (string | identifier) ':' value
//	array   := '[' [value (',' value)*] [','] ']'
//	string  := [h|H][@]'...' or [h|H][@]"..."  (KQL escapes; @ verbatim)
//	typed   := datetime(...) | time(...) | timespan(...) | guid(...)
//	         | long(...) | int(...) | real(...) | double(...)
//	         | decimal(...) | bool(...) | dynamic(...)
//	timespan literal := number unit, e.g. 1d, 1.5h, 100ms
//
// Datetimes become ISO 8601 strings with seven fractional digits,
// timespans their [d.]hh:mm:ss[.fffffff] form, guids strings.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// dynamicLiteralToJSON converts the literal at the start of s, which
// begins with '{' or '[', returning the JSON and the bytes consumed.
func dynamicLiteralToJSON(s string) (string, int, error) {
	d := &dynLit{s: s}
	var out strings.Builder
	if err := d.value(&out); err != nil {
		return "", 0, err
	}
	return out.String(), d.pos, nil
}

type dynLit struct {
	s   string
	pos int
}

func (d *dynLit) ws() {
	for d.pos < len(d.s) {
		c := d.s[d.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			d.pos++
			continue
		}
		// // comments run to the end of the line.
		if c == '/' && d.pos+1 < len(d.s) && d.s[d.pos+1] == '/' {
			for d.pos < len(d.s) && d.s[d.pos] != '\n' {
				d.pos++
			}
			continue
		}
		return
	}
}

func (d *dynLit) errf(format string, a ...interface{}) error {
	return fmt.Errorf("dynamic literal at offset %d: %s", d.pos, fmt.Sprintf(format, a...))
}

func (d *dynLit) value(out *strings.Builder) error {
	d.ws()
	if d.pos >= len(d.s) {
		return d.errf("unexpected end")
	}
	c := d.s[d.pos]
	switch {
	case c == '{':
		return d.object(out)
	case c == '[':
		return d.array(out)
	case c == '\'' || c == '"' || c == '@' || ((c == 'h' || c == 'H') && d.pos+1 < len(d.s) && strings.ContainsRune(`'"@`, rune(d.s[d.pos+1]))):
		str, err := d.str()
		if err != nil {
			return err
		}
		writeJSONString(out, str)
		return nil
	case c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9'):
		return d.number(out)
	case isIdentStart(c):
		return d.word(out)
	}
	return d.errf("unexpected %q", c)
}

func (d *dynLit) object(out *strings.Builder) error {
	d.pos++ // {
	out.WriteByte('{')
	first := true
	for {
		d.ws()
		if d.pos >= len(d.s) {
			return d.errf("unterminated object")
		}
		if d.s[d.pos] == '}' {
			d.pos++
			out.WriteByte('}')
			return nil
		}
		if !first {
			if d.s[d.pos] != ',' {
				return d.errf("expected , or } in object")
			}
			d.pos++
			d.ws()
			if d.pos < len(d.s) && d.s[d.pos] == '}' { // trailing comma
				d.pos++
				out.WriteByte('}')
				return nil
			}
			out.WriteByte(',')
		}
		first = false
		var key string
		c := d.s[d.pos]
		if c == '\'' || c == '"' || c == '@' || c == 'h' || c == 'H' {
			if c == 'h' || c == 'H' {
				if d.pos+1 < len(d.s) && strings.ContainsRune(`'"@`, rune(d.s[d.pos+1])) {
					k, err := d.str()
					if err != nil {
						return err
					}
					key = k
				} else {
					key = d.ident()
				}
			} else {
				k, err := d.str()
				if err != nil {
					return err
				}
				key = k
			}
		} else if isIdentStart(c) {
			key = d.ident()
		} else {
			return d.errf("expected a property name")
		}
		d.ws()
		if d.pos >= len(d.s) || d.s[d.pos] != ':' {
			return d.errf("expected : after property %q", key)
		}
		d.pos++
		writeJSONString(out, key)
		out.WriteByte(':')
		if err := d.value(out); err != nil {
			return err
		}
	}
}

func (d *dynLit) array(out *strings.Builder) error {
	d.pos++ // [
	out.WriteByte('[')
	first := true
	for {
		d.ws()
		if d.pos >= len(d.s) {
			return d.errf("unterminated array")
		}
		if d.s[d.pos] == ']' {
			d.pos++
			out.WriteByte(']')
			return nil
		}
		if !first {
			if d.s[d.pos] != ',' {
				return d.errf("expected , or ] in array")
			}
			d.pos++
			d.ws()
			if d.pos < len(d.s) && d.s[d.pos] == ']' {
				d.pos++
				out.WriteByte(']')
				return nil
			}
			out.WriteByte(',')
		}
		first = false
		if err := d.value(out); err != nil {
			return err
		}
	}
}

// str reads a KQL string literal (optional h/H obfuscation prefix and
// @ verbatim marker) and returns its value.
func (d *dynLit) str() (string, error) {
	if c := d.s[d.pos]; c == 'h' || c == 'H' {
		d.pos++
	}
	verbatim := false
	if d.s[d.pos] == '@' {
		verbatim = true
		d.pos++
	}
	if d.pos >= len(d.s) || (d.s[d.pos] != '\'' && d.s[d.pos] != '"') {
		return "", d.errf("expected a quoted string")
	}
	start := d.pos
	q := d.s[d.pos]
	d.pos++
	for d.pos < len(d.s) {
		c := d.s[d.pos]
		if verbatim {
			if c == q {
				if d.pos+1 < len(d.s) && d.s[d.pos+1] == q {
					d.pos += 2
					continue
				}
				d.pos++
				break
			}
			d.pos++
			continue
		}
		if c == '\\' {
			d.pos += 2
			continue
		}
		d.pos++
		if c == q {
			break
		}
	}
	raw := d.s[start:d.pos]
	if verbatim {
		raw = "@" + raw
	}
	e, err := ParseExpr(raw)
	if err != nil {
		return "", d.errf("bad string %s: %v", raw, err)
	}
	lit, ok := e.(*Literal)
	if !ok {
		return "", d.errf("bad string %s", raw)
	}
	v, _ := lit.Value.(string)
	return v, nil
}

func (d *dynLit) ident() string {
	start := d.pos
	for d.pos < len(d.s) && isIdentChar(d.s[d.pos]) {
		d.pos++
	}
	return d.s[start:d.pos]
}

// number reads a JSON number, or a timespan literal (number + unit).
func (d *dynLit) number(out *strings.Builder) error {
	start := d.pos
	if c := d.s[d.pos]; c == '-' || c == '+' {
		d.pos++
	}
	for d.pos < len(d.s) {
		c := d.s[d.pos]
		if (c >= '0' && c <= '9') || c == '.' {
			d.pos++
			continue
		}
		if (c == 'e' || c == 'E') && d.pos+1 < len(d.s) && (d.s[d.pos+1] == '-' || d.s[d.pos+1] == '+' || (d.s[d.pos+1] >= '0' && d.s[d.pos+1] <= '9')) {
			d.pos += 2
			continue
		}
		break
	}
	num := d.s[start:d.pos]
	if d.pos < len(d.s) && isIdentStart(d.s[d.pos]) {
		// 1d, 1.5h, 100ms: a timespan.
		unitStart := d.pos
		unit := d.ident()
		ticks, ok := types.ParseValue(num+unit, types.TypeTimespan)
		if ok != nil || ticks == nil {
			return d.errf("bad literal %s%s", num, d.s[unitStart:d.pos])
		}
		writeJSONString(out, formatTicks(ticks.(int64)))
		return nil
	}
	num = strings.TrimPrefix(num, "+")
	if strings.HasPrefix(num, ".") || strings.HasPrefix(num, "-.") {
		num = strings.Replace(num, ".", "0.", 1)
	}
	if strings.HasSuffix(num, ".") {
		num += "0"
	}
	if !json.Valid([]byte(num)) {
		return d.errf("bad number %q", num)
	}
	out.WriteString(num)
	return nil
}

// word reads true/false/null or a typed literal f(...).
func (d *dynLit) word(out *strings.Builder) error {
	w := d.ident()
	lw := strings.ToLower(w)
	switch lw {
	case "true", "false", "null":
		out.WriteString(lw)
		return nil
	}
	d.ws()
	if d.pos >= len(d.s) || d.s[d.pos] != '(' {
		return d.errf("unexpected %q", w)
	}
	if lw == "dynamic" {
		d.pos++
		d.ws()
		if d.pos < len(d.s) && (d.s[d.pos] == '{' || d.s[d.pos] == '[') {
			if err := d.value(out); err != nil {
				return err
			}
		} else {
			// dynamic(null), dynamic(1), dynamic("s")
			var inner strings.Builder
			if err := d.value(&inner); err != nil {
				return err
			}
			out.WriteString(inner.String())
		}
		d.ws()
		if d.pos >= len(d.s) || d.s[d.pos] != ')' {
			return d.errf("expected ) after dynamic(")
		}
		d.pos++
		return nil
	}
	// The raw argument text up to the matching ')'.
	depth := 0
	start := d.pos + 1
	end := -1
	for i := d.pos; i < len(d.s); i++ {
		switch d.s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return d.errf("unterminated %s(", w)
	}
	arg := strings.TrimSpace(d.s[start:end])
	d.pos = end + 1
	argLower := strings.ToLower(arg)
	if argLower == "null" || arg == "" {
		out.WriteString("null")
		return nil
	}
	unq := strings.Trim(arg, `'"`)
	switch lw {
	case "datetime", "todatetime":
		v, err := types.ParseValue(dateDashSpaceRe.ReplaceAllString(unq, "-"), types.TypeDatetime)
		if err != nil || v == nil {
			return d.errf("bad datetime(%s)", arg)
		}
		writeJSONString(out, time.Unix(0, v.(int64)).UTC().Format("2006-01-02T15:04:05.0000000Z"))
	case "time", "timespan", "totimespan":
		v, err := types.ParseValue(unq, types.TypeTimespan)
		if err != nil || v == nil {
			return d.errf("bad %s(%s)", w, arg)
		}
		writeJSONString(out, formatTicks(v.(int64)))
	case "guid", "toguid":
		writeJSONString(out, strings.ToLower(unq))
	case "long", "int", "tolong", "toint":
		n, err := strconv.ParseInt(unq, 0, 64)
		if err != nil {
			return d.errf("bad %s(%s)", w, arg)
		}
		out.WriteString(strconv.FormatInt(n, 10))
	case "real", "double", "decimal", "toreal", "todouble":
		f, err := strconv.ParseFloat(unq, 64)
		if err != nil {
			return d.errf("bad %s(%s)", w, arg)
		}
		out.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
	case "bool", "boolean", "tobool":
		switch argLower {
		case "true", "1":
			out.WriteString("true")
		case "false", "0":
			out.WriteString("false")
		default:
			return d.errf("bad %s(%s)", w, arg)
		}
	default:
		return d.errf("unsupported %s(...) in a dynamic literal", w)
	}
	return nil
}

func writeJSONString(out *strings.Builder, s string) {
	b, _ := json.Marshal(s)
	out.Write(b)
}

// formatTicks is the [-][d.]hh:mm:ss[.fffffff] form of a timespan.
func formatTicks(ticks int64) string {
	neg := ticks < 0
	if neg {
		ticks = -ticks
	}
	frac := ticks % 10000000
	secs := ticks / 10000000
	days := secs / 86400
	s := fmt.Sprintf("%02d:%02d:%02d", (secs%86400)/3600, (secs%3600)/60, secs%60)
	if days > 0 {
		s = fmt.Sprintf("%d.%s", days, s)
	}
	if frac != 0 {
		s += fmt.Sprintf(".%07d", frac)
	}
	if neg {
		s = "-" + s
	}
	return s
}
