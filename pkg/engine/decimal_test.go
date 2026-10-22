package engine

import (
	"testing"
)

// The parse_version reference examples, exact.
func TestParseVersionReference(t *testing.T) {
	tbl := queryResult(t, `let dt = datatable(v: string)["0.0.0.5", "0.0.7.0", "0.0.3", "0.2", "0.1.2.0", "1.2.3.4", "1"]; dt | extend parsedVersion = parse_version(v)`)
	want := []string{"5", "700000000", "300000000", "20000000000000000", "10000000200000000", "1000000020000000300000004", "1000000000000000000000000"}
	for i, w := range want {
		expectCell(t, tbl, i, 1, w)
	}
	tbl = queryResult(t, `let dt = datatable(lab: string, v: string)["Lab A", "0.0.0.5", "Lab B", "0.0.7.0", "Lab D","0.0.3", "Lab C", "0.2", "Lab G", "0.1.2.0", "Lab F", "1.2.3.4", "Lab E", "1"];
dt | extend parsed_version = parse_version(v) | extend needs_update = iff(parsed_version < parse_version("1.0.0.0"), "Yes", "No") | project lab, v, needs_update | sort by lab asc, v, needs_update`)
	for i, w := range []string{"Yes", "Yes", "Yes", "Yes", "No", "No", "Yes"} {
		expectCell(t, tbl, i, 2, w)
	}
	for _, bad := range []string{`""`, `"1.2.3.4.5"`, `"1..2"`, `"123456789"`, `"1.a"`, `"-1"`} {
		tbl := queryResult(t, `print v = parse_version(`+bad+`)`)
		if cellVal(t, tbl, 0, 0) != nil {
			t.Errorf("parse_version(%s) = %v, want null", bad, cellVal(t, tbl, 0, 0))
		}
	}
}

// The todecimal reference example, and literals parsed from text.
func TestDecimalLiteralsAndToDecimal(t *testing.T) {
	tbl := queryResult(t, `print a = todecimal("123.45678") == decimal(123.45678), b = decimal(1.50), c = decimal(1e5), d = decimal(null), e = decimal(0.1) + decimal(0.2) == decimal(0.3), f = todecimal("x"), g = todecimal(3), h = todecimal(0.1)`)
	expectCell(t, tbl, 0, 0, "true")
	expectCell(t, tbl, 0, 1, "1.50")
	expectCell(t, tbl, 0, 2, "100000")
	if cellVal(t, tbl, 0, 3) != nil || cellVal(t, tbl, 0, 5) != nil {
		t.Error("decimal(null) / todecimal(\"x\") not null")
	}
	expectCell(t, tbl, 0, 4, "true")
	expectCell(t, tbl, 0, 6, "3")
	expectCell(t, tbl, 0, 7, "0.1")
}

// Result types as Kusto.Language declares them: decimal dominates in
// arithmetic, and the aggregates and math functions keep decimal.
func TestDecimalTyping(t *testing.T) {
	tbl := queryResult(t, `print a = gettype(decimal(1.5) + 1), b = gettype(decimal(1.5) + 1.0), c = gettype(decimal(1) / 2), d = gettype(-decimal(1)), e = gettype(todecimal("1")), f = gettype(parse_version("1")), g = gettype(abs(decimal(-1))), h = gettype(round(decimal(1.25), 1)), i = gettype(bin(decimal(7.3), 2)), j = gettype(iff(true, decimal(1), 2))`)
	for i := 0; i < 10; i++ {
		expectCell(t, tbl, 0, i, "decimal")
	}
	tbl = queryResult(t, `datatable(d:decimal)[decimal(0.1), decimal(0.2), decimal(0.3)] | summarize s = sum(d), a = avg(d), mn = min(d), mx = max(d), si = sumif(d, d > 0.15) | extend ts = gettype(s), ta = gettype(a), exact = s == decimal(0.6)`)
	expectCell(t, tbl, 0, 0, "0.6")
	expectCell(t, tbl, 0, 1, "0.2")
	expectCell(t, tbl, 0, 2, "0.1")
	expectCell(t, tbl, 0, 3, "0.3")
	expectCell(t, tbl, 0, 4, "0.5")
	expectCell(t, tbl, 0, 5, "decimal")
	expectCell(t, tbl, 0, 6, "decimal")
	expectCell(t, tbl, 0, 7, "true")
}

func TestDecimalArithmeticAndMath(t *testing.T) {
	tbl := queryResult(t, `print a = decimal(1)/decimal(3), b = decimal(7.5) % 2, c = decimal(2) * 1.5, d = decimal(5) > 4.5, e = abs(decimal(-2.5)), f = round(decimal(2.345), 2), g = floor(decimal(-1.5), 1), h = ceiling(decimal(1.2)), i = decimal(1) / 0, j = -decimal(1.5), k = tolong(decimal(-1.9)), l = toreal(decimal(1.5)), m = tostring(decimal(2.50))`)
	for i, w := range []string{"0.3333333333333333333333333333", "1.5", "3.0", "true", "2.5", "2.35", "-2.0", "2.0", "", "-1.5", "-1", "1.5", "2.50"} {
		expectCell(t, tbl, 0, i, w)
	}
}

// Unary minus on any numeric operand (it used to parse only before a
// numeric literal).
func TestUnaryMinus(t *testing.T) {
	tbl := queryResult(t, `print a = -(1.5), b = -abs(2), c = 3 - -1, d = -(1h), e = gettype(-(toint(1))), f = 5 - (-2)`)
	for i, w := range []string{"-1.5", "-2", "4", "-01:00:00", "int", "7"} {
		expectCell(t, tbl, 0, i, w)
	}
}

// A decimal column round-trips through storage exactly, including in
// a fresh engine reading the files.
func TestDecimalStorage(t *testing.T) {
	dir := t.TempDir()
	eng := discoverEngine(t, dir)
	diskExec(t, eng, `.create table V (App: string, P: decimal)`)
	diskExec(t, eng, ".ingest inline into table V <| a,0.10\nb,12345678901234567890.123456789\nn,")
	diskExec(t, eng, `.set-or-append V <| datatable(App:string, P:decimal)["c", decimal(1.5)]`)
	fresh := discoverEngine(t, dir)
	expectCell(t, diskQuery(t, fresh, `V | getschema | where ColumnName == "P" | project ColumnType`), 0, 0, "decimal")
	tbl := diskQuery(t, fresh, `V | summarize s = sum(P), mx = max(P), nulls = countif(isnull(P))`)
	expectCell(t, tbl, 0, 0, "12345678901234567891.723456789")
	expectCell(t, tbl, 0, 1, "12345678901234567890.123456789")
	expectCell(t, tbl, 0, 2, "1")
	tbl = diskQuery(t, fresh, `V | where isnotnull(P) | sort by P asc | project App`)
	for i, w := range []string{"a", "c", "b"} {
		expectCell(t, tbl, i, 0, w)
	}
}

// Conditional and choice functions take their results' common type
// (Kusto.Language); they were always inferred as string, so a sort on
// an iff() column compared text ("10" before "9").
func TestConditionalResultTypes(t *testing.T) {
	tbl := queryResult(t, `print a = gettype(iff(true, 1, 2)), b = gettype(iff(true, 1.5, 2)), c = gettype(iff(true, toint(1), 2)), d = gettype(iff(true, "x", "y")), e = gettype(iff(true, 1, real(null))), f = gettype(iff(true, datetime(2020-01-01), now())), g = gettype(case(true, 1, false, 2.5, 3)), h = gettype(coalesce(toint(1), 2)), i = gettype(max_of(1, 2.5)), j = gettype(min_of(toint(1), toint(2))), k = gettype(iff(true, 1s, 2s))`)
	for i, w := range []string{"long", "real", "long", "string", "real", "datetime", "real", "long", "real", "int", "timespan"} {
		expectCell(t, tbl, 0, i, w)
	}
	tbl = queryResult(t, `range r from 1 to 12 step 1 | extend v = iff(r > 0, r, 0) | sort by v desc | take 1 | project v`)
	expectCell(t, tbl, 0, 0, "12")
}
