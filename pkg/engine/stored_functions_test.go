package engine

import (
	"testing"
)

func storedFunctionsTestScope(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table Tasks (Id: string, Status: string, Priority: string)`)
	diskExec(t, eng, `.set-or-append Tasks <| datatable(Id:string, Status:string, Priority:string) `+
		`["t1","open","high","t2","blocked","low","t3","open","medium"]`)
	return eng
}

// TestStoredFunctionCreateAndCall guards the core round trip: define,
// then call as a query source, with real result data — not just that
// the commands run without error.
func TestStoredFunctionCreateAndCall(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function OpenTasks() { Tasks | where Status == "open" }`)

	tbl := diskQuery(t, eng, `OpenTasks() | sort by Id asc`)
	expectRows(t, tbl, 2)
	expectCell(t, tbl, 0, 0, "t1")
	expectCell(t, tbl, 1, 0, "t3")
}

// TestStoredFunctionComposesAsPipeSource guards the exact composability
// requirement this feature exists for: OpenTasks() | where ... must
// parse and execute correctly, not just a bare call with no further
// pipeline. Verified against real data with a specific expected row,
// not just a row count.
func TestStoredFunctionComposesAsPipeSource(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function OpenTasks() { Tasks | where Status == "open" }`)

	tbl := diskQuery(t, eng, `OpenTasks() | where Priority == "high"`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "t1")
}

// TestStoredFunctionShowFunctionsEmitsDocString guards that .show
// functions surfaces DocString correctly — required for a small model
// to discover what a function does without reading its body.
func TestStoredFunctionShowFunctionsEmitsDocString(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function with (docstring="Open tasks only", folder="MyFolder") OpenTasks() { Tasks | where Status == "open" }`)

	tbl := diskQuery(t, eng, `.show functions`)
	expectRows(t, tbl, 1)
	nameIdx := tbl.Schema.ColumnIndex("Name")
	docIdx := tbl.Schema.ColumnIndex("DocString")
	folderIdx := tbl.Schema.ColumnIndex("Folder")
	if tbl.Rows[0][nameIdx] != "OpenTasks" {
		t.Errorf("expected Name=OpenTasks, got %v", tbl.Rows[0][nameIdx])
	}
	if tbl.Rows[0][docIdx] != "Open tasks only" {
		t.Errorf("expected DocString to round-trip correctly, got %v", tbl.Rows[0][docIdx])
	}
	if tbl.Rows[0][folderIdx] != "MyFolder" {
		t.Errorf("expected Folder to round-trip correctly, got %v", tbl.Rows[0][folderIdx])
	}
}

// TestStoredFunctionCreateWithoutOrAlterErrorsOnRedefinition guards
// real ADX's documented distinction: plain .create function errors on
// an existing name (unlike .create-or-alter, which upserts).
func TestStoredFunctionCreateWithoutOrAlterErrorsOnRedefinition(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create function F() { Tasks | take 1 }`)
	diskQueryError(t, eng, `.create function F() { Tasks | take 2 }`)
}

// TestStoredFunctionCreateIfNotExistsIsNoOp guards that ifnotexists on
// an existing function is a true no-op (the original definition still
// resolves), not silently replaced.
func TestStoredFunctionCreateIfNotExistsIsNoOp(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create function F() { Tasks | where Status == "open" }`)
	diskExec(t, eng, `.create function ifnotexists F() { Tasks | take 1 }`)

	tbl := diskQuery(t, eng, `F() | count`)
	expectCell(t, tbl, 0, 0, "2") // still the ORIGINAL definition (2 open tasks), not "take 1"
}

// TestStoredFunctionTableNameCollisionRejected guards the namespace
// check inferred from real ADX's materialized-view docs (tables,
// functions, and views share one namespace) — a function can't be
// defined with the same name as an existing table.
func TestStoredFunctionTableNameCollisionRejected(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskQueryError(t, eng, `.create-or-alter function Tasks() { Tasks | take 1 }`)
}

// TestStoredFunctionDropSingularErrorsWhenMissing guards .drop
// function Name's documented singular semantics — errors if the
// function doesn't exist, distinct from the plural form below.
func TestStoredFunctionDropSingularErrorsWhenMissing(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskQueryError(t, eng, `.drop function NoSuchFunction`)
}

// TestStoredFunctionDropAlreadyDroppedErrors guards a real bug found
// live during manual testing: a function that was already dropped is
// "everDefined" (its tombstone row is real history in _Functions) but
// must still be treated as not currently existing — dropping it again
// via the singular form must error, not silently "succeed" a second
// time. The first version of applyDropFunction only checked
// !everDefined and missed this case entirely.
func TestStoredFunctionDropAlreadyDroppedErrors(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function F() { Tasks | take 1 }`)
	diskExec(t, eng, `.drop function F`)
	diskQueryError(t, eng, `.drop function F`)
}

// TestStoredFunctionDropPluralToleratesMissing guards .drop functions
// (A, B, C)'s documented plural semantics — silently tolerates
// missing (or already-dropped) names, returns the list of REMAINING
// functions, rather than erroring.
func TestStoredFunctionDropPluralToleratesMissing(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function A() { Tasks | take 1 }`)
	diskExec(t, eng, `.create-or-alter function B() { Tasks | take 2 }`)

	tbl := diskQuery(t, eng, `.drop functions (A, NoSuchFunction)`)
	expectRows(t, tbl, 1)
	nameIdx := tbl.Schema.ColumnIndex("Name")
	if tbl.Rows[0][nameIdx] != "B" {
		t.Errorf("expected only B remaining after dropping A (and tolerating the missing name), got %v", tbl.Rows[0][nameIdx])
	}
}

// TestStoredFunctionDroppedFunctionNoLongerCallable guards the actual
// end-to-end effect of a drop: calling a dropped function must error,
// not silently return the old definition's results. This is the
// specific case the CreatedAt-as-time.Time bug (see below) broke.
func TestStoredFunctionDroppedFunctionNoLongerCallable(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function F() { Tasks | take 1 }`)
	diskExec(t, eng, `.drop function F`)
	diskQueryError(t, eng, `F()`)
}

// TestStoredFunctionCreatedAtStoredAsRealTimestamp guards a real,
// live-caught bug directly: an earlier version of this code wrote
// time.Now() (a time.Time) into a datetime column instead of
// time.Now().UTC().UnixNano() (an int64) — the representation
// types.ParseValue itself actually uses for TypeDatetime. The storage
// layer didn't recognize time.Time for a datetime column and silently
// stored the zero value for EVERY row, so "latest by CreatedAt"
// degenerated into "first row encountered wins" — every comparison
// tied, so a dropped function's tombstone could never beat its own
// original definition, and TestStoredFunctionDroppedFunctionNoLongerCallable
// above would have failed (calling a "dropped" function still worked)
// without this specific fix. Asserts directly against the real
// _Functions table's stored value, not just the higher-level symptom,
// so a future regression of the representation itself is caught here
// even if some other part of the drop logic happened to mask the
// symptom.
func TestStoredFunctionCreatedAtStoredAsRealTimestamp(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function F() { Tasks | take 1 }`)

	tbl := diskQuery(t, eng, `_Functions | project CreatedAt`)
	expectRows(t, tbl, 1)
	created, ok := tbl.Rows[0][0].(int64)
	if !ok {
		t.Fatalf("expected CreatedAt stored as int64 (UnixNano), got %T: %v", tbl.Rows[0][0], tbl.Rows[0][0])
	}
	// A real Unix nanosecond timestamp for "now" is a huge number
	// (2026 is roughly 1.7e18 ns since epoch) -- the zero-value bug
	// this test guards against would show exactly 0 here.
	if created == 0 {
		t.Fatal("CreatedAt stored as zero -- the exact symptom of the time.Time-instead-of-UnixNano bug")
	}
}

// TestStoredFunctionRecursionDetected guards real ADX's explicit
// "functions can call other functions (recursiveness isn't
// supported)" rule -- here it's not just conformance, an unguarded
// cycle would be genuine infinite recursion through repeated
// Parse+Execute calls.
func TestStoredFunctionRecursionDetected(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function FuncA() { FuncB() }`)
	diskExec(t, eng, `.create-or-alter function FuncB() { FuncA() }`)
	diskQueryError(t, eng, `FuncA()`)
}

// TestStoredFunctionCallsNonRecursiveFunction guards that functions
// calling OTHER (non-cyclic) functions still works correctly -- real
// ADX explicitly allows this; only a genuine cycle is rejected.
func TestStoredFunctionCallsNonRecursiveFunction(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function AllTasks() { Tasks }`)
	diskExec(t, eng, `.create-or-alter function OpenOnly() { AllTasks() | where Status == "open" }`)

	tbl := diskQuery(t, eng, `OpenOnly() | count`)
	expectCell(t, tbl, 0, 0, "2")
}

// TestStoredFunctionScalarParameter guards the core motivating case
// this increment exists for: CascadeCheck("F123")-style single-value
// lookups. Verified against real data with a specific expected row,
// not just that the call succeeds.
func TestStoredFunctionScalarParameter(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function CascadeCheck(cardId: string) { Tasks | where Id == cardId }`)

	tbl := diskQuery(t, eng, `CascadeCheck("t1")`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "t1")
}

// TestStoredFunctionParameterComposesAsPipeSource guards that a
// parameterized call still composes with a following pipeline exactly
// like the parameterless case already does.
func TestStoredFunctionParameterComposesAsPipeSource(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function ByStatus(s: string) { Tasks | where Status == s }`)

	tbl := diskQuery(t, eng, `ByStatus("open") | where Priority == "high"`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "t1")
}

// TestStoredFunctionDefaultParameterValue guards that an omitted
// TRAILING argument correctly falls back to its declared default, and
// that supplying a value still overrides it.
func TestStoredFunctionDefaultParameterValue(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function ByStatus(status: string = "open") { Tasks | where Status == status }`)

	tbl := diskQuery(t, eng, `ByStatus() | count`)
	expectCell(t, tbl, 0, 0, "2") // default "open" -> t1, t3

	tbl = diskQuery(t, eng, `ByStatus("blocked") | count`)
	expectCell(t, tbl, 0, 0, "1") // explicit override -> t2
}

// TestStoredFunctionMissingRequiredArgumentErrors guards that omitting
// a required (no-default) argument is a clear error, not silently
// bound to null or zero.
func TestStoredFunctionMissingRequiredArgumentErrors(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function ByStatus(status: string) { Tasks | where Status == status }`)
	diskQueryError(t, eng, `ByStatus()`)
}

// TestStoredFunctionTooManyArgumentsErrors guards the arity check on
// the other side — more arguments than declared parameters.
func TestStoredFunctionTooManyArgumentsErrors(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function ByStatus(status: string) { Tasks | where Status == status }`)
	diskQueryError(t, eng, `ByStatus("open", "extra")`)
}

// TestStoredFunctionArgumentTypeMismatchErrors guards type checking
// against the DECLARED parameter type, using a case that would be
// invisible to a runtime-value-based check: a plain long (42) passed
// where datetime is declared. Both are internally just int64, so only
// checking the evaluated value's Go type could never catch this --
// this specifically exercises inferExprType (the same expression-
// based type inferrer reused to fix print's datetime bug earlier this
// session) being applied to the ARGUMENT EXPRESSION itself.
func TestStoredFunctionArgumentTypeMismatchErrors(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function AsOf(cutoff: datetime) { Tasks }`)
	diskQueryError(t, eng, `AsOf(42)`)

	// The correctly-typed call must still succeed.
	tbl := diskQuery(t, eng, `AsOf(datetime(2026-01-01)) | count`)
	expectCell(t, tbl, 0, 0, "3")
}

// TestStoredFunctionMultipleParameters guards positional binding
// across more than one parameter, verified against a specific
// computed intersection, not just a row count.
func TestStoredFunctionMultipleParameters(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function Filtered(s: string, p: string) { Tasks | where Status == s and Priority == p }`)

	tbl := diskQuery(t, eng, `Filtered("open", "high")`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "t1")
}

// TestColumnIndexCaseSensitive guards a real, live bug found while
// testing scalar parameters, not specific to stored functions at all:
// Schema.ColumnIndex was case-INSENSITIVE, so a scalar let binding (or
// stored function parameter) whose name case-insensitively matched an
// existing column resolved as that SAME column instead of falling
// through to the let/parameter binding. `let status = "active";
// T | where Status == status` silently became `where Status ==
// Status` -- a tautology matching every row, not a filter, with no
// error at all. Verified against real Kusto's own docs before fixing:
// "KQL is case-sensitive for everything." lowerCamelCase parameter
// names next to PascalCase columns (cardId vs Id, status vs Status)
// is an entirely natural, idiomatic convention -- this bug would have
// made stored-function parameters unreliable in exactly the cases
// most likely to occur in practice.
func TestColumnIndexCaseSensitive(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	tbl := diskQuery(t, eng, `let status = "open"; Tasks | where Status == status`)
	expectRows(t, tbl, 2) // t1, t3 -- NOT all 3, which is what the tautology bug produced
}

// --- Tabular parameters ---
//
// Verified against real ADX's own worked examples (user-defined-
// functions docs) before implementing any of this: MyFilter =
// (T:(x:long), v:long) { T | where x >= v }, called as
// MyFilter((range x from 1 to 10 step 1), 9). This engine has no
// range operator at all (a separate, unrelated, pre-existing gap,
// confirmed by testing `range x from 1 to 10 step 1` fails identically
// standalone, with no function involved) -- these tests use a real
// table or a datatable literal as the tabular argument instead, which
// exercise exactly the same binding path.

// TestStoredFunctionTabularParameter guards the core round trip for a
// tabular parameter with a declared, validated schema — a real table
// passed as the argument, filtered inside the function body by a
// column that only exists because the tabular binding worked.
func TestStoredFunctionTabularParameter(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function ByPriority(T:(Priority: string), p: string) { T | where Priority == p }`)

	tbl := diskQuery(t, eng, `ByPriority(Tasks, "high")`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "t1")
}

// TestStoredFunctionTabularParameterWithDatatableLiteral guards that a
// datatable LITERAL (not just a named table) works as a tabular
// argument, matching real ADX's own call syntax of wrapping the
// argument expression in its own parens.
func TestStoredFunctionTabularParameterWithDatatableLiteral(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function AboveX(T:(x: long), threshold: long) { T | where x >= threshold }`)

	tbl := diskQuery(t, eng, `AboveX((datatable(x:long) [5, 12, 20]), 10)`)
	expectRows(t, tbl, 2)
}

// TestStoredFunctionTabularAnySchema guards the T:(*) form — real
// ADX's "any tabular schema" wildcard — accepting an argument WITHOUT
// validating its columns against any declared schema at all.
func TestStoredFunctionTabularAnySchema(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function CountRows(T:(*)) { T | count }`)

	tbl := diskQuery(t, eng, `CountRows(Tasks)`)
	expectCell(t, tbl, 0, 0, "3")
}

// TestStoredFunctionTabularSchemaMismatchRejected guards that a
// declared (non-*) tabular schema is actually validated against the
// argument's real columns — an argument missing a declared column
// must be a clear error, not silently proceed with a missing/null
// column inside the function body.
func TestStoredFunctionTabularSchemaMismatchRejected(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function NeedsScore(T:(Score: long)) { T | count }`)
	diskQueryError(t, eng, `NeedsScore(Tasks)`) // Tasks has no Score column
}

// TestStoredFunctionTabularBeforeScalarOrderEnforced guards real
// ADX's own documented rule: "when using both tabular input arguments
// and scalar input arguments, put all tabular input arguments before
// the scalar input arguments." A definition violating this must be
// rejected at CREATE time, not silently accepted and misbehave later.
func TestStoredFunctionTabularBeforeScalarOrderEnforced(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskQueryError(t, eng, `.create-or-alter function Bad(v: long, T:(x: long)) { T | where x >= v }`)
}

// TestStoredFunctionMissingTabularArgumentErrors guards that a
// tabular parameter, which real ADX never allows a default value for,
// is always a hard error when omitted — never silently skipped or
// bound to an empty table.
func TestStoredFunctionMissingTabularArgumentErrors(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function NeedsTable(T:(x: long)) { T | count }`)
	diskQueryError(t, eng, `NeedsTable()`)
}

// TestStoredFunctionTwoTabularParameters guards multiple tabular
// parameters together (both declared BEFORE the trailing scalar,
// matching the enforced ordering), combined inside the function body
// — verified against real computed values, not just that it runs.
func TestStoredFunctionTwoTabularParameters(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create table More (x: long)`)
	diskExec(t, eng, `.set-or-append More <| datatable(x:long) [100,200]`)
	diskExec(t, eng, `.create table Nums (x: long)`)
	diskExec(t, eng, `.set-or-append Nums <| datatable(x:long) [1,2,3]`)

	diskExec(t, eng, `.create-or-alter function Combine(A:(x:long), B:(x:long), threshold:long) { A | union B | where x >= threshold }`)

	tbl := diskQuery(t, eng, `Combine(Nums, More, 50) | sort by x asc`)
	expectRows(t, tbl, 2)
	xIdx := tbl.Schema.ColumnIndex("x")
	if tbl.Rows[0][xIdx] != int64(100) || tbl.Rows[1][xIdx] != int64(200) {
		t.Errorf("expected [100, 200], got [%v, %v]", tbl.Rows[0][xIdx], tbl.Rows[1][xIdx])
	}
}

// TestStoredFunctionShowDisplaysRealTabularParameterText guards that
// .show function(s) shows the actual tabular schema syntax, not an
// approximation — confirms the "keep raw text, don't reconstruct"
// storage convention already established for scalar parameters
// extends to tabular ones with no changes needed at all (the same
// Parameters column already stores whatever text was written,
// verified here specifically for the tabular shape).
func TestStoredFunctionShowDisplaysRealTabularParameterText(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function ByPriority(T:(Priority: string), p: string) { T | where Priority == p }`)

	tbl := diskQuery(t, eng, `.show function ByPriority`)
	expectRows(t, tbl, 1)
	paramsIdx := tbl.Schema.ColumnIndex("Parameters")
	if tbl.Rows[0][paramsIdx] != "(T:(Priority: string), p: string)" {
		t.Errorf("expected exact original tabular parameter text, got %v", tbl.Rows[0][paramsIdx])
	}
}

// --- let inside a stored-function body ---
//
// Guards a real, live bug found while testing (a different model,
// Kimi): splitStatements (parser.go) tracked only (...) nesting when
// deciding whether a semicolon was a top-level statement separator,
// never {...} at all -- so the ; inside a function body's own
// let s = ...; Body braces was treated as splitting the TOP-LEVEL
// .create-or-alter function command itself, silently discarding the
// let binding entirely (it evaporated with no error at all, later
// surfacing as "column not found" when the body referenced it) and
// leaving the function body's own closing brace glued onto its final
// operator's text ("unknown operator: \"count }\"").

// TestStoredFunctionLetInBody guards the core, minimal case directly.
func TestStoredFunctionLetInBody(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function TestFn() { let s = "open"; Tasks | where Status == s | count }`)

	tbl := diskQuery(t, eng, `TestFn()`)
	expectCell(t, tbl, 0, 0, "2") // t1, t3
}

// TestStoredFunctionLetPlusToscalarInBody guards the actual,
// real-world motivating combination Kimi needed for
// LatestSessionActivity(): let and toscalar() together inside a
// single function body.
func TestStoredFunctionLetPlusToscalarInBody(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function TestFn() { let total = toscalar(Tasks | summarize count()); Tasks | where Status == "open" | count }`)

	tbl := diskQuery(t, eng, `TestFn()`)
	expectCell(t, tbl, 0, 0, "2")
}

// TestStoredFunctionMultipleLetsInBody guards more than one let
// statement inside a single function body.
func TestStoredFunctionMultipleLetsInBody(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function TestFn() { let a = "open"; let b = "blocked"; Tasks | where Status == a or Status == b | count }`)

	tbl := diskQuery(t, eng, `TestFn()`)
	expectCell(t, tbl, 0, 0, "3") // t1, t2, t3
}

// --- let inside a tabular argument's own text ---
//
// Guards two real, live bugs found via systematic review of tabular
// stored-function parameters, after the fact rather than assumed
// complete: a call like MyFilter((let x = 10; T | where Y >= x), 9)
// -- a tabular argument whose own text has its own let binding --
// previously failed two separate ways in sequence.

// TestTabularArgumentWithOwnLetBinding guards the core case directly:
// parseTabularArgument (stored_functions.go) used to require the
// parsed argument to be exactly a *parser.Query, but a tabular
// argument with its own let binding parses as a
// *parser.CompoundStatement instead, rejected outright with a
// genuinely misleading error ("must be a tabular expression, not a
// management command" -- it's neither). Widened to accept both.
func TestTabularArgumentWithOwnLetBinding(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	// Exercised against the real, disk-backed Tasks table (not an
	// in-memory datatable literal) per this session's own established
	// discipline -- an in-memory-only test previously masked a real
	// planner bug earlier this same session.
	diskExec(t, eng, `.create-or-alter function TasksFiltered(T:(Id:string,Status:string), s:string) { T | where Status == s }`)

	tbl := diskQuery(t, eng, `TasksFiltered((let want = "open"; Tasks | where Status == want), "open")`)
	if len(tbl.Rows) == 0 {
		t.Fatalf("expected at least one row from a tabular argument with its own let binding")
	}
	statusIdx := tbl.Schema.ColumnIndex("Status")
	for _, row := range tbl.Rows {
		if row[statusIdx] != "open" {
			t.Errorf("expected every row to have Status=open, got %v", row[statusIdx])
		}
	}
}

// TestExecuteCompoundRestoresPriorLetContextNotNil directly guards the
// second, deeper bug the case above exposed: executeCompound's own
// defer used to unconditionally clear activeLetContext to nil on
// exit, correct for a top-level call (where the prior value already
// IS nil) but a real, live bug for a NESTED one -- a tabular
// argument's own let-bearing text is validated via a nested
// e.Execute call (bindStoredFunctionArgs), which itself recurses into
// executeCompound. The inner call's unconditional-to-nil cleanup wiped
// out the OUTER, function-level let context (which still held the
// function's OTHER, already-bound parameters) partway through
// evaluating the outer context's own remaining let bindings, well
// before the outer call's own defer had a chance to run. Surfaced
// originally as a scalar parameter (v) appearing to vanish --
// "column v not found" -- despite having been correctly bound moments
// earlier. Fixed by saving and restoring the PRIOR context instead of
// unconditionally clearing it, the same principle already applied
// twice this session for the identical class of nested-call bug.
func TestExecuteCompoundRestoresPriorLetContextNotNil(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function FilterByStatus(T:(Id:string,Status:string), s:string) { T | where Status == s }`)

	// The scalar parameter (s) must survive the nested let-context
	// switch triggered by validating the tabular argument's own
	// let-bearing text. "blocked" -- not "closed" -- matches the real
	// fixture data (storedFunctionsTestScope's own Tasks rows).
	tbl := diskQuery(t, eng, `FilterByStatus((let want = "blocked"; Tasks | where Status == want), "blocked")`)
	if len(tbl.Rows) == 0 {
		t.Fatalf("expected at least one row; a passing result at all confirms the scalar parameter 's' survived the nested let-context switch")
	}
	statusIdx := tbl.Schema.ColumnIndex("Status")
	for _, row := range tbl.Rows {
		if row[statusIdx] != "blocked" {
			t.Errorf("expected every row to have Status=blocked (the scalar arg), got %v -- the outer let context was corrupted", row[statusIdx])
		}
	}
}

// TestTabularArgumentWithToscalar guards toscalar() used inside a
// tabular argument's own filter — a combination that didn't exist
// when tabular parameters were first built (toscalar came later, this
// same session) and had never been exercised together until this
// systematic review.
func TestTabularArgumentWithToscalar(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function FilterByStatus2(T:(Id:string,Status:string), s:string) { T | where Status == s }`)

	// toscalar() picks the status value from whichever row has the
	// alphabetically first Id — real data, not a synthetic constant,
	// verified against the known, real Tasks fixture (t1=open).
	tbl := diskQuery(t, eng, `FilterByStatus2((Tasks | where Status == toscalar(Tasks | where Id == "t1" | project Status)), "open")`)
	if len(tbl.Rows) == 0 {
		t.Fatalf("expected at least one row matching the toscalar()-derived status")
	}
}

// TestNestedFunctionCallAsTabularArgument guards one stored function's
// result passed directly as another's tabular argument.
func TestNestedFunctionCallAsTabularArgument(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function AllTasks() { Tasks }`)
	diskExec(t, eng, `.create-or-alter function OpenOnly(T:(Id:string,Status:string), s:string) { T | where Status == s }`)

	tbl := diskQuery(t, eng, `OpenOnly((AllTasks()), "open")`)
	if len(tbl.Rows) == 0 {
		t.Fatalf("expected at least one open task via a nested function call as the tabular argument")
	}
}

// TestLetBoundTableAsTabularArgument guards a real, live bug found and
// fixed while implementing the invoke operator (2026-08-15): a
// caller-scope `let`-bound table passed as a stored function's own
// tabular argument previously failed with "table not found" — the
// argument's AST was carried forward to be re-executed a second time
// INSIDE the callee's own executeCompound call, by which point
// executeCompound had already installed its own fresh LetContext
// (which naturally doesn't contain the caller's own `let` bindings).
// See PrecomputedTable's own doc comment (pkg/parser/ast.go) for the
// full fix: the argument is now executed exactly once, in the
// caller's still-current LetContext, and its result carried forward
// directly rather than re-derived from AST.
func TestLetBoundTableAsTabularArgument(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function DoubleIt(T:(x:long)) { T | extend x2 = x * 2 }`)

	tbl := diskQuery(t, eng, `let MyTable = datatable(x:long)[1,2,3]; DoubleIt((MyTable))`)
	expectRows(t, tbl, 3)
	x2Idx := tbl.Schema.ColumnIndex("x2")
	if x2Idx < 0 {
		t.Fatalf("expected column x2, schema: %+v", tbl.Schema.Columns)
	}
	want := []int64{2, 4, 6}
	for i, w := range want {
		got, ok := tbl.Rows[i][x2Idx].(int64)
		if !ok || got != w {
			t.Errorf("row %d x2 = %v, want %d", i, tbl.Rows[i][x2Idx], w)
		}
	}
}

// TestLetBoundTableWithOwnLetsAsTabularArgument guards the same fix
// for the CompoundStatement shape (a tabular argument with its own
// SELF-CONTAINED nested let bindings, e.g. "(let y = 1; T | where x >
// y)") — a separate code path (parseTabularArgument returns a
// *parser.CompoundStatement here, not a bare *parser.Query) that must
// be fixed identically for the case that doesn't ALSO require
// cross-scope resolution.
//
// Deliberately does NOT test a nested let referencing an OUTER-scope
// name (e.g. an inner "let y = 1; MyTable | where x > y" trying to see
// a MyTable bound in the caller's own outer scope) — that combination
// hits a genuinely separate, deeper, pre-existing limitation found
// while writing this test: LetContext isn't lexically scoped/chained
// at all. Every executeCompound call installs a completely isolated
// fresh context with no fallback to any enclosing scope, so a nested
// compound argument's own Lets can never see an outer let-bound name,
// regardless of this session's PrecomputedTable fix (which only
// addresses the CALLER-to-CALLEE single-level handoff, not general
// lexical nesting). Real, worth fixing eventually, but a materially
// bigger architectural change (a parent-context chain touching every
// LetContext-reading call site) than this session's actual scope --
// documented here rather than silently worked around or claimed as
// covered.
func TestLetBoundTableWithOwnLetsAsTabularArgument(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function DoubleIt(T:(x:long)) { T | extend x2 = x * 2 }`)

	tbl := diskQuery(t, eng, `DoubleIt((let y = 1; datatable(x:long)[1,2,3] | where x > y))`)
	expectRows(t, tbl, 2) // x=2,3 (x>1)
}

// TestStoredFunctionIntLiteralWidensToRealParameter guards a real bug
// found and fixed while testing invoke against real ADX's own
// clipped_average worked example (2026-08-15): kqlTypesCompatible
// rejected a bare integer literal argument for a real-typed parameter
// outright, with no long/int->real widening at all — a standard,
// universally-expected implicit conversion missing from what its own
// doc comment already flagged as a deliberately incomplete conversion
// table. Also confirms the VALUE itself is coerced, not just the type
// tag: the parameter's own arithmetic must see a real 5.0, not an
// unconverted int64 5 mislabeled as real.
func TestStoredFunctionIntLiteralWidensToRealParameter(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function Half(v: real) { print result = v / 2 }`)

	tbl := diskQuery(t, eng, `Half(5)`)
	expectRows(t, tbl, 1)
	got, ok := tbl.Rows[0][0].(float64)
	if !ok {
		t.Fatalf("expected float64 result, got %T: %v", tbl.Rows[0][0], tbl.Rows[0][0])
	}
	if got != 2.5 {
		t.Errorf("Half(5) = %v, want 2.5 (5 must widen to a real 5.0, not integer-divide as 5/2=2)", got)
	}
}

// TestPercentilesPluralAliasesToSingleValuePercentile guards a real
// bug found and fixed alongside the above (2026-08-15): "percentiles"
// (plural — real ADX's own actual spelling in its clipped_average
// worked example, `percentiles(x, upPercentile)`) wasn't recognized as
// an aggregation function at all, only the singular "percentile". With
// exactly one percentile value requested, the two are functionally
// identical, so "percentiles" is aliased to the same single-value code
// for that one case — NOT a claim of full multi-percentile support
// (a separate, bigger, real ADX feature not implemented here).
func TestPercentilesPluralAliasesToSingleValuePercentile(t *testing.T) {
	result := queryResult(t, `datatable(x:long)[1,2,3,4,5,6,7,8,9,10]
		| summarize percentiles(x, 50)`)
	if result.RowCount() != 1 {
		t.Fatalf("expected 1 row, got %d", result.RowCount())
	}
	got, ok := result.Rows[0][0].(float64)
	if !ok {
		t.Fatalf("expected float64, got %T: %v", result.Rows[0][0], result.Rows[0][0])
	}
	if got < 5 || got > 6 {
		t.Errorf("median of 1..10 = %v, expected between 5 and 6", got)
	}
}
