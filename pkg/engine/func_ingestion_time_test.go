package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// func_ingestion_time_test.go — ingestion_time(), added 2026-08-18.
// Real ADX's own docs (ingestion-time-function.md, ingestion-time-
// policy.md) describe ingestion_time() as a function-form accessor
// for a HIDDEN $IngestionTime column, returning null "if there's no
// datetime column in the table, or the IngestionTime policy wasn't
// enabled." This engine's own "_TimeReceived" is a real, ordinary
// schema column internally (not a specially-typed pseudo-column), but
// IS hidden from query OUTPUT unless explicitly referenced by name
// somewhere in the operator chain (hideTimeReceivedUnlessExplicit,
// timereceived.go — found live while writing this test: an earlier
// pass of that file's own header comment claimed "NO special hidden
// ... mechanism was built," which was accurate when written but is
// now stale relative to that later addition; not fixed here, out of
// scope for this function, but worth flagging). So ingestion_time()
// here is a thin accessor that reads the real underlying column value
// during expression evaluation, BEFORE that later output-hiding step
// runs — matching real ADX's own null-if-absent contract for the
// VALUE, while this codebase's own separate display convention still
// governs whether the raw "_TimeReceived" column itself also appears
// in the final result.

// TestIngestionTimeReturnsColumnValue guards the positive case: when
// a table has a real "_TimeReceived" column, ingestion_time() returns
// its value for the current row. Real ADX's own documented display
// rule (verified as already correctly implemented in this codebase's
// own hideTimeReceivedUnlessExplicit, timereceived.go, before writing
// this test — not assumed) hides _TimeReceived from the final result
// entirely unless it's referenced by name somewhere in the operator
// chain, so the "ing" column ends up at index 1, not 2 — the raw
// "_TimeReceived" column itself is correctly absent from the output
// here, exactly as intended.
func TestIngestionTimeReturnsColumnValue(t *testing.T) {
	result := queryResult(t, `datatable(Id: string, _TimeReceived: datetime)
		["a", datetime(2024-01-01T00:00:00Z)]
		| extend ing=ingestion_time()`)
	got := types.FormatValue(result.Rows[0][1], types.TypeDatetime)
	want := "2024-01-01T00:00:00Z"
	if got != want {
		t.Errorf("ing = %v, want %v", got, want)
	}
}

// TestIngestionTimeNullWhenColumnAbsent guards real ADX's own
// documented "no datetime column in the table" case: a table with no
// _TimeReceived column at all returns null, not an error -- verified
// live against a real production scope table (dumuzi_scope's own
// Findings table predates this engine's _TimeReceived feature and
// genuinely has no such column) before writing this as a synthetic
// test, so this is a confirmed real case, not a hypothetical one.
func TestIngestionTimeNullWhenColumnAbsent(t *testing.T) {
	result := queryResult(t, `datatable(Id: string) ["a"] | extend ing=ingestion_time()`)
	if result.Rows[0][1] != nil {
		t.Errorf("ing = %v, want null", result.Rows[0][1])
	}
}

// TestIngestionTimeNullWhenRowValueIsNull guards real ADX's own
// documented "IngestionTime policy wasn't enabled when a record was
// ingested" case: the column exists in the schema but this specific
// row's own value is null (e.g. a pre-feature row not yet rewritten,
// per timereceived.go's own documented deferred-backfill design).
func TestIngestionTimeNullWhenRowValueIsNull(t *testing.T) {
	result := queryResult(t, `datatable(Id: string, _TimeReceived: datetime)
		["a", datetime(null)]
		| extend ing=ingestion_time()`)
	if result.Rows[0][1] != nil {
		t.Errorf("ing = %v, want null", result.Rows[0][1])
	}
}

// TestIngestionTimeRejectsArguments guards that ingestion_time()
// (real ADX's own documented zero-argument function) errors on any
// argument rather than silently ignoring it.
func TestIngestionTimeRejectsArguments(t *testing.T) {
	queryError(t, `print r = ingestion_time(1)`)
}
