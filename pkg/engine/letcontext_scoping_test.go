package engine

import "testing"

// letcontext_scoping_test.go — LetContext's parent-chain lookup
// (LookupScalar/LookupTable/LookupFunction, engine.go), added
// 2026-08-17 to fix a real, live lexical-scoping gap: see
// TestNestedLetSeesOuterScopeTable (stored_functions_test.go) for the
// original, real-world failing case this fixes end to end. The tests
// below check the scoping rules more directly and more thoroughly
// than that one end-to-end case alone: an inner scope must see an
// outer scope's bindings when it doesn't redeclare them, and must
// correctly SHADOW (not merge with, not error on) an outer binding of
// the same name when it does.
//
// All three use (*) any-schema tabular parameters rather than a
// declared column type (e.g. T:(v:long)) — at the time these were
// written, a declared-type parameter's schema VALIDATION step
// rejected a `print v = <scalar let>` argument with "expected type
// long, argument has string", a separate, real, pre-existing bug (not
// this session's own fix) in inferExprType's own ColumnRef case,
// which couldn't resolve a let-bound scalar's type at all (only
// checked a schema, defaulting to TypeString for anything else — the
// same class of gap already found and worked around, differently, in
// make-series's own step-unit conversion, Sprint 8).
//
// RESOLVED 2026-08-18 (Sprint 20) — see LetContext.ScalarTypes and
// LookupScalarType's own doc comment (engine.go), and
// inferexprtype_letbound_scalar_test.go for the dedicated tests
// guarding the fix (including the exact declared-type case these
// tests below still deliberately avoid). (*) parameters are still
// used below anyway — the fix doesn't change what these SPECIFIC
// tests need to check (parent-chain scoping rules, not type
// resolution), so there's no reason to touch working, already-correct
// tests just because the workaround they used is no longer strictly
// necessary.
// to check.

// TestNestedLetShadowsOuterScalar confirms an inner `let x = ...`
// shadows an outer one with the same name — the standard lexical-
// scoping rule, not just "outer bindings become visible." Uses a
// tabular argument (the mechanism this session's own fix actually
// touches — a scalar argument doesn't support the "(let ...; expr)"
// compound form at all, a separate, pre-existing, unrelated
// limitation found while writing this test).
func TestNestedLetShadowsOuterScalar(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function Echo(T:(*)) { T }`)

	tbl := diskQuery(t, eng, `let x = 1; Echo((let x = 2; print v = x))`)
	got, ok := tbl.Rows[0][0].(int64)
	if !ok || got != 2 {
		t.Fatalf("expected the INNER x (2) to shadow the outer x (1), got %v", tbl.Rows[0][0])
	}
}

// TestOuterScalarVisibleWhenNotShadowed confirms an outer scalar let
// binding is visible from within a nested compound argument's own
// body when the inner scope doesn't redeclare it.
func TestOuterScalarVisibleWhenNotShadowed(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function Echo(T:(*)) { T }`)

	tbl := diskQuery(t, eng, `let threshold = 5; Echo((let ignored = 1; print v = threshold))`)
	got, ok := tbl.Rows[0][0].(int64)
	if !ok || got != 5 {
		t.Fatalf("expected the outer threshold (5) to be visible inside the nested scope, got %v", tbl.Rows[0][0])
	}
}

// TestSiblingNestedScopesDontLeakIntoEachOther confirms two
// INDEPENDENT nested compound arguments (siblings, not nested inside
// each other) each get their own isolated scope on top of the shared
// outer one — an inner binding in one sibling must not leak into the
// other, even though both share the same parent.
func TestSiblingNestedScopesDontLeakIntoEachOther(t *testing.T) {
	eng := storedFunctionsTestScope(t)
	diskExec(t, eng, `.create-or-alter function Combine(A:(*), B:(*)) { A | extend b = toscalar(B) }`)

	// Each side's own "let y = ..." must not be visible to the other.
	tbl := diskQuery(t, eng, `let base = 100; Combine((let y = 1; print a = base + y), (let y = 2; print b = base + y))`)
	aIdx := tbl.Schema.ColumnIndex("a")
	bIdx := tbl.Schema.ColumnIndex("b")
	aGot, aOk := tbl.Rows[0][aIdx].(int64)
	bGot, bOk := tbl.Rows[0][bIdx].(int64)
	if !aOk || aGot != 101 {
		t.Errorf("a = %v, want 101 (100 + this side's own y=1)", tbl.Rows[0][aIdx])
	}
	if !bOk || bGot != 102 {
		t.Errorf("b = %v, want 102 (100 + this side's own y=2, not leaked from the other sibling's y=1)", tbl.Rows[0][bIdx])
	}
}

