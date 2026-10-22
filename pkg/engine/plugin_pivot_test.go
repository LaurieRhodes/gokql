package engine

import "testing"

// plugin_pivot_test.go — evaluate pivot (the reference examples' shapes
// on a small StormEvents-like table) and evaluate narrow; tostring and
// strcat of datetime/timespan; bracket-quoted identifiers.

const pivotData = `datatable(State:string, EventType:string, DeathsDirect:long, Source:string)[
	'ARKANSAS','Heavy Rain',1,'EM', 'ARIZONA','Lightning',1,'NP',
	'ARIZONA','Flash Flood',2,'TS', 'ARIZONA','Flash Flood',3,'BM',
	'ARIZONA','Flash Flood',1,'NP', 'ARKANSAS','Heat',3,'NP']`

func TestPivotReference(t *testing.T) {
	cases := []struct {
		q    string
		want []string
	}{
		{`| project State, EventType | evaluate pivot(State)`, []string{
			"EventType,ARKANSAS,ARIZONA", "Heavy Rain,1,0", "Lightning,0,1", "Flash Flood,0,3", "Heat,1,0"}},
		{`| project State, EventType, DeathsDirect | evaluate pivot(State, sum(DeathsDirect))`, []string{
			"EventType,ARKANSAS,ARIZONA", "Heavy Rain,1,0", "Lightning,0,1", "Flash Flood,0,6", "Heat,3,0"}},
		{`| project State, EventType, DeathsDirect | evaluate pivot(State, sum(DeathsDirect), EventType)`, []string{
			"EventType,ARKANSAS,ARIZONA", "Heavy Rain,1,0", "Lightning,0,1", "Flash Flood,0,6", "Heat,3,0"}},
		{`| evaluate pivot(State, sum(DeathsDirect), EventType, Source)`, []string{
			"EventType,Source,ARKANSAS,ARIZONA", "Heavy Rain,EM,1,0", "Lightning,NP,0,1",
			"Flash Flood,TS,0,2", "Flash Flood,BM,0,3", "Flash Flood,NP,0,1", "Heat,NP,3,0"}},
		{`| project State, EventType | evaluate pivot(State): (EventType:string, ARIZONA:long, ALASKA:long)`, []string{
			"EventType,ARIZONA,ALASKA", "Heavy Rain,0,0", "Lightning,1,0", "Flash Flood,3,0", "Heat,0,0"}},
		{`| project State, EventType, DeathsDirect | evaluate pivot(State, avg(DeathsDirect))`, []string{
			"EventType,ARKANSAS,ARIZONA", "Heavy Rain,1,", "Lightning,,1", "Flash Flood,,2", "Heat,3,"}},
		{`| project State, EventType, DeathsDirect | evaluate pivot(State, make_list(DeathsDirect)) | where EventType == 'Heat'`, []string{
			"EventType,ARKANSAS,ARIZONA", "Heat,[3],[]"}},
	}
	for _, c := range cases {
		assertCSVRows(t, ipLookupRows(t, pivotData+c.q), c.want)
	}
}

func TestNarrow(t *testing.T) {
	q := `print a=1, b=datetime(2020-01-01), d=real(null), e=1h | evaluate narrow() | extend tr=gettype(Row), tv=gettype(Value)`
	assertCSVRows(t, ipLookupRows(t, q), []string{
		"Row,Column,Value,tr,tv",
		"0,a,1,long,string",
		"0,b,2020-01-01T00:00:00Z,long,string",
		"0,d,,long,string",
		"0,e,01:00:00,long,string",
	})
	r := queryResult(t, pivotData+` | take 2 | evaluate narrow() | summarize max(Row), count()`)
	if formatCell(r, 0, 0) != "1" || formatCell(r, 0, 1) != "8" {
		t.Errorf("narrow over 2x4 table: max Row %s, count %s", formatCell(r, 0, 0), formatCell(r, 0, 1))
	}
}

func TestToStringTemporal(t *testing.T) {
	cases := []struct{ expr, want string }{
		{`tostring(datetime(2020-01-01))`, "2020-01-01T00:00:00Z"},
		{`tostring(datetime(2020-01-01 10:00:00.5))`, "2020-01-01T10:00:00.5000000Z"},
		{`tostring(1h)`, "01:00:00"},
		{`tostring(500ms)`, "00:00:00.5000000"},
		{`tostring(-1.5d - 2s)`, "-1.12:00:02"},
		{`tostring(10d)`, "10.00:00:00"},
		{`strcat('at ', datetime(2020-01-01), ' for ', 90m)`, "at 2020-01-01T00:00:00Z for 01:30:00"},
		{`strcat_delim('|', 1h, datetime(2021-06-01))`, "01:00:00|2021-06-01T00:00:00Z"},
	}
	for _, c := range cases {
		if got := formatCell(queryResult(t, `print x = `+c.expr), 0, 0); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
	r := queryResult(t, `datatable(b:datetime, s:timespan)[datetime(2020-01-01 10:00), 2h] | project x = strcat(b, '/', s)`)
	if got := formatCell(r, 0, 0); got != "2020-01-01T10:00:00Z/02:00:00" {
		t.Errorf("column strcat = %q", got)
	}
}

func TestQuotedIdentifiers(t *testing.T) {
	assertCSVRows(t, ipLookupRows(t, `print ['a b'] = 1 | extend ['c d'] = 2, ['e'] = 3 | project-rename ['f g'] = ['a b'] | project-away ['c d']`),
		[]string{"f g,e", "1,3"})
	assertCSVRows(t, ipLookupRows(t, `print x=1, ['y z']=2 | project ['y'] = x, ['y z'], k=["y z"] + 1`),
		[]string{"y,y z,k", "1,2,3"})
	assertCSVRows(t, ipLookupRows(t, `datatable(['a b']:string, ['c:d']:long)['v',1,'v',2] | where ['c:d'] > 0 | summarize n=count() by ['a b']`),
		[]string{"a b,n", "v,2"})
	assertCSVRows(t, ipLookupRows(t, `print d = dynamic({'p':{'q':5}}) | project v = ['d'].p['q']`),
		[]string{"v", "5"})
}
