package engine

import "testing"

// plugin_rolling_test.go — sliding_window_counts, rolling_percentile
// and schema_merge against their reference examples.

func TestSlidingWindowCountsReference(t *testing.T) {
	q := `let start = datetime(2017-08-01); let end = datetime(2017-08-07); let lookbackWindow = 3d; let bin = 1d;
let T = datatable(UserId: string, Timestamp: datetime)['Bob', datetime(2017-08-01), 'David', datetime(2017-08-01),
	'David', datetime(2017-08-01), 'John', datetime(2017-08-01), 'Bob', datetime(2017-08-01), 'Ananda', datetime(2017-08-02),
	'Atul', datetime(2017-08-02), 'John', datetime(2017-08-02), 'Ananda', datetime(2017-08-03), 'Atul', datetime(2017-08-03),
	'Atul', datetime(2017-08-03), 'John', datetime(2017-08-03), 'Bob', datetime(2017-08-03), 'Betsy', datetime(2017-08-04),
	'Bob', datetime(2017-08-05)];
T | evaluate sliding_window_counts(UserId, Timestamp, start, end, lookbackWindow, bin)`
	assertCSVRows(t, ipLookupRows(t, q), []string{
		"Timestamp,Count,Dcount",
		"2017-08-01T00:00:00Z,5,3", "2017-08-02T00:00:00Z,8,5", "2017-08-03T00:00:00Z,13,5",
		"2017-08-04T00:00:00Z,9,5", "2017-08-05T00:00:00Z,7,5", "2017-08-06T00:00:00Z,2,2",
		"2017-08-07T00:00:00Z,1,1",
	})
	// a dimension slices the windows; End bounds the output
	q2 := `datatable(Id:string, T:long, D:string)['a',1,'x', 'b',2,'x', 'a',2,'y', 'c',9,'x']
| evaluate sliding_window_counts(Id, T, 0, 4, 2, 1, D)`
	assertCSVRows(t, ipLookupRows(t, q2), []string{
		"T,D,Count,Dcount", "1,x,1,1", "2,x,2,2", "3,x,1,1", "2,y,1,1", "3,y,1,1",
	})
}

const rollingData = `let T = range idx from 0 to 24 * 10 - 1 step 1
| project Timestamp = datetime(2018-01-01) + 1h * idx, val = idx + 1
| extend EvenOrOdd = iff(val % 2 == 0, "Even", "Odd");
`

func TestRollingPercentileReference(t *testing.T) {
	want := []string{"Timestamp,rolling_3_percentile_val_50"}
	vals := []string{"12", "24", "36", "60", "84", "108", "132", "156", "180", "204"}
	for i, v := range vals {
		want = append(want, "2018-01-"+pad2(i+1)+"T00:00:00Z,"+v)
	}
	assertCSVRows(t, ipLookupRows(t, rollingData+`T | evaluate rolling_percentile(val, 50, Timestamp, 1d, 3)`), want)

	want = []string{"Timestamp,EvenOrOdd,rolling_3_percentile_val_50"}
	odd := []string{"11", "23", "35", "59", "83", "107", "131", "155", "179", "203"}
	for i, v := range vals {
		want = append(want, "2018-01-"+pad2(i+1)+"T00:00:00Z,Even,"+v)
	}
	for i, v := range odd {
		want = append(want, "2018-01-"+pad2(i+1)+"T00:00:00Z,Odd,"+v)
	}
	assertCSVRows(t, ipLookupRows(t, rollingData+`T | evaluate rolling_percentile(val, 50, Timestamp, 1d, 3, EvenOrOdd)`), want)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func TestSchemaMergeReference(t *testing.T) {
	assertCSVRows(t, ipLookupRows(t, `let schema1 = datatable(Uri:string, HttpStatus:int)[] | getschema;
let schema2 = datatable(Uri:string, HttpStatus:int, Referrer:string)[] | getschema;
union schema1, schema2 | evaluate schema_merge()`), []string{
		"ColumnName,ColumnOrdinal,DataType,ColumnType",
		"Uri,0,System.String,string", "HttpStatus,1,System.Int32,int", "Referrer,2,System.String,string"})
	assertCSVRows(t, ipLookupRows(t, `let schema1 = datatable(Uri:string, HttpStatus:int)[] | getschema;
let schema2 = datatable(Uri:string, Referrer:string, HttpStatus:int)[] | getschema;
union schema1, schema2 | evaluate schema_merge()`), []string{
		"ColumnName,ColumnOrdinal,DataType,ColumnType",
		"Uri,0,System.String,string", "Referrer,1,System.String,string",
		"HttpStatus,-1,ERROR(unknown CSL type:ERROR(columns are out of order)),ERROR(columns are out of order)"})
	assertCSVRows(t, ipLookupRows(t, `let schema1 = datatable(Uri:string, HttpStatus:int)[] | getschema;
let schema2 = datatable(Uri:string, Referrer:string, HttpStatus:int)[] | getschema;
union schema1, schema2 | evaluate schema_merge(PreserveOrder = false)`), []string{
		"ColumnName,ColumnOrdinal,DataType,ColumnType",
		"Uri,0,System.String,string", "Referrer,1,System.String,string", "HttpStatus,2,System.Int32,int"})
	// numeric types reduce to the wider
	assertCSVRows(t, ipLookupRows(t, `union (datatable(x:int)[] | getschema), (datatable(x:real)[] | getschema) | evaluate schema_merge()`),
		[]string{"ColumnName,ColumnOrdinal,DataType,ColumnType", "x,0,System.Double,real"})
}
