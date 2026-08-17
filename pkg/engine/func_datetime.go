package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// evalDatetimeFunc handles datetime and timespan functions.
func evalDatetimeFunc(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "now":
		return time.Now().UTC().UnixNano(), true, nil

	case "ago":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("ago requires 1 argument (timespan)")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		ticks := types.ToInt64(val)
		nanos := ticks * 100
		return time.Now().UTC().UnixNano() - nanos, true, nil

	case "datetime", "todatetime", "make_datetime":
		if len(fc.Args) < 1 {
			return nil, true, fmt.Errorf("%s requires at least 1 argument", fc.Name)
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		if n, ok := val.(int64); ok {
			return n, true, nil
		}
		s := fmt.Sprintf("%v", val)
		parsed, parseErr := types.ParseValue(s, types.TypeDatetime)
		if parseErr != nil {
			return nil, true, fmt.Errorf("cannot parse %q as datetime", s)
		}
		return parsed, true, nil

	case "bin", "floor":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("bin requires 2 arguments")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		roundTo, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil || roundTo == nil {
			return nil, true, nil
		}
		vn := types.ToInt64(val)
		rn := types.ToInt64(roundTo)
		if rn == 0 {
			return nil, true, nil
		}
		valType := inferExprType(fc.Args[0], schema)
		roundType := inferExprType(fc.Args[1], schema)
		if valType == types.TypeDatetime && roundType == types.TypeTimespan {
			roundNanos := rn * 100
			if roundNanos == 0 {
				return nil, true, nil
			}
			return (vn / roundNanos) * roundNanos, true, nil
		}
		return (vn / rn) * rn, true, nil

	case "datetime_diff":
		if len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("datetime_diff requires 3 arguments (part, dt1, dt2)")
		}
		partVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		dt1Val, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		dt2Val, err := evalExpr(fc.Args[2], schema, row)
		if err != nil {
			return nil, true, err
		}
		if dt1Val == nil || dt2Val == nil {
			return nil, true, nil
		}
		part := strings.ToLower(fmt.Sprintf("%v", partVal))
		diffNanos := types.ToInt64(dt1Val) - types.ToInt64(dt2Val)
		switch part {
		case "year":
			t1 := time.Unix(0, types.ToInt64(dt1Val)).UTC()
			t2 := time.Unix(0, types.ToInt64(dt2Val)).UTC()
			return int64(t1.Year() - t2.Year()), true, nil
		case "month":
			t1 := time.Unix(0, types.ToInt64(dt1Val)).UTC()
			t2 := time.Unix(0, types.ToInt64(dt2Val)).UTC()
			return int64((t1.Year()-t2.Year())*12 + int(t1.Month()) - int(t2.Month())), true, nil
		case "day":
			return diffNanos / (24 * 60 * 60 * 1e9), true, nil
		case "hour":
			return diffNanos / (60 * 60 * 1e9), true, nil
		case "minute":
			return diffNanos / (60 * 1e9), true, nil
		case "second":
			return diffNanos / 1e9, true, nil
		case "millisecond":
			return diffNanos / 1e6, true, nil
		default:
			return nil, true, fmt.Errorf("unknown datetime_diff part: %q", part)
		}

	case "startofday":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("startofday requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		t := time.Unix(0, types.ToInt64(val)).UTC()
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).UnixNano(), true, nil

	case "startofweek":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("startofweek requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		t := time.Unix(0, types.ToInt64(val)).UTC()
		weekday := int(t.Weekday())
		return time.Date(t.Year(), t.Month(), t.Day()-weekday, 0, 0, 0, 0, time.UTC).UnixNano(), true, nil

	case "startofmonth":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("startofmonth requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		t := time.Unix(0, types.ToInt64(val)).UTC()
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).UnixNano(), true, nil

	case "startofyear":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("startofyear requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		t := time.Unix(0, types.ToInt64(val)).UTC()
		return time.Date(t.Year(), 1, 1, 0, 0, 0, 0, time.UTC).UnixNano(), true, nil

	case "endofday":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("endofday requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		t := time.Unix(0, types.ToInt64(val)).UTC()
		return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC).UnixNano() - 1, true, nil

	case "endofweek":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("endofweek requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		t := time.Unix(0, types.ToInt64(val)).UTC()
		daysToNextSunday := 7 - int(t.Weekday())
		return time.Date(t.Year(), t.Month(), t.Day()+daysToNextSunday, 0, 0, 0, 0, time.UTC).UnixNano() - 1, true, nil

	case "endofmonth":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("endofmonth requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		t := time.Unix(0, types.ToInt64(val)).UTC()
		return time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC).UnixNano() - 1, true, nil

	case "format_datetime":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("format_datetime requires 2 arguments")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		fmtVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		t := time.Unix(0, types.ToInt64(val)).UTC()
		fmtStr := fmt.Sprintf("%v", fmtVal)
		return formatDatetimeKQL(t, fmtStr), true, nil

	case "format_timespan":
		// format_timespan(timespan, format) — formats a timespan value
		// Default format: d.hh:mm:ss.fffffff
		if len(fc.Args) < 1 || len(fc.Args) > 2 {
			return nil, true, fmt.Errorf("format_timespan requires 1-2 arguments")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		ticks := types.ToInt64(val) // 100ns ticks
		fmtStr := "d.hh:mm:ss.fffffff"
		if len(fc.Args) == 2 {
			fmtVal, err := evalExpr(fc.Args[1], schema, row)
			if err != nil {
				return nil, true, err
			}
			fmtStr = fmt.Sprintf("%v", fmtVal)
		}
		return formatTimespanKQL(ticks, fmtStr), true, nil

	case "dayofweek":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("dayofweek requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		t := time.Unix(0, types.ToInt64(val)).UTC()
		dayNanos := int64(t.Weekday()) * 24 * 60 * 60 * 1e9
		return dayNanos / 100, true, nil

	case "dayofmonth", "getmonth", "getyear", "hourofday", "monthofyear":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("%s requires 1 argument", fc.Name)
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		t := time.Unix(0, types.ToInt64(val)).UTC()
		switch fc.Name {
		case "dayofmonth":
			return int64(t.Day()), true, nil
		case "getmonth":
			return int64(t.Month()), true, nil
		case "monthofyear":
			return int64(t.Month()), true, nil
		case "getyear":
			return int64(t.Year()), true, nil
		case "hourofday":
			return int64(t.Hour()), true, nil
		}
		return nil, true, nil

	case "unixtime_seconds_todatetime":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("unixtime_seconds_todatetime requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		secs := types.ToFloat64(val)
		return int64(secs * 1e9), true, nil

	case "unixtime_milliseconds_todatetime":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("unixtime_milliseconds_todatetime requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		ms := types.ToFloat64(val)
		return int64(ms * 1e6), true, nil

	case "datetime_add":
		// datetime_add(part, amount, datetime)
		if len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("datetime_add requires 3 arguments (part, amount, datetime)")
		}
		partVal, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		amountVal, err := evalExpr(fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		dtVal, err := evalExpr(fc.Args[2], schema, row)
		if err != nil {
			return nil, true, err
		}
		if dtVal == nil {
			return nil, true, nil
		}
		part := strings.ToLower(fmt.Sprintf("%v", partVal))
		amount := int(types.ToInt64(amountVal))
		t := time.Unix(0, types.ToInt64(dtVal)).UTC()
		switch part {
		case "year":
			t = t.AddDate(amount, 0, 0)
		case "quarter":
			t = t.AddDate(0, amount*3, 0)
		case "month":
			t = t.AddDate(0, amount, 0)
		case "week":
			t = t.AddDate(0, 0, amount*7)
		case "day":
			t = t.AddDate(0, 0, amount)
		case "hour":
			t = t.Add(time.Duration(amount) * time.Hour)
		case "minute":
			t = t.Add(time.Duration(amount) * time.Minute)
		case "second":
			t = t.Add(time.Duration(amount) * time.Second)
		case "millisecond":
			t = t.Add(time.Duration(amount) * time.Millisecond)
		case "microsecond":
			t = t.Add(time.Duration(amount) * time.Microsecond)
		default:
			return nil, true, fmt.Errorf("unknown datetime_add part: %q", part)
		}
		return t.UnixNano(), true, nil

	case "totimespan":
		// totimespan(value) — convert string or number to timespan (ticks)
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("totimespan requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		// If already int64 (ticks), return as-is
		if n, ok := val.(int64); ok {
			return n, true, nil
		}
		// Parse timespan string like "1.02:03:04.567" or "1d" etc.
		s := fmt.Sprintf("%v", val)
		parsed, parseErr := types.ParseValue(s, types.TypeTimespan)
		if parseErr != nil {
			return nil, true, nil // return null on parse failure like KQL
		}
		return parsed, true, nil

	case "make_timespan":
		// make_timespan(hours, minutes) or make_timespan(hours, minutes, seconds)
		// or make_timespan(days, hours, minutes, seconds)
		if len(fc.Args) < 2 || len(fc.Args) > 4 {
			return nil, true, fmt.Errorf("make_timespan requires 2-4 arguments")
		}
		args := make([]int64, len(fc.Args))
		for i, arg := range fc.Args {
			v, err := evalExpr(arg, schema, row)
			if err != nil {
				return nil, true, err
			}
			args[i] = types.ToInt64(v)
		}
		var totalTicks int64
		switch len(args) {
		case 2: // hours, minutes
			totalTicks = (args[0]*3600 + args[1]*60) * 10000000
		case 3: // hours, minutes, seconds
			totalTicks = (args[0]*3600 + args[1]*60 + args[2]) * 10000000
		case 4: // days, hours, minutes, seconds
			totalTicks = (args[0]*86400 + args[1]*3600 + args[2]*60 + args[3]) * 10000000
		}
		return totalTicks, true, nil

	case "endofyear":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("endofyear requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		t := time.Unix(0, types.ToInt64(val)).UTC()
		return time.Date(t.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano() - 1, true, nil

	case "dayofyear":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("dayofyear requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		t := time.Unix(0, types.ToInt64(val)).UTC()
		return int64(t.YearDay()), true, nil

	case "weekofyear", "week_of_year":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("weekofyear requires 1 argument")
		}
		val, err := evalExpr(fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		t := time.Unix(0, types.ToInt64(val)).UTC()
		_, week := t.ISOWeek()
		return int64(week), true, nil

	default:
		return nil, false, nil
	}
}

// formatDatetimeKQL converts a KQL format string to Go time format and formats the time.
// formatDatetimeKQL implements real Kusto's format_datetime() format
// specifiers directly against the actual time value, not via
// strings.Replacer substitution into a Go reference-time layout string
// — that approach is fundamentally the wrong tool for this and was a
// real, live, silent-wrong-value bug, not just an incomplete one.
// strings.Replacer tries each old/new pair in the order given and
// takes the first match at each position, not the longest: with "fff"
// listed before "ffff", a run of four f's matched "fff" first
// (consuming 3), leaving a lone "f" to match separately — turning
// format_datetime(dt, "ffff") into "000"+"0" = "0000" instead of the
// real ten-thousandths-of-a-second value. Verified against Microsoft's
// own complete format-specifier table (with its own worked examples)
// before rewriting this, not assumed: bare d/h/m/s/M/y (no leading
// zero), tt (AM/PM), and the entire uppercase F-class (conditional,
// suppressed-if-zero fractional digits) were missing from the old
// implementation ENTIRELY — not silently wrong, simply absent, so
// those specifiers passed through as literal text (format_datetime(dt,
// "d") returned the literal string "d", not the day of month).
//
// Every specifier and delimiter in this function was checked directly
// against a worked example from the real docs, not just implemented
// from the description alone.
func formatDatetimeKQL(t time.Time, kqlFmt string) string {
	frac7 := fmt.Sprintf("%07d", t.Nanosecond()/100) // 100ns-tick precision, matching this engine's own internal datetime resolution

	var out strings.Builder
	runes := []rune(kqlFmt)
	for i := 0; i < len(runes); {
		ch := runes[i]
		j := i
		for j < len(runes) && runes[j] == ch {
			j++
		}
		run := string(runes[i:j])
		n := j - i

		switch ch {
		case 'y':
			switch n {
			case 4:
				out.WriteString(fmt.Sprintf("%04d", t.Year()))
			case 2:
				out.WriteString(fmt.Sprintf("%02d", t.Year()%100))
			case 1:
				out.WriteString(fmt.Sprintf("%d", t.Year()%100))
			default:
				out.WriteString(run)
			}
		case 'M':
			if n == 2 {
				out.WriteString(fmt.Sprintf("%02d", int(t.Month())))
			} else if n == 1 {
				out.WriteString(fmt.Sprintf("%d", int(t.Month())))
			} else {
				out.WriteString(run)
			}
		case 'd':
			if n == 2 {
				out.WriteString(fmt.Sprintf("%02d", t.Day()))
			} else if n == 1 {
				out.WriteString(fmt.Sprintf("%d", t.Day()))
			} else {
				out.WriteString(run)
			}
		case 'H':
			if n == 2 {
				out.WriteString(fmt.Sprintf("%02d", t.Hour()))
			} else if n == 1 {
				out.WriteString(fmt.Sprintf("%d", t.Hour()))
			} else {
				out.WriteString(run)
			}
		case 'h':
			h12 := t.Hour() % 12
			if h12 == 0 {
				h12 = 12
			}
			if n == 2 {
				out.WriteString(fmt.Sprintf("%02d", h12))
			} else if n == 1 {
				out.WriteString(fmt.Sprintf("%d", h12))
			} else {
				out.WriteString(run)
			}
		case 'm':
			if n == 2 {
				out.WriteString(fmt.Sprintf("%02d", t.Minute()))
			} else if n == 1 {
				out.WriteString(fmt.Sprintf("%d", t.Minute()))
			} else {
				out.WriteString(run)
			}
		case 's':
			if n == 2 {
				out.WriteString(fmt.Sprintf("%02d", t.Second()))
			} else if n == 1 {
				out.WriteString(fmt.Sprintf("%d", t.Second()))
			} else {
				out.WriteString(run)
			}
		case 'f':
			// Always shown, zero-padded to n digits — the first n
			// digits of the 7-digit (100ns-tick) fractional second.
			if n >= 1 && n <= 7 {
				out.WriteString(frac7[:n])
			} else {
				out.WriteString(run)
			}
		case 'F':
			// Conditional — shown only if the first n digits, taken
			// TOGETHER, are non-zero; suppressed (no output at all)
			// otherwise. Verified against the real docs' own worked
			// examples for exactly this rule: FF on .0050000 (first 2
			// digits "00") produces no output; on .6170000 (first 2
			// digits "61") produces "61".
			if n >= 1 && n <= 7 {
				digits := frac7[:n]
				allZero := true
				for _, d := range digits {
					if d != '0' {
						allZero = false
						break
					}
				}
				if !allZero {
					out.WriteString(digits)
				}
			} else {
				out.WriteString(run)
			}
		case 't':
			if n == 2 {
				if t.Hour() < 12 {
					out.WriteString("AM")
				} else {
					out.WriteString("PM")
				}
			} else {
				out.WriteString(run)
			}
		default:
			// Delimiters (space, /, -, :, comma, ., _, [, ]) and any
			// other character pass through literally, matching the
			// real docs' own supported-delimiters list.
			out.WriteString(run)
		}
		i = j
	}
	return out.String()
}

// formatTimespanKQL formats a timespan (100ns ticks) according to a KQL format string.
// Format specifiers: d=days, h/hh=hours, m/mm=minutes, s/ss=seconds, f-fffffff=fractional.
func formatTimespanKQL(ticks int64, kqlFmt string) string {
	negative := ticks < 0
	if negative {
		ticks = -ticks
	}

	totalNanos := ticks * 100 // 100ns ticks → nanoseconds
	days := totalNanos / (24 * 60 * 60 * 1e9)
	remainder := totalNanos % (24 * 60 * 60 * 1e9)
	hours := remainder / (60 * 60 * 1e9)
	remainder %= 60 * 60 * 1e9
	minutes := remainder / (60 * 1e9)
	remainder %= 60 * 1e9
	seconds := remainder / 1e9
	fracNanos := remainder % 1e9

	r := strings.NewReplacer(
		"fffffff", fmt.Sprintf("%07d", fracNanos/100),
		"ffffff", fmt.Sprintf("%06d", fracNanos/1000),
		"fffff", fmt.Sprintf("%05d", fracNanos/10000),
		"ffff", fmt.Sprintf("%04d", fracNanos/100000),
		"fff", fmt.Sprintf("%03d", fracNanos/1000000),
		"ff", fmt.Sprintf("%02d", fracNanos/10000000),
		"f", fmt.Sprintf("%d", fracNanos/100000000),
		"dd", fmt.Sprintf("%02d", days),
		"d", fmt.Sprintf("%d", days),
		"hh", fmt.Sprintf("%02d", hours),
		"h", fmt.Sprintf("%d", hours),
		"mm", fmt.Sprintf("%02d", minutes),
		"m", fmt.Sprintf("%d", minutes),
		"ss", fmt.Sprintf("%02d", seconds),
		"s", fmt.Sprintf("%d", seconds),
	)
	result := r.Replace(kqlFmt)
	if negative {
		result = "-" + result
	}
	return result
}
