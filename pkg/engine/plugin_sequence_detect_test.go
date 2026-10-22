package engine

import "testing"

// plugin_sequence_detect_test.go — the reference StormEvents output,
// from an input reconstructed to contain its rows plus distractors (a
// wildfire more than 5 days after the heat, one in a state with no heat).

func TestSequenceDetectReference(t *testing.T) {
	q := `datatable(StartTime:datetime, EventType:string, State:string)[
	datetime(2007-05-08), 'Excessive Heat', 'CALIFORNIA', datetime(2007-05-08 16:02), 'Wildfire', 'CALIFORNIA',
	datetime(2007-05-10 11:30), 'Wildfire', 'CALIFORNIA', datetime(2007-05-20), 'Wildfire', 'CALIFORNIA',
	datetime(2007-07-04 09:00), 'Excessive Heat', 'CALIFORNIA', datetime(2007-07-05 23:01), 'Wildfire', 'CALIFORNIA',
	datetime(2007-07-23 12:00), 'Excessive Heat', 'SOUTH DAKOTA', datetime(2007-07-27 09:00), 'Wildfire', 'SOUTH DAKOTA',
	datetime(2007-08-10 08:00), 'Excessive Heat', 'TEXAS', datetime(2007-08-11 13:56), 'Wildfire', 'TEXAS',
	datetime(2007-08-31 08:00), 'Excessive Heat', 'CALIFORNIA', datetime(2007-09-01 11:28), 'Wildfire', 'CALIFORNIA',
	datetime(2007-09-02 12:00), 'Excessive Heat', 'CALIFORNIA', datetime(2007-09-02 13:30), 'Wildfire', 'CALIFORNIA',
	datetime(2007-08-11), 'Wildfire', 'OREGON']
| evaluate sequence_detect(StartTime, 5d, 5d, heat=(EventType == "Excessive Heat"), wildfire=(EventType == 'Wildfire'), State)`
	assertCSVRows(t, ipLookupRows(t, q), []string{
		"State,heat_StartTime,wildfire_StartTime,Duration",
		"CALIFORNIA,2007-05-08T00:00:00Z,2007-05-08T16:02:00Z,16:02:00",
		"CALIFORNIA,2007-05-08T00:00:00Z,2007-05-10T11:30:00Z,2.11:30:00",
		"CALIFORNIA,2007-07-04T09:00:00Z,2007-07-05T23:01:00Z,1.14:01:00",
		"SOUTH DAKOTA,2007-07-23T12:00:00Z,2007-07-27T09:00:00Z,3.21:00:00",
		"TEXAS,2007-08-10T08:00:00Z,2007-08-11T13:56:00Z,1.05:56:00",
		"CALIFORNIA,2007-08-31T08:00:00Z,2007-09-01T11:28:00Z,1.03:28:00",
		"CALIFORNIA,2007-08-31T08:00:00Z,2007-09-02T13:30:00Z,2.05:30:00",
		"CALIFORNIA,2007-09-02T12:00:00Z,2007-09-02T13:30:00Z,01:30:00",
	})
	// three steps, step window shorter than span; unnamed predicates
	q2 := `datatable(T:long, e:string)[1,'a', 2,'b', 3,'c', 10,'a', 12,'b', 20,'c']
| evaluate sequence_detect(T, 3, 5, e == 'a', e == 'b', e == 'c')`
	assertCSVRows(t, ipLookupRows(t, q2), []string{"_T,_T1,_T2,Duration", "1,2,3,2"})
}
