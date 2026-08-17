package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// fakeExternalResolver is a minimal, deterministic
// ExternalFunctionResolver for testing — recognizes exactly the names
// listed in "handles", returning a fixed table for each; everything
// else is ok=false (falls through). A name in "errNames" is
// recognized (ok=true) but always fails, to test error propagation.
type fakeExternalResolver struct {
	handles  map[string]*types.Table
	errNames map[string]error
}

func (f *fakeExternalResolver) ResolveExternalFunction(name string, argTexts []string) (*types.Table, bool, error) {
	if tbl, ok := f.handles[name]; ok {
		return tbl, true, nil
	}
	if err, ok := f.errNames[name]; ok {
		return nil, true, err
	}
	return nil, false, nil
}

func fakeTable(colName string, vals ...types.Value) *types.Table {
	tbl := types.NewTable("external", types.Schema{Columns: []types.Column{{Name: colName, Type: types.TypeString}}})
	for _, v := range vals {
		tbl.AddRow(types.Row{v})
	}
	return tbl
}

// TestExternalResolverNilByDefaultNoImpact guards that a nil
// ExternalResolver (the default, zero-value state) doesn't change
// this engine's own behavior at all — the whole point of this being
// purely additive.
func TestExternalResolverNilByDefaultNoImpact(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table T (Id: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string) ["a","b"]`)

	tbl := diskQuery(t, eng, `T | count`)
	expectCell(t, tbl, 0, 0, "2")
}

// TestExternalResolverHandlesRecognizedName guards the core,
// motivating case: an external resolver recognizes a name and its
// result composes correctly with the REST of the pipeline running
// through this engine's own, real, unmodified operators — not just
// that the resolver gets called, but that
// externalFunc(...) | where ... actually works end to end.
func TestExternalResolverHandlesRecognizedName(t *testing.T) {
	eng := diskEngineEmpty(t)
	eng.ExternalResolver = &fakeExternalResolver{
		handles: map[string]*types.Table{
			"connectors": fakeTable("name", "alpha", "beta", "gamma"),
		},
	}

	tbl := diskQuery(t, eng, `connectors(first: 100) | where name != "beta" | count`)
	expectCell(t, tbl, 0, 0, "2")
}

// TestExternalResolverFallsThroughWhenNotMine guards ok=false: a name
// the resolver doesn't recognize falls through to this engine's own,
// normal stored-function lookup completely unchanged.
func TestExternalResolverFallsThroughWhenNotMine(t *testing.T) {
	eng := diskEngineEmpty(t)
	eng.ExternalResolver = &fakeExternalResolver{
		handles: map[string]*types.Table{"connectors": fakeTable("name", "alpha")},
	}
	diskExec(t, eng, `.create table T (Id: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string) ["a"]`)
	diskExec(t, eng, `.create-or-alter function MyFunc() { T }`)

	// MyFunc is not recognized by the fake resolver at all — must fall
	// through to the real, normal stored-function lookup.
	tbl := diskQuery(t, eng, `MyFunc() | count`)
	expectCell(t, tbl, 0, 0, "1")
}

// TestExternalResolverErrorPropagatesDirectly guards ok=true, err !=
// nil: the resolver's own error surfaces directly, NOT a confusing,
// unrelated "no such stored function" from falling through to the
// normal lookup afterward.
func TestExternalResolverErrorPropagatesDirectly(t *testing.T) {
	eng := diskEngineEmpty(t)
	eng.ExternalResolver = &fakeExternalResolver{
		errNames: map[string]error{"connectors": errors.New("upstream auth failed")},
	}

	_, err := runStmt(t, eng, `connectors(first: 100)`)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "upstream auth failed") {
		t.Errorf("expected the resolver's own error to propagate directly, got: %v", err)
	}
	if strings.Contains(err.Error(), "no such stored function") {
		t.Errorf("error should not have fallen through to the normal stored-function-not-found path, got: %v", err)
	}
}

// TestExternalResolverPrecedenceOverSameNamedStoredFunction guards
// that the external resolver is checked FIRST — the same precedence
// rule this codebase already applies to its own built-in
// table-valued functions (csv(), json(), etc. always win over a
// same-named stored function).
func TestExternalResolverPrecedenceOverSameNamedStoredFunction(t *testing.T) {
	eng := diskEngineEmpty(t)
	eng.ExternalResolver = &fakeExternalResolver{
		handles: map[string]*types.Table{"connectors": fakeTable("name", "from-external")},
	}
	diskExec(t, eng, `.create table T (Id: string)`)
	diskExec(t, eng, `.set-or-append T <| datatable(Id:string) ["from-stored-function"]`)
	diskExec(t, eng, `.create-or-alter function connectors() { T }`)

	tbl := diskQuery(t, eng, `connectors() | project name`)
	expectCell(t, tbl, 0, 0, "from-external")
}

