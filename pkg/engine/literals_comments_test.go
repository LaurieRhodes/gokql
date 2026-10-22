package engine

import (
	"math"
	"testing"
)

// literals_comments_test.go — literal forms (hex, special reals,
// unquoted guids, obfuscated and adjacent strings), // comments in any
// position, and materialize().

func TestLiteralForms(t *testing.T) {
	r := queryResult(t, `print a = 0x1F, b = 0xff + 1, c = real(+inf), d = real(-inf),
		e = real(nan), f = guid(74BE27DE-1e4e-49d9-b579-fe0b331d3642),
		g = h"secret", h = H@"c:\x", i = "a" "b" @"c"`)
	row := r.Rows[0]
	if row[0] != int64(31) || row[1] != int64(256) {
		t.Errorf("hex: %v, %v", row[0], row[1])
	}
	if c, _ := row[2].(float64); !math.IsInf(c, 1) {
		t.Errorf("real(+inf) = %v", row[2])
	}
	if d, _ := row[3].(float64); !math.IsInf(d, -1) {
		t.Errorf("real(-inf) = %v", row[3])
	}
	if e, _ := row[4].(float64); !math.IsNaN(e) {
		t.Errorf("real(nan) = %v", row[4])
	}
	if row[5] != "74be27de-1e4e-49d9-b579-fe0b331d3642" {
		t.Errorf("guid = %v", row[5])
	}
	if row[6] != "secret" || row[7] != `c:\x` || row[8] != "abc" {
		t.Errorf("strings = %v, %v, %v", row[6], row[7], row[8])
	}
}

func TestLineCommentsAnywhere(t *testing.T) {
	r := queryResult(t, `datatable(a:long)[1,2] // c1
		| extend b = "x//y", c = @"\\srv//share" // c2
		| where a > 1 // c3
		| project b, c`)
	assertRows(t, r.Rows, [][]interface{}{{"x//y", `\\srv//share`}})
}

func TestMaterialize(t *testing.T) {
	r := queryResult(t, `let m = materialize(datatable(a:long)[1,2,3] | where a > 1); union m, m | count`)
	if got := r.Rows[0][0]; got != int64(4) {
		t.Errorf("count = %v, want 4", got)
	}
	r = queryResult(t, `materialize(datatable(a:long)[1,2,3] | where a > 1) | summarize s = sum(a)`)
	if got := r.Rows[0][0]; got != int64(5) {
		t.Errorf("sum = %v, want 5", got)
	}
}
