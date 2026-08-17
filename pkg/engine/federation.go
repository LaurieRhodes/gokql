package engine

// federation.go — filesystem-based, read-only cross-database queries
// via database('alias').TableName (see parser.DatabaseTableRef's own
// doc comment for why this real-ADX-conformant syntax was chosen
// rather than inventing new syntax).
//
// Deliberately filesystem-based, not network-based, per the design
// discussion this responds to: real cross-cluster/cross-database
// queries in ADX go over the network to a genuinely remote service,
// which means solving auth, TLS between instances, and serialization
// cost for every row before it does anything useful. Filesystem
// federation needs almost none of that — it reuses catalog.Discover
// and everything built on top of it this whole session, unchanged;
// "alias" resolves to a local (or locally-mounted, e.g. a shared
// container volume) directory, not a URL. A single container with one
// parent directory mounted, containing several sibling scope
// directories, gets this for free without any network reachability
// between processes at all.
//
// Deliberately read-only, for now: writing THROUGH an alias would
// need real coordination over who owns writes to that directory
// between two independent processes that don't know about each
// other — reintroducing exactly the kind of concurrent-writer problem
// the _Dictionaries lock exists to solve, except across process
// boundaries this time. Querying across an alias is safe and cheap;
// this file enforces the read-only boundary explicitly (see
// resolveFederatedTable) rather than leaving it as an unenforced
// convention someone could accidentally violate.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// federationConfigFileName is read from INSIDE the local scope
// directory — the same "config lives alongside the data it
// describes" convention .okql-server.json already established, so a
// session inspecting a scope directory finds every engine-internal
// setting file in one place, not scattered across different
// locations depending on which feature it configures.
const federationConfigFileName = ".okql-federation.json"

// federationAlias is one entry in the config file. Path is absolute,
// per this session's own design decision — a relative path would need
// deciding "relative to what" (the config file's own directory? the
// process's working directory? something else?) in a way that's
// genuinely ambiguous for a containerized deployment where the mount
// layout, not the scope directory's own location, is what's stable;
// absolute paths sidestep that ambiguity entirely at the cost of
// needing regeneration if a mount layout ever changes — an explicit,
// accepted tradeoff, not an oversight.
type federationAlias struct {
	Alias string `json:"alias"`
	Path  string `json:"path"`
}

type federationConfig struct {
	Aliases []federationAlias `json:"aliases"`
}

// loadFederationConfig reads the federation config from inside dbPath.
// Its absence is not an error — most scopes will never define any
// aliases at all, and querying database('anything') against a scope
// with no config file should fail with a clear "no such alias" error
// (see resolveFederatedTable), not a confusing file-not-found one.
func loadFederationConfig(dbPath string) (*federationConfig, error) {
	data, err := os.ReadFile(filepath.Join(dbPath, federationConfigFileName))
	if os.IsNotExist(err) {
		return &federationConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", federationConfigFileName, err)
	}
	var cfg federationConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", federationConfigFileName, err)
	}
	return &cfg, nil
}

// pushableFederationOps is the allowlist of operator types safe to
// execute on the REMOTE engine rather than pulling the full table
// across first — every one of them is row-local (operates only on the
// current row/table's own columns; can never reference a second table
// or any local-only construct like a stored function defined in the
// CALLING scope). Deliberately excludes anything that references a
// second table (JoinOp, LookupOp, UnionOp — the second table is
// almost certainly local, not on the remote side, so pushing these
// down would silently look up the wrong table or fail entirely) and
// anything not confidently row-local for a first version (MvApplyOp,
// the graph operators) rather than risk a subtly wrong push. Real
// source-position operators (DataTableOp, PrintOp, SearchOp) are
// listed nowhere here since they can never appear mid-pipeline in the
// first place.
func isPushableFederationOp(op parser.Operator) bool {
	switch op.(type) {
	case *parser.WhereOp, *parser.ProjectOp, *parser.ProjectAwayOp,
		*parser.ProjectRenameOp, *parser.ProjectReorderOp,
		*parser.SerializeOp, *parser.ParseOp, *parser.ExtendOp,
		*parser.TakeOp, *parser.SampleOp, *parser.CountOp,
		*parser.DistinctOp, *parser.OrderByOp, *parser.TopOp,
		*parser.SummarizeOp, *parser.GetSchemaOp, *parser.MvExpandOp:
		return true
	default:
		return false
	}
}

// splitPushableFederationOps returns the longest LEADING prefix of
// operators that are all pushable (per isPushableFederationOp above),
// and everything after the first non-pushable one. Stops at the FIRST
// non-pushable operator, not just skipping it, since operators after
// it may depend on state that the non-pushable one itself
// established (a JoinOp's own output columns, for instance) — the
// prefix must be a genuine, unbroken run from the start.
func splitPushableFederationOps(operators []parser.Operator) (pushable, remaining []parser.Operator) {
	for i, op := range operators {
		if !isPushableFederationOp(op) {
			return operators[:i], operators[i:]
		}
	}
	return operators, nil
}

// resolveFederatedTable executes ref against the local scope's
// federation config: resolves the alias to a path, opens a fresh,
// independent Engine for it (same "fresh per use" principle the HTTP
// server already established, for the same reason — reusing an
// already-proven-safe model rather than introducing new,
// cross-request state to reason about), and scans ref.TableName from
// it. Returns BOTH the result table and the operators that were NOT
// pushed down (the caller — executeQuery — still needs to apply those
// locally via applyPipeline; pipeline stages have no notion of which
// engine produced their input, so this only needs to intervene at the
// source and report what's left, not thread pushdown through every
// operator itself).
//
// Pushdown reuses the remote engine's OWN, already-built pipeline
// machinery unchanged — the pushable prefix is handed to a normal
// remoteEng.executeQuery call with those operators attached, which
// automatically gets that engine's own column-projection pushdown
// (RequiredColumns) and zone-map pruning (extractZoneFilter) for free,
// exactly as if the query had been run locally against that scope
// directly. No new remote-side logic was needed for this at all — the
// only new code is deciding WHICH operators are safe to hand across
// in the first place (splitPushableFederationOps above).
//
// Only a PREFIX of pushable operators is sent, not pushable operators
// wherever they occur — see splitPushableFederationOps's own doc
// comment for why stopping at the first non-pushable operator,
// rather than skipping it and continuing, is the only safe rule: an
// operator like JoinOp changes what's available to reference
// afterward, in ways a later where/project might depend on.
func (e *Engine) resolveFederatedTable(ref *parser.DatabaseTableRef, operators []parser.Operator) (*types.Table, []parser.Operator, error) {
	localPath := e.Catalog.DatabasePath()
	cfg, err := loadFederationConfig(localPath)
	if err != nil {
		return nil, nil, err
	}

	var remotePath string
	found := false
	for _, a := range cfg.Aliases {
		if a.Alias == ref.Alias {
			remotePath = a.Path
			found = true
			break
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("database(%q): no such alias in %s — check %s in %s",
			ref.Alias, federationConfigFileName, federationConfigFileName, localPath)
	}
	if !filepath.IsAbs(remotePath) {
		return nil, nil, fmt.Errorf("database(%q): federated path %q must be absolute (see federation.go's design note)", ref.Alias, remotePath)
	}

	// OpenAuto, not a hardcoded Discover: a federated target should
	// open correctly regardless of whether it's a discovery-mode scope
	// (every real scope this session has built) or a legacy
	// catalog-mode database — the same mode-detection every CLI
	// invocation already gets, shared via catalog.OpenAuto specifically
	// so this doesn't narrow what federation can point at compared to
	// what the CLI itself can open.
	remoteCat, err := catalog.OpenAuto(remotePath, false)
	if err != nil {
		return nil, nil, fmt.Errorf("database(%q): opening %s: %w", ref.Alias, remotePath, err)
	}
	remoteEng := New(remoteCat)

	remoteTableDef := remoteEng.Catalog.GetTable(ref.TableName)
	if remoteTableDef == nil {
		return nil, nil, fmt.Errorf("database(%q).%s: table not found in %s", ref.Alias, ref.TableName, remotePath)
	}

	pushable, remaining := splitPushableFederationOps(operators)
	if e.Verbose && len(pushable) > 0 {
		fmt.Fprintf(os.Stderr, "[federation] database(%q).%s: pushing %d operator(s) to remote scan, %d remain local\n",
			ref.Alias, ref.TableName, len(pushable), len(remaining))
	}

	result, err := remoteEng.executeQuery(&parser.Query{Source: ref.TableName, Operators: pushable})
	if err != nil {
		return nil, nil, fmt.Errorf("database(%q).%s: %w", ref.Alias, ref.TableName, err)
	}
	return result, remaining, nil
}
