package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
)

// Expected values are the Kusto emulator's (engine 2025.07.06).

func expectSeries(t *testing.T, q string, col int, want []float64, tol float64) {
	t.Helper()
	tbl := queryResult(t, q)
	var got []interface{}
	if err := json.Unmarshal([]byte(fmt.Sprint(tbl.Rows[0][col])), &got); err != nil {
		t.Fatalf("%s: column %d is not an array: %v", q, col, tbl.Rows[0][col])
	}
	if len(got) != len(want) {
		t.Fatalf("%s: length %d, want %d", q, len(got), len(want))
	}
	for i, g := range got {
		w := want[i]
		if math.IsNaN(w) {
			if g != nil {
				t.Errorf("%s [%d] = %v, want null", q, i, g)
			}
			continue
		}
		f, ok := g.(float64)
		if !ok || math.Abs(f-w) > tol*math.Max(1, math.Abs(w)) {
			t.Errorf("%s [%d] = %v, want %v", q, i, g, w)
		}
	}
}

func TestSeriesOutliersEmulator(t *testing.T) {
	s19 := `dynamic([1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,100])`
	expectSeries(t, `print series_outliers(`+s19+`)`, 0, []float64{-0.3465757006884894, -0.16416743716823182,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0.1641674371682322, 14.939236782309097}, 1e-7)
	expectSeries(t, `print series_outliers(`+s19+`, "tukey")`, 0, []float64{-0.5013751473345671, -0.39582248473781617,
		-0.29026982214106517, -0.1847171595443142, -0.07916449694756322, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0.07916449694756322, 0.1847171595443142, 0.29026982214106517, 0.39582248473781617, 8.945588155074644}, 1e-7)
	expectSeries(t, `print series_outliers(dynamic([1,2,null,4,5,6,7,8,9,30]))`, 0,
		[]float64{-0.1945688144216083, 0, 0, 0, 0, 0, 0, 0, 0, 4.0859451028537785}, 1e-7)
	expectSeries(t, `print series_outliers(dynamic([1,2,-1,4,5,6,7,8,9,30]), "ctukey", -1)`, 0,
		[]float64{-0.1945688144216083, 0, 0, 0, 0, 0, 0, 0, 0, 4.0859451028537785}, 1e-7)
	expectSeries(t, `print series_outliers(dynamic([5,5,5,5,9]))`, 0, []float64{0, 0, 0, 0, 1.8484037370052793}, 1e-7)
	expectSeries(t, `print series_outliers(dynamic([1,2,3,4,5,6,7,8,9,10,-50]), "tukey")`, 0, []float64{-0.30082508840074024,
		-0.10027502946691343, 0, 0, 0, 0, 0, 0.10027502946691343, 0.30082508840074024, 0.5013751473345671, -10.52887809402591}, 1e-7)
	for _, q := range []string{`print series_outliers(dynamic([1,2]))`, `print series_outliers(dynamic([1,"x",3,4]))`} {
		if v := queryResult(t, q).Rows[0][0]; v != nil {
			t.Errorf("%s = %v, want null", q, v)
		}
	}
}

func TestSeriesDecomposeEmulator(t *testing.T) {
	s := `let s=dynamic([1,5,2,8,3,9,4,12,5,11,7,15,6,14,8,30]); `
	q := s + `print x=1 | extend (b, se, tr, r) = series_decompose(s, 2, 'avg') | project b, se, tr, r`
	expectSeries(t, q, 0, []float64{5.25, 12.25, 5.25, 12.25, 5.25, 12.25, 5.25, 12.25, 5.25, 12.25, 5.25, 12.25, 5.25, 12.25, 5.25, 12.25}, 1e-12)
	expectSeries(t, q, 3, []float64{-4.25, -7.25, -3.25, -4.25, -2.25, -3.25, -1.25, -0.25, -0.25, -1.25, 1.75, 2.75, 0.75, 1.75, 2.75, 17.75}, 1e-12)
	q = s + `print x=1 | extend (b, se, tr, r) = series_decompose(s, 2, 'linefit', 4) | project b, se, tr, r`
	expectSeries(t, q, 0, []float64{-0.32051282051282115, 6.889277389277389, 1.0990675990675984, 8.308857808857809,
		2.5186480186480185, 9.728438228438229, 3.938228438228438, 11.148018648018649, 5.357808857808858, 12.567599067599069,
		6.777389277389278, 13.987179487179487, 8.196969696969697, 15.406759906759907, 9.616550116550117, 16.82634032634033}, 1e-9)
	expectSeries(t, s+`print series_decompose_forecast(s, 4, 2)`, 0, []float64{-0.32051282051282115, 6.889277389277389,
		1.0990675990675984, 8.308857808857809, 2.5186480186480185, 9.728438228438229, 3.938228438228438, 11.148018648018649,
		5.357808857808858, 12.567599067599069, 6.777389277389278, 13.987179487179487, 8.196969696969697, 15.406759906759907,
		9.616550116550117, 16.82634032634033}, 1e-9)
	q = s + `print x=1 | extend (ad, sc, bl) = series_decompose_anomalies(s, 1.0, 2, 'linefit', 0, 'tukey') | project ad, sc, bl`
	expectSeries(t, q, 0, []float64{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, 0)
	expectSeries(t, q, 1, []float64{1.082391112296286, 0, 0.6769469498307872, 0, 0.271502787365289, 0, 0, 0, 0,
		-0.47784490576290894, 0, 0, -0.8398486222499614, -0.7964081762715153, -0.7529677302930692, 5.180273182929713}, 1e-7)
	// test points: the outlier fences come from the training residual only
	q = `let s=dynamic([1,5,2,8,3,9,4,12,5,11,7,15,6,14,-20,30]); print x=1 | extend (ad, sc, bl) = series_decompose_anomalies(s, 0.5, 2, 'avg', 3) | project ad, sc, bl`
	expectSeries(t, q, 0, []float64{0, -1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 1, -1, 1}, 0)
	expectSeries(t, q, 1, []float64{-0.09902162876813969, -1.089237916449538, 0, 0, 0, 0, 0, 0, 0, 0, 0.09902162876814036,
		1.0892379164495385, 0, 0.5941297726088395, -10.496292649422822, 8.515860074060026}, 1e-7)
	// a null makes the trend, baseline and residual null; seasonal skips it
	q = `let s=dynamic([1,5,null,8,3,9,4,12,5,null,7,15,6,14,8,30]); print x=1 | extend (b, se, tr, r) = series_decompose(s, 2, "avg") | project b, se, tr, r`
	expectSeries(t, q, 1, []float64{5, 12, 5, 12, 5, 12, 5, 12, 5, 12, 5, 12, 5, 12, 5, 12}, 0)
	n := math.NaN()
	expectSeries(t, q, 2, []float64{n, n, n, n, n, n, n, n, n, n, n, n, n, n, n, n}, 0)
	// column names follow Kusto.Language
	expectColNames(t, queryResult(t, `print x=dynamic([1,5,2,6,3,7,4,8,5,9]) | extend series_decompose(x, 2)`),
		"x", "series_decompose_x_baseline", "series_decompose_x_seasonal", "series_decompose_x_trend", "series_decompose_x_residual")
}

func TestSeriesFit2LinesEmulator(t *testing.T) {
	tbl := queryResult(t, `print x=dynamic([5,1,4,2,8,9,1,7]) | extend series_fit_2lines(x)`)
	expectColNames(t, tbl, "x", "series_fit_2lines_x_rsquare", "series_fit_2lines_x_split_idx", "series_fit_2lines_x_variance",
		"series_fit_2lines_x_rvariance", "series_fit_2lines_x_line_fit", "series_fit_2lines_x_right_rsquare",
		"series_fit_2lines_x_right_slope", "series_fit_2lines_x_right_interception", "series_fit_2lines_x_right_variance",
		"series_fit_2lines_x_right_rvariance", "series_fit_2lines_x_left_rsquare", "series_fit_2lines_x_left_slope",
		"series_fit_2lines_x_left_interception", "series_fit_2lines_x_left_variance", "series_fit_2lines_x_left_rvariance")
	want := []float64{0.5834738904506346, 6, 9.982142857142858, 4.157823129251701}
	for i, w := range want {
		if g := cellVal(t, tbl, 0, i+1); math.Abs(toF(g)-w) > 1e-9*math.Max(1, w) {
			t.Errorf("col %d = %v, want %v", i+1, g, w)
		}
	}
	// left line 1.1142857142857143x + 0.9333; right 6x - 5 (local x from 1)
	if g := toF(cellVal(t, tbl, 0, 12)); math.Abs(g-1.1142857142857143) > 1e-9 {
		t.Errorf("left slope = %v", g)
	}
	if g := toF(cellVal(t, tbl, 0, 8)); math.Abs(g+5) > 1e-9 {
		t.Errorf("right interception = %v", g)
	}
	expectSeries(t, `print x=dynamic([1,5,2]) | project series_fit_2lines_dynamic(x).line_fit`, 0, []float64{1, 5, 2}, 0)
	if g := toF(queryResult(t, `print toreal(series_fit_2lines_dynamic(dynamic([1,5,2])).variance)`).Rows[0][0]); math.Abs(g-2.8888888888888893) > 1e-12 {
		t.Errorf("3-point variance = %v", g)
	}
}

func toF(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	}
	return math.NaN()
}
