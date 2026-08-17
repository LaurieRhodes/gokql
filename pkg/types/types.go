// Package types defines KQL data types and their mapping to Go native types
// and Vortex DTypes. This is the bridge between KQL's type system and the
// storage layer.
package types

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// KQLType represents a KQL scalar data type.
type KQLType int

const (
	TypeString   KQLType = iota // string — UTF-8 text
	TypeLong                    // long — 64-bit signed integer
	TypeInt                     // int — 32-bit signed integer
	TypeReal                    // real — 64-bit IEEE 754 float
	TypeBool                    // bool — true/false
	TypeDatetime                // datetime — UTC timestamp (nanosecond precision)
	TypeTimespan                // timespan — duration (100ns ticks)
	TypeGUID                    // guid — UUID as string
	TypeDynamic                 // dynamic — JSON as string
)

// String returns the KQL type name.
func (t KQLType) String() string {
	switch t {
	case TypeString:
		return "string"
	case TypeLong:
		return "long"
	case TypeInt:
		return "int"
	case TypeReal:
		return "real"
	case TypeBool:
		return "bool"
	case TypeDatetime:
		return "datetime"
	case TypeTimespan:
		return "timespan"
	case TypeGUID:
		return "guid"
	case TypeDynamic:
		return "dynamic"
	default:
		return "unknown"
	}
}

// ParseType converts a KQL type name string to a KQLType.
func ParseType(s string) (KQLType, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "string":
		return TypeString, nil
	case "long", "int64":
		return TypeLong, nil
	case "int", "int32":
		return TypeInt, nil
	case "real", "double", "float":
		return TypeReal, nil
	case "bool", "boolean":
		return TypeBool, nil
	case "datetime", "date":
		return TypeDatetime, nil
	case "timespan":
		return TypeTimespan, nil
	case "guid", "uuid":
		return TypeGUID, nil
	case "dynamic":
		return TypeDynamic, nil
	default:
		return 0, fmt.Errorf("unknown KQL type: %q", s)
	}
}

// IsNumeric returns true for types that support arithmetic and comparison.
func (t KQLType) IsNumeric() bool {
	return t == TypeLong || t == TypeInt || t == TypeReal || t == TypeDatetime || t == TypeTimespan
}

// IsString returns true for types stored as UTF-8 strings.
func (t KQLType) IsString() bool {
	return t == TypeString || t == TypeGUID || t == TypeDynamic
}

// Column defines a named, typed column in a table schema.
type Column struct {
	Name string
	Type KQLType
}

// DotNetTypeName returns the .NET type name real Kusto reports for a
// column's DataType field (getschema, and the network-server response
// shape — see cmd/gokql/server.go). Extracted here specifically so
// applyGetSchema (operators.go) and the HTTP server share exactly one
// mapping rather than maintaining two that could drift — the same
// principle this codebase has already paid for getting wrong more
// than once this session (convertDataTableValue's duplicate datetime
// parsing being the clearest earlier example).
func (t KQLType) DotNetTypeName() string {
	switch t {
	case TypeLong:
		return "System.Int64"
	case TypeInt:
		// int (32-bit) is genuinely distinct from long (64-bit) in
		// real Kusto -- verified against Microsoft's own examples
		// before writing this, not assumed. An earlier version of this
		// mapping (in operators.go's applyGetSchema, before this was
		// extracted into one shared function) never had a case for
		// TypeInt at all and silently fell through to System.String --
		// a real, previously-unnoticed gap this extraction incidentally
		// surfaced and fixed while consolidating the two
		// implementations into one.
		return "System.Int32"
	case TypeReal:
		return "System.Double"
	case TypeDatetime:
		return "System.DateTime"
	case TypeBool:
		return "System.Boolean"
	case TypeTimespan:
		return "System.TimeSpan"
	case TypeDynamic:
		return "System.Object"
	default: // TypeString, TypeGUID, and any future/unrecognized type
		return "System.String"
	}
}

// Schema is an ordered list of columns defining a table's structure.
type Schema struct {
	Columns []Column
}

// ColumnIndex returns the index of a column by name, or -1 if not found.
func (s *Schema) ColumnIndex(name string) int {
	// Case-SENSITIVE, deliberately -- verified against real Kusto's
	// own docs before fixing what was here: "KQL is case-sensitive for
	// everything -- table names, table column names, operators,
	// functions, and so on." The case-insensitive version of this
	// function was a real, live bug, not a deliberate leniency: a
	// scalar let binding (or, discovered via the same path, a stored
	// function parameter) whose name case-insensitively matched an
	// existing column -- e.g. `let status = "active"; T | where
	// Status == status`, or a parameter named cardId next to a column
	// Id -- silently resolved the RIGHT-hand reference as the SAME
	// column instead of falling through to the let/parameter binding,
	// turning `where Status == status` into `where Status == Status`:
	// a tautology matching every row, not a filter, with no error at
	// all. lowerCamelCase parameter/variable names next to PascalCase
	// columns is an entirely natural, idiomatic naming convention --
	// this bug would have made stored-function parameters (and every
	// existing scalar let binding) unreliable in exactly the cases
	// most likely to occur in practice.
	for i, col := range s.Columns {
		if col.Name == name {
			return i
		}
	}
	return -1
}

// ColumnByName returns the column definition, or nil if not found.
func (s *Schema) ColumnByName(name string) *Column {
	idx := s.ColumnIndex(name)
	if idx < 0 {
		return nil
	}
	return &s.Columns[idx]
}

// ColumnNames returns just the column name strings.
func (s *Schema) ColumnNames() []string {
	names := make([]string, len(s.Columns))
	for i, c := range s.Columns {
		names[i] = c.Name
	}
	return names
}

// Value represents a KQL scalar value. nil means null.
type Value = interface{}

// Row is an ordered slice of values, one per column in the schema.
type Row = []Value

// Table is an in-memory result set: schema + rows.
type Table struct {
	Name   string
	Schema Schema
	Rows   []Row
}

// NewTable creates an empty table with the given schema.
func NewTable(name string, schema Schema) *Table {
	return &Table{
		Name:   name,
		Schema: schema,
		Rows:   nil,
	}
}

// AddRow appends a row. No type checking — caller must ensure correct types.
func (t *Table) AddRow(row Row) {
	t.Rows = append(t.Rows, row)
}

// RowCount returns the number of rows.
func (t *Table) RowCount() int {
	return len(t.Rows)
}

// ParseValue parses a string into a typed Go value for the given KQL type.
// Used by the ingest path to convert CSV fields.
func ParseValue(s string, typ KQLType) (Value, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return nil, nil
	}

	switch typ {
	case TypeString, TypeGUID, TypeDynamic:
		return s, nil

	case TypeLong:
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("cannot parse %q as long: %w", s, err)
		}
		return v, nil

	case TypeInt:
		v, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("cannot parse %q as int: %w", s, err)
		}
		return int32(v), nil

	case TypeReal:
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("cannot parse %q as real: %w", s, err)
		}
		return v, nil

	case TypeBool:
		switch strings.ToLower(s) {
		case "true", "1", "yes":
			return true, nil
		case "false", "0", "no":
			return false, nil
		default:
			return nil, fmt.Errorf("cannot parse %q as bool", s)
		}

	case TypeDatetime:
		// Try common formats. The no-seconds variants (T15:04 / " 15:04")
		// matter, not redundant with the with-seconds ones above them --
		// verified against Microsoft's own datetime docs before adding:
		// %Y-%m-%dT%H:%M ("2014-05-25T08:20") is its own explicitly
		// documented, valid ISO 8601 format, not an abbreviation. An
		// earlier version of this list lacked them entirely, so a
		// documented-valid literal like datetime(2014-05-25T08:20)
		// failed to parse even after the bare-literal regex (expr.go)
		// was separately fixed to stop rejecting it at the tokenizing
		// stage -- both layers needed the same fix to actually work
		// end to end.
		for _, layout := range []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02T15:04:05",
			"2006-01-02 15:04:05",
			"2006-01-02T15:04",
			"2006-01-02 15:04",
			"2006-01-02",
		} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UnixNano(), nil
			}
		}
		// Try as epoch nanos directly
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			return v, nil
		}
		return nil, fmt.Errorf("cannot parse %q as datetime", s)

	case TypeTimespan:
		// Try KQL timespan format: [d.]hh:mm:ss[.fffffff]
		if ticks, err := parseKQLTimespan(s); err == nil {
			return ticks, nil
		}
		// Try Go duration format: 1h30m, 5m, etc.
		if d, err := time.ParseDuration(s); err == nil {
			return d.Nanoseconds() / 100, nil // Convert to 100ns ticks
		}
		// Try raw ticks
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			return v, nil
		}
		return nil, fmt.Errorf("cannot parse %q as timespan", s)

	default:
		return nil, fmt.Errorf("unsupported type: %s", typ)
	}
}

// FormatValue converts a typed Go value back to a display string.
func FormatValue(v Value, typ KQLType) string {
	if v == nil {
		return ""
	}

	switch typ {
	case TypeString, TypeGUID, TypeDynamic:
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", v)

	case TypeLong:
		if i, ok := v.(int64); ok {
			return strconv.FormatInt(i, 10)
		}
		return fmt.Sprintf("%v", v)

	case TypeInt:
		if i, ok := v.(int32); ok {
			return strconv.FormatInt(int64(i), 10)
		}
		return fmt.Sprintf("%v", v)

	case TypeReal:
		if f, ok := v.(float64); ok {
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
		return fmt.Sprintf("%v", v)

	case TypeBool:
		if b, ok := v.(bool); ok {
			if b {
				return "true"
			}
			return "false"
		}
		return fmt.Sprintf("%v", v)

	case TypeDatetime:
		if nanos, ok := v.(int64); ok {
			t := time.Unix(0, nanos).UTC()
			return t.Format(time.RFC3339Nano)
		}
		return fmt.Sprintf("%v", v)

	case TypeTimespan:
		if ticks, ok := v.(int64); ok {
			// Format as KQL-style timespan: [d.]hh:mm:ss[.fffffff]
			nanos := ticks * 100
			negative := nanos < 0
			if negative {
				nanos = -nanos
			}
			totalSecs := nanos / 1e9
			days := totalSecs / 86400
			hours := (totalSecs % 86400) / 3600
			mins := (totalSecs % 3600) / 60
			secs := totalSecs % 60
			fracNanos := nanos % 1e9

			var s string
			if days > 0 {
				s = fmt.Sprintf("%d.%02d:%02d:%02d", days, hours, mins, secs)
			} else {
				s = fmt.Sprintf("%02d:%02d:%02d", hours, mins, secs)
			}
			if fracNanos > 0 {
				frac := fmt.Sprintf(".%07d", fracNanos/100) // 100ns ticks
				s += strings.TrimRight(frac, "0")
			}
			if negative {
				s = "-" + s
			}
			return s
		}
		return fmt.Sprintf("%v", v)

	default:
		return fmt.Sprintf("%v", v)
	}
}

// parseKQLTimespan parses KQL-format timespan: [d.]hh:mm:ss[.fffffff]
// Examples: "1.02:03:04", "02:03:04", "1.02:03:04.5000000", "00:05:00"
func parseKQLTimespan(s string) (int64, error) {
	negative := false
	if strings.HasPrefix(s, "-") {
		negative = true
		s = s[1:]
	}

	// Must contain at least one ':' to be a KQL timespan
	if !strings.Contains(s, ":") {
		return 0, fmt.Errorf("not a KQL timespan")
	}

	var days, hours, mins, secs int64
	var fracTicks int64

	// Split off fractional seconds
	mainPart := s
	if dotIdx := strings.LastIndex(s, "."); dotIdx > strings.LastIndex(s, ":") {
		mainPart = s[:dotIdx]
		fracStr := s[dotIdx+1:]
		// Pad or truncate to 7 digits (100ns ticks)
		for len(fracStr) < 7 {
			fracStr += "0"
		}
		fracStr = fracStr[:7]
		fracTicks, _ = strconv.ParseInt(fracStr, 10, 64)
	}

	// Split days from time: "1.02:03:04" or "02:03:04"
	timePart := mainPart
	if dotIdx := strings.Index(mainPart, "."); dotIdx >= 0 {
		// Check if the dot is before the first colon (days separator)
		colonIdx := strings.Index(mainPart, ":")
		if dotIdx < colonIdx {
			days, _ = strconv.ParseInt(mainPart[:dotIdx], 10, 64)
			timePart = mainPart[dotIdx+1:]
		}
	}

	// Parse hh:mm:ss
	parts := strings.Split(timePart, ":")
	switch len(parts) {
	case 3:
		hours, _ = strconv.ParseInt(parts[0], 10, 64)
		mins, _ = strconv.ParseInt(parts[1], 10, 64)
		secs, _ = strconv.ParseInt(parts[2], 10, 64)
	case 2:
		hours, _ = strconv.ParseInt(parts[0], 10, 64)
		mins, _ = strconv.ParseInt(parts[1], 10, 64)
	default:
		return 0, fmt.Errorf("invalid time format")
	}

	totalTicks := (days*86400+hours*3600+mins*60+secs)*10000000 + fracTicks
	if negative {
		totalTicks = -totalTicks
	}
	return totalTicks, nil
}

// CompareValues compares two values of the same type.
// Returns -1 (a < b), 0 (a == b), or 1 (a > b).
// Nulls sort last (null > any non-null).
// CompareValues treats a nil (null) value as the smallest possible
// value of any type — verified against real Kusto's own documented
// sort behavior before fixing what was here: "Default for asc is
// nulls first. Default for desc is nulls last" is exactly the
// observable behavior "null is always smallest" produces (ascending
// = smallest-first, so null appears first; descending = smallest-
// last, so null appears last) — the same rule SQL databases use for
// this too ("NULL values are ordered as less than values that are
// not NULL"). An earlier version of this function had both branches
// backwards (a==nil returned 1 -- "a is greater" -- b==nil returned
// -1 -- "a is less"), the exact opposite of this rule. Found live via
// a different model's testing (Kimi), against real data: `sort by
// _TimeReceived desc` put null (legacy, pre-retrofit) rows FIRST
// instead of last, so genuinely newer, real-timestamped rows lost to
// stale ones -- and, separately, okql's own arg_max/arg_min
// (aggregation.go's findArgBestRow) already had the CORRECT
// null-loses-to-non-null behavior, but via its own, separate,
// explicit null-skipping logic that never went through this function
// at all, which is exactly why that path was unaffected by this bug
// while sort/top were not.
//
// Real Kusto also supports an explicit nulls-first/nulls-last
// override syntax (`sort by X asc nulls last`) — not built here at
// all; this fix corrects only the DEFAULT ordering, matching what
// this engine's own sort/top already attempt to implement without any
// override syntax existing yet to correct.
func CompareValues(a, b Value, typ KQLType) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}

	switch typ {
	case TypeString, TypeGUID, TypeDynamic:
		// Defensive: declared type can disagree with the stored value
		// when expression type inference missed (schema says string,
		// value is int64). Fall back to formatted comparison rather
		// than panicking on a hard assertion.
		sa, okA := a.(string)
		sb, okB := b.(string)
		if !okA || !okB {
			sa = fmt.Sprintf("%v", a)
			sb = fmt.Sprintf("%v", b)
		}
		if sa < sb {
			return -1
		}
		if sa > sb {
			return 1
		}
		return 0

	case TypeLong, TypeDatetime, TypeTimespan:
		ia, ib := toInt64(a), toInt64(b)
		if ia < ib {
			return -1
		}
		if ia > ib {
			return 1
		}
		return 0

	case TypeInt:
		ia, ib := toInt32(a), toInt32(b)
		if ia < ib {
			return -1
		}
		if ia > ib {
			return 1
		}
		return 0

	case TypeReal:
		fa, fb := toFloat64(a), toFloat64(b)
		if fa < fb {
			return -1
		}
		if fa > fb {
			return 1
		}
		return 0

	case TypeBool:
		ba, okA := a.(bool)
		bb, okB := b.(bool)
		if !okA || !okB {
			// Same defensive fallback as the string case.
			sa, sb := fmt.Sprintf("%v", a), fmt.Sprintf("%v", b)
			if sa < sb {
				return -1
			}
			if sa > sb {
				return 1
			}
			return 0
		}
		if ba == bb {
			return 0
		}
		if !ba {
			return -1
		}
		return 1

	default:
		return 0
	}
}

// toInt64 coerces a numeric value to int64.
func toInt64(v Value) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int32:
		return int64(x)
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case string:
		// tolong("42") previously returned 0 unconditionally (no string
		// case existed at all) — found live during the backlog pass.
		// Parses as float first so "42.0"-shaped strings also convert,
		// matching real Kusto's tolong/toint leniency; a non-numeric
		// string still yields 0, preserving the existing always-a-value
		// contract every other caller of this widely-used helper relies on.
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return int64(f)
		}
		return 0
	default:
		return 0
	}
}

// toInt32 coerces a numeric value to int32.
func toInt32(v Value) int32 {
	switch x := v.(type) {
	case int32:
		return x
	case int64:
		return int32(x)
	case float64:
		return int32(x)
	case int:
		return int32(x)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return int32(f)
		}
		return 0
	default:
		return 0
	}
}

// toFloat64 coerces a numeric value to float64.
func toFloat64(v Value) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case int32:
		return float64(x)
	case int:
		return float64(x)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return f
		}
		return 0
	default:
		return 0
	}
}

// ToFloat64 is the exported version for use by other packages.
func ToFloat64(v Value) float64 {
	return toFloat64(v)
}

// ToInt64 is the exported version for use by other packages.
func ToInt64(v Value) int64 {
	return toInt64(v)
}
