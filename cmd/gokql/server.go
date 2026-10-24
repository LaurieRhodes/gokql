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
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/config"
	"github.com/LaurieRhodes/gokql/pkg/engine"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// serverConfig holds everything one running server instance needs —
// deliberately small and flat, matching this codebase's existing
// preference for plain, explicit config over layered abstraction.
type serverConfig struct {
	dbPath    string            // filesystem path to the PRIMARY scope this server serves
	dbName    string            // the name clients send in "db" for the primary database
	databases map[string]string // additional dbName -> path, all fully read-write (see serverDatabase's doc comment)
	verbose   bool
	readOnly  bool // -readonly or server.readonly: every write command is refused
	// readOnlyDBs marks additional databases served read-only
	// (server.databases[].readonly).
	readOnlyDBs map[string]bool
	addr        string
	tlsCert     string // empty means plain HTTP
	tlsKey      string
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
// increasing precedence: built-in defaults, the server section of the
// database's gokql.yaml (pkg/config; the older .okql-server.json is
// read when it is absent), then explicitly-set CLI flags. explicitFlags
// names which flags the user actually typed (via flag.Visit), so a flag
// left at its zero value doesn't override a real config-file setting.
func loadServerConfig(dbPath, addrFlag, dbNameFlag string, verbose bool, explicitFlags map[string]bool) (serverConfig, error) {
	cfg := serverConfig{
		dbPath:  dbPath,
		dbName:  filepath.Base(filepath.Clean(dbPath)),
		addr:    "127.0.0.1:8080", // this machine only: there is no authentication
		verbose: verbose,
	}
	f, err := config.Load(dbPath)
	if err != nil {
		return serverConfig{}, err
	}
	src := f.Path
	sv := f.Server
	if sv.Addr != "" {
		cfg.addr = sv.Addr
	}
	if sv.Name != "" {
		cfg.dbName = sv.Name
	}
	cfg.tlsCert, cfg.tlsKey = sv.TLS.Cert, sv.TLS.Key
	cfg.readOnly = sv.ReadOnly
	if len(sv.Databases) > 0 {
		cfg.databases = make(map[string]string, len(sv.Databases))
		cfg.readOnlyDBs = map[string]bool{}
		for _, db := range sv.Databases {
			if db.Name == cfg.dbName {
				return serverConfig{}, fmt.Errorf("%s: server.databases entry %q collides with the primary database's own name", src, db.Name)
			}
			cfg.databases[db.Name] = db.Path
			cfg.readOnlyDBs[db.Name] = db.ReadOnly
		}
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
	// Properties are the client request properties: a JSON object, or
	// (as Kusto clients send it) a string holding one. Only
	// Options.query_language and Options.query_graph_reference (GQL /
	// openCypher) are used.
	Properties json.RawMessage `json:"properties"`
}

// queryLanguage extracts Options.query_language and
// Options.query_graph_reference from the request properties.
func (r kustoRequest) queryLanguage() (lang, graphRef string) {
	raw := r.Properties
	var str string
	if json.Unmarshal(raw, &str) == nil {
		raw = json.RawMessage(str)
	}
	var props struct {
		Options map[string]interface{} `json:"Options"`
	}
	if json.Unmarshal(raw, &props) != nil {
		return "", ""
	}
	get := func(k string) string {
		for name, v := range props.Options {
			if strings.EqualFold(name, k) {
				if s, ok := v.(string); ok {
					return s
				}
			}
		}
		return ""
	}
	return get("query_language"), get("query_graph_reference")
}

// parameters extracts properties.Parameters: the values of the
// query's declare query_parameters(...), as KQL literal text.
func (r kustoRequest) parameters() map[string]string {
	raw := r.Properties
	var str string
	if json.Unmarshal(raw, &str) == nil {
		raw = json.RawMessage(str)
	}
	var props struct {
		Parameters map[string]interface{} `json:"Parameters"`
	}
	if json.Unmarshal(raw, &props) != nil || len(props.Parameters) == 0 {
		return nil
	}
	out := make(map[string]string, len(props.Parameters))
	for k, v := range props.Parameters {
		if s, ok := v.(string); ok {
			out[k] = s
		} else {
			b, _ := json.Marshal(v)
			out[k] = string(b)
		}
	}
	return out
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
	cat, err := openCatalog(dbPath, cfg.verbose)
	if err != nil {
		writeKustoError(w, http.StatusInternalServerError, fmt.Errorf("open database: %w", err))
		return
	}
	eng := engine.New(cat)
	eng.Verbose = cfg.verbose
	eng.ReadOnly = cfg.readOnly || cfg.readOnlyDBs[req.Database]

	query := req.Query
	if lang, ref := req.queryLanguage(); lang != "" {
		if query, err = parser.TranslateQueryLanguage(query, lang, ref); err != nil {
			writeKustoError(w, http.StatusBadRequest, fmt.Errorf("parse: %w", err))
			return
		}
	}
	stmt, err := parser.ParseWithParameters(query, req.parameters())
	if err != nil {
		writeKustoError(w, http.StatusBadRequest, fmt.Errorf("parse: %w", err))
		return
	}

	results, err := eng.ExecuteAll(stmt)
	if err != nil {
		writeKustoError(w, http.StatusInternalServerError, err)
		return
	}

	writeKustoResults(w, results)
}

// writeKustoResult serializes a *types.Table into the Tables[] shape
// real Kustainer returns. A nil result (a command with no tabular
// output, e.g. .create table) is reported as a single empty table —
// there is no real Kustainer example of this specific case verified
// against, so this is a reasonable, documented choice for this
// implementation, not a claim of exact real-Kustainer parity for
// every management command's response shape.
// writeKustoResults serializes one table per result, Table_0,
// Table_1, ... (fork and facet return several); no results is one
// empty table, as for a single nil result.
func writeKustoResults(w http.ResponseWriter, ts []*types.Table) {
	if len(ts) == 0 {
		ts = []*types.Table{nil}
	}
	resp := kustoResponse{}
	for i, t := range ts {
		resp.Tables = append(resp.Tables, kustoTable(fmt.Sprintf("Table_%d", i), t))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func kustoTable(name string, t *types.Table) kustoTableResult {
	table := kustoTableResult{
		TableName: name,
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
			// Cells are typed as the json modes type them (json_out.go):
			// datetime and timespan as their text, decimals as strings so
			// clients do not round them through float64, dynamic as
			// embedded JSON, non-finite reals as "NaN"/"Infinity". A raw
			// NaN would make the whole response fail to encode.
			cells := make([]interface{}, len(t.Schema.Columns))
			for i, col := range t.Schema.Columns {
				var v types.Value
				if i < len(row) {
					v = row[i]
				}
				var b bytes.Buffer
				appendJSONValue(&b, v, col.Type)
				cells[i] = json.RawMessage(b.Bytes())
			}
			table.Rows = append(table.Rows, cells)
		}
	}
	return table
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
