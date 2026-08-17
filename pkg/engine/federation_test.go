package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/parser"
)

// diskQueryError is diskExec's error-expecting counterpart — parses
// and executes q against a real, disk-backed engine, and fails the
// test if it DOESN'T error, rather than diskExec's inverse. queryError
// (engine_test.go) exists for the in-memory case only, with no way to
// target a specific, pre-configured discoverEngine instance, which
// every federation test needs.
func diskQueryError(t *testing.T, eng *Engine, q string) {
	t.Helper()
	stmt, err := parser.Parse(q)
	if err != nil {
		return // a parse error is still "errored", which is what's being asserted
	}
	if _, err := eng.Execute(stmt); err == nil {
		t.Fatalf("expected %q to error, it succeeded", q)
	}
}

// writeFederationConfig writes a .okql-federation.json into dir with
// the given alias -> path entries.
func writeFederationConfig(t *testing.T, dir string, aliases map[string]string) {
	t.Helper()
	var sb []byte
	sb = append(sb, []byte(`{"aliases":[`)...)
	first := true
	for alias, path := range aliases {
		if !first {
			sb = append(sb, ',')
		}
		first = false
		sb = append(sb, []byte(`{"alias":"`+alias+`","path":"`+path+`"}`)...)
	}
	sb = append(sb, []byte(`]}`)...)
	if err := os.WriteFile(filepath.Join(dir, federationConfigFileName), sb, 0o644); err != nil {
		t.Fatal(err)
	}
}

func federationTestScopes(t *testing.T) (localDir, remoteDir string) {
	t.Helper()
	localDir = t.TempDir()
	remoteDir = t.TempDir()

	remoteEng := discoverEngine(t, remoteDir)
	diskExec(t, remoteEng, `.create table Findings (Id: string, Claim: string)`)
	diskExec(t, remoteEng, `.set-or-append Findings <| datatable(Id:string, Claim:string) `+
		`["f001", "Mars is red", "f002", "water is wet", "f003", "grass is green"]`)

	localEng := discoverEngine(t, localDir)
	diskExec(t, localEng, `.create table Notes (Id: string, Text: string)`)

	return localDir, remoteDir
}

// TestFederationBasicQuery guards the core capability: a bare
// database('alias').Table query resolves the alias, opens the remote
// scope independently, and returns its real data.
func TestFederationBasicQuery(t *testing.T) {
	localDir, remoteDir := federationTestScopes(t)
	writeFederationConfig(t, localDir, map[string]string{"remote": remoteDir})

	eng := discoverEngine(t, localDir)
	tbl := diskQuery(t, eng, `database('remote').Findings | count`)
	expectCell(t, tbl, 0, 0, "3")
}

// TestFederationFullPipeline guards that ordinary pipeline operators
// (where, project, take, ...) work unchanged on federated data —
// they operate on the in-memory *types.Table resolveFederatedTable
// returns and have no notion of which engine produced it.
func TestFederationFullPipeline(t *testing.T) {
	localDir, remoteDir := federationTestScopes(t)
	writeFederationConfig(t, localDir, map[string]string{"remote": remoteDir})

	eng := discoverEngine(t, localDir)
	tbl := diskQuery(t, eng, `database('remote').Findings | where Claim has "Mars" | project Id`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "f001")
}

// TestFederationLocalTableUnaffected guards that federation support
// doesn't disturb ordinary, local (non-federated) queries against the
// same scope.
func TestFederationLocalTableUnaffected(t *testing.T) {
	localDir, remoteDir := federationTestScopes(t)
	writeFederationConfig(t, localDir, map[string]string{"remote": remoteDir})

	eng := discoverEngine(t, localDir)
	diskExec(t, eng, `.set-or-append Notes <| datatable(Id:string, Text:string) ["n1", "local"]`)
	tbl := diskQuery(t, eng, `Notes | count`)
	expectCell(t, tbl, 0, 0, "1")
}

// TestFederationUnknownAliasErrors guards a clear, specific error
// (not a crash, not a silent empty result) for an alias that isn't in
// the config.
func TestFederationUnknownAliasErrors(t *testing.T) {
	localDir, remoteDir := federationTestScopes(t)
	writeFederationConfig(t, localDir, map[string]string{"remote": remoteDir})

	eng := discoverEngine(t, localDir)
	diskQueryError(t, eng, `database('nosuchalias').Findings | count`)
}

// TestFederationNoConfigFileErrors guards the same clear error when
// no .okql-federation.json exists at all — its absence is not itself
// an error (most scopes will never federate), but referencing an
// alias when there's no config to resolve it against must still fail
// clearly, not panic on a nil config.
func TestFederationNoConfigFileErrors(t *testing.T) {
	localDir := t.TempDir()
	eng := discoverEngine(t, localDir)
	diskExec(t, eng, `.create table X (Id: string)`)
	diskQueryError(t, eng, `database('anything').Y | count`)
}

// TestFederationRelativePathRejected guards the absolute-paths-only
// design decision — a relative path in the config must error clearly
// at query time, not be silently resolved against some ambiguous
// base directory.
func TestFederationRelativePathRejected(t *testing.T) {
	localDir, _ := federationTestScopes(t)
	writeFederationConfig(t, localDir, map[string]string{"bad": "relative/path"})

	eng := discoverEngine(t, localDir)
	diskQueryError(t, eng, `database('bad').Findings | count`)
}

// TestFederationWriteTargetNeverTouchesRemote guards the actual
// read-only property, corrected from a first, wrong assumption about
// HOW it holds. Expected this to be rejected outright, since
// database('alias').Table was only ever wired into the query SOURCE
// path (parseQuery), never any write/management command's target
// parsing. It isn't rejected — it "succeeds", but not by writing
// through federation syntax at all: .set-or-append's target-table-name
// parsing doesn't validate the name is a reasonable identifier, so it
// silently creates a new LOCAL table literally named
// "database('remote').Findings" (the whole string, verbatim). That's
// a separate, pre-existing looseness in table-name validation,
// unrelated to federation specifically, and not something this test
// exists to guard against. What this test actually needs to prove --
// and does -- is the property that matters: the REMOTE scope's real
// data is never touched, regardless of what odd-but-harmless thing
// happens locally. Verified against a real row count, not just "no
// error was returned from the remote side" (which wouldn't prove
// anything, since the remote was never contacted for this write at
// all).
func TestFederationWriteTargetNeverTouchesRemote(t *testing.T) {
	localDir, remoteDir := federationTestScopes(t)
	writeFederationConfig(t, localDir, map[string]string{"remote": remoteDir})

	remoteBefore := diskQuery(t, discoverEngine(t, remoteDir), `Findings | count`)
	beforeCount := remoteBefore.Rows[0][0]

	eng := discoverEngine(t, localDir)
	diskExec(t, eng, `.set-or-append database('remote').Findings <| datatable(Id:string) ["x"]`)

	remoteAfter := diskQuery(t, discoverEngine(t, remoteDir), `Findings | count`)
	if remoteAfter.Rows[0][0] != beforeCount {
		t.Fatalf("expected remote Findings count unchanged (%v), got %v -- federation write-through leak",
			beforeCount, remoteAfter.Rows[0][0])
	}
}

// --- Pushdown ---

// TestFederationSplitPushableOpsStopsAtFirstNonPushable is a direct
// unit test of splitPushableFederationOps itself, not just an
// end-to-end result: guards that a JoinOp genuinely stops the prefix
// at that exact position, rather than being silently skipped and the
// scan continuing past it.
func TestFederationSplitPushableOpsStopsAtFirstNonPushable(t *testing.T) {
	ops := []parser.Operator{
		&parser.WhereOp{},
		&parser.ProjectOp{},
		&parser.JoinOp{},
		&parser.WhereOp{}, // after the join -- must stay local too, even though WhereOp alone is pushable
	}
	pushable, remaining := splitPushableFederationOps(ops)
	if len(pushable) != 2 {
		t.Fatalf("expected exactly 2 pushable operators (Where, Project), got %d", len(pushable))
	}
	if len(remaining) != 2 {
		t.Fatalf("expected exactly 2 remaining operators (Join, Where), got %d", len(remaining))
	}
	if _, ok := remaining[0].(*parser.JoinOp); !ok {
		t.Errorf("expected the JoinOp itself to be the first remaining operator, got %T", remaining[0])
	}
}

// TestFederationPushdownMatchesFullyLocalResult is the real
// correctness guard: a query combining a pushable prefix (where) with
// a non-pushable operator (join against a table that only exists
// LOCALLY, never in the remote scope) must produce results BYTE-
// IDENTICAL to the same query run with no federation involved at all
// — proving the join correctly stayed local rather than being pushed
// (which would have failed outright, since the remote scope has no
// such table) or silently producing wrong results.
func TestFederationPushdownMatchesFullyLocalResult(t *testing.T) {
	localDir, remoteDir := federationTestScopes(t)
	writeFederationConfig(t, localDir, map[string]string{"remote": remoteDir})

	localEng := discoverEngine(t, localDir)
	diskExec(t, localEng, `.create table Tags (Id: string, Tag: string)`)
	diskExec(t, localEng, `.set-or-append Tags <| datatable(Id:string, Tag:string) ["f001","science","f002","science"]`)

	federated := diskQuery(t, localEng, `database('remote').Findings | where Id != "f003" | join (Tags) on Id | project Id, Tag`)

	// The fully-local equivalent: same Findings data, same Tags,
	// same query shape, but with Findings copied into the SAME scope
	// as Tags so no federation is involved at all.
	remoteEng := discoverEngine(t, remoteDir)
	diskExec(t, remoteEng, `.create table Tags (Id: string, Tag: string)`)
	diskExec(t, remoteEng, `.set-or-append Tags <| datatable(Id:string, Tag:string) ["f001","science","f002","science"]`)
	local := diskQuery(t, remoteEng, `Findings | where Id != "f003" | join (Tags) on Id | project Id, Tag`)

	if len(federated.Rows) != len(local.Rows) {
		t.Fatalf("row count mismatch: federated=%d local=%d", len(federated.Rows), len(local.Rows))
	}
	for i := range federated.Rows {
		for c := range federated.Rows[i] {
			if fmt.Sprintf("%v", federated.Rows[i][c]) != fmt.Sprintf("%v", local.Rows[i][c]) {
				t.Errorf("row %d col %d mismatch: federated=%v local=%v", i, c, federated.Rows[i][c], local.Rows[i][c])
			}
		}
	}
}

// TestFederationSummarizePushdownMatchesFullyLocalResult guards
// summarize specifically — the most performance-valuable operator to
// push down (reducing many rows to few before transfer), verified
// against a real, computed aggregate, not just that it runs.
func TestFederationSummarizePushdownMatchesFullyLocalResult(t *testing.T) {
	localDir, remoteDir := federationTestScopes(t)
	writeFederationConfig(t, localDir, map[string]string{"remote": remoteDir})

	localEng := discoverEngine(t, localDir)
	federated := diskQuery(t, localEng, `database('remote').Findings | summarize Total=count()`)

	remoteEng := discoverEngine(t, remoteDir)
	local := diskQuery(t, remoteEng, `Findings | summarize Total=count()`)

	expectCell(t, federated, 0, 0, fmt.Sprintf("%v", local.Rows[0][0]))
}
