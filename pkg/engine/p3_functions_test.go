package engine

import "testing"

// p3_functions_test.go — punycode family, series_stats expansion and
// buildschema against their reference examples.

func TestPunycodeReference(t *testing.T) {
	for _, c := range []struct{ expr, want string }{
		{`punycode_to_string('acadmie-franaise-npb1a')`, "académie-française"},
		{`punycode_from_string('académie-française')`, "acadmie-franaise-npb1a"},
		{`punycode_from_string('艺术')`, "cqv902d"},
		{`punycode_to_string('#bad')`, ""},
		{`punycode_to_string(punycode_from_string('Rúyì Jīngū'))`, "Rúyì Jīngū"},
	} {
		if got := formatCell(queryResult(t, `print x = `+c.expr), 0, 0); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
	assertCSVRows(t, ipLookupRows(t, `datatable(domain:string)['Lê Lợi。Thuận Thiên。com', 'Riðill｡Skáldskaparmál｡org', "Kaledvoulc'h.Artorījos.edu"] | project str = punycode_domain_to_string(domain)`),
		[]string{"str", "xn--L Li-gpa4517b.xn--Thun Thin-s4a7194f.com", "xn--Riill-jta.xn--Skldskaparml-dbbj.org", "Kaledvoulc'h.xn--Artorjos-ejb.edu"})
	assertCSVRows(t, ipLookupRows(t, `datatable(encoded:string)["xn--Ge-mia.Bulg.edu", "xn--Lin-8na.Celtchair.org", "xn--Ry-lja8c.xn--Jng-uta63a.xn--Bng-9ka.com"] | project domain = punycode_domain_from_string(encoded)`),
		[]string{"domain", "Gáe.Bulg.edu", "Lúin.Celtchair.org", "Rúyì.Jīngū.Bàng.com"})
}

func TestSeriesStatsReference(t *testing.T) {
	r := queryResult(t, `print x = dynamic([23, 46, 23, 87, 4, 8, 3, 75, 2, 56, 13, 75, 32, 16, 29]) | project series_stats(x)`)
	want := []struct{ name, val, typ string }{
		{"series_stats_x_min", "2", "real"}, {"series_stats_x_min_idx", "8", "long"},
		{"series_stats_x_max", "87", "real"}, {"series_stats_x_max_idx", "3", "long"},
		{"series_stats_x_avg", "32.8", "real"}, {"series_stats_x_stdev", "28.503633853548266", "real"},
		{"series_stats_x_variance", "812.4571428571428", "real"},
	}
	if len(r.Schema.Columns) != len(want) {
		t.Fatalf("columns: %v", r.Schema.Columns)
	}
	for i, w := range want {
		c := r.Schema.Columns[i]
		if c.Name != w.name || c.Type.String() != w.typ || formatCell(r, 0, i) != w.val {
			t.Errorf("column %d = %s:%s %s, want %s:%s %s", i, c.Name, c.Type, formatCell(r, 0, i), w.name, w.typ, w.val)
		}
	}
	r2 := queryResult(t, `print x = dynamic([1, 2, 3]) | extend (a, b) = series_stats(x)`)
	if names := columnNames(&r2.Schema); len(names) != 8 || names[1] != "a" || names[2] != "b" || names[3] != "series_stats_x_max" {
		t.Errorf("tuple names: %v", names)
	}
}

func TestBuildSchemaReference(t *testing.T) {
	r := queryResult(t, `datatable(value: dynamic) [dynamic({"x":1, "y":3.5}), dynamic({"x":"somevalue", "z":[1, 2, 3]}),
	dynamic({"y":{"w":"zzz"}, "t":["aa", "bb"], "z":["foo"]})] | summarize buildschema(value)`)
	want := "{\"x\":[\"long\",\"string\"],\"y\":[\"double\",{\"w\":\"string\"}],\"z\":{\"`indexer`\":[\"long\",\"string\"]},\"t\":{\"`indexer`\":\"string\"}}"
	if r.Schema.Columns[0].Name != "schema_value" || formatCell(r, 0, 0) != want {
		t.Errorf("%s = %s", r.Schema.Columns[0].Name, formatCell(r, 0, 0))
	}
}

func TestSeriesFitReference(t *testing.T) {
	r := queryResult(t, `print y = dynamic([2, 5, 6, 8, 11, 15, 17, 18, 25, 26, 30, 30])
| extend (RSquare, Slope, Variance, RVariance, Interception, LineFit) = series_fit_line(y)
| extend f0 = todouble(LineFit[0]), f11 = todouble(LineFit[11]) | project-away y, LineFit`)
	// reference: 0.982, 2.730, 98.628, 1.686, -1.666, LineFit 1.064 ... 31.102
	want := []float64{0.982900730884447, 2.7307692307692304, 98.62878787878786, 1.686480186480187, -1.6666666666666667, 1.0641025641025641, 31.102564102564102}
	for i, w := range want {
		got := r.Rows[0][i].(float64)
		if d := got - w; d > 1e-9 || d < -1e-9 {
			t.Errorf("%s = %v, want %v", r.Schema.Columns[i].Name, got, w)
		}
	}
	// degree-1 poly equals the line; cubic against numpy.polyfit
	r2 := queryResult(t, `print x = dynamic([0, 1, 2, 3, 4]), y = dynamic([1, 0, 3, 10, 25]) | extend series_fit_poly(y, x, 3)
| project c0 = todouble(series_fit_poly_y_coefficients[0]), c3 = todouble(series_fit_poly_y_coefficients[3])`)
	if c0, c3 := r2.Rows[0][0].(float64), r2.Rows[0][1].(float64); c0-1.0/3 > 1e-9 || 1.0/3-c0 > 1e-9 || c3-0.94285714285714 > 1e-9 || 0.94285714285714-c3 > 1e-9 {
		t.Errorf("cubic coefficients %v %v", c0, c3)
	}
	cols := columnNames(&queryResult(t, `print y = dynamic([1, 2, 4]) | project series_fit_line(y)`).Schema)
	if len(cols) != 6 || cols[0] != "series_fit_line_y_rsquare" || cols[5] != "series_fit_line_y_line_fit" {
		t.Errorf("series_fit_line columns: %v", cols)
	}
}
