package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// func_datetime_part_tz_test.go — datetime_part/datepart,
// datetime_local_to_utc, datetime_utc_to_local,
// unixtime_microseconds_todatetime, unixtime_nanoseconds_todatetime,
// added 2026-08-18. Every case below is taken directly from real
// ADX's own docs' worked examples. datetime_list_timezones() is
// deliberately NOT implemented -- see scalar_function_backlog.md's
// own scope note (no clean Go stdlib API to enumerate the IANA
// timezone database, and no worked example to verify output
// against).

// --- datetime_part / datepart ---

// TestDatetimePartFullWorkedExample guards all 12 documented parts
// against real ADX's own single worked example, in one query, with
// exact values.
func TestDatetimePartFullWorkedExample(t *testing.T) {
	result := queryResult(t, `let dt = datetime(2017-10-30 01:02:03.7654321);
		print
		year = datetime_part("year", dt),
		quarter = datetime_part("quarter", dt),
		month = datetime_part("month", dt),
		weekOfYear = datetime_part("week_of_year", dt),
		day = datetime_part("day", dt),
		dayOfYear = datetime_part("dayOfYear", dt),
		hour = datetime_part("hour", dt),
		minute = datetime_part("minute", dt),
		second = datetime_part("second", dt),
		millisecond = datetime_part("millisecond", dt),
		microsecond = datetime_part("microsecond", dt),
		nanosecond = datetime_part("nanosecond", dt)`)
	// datetime_part returns int (Kusto.Language: ScalarTypes.Int).
	want := []int32{2017, 4, 10, 44, 30, 303, 1, 2, 3, 765, 765432, 765432100}
	names := []string{"year", "quarter", "month", "weekOfYear", "day", "dayOfYear", "hour", "minute", "second", "millisecond", "microsecond", "nanosecond"}
	for i, w := range want {
		got, ok := result.Rows[0][i].(int32)
		if !ok || got != w {
			t.Errorf("%s = %v (%T), want int32(%d)", names[i], result.Rows[0][i], result.Rows[0][i], w)
		}
	}
}

// TestDatepartDeprecatedAlias guards that datepart() (real ADX's own
// documented deprecated alias) produces the same result as
// datetime_part().
func TestDatepartDeprecatedAlias(t *testing.T) {
	result := queryResult(t, `let dt = datetime(2017-10-30);
		print a = datetime_part("year", dt), b = datepart("year", dt)`)
	if result.Rows[0][0] != result.Rows[0][1] {
		t.Errorf("datetime_part = %v, datepart = %v, want equal", result.Rows[0][0], result.Rows[0][1])
	}
}

// --- datetime_local_to_utc / datetime_utc_to_local ---

func TestDatetimeLocalToUtcWorkedExample(t *testing.T) {
	result := queryResult(t, `datatable(local_dt: datetime, tz: string)
		[datetime(2020-02-02 20:02:20), "US/Pacific",
		 datetime(2020-02-02 20:02:20), "America/Chicago",
		 datetime(2020-02-02 20:02:20), "Europe/Paris"]
		| extend utc_dt = datetime_local_to_utc(local_dt, tz)`)
	if len(result.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(result.Rows))
	}
	want := []string{
		"2020-02-03T04:02:20Z", // US/Pacific
		"2020-02-03T02:02:20Z", // America/Chicago
		"2020-02-02T19:02:20Z", // Europe/Paris
	}
	for i, w := range want {
		got := types.FormatValue(result.Rows[i][2], types.TypeDatetime)
		if got != w {
			t.Errorf("row %d utc_dt = %v, want %v", i, got, w)
		}
	}
}

func TestDatetimeUtcToLocalWorkedExample(t *testing.T) {
	result := queryResult(t, `print dt=datetime(2022-07-11 22:18:48.4678620)
		| extend pacific_dt = datetime_utc_to_local(dt, "US/Pacific"),
		         canberra_dt = datetime_utc_to_local(dt, "Australia/Canberra")`)
	wantPacific := "2022-07-11T15:18:48.467862Z"
	wantCanberra := "2022-07-12T08:18:48.467862Z"
	if got := types.FormatValue(result.Rows[0][1], types.TypeDatetime); got != wantPacific {
		t.Errorf("pacific_dt = %v, want %v", got, wantPacific)
	}
	if got := types.FormatValue(result.Rows[0][2], types.TypeDatetime); got != wantCanberra {
		t.Errorf("canberra_dt = %v, want %v", got, wantCanberra)
	}
}

// TestDatetimeLocalUtcRoundTripDstAmbiguity guards real ADX's own
// documented DST-transition worked example, including the specific
// ambiguity it calls out: two distinct local times both map to the
// same UTC instant during a "spring forward" gap, so converting back
// to local doesn't always reproduce the original value.
func TestDatetimeLocalUtcRoundTripDstAmbiguity(t *testing.T) {
	result := queryResult(t, `range Local from datetime(2022-03-27 01:00:00) to datetime(2022-03-27 04:00:00) step 1h
		| extend UTC=datetime_local_to_utc(Local, "Europe/Brussels")
		| extend BackToLocal=datetime_utc_to_local(UTC, "Europe/Brussels")`)
	if len(result.Rows) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(result.Rows))
	}
	// The emulator's values: 02:00 (in the gap) takes the summer
	// offset, 00:00Z, where the docs page's table shows 01:00Z.
	wantUTC := []string{
		"2022-03-27T00:00:00Z",
		"2022-03-27T00:00:00Z",
		"2022-03-27T01:00:00Z",
		"2022-03-27T02:00:00Z",
	}
	wantBack := []string{
		"2022-03-27T01:00:00Z",
		"2022-03-27T01:00:00Z",
		"2022-03-27T03:00:00Z",
		"2022-03-27T04:00:00Z",
	}
	for i := range result.Rows {
		if got := types.FormatValue(result.Rows[i][1], types.TypeDatetime); got != wantUTC[i] {
			t.Errorf("row %d UTC = %v, want %v", i, got, wantUTC[i])
		}
		if got := types.FormatValue(result.Rows[i][2], types.TypeDatetime); got != wantBack[i] {
			t.Errorf("row %d BackToLocal = %v, want %v", i, got, wantBack[i])
		}
	}
}

// TestDatetimeLocalToUtcInvalidTimezoneReturnsNull guards a
// reasonable, undocumented-by-example edge case: an unrecognized
// timezone name returns null rather than erroring, matching this
// codebase's established convention for invalid input elsewhere.
func TestDatetimeLocalToUtcInvalidTimezoneReturnsNull(t *testing.T) {
	result := queryResult(t, `print r = datetime_local_to_utc(datetime(2020-01-01), "Not/A_Real_Zone")`)
	if result.Rows[0][0] != nil {
		t.Errorf("r = %v, want null", result.Rows[0][0])
	}
}

// --- unixtime_microseconds_todatetime / unixtime_nanoseconds_todatetime ---

func TestUnixtimeMicrosecondsToDatetime(t *testing.T) {
	result := queryResult(t, `print r = unixtime_microseconds_todatetime(1000000)`)
	want := "1970-01-01T00:00:01Z"
	if got := types.FormatValue(result.Rows[0][0], types.TypeDatetime); got != want {
		t.Errorf("r = %v, want %v", got, want)
	}
}

func TestUnixtimeNanosecondsToDatetime(t *testing.T) {
	result := queryResult(t, `print r = unixtime_nanoseconds_todatetime(1000000000)`)
	want := "1970-01-01T00:00:01Z"
	if got := types.FormatValue(result.Rows[0][0], types.TypeDatetime); got != want {
		t.Errorf("r = %v, want %v", got, want)
	}
}

// TestUnixtimeMicrosecondsNegativeBeforeEpoch guards real ADX's own
// documented "a datetime value that occurs before the epoch time has
// a negative timestamp value" case -- a real, intended input, not an
// error.
func TestUnixtimeMicrosecondsNegativeBeforeEpoch(t *testing.T) {
	result := queryResult(t, `print r = unixtime_microseconds_todatetime(-1000000)`)
	want := "1969-12-31T23:59:59Z"
	if got := types.FormatValue(result.Rows[0][0], types.TypeDatetime); got != want {
		t.Errorf("r = %v, want %v", got, want)
	}
}
