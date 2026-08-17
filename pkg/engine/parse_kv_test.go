package engine

import "testing"

// parse_kv_test.go — the parse-kv operator (specified-delimiter mode
// only — see ParseKVOp's own doc comment for scope). Every test below
// checks against a real, worked example from real ADX's own docs
// (parse-kv-operator.md), values included, not just "does it run".

// TestParseKVBasic guards the simplest documented shape: a plain
// pair_delimiter/kv_delimiter split with no quoting involved.
func TestParseKVBasic(t *testing.T) {
	result := queryResult(t, `print str="ThreadId:458745723, Machine:Node001"
		| parse-kv str as (ThreadId:long, Machine:string) with (pair_delimiter=',', kv_delimiter=':')`)
	if result.RowCount() != 1 {
		t.Fatalf("expected 1 row, got %d", result.RowCount())
	}
	tid := result.Schema.ColumnIndex("ThreadId")
	mach := result.Schema.ColumnIndex("Machine")
	if got := result.Rows[0][tid]; got != int64(458745723) {
		t.Errorf("ThreadId = %v (%T), want int64(458745723)", got, got)
	}
	if got := result.Rows[0][mach]; got != "Node001" {
		t.Errorf("Machine = %v, want Node001", got)
	}
}

// TestParseKVQuotedKeyAndValue guards real ADX's own primary worked
// example: a bracketed/quoted key name (['event time']) and a
// quote='"'-wrapped value containing the pair delimiter itself
// (space), which a naive unquoted split would incorrectly cut on.
func TestParseKVQuotedKeyAndValue(t *testing.T) {
	result := queryResult(t, `print str='src=10.1.1.123 dst=10.1.1.124 bytes=125 failure="connection aborted" "event time"=2021-01-01T10:00:54'
		| parse-kv str as (['event time']:datetime, src:string, dst:string, bytes:long, failure:string) with (pair_delimiter=' ', kv_delimiter='=', quote='"')
		| project-away str`)
	if result.RowCount() != 1 {
		t.Fatalf("expected 1 row, got %d", result.RowCount())
	}
	srcIdx := result.Schema.ColumnIndex("src")
	dstIdx := result.Schema.ColumnIndex("dst")
	bytesIdx := result.Schema.ColumnIndex("bytes")
	failIdx := result.Schema.ColumnIndex("failure")
	timeIdx := result.Schema.ColumnIndex("event time")

	if got := result.Rows[0][srcIdx]; got != "10.1.1.123" {
		t.Errorf("src = %v, want 10.1.1.123", got)
	}
	if got := result.Rows[0][dstIdx]; got != "10.1.1.124" {
		t.Errorf("dst = %v, want 10.1.1.124", got)
	}
	if got := result.Rows[0][bytesIdx]; got != int64(125) {
		t.Errorf("bytes = %v (%T), want int64(125)", got, got)
	}
	if got := result.Rows[0][failIdx]; got != "connection aborted" {
		t.Errorf("failure = %v, want %q", got, "connection aborted")
	}
	if result.Rows[0][timeIdx] == nil {
		t.Errorf("event time = nil, want a parsed datetime")
	}
}

// TestParseKVEscapedQuoteInValue guards real ADX's own escape='\\'
// worked example: a value that contains a properly escaped quote
// character (\"bye!\" inside a "..."-quoted value) must unescape to a
// literal embedded quote, not terminate the value early or keep the
// backslashes. This is also the regression test for the
// precededByOddBackslashes fix — a doubled '\\' immediately before the
// with(...) clause's own closing paren was previously desyncing
// splitPipe's segment boundaries and swallowing the trailing
// "| project-away str" into the parse-kv segment's own text.
func TestParseKVEscapedQuoteInValue(t *testing.T) {
	result := queryResult(t, `print str='src=10.1.1.123 dst=10.1.1.124 bytes=125 failure="the remote host sent \"bye!\"" time=2021-01-01T10:00:54'
		| parse-kv str as (['time']:datetime, src:string, dst:string, bytes:long, failure:string) with (pair_delimiter=' ', kv_delimiter='=', quote='"', escape='\\')
		| project-away str`)
	if result.RowCount() != 1 {
		t.Fatalf("expected 1 row, got %d", result.RowCount())
	}
	failIdx := result.Schema.ColumnIndex("failure")
	want := `the remote host sent "bye!"`
	if got := result.Rows[0][failIdx]; got != want {
		t.Errorf("failure = %q, want %q", got, want)
	}
}

// TestParseKVGreedy guards real ADX's own greedy=true worked example:
// without greedy mode, a value containing an unquoted pair_delimiter
// (a space inside "John Doe") is incorrectly truncated at the first
// space; greedy mode correctly extends the value up to the next
// recognized declared key.
func TestParseKVGreedy(t *testing.T) {
	nonGreedy := queryResult(t, `print str='name=John Doe phone=555 5555 city=New York'
		| parse-kv str as (name:string, phone:string, city:string) with (pair_delimiter=' ', kv_delimiter='=')`)
	greedy := queryResult(t, `print str='name=John Doe phone=555 5555 city=New York'
		| parse-kv str as (name:string, phone:string, city:string) with (pair_delimiter=' ', kv_delimiter='=', greedy=true)`)

	nameIdx := nonGreedy.Schema.ColumnIndex("name")
	if got := nonGreedy.Rows[0][nameIdx]; got != "John" {
		t.Errorf("non-greedy name = %v, want John (truncated at first space)", got)
	}

	nameIdx = greedy.Schema.ColumnIndex("name")
	phoneIdx := greedy.Schema.ColumnIndex("phone")
	cityIdx := greedy.Schema.ColumnIndex("city")
	if got := greedy.Rows[0][nameIdx]; got != "John Doe" {
		t.Errorf("greedy name = %v, want %q", got, "John Doe")
	}
	if got := greedy.Rows[0][phoneIdx]; got != "555 5555" {
		t.Errorf("greedy phone = %v, want %q", got, "555 5555")
	}
	if got := greedy.Rows[0][cityIdx]; got != "New York" {
		t.Errorf("greedy city = %v, want %q", got, "New York")
	}
}

// TestParseKVMissingKeyLeftNull confirms a declared key absent from the
// source string leaves that output column null rather than erroring —
// matching real ADX's own documented rule ("the corresponding column
// value is either null or an empty string").
func TestParseKVMissingKeyLeftNull(t *testing.T) {
	result := queryResult(t, `print str='src=1.2.3.4'
		| parse-kv str as (src:string, dst:string) with (pair_delimiter=' ', kv_delimiter='=')`)
	srcIdx := result.Schema.ColumnIndex("src")
	dstIdx := result.Schema.ColumnIndex("dst")
	if got := result.Rows[0][srcIdx]; got != "1.2.3.4" {
		t.Errorf("src = %v, want 1.2.3.4", got)
	}
	if got := result.Rows[0][dstIdx]; got != nil {
		t.Errorf("dst = %v, want nil (key absent from source)", got)
	}
}

// TestParseKVFirstOccurrenceWins confirms a repeated key in the source
// string keeps only its first value — "The first appearance of a key
// is extracted, and subsequent values are ignored" per real docs.
func TestParseKVFirstOccurrenceWins(t *testing.T) {
	result := queryResult(t, `print str='a=1 a=2 a=3'
		| parse-kv str as (a:string) with (pair_delimiter=' ', kv_delimiter='=')`)
	aIdx := result.Schema.ColumnIndex("a")
	if got := result.Rows[0][aIdx]; got != "1" {
		t.Errorf("a = %v, want 1 (first occurrence)", got)
	}
}

// TestParseKVRegexModeUnimplemented confirms regex=... mode is
// rejected with a clear error rather than silently mis-parsed, per
// ParseKVOp's own documented scope.
func TestParseKVRegexModeUnimplemented(t *testing.T) {
	queryError(t, `print str='x' | parse-kv str as (a:string) with (regex='(?P<a>.*)')`)
}

// TestParseKVNonSpecifiedDelimiterModeUnimplemented confirms the
// "non-specified delimiter" mode (no pair_delimiter/kv_delimiter given)
// is rejected with a clear error, per ParseKVOp's own documented scope.
func TestParseKVNonSpecifiedDelimiterModeUnimplemented(t *testing.T) {
	queryError(t, `print str='a:1,b:2' | parse-kv str as (a:string, b:string) with (quote='"')`)
}

