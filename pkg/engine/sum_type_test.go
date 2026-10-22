package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// sum_type_test.go — sum/sumif return the argument type promoted, as
// declared in Kusto.Language (ReturnTypeKind.Parameter0Promoted:
// int becomes long, every other type is kept): int and long give
// long, timespan gives timespan, real gives real.
func TestSumReturnTypes(t *testing.T) {
	r := queryResult(t, `datatable(a:long, i:int, t:timespan, r:real)[1, 2, 2d, 0.5, 2, 3, 1h, 1.5]
		| summarize sa = sum(a), si = sum(i), st = sum(t), sr = sum(r), sif = sumif(a, a > 1)`)
	wantType := []types.KQLType{types.TypeLong, types.TypeLong, types.TypeTimespan, types.TypeReal, types.TypeLong}
	wantVal := []interface{}{int64(3), int64(5), int64(2*864000000000 + 36000000000), float64(2), int64(2)}
	for i := range wantType {
		if got := r.Schema.Columns[i].Type; got != wantType[i] {
			t.Errorf("%s type = %v, want %v", r.Schema.Columns[i].Name, got, wantType[i])
		}
		if got := r.Rows[0][i]; got != wantVal[i] {
			t.Errorf("%s = %v (%T), want %v (%T)", r.Schema.Columns[i].Name, got, got, wantVal[i], wantVal[i])
		}
	}
}

// Exactness: a long sum beyond 2^53 must not be rounded through float64.
func TestSumLongExact(t *testing.T) {
	r := queryResult(t, `datatable(a:long)[9007199254740993, 1] | summarize s = sum(a)`)
	if got := r.Rows[0][0]; got != int64(9007199254740994) {
		t.Errorf("sum = %v, want 9007199254740994", got)
	}
}

// datatable accepts KQL timespan literals.
func TestDatatableTimespanLiterals(t *testing.T) {
	r := queryResult(t, `datatable(t:timespan)[2d, 1.5h, 30m, 10s, 100ms] | summarize s = sum(t)`)
	want := int64(2*864000000000 + 54000000000 + 18000000000 + 100000000 + 1000000)
	if got := r.Rows[0][0]; got != want {
		t.Errorf("sum = %v, want %v", got, want)
	}
}
