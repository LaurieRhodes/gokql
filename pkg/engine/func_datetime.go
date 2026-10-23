package engine

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// evalDatetimeFunc handles datetime and timespan functions.
func evalDatetimeFunc(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "now":
		// One value per query (ADX), or set query_now's.
		n := eng.nowNanos()
		if len(fc.Args) == 1 { // now(offset)
			off, err := evalExpr(eng, fc.Args[0], schema, row)
			if err != nil || off == nil {
				return nil, true, err
			}
			n += types.ToInt64(off) * 100
		}
		return n, true, nil

	case "ingestion_time":
		// ingestion_time() — real ADX's own function-form accessor
		// for a HIDDEN $IngestionTime column, "if there's no datetime
		// column in the table, or the IngestionTime policy wasn't
		// enabled when a record was ingested, a null value is
		// returned" (real ADX's own IngestionTime policy docs).
		// This engine's own "_TimeReceived" is a real, ordinary
		// schema column internally — verified directly against real
		// Log Analytics/ADX export tooling before building it that
		// real ADX's own _TimeReceived is actually an ordinary column
		// too, not a specially-typed one — but IS hidden from query
		// OUTPUT unless explicitly referenced by name somewhere in
		// the operator chain (hideTimeReceivedUnlessExplicit,
		// timereceived.go, confirmed live while adding this function:
		// see that file's own header comment, which is now slightly
		// stale relative to that later addition — not fixed here, out
		// of scope for this specific function). This case reads the
		// real underlying value during expression evaluation, BEFORE
		// that later output-hiding step ever runs, so the VALUE
		// returned here matches real ADX's own null-if-absent
		// contract exactly: if the current table has no such column
		// (schema.ColumnIndex returns -1, e.g. a scope with the
		// feature disabled, or a table created with
		// notimereceived=true), or the column is null for this
		// specific row (a pre-feature row not yet rewritten, or an
		// ingest path that didn't populate it), null is returned
		// either way — not an error.
		if len(fc.Args) != 0 {
			return nil, true, fmt.Errorf("ingestion_time requires 0 arguments")
		}
		idx := schema.ColumnIndex(timeReceivedColumnName)
		if idx < 0 {
			return nil, true, nil
		}
		return row[idx], true, nil

	case "cursor_current":
		// A database cursor, as ADX's: the .NET ticks of the query's
		// now(). Rows written later compare after it (cursor_after).
		if len(fc.Args) != 0 {
			return nil, true, fmt.Errorf("cursor_current requires 0 arguments")
		}
		return strconv.FormatInt(eng.nowNanos()/100+dotnetTicksAtUnixEpoch, 10), true, nil

	case "cursor_after", "cursor_before_or_at":
		// cursor_after(c): the row was written after cursor c (from
		// cursor_current()); cursor_before_or_at(c): at or before it.
		// Uses the row's _TimeReceived; false for rows without one.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("%s requires 1 argument (a cursor)", fc.Name)
		}
		v, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		idx := schema.ColumnIndex(timeReceivedColumnName)
		if idx < 0 || row[idx] == nil {
			return false, true, nil
		}
		at := types.ToInt64(row[idx])
		cursor := fmt.Sprintf("%v", v)
		if v == nil || cursor == "" {
			// An empty cursor stands before everything.
			return fc.Name == "cursor_after", true, nil
		}
		ticks, err := strconv.ParseInt(strings.TrimSpace(cursor), 10, 64)
		if err != nil {
			return nil, true, fmt.Errorf("%s: %q is not a cursor (cursor_current() produces one)", fc.Name, cursor)
		}
		cut := (ticks - dotnetTicksAtUnixEpoch) * 100
		if fc.Name == "cursor_after" {
			return at > cut, true, nil
		}
		return at <= cut, true, nil

	case "ago":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("ago requires 1 argument (timespan)")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		ticks := types.ToInt64(val)
		nanos := ticks * 100
		return eng.nowNanos() - nanos, true, nil

	case "make_datetime":
		return evalMakeDatetime(eng, fc, schema, row)

	case "datetime", "todatetime":
		if fc.Name == "datetime" && len(fc.Args) >= 3 {
			// datetime(year, month, day[, hour, minute[, second]]).
			return evalMakeDatetime(eng, fc, schema, row)
		}
		if len(fc.Args) < 1 {
			return nil, true, fmt.Errorf("%s requires at least 1 argument", fc.Name)
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		if n, ok := val.(int64); ok {
			return n, true, nil
		}
		s, isStr := val.(string)
		if !isStr {
			s = fmt.Sprintf("%v", val)
		}
		parsed, parseErr := types.ParseValue(s, types.TypeDatetime)
		if parseErr != nil {
			if fc.Name == "todatetime" {
				return nil, true, nil // a conversion that fails is null
			}
			return nil, true, fmt.Errorf("cannot parse %q as datetime", s)
		}
		return parsed, true, nil

	case "bin", "floor", "bin_at":
		return evalBin(eng, fc, schema, row)

	case "bin_auto":
		// bin_auto(x): bin(x, query_bin_auto_size), aligned to
		// query_bin_auto_at when set; the size must be set.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("bin_auto requires 1 argument")
		}
		if eng == nil || eng.binAutoSize == nil {
			return nil, true, fmt.Errorf("bin_auto: set query_bin_auto_size first")
		}
		call := &parser.FuncCall{Name: "bin", Args: []parser.Expr{fc.Args[0], eng.binAutoSize}}
		if eng.binAutoAt != nil {
			call = &parser.FuncCall{Name: "bin_at", Args: []parser.Expr{fc.Args[0], eng.binAutoSize, eng.binAutoAt}}
		}
		return evalBin(eng, call, schema, row)

	case "datetime_diff":
		if len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("datetime_diff requires 3 arguments (part, dt1, dt2)")
		}
		partVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		dt1Val, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		dt2Val, err := evalExpr(eng, fc.Args[2], schema, row)
		if err != nil {
			return nil, true, err
		}
		if dt1Val == nil || dt2Val == nil {
			return nil, true, nil
		}
		part := strings.ToLower(fmt.Sprintf("%v", partVal))
		v, ok := datetimeDiff(part, types.ToInt64(dt1Val), types.ToInt64(dt2Val))
		if !ok {
			return nil, true, fmt.Errorf("unknown datetime_diff part: %q", part)
		}
		return v, true, nil

	case "startofday", "startofweek", "startofmonth", "startofyear",
		"endofday", "endofweek", "endofmonth", "endofyear":
		return evalPeriodBoundary(eng, fc, schema, row)

	case "format_datetime":
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("format_datetime requires 2 arguments")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		fmtVal, err := evalExpr(eng, fc.Args[1], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		ticks := types.ToInt64(val) // 100ns ticks
		fmtStr := "d.hh:mm:ss.fffffff"
		if len(fc.Args) == 2 {
			fmtVal, err := evalExpr(eng, fc.Args[1], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		ms := types.ToFloat64(val)
		return int64(ms * 1e6), true, nil

	case "unixtime_microseconds_todatetime":
		// verified against real ADX's own docs before implementing:
		// same linear-scaling family as the already-implemented
		// seconds/milliseconds siblings, "a datetime value that
		// occurs before the epoch time has a negative timestamp
		// value" — negative input is a real, intended case, not an
		// error, handled naturally by the same multiply-by-scale
		// arithmetic already used for the sibling functions.
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("unixtime_microseconds_todatetime requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		us := types.ToFloat64(val)
		return int64(us * 1e3), true, nil

	case "unixtime_nanoseconds_todatetime":
		// verified against real ADX's own docs before implementing:
		// this engine's own internal datetime representation IS
		// already unix-epoch nanoseconds, so this is a direct
		// pass-through (via types.ToInt64 rather than the
		// float64-then-scale pattern the coarser-granularity siblings
		// use, since no scaling multiplication is needed here and an
		// int64->float64->int64 round trip risks losing precision at
		// nanosecond scale for large epoch values).
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("unixtime_nanoseconds_todatetime requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		if val == nil {
			return nil, true, nil
		}
		return types.ToInt64(val), true, nil

	case "datetime_local_to_utc":
		// datetime_local_to_utc(from, timezone) — verified against
		// real ADX's own docs before implementing, all 3 rows of its
		// own worked example checked with exact values
		// (datetime(2020-02-02 20:02:20) local in 'Europe/Paris' ->
		// 2020-02-02 19:02:20 UTC; in 'America/Chicago' -> 2020-02-03
		// 02:02:20 UTC; in 'US/Pacific' -> 2020-02-03 04:02:20 UTC).
		// Real ADX's own docs state real KQL datetime values are
		// ALWAYS stored as UTC internally (confirmed on the datetime
		// data type's own doc page) — so "from" here is really a raw
		// wall-clock reading that needs to be REINTERPRETED as
		// belonging to the named timezone, not an actual UTC instant
		// shifted by an offset. Implemented by extracting this
		// engine's own stored Y/M/D/H/M/S/ns components (via
		// time.Unix(...).UTC(), which is how this engine always reads
		// its own internal representation) and reconstructing a NEW
		// time.Time using those exact same numbers but tagged with
		// the target IANA location instead of UTC — Go's own
		// time.Date does exactly this reinterpretation when given a
		// non-UTC *time.Location, then .UTC() on the result performs
		// the real timezone-offset conversion.
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("datetime_local_to_utc requires 2 arguments (from, timezone)")
		}
		fromVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		tzVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if fromVal == nil {
			return nil, true, nil
		}
		loc, lerr := time.LoadLocation(fmt.Sprintf("%v", tzVal))
		if lerr != nil {
			return nil, true, nil
		}
		return localToUTC(types.ToInt64(fromVal), loc), true, nil

	case "datetime_list_timezones":
		if len(fc.Args) != 0 {
			return nil, true, fmt.Errorf("datetime_list_timezones takes no arguments")
		}
		return kqlTimezonesJSON, true, nil

	case "datetime_utc_to_local":
		// datetime_utc_to_local(from, timezone) — the documented
		// inverse of datetime_local_to_utc(), verified against real
		// ADX's own worked example: real UTC instant converted to the
		// target zone's own wall-clock reading, then that reading is
		// RE-TAGGED as UTC (matching real ADX's own "datetime is
		// always UTC internally, so a local reading is represented by
		// storing its own numbers as if they were UTC" convention —
		// same one datetime_local_to_utc's own comment above
		// describes from the other direction). Implemented by taking
		// this engine's own stored instant (a true UTC moment),
		// converting to the target zone via .In(loc), then
		// reconstructing a UTC-tagged time.Time from THAT
		// representation's own Y/M/D/H/M/S/ns components.
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("datetime_utc_to_local requires 2 arguments (from, timezone)")
		}
		fromVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		tzVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if fromVal == nil {
			return nil, true, nil
		}
		loc, lerr := time.LoadLocation(fmt.Sprintf("%v", tzVal))
		if lerr != nil {
			return nil, true, nil
		}
		utcInstant := time.Unix(0, types.ToInt64(fromVal)).UTC()
		local := utcInstant.In(loc)
		relabeled := time.Date(local.Year(), local.Month(), local.Day(),
			local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), time.UTC)
		return relabeled.UnixNano(), true, nil

	case "datetime_add":
		// datetime_add(part, amount, datetime)
		if len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("datetime_add requires 3 arguments (part, amount, datetime)")
		}
		partVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		amountVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		dtVal, err := evalExpr(eng, fc.Args[2], schema, row)
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

	case "datetime_part", "datepart":
		// datetime_part(part, datetime) — verified against real ADX's
		// own docs before implementing, its own full 12-part worked
		// example checked with exact values against
		// datetime(2017-10-30 01:02:03.7654321): year=2017,
		// quarter=4, month=10, week_of_year=44, day=30, dayOfYear=303,
		// hour=1, minute=2, second=3, millisecond=765,
		// microsecond=765432, nanosecond=765432100. `datepart` is
		// real ADX's own documented deprecated alias, routed
		// identically. week_of_year uses the same ISO 8601 week
		// calculation (Go's own t.ISOWeek()) already established and
		// verified for the pre-existing weekofyear/week_of_year
		// scalar function elsewhere in this file, reused directly
		// rather than reimplemented, so the two can't silently drift
		// apart. Implemented directly against Go's time.Time methods
		// rather than delegating to this engine's own year()/month()/
		// etc functions — checked first and found those don't
		// actually exist as standalone callable functions in this
		// engine (only getyear()/monthofyear()/hourofday()-style
		// named aliases do), so a "thin wrapper" as the backlog
		// suggested checking for wasn't actually available to wrap.
		if len(fc.Args) != 2 {
			return nil, true, fmt.Errorf("%s requires 2 arguments (part, datetime)", fc.Name)
		}
		partVal, err := evalExpr(eng, fc.Args[0], schema, row)
		if err != nil {
			return nil, true, err
		}
		dtVal, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if dtVal == nil {
			return nil, true, nil
		}
		t := time.Unix(0, types.ToInt64(dtVal)).UTC()
		switch strings.ToLower(fmt.Sprintf("%v", partVal)) {
		case "year":
			return int64(t.Year()), true, nil
		case "quarter":
			return int64((int(t.Month())-1)/3 + 1), true, nil
		case "month":
			return int64(t.Month()), true, nil
		case "week_of_year":
			_, week := t.ISOWeek()
			return int64(week), true, nil
		case "day":
			return int64(t.Day()), true, nil
		case "dayofyear":
			return int64(t.YearDay()), true, nil
		case "hour":
			return int64(t.Hour()), true, nil
		case "minute":
			return int64(t.Minute()), true, nil
		case "second":
			return int64(t.Second()), true, nil
		case "millisecond":
			return int64(t.Nanosecond() / 1_000_000), true, nil
		case "microsecond":
			return int64(t.Nanosecond() / 1_000), true, nil
		case "nanosecond":
			return int64(t.Nanosecond()), true, nil
		default:
			return nil, true, fmt.Errorf("%s: unknown part %q", fc.Name, fmt.Sprintf("%v", partVal))
		}

	case "totimespan", "totime", "time", "timespan":
		// totimespan(value) — convert string or number to timespan
		// (ticks). time(...) is the timespan literal form, time(1h).
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("%s requires 1 argument", fc.Name)
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		// The seconds argument is real: make_timespan(1,12,30,55.123)
		// is 1.12:30:55.1230000.
		args := make([]float64, len(fc.Args))
		for i, arg := range fc.Args {
			v, err := evalExpr(eng, arg, schema, row)
			if err != nil {
				return nil, true, err
			}
			if v == nil {
				return nil, true, nil
			}
			args[i] = types.ToFloat64(v)
		}
		var secs float64
		switch len(args) {
		case 2: // hours, minutes
			secs = args[0]*3600 + args[1]*60
		case 3: // hours, minutes, seconds
			secs = args[0]*3600 + args[1]*60 + args[2]
		case 4: // days, hours, minutes, seconds
			secs = args[0]*86400 + args[1]*3600 + args[2]*60 + args[3]
		}
		return int64(math.Round(secs * 1e7)), true, nil

	case "dayofyear":
		if len(fc.Args) != 1 {
			return nil, true, fmt.Errorf("dayofyear requires 1 argument")
		}
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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
		val, err := evalExpr(eng, fc.Args[0], schema, row)
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

	// Runs of one specifier letter are one element: d.. (days, padded
	// to the run length), h/H, m, s (one or two digits), f.. (that
	// many fraction digits), F.. (the same, trailing zeros dropped,
	// nothing when zero). Anything else is copied.
	frac7 := fmt.Sprintf("%07d", fracNanos/100)
	var out strings.Builder
	for i := 0; i < len(kqlFmt); {
		c := kqlFmt[i]
		j := i
		for j < len(kqlFmt) && kqlFmt[j] == c {
			j++
		}
		n := j - i
		switch c {
		case 'd':
			out.WriteString(fmt.Sprintf("%0*d", n, days))
		case 'h', 'H':
			out.WriteString(fmt.Sprintf("%0*d", min(n, 2), hours))
		case 'm':
			out.WriteString(fmt.Sprintf("%0*d", min(n, 2), minutes))
		case 's':
			out.WriteString(fmt.Sprintf("%0*d", min(n, 2), seconds))
		case 'f':
			out.WriteString(frac7[:min(n, 7)])
		case 'F':
			out.WriteString(strings.TrimRight(frac7[:min(n, 7)], "0"))
		default:
			out.WriteString(kqlFmt[i:j])
		}
		i = j
	}
	result := out.String()
	if negative {
		result = "-" + result
	}
	return result
}

// evalMakeDatetime is make_datetime(year, month, day [, hour, minute
// [, second]]): second may be fractional (to 100ns ticks). A part out
// of range (or a day the month lacks) gives null.
func evalMakeDatetime(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if n := len(fc.Args); n != 3 && n != 5 && n != 6 {
		return nil, true, fmt.Errorf("make_datetime takes (year, month, day [, hour, minute [, second]])")
	}
	parts := make([]float64, 6)
	for i, a := range fc.Args {
		v, err := evalExpr(eng, a, schema, row)
		if err != nil {
			return nil, true, err
		}
		if v == nil {
			return nil, true, nil
		}
		parts[i] = types.ToFloat64(v)
	}
	year, month, day, hour, minute := parts[0], parts[1], parts[2], parts[3], parts[4]
	sec := parts[5]
	for i, p := range parts[:5] {
		if p != math.Trunc(p) || (i < len(fc.Args) && math.IsNaN(p)) {
			return nil, true, nil
		}
	}
	if year < 0 || year > 9999 || month < 1 || month > 12 || day < 1 || day > 31 ||
		hour < 0 || hour > 23 || minute < 0 || minute > 59 || sec < 0 || sec >= 60 || math.IsNaN(sec) {
		return nil, true, nil
	}
	t := time.Date(int(year), time.Month(month), int(day), int(hour), int(minute), 0, 0, time.UTC)
	if t.Day() != int(day) { // e.g. February 30
		return nil, true, nil
	}
	ticks := int64(math.Round(sec * 1e7))
	return t.UnixNano() + ticks*100, true, nil
}

// evalPeriodBoundary implements startof/endof day, week, month and
// year: f(date [, offset]), offset counting whole periods. Weeks start
// on Sunday; an end is one tick (100ns) before the next start.
func evalPeriodBoundary(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if len(fc.Args) < 1 || len(fc.Args) > 2 {
		return nil, true, fmt.Errorf("%s requires 1 or 2 arguments", fc.Name)
	}
	val, err := evalExpr(eng, fc.Args[0], schema, row)
	if err != nil {
		return nil, true, err
	}
	if val == nil {
		return nil, true, nil
	}
	offset := 0
	if len(fc.Args) == 2 {
		ov, err := evalExpr(eng, fc.Args[1], schema, row)
		if err != nil {
			return nil, true, err
		}
		if ov == nil {
			return nil, true, nil
		}
		offset = int(types.ToInt64(ov))
	}
	end := strings.HasPrefix(fc.Name, "end")
	if end {
		offset++
	}
	t := time.Unix(0, types.ToInt64(val)).UTC()
	var start time.Time
	switch strings.TrimPrefix(strings.TrimPrefix(fc.Name, "start"), "end") {
	case "ofday":
		start = time.Date(t.Year(), t.Month(), t.Day()+offset, 0, 0, 0, 0, time.UTC)
	case "ofweek":
		start = time.Date(t.Year(), t.Month(), t.Day()-int(t.Weekday())+7*offset, 0, 0, 0, 0, time.UTC)
	case "ofmonth":
		start = time.Date(t.Year(), t.Month()+time.Month(offset), 1, 0, 0, 0, 0, time.UTC)
	case "ofyear":
		start = time.Date(t.Year()+offset, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	n := start.UnixNano()
	if end {
		n -= 100
	}
	return n, true, nil
}

// localToUTC reads nanos as a wall-clock time in loc and returns the
// UTC instant. Around a transition it follows the emulator: a time in
// a spring-forward gap takes the offset of larger magnitude (02:00 in
// Brussels is 00:00Z; 02:30 in New York is 07:30Z); a time that occurs
// twice takes the offset after the transition (standard time).
func localToUTC(nanos int64, loc *time.Location) int64 {
	wall := time.Unix(0, nanos).UTC()
	t := time.Date(wall.Year(), wall.Month(), wall.Day(),
		wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), loc)
	_, offBefore := t.Add(-12 * time.Hour).Zone()
	_, offAfter := t.Add(12 * time.Hour).Zone()
	if offBefore == offAfter {
		return t.UnixNano()
	}
	w := wall.UnixNano() // the wall reading as if UTC
	cand := func(off int) (int64, bool) {
		u := w - int64(off)*int64(time.Second)
		lw := time.Unix(0, u).In(loc)
		return u, lw.Year() == wall.Year() && lw.YearDay() == wall.YearDay() &&
			lw.Hour() == wall.Hour() && lw.Minute() == wall.Minute() && lw.Second() == wall.Second()
	}
	ub, okb := cand(offBefore)
	ua, oka := cand(offAfter)
	switch {
	case oka && okb: // occurs twice
		return ua
	case oka:
		return ua
	case okb:
		return ub
	}
	// in a gap
	if abs(offAfter) >= abs(offBefore) {
		return ua
	}
	return ub
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// datetimeDiff is datetime_diff(part, a, b): the number of part
// boundaries between b and a, as the emulator counts them (one second
// across midnight is a day; weeks start on Sunday and the week count
// is real).
func datetimeDiff(part string, a, b int64) (types.Value, bool) {
	floorDiv := func(x, d int64) int64 {
		q := x / d
		if (x%d != 0) && ((x < 0) != (d < 0)) {
			q--
		}
		return q
	}
	units := map[string]int64{
		"day": 24 * 3600 * 1e9, "hour": 3600 * 1e9, "minute": 60 * 1e9, "second": 1e9,
		"millisecond": 1e6, "microsecond": 1e3,
	}
	if u, ok := units[part]; ok {
		return floorDiv(a, u) - floorDiv(b, u), true
	}
	ta, tb := time.Unix(0, a).UTC(), time.Unix(0, b).UTC()
	switch part {
	case "year":
		return int64(ta.Year() - tb.Year()), true
	case "quarter":
		q := func(t time.Time) int64 { return int64(t.Year())*4 + int64(t.Month()-1)/3 }
		return q(ta) - q(tb), true
	case "month":
		return int64((ta.Year()-tb.Year())*12 + int(ta.Month()) - int(tb.Month())), true
	case "week":
		const day = 24 * 3600 * 1e9
		wk := func(x int64) int64 { return floorDiv(floorDiv(x, day)-3, 7) } // 1970-01-04 is a Sunday
		return float64(wk(a) - wk(b)), true
	case "nanosecond":
		return (a - a%100) - (b - b%100), true
	}
	return nil, false
}
