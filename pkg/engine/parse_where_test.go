package engine

import "testing"

// parse_where_test.go — the parse-where operator, verified against real
// ADX's own documented worked example (parse-where-operator.md) and
// against this engine's own ParseOp to confirm the one real semantic
// difference: parse-where drops non-matching rows, plain parse keeps
// them with the new columns left null.

// TestParseWhereDropsNonMatching guards the core, documented difference
// from parse: "Only successfully parsed strings will be in the output.
// Strings that don't match the pattern will be filtered out."
func TestParseWhereDropsNonMatching(t *testing.T) {
	result := queryResult(t, `datatable(EventText:string) [
		"totalSlices=27, sliceNumber=15",
		"not a matching line at all"]
		| parse-where EventText with "totalSlices=" totalSlices ", sliceNumber=" sliceNumber
		| project totalSlices, sliceNumber`)

	if result.RowCount() != 1 {
		t.Fatalf("expected 1 row (non-matching row dropped), got %d", result.RowCount())
	}
	tsIdx := result.Schema.ColumnIndex("totalSlices")
	snIdx := result.Schema.ColumnIndex("sliceNumber")
	if got := result.Rows[0][tsIdx]; got != "27" {
		t.Errorf("totalSlices = %v, want 27", got)
	}
	if got := result.Rows[0][snIdx]; got != "15" {
		t.Errorf("sliceNumber = %v, want 15", got)
	}
}

// TestParseWhereVsParseRowCount confirms plain parse keeps BOTH rows
// (the non-matching one with null new columns) on the exact same input
// where parse-where keeps only one — a direct, same-input contrast
// rather than trusting each operator's own isolated test.
func TestParseWhereVsParseRowCount(t *testing.T) {
	parseResult := queryResult(t, `datatable(X:string) ["A=1", "no match"]
		| parse X with "A=" val`)
	whereResult := queryResult(t, `datatable(X:string) ["A=1", "no match"]
		| parse-where X with "A=" val`)

	if parseResult.RowCount() != 2 {
		t.Errorf("plain parse: expected 2 rows (unmatched kept null), got %d", parseResult.RowCount())
	}
	if whereResult.RowCount() != 1 {
		t.Errorf("parse-where: expected 1 row (unmatched dropped), got %d", whereResult.RowCount())
	}
}

// TestParseWhereKindRegex confirms kind=regex works the same as it does
// for plain parse — parse-where shares the full pattern grammar, not
// just the simple/relaxed kinds.
//
// Query updated 2026-08-18: the previous version used
// `"id-(?P<num>[0-9]+)"` as a single named-group regex literal — that
// was testing this engine's OLD, incorrect kind=regex model (a single
// hand-written regex with named capture groups), which real ADX's own
// docs confirm is not real KQL syntax at all; real kind=regex uses the
// exact same fragment syntax as kind=simple (`with "str" col ...`),
// just interpreting each literal fragment as a regex snippet instead
// of exact text — see operators.go's applyParseCore for the full
// rewrite this test now exercises correctly.
func TestParseWhereKindRegex(t *testing.T) {
	result := queryResult(t, `datatable(X:string) ["id-42", "nope"]
		| parse-where kind=regex X with "id-" num
		| project num`)
	if result.RowCount() != 1 {
		t.Fatalf("expected 1 row, got %d", result.RowCount())
	}
	numIdx := result.Schema.ColumnIndex("num")
	if got := result.Rows[0][numIdx]; got != "42" {
		t.Errorf("num = %v, want 42", got)
	}
}

// TestParseWhereNoMatches confirms an all-non-matching input produces
// an empty (but schema-correct) result, not an error.
func TestParseWhereNoMatches(t *testing.T) {
	result := queryResult(t, `datatable(X:string) ["nope", "still nope"]
		| parse-where X with "A=" val`)
	if result.RowCount() != 0 {
		t.Fatalf("expected 0 rows, got %d", result.RowCount())
	}
	if result.Schema.ColumnIndex("val") < 0 {
		t.Errorf("expected 'val' column present in schema even with 0 rows")
	}
}

