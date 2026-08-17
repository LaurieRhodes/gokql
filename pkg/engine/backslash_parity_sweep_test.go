package engine

import "testing"

// backslash_parity_sweep_test.go — end-to-end regression tests for the
// 2026-08-17 sweep of the 9 backslash-parity call sites left over from
// Sprint 7's own fix (findMatchingParen, findMatchingBracket,
// findMatchingBrace, isTabularExpression, tryParseFunctionDef,
// splitStatements, findTopLevelKeyword, splitDataTableValues,
// assignmentEqIndex — see pkg/parser/backslash_parity_test.go for the
// unit-level precededByOddBackslashes tests and the full site list).
// Each test below exercises its function through a real, top-level
// query, since most of these functions are unexported and only
// reachable through full parsing/execution — specifically via a
// string argument ending in a doubled backslash (KQL's own escape for
// one literal backslash) immediately before content the old, naive
// "preceded by exactly one backslash" check would have misparsed as
// still "inside a string."

// TestFindMatchingParenDoubledBackslash guards findMatchingParen — the
// most widely-used of the 9 (function-call argument lists throughout
// the parser). A string literal argument ending in \\ immediately
// before the closing ) must not desync the paren depth count.
func TestFindMatchingParenDoubledBackslash(t *testing.T) {
	result := queryResult(t, `print r = strlen('a\\')`)
	got, ok := result.Rows[0][0].(int64)
	if !ok || got != 2 {
		t.Fatalf("strlen('a\\\\') = %v, want 2 (one literal backslash + 'a')", result.Rows[0][0])
	}
}

// TestFindMatchingBracketDoubledBackslash guards findMatchingBracket
// via a dynamic array literal containing a string ending in \\
// immediately before the closing ].
func TestFindMatchingBracketDoubledBackslash(t *testing.T) {
	result := queryResult(t, `print r = dynamic(["a\\", "b"])`)
	arr := seriesJSONArray(t, result.Rows[0][0].(string))
	if arr[0] != `a\` {
		t.Errorf("dynamic array elem 0 = %q, want %q", arr[0], `a\`)
	}
}

// TestSplitStatementsDoubledBackslash guards splitStatements — a
// semicolon-separated multi-statement query where an earlier
// statement's own string literal ends in \\ right before its closing
// quote must not swallow the later statement into the same segment.
func TestSplitStatementsDoubledBackslash(t *testing.T) {
	result := queryResult(t, `let x = 'a\\'; print r = 1`)
	got, ok := result.Rows[0][0].(int64)
	if !ok || got != 1 {
		t.Fatalf("expected the second statement (print r = 1) to execute on its own, got %v", result.Rows[0][0])
	}
}

// TestAssignmentEqIndexDoubledBackslash guards assignmentEqIndex via a
// print assignment whose right-hand string literal ends in \\
// immediately before a later, real '=' the parser must still find
// correctly (not get confused into treating the whole rest of the
// clause as still "inside" the first string).
func TestAssignmentEqIndexDoubledBackslash(t *testing.T) {
	result := queryResult(t, `print a = 'x\\', b = 5`)
	aIdx := result.Schema.ColumnIndex("a")
	bIdx := result.Schema.ColumnIndex("b")
	if result.Rows[0][aIdx] != `x\` {
		t.Errorf("a = %q, want %q", result.Rows[0][aIdx], `x\`)
	}
	got, ok := result.Rows[0][bIdx].(int64)
	if !ok || got != 5 {
		t.Fatalf("b = %v, want int64(5) (assignmentEqIndex must find the real second '=')", result.Rows[0][bIdx])
	}
}

// TestDataTableDoubledBackslash guards splitDataTableValues — a
// datatable literal string value ending in \\ right before its comma
// separator must not desync the value-splitting.
func TestDataTableDoubledBackslash(t *testing.T) {
	result := queryResult(t, `datatable(x:string, y:long) ["a\\", 5]`)
	xIdx := result.Schema.ColumnIndex("x")
	yIdx := result.Schema.ColumnIndex("y")
	if result.Rows[0][xIdx] != `a\` {
		t.Errorf("x = %q, want %q", result.Rows[0][xIdx], `a\`)
	}
	got, ok := result.Rows[0][yIdx].(int64)
	if !ok || got != 5 {
		t.Fatalf("y = %v, want int64(5)", result.Rows[0][yIdx])
	}
}

