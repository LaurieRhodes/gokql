package main

// server.go implements gokql's network mode: a minimal HTTP server
// speaking the same two-endpoint protocol real Microsoft Kustainer
// exposes (POST /v1/rest/query, POST /v1/rest/mgmt — both accept
// {"db": "...", "csl": "..."} and return {"Tables": [...]}), verified
// directly against a real client's code
// (kustainer-ui's internal/kusto/client.go) rather than assumed from
// general Kusto REST API familiarity.
//
// Single-database, read-only-friendly first version, deliberately:
// - No auth. Real Kustainer has none either — verified by its
//   absence in kustainer-ui's client (no Authorization header set
//   anywhere) — matching its own threat model (trusted local
//   container, not a network-exposed service). A network-reachable
//   gokql inherits that same assumption; if this ever needs to be
//   reachable beyond a trusted boundary, that's a deliberate,
//   separate decision to make explicitly, not something to bolt on
//   here by default.
// - Fresh catalog.Discover + engine.New per request, not one shared,
//   long-lived Engine serving every request. This is a genuine
//   performance cost (re-globbing the scope directory's extent
//   footers on every single request) traded deliberately for safety:
//   it matches exactly how every existing gokql CLI invocation already
//   works (a fresh process, fresh discovery, every time) — a model
//   already proven safe under concurrent multi-writer access this
//   entire session. A shared Engine across concurrent HTTP requests
//   would need its own concurrency audit (does anything on Engine
//   mutate shared state outside dictCacheMu's already-covered path
//   during query execution?) that hasn't been done — reusing the
//   already-proven-safe model avoids introducing that open question
//   at all for this first version, at the cost of not caching
//   anything (dictCache, in particular) across requests.
// - The "db" field in every request is checked against a single
//   configured name, not used to route between multiple databases —
//   multi-database serving is deliberately out of scope for this
//   version (see the design discussion this responds to). A request
//   naming a different database gets a clear error, not silently
//   ignored or misrouted.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/LaurieRhodes/gokql/pkg/engine"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// serverConfigFileName is the config file runServer looks for INSIDE
// the served scope directory (-db's target), read once at startup.
// Dot-prefixed and distinctly named, matching this codebase's existing
// convention for engine-internal files that live alongside scope data
// without being mistaken for it (.dropped/, _dictionaries.lock) — a
// session inspecting a scope directory should be able to tell at a
// glance which files are its own data and which aren't.
const serverConfigFileName = ".okql-server.json"

// serverFileConfig is the on-disk shape of serverConfigFileName —
// every field optional, since the point of this file is to let a
// containerized deployment fully configure a server (port, TLS,
// eventually retention/compaction settings) by dropping one file
// alongside the database, without needing to know or construct the
// exact CLI invocation in advance. Precedence: an explicitly-set CLI
// flag always wins over this file (flag.Parse's own defaults can't be
// distinguished from "user typed the default value" with the
// standard flag package, so "explicitly set" here means the flag's
// package-level Visit call found it on the command line — see
// loadServerConfig) — this file exists specifically for the case
// where NO flags beyond -serve -db are given at all, matching the
// "one command, config lives with the data" deployment shape this was
// built for.
type serverFileConfig struct {
	Addr      string           `json:"addr,omitempty"`      // e.g. ":8443" or "0.0.0.0:8080"
	DBName    string           `json:"dbName,omitempty"`    // overrides the directory-basename default
	TLSCert   string           `json:"tlsCert,omitempty"`   // path to a PEM certificate file
	TLSKey    string           `json:"tlsKey,omitempty"`    // path to a PEM private key file
	Databases []serverDatabase `json:"databases,omitempty"` // additional, fully read-write databases this server also serves — see serverDatabase's own doc comment for why this is deliberately NOT the same mechanism as federation's read-only aliases
}

// serverDatabase names one ADDITIONAL database this server serves
// beyond its primary -db, with full read-write access — this is
// deliberately a SEPARATE mechanism from federation's
// .okql-federation.json aliases, not a reuse of it, even though both
// resolve a name to a local path. Federation exists for querying
// scopes potentially owned and actively written by a DIFFERENT
// process, where read-only is the safety boundary that avoids
// needing any cross-process write coordination at all. Multi-database
// serving is the opposite situation: this server process IS the
// owner of every database listed here, in exactly the same sense it
// owns its primary -db, so there's no cross-process coordination
// problem to avoid — restricting these to read-only would just be an
// arbitrary, unhelpful limitation with no safety benefit behind it.
// Conflating the two mechanisms would either make federation
// accidentally writable (a real regression) or make multi-database
// serving needlessly read-only (a real, avoidable limitation) — kept
// separate specifically so getting one right doesn't silently weaken
// the other.
type serverDatabase struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// serverConfig holds everything one running server instance needs —
// deliberately small and flat, matching this codebase's existing
// preference for plain, explicit config over layered abstraction.
type serverConfig struct {
	dbPath        string // filesystem path to the PRIMARY scope this server serves
	dbName        string // the name clients send in "db" for the primary database
	databases     map[string]string // additional dbName -> path, all fully read-write (see serverDatabase's doc comment)
	forceDiscover bool
	verbose       bool
	addr          string
	tlsCert       string // empty means plain HTTP
	tlsKey        string
}

// resolveDatabase maps a request's "db" field to the filesystem path
// it should be served from — the primary database, one of the
// additional serverDatabase entries, or an error naming what WAS
// available so a config mistake is easy to spot rather than a bare
// "not found".
func (cfg serverConfig) resolveDatabase(name string) (string, error) {
	if name == "" || name == cfg.dbName {
		return cfg.dbPath, nil
	}
	if path, ok := cfg.databases[name]; ok {
		return path, nil
	}
	known := []string{cfg.dbName}
	for n := range cfg.databases {
		known = append(known, n)
	}
	return "", fmt.Errorf("unknown database %q — this server serves: %v", name, known)
}

// loadServerConfig resolves the final serverConfig by layering, in
// increasing precedence: built-in defaults, the on-disk
// .okql-server.json inside dbPath (if present — its absence is not an
// error, since most of this file's fields are genuinely optional),
// then explicitly-set CLI flags. explicitFlags names which flags the
// user actually typed (via flag.Visit), so a flag left at its zero
// value doesn't silently override a real config-file setting with
// nothing.
func loadServerConfig(dbPath, addrFlag, dbNameFlag string, forceDiscover, verbose bool, explicitFlags map[string]bool) (serverConfig, error) {
	cfg := serverConfig{
		dbPath:        dbPath,
		dbName:        filepath.Base(filepath.Clean(dbPath)),
		addr:          ":8080",
		forceDiscover: forceDiscover,
		verbose:       verbose,
	}

	fileCfgPath := filepath.Join(dbPath, serverConfigFileName)
	data, err := os.ReadFile(fileCfgPath)
	switch {
	case err == nil:
		var fc serverFileConfig
		if jsonErr := json.Unmarshal(data, &fc); jsonErr != nil {
			return serverConfig{}, fmt.Errorf("parse %s: %w", fileCfgPath, jsonErr)
		}
		if fc.Addr != "" {
			cfg.addr = fc.Addr
		}
		if fc.DBName != "" {
			cfg.dbName = fc.DBName
		}
		cfg.tlsCert = fc.TLSCert
		cfg.tlsKey = fc.TLSKey
		if len(fc.Databases) > 0 {
			cfg.databases = make(map[string]string, len(fc.Databases))
			for _, db := range fc.Databases {
				if db.Name == "" || db.Path == "" {
					return serverConfig{}, fmt.Errorf("%s: databases entry needs both name and path (got name=%q path=%q)",
						fileCfgPath, db.Name, db.Path)
				}
				if db.Name == cfg.dbName {
					return serverConfig{}, fmt.Errorf("%s: databases entry %q collides with the primary database's own name", fileCfgPath, db.Name)
				}
				if !filepath.IsAbs(db.Path) {
					return serverConfig{}, fmt.Errorf("%s: databases entry %q path %q must be absolute (same requirement as federation aliases)",
						fileCfgPath, db.Name, db.Path)
				}
				cfg.databases[db.Name] = db.Path
			}
		}
	case os.IsNotExist(err):
		// No config file — fine, every field it could set is optional.
	default:
		return serverConfig{}, fmt.Errorf("read %s: %w", fileCfgPath, err)
	}

	// Explicit CLI flags win over whatever the file said.
	if explicitFlags["addr"] {
		cfg.addr = addrFlag
	}
	if explicitFlags["dbname"] {
		cfg.dbName = dbNameFlag
	}

	return cfg, nil
}

// kustoRequest is the request body shape both /v1/rest/query and
// /v1/rest/mgmt accept — identical for both, verified against
// kustainer-ui's client.go (models.QueryRequest / the inline map both
// endpoints there use), not assumed.
type kustoRequest struct {
	Database string `json:"db"`
	Query    string `json:"csl"`
}

// kustoColumn mirrors kustainer-ui's models.Column exactly — verified
// field names and JSON tags against that real client, not guessed.
type kustoColumn struct {
	Name     string `json:"ColumnName"`
	Type     string `json:"ColumnType"`
	DataType string `json:"DataType"`
}

// kustoTableResult mirrors kustainer-ui's models.TableResult (the
// plain, always-array-rows shape — this server never emits the
// exception-object-as-a-row variant that struct's custom
// UnmarshalJSON exists to tolerate, since that's real Kustainer's own
// size-limit behavior, not something this implementation needs to
// replicate for a first version).
type kustoTableResult struct {
	TableName string          `json:"TableName"`
	Columns   []kustoColumn   `json:"Columns"`
	Rows      [][]interface{} `json:"Rows"`
}

type kustoResponse struct {
	Tables []kustoTableResult `json:"Tables"`
}

// runServer starts the HTTP(S) server and blocks until it exits
// (which, absent a signal-driven shutdown — not built for this first
// version — means until the process is killed).
func runServer(cfg serverConfig) error {
	mux := http.NewServeMux()
	handler := func(w http.ResponseWriter, r *http.Request) {
		handleKustoRequest(cfg, w, r)
	}
	mux.HandleFunc("/v1/rest/query", handler)
	mux.HandleFunc("/v1/rest/mgmt", handler)

	tls := cfg.tlsCert != "" || cfg.tlsKey != ""
	if tls && (cfg.tlsCert == "" || cfg.tlsKey == "") {
		return fmt.Errorf("TLS requires both tlsCert and tlsKey — only one was set")
	}

	scheme := "http"
	if tls {
		scheme = "https"
	}
	log.Printf("gokql network mode: serving database %q from %s on %s://%s", cfg.dbName, cfg.dbPath, scheme, cfg.addr)
	for name, path := range cfg.databases {
		log.Printf("  + also serving database %q from %s (read-write)", name, path)
	}
	log.Printf("no authentication — matches real Kustainer's own threat model; do not expose this beyond a trusted network")

	if tls {
		return http.ListenAndServeTLS(cfg.addr, cfg.tlsCert, cfg.tlsKey, mux)
	}
	return http.ListenAndServe(cfg.addr, mux)
}

// handleKustoRequest serves BOTH /v1/rest/query and /v1/rest/mgmt
// identically — deliberately. gokql's own parser already routes a KQL
// query and a "."-prefixed management command through the exact same
// parse-then-Execute path (see runCommand), so there is no real
// distinction here to preserve; real Kustainer's own split into two
// endpoints reflects ITS internal architecture, not something a
// client-compatible server is obligated to replicate as two different
// code paths.
func handleKustoRequest(cfg serverConfig, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "expected POST", http.StatusMethodNotAllowed)
		return
	}

	var req kustoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeKustoError(w, http.StatusBadRequest, fmt.Errorf("decode request: %w", err))
		return
	}

	dbPath, err := cfg.resolveDatabase(req.Database)
	if err != nil {
		writeKustoError(w, http.StatusBadRequest, err)
		return
	}

	// Fresh catalog + engine per request — see the file-level doc
	// comment for why this trades performance for reusing an
	// already-proven-safe model rather than a new, unaudited one. This
	// applies identically to every additional database this server
	// serves via cfg.databases, not just the primary one — same
	// concurrency reasoning, same tradeoff, no special case needed.
	cat, err := openCatalog(dbPath, cfg.forceDiscover, cfg.verbose)
	if err != nil {
		writeKustoError(w, http.StatusInternalServerError, fmt.Errorf("open database: %w", err))
		return
	}
	eng := engine.New(cat)
	eng.Verbose = cfg.verbose

	stmt, err := parser.Parse(req.Query)
	if err != nil {
		writeKustoError(w, http.StatusBadRequest, fmt.Errorf("parse: %w", err))
		return
	}

	result, err := eng.Execute(stmt)
	if err != nil {
		writeKustoError(w, http.StatusInternalServerError, err)
		return
	}

	writeKustoResult(w, result)
}

// writeKustoResult serializes a *types.Table into the Tables[] shape
// real Kustainer returns. A nil result (a command with no tabular
// output, e.g. .create table) is reported as a single empty table —
// there is no real Kustainer example of this specific case verified
// against, so this is a reasonable, documented choice for this
// implementation, not a claim of exact real-Kustainer parity for
// every management command's response shape.
func writeKustoResult(w http.ResponseWriter, t *types.Table) {
	table := kustoTableResult{
		TableName: "Table_0",
		Columns:   make([]kustoColumn, 0),
		Rows:      make([][]interface{}, 0),
	}
	if t != nil {
		for _, col := range t.Schema.Columns {
			table.Columns = append(table.Columns, kustoColumn{
				Name:     col.Name,
				Type:     col.Type.String(),
				DataType: col.Type.DotNetTypeName(),
			})
		}
		for _, row := range t.Rows {
			table.Rows = append(table.Rows, append([]interface{}(nil), row...))
		}
	}

	resp := kustoResponse{Tables: []kustoTableResult{table}}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// writeKustoError reports a failure. Real Kustainer's own error
// response shape (an "Exceptions" array embedded in a row, per
// kustainer-ui's TableResult.UnmarshalJSON comment) was verified as
// existing but not verified in full detail — this implementation uses
// a plain HTTP error status plus a JSON body instead of replicating
// that specific embedded-row convention, since matching it exactly
// wasn't confirmed against a real example and guessing at an error
// contract risks being wrong in a way that's harder to notice than a
// wrong success response.
func writeKustoError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
