package engine

import "testing"

// Expected rows from the Kusto emulator (engine 2025.07.06).
func TestFunnelSequence(t *testing.T) {
	src := `datatable(Id:string, Ts:datetime, S:string)["u1",datetime(2020-01-01 01:00),"home","u1",datetime(2020-01-01 03:10),"cart","u1",datetime(2020-01-01 03:20),"pay","u1",datetime(2020-01-01 09:30),"done","u2",datetime(2020-01-01 02:00),"cart","u2",datetime(2020-01-01 02:05),"pay","u2",datetime(2020-01-01 02:06),"cart","u2",datetime(2020-01-01 02:07),"pay","u2",datetime(2020-01-01 02:08),"x","u5",datetime(2020-01-01 23:50),"cart","u5",datetime(2020-01-02 00:10),"pay","u6",datetime(2020-01-01 12:00),"cart","u6",datetime(2020-01-01 12:00),"pay","u7",datetime(2020-01-01 12:00),"cart","u7",datetime(2020-01-01 12:10),"cart","u7",datetime(2020-01-01 12:20),"pay"]`
	q := src + ` | evaluate funnel_sequence(Id, Ts, datetime(2020-01-01), datetime(2020-01-03), 1h, 6h, S, dynamic(['cart','pay']))`
	tbls := executeAll(t, q)
	if len(tbls) != 3 {
		t.Fatalf("got %d tables, want 3", len(tbls))
	}
	expectColNames(t, tbls[0], "Ts", "prev", "next", "dcount", "samples")
	expectColNames(t, tbls[1], "Ts", "prev", "dcount_userId", "samples")
	expectColNames(t, tbls[2], "Ts", "next", "dcount_userId", "samples")
	// u2: cart, pay, cart, pay, x -> ("", cart), ("", x), (pay, x); u6's
	// steps share a timestamp and do not match; u7's repeated cart gives
	// no "cart" prev.
	want := [][]string{
		{"2020-01-01T00:00:00Z", "", "", "1"},
		{"2020-01-01T00:00:00Z", "", "cart", "1"},
		{"2020-01-01T00:00:00Z", "", "x", "1"},
		{"2020-01-01T00:00:00Z", "pay", "x", "1"},
		{"2020-01-01T12:00:00Z", "", "", "1"},
		{"2020-01-01T18:00:00Z", "", "", "1"},
	}
	expectRows(t, tbls[0], len(want))
	for i, w := range want {
		for c, v := range w {
			expectCell(t, tbls[0], i, c, v)
		}
	}
	expectCell(t, tbls[1], 0, 2, "2")
	// prev/next skip same-time events and the boundary step's own state
	tbls = executeAll(t, `datatable(Id:string, Ts:datetime, S:string)["E",datetime(2020-01-01 09:10),"H","E",datetime(2020-01-01 09:30),"T","E",datetime(2020-01-01 10:00),"T","E",datetime(2020-01-01 10:20),"T","E",datetime(2020-01-01 10:50),"Z","T6",datetime(2020-01-01 09:55),"q","T6",datetime(2020-01-01 10:00),"T","T6",datetime(2020-01-01 10:00),"h"] | evaluate funnel_sequence(Id, Ts, datetime(2020-01-01), datetime(2020-01-03), 1h, 1d, S, dynamic(['T']))`)
	got := map[string]bool{}
	for _, r := range tbls[0].Rows {
		got[r[1].(string)+">"+r[2].(string)] = true
	}
	for _, k := range []string{">Z", "H>Z", "H>", "q>"} {
		if !got[k] {
			t.Errorf("missing prev>next row %q; got %v", k, got)
		}
	}
}

func TestFunnelSequenceCompletion(t *testing.T) {
	src := `datatable(Id:string, Ts:datetime, S:string)["u1",datetime(2020-01-01 01:00),"home","u1",datetime(2020-01-01 01:10),"cart","u1",datetime(2020-01-01 01:50),"pay","u1",datetime(2020-01-01 03:30),"done","u2",datetime(2020-01-01 02:00),"home","u2",datetime(2020-01-01 02:05),"cart","u3",datetime(2020-01-01 03:00),"home","u3",datetime(2020-01-01 07:00),"cart","u3",datetime(2020-01-01 07:10),"pay","u4",datetime(2020-01-02 01:00),"cart","u4",datetime(2020-01-02 01:01),"pay","u5",datetime(2020-01-01 23:00),"home","u5",datetime(2020-01-02 00:30),"cart"]`
	tbl := queryResult(t, src+` | evaluate funnel_sequence_completion(Id, Ts, datetime(2020-01-01), datetime(2020-01-03), 1d, S, dynamic(['home','cart','pay','done']), dynamic([1h, 4h, 1d])) | order by Ts asc, Period asc, StepId asc`)
	expectColNames(t, tbl, "Ts", "S", "Period", "dcount", "StepId")
	want := []string{"4", "2", "1", "0", "4", "2", "1", "1", "4", "3", "2", "1"}
	expectRows(t, tbl, len(want))
	for i, w := range want {
		expectCell(t, tbl, i, 3, w)
	}
	// ties: a step may share its predecessor's time if it comes later in the input
	for _, c := range []struct{ rows, want string }{
		{`"A",datetime(2020-01-01 10:00),"h","A",datetime(2020-01-01 10:00),"c","A",datetime(2020-01-01 10:00),"p"`, "1,1,1"},
		{`"B",datetime(2020-01-01 10:00),"p","B",datetime(2020-01-01 10:00),"c","B",datetime(2020-01-01 10:00),"h"`, "1,0,0"},
	} {
		tbl := queryResult(t, `datatable(Id:string, Ts:datetime, S:string)[`+c.rows+`] | evaluate funnel_sequence_completion(Id, Ts, datetime(2020-01-01), datetime(2020-01-02), 1d, S, dynamic(['h','c','p']), dynamic([1h])) | summarize s=strcat_array(make_list(dcount), ",")`)
		expectCell(t, tbl, 0, 0, c.want)
	}
}
