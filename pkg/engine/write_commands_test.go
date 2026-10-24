package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// execErr runs q and returns its error ("" when it succeeds).
func execErr(t *testing.T, eng *Engine, q string) string {
	t.Helper()
	stmt, err := parser.Parse(q)
	if err == nil {
		_, err = eng.Execute(stmt)
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func expectErrContains(t *testing.T, eng *Engine, q, want string) {
	t.Helper()
	got := execErr(t, eng, q)
	if got == "" || !strings.Contains(got, want) {
		t.Fatalf("%s\n error %q, want one containing %q", q, got, want)
	}
}

// expectCol checks one column of a command's result.
func expectCol(t *testing.T, eng *Engine, q, col string, want ...string) {
	t.Helper()
	tbl := diskExec(t, eng, q)
	ci := tbl.Schema.ColumnIndex(col)
	var got []string
	for _, r := range tbl.Rows {
		s := cellString(r[ci])
		if i := strings.Index(s, ":"); i >= 0 && strings.HasPrefix(s, "FAILED") {
			s = s[:i]
		}
		got = append(got, s)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s\n got %v\nwant %v", q, got, want)
	}
}

func expectWrRows(t *testing.T, eng *Engine, q string, want ...string) {
	t.Helper()
	got := gmRows(t, eng, q)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s\n got:\n  %s\nwant:\n  %s", q, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestSetOrAppendOptionsAndSchema(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.set-or-append T <| print x=1`)
	// An extra column is an error, not silently dropped.
	expectErrContains(t, eng, `.set-or-append T <| print x=2, y=4`, "extend_schema")
	// with (...) is a property list, not part of the table name.
	diskExec(t, eng, `.set-or-append T with (extend_schema=true) <| print x=3, y=4`)
	expectWrRows(t, eng, `.show tables | project TableName`, "T")
	expectWrRows(t, eng, `T | order by x asc`, "1 | ", "3 | 4")
	expectErrContains(t, eng, `.set-or-append T with (foo=1) <| print x=1`, `unsupported property "foo"`)
	expectErrContains(t, eng, `.append U <| print x=1`, "does not exist")
	diskExec(t, eng, `.append T <| print x=5, y=long(null)`)
	expectWrRows(t, eng, `T | count`, "3")
}

func TestSetOrReplaceAndDeleteRecords(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.set-or-append T <| datatable(x:long, s:string)[1,"a", 2,"b"]`)
	diskExec(t, eng, `.set-or-append T <| datatable(x:long, s:string)[3,"c"]`)
	before := gmRows(t, eng, `T | where x == 1 | project ingestion_time()`)

	expectWrRows(t, eng, `.delete table T records with (whatif=true) <| T | where x >= 2`, "WhatIf | 2 | 0")
	expectWrRows(t, eng, `T | count`, "3")
	expectWrRows(t, eng, `.delete table T records <| T | where x == 2`, "OK | 1 | 1")
	expectWrRows(t, eng, `T | order by x asc | project x`, "1", "3")
	// Kept rows keep their original ingestion time.
	expectWrRows(t, eng, `T | where x == 1 | project ingestion_time()`, before...)
	expectErrContains(t, eng, `.delete table T records <| T | take 1`, "only where operators")

	diskExec(t, eng, `.set-or-replace T <| T | where x == 3`)
	expectWrRows(t, eng, `T | project x`, "3")
	diskExec(t, eng, `.set-or-replace T with (recreate_schema=true) <| print z="new"`)
	expectWrRows(t, eng, `T`, "new")
}

func TestExecuteDatabaseScript(t *testing.T) {
	eng := diskEngineEmpty(t)
	script := ".execute database script <|\n.set-or-append A <| print a=1\n.set-or-append A <| print a=2, b=3\n.set-or-append B <| print b=1"
	expectCol(t, eng, script, "Result", "Completed", "Failed", "Skipped")
	expectWrRows(t, eng, `.show tables | project TableName`, "A")
	diskExec(t, eng, strings.Replace(script, "script <|", "script with (ContinueOnErrors=true) <|", 1))
	expectWrRows(t, eng, `.show tables | project TableName | order by TableName asc`, "A", "B")
	expectErrContains(t, eng, strings.Replace(script, "script <|", "script with (ThrowOnErrors=true) <|", 1), "command 2 failed")

	// Atomic: a failing command leaves nothing written.
	expectErrContains(t, eng, ".execute database script with (Atomic=true) <|\n.set-or-append C <| print c=1\n"+
		`.set-or-append D <| print d=1 | where assert(false, "invalid claim")`, "nothing written")
	if eng.Catalog.GetTable("C") != nil {
		t.Fatal("atomic script wrote C before a later command failed")
	}
	diskExec(t, eng, ".execute database script with (Atomic=true) <|\n.set-or-append C <| print c=1\n.set-or-append D <| print d=2")
	expectWrRows(t, eng, `union C, D | count`, "2")
}

func TestUpdatePolicy(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table RawClaims (Id:string, Status:string)`)
	diskExec(t, eng, `.create table Claims (Id:string, Status:string)`)
	diskExec(t, eng, `.create-or-alter function ValidClaims() { RawClaims | extend ok = assert(Status in ("asserted", "computed", "cited", "ruled"), "bad status") | project-away ok }`)
	diskExec(t, eng, `.alter table Claims policy update @'[{"IsEnabled": true, "Source": "RawClaims", "Query": "ValidClaims()", "IsTransactional": true, "BlockDirectWrites": true}]'`)

	expectCol(t, eng, `.set-or-append RawClaims <| datatable(Id:string, Status:string)["c1", "computed"]`, "Result",
		"OK", "OK: update policy -> Claims")
	// The policy query sees only the new rows.
	diskExec(t, eng, `.set-or-append RawClaims <| datatable(Id:string, Status:string)["c2", "ruled"]`)
	expectWrRows(t, eng, `Claims | order by Id asc | project Id`, "c1", "c2")
	// A transactional failure writes nothing, source included.
	expectErrContains(t, eng, `.set-or-append RawClaims <| datatable(Id:string, Status:string)["c3", "supported"]`, "bad status")
	expectWrRows(t, eng, `RawClaims | count`, "2")
	// Direct writes to the target, and ingests that would skip the policy, are refused.
	expectErrContains(t, eng, `.set-or-append Claims <| datatable(Id:string, Status:string)["x", "ruled"]`, "only through its update policy")
	expectErrContains(t, eng, `.ingest inline into table RawClaims <| c4,ruled`, "source of Claims")
	// Non-transactional: the source write stands and the failure is reported.
	diskExec(t, eng, `.alter table Claims policy update @'[{"IsEnabled": true, "Source": "RawClaims", "Query": "ValidClaims()", "IsTransactional": false}]'`)
	expectCol(t, eng, `.set-or-append RawClaims <| datatable(Id:string, Status:string)["c5", "supported"]`, "Result",
		"OK", "FAILED")
	expectWrRows(t, eng, `RawClaims | count`, "3")
	expectWrRows(t, eng, `Claims | count`, "2")
	diskExec(t, eng, `.delete table Claims policy update`)
	expectWrRows(t, eng, `.show table Claims policy update | project Policy`, "null")
	expectErrContains(t, eng, `.alter table Claims policy update @'[{"Source": "Nope", "Query": "1"}]'`, "not found")
}

// ADX's .ingest into table T (uris) with (...).
func TestIngestInto(t *testing.T) {
	eng := diskEngineEmpty(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "a.csv")
	if err := os.WriteFile(p, []byte("Id,N\na,1\nb,2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectErrContains(t, eng, `.ingest into table T (h@"`+p+`") with (format="csv", ignoreFirstRecord=true)`, "does not exist")
	diskExec(t, eng, `.create table T (Id:string, N:long)`)
	diskExec(t, eng, `.ingest into table T (h@"`+p+`") with (format="csv", ignoreFirstRecord=true)`)
	expectWrRows(t, eng, `T | order by Id asc`, "a | 1", "b | 2")
}

// between on the columnar path (stored table, where | count) gives the
// row engine's answer, including a datetime .. timespan range.
func TestColumnarBetween(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.set-or-append T <| range i from 0 to 99 step 1 | extend At = datetime(2026-01-01) + i * 1h, N = i`)
	expectWrRows(t, eng, `T | where At between (datetime(2026-01-02) .. 1d) | count`, "25")
	expectWrRows(t, eng, `T | where N between (10 .. 19) | count`, "10")
	expectWrRows(t, eng, `T | where N !between (10 .. 19) | count`, "90")
	expectWrRows(t, eng, `T | where N between (10 .. 19) and At >= datetime(2026-01-01 15:00) | count`, "5")
}
