// Command gokql is a KQL query processor for Vortex columnar databases.
//
// Usage:
//
//	gokql                             Interactive REPL, in-memory
//	gokql mydb.okql                   Interactive REPL, persistent database
//	gokql mydb.okql "Logs | take 5"   One-shot query
//	gokql "print 6 * 7"               One-shot query, in-memory
//	echo "query" | gokql mydb.okql    Pipe mode
//	gokql -e "query" -db mydb.okql    Legacy flag mode
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/engine"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

const version = "0.3.0"

// buildInfo reports enough to answer "is this binary actually current"
// without needing to trust a claim from a different session/process —
// exactly the ambiguity that caused a real, live cross-session
// confusion once already (one session rebuilt okql; a different
// session, invoking a different binary on the same shared data
// directory, saw no change and reasonably concluded the rebuild never
// happened). -version previously printed a hand-maintained constant
// that never changed between builds, so it couldn't answer that
// question at all.
//
// Uses Go's automatic VCS build-info stamping (available since Go
// 1.18, via `go build`/`go install` — not `go run` — whenever the
// build happens inside a git repository), not a hand-passed -ldflags
// value. Deliberately: this means the existing, documented build
// command (`go install ./cmd/gokql`) is sufficient on its own — nobody
// needs to remember a longer command with extra flags for this
// information to be embedded correctly.
func buildInfo() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "build info unavailable (built without module/VCS info — go run, or GOFLAGS=-buildvcs=false)"
	}
	var revision, dirty, buildTime string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = " (dirty — uncommitted changes present at build time)"
			}
		case "vcs.time":
			buildTime = s.Value
		}
	}
	if revision == "" {
		return "build info present but no VCS revision recorded"
	}
	short := revision
	if len(short) > 12 {
		short = short[:12]
	}
	return fmt.Sprintf("commit %s%s, committed %s", short, dirty, buildTime)
}

// repl state
type replState struct {
	eng       *engine.Engine
	timer     bool
	mode      string // table, csv, json, jsonl, tsv
	output    io.Writer
	outputFile *os.File // non-nil if redirected to file
}

func main() {
	// --- Flag parsing (backward-compatible) ---
	dbFlag := flag.String("db", "", "Path to database directory (legacy)")
	execCmd := flag.String("e", "", "Execute a single command and exit")
	verbose := flag.Bool("v", false, "Verbose: show scan diagnostics")
	readOnly := flag.Bool("readonly", false, "Open database read-only")
	discoverFlag := flag.Bool("discover", false, "Force catalog-free discovery mode (scan .vtx footers; ignore/skip catalog.json)")
	modeFlag := flag.String("mode", "table", "Output mode: table, csv, json, jsonl, tsv")
	noRC := flag.Bool("no-rc", false, "Don't load ~/.okqlrc")
	showVersion := flag.Bool("version", false, "Print version and exit")
	serveFlag := flag.Bool("serve", false, "Network mode: serve -db over HTTP, speaking Kustainer's REST protocol (POST /v1/rest/query, /v1/rest/mgmt). Single database, no auth — see server.go's file-level doc comment before exposing beyond a trusted network.")
	serveAddr := flag.String("addr", ":8080", "Address to listen on in -serve mode")
	dbNameFlag := flag.String("dbname", "", "Database name clients must send in -serve mode (defaults to -db's final path component)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("gokql v%s\n", version)
		fmt.Printf("build: %s\n", buildInfo())
		return
	}

	// --- Resolve database path and query from positional args ---
	args := flag.Args()
	var dbPath, query string

	if *dbFlag != "" {
		dbPath = *dbFlag
	}
	if *execCmd != "" {
		query = *execCmd
	}

	// Positional argument resolution
	switch len(args) {
	case 1:
		if looksLikePath(args[0]) {
			dbPath = args[0]
		} else {
			query = args[0]
		}
	case 2:
		dbPath = args[0]
		query = args[1]
	}

	// --- Network mode: serve -db over HTTP(S) and never reach the CLI
	// paths below at all. Checked here, right after dbPath/query are
	// resolved but before the single, long-lived catalog this CLI
	// normally opens once — server mode deliberately does NOT open one
	// here, since it reopens a fresh catalog per request instead (see
	// server.go's file-level doc comment for why).
	if *serveFlag {
		if dbPath == "" {
			fmt.Fprintln(os.Stderr, "Error: -serve requires -db <path>")
			os.Exit(1)
		}
		// Which flags the user actually typed, not just which ended up
		// non-zero — loadServerConfig needs this to know whether a CLI
		// flag should override a real .okql-server.json setting, or
		// whether it's just sitting at its own unrelated default.
		explicit := make(map[string]bool)
		flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

		cfg, err := loadServerConfig(dbPath, *serveAddr, *dbNameFlag, *discoverFlag, *verbose, explicit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if err := runServer(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// --- Open catalog ---
	var cat *catalog.Catalog
	var err error

	if dbPath != "" {
		if *readOnly {
			// Check exists first
			if _, err := os.Stat(dbPath); os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "Error: database %q does not exist (readonly mode)\n", dbPath)
				os.Exit(1)
			}
		}
		cat, err = openCatalog(dbPath, *discoverFlag, *verbose)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
			os.Exit(1)
		}
	} else {
		cat = catalog.NewMemory()
	}

	eng := engine.New(cat)
	eng.Verbose = *verbose

	// Wait for any materialized-view maintenance this process's own
	// writes triggered before actually exiting via a normal return —
	// a goroutine spawned by triggerMaterializedViewMaintenance and
	// never waited on is simply killed mid-work when a short-lived CLI
	// process exits (see mv_maintenance.go's own doc comment). Covers
	// every NORMAL return path below (one-shot success, pipe-mode EOF,
	// REPL EOF) — does NOT cover os.Exit calls, which skip deferred
	// functions entirely; those are handled with an explicit call at
	// each such call site instead (see dispatch's .quit/.exit case).
	// A no-op if dbPath is empty (in-memory session — nothing to wait
	// on) or nothing was ever in flight.
	if dbPath != "" {
		defer engine.WaitForAllMaterializedViewMaintenance(dbPath)
	}

	st := &replState{
		eng:    eng,
		mode:   *modeFlag,
		output: os.Stdout,
	}

	// --- One-shot mode ---
	if query != "" {
		if err := st.runCommand(query); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// --- Pipe mode ---
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		scanner := bufio.NewScanner(os.Stdin)
		// Increase buffer for long queries
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "//") {
				continue
			}
			if err := st.dispatch(line); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
		}
		return
	}

	// --- Interactive REPL ---
	fmt.Printf("gokql v%s\n", version)
	if cat.IsMemory() {
		fmt.Println("Connected to a transient in-memory database.")
		fmt.Println("Use .open PATH to connect to a persistent database.")
	} else {
		tables := cat.ListTables()
		totalExtents := 0
		for _, t := range tables {
			if tbl := cat.GetTable(t); tbl != nil {
				totalExtents += len(tbl.Extents)
			}
		}
		fmt.Printf("Database: %s (%d tables, %d extents)\n", dbPath, len(tables), totalExtents)
	}
	fmt.Println("Type .help for commands, .quit to exit")
	fmt.Println()

	// Load RC file
	if !*noRC {
		st.loadRC()
	}

	reader := bufio.NewReader(os.Stdin)
	var multiLine strings.Builder
	inMultiLine := false

	for {
		if inMultiLine {
			fmt.Print("   ... ")
		} else {
			fmt.Print("gokql> ")
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				fmt.Println()
			}
			st.closeOutput()
			return
		}

		line = strings.TrimRight(line, "\r\n")

		// Multi-line input for .ingest inline
		if inMultiLine {
			if line == "" {
				cmd := multiLine.String()
				multiLine.Reset()
				inMultiLine = false
				if err := st.runCommand(cmd); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				}
				continue
			}
			multiLine.WriteString("\n")
			multiLine.WriteString(line)
			continue
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Check for multi-line ingest (must be checked before dispatch)
		lower := strings.ToLower(trimmed)

		// Graceful REPL exit
		if lower == ".quit" || lower == ".exit" || lower == "exit" || lower == "quit" {
			fmt.Println("Bye.")
			st.closeOutput()
			return
		}

		if strings.Contains(trimmed, "<|") &&
			strings.HasPrefix(lower, ".ingest inline") {
			multiLine.WriteString(trimmed)
			inMultiLine = true
			continue
		}

		if err := st.dispatch(trimmed); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
	}
}

// --- Command dispatch ---

// dispatch routes dot commands and KQL queries. Works in both REPL and pipe mode.
func (st *replState) dispatch(input string) error {
	lower := strings.ToLower(input)
	switch {
	case lower == ".quit" || lower == ".exit" || lower == "exit" || lower == "quit":
		// os.Exit skips deferred functions entirely — main()'s own
		// defer (which covers every NORMAL return path) never runs
		// here, so this exact same wait needs its own explicit call
		// at this specific exit point too. dbPath's own emptiness
		// check isn't available here (a REPL-mode-only path, an
		// in-memory session has no path to wait on and
		// WaitForAllMaterializedViewMaintenance("") is simply a no-op
		// against an empty prefix anyway).
		engine.WaitForAllMaterializedViewMaintenance(st.eng.Catalog.DatabasePath())
		os.Exit(0)
	case lower == ".help" || lower == "help":
		st.printHelp()
		return nil
	case lower == ".version":
		fmt.Fprintf(st.output, "gokql v%s\n", version)
		return nil
	case strings.HasPrefix(lower, ".timer"):
		st.handleTimer(input)
		return nil
	case strings.HasPrefix(lower, ".mode"):
		st.handleMode(input)
		return nil
	case strings.HasPrefix(lower, ".output"):
		st.handleOutput(input)
		return nil
	case strings.HasPrefix(lower, ".read "):
		st.handleRead(input)
		return nil
	case strings.HasPrefix(lower, ".open"):
		st.handleOpen(input)
		return nil
	case strings.HasPrefix(lower, ".schema"):
		st.handleSchema(input)
		return nil
	}
	return st.runCommand(input)
}

// --- Command execution ---

func (st *replState) runCommand(input string) error {
	start := time.Now()

	stmt, err := parser.Parse(input)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	result, err := st.eng.Execute(stmt)
	if err != nil {
		return err
	}

	elapsed := time.Since(start)

	if result != nil {
		st.printResult(result)
	} else {
		fmt.Fprintln(st.output, "OK")
	}

	if st.timer {
		fmt.Fprintf(os.Stderr, "Elapsed: %v\n", elapsed.Round(time.Microsecond))
	}
	return nil
}

// --- Output rendering ---

func (st *replState) printResult(t *types.Table) {
	switch st.mode {
	case "csv":
		st.printCSV(t, ",")
	case "tsv":
		st.printCSV(t, "\t")
	case "json":
		st.printJSON(t)
	case "jsonl":
		st.printJSONL(t)
	default:
		st.printTable(t)
	}
}

func (st *replState) printTable(t *types.Table) {
	if len(t.Schema.Columns) == 0 {
		return
	}

	w := tabwriter.NewWriter(st.output, 0, 0, 2, ' ', 0)

	// Header
	headers := make([]string, len(t.Schema.Columns))
	for i, col := range t.Schema.Columns {
		headers[i] = col.Name
	}
	fmt.Fprintln(w, strings.Join(headers, "\t"))

	// Separator
	seps := make([]string, len(t.Schema.Columns))
	for i, col := range t.Schema.Columns {
		seps[i] = strings.Repeat("─", maxInt(len(col.Name), 4))
	}
	fmt.Fprintln(w, strings.Join(seps, "\t"))

	// Rows
	for _, row := range t.Rows {
		cells := make([]string, len(t.Schema.Columns))
		for i, col := range t.Schema.Columns {
			if i < len(row) {
				cells[i] = types.FormatValue(row[i], col.Type)
			}
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}

	w.Flush()
	fmt.Fprintf(st.output, "(%d rows)\n\n", len(t.Rows))
}

func (st *replState) printCSV(t *types.Table, sep string) {
	// Header
	headers := make([]string, len(t.Schema.Columns))
	for i, col := range t.Schema.Columns {
		headers[i] = col.Name
	}
	fmt.Fprintln(st.output, strings.Join(headers, sep))

	// Rows
	for _, row := range t.Rows {
		cells := make([]string, len(t.Schema.Columns))
		for i, col := range t.Schema.Columns {
			if i < len(row) {
				v := types.FormatValue(row[i], col.Type)
				// Quote fields containing separator or quotes
				if strings.ContainsAny(v, sep+"\"\n") {
					v = "\"" + strings.ReplaceAll(v, "\"", "\"\"") + "\""
				}
				cells[i] = v
			}
		}
		fmt.Fprintln(st.output, strings.Join(cells, sep))
	}
}

func (st *replState) printJSON(t *types.Table) {
	var rows []map[string]interface{}
	for _, row := range t.Rows {
		obj := make(map[string]interface{})
		for i, col := range t.Schema.Columns {
			if i < len(row) {
				obj[col.Name] = row[i]
			}
		}
		rows = append(rows, obj)
	}
	enc := json.NewEncoder(st.output)
	enc.SetIndent("", "  ")
	enc.Encode(rows)
}

func (st *replState) printJSONL(t *types.Table) {
	enc := json.NewEncoder(st.output)
	for _, row := range t.Rows {
		obj := make(map[string]interface{})
		for i, col := range t.Schema.Columns {
			if i < len(row) {
				obj[col.Name] = row[i]
			}
		}
		enc.Encode(obj)
	}
}

// --- Dot command handlers ---

func (st *replState) handleTimer(input string) {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		if st.timer {
			fmt.Fprintln(st.output, "Timer: on")
		} else {
			fmt.Fprintln(st.output, "Timer: off")
		}
		return
	}
	switch strings.ToLower(parts[1]) {
	case "on":
		st.timer = true
		fmt.Fprintln(st.output, "Timer: on")
	case "off":
		st.timer = false
		fmt.Fprintln(st.output, "Timer: off")
	default:
		fmt.Fprintf(os.Stderr, "Usage: .timer [on|off]\n")
	}
}

func (st *replState) handleMode(input string) {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		fmt.Fprintf(st.output, "Mode: %s\n", st.mode)
		return
	}
	mode := strings.ToLower(parts[1])
	switch mode {
	case "table", "csv", "json", "jsonl", "tsv":
		st.mode = mode
		fmt.Fprintf(st.output, "Mode: %s\n", st.mode)
	default:
		fmt.Fprintf(os.Stderr, "Unknown mode %q. Options: table, csv, json, jsonl, tsv\n", mode)
	}
}

func (st *replState) handleOutput(input string) {
	parts := strings.Fields(input)
	if len(parts) < 2 || parts[1] == "-" {
		st.closeOutput()
		st.output = os.Stdout
		st.outputFile = nil
		fmt.Println("Output: stdout")
		return
	}
	path := strings.TrimSpace(strings.TrimPrefix(input, ".output"))
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	st.closeOutput()
	st.outputFile = f
	st.output = f
	fmt.Printf("Output: %s\n", path)
}

func (st *replState) handleRead(input string) {
	path := strings.TrimSpace(strings.TrimPrefix(input, ".read "))
	if path == "" {
		fmt.Fprintf(os.Stderr, "Usage: .read FILE\n")
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if err := st.dispatch(line); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
	}
}

func (st *replState) handleOpen(input string) {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		// Re-open as in-memory
		cat := catalog.NewMemory()
		st.eng = engine.New(cat)
		fmt.Fprintln(st.output, "Connected to a transient in-memory database.")
		return
	}

	path := parts[len(parts)-1]
	cat, err := openCatalog(path, false, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	st.eng = engine.New(cat)
	tables := cat.ListTables()
	fmt.Fprintf(st.output, "Database: %s (%d tables)\n", path, len(tables))
}

func (st *replState) handleSchema(input string) {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		// Show all tables
		tables := st.eng.Catalog.ListTables()
		if len(tables) == 0 {
			fmt.Fprintln(st.output, "No tables.")
			return
		}
		for _, name := range tables {
			tbl := st.eng.Catalog.GetTable(name)
			if tbl == nil {
				continue
			}
			fmt.Fprintf(st.output, "%s (", name)
			for i, col := range tbl.Schema.Columns {
				if i > 0 {
					fmt.Fprint(st.output, ", ")
				}
				fmt.Fprintf(st.output, "%s: %s", col.Name, col.Type)
			}
			fmt.Fprintln(st.output, ")")
		}
		return
	}
	name := parts[1]
	tbl := st.eng.Catalog.GetTable(name)
	if tbl == nil {
		fmt.Fprintf(os.Stderr, "Table %q not found\n", name)
		return
	}
	w := tabwriter.NewWriter(st.output, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Column\tType")
	fmt.Fprintln(w, "──────\t────")
	for _, col := range tbl.Schema.Columns {
		fmt.Fprintf(w, "%s\t%s\n", col.Name, col.Type)
	}
	w.Flush()
}

func (st *replState) closeOutput() {
	if st.outputFile != nil {
		st.outputFile.Close()
	}
}

func (st *replState) loadRC() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	rcPath := filepath.Join(home, ".okqlrc")
	data, err := os.ReadFile(rcPath)
	if err != nil {
		return // No RC file — fine
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "#") {
			continue
		}
		// Only process dot commands from RC (not queries)
		if strings.HasPrefix(line, ".") {
			lower := strings.ToLower(line)
			switch {
			case strings.HasPrefix(lower, ".timer"):
				st.handleTimer(line)
			case strings.HasPrefix(lower, ".mode"):
				st.handleMode(line)
			}
		}
	}
}

// --- Help ---

func (st *replState) printHelp() {
	fmt.Fprint(st.output, `
Queries:
  TableName                              Select all rows
  TableName | where Col > 5              Filter rows
  TableName | project Col1, Col2         Select columns
  TableName | extend NewCol = expr       Add computed column
  TableName | take 10                    First N rows
  TableName | count                      Count rows
  TableName | distinct Col1, Col2        Unique values
  TableName | order by Col desc          Sort rows
  TableName | top 5 by Col               Top N by column
  TableName | summarize count() by Col   Aggregate
  print Expr1, Expr2                     Evaluate expressions

File queries:
  csv("file.csv") | where Col > 5       Query CSV file directly
  csv("file.csv") | getschema            Inspect CSV schema
  json("file.json") | take 10            Query JSON array file
  ndjson("file.jsonl") | count           Query newline-delimited JSON
  parquet("file.parquet") | take 10      Query Apache Parquet file

Management:
  .create table T (Col1: type, ...)      Create table
  .create-merge table T (Col1: type)     Create or add columns
  .drop table T                          Drop table
  .drop extent <id>                      Drop single extent
  .show tables                           List all tables
  .show table T extents                  Show extents for table
  .show database                         Database info
  .ingest csv into table T from "f"      Ingest CSV file
  .ingest inline into table T <| data    Ingest CSV data

REPL commands:
  .open [PATH]                           Switch database (no arg = in-memory)
  .schema [TABLE]                        Show table schema(s)
  .timer [on|off]                        Show query execution time
  .mode [table|csv|json|jsonl|tsv]       Output format
  .output [FILE|-]                       Redirect output to file
  .read FILE                             Execute KQL script file
  .version                               Show version
  .help                                  This help
  .quit                                  Exit

Types: string, long, int, real, bool, datetime, guid, dynamic
`)
	fmt.Fprintln(st.output)
}

// --- Helpers ---

// looksLikePath returns true if the argument appears to be a file/directory path
// rather than a KQL query.
func looksLikePath(s string) bool {
	lower := strings.ToLower(s)

	// Query indicators take priority — these are never paths
	if strings.Contains(s, "|") {
		return false
	}
	for _, prefix := range []string{"csv(", "json(", "ndjson(", "parquet(", "vortex(", "print ", "union "} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	// Management commands are not paths
	if strings.HasPrefix(lower, ".create") || strings.HasPrefix(lower, ".show") ||
		strings.HasPrefix(lower, ".drop") || strings.HasPrefix(lower, ".ingest") ||
		strings.HasPrefix(lower, ".help") || strings.HasPrefix(lower, ".open") ||
		strings.HasPrefix(lower, ".timer") || strings.HasPrefix(lower, ".mode") {
		return false
	}

	// Now check path indicators
	if strings.ContainsAny(s, "/\\") {
		return true
	}
	ext := strings.ToLower(filepath.Ext(s))
	switch ext {
	case ".vortex", ".okql", ".db", ".duckdb":
		return true
	}
	// Starts with . (relative path like ./mydb)
	if strings.HasPrefix(s, ".") {
		return true
	}
	return false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// openCatalog opens a database directory, choosing catalog-free
// discovery when catalog.json is absent but .vtx extent files exist
// (directly or under extents/), or when forced. A fresh empty
// directory opens in catalog mode for compatibility.
func openCatalog(dbPath string, forceDiscover, verbose bool) (*catalog.Catalog, error) {
	// Mode auto-detection itself now lives in catalog.OpenAuto,
	// shared with pkg/engine's federation support — this wrapper
	// keeps only the CLI-specific verbose logging.
	cat, err := catalog.OpenAuto(dbPath, forceDiscover)
	if err != nil {
		return nil, err
	}
	if verbose && cat.IsDiscovery() {
		fmt.Fprintf(os.Stderr, "[catalog] discovery mode: %d tables from extent footers (no catalog.json)\n",
			len(cat.Database.Tables))
	}
	return cat, nil
}
