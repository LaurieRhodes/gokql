package engine

import (
	"math"
	"testing"
)

func TestSeriesPeriodsDetect(t *testing.T) {
	// reference example: the 14-bin period (ADX scores it 0.84)
	tbl := queryResult(t, `print y=dynamic([80, 139, 87, 110, 68, 54, 50, 51, 53, 133, 86, 141, 97, 156, 94, 149, 95, 140, 77, 61, 50, 54, 47, 133, 72, 152, 94, 148, 105, 162, 101, 160, 87, 63, 53, 55, 54, 151, 103, 189, 108, 183, 113, 175, 113, 178, 90, 71, 62, 62, 65, 165, 109, 181, 115, 182, 121, 178, 114, 170]) | project series_periods_detect(y, 0.0, 50.0, 2)`)
	expectColNames(t, tbl, "series_periods_detect_y_periods", "series_periods_detect_y_scores")
	expectSeries(t, `print y=dynamic([80, 139, 87, 110, 68, 54, 50, 51, 53, 133, 86, 141, 97, 156, 94, 149, 95, 140, 77, 61, 50, 54, 47, 133, 72, 152, 94, 148, 105, 162, 101, 160, 87, 63, 53, 55, 54, 151, 103, 189, 108, 183, 113, 175, 113, 178, 90, 71, 62, 62, 65, 165, 109, 181, 115, 182, 121, 178, 114, 170]) | project series_periods_detect(y, 0.0, 50.0, 2)`, 0, []float64{14, 0}, 0)
	// a pure sine whose length is a whole number of periods scores 1
	q := `let s=toscalar(range t from 0 to 99 step 1 | extend y=sin(2*pi()*t/10) | summarize make_list(y)); print x=1 | extend (p, sc) = series_periods_detect(s, 0.0, 50.0, 3) | project p, sc`
	expectSeries(t, q, 0, []float64{10, 0, 0}, 0)
	expectSeries(t, q, 1, []float64{1, 0, 0}, 1e-9)
	// weekly with a daily pattern, hourly bins: 168 then 24 (as ADX)
	q = `let s=toscalar(range t from 0 to 335 step 1 | extend y=iff((t/24)%7>=5, 10.0, 15.0) - (((t%24)/10)*((t%24)/10)) + ((t*7919)%101)/50.0 | summarize make_list(y)); print x=1 | extend (p, sc) = series_periods_detect(s, 0.0, 200.0, 2) | project p`
	expectSeries(t, q, 0, []float64{168, 24}, 0)
}

func TestSeriesPeriodsValidate(t *testing.T) {
	q := `let s=toscalar(range t from 0 to 99 step 1 | extend y=sin(2*pi()*t/10) | summarize make_list(y)); print x=1 | extend (p, sc) = series_periods_validate(s, 10.0, 9.0, 11.0, 20.0, 5.0, 3.0) | project p, sc`
	expectSeries(t, q, 0, []float64{10, 9, 11, 20, 5, 3}, 0)
	// ADX's values: (1 + cos 36°)/2 next to the period, 0 off the spectral peak or below 4
	c := (1 + math.Cos(math.Pi/5)) / 2
	expectSeries(t, q, 1, []float64{1, c, c, 0, 0, 0}, 1e-9)
}

func TestSeriesDecomposeAutoSeasonality(t *testing.T) {
	// seasonality -1 (the default) detects the weekly period; the
	// emulator's baseline and forecast match these to 1e-6
	w := `let s=toscalar(range t from 0 to 335 step 1 | extend y=iff((t/24)%7>=5, 10.0, 15.0) - (((t%24)/10)*((t%24)/10)) + ((t*7919)%101)/50.0 + iff(t==100 or t==250, 9.0, 0.0) | summarize make_list(y)); `
	tbl := queryResult(t, w+`print x=1 | extend (ad, sc, bl) = series_decompose_anomalies(s) | project a=array_sum(ad), i1=ad[100], i2=ad[250]`)
	expectCell(t, tbl, 0, 1, "1")
	expectCell(t, tbl, 0, 2, "1")
	tbl = queryResult(t, w+`print f=series_decompose_forecast(s, 24)`)
	if g := toF(queryResult(t, w+`print toreal(series_decompose_forecast(s, 24)[0])`).Rows[0][0]); math.Abs(g-12.625436225116736) > 1e-9 {
		t.Errorf("forecast[0] = %v, want 12.625436225116736 (period 144 on the training points, as ADX)", g)
	}
	_ = tbl
}
