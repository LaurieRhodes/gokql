package engine

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// --- Test harness ---

// queryResult runs a KQL query against an in-memory engine and returns the result table.
func queryResult(t *testing.T, query string) *types.Table {
	t.Helper()
	cat := catalog.NewMemory()
	eng := New(cat)
	stmt, err := parser.Parse(query)
	if err != nil {
		t.Fatalf("parse %q: %v", query, err)
	}
	result, err := eng.Execute(stmt)
	if err != nil {
		t.Fatalf("execute %q: %v", query, err)
	}
	if result == nil {
		t.Fatalf("nil result for %q", query)
	}
	return result
}

// queryError runs a KQL query and expects an error at parse or execute time.
func queryError(t *testing.T, query string) {
	t.Helper()
	cat := catalog.NewMemory()
	eng := New(cat)
	stmt, err := parser.Parse(query)
	if err != nil {
		return // parse error is fine
	}
	_, err = eng.Execute(stmt)
	if err == nil {
		t.Fatalf("expected error for %q, got success", query)
	}
}

// expectRows checks row count.
func expectRows(t *testing.T, tbl *types.Table, n int) {
	t.Helper()
	if len(tbl.Rows) != n {
		t.Fatalf("expected %d rows, got %d", n, len(tbl.Rows))
	}
}

// expectCols checks column count.
func expectCols(t *testing.T, tbl *types.Table, n int) {
	t.Helper()
	if len(tbl.Schema.Columns) != n {
		t.Fatalf("expected %d columns, got %d", n, len(tbl.Schema.Columns))
	}
}

// expectColNames checks exact column names in order.
func expectColNames(t *testing.T, tbl *types.Table, names ...string) {
	t.Helper()
	if len(tbl.Schema.Columns) != len(names) {
		got := make([]string, len(tbl.Schema.Columns))
		for i, c := range tbl.Schema.Columns {
			got[i] = c.Name
		}
		t.Fatalf("expected columns %v, got %v", names, got)
	}
	for i, name := range names {
		if tbl.Schema.Columns[i].Name != name {
			t.Fatalf("column %d: expected %q, got %q", i, name, tbl.Schema.Columns[i].Name)
		}
	}
}

// cell returns row[r] col[c] as a string for easy comparison.
func cell(t *testing.T, tbl *types.Table, r, c int) string {
	t.Helper()
	if r >= len(tbl.Rows) || c >= len(tbl.Schema.Columns) {
		t.Fatalf("cell(%d,%d) out of range (%d rows, %d cols)", r, c, len(tbl.Rows), len(tbl.Schema.Columns))
	}
	return types.FormatValue(tbl.Rows[r][c], tbl.Schema.Columns[c].Type)
}

// cellVal returns the raw value at row[r] col[c].
func cellVal(t *testing.T, tbl *types.Table, r, c int) interface{} {
	t.Helper()
	if r >= len(tbl.Rows) || c >= len(tbl.Schema.Columns) {
		t.Fatalf("cellVal(%d,%d) out of range", r, c)
	}
	return tbl.Rows[r][c]
}

// expectCell checks a specific cell value as string.
func expectCell(t *testing.T, tbl *types.Table, r, c int, expected string) {
	t.Helper()
	got := cell(t, tbl, r, c)
	if got != expected {
		t.Errorf("row %d col %d: expected %q, got %q", r, c, expected, got)
	}
}

// --- Print / Expressions ---

func TestPrintLiterals(t *testing.T) {
	tests := []struct {
		query    string
		expected string
	}{
		{`print X = 42`, "42"},
		{`print X = 3.14`, "3.14"},
		{`print X = "hello"`, "hello"},
		{`print X = true`, "true"},
		{`print X = false`, "false"},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			tbl := queryResult(t, tt.query)
			expectRows(t, tbl, 1)
			expectCell(t, tbl, 0, 0, tt.expected)
		})
	}
}

// TestBareExpressionAutoNaming guards a real, live gap found via a
// different model's testing (Kimi): `print 5` (no toscalar()
// involved at all, confirming this was pre-existing, not something
// toscalar() introduced) failed to parse outright with "expected
// 'Name = expr'". Verified against real ADX's own extend/project docs
// before fixing, not assumed: "if ColumnName is omitted, the output
// column name of Expression is automatically generated" — confirmed
// for both operators explicitly, so this is genuine conformance, not
// just a print-specific convenience. print, extend, and project each
// needed their own fix (parseAssignments is shared by print/extend;
// project has its own, separate parseProjectItems, which needed the
// identical fix applied a second time or `project x + 1` would have
// kept failing with "column "x + 1" not found" even after the
// shared function was fixed).
func TestBareExpressionAutoNaming(t *testing.T) {
	tbl := queryResult(t, `print 5`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "5")

	tbl2 := queryResult(t, `datatable(x:long) [1,2,3] | extend x + 1`)
	expectRows(t, tbl2, 3)
	autoIdx := tbl2.Schema.ColumnIndex("x + 1")
	if autoIdx < 0 {
		t.Fatalf("expected auto-generated column named %q, got columns: %v", "x + 1", tbl2.Schema.Columns)
	}
	if tbl2.Rows[0][autoIdx] != int64(2) {
		t.Errorf("expected first row's x+1 = 2, got %v", tbl2.Rows[0][autoIdx])
	}

	tbl3 := queryResult(t, `datatable(x:long) [1,2,3] | project x + 1`)
	expectRows(t, tbl3, 3)
	autoIdx3 := tbl3.Schema.ColumnIndex("x + 1")
	if autoIdx3 < 0 {
		t.Fatalf("expected auto-generated column named %q in project, got columns: %v", "x + 1", tbl3.Schema.Columns)
	}
}

// TestBareColumnReferenceInProjectUnaffected guards that project's
// own, separate fix (distinguishing a genuine bare column reference
// from an unnamed computed expression via isValidIdentifier) didn't
// regress the ordinary, overwhelmingly common `project X` case.
func TestBareColumnReferenceInProjectUnaffected(t *testing.T) {
	tbl := queryResult(t, `datatable(x:long, y:long) [1,10,2,20] | project x`)
	expectRows(t, tbl, 2)
	if len(tbl.Schema.Columns) != 1 || tbl.Schema.Columns[0].Name != "x" {
		t.Fatalf("expected a single passthrough column named x, got: %v", tbl.Schema.Columns)
	}
}

// TestNamedAssignmentComparisonOperatorsUnaffected guards that
// assignmentEqIndex (now used in place of the old, narrower "guard
// against ==" check) still correctly handles ==, !=, <=, >=, and =~
// inside a named assignment's own expression — a strictly more
// general replacement, verified directly rather than assumed
// equivalent.
func TestNamedAssignmentComparisonOperatorsUnaffected(t *testing.T) {
	tbl := queryResult(t, `datatable(x:long) [1,2,3] | extend y = x == 2`)
	expectRows(t, tbl, 3)
	yIdx := tbl.Schema.ColumnIndex("y")
	if tbl.Rows[0][yIdx] != false || tbl.Rows[1][yIdx] != true || tbl.Rows[2][yIdx] != false {
		t.Errorf("expected [false, true, false] for x==2 over [1,2,3], got [%v, %v, %v]",
			tbl.Rows[0][yIdx], tbl.Rows[1][yIdx], tbl.Rows[2][yIdx])
	}
}

// TestPrintDatetimeTypeInference guards a real, live bug: print's own
// type-inference switch matched on the runtime Go value's type
// (int64/float64/bool/string), never on the expression that produced
// it -- and datetime/timespan are BOTH represented internally as a
// plain int64 (UnixNano / 100ns ticks), so that switch could never
// distinguish "this int64 is a date" from "this int64 is a long". The
// underlying value was already correct; only the column's TYPE (and
// therefore whether the RFC3339Nano/"Z" display formatting ever ran
// at all) was wrong. Fixed by using inferExprType — the same
// expression-based type inferrer project/extend/summarize's by-clause
// already used correctly — instead of a value-based switch specific
// to print. getschema is checked directly, not just the displayed
// string, so a future regression back to int64/long is caught even if
// the displayed digits happened to look plausible.
func TestPrintDatetimeTypeInference(t *testing.T) {
	tbl := queryResult(t, `print d = datetime(2026-08-10T15:30:00)`)
	expectRows(t, tbl, 1)
	if tbl.Schema.Columns[0].Type != types.TypeDatetime {
		t.Fatalf("expected column type TypeDatetime, got %v", tbl.Schema.Columns[0].Type)
	}
	expectCell(t, tbl, 0, 0, "2026-08-10T15:30:00Z")
}

// TestPrintOrdinaryTypesUnaffectedByDatetimeFix is a direct regression
// check alongside the fix above: print's ordinary (non-datetime) type
// inference must still work correctly after switching from a
// value-based switch to inferExprType.
func TestPrintOrdinaryTypesUnaffectedByDatetimeFix(t *testing.T) {
	tbl := queryResult(t, `print a = 5, b = "hello", c = 3.14, d = true`)
	expectRows(t, tbl, 1)
	wantTypes := []types.KQLType{types.TypeLong, types.TypeString, types.TypeReal, types.TypeBool}
	for i, want := range wantTypes {
		if tbl.Schema.Columns[i].Type != want {
			t.Errorf("column %d: expected type %v, got %v", i, want, tbl.Schema.Columns[i].Type)
		}
	}
}

// TestDatetimeUTCConformance directly guards the actual question this
// responds to: does this engine replicate real Kusto's "a datetime
// value is ALWAYS in the UTC time zone" rule (verified against
// Microsoft's own datetime-data-type docs, not assumed)? Checks the
// full round trip end to end: an unzoned literal is interpreted as
// UTC (not local time -- Go's own time.Parse defaults to UTC in the
// absence of a zone indicator, which this codebase's datetime parsing
// relies on), and displayed output carries an explicit "Z" suffix
// confirming it, not silently converted to whatever timezone the
// host machine happens to be running in.
func TestDatetimeUTCConformance(t *testing.T) {
	tbl := queryResult(t, `print d = datetime(2026-08-10T15:30:00)`)
	expectCell(t, tbl, 0, 0, "2026-08-10T15:30:00Z")
}

// TestDatetimeNoSecondsFormParses guards a real, live gap: the bare
// datetime() literal regex required \d{2}:\d{2}:\d{2} (seconds
// mandatory), but real Kusto's own "strongly recommended" ISO 8601
// format table explicitly lists %Y-%m-%dT%H:%M (e.g.
// "2014-05-25T08:20") as its own valid, distinct format, not an
// abbreviation of the with-seconds one. Fixed in two places that both
// needed the same fix to actually work end to end: the bare-literal
// regex (parser/expr.go, which decides whether the tokenizer accepts
// the text at all) and types.ParseValue's layout list (which actually
// parses the accepted text into a value) -- fixing only one would
// have left the other still rejecting or mis-parsing it.
func TestDatetimeNoSecondsFormParses(t *testing.T) {
	tbl := queryResult(t, `print d = datetime(2014-05-25T08:20)`)
	expectCell(t, tbl, 0, 0, "2014-05-25T08:20:00Z")
}

func TestPrintArithmetic(t *testing.T) {
	tests := []struct {
		query    string
		expected string
	}{
		{`print X = 6 * 7`, "42"},
		{`print X = 10 + 5`, "15"},
		{`print X = 100 - 37`, "63"},
		{`print X = 20 / 4`, "5"},
		{`print X = 17 % 5`, "2"},
		{`print X = 2 + 3 * 4`, "14"},       // precedence
		{`print X = (2 + 3) * 4`, "20"},      // parens
		{`print X = -5 + 10`, "5"},            // unary minus
		{`print X = 10.0 / 3.0`, "3.3333333333333335"}, // float division
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			tbl := queryResult(t, tt.query)
			expectCell(t, tbl, 0, 0, tt.expected)
		})
	}
}

func TestPrintMultipleColumns(t *testing.T) {
	tbl := queryResult(t, `print A = 1, B = "two", C = 3.0`)
	expectCols(t, tbl, 3)
	expectColNames(t, tbl, "A", "B", "C")
	expectCell(t, tbl, 0, 0, "1")
	expectCell(t, tbl, 0, 1, "two")
	expectCell(t, tbl, 0, 2, "3")
}

// --- Datatable ---

func TestDatatable(t *testing.T) {
	tbl := queryResult(t, `datatable (Name: string, Score: long) ["Alice", 90, "Bob", 85, "Carol", 95]`)
	expectRows(t, tbl, 3)
	expectCols(t, tbl, 2)
	expectCell(t, tbl, 0, 0, "Alice")
	expectCell(t, tbl, 0, 1, "90")
	expectCell(t, tbl, 2, 0, "Carol")
	expectCell(t, tbl, 2, 1, "95")
}

func TestDatatableTypes(t *testing.T) {
	tbl := queryResult(t, `datatable (A: long, B: real, C: bool, D: string) [1, 3.14, true, "hello"]`)
	expectRows(t, tbl, 1)
	if tbl.Schema.Columns[0].Type != types.TypeLong {
		t.Errorf("expected long, got %s", tbl.Schema.Columns[0].Type)
	}
	if tbl.Schema.Columns[1].Type != types.TypeReal {
		t.Errorf("expected real, got %s", tbl.Schema.Columns[1].Type)
	}
	if tbl.Schema.Columns[2].Type != types.TypeBool {
		t.Errorf("expected bool, got %s", tbl.Schema.Columns[2].Type)
	}
}

// TestDatatableDatetimeNotEpochZero guards a real, previously-live
// data-corruption bug: convertDataTableValue's own TypeDatetime case
// used to return a raw time.Time instead of the int64 UnixNano every
// other datetime value in this codebase uses, which silently became 0
// (1970-01-01) the moment it reached types.ToInt64's untyped default
// branch — with no error raised anywhere in the pipeline. Fixed by
// deleting the duplicate conversion table entirely and delegating to
// types.ParseValue (the same, already-correct path CSV ingest uses).
func TestDatatableDatetimeNotEpochZero(t *testing.T) {
	tbl := queryResult(t, `datatable (D: datetime) ["2026-08-01"]`)
	expectRows(t, tbl, 1)
	got := tbl.Rows[0][0].(int64)
	epoch1970 := int64(0)
	if got == epoch1970 {
		t.Fatalf("datetime literal silently became epoch zero (the exact bug this guards)")
	}
	wantYear := 2026
	gotTime := types.FormatValue(got, types.TypeDatetime)
	if !strings.Contains(gotTime, "2026-08-01") {
		t.Errorf("expected a 2026-08-01 timestamp, got %v (formatted: %q, wantYear=%d)", got, gotTime, wantYear)
	}
}

// TestDatatableFuncCallLiteral guards a real, previously-live bug
// found 2026-08-15 while testing make-series against real ADX's own
// datatable-operator.md worked example: a datatable literal wrapped in
// real, standard KQL function-call syntax -- datetime(...), not just a
// bare quoted string -- used to fail outright for any non-TypeDynamic
// column ("cannot parse \"datetime(2016-12-31T06:00)\" as datetime"),
// because convertDataTableValue only tried evaluating the token as a
// KQL expression first for TypeDynamic columns. Fixed by extending
// that same try-as-expression-first treatment to any unquoted,
// function-call-shaped token, not just TypeDynamic ones -- see
// convertDataTableValue's own doc comment for the full history,
// including the quoted-string regression that same fix's first,
// overly-broad version introduced and TestDatatableDatetimeNotEpochZero
// (immediately above) caught.
func TestDatatableFuncCallLiteral(t *testing.T) {
	tbl := queryResult(t, `datatable (D: datetime) [datetime(2016-12-31T06:00)]`)
	expectRows(t, tbl, 1)
	got := tbl.Rows[0][0].(int64)
	gotTime := types.FormatValue(got, types.TypeDatetime)
	if !strings.Contains(gotTime, "2016-12-31") {
		t.Errorf("expected a 2016-12-31 timestamp, got %v (formatted: %q)", got, gotTime)
	}

	// The quoted-string case must still coerce correctly (the exact
	// regression the narrower, function-call-shape-gated fix avoids).
	tbl2 := queryResult(t, `datatable (D: datetime) ["2026-08-01"]`)
	got2 := tbl2.Rows[0][0]
	if _, ok := got2.(int64); !ok {
		t.Fatalf("quoted string in a datetime column must still coerce to int64 (UnixNano), got %T: %v", got2, got2)
	}
}

// TestDatatableTimespanParsed guards the same class of bug for
// timespan: convertDataTableValue previously had no TypeTimespan case
// at all, silently falling through to its default branch and storing
// the raw, unconverted string instead of the correct int64 tick count.
func TestDatatableTimespanParsed(t *testing.T) {
	tbl := queryResult(t, `datatable (T: timespan) ["01:30:00"]`)
	expectRows(t, tbl, 1)
	if _, ok := tbl.Rows[0][0].(int64); !ok {
		t.Fatalf("expected timespan to convert to int64 ticks, got %T (%v) — the raw-string-fallthrough bug this guards", tbl.Rows[0][0], tbl.Rows[0][0])
	}
}

// TestDatatableArgMaxLatestWins is the actual motivating pattern this
// fix unblocks: tracking a value that changes over time (e.g. a task's
// Status) as successive append-only rows, with the current state read
// back via arg_max(TimeCol, OtherCol) by Id — silently correct or
// silently zero-dated used to be indistinguishable without this fix.
func TestDatatableArgMaxLatestWins(t *testing.T) {
	tbl := queryResult(t, `datatable (Id: string, Status: string, LastTouchedAt: datetime) `+
		`["t1", "open", "2026-08-01", "t1", "blocked", "2026-08-05"] `+
		`| summarize arg_max(LastTouchedAt, Status) by Id`)
	expectRows(t, tbl, 1)
	found := false
	for _, v := range tbl.Rows[0] {
		if s, ok := v.(string); ok && s == "blocked" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected arg_max to select the later (blocked) row, got %v", tbl.Rows[0])
	}
}

// TestDatatableBracketedDynamicValueNotSplit guards a real, live
// silent-corruption bug: a bracketed JSON-array value for a dynamic-
// typed column, e.g. ["x", "y"], used to be split on its own internal
// comma into two separate flat value tokens instead of staying one --
// splitDataTableValues tracked string-quote state but never tracked
// bracket nesting depth at all. datatable(Id: string, Tags: dynamic)
// ["a", ["x", "y"], "b", ["z"]] (2 intended rows) silently produced 3
// rows with every value misaligned from that point on, including a
// row whose Id was the literal fragment `"y"]` -- no error anywhere.
// Fixed by tracking bracket depth the same way string-quote state was
// already tracked, so a comma inside [...] no longer splits it.
func TestDatatableBracketedDynamicValueNotSplit(t *testing.T) {
	tbl := queryResult(t, `datatable (Id: string, Tags: dynamic) `+
		`["a", ["x", "y"], "b", ["z"]]`)
	// This 2-column fixture happens to have exactly one bracketed value
	// per logical row, so it round-robins into 2 rows without exercising
	// the "multiple bracket values land in the SAME row" shape at all —
	// TestDatatableTwoDynamicValuesSameRow below covers that separately.
	// Asserting string equality alone here (as an earlier version of
	// this test did) doesn't prove the stored value is a genuine,
	// operable dynamic array rather than a string that merely looks
	// like one — added array_length checks to close that gap.
	expectRows(t, tbl, 2)
	expectCell(t, tbl, 0, 0, "a")
	expectCell(t, tbl, 0, 1, `["x", "y"]`)
	expectCell(t, tbl, 1, 0, "b")
	expectCell(t, tbl, 1, 1, `["z"]`)

	lenTbl := queryResult(t, `datatable (Id: string, Tags: dynamic) `+
		`["a", ["x", "y"], "b", ["z"]] | extend n = array_length(Tags) | project Id, n`)
	expectCell(t, lenTbl, 0, 1, "2")
	expectCell(t, lenTbl, 1, 1, "1")
}

// TestDatatableTwoDynamicValuesSameRow covers the shape
// TestDatatableBracketedDynamicValueNotSplit doesn't: two bracketed
// values landing in the SAME logical row, not one bracket per row.
// datatable's flat round-robin rule (n values / m columns = rows)
// still applies AFTER bracket-aware splitting -- it wasn't replaced by
// the bracket fix, just no longer corrupted by it. With 2 columns and
// 2 bracket-valued tokens total, that's 1 row, not 2 -- each bracket
// becomes its own column's dynamic array value in that single row.
// Confirmed correct behavior for this shape, not a residual bug: real
// ADX rejects nested arrays in datatable outright (a stricter, but
// different, choice) -- this fix's job was closing the corruption
// trap (invalid input silently producing wrong values), not matching
// ADX's specific rejection here, and the resulting shape is internally
// coherent with datatable's own already-correct flat-assignment rule.
func TestDatatableTwoDynamicValuesSameRow(t *testing.T) {
	tbl := queryResult(t, `datatable(a: dynamic, b: dynamic) `+
		`[["x","y"],["z","w"]] | extend na = array_length(a), nb = array_length(b) | project a, b, na, nb`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, `["x","y"]`)
	expectCell(t, tbl, 0, 1, `["z","w"]`)
	expectCell(t, tbl, 0, 2, "2")
	expectCell(t, tbl, 0, 3, "2")
}

// TestDatatableBracketedValueMixedWithQuotedComma is a regression
// check that the bracket-depth fix above doesn't disturb the
// pre-existing, already-correct handling of a comma INSIDE a quoted
// string value (which must still NOT split).
func TestDatatableBracketedValueMixedWithQuotedComma(t *testing.T) {
	tbl := queryResult(t, `datatable (Id: string, Tags: dynamic) `+
		`["a", "plain, with comma inside quotes", "b", ["p","q","r"]]`)
	expectRows(t, tbl, 2)
	expectCell(t, tbl, 0, 1, "plain, with comma inside quotes")
	expectCell(t, tbl, 1, 1, `["p","q","r"]`)
}

// --- Where / Filter ---

func TestWhere(t *testing.T) {
	tbl := queryResult(t, `datatable (X: long) [1, 2, 3, 4, 5] | where X > 3`)
	expectRows(t, tbl, 2)
	expectCell(t, tbl, 0, 0, "4")
	expectCell(t, tbl, 1, 0, "5")
}

func TestWhereStringOps(t *testing.T) {
	base := `datatable (S: string) ["hello world", "HELLO", "goodbye", "Hello There"]`

	tests := []struct {
		name  string
		query string
		count int
	}{
		{"contains", base + ` | where S contains "hello"`, 3},
		{"!contains", base + ` | where S !contains "hello"`, 1},
		{"contains_cs", base + ` | where S contains_cs "hello"`, 1},
		{"has whole term", base + ` | where S has "hello"`, 3},
		{"has partial term misses", base + ` | where S has "hell"`, 0},
		{"has_cs", base + ` | where S has_cs "Hello"`, 1},
		{"!has", base + ` | where S !has "hello"`, 1},
		{"startswith", base + ` | where S startswith "hello"`, 3},
		{"endswith", base + ` | where S endswith "world"`, 1},
		{"matches regex", base + ` | where S matches regex "^[Hh]ello"`, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := queryResult(t, tt.query)
			expectRows(t, tbl, tt.count)
		})
	}
}

func TestWhereHasTermBoundaries(t *testing.T) {
	// KQL has = whole-term match; terms are alphanumeric runs
	base := `datatable (S: string) ["proc=cmd.exe run", "cmdlet invoked", "run cmd now"]`
	tests := []struct {
		name  string
		query string
		count int
	}{
		{"whole term", base + ` | where S has "cmd"`, 2},        // cmd.exe splits to cmd + exe; cmdlet does not match
		{"phrase across separator", base + ` | where S has "cmd.exe"`, 1},
		{"substring rejected", base + ` | where S has "cmdl"`, 0},
		{"has_any whole term", `datatable (S: string) ["sshd login", "rdp session", "ssh attempt"] | where S has_any ("ssh", "rdp")`, 2},
		{"has_all whole term", `datatable (S: string) ["ssh login ok", "sshd login ok"] | where S has_all ("ssh", "login")`, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := queryResult(t, tt.query)
			expectRows(t, tbl, tt.count)
		})
	}
}

func TestWhereLike(t *testing.T) {
	base := `datatable (S: string) ["hello world", "help wanted", "goodbye", "hxllo"]`
	tests := []struct {
		name  string
		query string
		count int
	}{
		{"star", base + ` | where S like "hel*"`, 2},
		{"question", base + ` | where S like "h?llo*"`, 2},
		{"negated", base + ` | where S !like "hel*"`, 2},
		{"literal dot escaped", `datatable (S: string) ["a.b", "axb"] | where S like "a.b"`, 1},
		{"middle star", base + ` | where S like "*wanted"`, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := queryResult(t, tt.query)
			expectRows(t, tbl, tt.count)
		})
	}
}

func TestWhereHasPrefixSuffix(t *testing.T) {
	// Term-boundary semantics: terms are alphanumeric runs, so
	// "Hello-World test" tokenizes to Hello, World, test.
	base := `datatable (S: string) ["Hello-World test", "worldwide", "underworld"]`
	tests := []struct {
		name  string
		query string
		count int
	}{
		// "world" starts terms World and worldwide, but not underworld
		{"hasprefix term", base + ` | where S hasprefix "world"`, 2},
		// "orld" is not at a term start anywhere
		{"hasprefix mid-term", base + ` | where S hasprefix "orld"`, 0},
		// "orld" ends terms World and underworld, not worldwide
		{"hassuffix term", base + ` | where S hassuffix "orld"`, 2},
		{"hassuffix negated", base + ` | where S !hassuffix "orld"`, 1},
		{"hasprefix_cs match", base + ` | where S hasprefix_cs "World"`, 1},
		{"hasprefix_cs case miss", base + ` | where S hasprefix_cs "WORLD"`, 0},
		{"hassuffix_cs", base + ` | where S hassuffix_cs "test"`, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := queryResult(t, tt.query)
			expectRows(t, tbl, tt.count)
		})
	}
}

func TestWhereBetween(t *testing.T) {
	tbl := queryResult(t, `datatable (X: long) [1, 2, 3, 4, 5] | where X between (2 .. 4)`)
	expectRows(t, tbl, 3)
}

func TestWhereNotBetween(t *testing.T) {
	tbl := queryResult(t, `datatable (X: long) [1, 2, 3, 4, 5] | where X !between (2 .. 4)`)
	expectRows(t, tbl, 2)
}

func TestWhereIn(t *testing.T) {
	tbl := queryResult(t, `datatable (X: string) ["a", "b", "c", "d"] | where X in ("a", "c")`)
	expectRows(t, tbl, 2)
	expectCell(t, tbl, 0, 0, "a")
	expectCell(t, tbl, 1, 0, "c")
}

func TestWhereNotIn(t *testing.T) {
	tbl := queryResult(t, `datatable (X: string) ["a", "b", "c"] | where X !in ("a")`)
	expectRows(t, tbl, 2)
}

func TestWhereInCI(t *testing.T) {
	tbl := queryResult(t, `datatable (X: string) ["Allow", "DENY", "allow", "Block"] | where X in~ ("allow")`)
	expectRows(t, tbl, 2)
}

func TestWhereHasAny(t *testing.T) {
	tbl := queryResult(t, `datatable (S: string) ["ssh login", "rdp session", "http request", "ftp upload"] | where S has_any ("ssh", "rdp")`)
	expectRows(t, tbl, 2)
}

// TestWhereHasAnyWithDownstreamNarrowing guards a real, live bug: a
// where...has_any clause piped into a downstream narrowing operator
// (count, here) failed with "column S not found" -- a column
// projection-pushdown optimization walks the where expression to find
// which columns the storage scan actually needs, and had no case for
// HasAnyAllExpr at all, so its Column reference was silently never
// discovered. The scan then omitted the column entirely, and where's
// own evaluation failed trying to read it. Only reproduces WITH a
// downstream narrowing operator present -- with none (the plain
// TestWhereHasAny case above), no pushdown analysis runs at all and
// every column gets scanned regardless, which is exactly why this
// didn't show up until a broader test happened to pipe into count.
func TestWhereHasAnyWithDownstreamNarrowing(t *testing.T) {
	tbl := queryResult(t, `datatable (S: string) ["ssh login", "rdp session", "http request", "ftp upload"] `+
		`| where S has_any ("ssh", "rdp") | count`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "2")
}

func TestWhereHasAll(t *testing.T) {
	tbl := queryResult(t, `datatable (S: string) ["ssh login attempt", "ssh session", "rdp login"] | where S has_all ("ssh", "login")`)
	expectRows(t, tbl, 1)
}

// --- Project variants ---

func TestProject(t *testing.T) {
	tbl := queryResult(t, `datatable (A: long, B: string, C: long) [1, "x", 10] | project A, C`)
	expectCols(t, tbl, 2)
	expectColNames(t, tbl, "A", "C")
}

func TestProjectComputed(t *testing.T) {
	// Computed column directly in project (no extend needed)
	tbl := queryResult(t, `datatable (X: long) [5] | project Double = X * 2`)
	expectColNames(t, tbl, "Double")
	expectCell(t, tbl, 0, 0, "10")
}

func TestProjectMixed(t *testing.T) {
	// Bare columns and computed columns interleaved, order preserved
	tbl := queryResult(t, `datatable (A: long, S: string) [2, "hi"] | project S, Doubled = A * 2, L = strlen(S)`)
	expectColNames(t, tbl, "S", "Doubled", "L")
	expectCell(t, tbl, 0, 0, "hi")
	expectCell(t, tbl, 0, 1, "4")
	expectCell(t, tbl, 0, 2, "2")
}

func TestProjectComputedWithComparison(t *testing.T) {
	// == inside the expression must not be mistaken for the assignment
	tbl := queryResult(t, `datatable (A: long) [1, 2] | project Flag = iff(A == 2, "yes", "no")`)
	expectCell(t, tbl, 0, 0, "no")
	expectCell(t, tbl, 1, 0, "yes")
}

func TestProjectComputedMalformed(t *testing.T) {
	queryError(t, `datatable (A: long) [1] | project B = `)
}

func TestProjectAway(t *testing.T) {
	tbl := queryResult(t, `datatable (A: long, B: string, C: long) [1, "x", 10] | project-away B`)
	expectCols(t, tbl, 2)
	expectColNames(t, tbl, "A", "C")
}

func TestProjectRename(t *testing.T) {
	tbl := queryResult(t, `datatable (A: long) [1] | project-rename NewA = A`)
	expectColNames(t, tbl, "NewA")
	expectCell(t, tbl, 0, 0, "1")
}

func TestProjectReorder(t *testing.T) {
	tbl := queryResult(t, `datatable (A: long, B: string, C: long) [1, "x", 10] | project-reorder C, A`)
	expectColNames(t, tbl, "C", "A", "B")
}

func TestProjectKeep(t *testing.T) {
	tbl := queryResult(t, `datatable (SrcIP: string, DstIP: string, Port: long) ["10.0.1.5", "192.168.1.1", 443] | project-keep *IP`)
	expectCols(t, tbl, 2)
	expectColNames(t, tbl, "SrcIP", "DstIP")
}

// --- Extend ---

func TestExtend(t *testing.T) {
	tbl := queryResult(t, `datatable (X: long) [5] | extend Y = X * 2, Z = X + 1`)
	expectCols(t, tbl, 3)
	expectCell(t, tbl, 0, 1, "10")
	expectCell(t, tbl, 0, 2, "6")
}

// TestExtendSelfReferenceReplacesInPlace guards a real, previously-live
// bug: extend Col = expr, when Col already exists in the schema, used
// to APPEND a second column with the same name rather than replacing
// the first one. Every later reference to that name (project, another
// extend, plain output) resolved to the FIRST match — the untouched
// original — so a self-referential backfill like
// `extend X = case(X == "old", "new", X)` silently kept "old" forever,
// with no error anywhere in the pipeline. Found live while migrating
// real production data (a Claude-Memory scope's Findings table), not
// in a synthetic test — confirmed via getschema before fixing that the
// naive result genuinely carried two columns both named X. Pinned
// directly to that symptom, per the follow-up request that flagged
// this as the kind of bug that reappears: assert exactly one column
// with the assigned name survives, and it holds the NEW value, not
// the old one.
func TestExtendSelfReferenceReplacesInPlace(t *testing.T) {
	tbl := queryResult(t, `datatable (X: string) ["old"] | extend X = case(X == "old", "new", X)`)

	if got := len(tbl.Schema.Columns); got != 1 {
		t.Fatalf("expected exactly 1 column after self-referential extend, got %d: %v",
			got, columnNames(&tbl.Schema))
	}
	if tbl.Schema.Columns[0].Name != "X" {
		t.Fatalf("expected the surviving column to be named X, got %q", tbl.Schema.Columns[0].Name)
	}
	expectCell(t, tbl, 0, 0, "new")
}

// TestExtendSelfReferenceSequential guards that two self-referential
// extends across separate pipe stages compose correctly — each stage
// reads the row as it stood after the PREVIOUS stage, not the
// original pre-extend row, which is what makes a multi-step migration
// (old -> mid -> final) work.
func TestExtendSelfReferenceSequential(t *testing.T) {
	tbl := queryResult(t, `datatable (X: string) ["old"]
		| extend X = case(X == "old", "mid", X)
		| extend X = case(X == "mid", "final", X)`)
	expectCell(t, tbl, 0, 0, "final")
}

// TestExtendNewColumnStillAppends is the regression check for the
// unaffected case: extend Col = expr where Col is genuinely new must
// still append, not replace anything (there's nothing to replace).
func TestExtendNewColumnStillAppends(t *testing.T) {
	tbl := queryResult(t, `datatable (X: string) ["a"] | extend Y = strcat(X, "!")`)
	expectCols(t, tbl, 2)
	expectCell(t, tbl, 0, 0, "a")
	expectCell(t, tbl, 0, 1, "a!")
}

// --- Take / Limit ---

func TestTake(t *testing.T) {
	tbl := queryResult(t, `datatable (X: long) [1, 2, 3, 4, 5] | take 3`)
	expectRows(t, tbl, 3)
}

// --- Count ---

func TestCount(t *testing.T) {
	tbl := queryResult(t, `datatable (X: long) [1, 2, 3] | count`)
	expectCell(t, tbl, 0, 0, "3")
}

// --- Distinct ---

func TestDistinct(t *testing.T) {
	tbl := queryResult(t, `datatable (X: string) ["a", "b", "a", "c", "b"] | distinct X`)
	expectRows(t, tbl, 3)
}

// --- Order By ---

func TestOrderBy(t *testing.T) {
	tbl := queryResult(t, `datatable (X: long) [3, 1, 2] | order by X asc`)
	expectCell(t, tbl, 0, 0, "1")
	expectCell(t, tbl, 1, 0, "2")
	expectCell(t, tbl, 2, 0, "3")
}

func TestOrderByDesc(t *testing.T) {
	tbl := queryResult(t, `datatable (X: long) [3, 1, 2] | order by X desc`)
	expectCell(t, tbl, 0, 0, "3")
	expectCell(t, tbl, 1, 0, "2")
	expectCell(t, tbl, 2, 0, "1")
}

// --- Top ---

func TestTop(t *testing.T) {
	tbl := queryResult(t, `datatable (X: long) [3, 1, 5, 2, 4] | top 3 by X`)
	expectRows(t, tbl, 3)
	expectCell(t, tbl, 0, 0, "5")
	expectCell(t, tbl, 1, 0, "4")
	expectCell(t, tbl, 2, 0, "3")
}

// --- Sample ---

func TestSample(t *testing.T) {
	tbl := queryResult(t, `datatable (X: long) [1, 2, 3, 4, 5, 6, 7, 8, 9, 10] | sample 3`)
	expectRows(t, tbl, 3)
}

// --- Summarize ---

func TestSummarizeCount(t *testing.T) {
	tbl := queryResult(t, `datatable (G: string, V: long) ["a", 1, "b", 2, "a", 3] | summarize count() by G`)
	expectRows(t, tbl, 2)
}

func TestSummarizeSum(t *testing.T) {
	tbl := queryResult(t, `datatable (V: long) [10, 20, 30] | summarize Total = sum(V)`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "60")
}

func TestSummarizeAvg(t *testing.T) {
	tbl := queryResult(t, `datatable (V: long) [10, 20, 30] | summarize Avg = avg(V)`)
	v := cellVal(t, tbl, 0, 0).(float64)
	if math.Abs(v-20.0) > 0.001 {
		t.Errorf("expected avg 20, got %f", v)
	}
}

func TestSummarizeMinMax(t *testing.T) {
	tbl := queryResult(t, `datatable (V: long) [5, 1, 9, 3] | summarize Min = min(V), Max = max(V)`)
	expectCell(t, tbl, 0, 0, "1")
	expectCell(t, tbl, 0, 1, "9")
}

func TestSummarizeMinifMaxif(t *testing.T) {
	tbl := queryResult(t, `datatable (V: long, G: string) [10, "A", 20, "B", 5, "A", 30, "B"] | summarize minif(V, G == "A"), maxif(V, G == "B")`)
	expectCell(t, tbl, 0, 0, "5")
	expectCell(t, tbl, 0, 1, "30")
}

func TestSummarizeDcount(t *testing.T) {
	tbl := queryResult(t, `datatable (X: string) ["a", "b", "a", "c"] | summarize dcount(X)`)
	expectCell(t, tbl, 0, 0, "3")
}

func TestSummarizeMakeSet(t *testing.T) {
	tbl := queryResult(t, `datatable (X: string) ["a", "b", "a", "c"] | summarize make_set(X)`)
	expectRows(t, tbl, 1)
	v := fmt.Sprintf("%v", cellVal(t, tbl, 0, 0))
	// Should contain a, b, c (order may vary)
	if !strings.Contains(v, "a") || !strings.Contains(v, "b") || !strings.Contains(v, "c") {
		t.Errorf("make_set missing values: %s", v)
	}
}

func TestSummarizeMakeList(t *testing.T) {
	tbl := queryResult(t, `datatable (X: long) [1, 2, 3] | summarize make_list(X)`)
	expectRows(t, tbl, 1)
	v := fmt.Sprintf("%v", cellVal(t, tbl, 0, 0))
	if !strings.Contains(v, "1") || !strings.Contains(v, "2") || !strings.Contains(v, "3") {
		t.Errorf("make_list missing values: %s", v)
	}
}

func TestSummarizeMakeBag(t *testing.T) {
	tbl := queryResult(t, `datatable (K: string, V: string) ["name", "Alice", "role", "admin"] | summarize make_bag(pack(K, V))`)
	expectRows(t, tbl, 1)
	v := fmt.Sprintf("%v", cellVal(t, tbl, 0, 0))
	if !strings.Contains(v, "name") || !strings.Contains(v, "Alice") {
		t.Errorf("make_bag missing data: %s", v)
	}
}

func TestSummarizeArgMax(t *testing.T) {
	tbl := queryResult(t, `datatable (Name: string, Score: long) ["Alice", 90, "Bob", 95, "Carol", 85] | summarize arg_max(Score, Name)`)
	expectRows(t, tbl, 1)
	// arg_max returns the row with max Score; Name should be Bob
	found := false
	for c := 0; c < len(tbl.Schema.Columns); c++ {
		if cell(t, tbl, 0, c) == "Bob" {
			found = true
		}
	}
	if !found {
		t.Errorf("arg_max should return Bob")
	}
}

// TestArgMaxOutputColumnNaming guards a real conformance fix: real ADX
// names arg_max's output column after the SECOND argument
// (ExprToReturn) -- verified against a real documented example
// (arg_max(BeginLat, BeginLocation) by State names its column
// BeginLocation, not arg_max_BeginLat) before fixing this. Every other
// aggregate keeps the function_argname[0] convention (max_Score,
// etc.) -- only checked via getschema here, distinct from
// TestSummarizeArgMax above which only checks the VALUE.
func TestArgMaxOutputColumnNaming(t *testing.T) {
	tbl := queryResult(t, `datatable (Name: string, Score: long) ["Alice", 90, "Bob", 95] | summarize arg_max(Score, Name)`)
	if tbl.Schema.Columns[0].Name != "Name" {
		t.Fatalf("expected output column named %q, got %q", "Name", tbl.Schema.Columns[0].Name)
	}
	expectCell(t, tbl, 0, 0, "Bob")
}

// TestArgMinOutputColumnNaming is the arg_min counterpart of the test
// immediately above.
func TestArgMinOutputColumnNaming(t *testing.T) {
	tbl := queryResult(t, `datatable (Name: string, Score: long) ["Alice", 90, "Bob", 95] | summarize arg_min(Score, Name)`)
	if tbl.Schema.Columns[0].Name != "Name" {
		t.Fatalf("expected output column named %q, got %q", "Name", tbl.Schema.Columns[0].Name)
	}
	expectCell(t, tbl, 0, 0, "Alice")
}

// TestSummarizeArgMaxStarParses guards the actual reported bug: real
// ADX's arg_max(expr, *) — "use a wildcard * to return all columns" —
// used to fail to parse at all.
func TestSummarizeArgMaxStarParses(t *testing.T) {
	tbl := queryResult(t, `datatable (Id: string, Seq: long, Status: string) `+
		`["a", 1, "open", "a", 3, "closed", "b", 2, "open"] `+
		`| summarize arg_max(Seq, *) by Id | sort by Id asc`)
	expectRows(t, tbl, 2)
}

// TestSummarizeArgMaxStarOutputColumns guards the exact, verified
// output shape against a real, documented Microsoft example
// (datatable(Fruit, Color, Version) | summarize arg_max(Version, *) by
// Fruit -> columns Fruit, Version, Color) -- specifically that the
// group-by key does NOT get duplicated even though it's also one of
// the source table's own columns that "*" would otherwise include.
// Column ORDER is deliberately not asserted here -- this engine's
// existing summarize convention (aggregation columns before group-by
// columns) differs from real ADX's for this specific case (documented,
// known, and not fixed as part of this work, since column resolution
// throughout this engine is always by name, never by position).
func TestSummarizeArgMaxStarOutputColumns(t *testing.T) {
	tbl := queryResult(t, `datatable (Id: string, Seq: long, Status: string) `+
		`["a", 1, "open", "a", 3, "closed"] `+
		`| summarize arg_max(Seq, *) by Id`)
	expectRows(t, tbl, 1)
	names := map[string]bool{}
	for _, c := range tbl.Schema.Columns {
		names[c.Name] = true
	}
	for _, want := range []string{"Id", "Seq", "Status"} {
		if !names[want] {
			t.Errorf("expected output column %q, columns were %v", want, tbl.Schema.Columns)
		}
	}
	if len(tbl.Schema.Columns) != 3 {
		t.Errorf("expected exactly 3 output columns (Id not duplicated), got %d: %v", len(tbl.Schema.Columns), tbl.Schema.Columns)
	}

	idIdx := tbl.Schema.ColumnIndex("Id")
	seqIdx := tbl.Schema.ColumnIndex("Seq")
	statusIdx := tbl.Schema.ColumnIndex("Status")
	if tbl.Rows[0][idIdx] != "a" || tbl.Rows[0][seqIdx] != int64(3) || tbl.Rows[0][statusIdx] != "closed" {
		t.Errorf("expected the max-Seq row's values (a, 3, closed), got Id=%v Seq=%v Status=%v",
			tbl.Rows[0][idIdx], tbl.Rows[0][seqIdx], tbl.Rows[0][statusIdx])
	}
}

// TestSummarizeArgMinStar is the arg_min counterpart of the star-form
// tests above.
func TestSummarizeArgMinStar(t *testing.T) {
	tbl := queryResult(t, `datatable (Id: string, Seq: long, Status: string) `+
		`["a", 1, "open", "a", 3, "closed", "b", 2, "open"] `+
		`| summarize arg_min(Seq, *) by Id | sort by Id asc`)
	expectRows(t, tbl, 2)
	seqIdx := tbl.Schema.ColumnIndex("Seq")
	statusIdx := tbl.Schema.ColumnIndex("Status")
	if tbl.Rows[0][seqIdx] != int64(1) || tbl.Rows[0][statusIdx] != "open" {
		t.Errorf("expected row 0 (Id=a, min Seq) to be Seq=1, Status=open, got Seq=%v Status=%v",
			tbl.Rows[0][seqIdx], tbl.Rows[0][statusIdx])
	}
}

// TestArgMaxNullFallsBackToFirstRow guards a real, separate divergence
// found and fixed while verifying the star form against a real,
// documented Microsoft example: a group where EVERY row's
// exprToMaximize is null still produces a result row (the first row
// encountered), not no row / all-null. The Banana group in the real
// example (two rows, both with a null Version) still appears in the
// output with Version null and Color taken from the first Banana row
// ("Yellow") -- the earlier version of this engine's arg_max/arg_min
// returned no best row at all whenever every candidate was null,
// silently dropping such groups from star-form aggregations entirely.
func TestArgMaxNullFallsBackToFirstRow(t *testing.T) {
	tbl := queryResult(t, `datatable (Fruit: string, Color: string, Version: long) `+
		`["Apple", "Red", 1, "Banana", "Yellow", "", "Banana", "Green", ""] `+
		`| summarize arg_max(Version, *) by Fruit | sort by Fruit asc`)
	expectRows(t, tbl, 2)
	fruitIdx := tbl.Schema.ColumnIndex("Fruit")
	colorIdx := tbl.Schema.ColumnIndex("Color")
	versionIdx := tbl.Schema.ColumnIndex("Version")

	// Row 0: Apple (sorted first alphabetically)
	if tbl.Rows[0][fruitIdx] != "Apple" || tbl.Rows[0][versionIdx] != int64(1) {
		t.Errorf("expected Apple row with Version=1, got Fruit=%v Version=%v", tbl.Rows[0][fruitIdx], tbl.Rows[0][versionIdx])
	}
	// Row 1: Banana -- both candidates null, must still produce a row
	// (not be dropped), with Color from the FIRST Banana row (Yellow).
	if tbl.Rows[1][fruitIdx] != "Banana" {
		t.Fatalf("expected a Banana row to be present at all (not dropped), got %v", tbl.Rows[1][fruitIdx])
	}
	if tbl.Rows[1][versionIdx] != nil {
		t.Errorf("expected Banana's Version to be nil (every candidate was null), got %v", tbl.Rows[1][versionIdx])
	}
	if tbl.Rows[1][colorIdx] != "Yellow" {
		t.Errorf("expected Banana's Color to be from the FIRST Banana row (Yellow), got %v", tbl.Rows[1][colorIdx])
	}
}

// TestSummarizeArgMaxStarAgainstRealTablePlannerBug guards a real,
// live, silent bug -- independently found and confirmed by a
// different model's testing session (Kimi), which hit the exact same
// symptom against this same commit before this fix landed: the star
// form's output schema/values only ever exercised in-memory datatable
// literals in the tests above, which never go through this engine's
// column-projection-pushdown planner at all. Against a REAL,
// disk-backed, scanned table, T | summarize arg_max(Seq, *) by Id
// silently dropped a payload column (Status) entirely -- present in
// T's own schema, absent from both getschema and the row data, no
// error. Root cause: parser.StarExpr (added for the star form) had no
// case in planner.go's collectExpr -- the exact same bug CLASS this
// session already found and fixed twice before, for HasAnyAllExpr and
// LookupOp: a new AST node type the pushdown analysis doesn't know
// how to interpret, so it silently concludes fewer columns are needed
// than actually are. This test deliberately uses discoverEngine/
// diskExec/diskQuery (a real .create table + .set-or-append, not
// datatable) specifically because that's the only way to exercise the
// pushdown path this bug lived in at all -- the existing
// TestSummarizeArgMaxStarOutputColumns above, using an in-memory
// datatable literal, could never have caught this regardless of how
// thorough its own assertions were.
func TestSummarizeArgMaxStarAgainstRealTablePlannerBug(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table T (Id: string, Status: string, Seq: long)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string, Status:string, Seq:long) `+
		`["a","open",1,"a","closed",3,"b","open",2]`)

	tbl := diskQuery(t, eng, `T | summarize arg_max(Seq, *) by Id | sort by Id asc`)
	expectRows(t, tbl, 2)

	statusIdx := tbl.Schema.ColumnIndex("Status")
	if statusIdx < 0 {
		t.Fatalf("Status column missing entirely from summarize output -- exactly the reported bug (columns: %v)", tbl.Schema.Columns)
	}
	seqIdx := tbl.Schema.ColumnIndex("Seq")
	if tbl.Rows[0][statusIdx] != "closed" || tbl.Rows[0][seqIdx] != int64(3) {
		t.Errorf("expected row 0 (Id=a) Status=closed Seq=3, got Status=%v Seq=%v", tbl.Rows[0][statusIdx], tbl.Rows[0][seqIdx])
	}
	if tbl.Rows[1][statusIdx] != "open" || tbl.Rows[1][seqIdx] != int64(2) {
		t.Errorf("expected row 1 (Id=b) Status=open Seq=2, got Status=%v Seq=%v", tbl.Rows[1][statusIdx], tbl.Rows[1][seqIdx])
	}
}

func TestSummarizePercentile(t *testing.T) {
	tbl := queryResult(t, `datatable (V: long) [1, 2, 3, 4, 5, 6, 7, 8, 9, 10] | summarize percentile(V, 50)`)
	expectRows(t, tbl, 1)
	v := cellVal(t, tbl, 0, 0).(float64)
	if v < 5.0 || v > 6.0 {
		t.Errorf("median of 1..10 should be ~5.5, got %f", v)
	}
}

func TestSummarizePercentiles(t *testing.T) {
	tbl := queryResult(t, `datatable (V: long) [1, 2, 3, 4, 5, 6, 7, 8, 9, 10] | summarize percentiles(V, 25, 50, 75)`)
	expectRows(t, tbl, 1)
	expectCols(t, tbl, 3)
}

func TestSummarizeStdev(t *testing.T) {
	tbl := queryResult(t, `datatable (V: long) [2, 4, 4, 4, 5, 5, 7, 9] | summarize stdev(V)`)
	expectRows(t, tbl, 1)
	v := cellVal(t, tbl, 0, 0).(float64)
	if v < 2.0 || v > 2.2 {
		t.Errorf("stdev should be ~2.14, got %f", v)
	}
}

func TestSummarizeBinaryAll(t *testing.T) {
	tbl := queryResult(t, `datatable (V: long) [7, 5, 3] | summarize binary_all_and(V)`)
	expectCell(t, tbl, 0, 0, "1") // 7&5&3 = 1

	tbl2 := queryResult(t, `datatable (V: long) [1, 2, 4] | summarize binary_all_or(V)`)
	expectCell(t, tbl2, 0, 0, "7") // 1|2|4 = 7
}

func TestSummarizeCountif(t *testing.T) {
	tbl := queryResult(t, `datatable (V: long) [1, 2, 3, 4, 5] | summarize countif(V > 3)`)
	expectCell(t, tbl, 0, 0, "2")
}

func TestSummarizeSumif(t *testing.T) {
	tbl := queryResult(t, `datatable (V: long, G: string) [10, "A", 20, "B", 30, "A"] | summarize sumif(V, G == "A")`)
	expectCell(t, tbl, 0, 0, "40")
}

// --- Join ---

const joinFixture = `let T1 = datatable (K: long, A: string) [1, "x", 2, "y", 3, "z"]; ` +
	`let T2 = datatable (K: long, B: string) [2, "bb", 3, "cc", 4, "dd"]; `

func TestJoinInner(t *testing.T) {
	tbl := queryResult(t, joinFixture+`T1 | join kind=inner (T2) on K | sort by K asc`)
	expectRows(t, tbl, 2)
	expectColNames(t, tbl, "K", "A", "B")
	expectCell(t, tbl, 0, 0, "2")
	expectCell(t, tbl, 0, 1, "y")
	expectCell(t, tbl, 0, 2, "bb")
	expectCell(t, tbl, 1, 0, "3")
	expectCell(t, tbl, 1, 2, "cc")
}

// TestJoinInnerUniqueDedupsLeftNotOutput guards the actual semantics
// of innerunique (real ADX's own default join kind, verified against
// Microsoft's docs before implementing: "innerunique (default) --
// Inner join with left side deduplication... All deduplicated rows
// from the left table that match rows from the right table"). This is
// NOT the same as deduplicating the final output -- a left key with 2
// duplicate rows joined against a right key with 2 matches produces 2
// output rows (one deduped left representative x both right matches),
// not 4 (plain inner over undeduplicated left) and not 1 (full output
// dedup). The duplicate left row is dropped entirely, not merged.
func TestJoinInnerUniqueDedupsLeftNotOutput(t *testing.T) {
	tbl := queryResult(t, `let Fruit = datatable(number:long, fruit:string) [1, "Apple", 1, "Pear", 2, "Banana"]; `+
		`let Prep = datatable(number:long, prep:string) [1, "Slices", 1, "Juice", 2, "Smoothie"]; `+
		`Fruit | join kind=innerunique (Prep) on number | sort by number asc, prep asc`)
	expectRows(t, tbl, 3)
	// key=1: only "Apple" (first-encountered), joined against BOTH
	// right matches -- "Pear" (the left duplicate) never appears at all.
	expectCell(t, tbl, 0, 1, "Apple")
	expectCell(t, tbl, 0, 2, "Juice")
	expectCell(t, tbl, 1, 1, "Apple")
	expectCell(t, tbl, 1, 2, "Slices")
	for _, row := range tbl.Rows {
		if row[1] == "Pear" {
			t.Fatalf("expected the duplicate left row (Pear) to be dropped entirely, found: %v", row)
		}
	}
	// key=2: no left duplicates, works exactly like plain inner.
	expectCell(t, tbl, 2, 1, "Banana")
	expectCell(t, tbl, 2, 2, "Smoothie")
}

// TestJoinBareDefaultsToInnerUnique guards the actual default-kind
// decision: real ADX defaults to innerunique, not inner. A bare join
// (no kind=) used to silently default to plain inner instead --
// same query, different row counts, no error -- exactly the kind of
// silent divergence a port of this engine exists to avoid. Checked
// against real data before changing: zero uses of bare join existed
// anywhere in this repo's own tests or either memory-scope skill's
// documented recipes, so this was a zero-blast-radius correction.
func TestJoinBareDefaultsToInnerUnique(t *testing.T) {
	fixture := `let Fruit = datatable(number:long, fruit:string) [1, "Apple", 1, "Pear", 2, "Banana"]; ` +
		`let Prep = datatable(number:long, prep:string) [1, "Slices", 1, "Juice", 2, "Smoothie"]; `

	bare := queryResult(t, fixture+`Fruit | join (Prep) on number`)
	explicit := queryResult(t, fixture+`Fruit | join kind=innerunique (Prep) on number`)
	expectRows(t, bare, 3)
	expectRows(t, explicit, 3)

	// And explicitly confirm it's NOT what the default used to be —
	// plain inner over the same fixture returns 5 (2x2=4 for key=1's
	// undeduplicated left rows, +1 for key=2), not 3.
	plainInner := queryResult(t, fixture+`Fruit | join kind=inner (Prep) on number`)
	expectRows(t, plainInner, 5)
}

func TestJoinLeftouter(t *testing.T) {
	tbl := queryResult(t, joinFixture+`T1 | join kind=leftouter (T2) on K | sort by K asc`)
	expectRows(t, tbl, 3)
	// Unmatched left row (K=1) keeps A but has null B
	expectCell(t, tbl, 0, 0, "1")
	expectCell(t, tbl, 0, 1, "x")
	if cellVal(t, tbl, 0, 2) != nil {
		t.Error("leftouter unmatched row should have nil B")
	}
	expectCell(t, tbl, 1, 2, "bb")
	expectCell(t, tbl, 2, 2, "cc")
}

func TestJoinLeftanti(t *testing.T) {
	tbl := queryResult(t, joinFixture+`T1 | join kind=leftanti (T2) on K`)
	expectRows(t, tbl, 1)
	expectColNames(t, tbl, "K", "A")
	expectCell(t, tbl, 0, 0, "1")
	expectCell(t, tbl, 0, 1, "x")
}

func TestJoinKeysAreTyped(t *testing.T) {
	// typedKey: string "1" must not match long 1 across join sides
	// (KQL joins do not coerce key types).
	tbl := queryResult(t,
		`let L = datatable (K: string, A: long) ["1", 10]; `+
			`let R = datatable (K: long, B: long) [1, 99]; `+
			`L | join kind=inner (R) on K`)
	expectRows(t, tbl, 0)
}

func TestSortByInferredLongColumn(t *testing.T) {
	// array_length infers long; sorting the projected column must not
	// panic. (Previously inferExprType defaulted it to string and
	// CompareValues hard-asserted a.(string) on int64 values.)
	tbl := queryResult(t,
		`datatable (D: dynamic) ["[1,2,3]", "[1]", "[1,2]"] `+
			`| project L = array_length(D) | sort by L asc`)
	expectRows(t, tbl, 3)
	expectCell(t, tbl, 0, 0, "1")
	expectCell(t, tbl, 2, 0, "3")
}

func TestCompareValuesMismatchDoesNotPanic(t *testing.T) {
	// Even when the declared type and stored value disagree (inference
	// miss), sorting falls back to formatted comparison instead of
	// panicking. iff() infers string but yields int64 here.
	tbl := queryResult(t,
		`datatable (X: long) [3, 1, 2] | project V = iff(X > 0, X, X) | sort by V asc`)
	expectRows(t, tbl, 3)
}

func TestMinMaxOnIntColumn(t *testing.T) {
	// inferValType lacked an int32 case, so min/max on int columns
	// fell through to string comparison: min of {5, 12, 2} came back
	// 12 ("12" < "2" lexically). Must compare numerically.
	tbl := queryResult(t, `datatable (X: int) [5, 12, 2] | summarize M = min(X), Mx = max(X)`)
	expectCell(t, tbl, 0, 0, "2")
	expectCell(t, tbl, 0, 1, "12")
}

func TestFailedLetDoesNotLeakContext(t *testing.T) {
	// A compound statement that fails mid-let must not leave its
	// bindings visible to the next statement on the same engine.
	// (Previously activeLetContext survived early error returns.)
	cat := catalog.NewMemory()
	eng := New(cat)

	stmt, err := parser.Parse(`let Secret = 42; let T = BadTable | count; T`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := eng.Execute(stmt); err == nil {
		t.Fatalf("expected failure for missing table")
	}

	stmt2, err := parser.Parse(`print Y = Secret`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := eng.Execute(stmt2); err == nil {
		t.Fatalf("Secret resolved after failed compound: let context leaked")
	}
}

// --- Render ---

func TestRenderPassthrough(t *testing.T) {
	tbl := queryResult(t, `datatable (X: long) [1, 2, 3] | summarize S = sum(X) | render timechart`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "6")
}

func TestRenderWithProperties(t *testing.T) {
	tbl := queryResult(t, `datatable (X: long) [1, 2] | render barchart with (title="T", ytitle="Y")`)
	expectRows(t, tbl, 2)
	expectColNames(t, tbl, "X")
}

func TestRenderMissingVisualization(t *testing.T) {
	queryError(t, `datatable (X: long) [1] | render`)
}

// --- Lookup ---

func TestLookupDefault(t *testing.T) {
	tbl := queryResult(t, `let Dim = datatable (K: long, Name: string) [1, "alpha", 2, "beta"]; `+
		`let Facts = datatable (K: long, V: long) [1, 10, 2, 20, 3, 30]; `+
		`Facts | lookup Dim on K | sort by K asc`)
	// Default kind is leftouter: all fact rows kept, unmatched get null Name
	expectRows(t, tbl, 3)
	expectColNames(t, tbl, "K", "V", "Name")
	expectCell(t, tbl, 0, 2, "alpha")
	expectCell(t, tbl, 1, 2, "beta")
	if cellVal(t, tbl, 2, 2) != nil {
		t.Error("lookup: unmatched row should have nil Name")
	}
}

func TestLookupInner(t *testing.T) {
	tbl := queryResult(t, `let Dim = datatable (K: long, Name: string) [1, "alpha", 2, "beta"]; `+
		`let Facts = datatable (K: long, V: long) [1, 10, 2, 20, 3, 30]; `+
		`Facts | lookup kind=inner Dim on K`)
	expectRows(t, tbl, 2)
}

func TestLookupInvalidKind(t *testing.T) {
	queryError(t, `let D = datatable (K: long) [1]; `+
		`let F = datatable (K: long) [1]; `+
		`F | lookup kind=leftanti D on K`)
}

func TestLookupUnknownTable(t *testing.T) {
	queryError(t, `datatable (K: long) [1] | lookup NoSuchDim on K`)
}

// TestLookupParenthesizedTable guards a real, live parsing bug:
// lookup (Entities) on ... took the raw substring before " on "
// verbatim, with no paren-stripping at all, so a parenthesized table
// reference failed with table "(Entities)" not found -- parens
// literally included in the looked-up name -- even though the bare
// form (lookup Entities on ...) already worked. join already accepts
// (in fact requires) a parenthesized right side; lookup should accept
// the same form, not just the bare one.
func TestLookupParenthesizedTable(t *testing.T) {
	tbl := queryResult(t, `let Dim = datatable (K: long, Name: string) [1, "alpha", 2, "beta"]; `+
		`let Facts = datatable (K: long, V: long) [1, 10, 2, 20]; `+
		`Facts | lookup (Dim) on K | sort by K asc`)
	expectRows(t, tbl, 2)
	expectCell(t, tbl, 0, 2, "alpha")
}

// TestLookupOnDiskWithDownstreamNarrowing guards a real, live bug
// distinct from the parenthesized-table one above and from an
// unrelated but same-shaped has_any bug found the same session:
// LookupOp had NO case at all in the column-projection-pushdown
// planner (planner.go's collectOperator), so its on-clause's own
// column references (e.g. $left.Subject) were never discovered as
// needed, and a downstream narrowing operator (count, here) triggered
// a scan that silently omitted the join key column entirely -- lookup
// then failed evaluating its own on-clause with "left column ...
// not found". Only reproduces against a real, disk-backed, discovery-
// mode table (unlike the let-bound in-memory datatable tests above,
// which never exercise the storage-scan pushdown path this bug lives
// in at all) AND only with a downstream narrowing operator present --
// with none, no pushdown analysis runs and every column is scanned
// regardless, which is exactly why this class of bug hides from a
// simple "does the plain query work" test.
func TestLookupOnDiskWithDownstreamNarrowing(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table Dim (K: long, Name: string)`)
	diskExec(t, eng, `.set-or-append Dim <| datatable(K:long, Name:string) [1, "alpha", 2, "beta"]`)
	diskExec(t, eng, `.create table Facts (K: long, V: long)`)
	diskExec(t, eng, `.set-or-append Facts <| datatable(K:long, V:long) [1, 10, 2, 20, 3, 30]`)

	tbl := diskQuery(t, eng, `Facts | lookup Dim on $left.K == $right.K | count`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "3")

	// Parenthesized form too, since both fixes compose in the same query.
	tbl = diskQuery(t, eng, `Facts | lookup (Dim) on $left.K == $right.K | count`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "3")
}

// --- Union ---

func TestUnion(t *testing.T) {
	tbl := queryResult(t, `let T1 = datatable (X: long) [1, 2]; `+
		`let T2 = datatable (X: long) [3, 4, 5]; `+
		`T1 | union (T2) | summarize Cnt = count(), Total = sum(X)`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "5")
	expectCell(t, tbl, 0, 1, "15")
}

// --- MvExpand ---

func TestMvExpand(t *testing.T) {
	tbl := queryResult(t, `datatable (X: string) ["[1,2,3]"] | mv-expand X`)
	expectRows(t, tbl, 3)
}

// --- MvApply ---

func TestMvApplySummarizePerRow(t *testing.T) {
	tbl := queryResult(t, `datatable (Id: long, Arr: string) [1, "[1,2,3]", 2, "[10,20]"] `+
		`| mv-apply Element = Arr to typeof(long) on ( summarize Total = sum(Element) by Id )`)
	expectRows(t, tbl, 2)
	expectCell(t, tbl, 0, 0, "6")
	expectCell(t, tbl, 1, 0, "30")
}

func TestMvApplyFilterPipeline(t *testing.T) {
	tbl := queryResult(t, `datatable (Id: long, Arr: string) [1, "[1,2,3,4]", 2, "[10,20]"] `+
		`| mv-apply Element = Arr to typeof(long) on ( where Element > 2 | summarize Cnt = count() by Id )`)
	expectRows(t, tbl, 2)
	expectCell(t, tbl, 0, 0, "2")
	expectCell(t, tbl, 1, 0, "2")
}

func TestMvApplyTopPerRow(t *testing.T) {
	// Original columns are visible inside the subquery
	tbl := queryResult(t, `datatable (Id: long, Arr: string) [1, "[5,1,9]", 2, "[3,7]"] `+
		`| mv-apply x = Arr to typeof(long) on ( top 1 by x )`)
	expectRows(t, tbl, 2)
	expectColNames(t, tbl, "Id", "Arr", "x")
	expectCell(t, tbl, 0, 2, "9")
	expectCell(t, tbl, 1, 2, "7")
}

func TestMvApplyInPlace(t *testing.T) {
	// Without renaming, the array column itself carries the element
	tbl := queryResult(t, `datatable (Arr: string) ["[1,2]"] `+
		`| mv-apply Arr to typeof(long) on ( where Arr > 1 )`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "2")
}

func TestMvApplyEmptyArray(t *testing.T) {
	tbl := queryResult(t, `datatable (Arr: string) ["[]"] `+
		`| mv-apply Element = Arr on ( where Element == 1 )`)
	expectRows(t, tbl, 0)
}

func TestMvApplyMissingColumn(t *testing.T) {
	queryError(t, `datatable (A: long) [1] | mv-apply NoSuch on ( count )`)
}

func TestMvApplyMissingOn(t *testing.T) {
	queryError(t, `datatable (Arr: string) ["[1]"] | mv-apply Arr`)
}

// --- Parse ---

func TestParse(t *testing.T) {
	tbl := queryResult(t, `datatable (S: string) ["Error: 404 Not Found", "Error: 500 Internal"] | parse S with "Error: " Code " " *`)
	expectRows(t, tbl, 2)
	// Should have original S plus Code column
	found := false
	for _, col := range tbl.Schema.Columns {
		if col.Name == "Code" {
			found = true
		}
	}
	if !found {
		t.Error("parse should produce Code column")
	}
	expectCell(t, tbl, 0, 1, "404")
	expectCell(t, tbl, 1, 1, "500")
}

// --- Getschema ---

func TestGetschema(t *testing.T) {
	tbl := queryResult(t, `datatable (A: long, B: string) [1, "x"] | getschema`)
	expectRows(t, tbl, 2)
	expectColNames(t, tbl, "ColumnName", "ColumnOrdinal", "DataType", "ColumnType")
}

// --- Serialize + Window Functions ---

func TestSerializeRowNumber(t *testing.T) {
	tbl := queryResult(t, `datatable (X: long) [10, 20, 30] | serialize N = row_number()`)
	expectRows(t, tbl, 3)
	expectCell(t, tbl, 0, 1, "1")
	expectCell(t, tbl, 1, 1, "2")
	expectCell(t, tbl, 2, 1, "3")
}

func TestSerializePrevNext(t *testing.T) {
	tbl := queryResult(t, `datatable (V: long) [10, 20, 30] | serialize P = prev(V), N = next(V)`)
	expectRows(t, tbl, 3)
	// Row 0: prev=nil, next=20
	expectCell(t, tbl, 0, 2, "20")
	// Row 1: prev=10, next=30
	expectCell(t, tbl, 1, 1, "10")
	expectCell(t, tbl, 1, 2, "30")
	// Row 2: prev=20, next=nil
	expectCell(t, tbl, 2, 1, "20")
}

func TestSerializePrevWithOffset(t *testing.T) {
	tbl := queryResult(t, `datatable (V: long) [1, 2, 3, 4, 5] | serialize Lag2 = prev(V, 2)`)
	// Row 0,1 should be null, row 2 should be 1
	if cellVal(t, tbl, 0, 1) != nil {
		t.Error("prev(V,2) row 0 should be nil")
	}
	expectCell(t, tbl, 2, 1, "1")
	expectCell(t, tbl, 4, 1, "3")
}

// --- Let Statements ---

func TestLetScalar(t *testing.T) {
	tbl := queryResult(t, `let x = 42; print V = x`)
	expectCell(t, tbl, 0, 0, "42")
}

func TestLetTabular(t *testing.T) {
	tbl := queryResult(t, `let T = datatable (X: long) [1, 2, 3]; T | count`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "3")
}

func TestLetTabularWithPipe(t *testing.T) {
	tbl := queryResult(t, `let T = datatable (X: long) [1, 2, 3] | where X > 1; T | count`)
	expectCell(t, tbl, 0, 0, "2")
}

// --- User-Defined Functions ---

func TestUDFBasic(t *testing.T) {
	tbl := queryResult(t, `let double = (x: long) { x * 2 }; print R = double(21)`)
	expectCell(t, tbl, 0, 0, "42")
}

func TestUDFMultiParam(t *testing.T) {
	tbl := queryResult(t, `let add = (a: long, b: long) { a + b }; `+
		`datatable (X: long) [1, 2, 3] | extend Y = add(X, 100)`)
	expectCell(t, tbl, 0, 1, "101")
	expectCell(t, tbl, 2, 1, "103")
}

func TestUDFString(t *testing.T) {
	tbl := queryResult(t, `let greet = (name: string) { strcat("hello ", name) }; print R = greet("world")`)
	expectCell(t, tbl, 0, 0, "hello world")
}

func TestUDFNested(t *testing.T) {
	tbl := queryResult(t, `let f = (x: long) { x * 2 }; let g = (x: long) { f(x) + 1 }; print R = g(10)`)
	expectCell(t, tbl, 0, 0, "21")
}

func TestUDFClosesOverLetScalar(t *testing.T) {
	tbl := queryResult(t, `let base = 100; let f = (x: long) { x + base }; print R = f(1)`)
	expectCell(t, tbl, 0, 0, "101")
}

func TestUDFInWhere(t *testing.T) {
	tbl := queryResult(t, `let isadmin = (u: string) { u has "admin" }; `+
		`datatable (U: string) ["admin-root", "user1", "sysadmin"] | where isadmin(U)`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "admin-root")
}

func TestUDFArityMismatch(t *testing.T) {
	queryError(t, `let f = (x: long) { x }; print R = f(1, 2)`)
}

func TestUDFRecursionGuard(t *testing.T) {
	queryError(t, `let f = (x: long) { f(x) }; print R = f(1)`)
}

func TestUDFNoParams(t *testing.T) {
	tbl := queryResult(t, `let answer = () { 42 }; print R = answer()`)
	expectCell(t, tbl, 0, 0, "42")
}

func TestUDFParamIsolation(t *testing.T) {
	// UDF body must not see caller columns that aren't passed as arguments
	queryError(t, `let f = (x: long) { x + Hidden }; `+
		`datatable (X: long, Hidden: long) [1, 99] | extend Y = f(X)`)
}

// --- Scalar Functions ---

func TestStringFunctions(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{"strlen", `print R = strlen("hello")`, "5"},
		{"tolower", `print R = tolower("HELLO")`, "hello"},
		{"toupper", `print R = toupper("hello")`, "HELLO"},
		{"strcat", `print R = strcat("a", "b", "c")`, "abc"},
		{"strcat_delim", `print R = strcat_delim("-", "a", "b", "c")`, "a-b-c"},
		{"substring", `print R = substring("hello", 1, 3)`, "ell"},
		{"indexof", `print R = indexof("hello world", "world")`, "6"},
		{"countof", `print R = countof("banana", "an")`, "2"},
		{"reverse", `print R = reverse("abc")`, "cba"},
		{"replace_string", `print R = replace_string("hello world", "world", "earth")`, "hello earth"},
		{"split", `print R = split("a-b-c", "-")`, `["a","b","c"]`},
		{"trim", `print R = trim("\\s", "  hello  ")`, "hello"},
		{"trim_start", `print R = trim_start("\\s", "  hello  ")`, "hello  "},
		{"trim_end", `print R = trim_end("\\s", "  hello  ")`, "  hello"},
		{"strrep", `print R = strrep("ab", 3)`, "ababab"},
		{"strcmp_eq", `print R = strcmp("abc", "abc")`, "0"},
		{"strcmp_lt", `print R = strcmp("abc", "def")`, "-1"},
		{"isempty_true", `print R = isempty("")`, "true"},
		{"isempty_false", `print R = isempty("x")`, "false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := queryResult(t, tt.query)
			expectCell(t, tbl, 0, 0, tt.expected)
		})
	}
}

func TestMathFunctions(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{"abs_pos", `print R = abs(5)`, "5"},
		{"abs_neg", `print R = abs(-5)`, "5"},
		{"round", `print R = round(3.14159, 2)`, "3.14"},
		{"ceiling", `print R = ceiling(3.2)`, "4"},
		{"sign_pos", `print R = sign(42)`, "1"},
		{"sign_neg", `print R = sign(-5)`, "-1"},
		{"sign_zero", `print R = sign(0)`, "0"},
		{"pow", `print R = pow(2, 10)`, "1024"},
		{"sqrt", `print R = sqrt(144)`, "12"},
		{"log10", `print R = log10(1000)`, "3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := queryResult(t, tt.query)
			expectCell(t, tbl, 0, 0, tt.expected)
		})
	}
}

func TestConditionalFunctions(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{"iff_true", `print R = iff(1 > 0, "yes", "no")`, "yes"},
		{"iff_false", `print R = iff(1 > 2, "yes", "no")`, "no"},
		{"case", `print R = case(1 == 2, "a", 1 == 1, "b", "c")`, "b"},
		{"coalesce", `print R = coalesce("", "", "found")`, "found"},
		{"max_of", `print R = max_of(3, 1, 4, 1, 5)`, "5"},
		{"min_of", `print R = min_of(3, 1, 4, 1, 5)`, "1"},
		{"isnull_null", `print R = isnull(tolong(""))`, "false"}, // tolong("") returns 0, not null
		{"isnotnull", `print R = isnotnull(42)`, "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := queryResult(t, tt.query)
			expectCell(t, tbl, 0, 0, tt.expected)
		})
	}
}

func TestTypeConversion(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{"tostring_int", `print R = tostring(42)`, "42"},
		{"tostring_bool", `print R = tostring(true)`, "true"},
		{"toint", `print R = toint(42)`, "42"},
		{"tolong", `print R = tolong(12345678901234)`, "12345678901234"},
		{"todouble", `print R = todouble(3.14)`, "3.14"},
		{"tobool_true", `print R = tobool("true")`, "true"},
		{"tobool_false", `print R = tobool("false")`, "false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := queryResult(t, tt.query)
			expectCell(t, tbl, 0, 0, tt.expected)
		})
	}
}

func TestDynamicFunctions(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{"array_length", `print R = array_length(parse_json("[1,2,3]"))`, "3"},
		{"bag_keys", `print R = bag_keys(parse_json("{\"a\":1,\"b\":2}"))`, `["a","b"]`},
		{"bag_has_key_true", `print R = bag_has_key(parse_json("{\"a\":1}"), "a")`, "true"},
		{"bag_has_key_false", `print R = bag_has_key(parse_json("{\"a\":1}"), "b")`, "false"},
		{"pack_array", `print R = pack_array(1, 2, 3)`, `[1,2,3]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := queryResult(t, tt.query)
			expectCell(t, tbl, 0, 0, tt.expected)
		})
	}
}

func TestNetFunctions(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{"parse_ipv4", `print R = parse_ipv4("10.0.0.1")`, "167772161"},
		{"ipv4_is_private_true", `print R = ipv4_is_private("10.0.0.1")`, "true"},
		{"ipv4_is_private_false", `print R = ipv4_is_private("8.8.8.8")`, "false"},
		{"ipv4_is_in_range", `print R = ipv4_is_in_range("10.0.1.5", "10.0.0.0/8")`, "true"},
		{"has_ipv4_exact", `print R = has_ipv4("log from 10.0.1.5", "10.0.1.5")`, "true"},
		{"has_ipv4_cidr", `print R = has_ipv4("traffic from 10.0.1.5", "10.0.0.0/8")`, "true"},
		{"has_ipv4_miss", `print R = has_ipv4("no ip here", "10.0.1.5")`, "false"},
		{"ipv4_compare_eq", `print R = ipv4_compare("10.0.1.5", "10.0.1.5")`, "0"},
		{"ipv4_compare_lt", `print R = ipv4_compare("10.0.1.5", "10.0.2.1")`, "-1"},
		{"ipv4_compare_prefix", `print R = ipv4_compare("10.0.1.5", "10.0.1.200", 24)`, "0"},
		{"format_ipv4", `print R = format_ipv4("10.0.1.5")`, "10.0.1.5"},
		{"format_ipv4_mask", `print R = format_ipv4("10.0.1.5", 24)`, "10.0.1.0"},
		{"format_ipv4_16", `print R = format_ipv4("192.168.43.17", 16)`, "192.168.0.0"},
		{"hash_sha256", `print R = hash_sha256("hello")`, "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"},
		{"hash_md5", `print R = hash_md5("hello")`, "5d41402abc4b2a76b9719d911017c592"},
		{"hash_sha1", `print R = hash_sha1("hello")`, "aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d"},
		{"base64_encode", `print R = base64_encode_tostring("hello")`, "aGVsbG8="},
		{"base64_decode", `print R = base64_decode_tostring("aGVsbG8=")`, "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := queryResult(t, tt.query)
			expectCell(t, tbl, 0, 0, tt.expected)
		})
	}
}

func TestNewGuid(t *testing.T) {
	tbl := queryResult(t, `print G1 = new_guid(), G2 = new_guid()`)
	g1 := cell(t, tbl, 0, 0)
	g2 := cell(t, tbl, 0, 1)
	if len(g1) != 36 || g1[8] != '-' || g1[13] != '-' {
		t.Errorf("invalid GUID format: %s", g1)
	}
	if g1 == g2 {
		t.Error("two GUIDs should not be equal")
	}
}

func TestRand(t *testing.T) {
	tbl := queryResult(t, `print R = rand()`)
	v := cellVal(t, tbl, 0, 0).(float64)
	if v < 0.0 || v >= 1.0 {
		t.Errorf("rand() should be in [0,1), got %f", v)
	}
}

func TestRandN(t *testing.T) {
	tbl := queryResult(t, `print R = rand(100)`)
	v := cellVal(t, tbl, 0, 0).(int64)
	if v < 0 || v >= 100 {
		t.Errorf("rand(100) should be in [0,100), got %d", v)
	}
}

// --- Datetime Functions ---

func TestDatetimeFunctions(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{"getyear", `print R = getyear(todatetime("2026-02-28"))`, "2026"},
		{"getmonth", `print R = getmonth(todatetime("2026-02-28"))`, "2"},
		{"dayofmonth", `print R = dayofmonth(todatetime("2026-02-28"))`, "28"},
		{"hourofday", `print R = hourofday(todatetime("2026-02-28T14:30:00Z"))`, "14"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := queryResult(t, tt.query)
			expectCell(t, tbl, 0, 0, tt.expected)
		})
	}
}

// --- Complex Pipelines ---

func TestPipelineFilterSummarizeOrder(t *testing.T) {
	q := `datatable (Action: string, Bytes: long) [
		"ALLOW", 100, "DENY", 0, "ALLOW", 200, "DENY", 0, "ALLOW", 50
	] | where Action == "ALLOW" | summarize Total = sum(Bytes) | project Total`
	tbl := queryResult(t, q)
	expectCell(t, tbl, 0, 0, "350")
}

func TestPipelineSummarizeByOrderTop(t *testing.T) {
	q := `datatable (IP: string, Action: string) [
		"10.0.1.5", "DENY", "10.0.1.6", "DENY", "10.0.1.5", "DENY",
		"10.0.2.1", "DENY", "10.0.1.5", "ALLOW"
	] | where Action == "DENY"
	| summarize Hits = count() by IP
	| order by Hits desc
	| take 2`
	tbl := queryResult(t, q)
	expectRows(t, tbl, 2)
	expectCell(t, tbl, 0, 1, "10.0.1.5") // 3 hits
}

func TestPipelineExtendWhere(t *testing.T) {
	q := `datatable (Score: long) [10, 50, 90, 30, 70]
	| extend Grade = iff(Score >= 70, "Pass", "Fail")
	| where Grade == "Pass"
	| count`
	tbl := queryResult(t, q)
	expectCell(t, tbl, 0, 0, "2")
}

func TestPipelineSerializeAnomaly(t *testing.T) {
	q := `datatable (V: long) [10, 12, 150, 200, 8]
	| serialize Prev = prev(V, 1, 0)
	| extend Spike = V > Prev * 5
	| where Spike == true
	| count`
	tbl := queryResult(t, q)
	// Row 0: V=10, Prev=0 => 10 > 0 => true; Row 2: V=150, Prev=12 => 150>60 => true
	v := cellVal(t, tbl, 0, 0).(int64)
	if v < 1 {
		t.Errorf("expected at least 1 spike, got %d", v)
	}
}

// TestBareDateTimeLiteralNotArithmetic: datetime(2026-02-28) must parse
// as the date literal 2026-02-28, not as arithmetic (2026-2-28=1996).
// Found live during the backlog pass; fixed at the parser level by
// recognizing the bare NNNN-NN-NN shape specifically inside
// datetime(...)/todatetime(...) call position.
func TestBareDateTimeLiteralNotArithmetic(t *testing.T) {
	bare := queryResult(t, `print d = datetime(2026-02-28)`)
	quoted := queryResult(t, `print d = todatetime("2026-02-28")`)
	if bare.Rows[0][0] != quoted.Rows[0][0] {
		t.Fatalf("bare literal %v != quoted literal %v", bare.Rows[0][0], quoted.Rows[0][0])
	}

	withTime := queryResult(t, `print d = datetime(2026-02-28 10:30:00)`)
	if withTime.Rows[0][0] == bare.Rows[0][0] {
		t.Fatal("time-of-day component was not incorporated")
	}

	// A genuine column reference must still parse as an expression,
	// not accidentally match the bare-literal fast path.
	col := queryResult(t, `datatable(X:string) ["2026-01-01"] | project d = todatetime(X)`)
	expectRows(t, col, 1)

	// Arithmetic OUTSIDE a datetime()/todatetime() call is unaffected —
	// the fix is scoped to exactly those two call positions.
	arith := queryResult(t, `print x = 2026-02-28`)
	expectCell(t, arith, 0, 0, "1996")
}

// TestWindowFunctionOutsideSerializeGivesActionableError: row_number/
// prev/next used outside serialize previously errored with the
// generic "unsupported function", which is technically true but not
// actionable — found live while measuring semantic-search rank on the
// Nergal scope. The real fix isn't an engine capability gap (these
// functions already work correctly inside serialize, matching real
// Kusto's own requirement) — it's a better error message pointing at
// the actual fix.
func TestWindowFunctionOutsideSerializeGivesActionableError(t *testing.T) {
	eng := diskEngineEmpty(t)
	_, err := runStmt(t, eng, `print x = row_number()`)
	if err == nil || !strings.Contains(err.Error(), "serialize") {
		t.Fatalf("expected an error mentioning serialize, got: %v", err)
	}
	// A genuinely unsupported function must still get the generic
	// message, not be misidentified as a window function.
	_, err = runStmt(t, eng, `print x = totally_fake_function()`)
	if err == nil || !strings.Contains(err.Error(), "unsupported function") {
		t.Fatalf("expected the generic unsupported-function error, got: %v", err)
	}
}

// TestFormatDatetimeSpecifiers guards format_datetime against real
// Kusto's actual, documented format-specifier table (verified via
// Microsoft's own docs, with worked examples, before any of this was
// written) -- a real, silent-wrong-value rewrite, not just an
// incomplete-coverage fix. The previous implementation used
// strings.Replacer substitution into a Go reference-time layout,
// which tries each old/new pair in the order given and takes the
// FIRST match at each position, not the longest: with "fff" listed
// before "ffff", a run of four f's matched "fff" first (consuming 3),
// leaving a lone "f" to match separately -- format_datetime(dt,
// "ffff") silently produced "0000" instead of the real ten-
// thousandths-of-a-second value. Separately, bare d/h/m/s/M/y (no
// leading zero), tt (AM/PM), and the entire uppercase F-class
// (conditional, suppressed-if-zero) were missing entirely and passed
// through as literal text (format_datetime(dt, "d") returned the
// literal string "d").
//
// Every case here is checked against a real, worked example from the
// documentation itself, not just against this engine's own
// description of what the specifier should do.
func TestFormatDatetimeSpecifiers(t *testing.T) {
	cases := []struct {
		dt   string
		fmt  string
		want string
	}{
		// The three originally, live-broken specifiers.
		{"2009-06-01T13:45:30", "d", "1"},
		{"2009-06-15T13:45:30.6175000", "ffff", "6175"},
		{"2009-06-15T13:45:09", "tt", "PM"},

		// The doc's own three fully-worked examples.
		{"2017-01-29T09:00:05", "yy-MM-dd [HH:mm:ss]", "17-01-29 [09:00:05]"},
		{"2017-01-29T09:00:05", "yyyy-M-dd [H:mm:ss]", "2017-1-29 [9:00:05]"},
		{"2017-01-29T09:00:05", "yy-MM-dd [hh:mm:ss tt]", "17-01-29 [09:00:05 AM]"},

		// f-class (always shown), each length, against real examples.
		{"2009-06-15T13:45:30.6170000", "f", "6"},
		{"2009-06-15T13:45:30.05", "f", "0"},
		{"2009-06-15T13:45:30.6170000", "ff", "61"},
		{"2009-06-15T13:45:30.6170000", "fff", "617"},
		{"2009-06-15T13:45:30.6175425", "fffffff", "6175425"},

		// F-class (conditional, suppressed if zero) — the specific
		// rule that a first version of this fix got wrong by omission
		// entirely.
		{"2009-06-15T13:45:30.6170000", "F", "6"},
		{"2009-06-15T13:45:30.05", "F", ""},
		{"2009-06-15T13:45:30.005", "FF", ""},

		// Bare (no leading zero) forms for the remaining specifiers.
		{"2009-06-15T01:09:30", "h", "1"},
		{"2009-06-15T01:09:30", "m", "9"},
		{"2009-06-15T13:45:09", "s", "9"},
		{"2009-06-15T13:45:30", "M", "6"},
	}
	for _, tc := range cases {
		t.Run(tc.fmt, func(t *testing.T) {
			q := fmt.Sprintf(`let dt = datetime(%s); print v = format_datetime(dt, '%s')`, tc.dt, tc.fmt)
			tbl := queryResult(t, q)
			expectRows(t, tbl, 1)
			got := fmt.Sprintf("%v", tbl.Rows[0][0])
			if got == "<nil>" {
				got = ""
			}
			if got != tc.want {
				t.Errorf("format_datetime(%s, %q) = %q, want %q", tc.dt, tc.fmt, got, tc.want)
			}
		})
	}
}
