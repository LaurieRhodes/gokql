package types

import "testing"

// datetime_mmddyyyy_test.go — guards the M/d/yyyy (US-culture short
// date) datetime parsing support added to ParseValue's TypeDatetime
// case (2026-08-18). This format is NOT in real ADX's own documented
// "Supported formats" table (scalar-data-types/datetime.md lists only
// ISO 8601 / RFC 822 / RFC 850 / Sortable, none of which are numeric
// month-first), but is confirmed live against real ADX's own "Parse
// and extend results" worked example (parse-operator.md /
// parse-where-operator.md): source strings like "lockTime=02/17/2016
// 08:40:01" parse into real, non-null datetime columns there, and only
// the example's deliberately-malformed sentinel values
// ("nonValidDateTime", "invalid_datetime") produce null. That means
// real ADX's actual parser is more permissive than its own published
// format table -- verified against the real worked example's own
// success/failure split, not assumed from the name or general .NET
// familiarity.
func TestParseValueDatetimeMMDDYYYY(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string // RFC3339Nano via FormatValue
	}{
		{
			name:  "zero-padded month and day, with seconds",
			input: "02/17/2016 08:40:01",
			want:  "2016-02-17T08:40:01Z",
		},
		{
			name:  "unpadded month, with seconds",
			input: "2/17/2016 08:40:01",
			want:  "2016-02-17T08:40:01Z",
		},
		{
			name:  "unpadded month and day",
			input: "2/1/2016 08:40:01",
			want:  "2016-02-01T08:40:01Z",
		},
		{
			name:  "date only, zero-padded",
			input: "02/17/2016",
			want:  "2016-02-17T00:00:00Z",
		},
		{
			name:  "date only, unpadded",
			input: "2/1/2016",
			want:  "2016-02-01T00:00:00Z",
		},
		{
			name:  "date + hour:minute, no seconds",
			input: "12/31/2016 23:59",
			want:  "2016-12-31T23:59:00Z",
		},
		{
			name:  "real worked-example previousLockTime value",
			input: "02/17/2016 08:39:01",
			want:  "2016-02-17T08:39:01Z",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := ParseValue(tt.input, TypeDatetime)
			if err != nil {
				t.Fatalf("ParseValue(%q, TypeDatetime): %v", tt.input, err)
			}
			got := FormatValue(v, TypeDatetime)
			if got != tt.want {
				t.Errorf("ParseValue(%q) formatted = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestParseValueDatetimeMMDDYYYYInvalidStillFails guards the real
// worked example's OTHER half: real ADX's own "nonValidDateTime"/
// "invalid_datetime" sentinel values must still fail to parse (produce
// an error here, null downstream via the parse operator) -- the new
// M/d/yyyy layouts must not become so permissive they swallow genuinely
// invalid input.
func TestParseValueDatetimeMMDDYYYYInvalidStillFails(t *testing.T) {
	invalid := []string{
		"nonValidDateTime",
		"invalid_datetime",
		"13/45/2016 08:40:01", // no valid month/day interpretation
		"not a date at all",
	}
	for _, in := range invalid {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseValue(in, TypeDatetime); err == nil {
				t.Errorf("ParseValue(%q, TypeDatetime) succeeded, want error", in)
			}
		})
	}
}

// TestParseValueDatetimeExistingFormatsUnaffected is a regression guard
// that the new M/d/yyyy layouts (added after all the pre-existing ones
// in the layout list) don't shadow or otherwise change parsing of the
// formats already supported before this change.
func TestParseValueDatetimeExistingFormatsUnaffected(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"2014-05-25T08:20:03.123456Z", "2014-05-25T08:20:03.123456Z"},
		{"2014-11-08 15:55:55", "2014-11-08T15:55:55Z"},
		{"2014-11-08T15:20", "2014-11-08T15:20:00Z"},
		{"2014-11-08", "2014-11-08T00:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			v, err := ParseValue(tt.input, TypeDatetime)
			if err != nil {
				t.Fatalf("ParseValue(%q, TypeDatetime): %v", tt.input, err)
			}
			got := FormatValue(v, TypeDatetime)
			if got != tt.want {
				t.Errorf("ParseValue(%q) formatted = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
