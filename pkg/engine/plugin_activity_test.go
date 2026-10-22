package engine

import "testing"

// plugin_activity_test.go — activity_counts_metrics, active_users_count,
// new_activity_metrics and activity_metrics against their references.

func TestActivityCountsMetricsReference(t *testing.T) {
	q := `let start=datetime(2017-08-01); let end=datetime(2017-08-04); let window=1d;
let T = datatable(UserId:string, Timestamp:datetime)['A', datetime(2017-08-01), 'D', datetime(2017-08-01),
	'J', datetime(2017-08-01), 'B', datetime(2017-08-01), 'C', datetime(2017-08-02), 'T', datetime(2017-08-02),
	'J', datetime(2017-08-02), 'H', datetime(2017-08-03), 'T', datetime(2017-08-03), 'T', datetime(2017-08-03),
	'J', datetime(2017-08-03), 'B', datetime(2017-08-03), 'S', datetime(2017-08-03), 'S', datetime(2017-08-04)];
T | evaluate activity_counts_metrics(UserId, Timestamp, start, end, window)`
	assertCSVRows(t, ipLookupRows(t, q), []string{
		"Timestamp,count,dcount,new_dcount,aggregated_dcount",
		"2017-08-01T00:00:00Z,4,4,4,4", "2017-08-02T00:00:00Z,3,3,2,6",
		"2017-08-03T00:00:00Z,6,5,2,8", "2017-08-04T00:00:00Z,1,1,0,8",
	})
}

func TestActiveUsersCountReference(t *testing.T) {
	q := `let Start = datetime(2018-07-01); let End = datetime(2018-07-31); let LookbackWindow = 8d;
let Period = 1d; let ActivePeriods = 3; let Bin = 7d;
let T = datatable(User:string, Timestamp:datetime)["B", datetime(2018-06-29), "B", datetime(2018-06-30),
	"A", datetime(2018-07-02), "B", datetime(2018-07-04), "B", datetime(2018-07-08), "A", datetime(2018-07-10),
	"A", datetime(2018-07-14), "A", datetime(2018-07-17), "A", datetime(2018-07-20), "B", datetime(2018-07-24)];
T | evaluate active_users_count(User, Timestamp, Start, End, LookbackWindow, Period, ActivePeriods, Bin)`
	assertCSVRows(t, ipLookupRows(t, q), []string{
		"Timestamp,dcount", "2018-07-01T00:00:00Z,1", "2018-07-15T00:00:00Z,1",
	})
}

const newActivityUsers = `let Users = datatable(Timestamp:datetime, s:dynamic)[
	datetime(2019-11-01), dynamic([0,2,3,4]), datetime(2019-11-02), dynamic([0,1,3,4,5]),
	datetime(2019-11-03), dynamic([0,2,4,5]), datetime(2019-11-04), dynamic([0,1,2,3]),
	datetime(2019-11-05), dynamic([0,1,2,3,4])]
	| mv-expand user = s to typeof(long) | project Timestamp, user;
`

func TestNewActivityMetricsReference(t *testing.T) {
	q := newActivityUsers + `let StartDate = datetime(2019-11-01 00:00:00); let EndDate = datetime(2019-11-07 00:00:00);
Users | evaluate new_activity_metrics(user, Timestamp, StartDate, EndDate-1tick, 1d)
| where from_Timestamp < datetime(2019-11-03 00:00:00.0000000)`
	h := "from_Timestamp,to_Timestamp,dcount_new_values,dcount_retained_values,dcount_churn_values,retention_rate,churn_rate"
	assertCSVRows(t, ipLookupRows(t, q), []string{h,
		"2019-11-01T00:00:00Z,2019-11-01T00:00:00Z,4,4,0,1,0",
		"2019-11-01T00:00:00Z,2019-11-02T00:00:00Z,2,3,1,0.75,0.25",
		"2019-11-01T00:00:00Z,2019-11-03T00:00:00Z,1,3,1,0.75,0.25",
		"2019-11-01T00:00:00Z,2019-11-04T00:00:00Z,1,3,1,0.75,0.25",
		"2019-11-01T00:00:00Z,2019-11-05T00:00:00Z,1,4,0,1,0",
		"2019-11-01T00:00:00Z,2019-11-06T00:00:00Z,0,0,4,0,1",
		"2019-11-02T00:00:00Z,2019-11-02T00:00:00Z,2,2,0,1,0",
		"2019-11-02T00:00:00Z,2019-11-03T00:00:00Z,0,1,1,0.5,0.5",
		"2019-11-02T00:00:00Z,2019-11-04T00:00:00Z,0,1,1,0.5,0.5",
		"2019-11-02T00:00:00Z,2019-11-05T00:00:00Z,0,1,1,0.5,0.5",
		"2019-11-02T00:00:00Z,2019-11-06T00:00:00Z,0,0,2,0,1",
	})
	// a single cohort, and a Lookback table excluding users 0-2
	q2 := newActivityUsers + `Users | evaluate new_activity_metrics(user, Timestamp, datetime(2019-11-01), datetime(2019-11-03), 1d, datetime(2019-11-01), Users | where user < 3)`
	assertCSVRows(t, ipLookupRows(t, q2), []string{h,
		"2019-11-01T00:00:00Z,2019-11-01T00:00:00Z,2,2,0,1,0",
		"2019-11-01T00:00:00Z,2019-11-02T00:00:00Z,1,2,0,1,0",
		"2019-11-01T00:00:00Z,2019-11-03T00:00:00Z,1,1,1,0.5,0.5",
	})
}

func TestActivityMetrics(t *testing.T) {
	q := newActivityUsers + `Users | evaluate activity_metrics(user, Timestamp, datetime(2019-11-01), datetime(2019-11-05), 1d)`
	assertCSVRows(t, ipLookupRows(t, q), []string{
		"Timestamp,dcount_values,dcount_newvalues,retention_rate,churn_rate",
		"2019-11-01T00:00:00Z,4,4,NaN,NaN",
		"2019-11-02T00:00:00Z,5,2,0.75,0.25",
		"2019-11-03T00:00:00Z,4,1,0.6,0.4",
		"2019-11-04T00:00:00Z,4,2,0.5,0.5",
		"2019-11-05T00:00:00Z,5,1,1,0",
	})
	// calendar periods
	q2 := newActivityUsers + `Users | evaluate activity_counts_metrics(user, Timestamp, datetime(2019-10-27), datetime(2019-11-30), 'week')`
	assertCSVRows(t, ipLookupRows(t, q2), []string{
		"Timestamp,count,dcount,new_dcount,aggregated_dcount",
		"2019-10-27T00:00:00Z,9,6,6,6", "2019-11-03T00:00:00Z,13,6,0,6",
	})
}
