package engine

import (
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// union_options_test.go — union kind=/withsource=/isfuzzy=, wildcard
// legs, per-type column suffixing, and the view lambdas the ADX
// reference examples are written with. The first four tests are the
// worked examples on the union-operator reference page.

const unionViews = `let View_1 = view () { print x=1 };
let View_2 = view () { print x=1 };
let OtherView_1 = view () { print x=1 };
`

func TestUnionIsFuzzyMissingView(t *testing.T) {
	r := queryResult(t, unionViews+`union isfuzzy=true
		(View_1 | where x > 0),
		(View_2 | where x > 0),
		(View_3 | where x > 0)
		| count`)
	if got := r.Rows[0][0]; got != int64(2) {
		t.Errorf("count = %v, want 2", got)
	}
}

func TestUnionIsFuzzyWildcards(t *testing.T) {
	r := queryResult(t, unionViews+`union isfuzzy=true View*, SomeView*, OtherView* | count`)
	if got := r.Rows[0][0]; got != int64(3) {
		t.Errorf("count = %v, want 3", got)
	}
}

func TestUnionColumnTypeMismatch(t *testing.T) {
	r := queryResult(t, `let View_1 = view () { print x=1 };
		let View_2 = view () { print x=toint(2) };
		union withsource=TableName View_1, View_2`)
	assertColumns(t, columnNames(&r.Schema), "TableName", "x_long", "x_int")
	want := [][]interface{}{{"View_1", int64(1), nil}, {"View_2", nil, int32(2)}}
	assertRows(t, r.Rows, want)
}

func TestUnionColumnTypeMismatchNameCollision(t *testing.T) {
	r := queryResult(t, `let View_1 = view () { print x=1 };
		let View_2 = view () { print x=toint(2) };
		let View_3 = view () { print x_long=3 };
		union withsource=TableName View_1, View_2, View_3`)
	assertColumns(t, columnNames(&r.Schema), "TableName", "x_long1", "x_int", "x_long")
	want := [][]interface{}{
		{"View_1", int64(1), nil, nil},
		{"View_2", nil, int32(2), nil},
		{"View_3", nil, nil, int64(3)},
	}
	assertRows(t, r.Rows, want)
}

func TestUnionKindInner(t *testing.T) {
	r := queryResult(t, `let A = datatable(a:long, b:string)[1,"x"];
		let B = datatable(a:long, c:string)[2,"y"];
		union kind=inner A, B`)
	assertColumns(t, columnNames(&r.Schema), "a")
	if len(r.Rows) != 2 {
		t.Errorf("rows = %d, want 2", len(r.Rows))
	}
}

func TestUnionPipedWithSource(t *testing.T) {
	r := queryResult(t, `let A = datatable(a:long)[1];
		let B = datatable(a:long)[2];
		A | union withsource=S (B | where a > 0)`)
	assertColumns(t, columnNames(&r.Schema), "S", "a")
	assertRows(t, r.Rows, [][]interface{}{{"A", int64(1)}, {"B", int64(2)}})
}

// The standalone form used to promote the first leg to the query
// source and reject a first leg with operators.
func TestUnionStandaloneFirstLegFiltered(t *testing.T) {
	r := queryResult(t, `let A = datatable(a:long)[1, 2, 3];
		union (A | where a > 1), A | count`)
	if got := r.Rows[0][0]; got != int64(5) {
		t.Errorf("count = %v, want 5", got)
	}
}

func TestUnionNotFuzzyMissingFails(t *testing.T) {
	_, err := queryErr(`let V = view () { print x=1 }; union V, Nope | count`)
	if err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Errorf("err = %v, want a resolution error naming Nope", err)
	}
}

func TestUnionFuzzyNothingResolvesFails(t *testing.T) {
	if _, err := queryErr(`union isfuzzy=true Nope1, Nope2 | count`); err == nil {
		t.Error("expected an error when no leg resolves")
	}
}

func TestUnionExecutionHintsAccepted(t *testing.T) {
	r := queryResult(t, `let A = datatable(a:long)[1];
		union hint.concurrency=2 hint.spread=1 A, A | count`)
	if got := r.Rows[0][0]; got != int64(2) {
		t.Errorf("count = %v, want 2", got)
	}
}

// A parameterless view binds like a tabular let.
func TestViewLambdaAsTable(t *testing.T) {
	r := queryResult(t, `let V = view () { datatable(a:long)[1, 2, 3] }; V | where a > 1 | count`)
	if got := r.Rows[0][0]; got != int64(2) {
		t.Errorf("count = %v, want 2", got)
	}
}

func assertColumns(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("columns = %v, want %v", got, want)
	}
}

func assertRows(t *testing.T, got []types.Row, want [][]interface{}) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("rows = %d, want %d", len(got), len(want))
	}
	for i := range want {
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Errorf("row %d col %d = %v (%T), want %v (%T)", i, j, got[i][j], got[i][j], want[i][j], want[i][j])
			}
		}
	}
}

// queryErr returns the parse or execution error of a query, if any.
func queryErr(query string) (*types.Table, error) {
	stmt, err := parser.Parse(query)
	if err != nil {
		return nil, err
	}
	return New(catalog.NewMemory()).Execute(stmt)
}
