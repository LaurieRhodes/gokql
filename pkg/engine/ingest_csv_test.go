package engine

// ingest_csv_test.go — regression coverage for CSV column-count handling.
//
// Found live: a 4-column Edges CSV ingested against the 5-column Edges
// schema defeated header detection (the header line no longer
// column-matched) and the header text itself was silently written as a
// literal data row — discovered only because the row count came out one
// higher than expected. ingestCSVFile previously padded/truncated any
// row whose field count didn't match the schema instead of erroring,
// which made this possible for every row, not just the header.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// tryExec parses and executes stmt against eng, returning the error
// without failing the test — for cases where an error IS the expected
// outcome.
func tryExec(t *testing.T, eng *Engine, stmt string) error {
	t.Helper()
	parsed, err := parser.Parse(stmt)
	if err != nil {
		return err
	}
	_, err = eng.Execute(parsed)
	return err
}

func writeCSV(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestIngestCSVRejectsColumnCountMismatch reproduces the exact bug: a
// header with one fewer column than the schema must error, not silently
// ingest the header as a data row.
func TestIngestCSVRejectsColumnCountMismatch(t *testing.T) {
	dir := t.TempDir()
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table Edges (Src: string, Dst: string, Rel: string, Basis: string, Provenance: string)`)

	path := writeCSV(t, dir, "bad.csv", "Src,Dst,Rel,Basis\nA,B,rel1,f1\n")
	stmt := `.ingest csv into table Edges from "` + path + `"`
	err := tryExec(t, eng, stmt)
	if err == nil {
		t.Fatal("expected column-count mismatch to error, got success")
	} else if !strings.Contains(err.Error(), "column count") {
		t.Fatalf("expected a column-count error, got: %v", err)
	}

	got := diskQuery(t, eng, `Edges | count`)
	expectCell(t, got, 0, 0, "0")
}

// TestIngestCSVRejectsRaggedRow: header matches, but a later row doesn't.
// Must reject before writing anything — no partial ingest.
func TestIngestCSVRejectsRaggedRow(t *testing.T) {
	dir := t.TempDir()
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table Edges (Src: string, Dst: string, Rel: string, Basis: string, Provenance: string)`)

	path := writeCSV(t, dir, "ragged.csv",
		"Src,Dst,Rel,Basis,Provenance\nA,B,rel1,f1,doc.md\nC,D,rel2\n")
	stmt := `.ingest csv into table Edges from "` + path + `"`
	if err := tryExec(t, eng, stmt); err == nil {
		t.Fatal("expected ragged row to error, got success")
	}

	got := diskQuery(t, eng, `Edges | count`)
	expectCell(t, got, 0, 0, "0") // all-or-nothing: the good row must NOT have landed either
}

// TestIngestCSVLargeFileRaggedRowNoPartialWrite: a malformed row at the
// tail of a file spanning many rows must not leave earlier rows
// committed — asserts the up-front validation pass runs before ANY
// flush, closing the partial-write window a batch-interleaved check
// would leave open.
func TestIngestCSVLargeFileRaggedRowNoPartialWrite(t *testing.T) {
	dir := t.TempDir()
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table Edges (Src: string, Dst: string, Rel: string, Basis: string, Provenance: string)`)

	var b strings.Builder
	b.WriteString("Src,Dst,Rel,Basis,Provenance\n")
	for i := 0; i < 2000; i++ {
		b.WriteString("A,B,rel,f1,doc.md\n")
	}
	b.WriteString("BAD,ROW,ONLY,THREE\n") // fails at the very end
	path := writeCSV(t, dir, "big_ragged.csv", b.String())

	stmt := `.ingest csv into table Edges from "` + path + `"`
	if err := tryExec(t, eng, stmt); err == nil {
		t.Fatal("expected trailing ragged row to error, got success")
	}

	got := diskQuery(t, eng, `Edges | count`)
	expectCell(t, got, 0, 0, "0") // none of the 2000 good rows should have landed
}

// TestIngestCSVHappyPath: a correct file still ingests normally.
func TestIngestCSVHappyPath(t *testing.T) {
	dir := t.TempDir()
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table Edges (Src: string, Dst: string, Rel: string, Basis: string, Provenance: string)`)

	path := writeCSV(t, dir, "good.csv",
		"Src,Dst,Rel,Basis,Provenance\nA,B,rel1,f1,doc.md\nC,D,rel2,f2,doc.md\n")
	diskExec(t, eng, `.ingest csv into table Edges from "`+path+`"`)

	got := diskQuery(t, eng, `Edges | count`)
	expectCell(t, got, 0, 0, "2")
}

// TestIngestCSVNoHeaderStillValidated: a headerless file (first line is
// data, not column names) must still be column-count validated.
func TestIngestCSVNoHeaderStillValidated(t *testing.T) {
	dir := t.TempDir()
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table Edges (Src: string, Dst: string, Rel: string, Basis: string, Provenance: string)`)

	path := writeCSV(t, dir, "noheader_bad.csv", "A,B,rel1,f1\n") // 4 fields, needs 5
	stmt := `.ingest csv into table Edges from "` + path + `"`
	if err := tryExec(t, eng, stmt); err == nil {
		t.Fatal("expected headerless mismatched row to error, got success")
	}
}

// TestIngestCSVHeaderReordersColumns reproduces the exact live bug: a
// header whose column order differs from the table's schema (the
// realistic trigger being .create-merge producing a different column
// order than a CSV file declares) used to be detected correctly (the
// header line was skipped) but then completely ignored — the
// row-building loop matched fields[i] to Columns[i] by raw position
// regardless, silently misaligning every value. A full 866-row ingest
// was corrupted this way in real use. Now: an exactly-matching header
// (same names, different order) is used to remap each row correctly.
func TestIngestCSVHeaderReordersColumns(t *testing.T) {
	dir := t.TempDir()
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table T (Id: string, Note: string, LastTouchedAt: datetime, Seq: long)`)

	// Header order deliberately does NOT match the table's declared
	// column order above.
	path := writeCSV(t, dir, "reordered.csv",
		"Id,Seq,LastTouchedAt,Note\na,1,2026-08-01,first\nb,2,2026-08-02,second\n")
	diskExec(t, eng, `.ingest csv into table T from "`+path+`"`)

	got := diskQuery(t, eng, `T | sort by Id asc`)
	expectRows(t, got, 2)
	expectCell(t, got, 0, 0, "a")
	expectCell(t, got, 0, 1, "first") // Note — would be "1" (Seq) if still positional
	expectCell(t, got, 1, 3, "2")     // Seq — would be misaligned/zeroed if still positional
}

// TestIngestCSVAmbiguousHeaderRejected: a header that weakly LOOKS like
// a header (matches at least half the columns, the pre-existing
// detection threshold) but doesn't exactly match the table's full
// column set by name must error, not guess at a remapping. Silently
// guessing wrong here is exactly the mechanism behind the live
// corruption this whole fix responds to.
func TestIngestCSVAmbiguousHeaderRejected(t *testing.T) {
	dir := t.TempDir()
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table T (Id: string, Note: string, LastTouchedAt: datetime, Seq: long)`)

	// "Sequence" doesn't match "Seq" — 2/4 columns match (Id, Note),
	// exactly the pre-existing "at least half" header-detection
	// threshold, but not a full, unambiguous match.
	path := writeCSV(t, dir, "ambiguous.csv",
		"Id,Note,Sequence,LastTouchedAt\na,first,1,2026-08-01\n")
	if err := tryExec(t, eng, `.ingest csv into table T from "`+path+`"`); err == nil {
		t.Fatal("expected ambiguous partial-match header to error, got success")
	}
}

// TestIngestCSVBadValueRejectedNotZeroed reproduces the second half of
// the same live incident: a value that fails to parse for its column's
// declared type used to be silently replaced with nil (which then
// round-trips as that type's zero value elsewhere in this codebase —
// e.g. a datetime silently reading back as epoch) instead of erroring.
// Now the whole ingest fails upfront, and nothing partially lands.
func TestIngestCSVBadValueRejectedNotZeroed(t *testing.T) {
	dir := t.TempDir()
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table T (Id: string, Seq: long)`)

	path := writeCSV(t, dir, "badvalue.csv", "a,1\nb,not-a-number\nc,3\n")
	if err := tryExec(t, eng, `.ingest csv into table T from "`+path+`"`); err == nil {
		t.Fatal("expected unparseable value to error, got success")
	}

	got := diskQuery(t, eng, `T | count`)
	expectCell(t, got, 0, 0, "0") // all-or-nothing: none of the good rows should have landed either
}
