package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// invoke_test.go — the invoke operator. Verified against real ADX
// docs (invoke-operator.md): "Invokes a lambda expression that
// receives the source of invoke as a tabular argument." See InvokeOp's
// own doc comment (pkg/parser/ast.go) for exactly what's in scope
// here: STORED functions only (`.create-or-alter function`), not real
// ADX's own primary worked example's inline `let`-lambda with a
// tabular parameter — this engine's `let name = (params) { body }`
// syntax only supports scalar parameters and a scalar body, a
// genuinely separate, pre-existing gap found while researching this
// operator, not something invoke itself needs to solve.

func invokeTestEngine(t *testing.T) *Engine {
	t.Helper()
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create-or-alter function DoubleIt(T:(x:long)) { T | extend x2 = x * 2 }`)
	diskExec(t, eng, `.create-or-alter function ScaleBy(T:(x:long), factor:long) { T | extend scaled = x * factor }`)
	return eng
}

// TestInvokeBasic guards the core shape: the pipeline's current table
// becomes the callee's implicit first (tabular) argument.
func TestInvokeBasic(t *testing.T) {
	eng := invokeTestEngine(t)
	result := diskExec(t, eng, `datatable(x:long)[1,2,3] | invoke DoubleIt()`)
	if result.RowCount() != 3 {
		t.Fatalf("expected 3 rows, got %d", result.RowCount())
	}
	x2Idx := result.Schema.ColumnIndex("x2")
	want := []int64{2, 4, 6}
	for i, w := range want {
		got, ok := result.Rows[i][x2Idx].(int64)
		if !ok || got != w {
			t.Errorf("row %d x2 = %v, want %d", i, result.Rows[i][x2Idx], w)
		}
	}
}

// TestInvokeWithExtraScalarArg confirms additional arguments after the
// implicit tabular one bind to the callee's remaining parameters
// correctly.
func TestInvokeWithExtraScalarArg(t *testing.T) {
	eng := invokeTestEngine(t)
	result := diskExec(t, eng, `datatable(x:long)[1,2,3] | invoke ScaleBy(10)`)
	scaledIdx := result.Schema.ColumnIndex("scaled")
	want := []int64{10, 20, 30}
	for i, w := range want {
		got, ok := result.Rows[i][scaledIdx].(int64)
		if !ok || got != w {
			t.Errorf("row %d scaled = %v, want %d", i, result.Rows[i][scaledIdx], w)
		}
	}
}

// TestInvokeClippedAverageWorkedExample guards real ADX's own primary
// invoke worked example end to end (clipped_average: compute
// percentile bounds, then average only the values within them) —
// adapted to this engine's own .create-or-alter function mechanism
// instead of an inline let-lambda (see this file's own top comment for
// why), but otherwise the exact same function body and call shape from
// real ADX's own docs. This is also the regression test for two real,
// separate bugs found while verifying it: (1) kqlTypesCompatible
// rejected a bare integer literal argument for a real-typed parameter
// (long->real widening was missing entirely), and (2) "percentiles"
// (plural, real ADX's own actual spelling in this exact worked
// example) wasn't recognized as an aggregation function at all, only
// the singular "percentile".
func TestInvokeClippedAverageWorkedExample(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create-or-alter function clipped_average(T:(x:long), lowPercentile:real, upPercentile:real) {
		let high = toscalar(T | summarize percentiles(x, upPercentile));
		let low = toscalar(T | summarize percentiles(x, lowPercentile));
		T | where x > low and x < high | summarize avg(x)
	}`)
	result := diskExec(t, eng, `range x from 1 to 100 step 1 | invoke clipped_average(5, 99)`)
	if result.RowCount() != 1 {
		t.Fatalf("expected 1 row, got %d", result.RowCount())
	}
	avgIdx := result.Schema.ColumnIndex("avg_x")
	if avgIdx < 0 {
		t.Fatalf("expected column avg_x, schema: %+v", result.Schema.Columns)
	}
	got, ok := result.Rows[0][avgIdx].(float64)
	if !ok {
		t.Fatalf("expected float64, got %T: %v", result.Rows[0][avgIdx], result.Rows[0][avgIdx])
	}
	// x runs 1..100; clipping the top/bottom 1%/5% and averaging the
	// remainder should land close to the unclipped mean (50.5) —
	// checked with tolerance since exact clip boundaries depend on
	// this engine's own percentile interpolation, not asserting a
	// single specific float real ADX itself doesn't publish either.
	if got < 45 || got > 55 {
		t.Errorf("clipped average = %v, expected roughly near the midpoint of 1..100 (45-55 range)", got)
	}
}

// TestInvokeUndeclaredFunctionRejected confirms a clear error for a
// nonexistent function, not a panic or silent no-op.
func TestInvokeUndeclaredFunctionRejected(t *testing.T) {
	eng := diskEngineEmpty(t)
	stmt, err := parser.Parse(`datatable(x:long)[1,2,3] | invoke NoSuchFunction()`)
	if err != nil {
		return // parse error is also acceptable
	}
	if _, err := eng.Execute(stmt); err == nil {
		t.Fatalf("expected an error calling an undeclared function via invoke, got success")
	}
}

// TestInvokeDoesNotLeakAcrossQueries mirrors TestAsDoesNotLeakAcrossQueries
// — invoke's own synthetic table binding (invokeSourceBinding) must
// not remain resolvable in a later, separate query against the same
// long-lived Engine.
func TestInvokeDoesNotLeakAcrossQueries(t *testing.T) {
	eng := invokeTestEngine(t)
	diskExec(t, eng, `datatable(x:long)[1,2,3] | invoke DoubleIt()`)
	stmt, err := parser.Parse(`__invoke_source__ | count`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := eng.Execute(stmt); err == nil {
		t.Fatalf("expected 'table not found' for __invoke_source__ in a later query, got no error (a leak across queries)")
	}
}

