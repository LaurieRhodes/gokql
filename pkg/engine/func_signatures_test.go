package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// func_signatures_test.go — static types come from the declared return
// types (func_signatures.go) and runtime values agree with them.

func TestDeclaredReturnTypesStatic(t *testing.T) {
	cases := map[string]types.KQLType{
		"pow(2, 3)":                              types.TypeReal,
		"sqrt(4)":                                types.TypeReal,
		"datetime_add('day', 1, now())":          types.TypeDatetime,
		"totimespan('1:00:00')":                  types.TypeTimespan,
		"tobool(1)":                              types.TypeBool,
		"parse_csv('a,b')":                       types.TypeDynamic,
		"strcmp('a', 'b')":                       types.TypeLong,
		"getyear(now())":                         types.TypeInt,
		"bag_pack('k', 1)":                       types.TypeDynamic,
		"series_add(dynamic([1]), dynamic([2]))": types.TypeDynamic,
		"make_timespan(1, 2)":                    types.TypeTimespan,
	}
	for expr, want := range cases {
		r := queryResult(t, `print x = `+expr)
		if got := r.Schema.Columns[0].Type; got != want {
			t.Errorf("%s: column type %v, want %v", expr, got, want)
		}
	}
}

// Runtime values are coerced to the declared representation.
func TestDeclaredReturnTypesRuntime(t *testing.T) {
	r := queryResult(t, `print a = getyear(datetime(2020-05-01)), b = pow(2, 3), c = strlen("abc")`)
	if v, ok := r.Rows[0][0].(int32); !ok || v != 2020 {
		t.Errorf("getyear = %v (%T), want int32(2020)", r.Rows[0][0], r.Rows[0][0])
	}
	if v, ok := r.Rows[0][1].(float64); !ok || v != 8 {
		t.Errorf("pow = %v (%T), want float64(8)", r.Rows[0][1], r.Rows[0][1])
	}
	if v, ok := r.Rows[0][2].(int64); !ok || v != 3 {
		t.Errorf("strlen = %v (%T), want int64(3)", r.Rows[0][2], r.Rows[0][2])
	}
}

// bag_pack was typed string, so a following bag_unpack rejected it.
func TestBagPackThenBagUnpack(t *testing.T) {
	r := queryResult(t, `datatable(a:long)[1] | extend d = bag_pack("k", a) | evaluate bag_unpack(d)`)
	assertColumns(t, columnNames(&r.Schema), "a", "k")
}
