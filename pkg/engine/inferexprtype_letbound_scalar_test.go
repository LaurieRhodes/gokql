package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// inferexprtype_letbound_scalar_test.go — guards the real,
// documented gap closed 2026-08-18: inferExprType's own ColumnRef
// case previously had no way to resolve a let-bound scalar's KQL
// type, so a tabular parameter with a DECLARED column type
// (T:(v:long)) rejected a `print v = <scalar let>` argument outright
// with "expected type long, argument has string" — found and left
// unfixed during the Sprint 15-adjacent LetContext lexical-scoping
// fix (commit bed111f), documented in letcontext_scoping_test.go's
// own file comment, and in kql_coverage.md / scalar_function_backlog.md.
//
// Fixed via LetContext.ScalarTypes (engine.go), populated alongside
// Scalars at let-bind time using the SAME expression-based inference
// applyPrint/applyExtend/applyProject already use for every other
// column — not from the let binding's raw runtime value, which is
// genuinely ambiguous for an int64 (could be long, datetime, or
// timespan, all represented identically internally). See
// LookupScalarType's own doc comment (engine.go) for the full story.

// TestDeclaredTypeParamAcceptsLetBoundLongScalar guards the exact
// repro from bed111f's own commit message and kql_coverage.md's gap
// entry: a declared-type tabular parameter (v:long) must now accept
// a `print v = <scalar let>` argument where the let is a long,
// instead of erroring on schema validation.
func TestDeclaredTypeParamAcceptsLetBoundLongScalar(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function Echo(T:(v:long)) { T }`)

	tbl := diskQuery(t, eng, `let x = 5; Echo((print v = x))`)
	expectRows(t, tbl, 1)
	got, ok := tbl.Rows[0][0].(int64)
	if !ok || got != 5 {
		t.Fatalf("v = %v (%T), want int64(5)", tbl.Rows[0][0], tbl.Rows[0][0])
	}
}

// TestDeclaredTypeParamRejectsMismatchedLetBoundScalar guards that the
// fix doesn't just make validation permissive across the board — a
// genuinely wrong type (a string let bound to a declared-long
// parameter) must still be rejected, matching real ADX's own tabular-
// argument type-checking behavior.
func TestDeclaredTypeParamRejectsMismatchedLetBoundScalar(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function Echo(T:(v:long)) { T }`)

	diskQueryError(t, eng, `let x = "not a number"; Echo((print v = x))`)
}

// TestDeclaredTypeParamAcceptsLetBoundDatetimeScalar guards the
// specific ambiguity this fix was designed to avoid: a let-bound
// datetime scalar (represented internally as the same int64 shape as
// a long) must resolve to TypeDatetime, not TypeLong, when read back
// through inferExprType — proving the fix reads the ORIGINAL
// expression's type (captured at bind time), not the ambiguous raw
// Go value.
func TestDeclaredTypeParamAcceptsLetBoundDatetimeScalar(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function Echo(T:(v:datetime)) { T }`)

	tbl := diskQuery(t, eng, `let d = datetime(2026-08-18); Echo((print v = d))`)
	expectRows(t, tbl, 1)
	got := types.FormatValue(tbl.Rows[0][0], types.TypeDatetime)
	want := "2026-08-18T00:00:00Z"
	if got != want {
		t.Fatalf("v = %v, want %v", got, want)
	}
}

// TestDeclaredTypeParamNestedLetStillResolves guards that this fix
// composes correctly with the pre-existing LetContext parent-chain
// lookup (bed111f, same session as the original gap report): an
// OUTER scalar let referenced from within a nested compound tabular
// argument must still resolve its type correctly through the parent
// chain, not just when declared in the immediately-enclosing scope.
func TestDeclaredTypeParamNestedLetStillResolves(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function Echo(T:(v:long)) { T }`)

	tbl := diskQuery(t, eng, `let outer = 42; Echo((let inner = 1; print v = outer))`)
	expectRows(t, tbl, 1)
	got, ok := tbl.Rows[0][0].(int64)
	if !ok || got != 42 {
		t.Fatalf("v = %v (%T), want int64(42)", tbl.Rows[0][0], tbl.Rows[0][0])
	}
}
