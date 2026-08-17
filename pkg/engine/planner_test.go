package engine

// planner_test.go directly guards the structural fix added in
// response to a pattern independently spotted and named by a
// different model's testing session (Kimi): the same bug class (a new
// parser.Operator or parser.Expr type with no case in this planner's
// column-collection switches) was independently found and fixed three
// separate times this session — StarExpr, HasAnyAllExpr, LookupOp —
// always the same way: silently under-collect required columns,
// producing wrong data with no error, invisible to every existing
// test because all of them used in-memory datatable literals, which
// never exercise this pushdown analysis at all.
//
// Rather than test only the three ALREADY-fixed cases (which the
// individual bug-fix commits already do, and which says nothing about
// whether a FOURTH, not-yet-invented type would be safe too), these
// tests prove the actual structural property directly: a genuinely
// unknown parser.Operator or parser.Expr type — one that cannot
// possibly have an explicit case in collectOperator/collectExpr,
// since it's defined right here in the test file and nowhere else —
// must still result in a safe outcome (scan every column) rather than
// a silent, wrong one (scan too few). If this ever regresses back to
// silent under-collection, these tests fail immediately, without
// needing to anticipate what the next new AST node type will be.

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// Uses parser.UnrecognizedTestOperator/UnrecognizedTestExpr — defined
// IN the parser package itself (not here), specifically because Go's
// unexported marker-method pattern (operatorNode/exprNode) means no
// type outside that package can ever satisfy parser.Operator/Expr at
// all. That constraint is exactly why these two sentinel types need
// to exist as real, exported parser-package types rather than local
// fakes defined here — see their own doc comments in ast.go.

func TestRequiredColumnsFailsSafeOnUnknownOperator(t *testing.T) {
	schema := &types.Schema{Columns: []types.Column{
		{Name: "A", Type: types.TypeString},
		{Name: "B", Type: types.TypeString},
		{Name: "C", Type: types.TypeString},
	}}
	// A narrowing operator (project) is present, which is exactly the
	// condition under which every one of the three real bugs actually
	// manifested — pushdown is only even attempted when something
	// downstream narrows. Without a narrowing operator at all, every
	// column is scanned regardless, which would trivially pass this
	// test without proving anything.
	cols := RequiredColumns(schema, []parser.Operator{
		&parser.ProjectOp{Items: []parser.ProjectItem{{Name: "A"}}},
		&parser.UnrecognizedTestOperator{},
	})
	if cols != nil {
		t.Fatalf("expected nil (all columns needed) for an unrecognized operator type, got %v -- "+
			"this means an unknown operator type silently under-collects required columns again", cols)
	}
}

func TestRequiredColumnsFailsSafeOnUnknownExpr(t *testing.T) {
	schema := &types.Schema{Columns: []types.Column{
		{Name: "A", Type: types.TypeString},
		{Name: "B", Type: types.TypeString},
		{Name: "C", Type: types.TypeString},
	}}
	// Nested inside a WhereOp, exercising collectExpr's own default
	// case specifically (not collectOperator's).
	cols := RequiredColumns(schema, []parser.Operator{
		&parser.ProjectOp{Items: []parser.ProjectItem{{Name: "A"}}},
		&parser.WhereOp{Predicate: &parser.UnrecognizedTestExpr{}},
	})
	if cols != nil {
		t.Fatalf("expected nil (all columns needed) for an unrecognized expression type, got %v -- "+
			"this means an unknown expression type silently under-collects required columns again", cols)
	}
}

// TestRequiredColumnsStarExprIsExplicitlyHandled guards that StarExpr
// specifically is NOT treated as "unknown" by collectExpr's own
// default (it has its own explicit, deliberate no-op case, since
// SummarizeOp already handles it correctly before ever calling
// collectExpr) — a direct collectExpr(*StarExpr{}) call, outside a
// SummarizeOp context, must still be a safe no-op rather than
// (redundantly, but not incorrectly) forcing needsAll.
func TestRequiredColumnsStarExprIsExplicitlyHandled(t *testing.T) {
	schema := &types.Schema{Columns: []types.Column{
		{Name: "A", Type: types.TypeString},
		{Name: "B", Type: types.TypeString},
	}}
	c := &columnCollector{schema: schema, columns: make(map[string]bool)}
	c.collectExpr(&parser.StarExpr{})
	if c.needsAll {
		t.Errorf("expected StarExpr to be a deliberate no-op in collectExpr (handled explicitly, not via the unknown-type default), got needsAll=true")
	}
}
