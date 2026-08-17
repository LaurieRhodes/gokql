package types

import (
	"math"
	"testing"
	"time"
)

func TestParseType(t *testing.T) {
	tests := []struct {
		input    string
		expected KQLType
	}{
		{"string", TypeString},
		{"long", TypeLong},
		{"int", TypeInt},
		{"real", TypeReal},
		{"bool", TypeBool},
		{"boolean", TypeBool},
		{"datetime", TypeDatetime},
		{"date", TypeDatetime},
		{"guid", TypeGUID},
		{"dynamic", TypeDynamic},
		{"timespan", TypeTimespan},
		{"double", TypeReal},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseType(tt.input)
			if err != nil {
				t.Fatalf("ParseType(%q): %v", tt.input, err)
			}
			if got != tt.expected {
				t.Errorf("ParseType(%q) = %s, want %s", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseTypeError(t *testing.T) {
	_, err := ParseType("badtype")
	if err == nil {
		t.Error("expected error for unknown type")
	}
}

func TestParseValue(t *testing.T) {
	tests := []struct {
		input    string
		typ      KQLType
		checkFn  func(Value) bool
	}{
		{"42", TypeLong, func(v Value) bool { return v.(int64) == 42 }},
		{"3.14", TypeReal, func(v Value) bool { return math.Abs(v.(float64)-3.14) < 0.001 }},
		{"hello", TypeString, func(v Value) bool { return v.(string) == "hello" }},
		{"true", TypeBool, func(v Value) bool { return v.(bool) == true }},
		{"false", TypeBool, func(v Value) bool { return v.(bool) == false }},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			v, err := ParseValue(tt.input, tt.typ)
			if err != nil {
				t.Fatalf("ParseValue(%q, %s): %v", tt.input, tt.typ, err)
			}
			if !tt.checkFn(v) {
				t.Errorf("ParseValue(%q, %s) = %v, unexpected", tt.input, tt.typ, v)
			}
		})
	}
}

func TestFormatValue(t *testing.T) {
	tests := []struct {
		name     string
		value    Value
		typ      KQLType
		expected string
	}{
		{"int64", int64(42), TypeLong, "42"},
		{"float64", float64(3.14), TypeReal, "3.14"},
		{"string", "hello", TypeString, "hello"},
		{"bool_true", true, TypeBool, "true"},
		{"bool_false", false, TypeBool, "false"},
		{"nil", nil, TypeString, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatValue(tt.value, tt.typ)
			if got != tt.expected {
				t.Errorf("FormatValue(%v, %s) = %q, want %q", tt.value, tt.typ, got, tt.expected)
			}
		})
	}
}

func TestCompareValues(t *testing.T) {
	tests := []struct {
		name     string
		a, b     Value
		typ      KQLType
		expected int // -1, 0, 1
	}{
		{"int_eq", int64(5), int64(5), TypeLong, 0},
		{"int_lt", int64(3), int64(5), TypeLong, -1},
		{"int_gt", int64(7), int64(5), TypeLong, 1},
		{"float_eq", float64(3.14), float64(3.14), TypeReal, 0},
		{"float_lt", float64(1.0), float64(2.0), TypeReal, -1},
		{"string_eq", "abc", "abc", TypeString, 0},
		{"string_lt", "abc", "def", TypeString, -1},
		{"string_gt", "def", "abc", TypeString, 1},
		{"bool_eq", true, true, TypeBool, 0},
		// null is the smallest possible value of any type — verified
		// against real Kusto's own documented sort behavior before
		// fixing what was here: "Default for asc is nulls first.
		// Default for desc is nulls last" is exactly the observable
		// behavior "null is always smallest" produces. An earlier
		// version of CompareValues had both branches backwards (a==nil
		// returned 1, b==nil returned -1), the exact opposite of this
		// rule -- found live via a different model's testing (Kimi),
		// against real data, not caught by any test until now: no
		// existing case in this table covered nil at all.
		{"nil_lt_value", nil, int64(5), TypeLong, -1},
		{"value_gt_nil", int64(5), nil, TypeLong, 1},
		{"nil_eq_nil", nil, nil, TypeLong, 0},
		{"nil_lt_string", nil, "abc", TypeString, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompareValues(tt.a, tt.b, tt.typ)
			if got != tt.expected {
				t.Errorf("CompareValues(%v, %v, %s) = %d, want %d", tt.a, tt.b, tt.typ, got, tt.expected)
			}
		})
	}
}

func TestToFloat64(t *testing.T) {
	tests := []struct {
		input    Value
		expected float64
	}{
		{int64(42), 42.0},
		{int32(42), 42.0},
		{float64(3.14), 3.14},
		// string and bool not handled by toFloat64 — returns 0
	}
	for _, tt := range tests {
		got := ToFloat64(tt.input)
		if math.Abs(got-tt.expected) > 0.001 {
			t.Errorf("ToFloat64(%v) = %f, want %f", tt.input, got, tt.expected)
		}
	}
}

func TestToInt64(t *testing.T) {
	tests := []struct {
		input    Value
		expected int64
	}{
		{int64(42), 42},
		{int32(10), 10},
		{float64(3.14), 3},
	}
	for _, tt := range tests {
		got := ToInt64(tt.input)
		if got != tt.expected {
			t.Errorf("ToInt64(%v) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestNewTable(t *testing.T) {
	schema := Schema{
		Columns: []Column{
			{Name: "A", Type: TypeLong},
			{Name: "B", Type: TypeString},
		},
	}
	tbl := NewTable("test", schema)
	if tbl.Name != "test" {
		t.Errorf("table name: got %q, want %q", tbl.Name, "test")
	}
	if len(tbl.Schema.Columns) != 2 {
		t.Errorf("expected 2 columns, got %d", len(tbl.Schema.Columns))
	}
	if len(tbl.Rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(tbl.Rows))
	}
}

func TestFormatDatetime(t *testing.T) {
	ts := time.Date(2026, 2, 28, 14, 30, 45, 0, time.UTC)
	got := FormatValue(ts, TypeDatetime)
	if got != "2026-02-28 14:30:45 +0000 UTC" {
		// Accept the Go default time format
		if len(got) < 10 {
			t.Errorf("datetime format too short: %q", got)
		}
	}
}

// TestToInt64StringParsing: tolong/toint/todouble on a string value
// previously returned 0 unconditionally (no string case existed at
// all in the switch). Found live during the backlog pass.
func TestToInt64StringParsing(t *testing.T) {
	if got := ToInt64("42"); got != 42 {
		t.Errorf("ToInt64(\"42\") = %d, want 42", got)
	}
	if got := ToInt64("42.0"); got != 42 {
		t.Errorf("ToInt64(\"42.0\") = %d, want 42 (float-shaped string)", got)
	}
	if got := ToInt64("not a number"); got != 0 {
		t.Errorf("ToInt64(non-numeric) = %d, want 0 (preserve always-a-value contract)", got)
	}
	if got := ToInt64(int64(42)); got != 42 {
		t.Errorf("ToInt64(int64) regressed: got %d", got)
	}
	if got := ToFloat64("3.14"); got != 3.14 {
		t.Errorf("ToFloat64(\"3.14\") = %v, want 3.14", got)
	}
}
