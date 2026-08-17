package engine

import (
	"encoding/json"
	"testing"
)

// make_series_test.go — the make-series operator. Every test below is
// checked against a real, worked example from real ADX's own docs
// (make-series-operator.md), values included, not just "does it run".
// See MakeSeriesOp's own doc comment (pkg/parser/ast.go) for exactly
// which parts of real ADX's documented grammar this covers (main
// syntax only; from/to both required; specified-delimiter-shaped
// aggregations only, not a bare-scalar-as-aggregation edge case one of
// the docs' own examples uses incidentally alongside its real subject).

// jsonArray decodes a make-series dynamic array column (stored as a
// JSON-encoded string, this engine's own dynamic-column convention)
// into a []interface{} for comparison.
func jsonArray(t *testing.T, s string) []interface{} {
	t.Helper()
	var arr []interface{}
	if err := json.Unmarshal([]byte(s), &arr); err != nil {
		t.Fatalf("jsonArray: invalid JSON %q: %v", s, err)
	}
	return arr
}

// TestMakeSeriesAvgWorkedExample guards real ADX's own primary
// make-series worked example exactly: a real-typed metric column,
// avg() aggregation with no explicit Column (auto-named avg_metric,
// matching this engine's existing summarize auto-naming convention),
// a default value of 0 (real ADX's own documented default when
// `default=` is omitted), and — critically — a data point
// (2016-12-31T06:00) BEFORE `from`, which must fall in the bin one
// step before start and therefore be excluded from every output bin
// entirely (the bin_at floor-division correctness case).
func TestMakeSeriesAvgWorkedExample(t *testing.T) {
	result := queryResult(t, `let data=datatable(timestamp:datetime, metric: real)
		[
		  datetime(2016-12-31T06:00), 50,
		  datetime(2017-01-01), 4,
		  datetime(2017-01-02), 3,
		  datetime(2017-01-03), 4,
		  datetime(2017-01-03T03:00), 6,
		  datetime(2017-01-05), 8,
		  datetime(2017-01-05T13:40), 13,
		  datetime(2017-01-06), 4,
		  datetime(2017-01-07), 3,
		  datetime(2017-01-08), 8,
		  datetime(2017-01-08T21:00), 8,
		  datetime(2017-01-09), 2,
		  datetime(2017-01-09T12:00), 11,
		  datetime(2017-01-10T05:00), 5,
		];
		let interval = 1d;
		let stime = datetime(2017-01-01);
		let etime = datetime(2017-01-10);
		data
		| make-series avg(metric) on timestamp from stime to etime step interval`)

	if result.RowCount() != 1 {
		t.Fatalf("expected 1 row, got %d", result.RowCount())
	}
	avgIdx := result.Schema.ColumnIndex("avg_metric")
	if avgIdx < 0 {
		t.Fatalf("expected auto-named column 'avg_metric', schema: %+v", result.Schema.Columns)
	}
	tsIdx := result.Schema.ColumnIndex("timestamp")

	avgArr := jsonArray(t, result.Rows[0][avgIdx].(string))
	want := []float64{4, 3, 5, 0, 10.5, 4, 3, 8, 6.5}
	if len(avgArr) != len(want) {
		t.Fatalf("avg_metric array length = %d, want %d: %v", len(avgArr), len(want), avgArr)
	}
	for i, w := range want {
		got, ok := avgArr[i].(float64)
		if !ok || got != w {
			t.Errorf("avg_metric[%d] = %v, want %v", i, avgArr[i], w)
		}
	}

	tsArr := jsonArray(t, result.Rows[0][tsIdx].(string))
	if len(tsArr) != 9 {
		t.Fatalf("timestamp array length = %d, want 9 (the 2016-12-31 and 2017-01-10 points must be excluded, outside [start,end))", len(tsArr))
	}
	if tsArr[0] != "2017-01-01T00:00:00.0000000Z" {
		t.Errorf("timestamp[0] = %v, want 2017-01-01T00:00:00.0000000Z", tsArr[0])
	}
	if tsArr[8] != "2017-01-09T00:00:00.0000000Z" {
		t.Errorf("timestamp[8] = %v, want 2017-01-09T00:00:00.0000000Z", tsArr[8])
	}
}

// TestMakeSeriesEmptyInputZeroRows guards real ADX's own documented
// empty-input behavior: "When the input to make-series is empty, the
// default behavior of make-series produces an empty result" — 0 rows,
// not 1 row of all-default values.
func TestMakeSeriesEmptyInputZeroRows(t *testing.T) {
	result := queryResult(t, `let data=datatable(timestamp:datetime, metric: real) [datetime(2017-01-01), 4];
		data
		| take 0
		| make-series avg(metric) default=1.0 on timestamp from datetime(2017-01-01) to datetime(2017-01-10) step 1d`)
	if result.RowCount() != 0 {
		t.Fatalf("expected 0 rows for empty input without kind=nonempty, got %d", result.RowCount())
	}
}

// TestMakeSeriesKindNonEmpty guards real ADX's own kind=nonempty
// worked example: the same empty input as above, but with
// kind=nonempty, producing exactly 1 row entirely filled with the
// default value in every bin.
func TestMakeSeriesKindNonEmpty(t *testing.T) {
	result := queryResult(t, `let data=datatable(timestamp:datetime, metric: real) [datetime(2017-01-01), 4];
		data
		| take 0
		| make-series kind=nonempty avg(metric) default=1.0 on timestamp from datetime(2017-01-01) to datetime(2017-01-10) step 1d`)
	if result.RowCount() != 1 {
		t.Fatalf("expected 1 row with kind=nonempty, got %d", result.RowCount())
	}
	avgIdx := result.Schema.ColumnIndex("avg_metric")
	arr := jsonArray(t, result.Rows[0][avgIdx].(string))
	if len(arr) != 9 {
		t.Fatalf("expected 9 bins, got %d", len(arr))
	}
	for i, v := range arr {
		if v.(float64) != 1.0 {
			t.Errorf("avg_metric[%d] = %v, want 1.0 (the default, every bin)", i, v)
		}
	}
}

// TestMakeSeriesSumWithDefaultAndMvExpand guards real ADX's own
// sum()+default()+mv-expand worked example (the "fill values for
// missing records" one) — checked end to end through mv-expand, the
// way the docs' own example uses it, not just the raw array.
func TestMakeSeriesSumWithDefaultAndMvExpand(t *testing.T) {
	result := queryResult(t, `let startDate = datetime(2025-01-06);
		let endDate = datetime(2025-02-09);
		let data = datatable(Time: datetime, Value: int)
		[
		    datetime(2025-01-07), 10,
		    datetime(2025-01-16), 20,
		    datetime(2025-02-01), 30
		];
		data
		| make-series Value=sum(Value) default=-2 on Time from startDate to endDate step 7d
		| mv-expand Value, Time
		| extend Time=todatetime(Time), Value=toint(Value)
		| project-reorder Time, Value`)

	valIdx := result.Schema.ColumnIndex("Value")
	if valIdx < 0 {
		t.Fatalf("expected column 'Value', schema: %+v", result.Schema.Columns)
	}
	want := []int64{10, 20, -2, 30, -2}
	if result.RowCount() != len(want) {
		t.Fatalf("expected %d rows, got %d", len(want), result.RowCount())
	}
	for i, w := range want {
		got := result.Rows[i][valIdx]
		gotI, ok := got.(int64)
		if !ok || gotI != w {
			t.Errorf("row %d Value = %v (%T), want %d", i, got, got, w)
		}
	}
}

// TestMakeSeriesByClause confirms multiple groups (the `by` clause)
// each get their own independently-binned array, sharing the same
// axis array — not covered by any single real-ADX doc snippet with
// exact values, so this checks internal consistency (per-group avg
// values match hand-computed expectations) rather than a docs
// citation.
func TestMakeSeriesByClause(t *testing.T) {
	result := queryResult(t, `datatable(t:datetime, v:real, grp:string)
		[
		  datetime(2020-01-01), 1, "A",
		  datetime(2020-01-02), 2, "A",
		  datetime(2020-01-01), 10, "B",
		  datetime(2020-01-03), 30, "B"
		]
		| make-series avg(v) on t from datetime(2020-01-01) to datetime(2020-01-04) step 1d by grp
		| project grp, avg_v`)

	if result.RowCount() != 2 {
		t.Fatalf("expected 2 groups (A, B), got %d", result.RowCount())
	}
	grpIdx := result.Schema.ColumnIndex("grp")
	avgIdx := result.Schema.ColumnIndex("avg_v")
	byGroup := map[string][]interface{}{}
	for _, row := range result.Rows {
		byGroup[row[grpIdx].(string)] = jsonArray(t, row[avgIdx].(string))
	}
	wantA := []float64{1, 2, 0}
	for i, w := range wantA {
		if byGroup["A"][i].(float64) != w {
			t.Errorf("group A avg_v[%d] = %v, want %v", i, byGroup["A"][i], w)
		}
	}
	wantB := []float64{10, 0, 30}
	for i, w := range wantB {
		if byGroup["B"][i].(float64) != w {
			t.Errorf("group B avg_v[%d] = %v, want %v", i, byGroup["B"][i], w)
		}
	}
}

// TestMakeSeriesRejectsNonNumericAggregation guards real ADX's own
// documented restriction: "Only aggregation functions that return
// numeric results can be used with the make-series operator."
func TestMakeSeriesRejectsNonNumericAggregation(t *testing.T) {
	queryError(t, `datatable(t:datetime,s:string)[datetime(2020-01-01),"x"]
		| make-series make_list(s) on t from datetime(2020-01-01) to datetime(2020-01-02) step 1d`)
}

// TestMakeSeriesRequiresFromAndTo guards this engine's own documented
// scope limitation (real ADX allows omitting from/to and auto-detects
// from the data; this engine requires both explicitly, per
// MakeSeriesOp's own doc comment).
func TestMakeSeriesRequiresFromAndTo(t *testing.T) {
	queryError(t, `datatable(t:datetime,v:real)[datetime(2020-01-01),1]
		| make-series avg(v) on t step 1d`)
}

// TestMakeSeriesEnforcesCap guards real ADX's own documented 2^20
// (1,048,576) generated-array cap.
func TestMakeSeriesEnforcesCap(t *testing.T) {
	queryError(t, `datatable(t:long,v:real)[1,1]
		| make-series avg(v) on t from 0 to 100000000 step 1`)
}

// TestMakeSeriesRejectsNonPositiveStep confirms a zero or negative
// step is rejected with a clear error rather than looping forever or
// producing a nonsensical result.
func TestMakeSeriesRejectsNonPositiveStep(t *testing.T) {
	queryError(t, `datatable(t:long,v:real)[1,1]
		| make-series avg(v) on t from 0 to 10 step 0`)
	queryError(t, `datatable(t:long,v:real)[1,1]
		| make-series avg(v) on t from 0 to 10 step -1`)
}

// TestMakeSeriesRealAxis confirms the float64 code path (a real-typed,
// not datetime-typed, AxisColumn) bins correctly — the numeric
// counterpart to the datetime-axis worked example above, since real
// ADX's own docs only show a datetime AxisColumn example.
func TestMakeSeriesRealAxis(t *testing.T) {
	result := queryResult(t, `datatable(x:real, v:real)
		[
		  0.5, 10,
		  1.5, 20,
		  2.5, 30
		]
		| make-series avg(v) on x from 0.0 to 3.0 step 1.0`)
	avgIdx := result.Schema.ColumnIndex("avg_v")
	arr := jsonArray(t, result.Rows[0][avgIdx].(string))
	want := []float64{10, 20, 30}
	if len(arr) != len(want) {
		t.Fatalf("expected %d bins, got %d: %v", len(want), len(arr), arr)
	}
	for i, w := range want {
		if arr[i].(float64) != w {
			t.Errorf("avg[%d] = %v, want %v", i, arr[i], w)
		}
	}
}

