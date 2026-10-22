package engine

import "testing"

// arith_percentile_test.go — mixed numeric arithmetic and comparison
// (types from Kusto.Language), and nearest-rank percentiles with ADX
// column naming.

func TestMixedNumericArithmetic(t *testing.T) {
	q := `datatable(i:int, l:long, r:real, t:timespan)[3, 4, 1.5, 1h] | project
		a=i+i, b=i*l, c=i*r, e=l/r, f=t*r, g=r*t, h=t/t, h2=90m/1h, k=t/l, m=i%i,
		n=t%25m, o=l*t, s=i/i, u=l%r, lt=l<r, it=i<3.5`
	r := queryResult(t, q)
	want := map[string]struct{ v, typ string }{
		"a": {"6", "long"}, "b": {"12", "long"}, "c": {"4.5", "real"}, "e": {"2.6666666666666665", "real"},
		"f": {"01:30:00", "timespan"}, "g": {"01:30:00", "timespan"}, "h": {"1", "real"}, "h2": {"1.5", "real"},
		"k": {"00:15:00", "timespan"}, "m": {"0", "long"}, "n": {"00:10:00", "timespan"}, "o": {"04:00:00", "timespan"},
		"s": {"1", "long"}, "u": {"1", "real"}, "lt": {"false", "bool"}, "it": {"true", "bool"},
	}
	for j, c := range r.Schema.Columns {
		w, ok := want[c.Name]
		if !ok {
			continue
		}
		if got := formatCell(r, 0, j); got != w.v {
			t.Errorf("%s = %s, want %s", c.Name, got, w.v)
		}
		if c.Type.String() != w.typ {
			t.Errorf("%s type = %s, want %s", c.Name, c.Type, w.typ)
		}
	}
	cases := []struct{ expr, want string }{
		{`2 * 1.5`, "3"}, {`1 + 0.5`, "1.5"}, {`1 < 1.5`, "true"}, {`7 / 2`, "3"}, {`7.0 / 2`, "3.5"},
		{`datetime(2020-01-01) + 2 * 1h`, "2020-01-01T02:00:00Z"}, {`1h * 1.5`, "01:30:00"}, {`1d / 4`, "06:00:00"},
	}
	for _, c := range cases {
		if got := formatCell(queryResult(t, `print x = `+c.expr), 0, 0); got != c.want {
			t.Errorf("%s = %s, want %s", c.expr, got, c.want)
		}
	}
	if got := formatCell(queryResult(t, `range x from 1 to 3 step 1 | where x < 1.5 | count`), 0, 0); got != "1" {
		t.Errorf("long < real filter count = %s, want 1", got)
	}
}

func TestPercentileNearestRank(t *testing.T) {
	assertCSVRows(t, ipLookupRows(t, `range x from 1 to 24 step 1 | summarize percentile(x, 50), percentile(x, 0), percentile(x, 100), percentile(x, 10), percentiles(x, 25, 99.5), p = percentile(x * 2, 50), percentile(x * 2, 50)`),
		[]string{"percentile_x_50,percentile_x_0,percentile_x_100,percentile_x_10,percentile_x_25,percentile_x_99_5,p,percentile__50",
			"12,1,24,3,6,24,24,24"})
	assertCSVRows(t, ipLookupRows(t, `range x from 1 to 24 step 1 | summarize (a, b) = percentiles(x, 5, 50), c = percentiles(x, 10, 90), percentiles_array(x, 5, 50)`),
		[]string{"a,b,c,percentile_x_90,percentiles_x", "2,12,3,22,[2,12]"})
	r := queryResult(t, `range x from 1 to 24 step 1 | extend i = toint(x), d = datetime(2020-01-01) + x * 1h, rr = x * 1.5 | summarize percentile(i, 50), percentile(d, 50), percentile(rr, 50)`)
	for j, w := range []struct{ v, typ string }{{"12", "long"}, {"2020-01-01T12:00:00Z", "datetime"}, {"18", "real"}} {
		if got := formatCell(r, 0, j); got != w.v || r.Schema.Columns[j].Type.String() != w.typ {
			t.Errorf("column %d = %s (%s), want %s (%s)", j, got, r.Schema.Columns[j].Type, w.v, w.typ)
		}
	}
	// weighted: 3 carries weight 10 of 13
	assertCSVRows(t, ipLookupRows(t, `datatable(v:long, w:long)[1,1, 2,1, 3,10, 4,1] | summarize percentilew(v, w, 50), percentilesw(v, w, 10, 90), percentilesw_array(v, w, 1, 99)`),
		[]string{"percentile_v_50,percentile_v_10,percentile_v_90,percentiles_v", "3,2,3,[1,4]"})
}
