package parser

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// Parse parses a KQL statement (query or management command).
// Supports multi-statement input with let bindings:
//
//	let name = expr; let name2 = Table | where ...; FinalQuery | ...
func Parse(input string) (Statement, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	// Split on semicolons (respecting strings and parentheses)
	stmts := splitStatements(input)

	// If single statement with no let prefix, parse directly
	if len(stmts) == 1 && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(stmts[0])), "let ") {
		return parseSingleStatement(stmts[0])
	}

	// Multi-statement: parse let bindings + final statement
	var lets []*LetStatement
	var finalStr string

	for i, s := range stmts {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		lower := strings.ToLower(s)
		if strings.HasPrefix(lower, "let ") {
			letStmt, err := parseLetStatement(s)
			if err != nil {
				return nil, fmt.Errorf("let statement %d: %w", i+1, err)
			}
			lets = append(lets, letStmt)
		} else {
			// This is the final (non-let) statement
			finalStr = s
		}
	}

	if finalStr == "" {
		// All statements are lets with no final query — error
		return nil, fmt.Errorf("expected a query after let statement(s)")
	}

	finalStmt, err := parseSingleStatement(finalStr)
	if err != nil {
		return nil, fmt.Errorf("final statement: %w", err)
	}

	if len(lets) == 0 {
		return finalStmt, nil
	}

	return &CompoundStatement{Lets: lets, Final: finalStmt}, nil
}

// parseSingleStatement parses a non-let statement.
// pipeableSimpleCommandPrefixes are dot-commands with no internal
// query/pipe grammar of their own (no <| clause, no bracket-depth-
// sensitive argument), so splitting the whole input on bare '|' is
// unambiguous and safe. Every OTHER dot-command (.set-or-append,
// .embed-into, .chunk-file, etc.) is deliberately excluded: splitPipe
// does not special-case '<|', so applying it generically would
// silently split a compound command's OWN internal query at the wrong
// point rather than at a trailing pipe-continuation — a real
// correctness risk, not just an unsupported-syntax error. Found live
// while working the backlog: `.show tables | where ...` and
// `.help | where ...` both failed with "unknown command" even though
// nothing about either command's own grammar conflicts with piping.
var pipeableSimpleCommandPrefixes = []string{
	".show tables", ".show database", ".help", ".show table ",
}

func parseSingleStatement(input string) (Statement, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}
	if input[0] == '.' {
		lower := strings.ToLower(input)
		for _, prefix := range pipeableSimpleCommandPrefixes {
			if !strings.HasPrefix(lower, prefix) {
				continue
			}
			parts := splitPipe(input)
			if len(parts) == 1 {
				break // no trailing pipe present; fall through to the normal path
			}
			inner, err := parseCommand(strings.TrimSpace(parts[0]))
			if err != nil {
				return nil, err
			}
			piped := &PipedCommand{Inner: inner}
			for i, seg := range parts[1:] {
				seg = strings.TrimSpace(seg)
				if seg == "" {
					continue
				}
				op, err := parseOperator(seg)
				if err != nil {
					return nil, fmt.Errorf("pipeline operator %d after %s: %w", i+1, strings.Fields(input)[0], err)
				}
				piped.Operators = append(piped.Operators, op)
			}
			return piped, nil
		}
		return parseCommand(input)
	}
	return parseQuery(input)
}

// parseLetStatement parses "let name = value_or_query".
func parseLetStatement(input string) (*LetStatement, error) {
	// Strip "let " prefix
	rest := strings.TrimSpace(input[4:])

	// Find the = sign
	eqIdx := strings.Index(rest, "=")
	if eqIdx < 0 {
		return nil, fmt.Errorf("expected '=' in let statement")
	}

	// Guard against ==
	if eqIdx+1 < len(rest) && rest[eqIdx+1] == '=' {
		return nil, fmt.Errorf("expected '=' in let statement, got '=='")
	}

	name := strings.TrimSpace(rest[:eqIdx])
	if name == "" {
		return nil, fmt.Errorf("expected name before '=' in let statement")
	}

	valueStr := strings.TrimSpace(rest[eqIdx+1:])
	if valueStr == "" {
		return nil, fmt.Errorf("expected value after '=' in let statement")
	}

	// User-defined function: let f = (x: long) { x * 2 }
	if fn, ok, err := tryParseFunctionDef(valueStr); ok {
		if err != nil {
			return nil, fmt.Errorf("let %s: %w", name, err)
		}
		return &LetStatement{Name: name, Value: fn}, nil
	}

	// Determine if this is a tabular expression or scalar.
	// Heuristic: if it contains a pipe '|' at depth 0, it's tabular.
	// Also if it looks like a bare table name (identifier with no operators), treat as tabular.
	// datatable and table-valued functions (csv, json, ndjson, parquet, vortex) are
	// always tabular even without a pipe, so route them to the query parser directly.
	if isTabularSource(valueStr) || isTabularExpression(valueStr) {
		query, err := parseQuery(valueStr)
		if err != nil {
			return nil, fmt.Errorf("let %s: %w", name, err)
		}
		return &LetStatement{Name: name, Value: query}, nil
	}

	// Scalar expression
	expr, err := ParseExpr(valueStr)
	if err != nil {
		return nil, fmt.Errorf("let %s: %w", name, err)
	}
	return &LetStatement{Name: name, Value: &ScalarExpr{Expr: expr}}, nil
}

// isTabularExpression checks if a string looks like a tabular query (has pipes at depth 0).
func isTabularExpression(s string) bool {
	depth := 0
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inQuote != 0 {
			if ch == inQuote && !precededByOddBackslashes(s, i) {
				inQuote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inQuote = ch
			continue
		}
		if ch == '(' {
			depth++
		}
		if ch == ')' && depth > 0 {
			depth--
		}
		if ch == '|' && depth == 0 {
			return true
		}
	}
	return false
}

// isTabularSource reports whether the value string begins with a construct that
// is inherently tabular: a datatable literal or a table-valued function call
// (csv, json, ndjson, parquet, vortex). These must be parsed as queries even
// when no pipe operator is present.
func isTabularSource(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(lower, "datatable") {
		rest := lower[len("datatable"):]
		// Word boundary: next non-space char must be '(' so an identifier like
		// "datatables" is not misclassified.
		rest = strings.TrimLeft(rest, " \t")
		return strings.HasPrefix(rest, "(")
	}
	// range columnName from ... — real ADX's own example shows this
	// let-bound directly (let _data = range x from 1 to 100 step 1;),
	// so it needs the same recognition datatable already gets here.
	// Word boundary: next non-space char after "range" must NOT be an
	// identifier character, so a column or table literally named
	// "rangeX" isn't misclassified.
	if strings.HasPrefix(lower, "range") {
		rest := lower[len("range"):]
		if rest == "" || !isIdentChar(rest[0]) {
			return true
		}
	}
	return parseTableFunc(strings.TrimSpace(s)) != nil
}

// tryParseFunctionDef detects and parses a UDF lambda of the form
// (name: type, ...) { body-expr }. Returns ok=false when the value does not
// look like a lambda (so let parsing can fall through to tabular/scalar);
// returns ok=true with a non-nil error when it is lambda-shaped but invalid.
func tryParseFunctionDef(s string) (*FunctionDef, bool, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "(") || !strings.HasSuffix(s, "}") {
		return nil, false, nil
	}
	// Find the matching close paren of the parameter list
	depth := 0
	closeIdx := -1
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inQuote != 0 {
			if ch == inQuote && !precededByOddBackslashes(s, i) {
				inQuote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inQuote = ch
			continue
		}
		if ch == '(' {
			depth++
		}
		if ch == ')' {
			depth--
			if depth == 0 {
				closeIdx = i
				break
			}
		}
	}
	if closeIdx < 0 {
		return nil, false, nil
	}
	rest := strings.TrimSpace(s[closeIdx+1:])
	if !strings.HasPrefix(rest, "{") {
		// Parenthesized scalar expression, not a lambda
		return nil, false, nil
	}
	body := strings.TrimSpace(rest[1 : len(rest)-1])
	if body == "" {
		return nil, true, fmt.Errorf("function body is empty")
	}

	// Parse parameters: name: type, name: type, ...
	paramStr := strings.TrimSpace(s[1:closeIdx])
	var params []FunctionParam
	if paramStr != "" {
		for _, part := range splitAndTrim(paramStr, ',') {
			colonIdx := strings.Index(part, ":")
			if colonIdx < 0 {
				return nil, true, fmt.Errorf("parameter %q: expected 'name: type'", part)
			}
			pname := strings.TrimSpace(part[:colonIdx])
			tname := strings.TrimSpace(part[colonIdx+1:])
			ptype, err := types.ParseType(tname)
			if err != nil {
				return nil, true, fmt.Errorf("parameter %q: %w", pname, err)
			}
			params = append(params, FunctionParam{Name: pname, Type: ptype})
		}
	}

	bodyExpr, err := ParseExpr(body)
	if err != nil {
		return nil, true, fmt.Errorf("function body: %w", err)
	}
	return &FunctionDef{Params: params, Body: bodyExpr}, true, nil
}

// splitStatements splits input on semicolons, respecting strings,
// parentheses, AND braces.
//
// depth previously tracked only (...) nesting, not {...} at all —
// found live, not hypothetical (a different model's testing, Kimi):
// .create-or-alter function T() { let s = "open"; Body | where ... }
// silently split on the ; INSIDE the function body's own { } block,
// since that ; sat at depth==0 as far as this function was concerned.
// Two compounding failures resulted from the single wrong split:
// the fragment before the semicolon (".create-or-alter function T() {
// let s = \"open\"") was silently discarded entirely (this function's
// own caller, Parse, treats any non-"let "-prefixed piece as THE
// final statement, so a later piece unconditionally overwrote it,
// with no error at all -- the let binding simply evaporated, later
// surfacing as "column s not found" when the body tried to reference
// it), while the fragment after it ("Body | where ... }") kept the
// function body's own closing brace glued onto its last operator's
// text, surfacing as "unknown operator: \"count }\"" or similar.
// Fixed by tracking { and } under the SAME depth counter as ( and ) —
// for the purpose of "is this ; inside some nested construct at all",
// which bracket kind is nesting doesn't matter, only whether we're
// nested at all.
func splitStatements(s string) []string {
	var parts []string
	var current strings.Builder
	inQuote := byte(0)
	depth := 0

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inQuote != 0 {
			current.WriteByte(ch)
			if ch == inQuote && !precededByOddBackslashes(s, i) {
				inQuote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inQuote = ch
			current.WriteByte(ch)
			continue
		}
		if ch == '(' || ch == '{' {
			depth++
		}
		if (ch == ')' || ch == '}') && depth > 0 {
			depth--
		}
		if ch == ';' && depth == 0 {
			parts = append(parts, current.String())
			current.Reset()
			continue
		}
		current.WriteByte(ch)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// --- Management Command Parsing ---

func parseCommand(input string) (Statement, error) {
	lower := strings.ToLower(input)

	// .show tables
	if lower == ".help" || lower == "help" {
		return &HelpCmd{}, nil
	}

	if lower == ".show tables" {
		return &ShowTablesCmd{}, nil
	}

	// .show database
	if lower == ".show database" {
		return &ShowDatabaseCmd{}, nil
	}

	// .show table T extents
	if strings.HasPrefix(lower, ".show table ") && strings.HasSuffix(lower, " extents") {
		name := strings.TrimSpace(input[len(".show table ") : len(input)-len(" extents")])
		if name == "" {
			return nil, fmt.Errorf("expected table name in .show table ... extents")
		}
		return &ShowTableExtentsCmd{TableName: name}, nil
	}

	// .drop extent <guid>
	if strings.HasPrefix(lower, ".drop extent ") {
		guid := strings.TrimSpace(input[len(".drop extent "):])
		return &DropExtentCmd{ExtentID: guid}, nil
	}

	// .drop table T
	if strings.HasPrefix(lower, ".drop table ") {
		name := strings.TrimSpace(input[len(".drop table "):])
		return &DropTableCmd{TableName: name}, nil
	}

	// .create-merge table T (...)
	if strings.HasPrefix(lower, ".create-merge table ") {
		return parseCreateTable(input[len(".create-merge table "):], true)
	}

	if strings.HasPrefix(lower, ".alter-merge table ") {
		return parseAlterMergeTable(input[len(".alter-merge table "):])
	}

	// .create table T (...)
	if strings.HasPrefix(lower, ".create table ") {
		return parseCreateTable(input[len(".create table "):], false)
	}

	// .ingest csv into table T from "path"
	if strings.HasPrefix(lower, ".ingest csv into table ") {
		rest := input[len(".ingest csv into table "):]
		fromIdx := strings.Index(strings.ToLower(rest), " from ")
		if fromIdx < 0 {
			return nil, fmt.Errorf(".ingest csv requires 'from path'")
		}
		tableName := strings.TrimSpace(rest[:fromIdx])
		pathStr := strings.TrimSpace(rest[fromIdx+6:])
		// Strip quotes
		pathStr = strings.Trim(pathStr, "\"'")
		return &IngestCSVCmd{TableName: tableName, FilePath: pathStr}, nil
	}

	// .ingest inline into table T <| data
	if strings.HasPrefix(lower, ".ingest inline into table ") {
		rest := input[len(".ingest inline into table "):]
		idx := strings.Index(rest, "<|")
		if idx < 0 {
			return nil, fmt.Errorf(".ingest inline requires <| delimiter")
		}
		tableName := strings.TrimSpace(rest[:idx])
		data := strings.TrimSpace(rest[idx+2:])
		return &IngestInlineCmd{TableName: tableName, Data: data}, nil
	}

	// .set T <| query
	// .set-or-append T <| query — appends the query's result rows as a
	// new extent. Checked before ".set " since it's a longer, more
	// specific prefix.
	if strings.HasPrefix(lower, ".set-or-append ") {
		rest := input[len(".set-or-append "):]
		idx := strings.Index(rest, "<|")
		if idx < 0 {
			return nil, fmt.Errorf(".set-or-append requires <| delimiter")
		}
		tableName := strings.TrimSpace(rest[:idx])
		queryStr := strings.TrimSpace(rest[idx+2:])
		query, err := parseQuery(queryStr)
		if err != nil {
			return nil, fmt.Errorf(".set-or-append query: %w", err)
		}
		return &SetOrAppendCmd{TableName: tableName, Query: query.(*Query)}, nil
	}

	// .chunk-file "path" into T — reads a markdown file, splits it into
	// paragraph-level chunks (blank-line delimited, heading-trail
	// tagged, oversized blocks auto-split), writes into T. See
	// engine/chunk_file.go.
	if strings.HasPrefix(lower, ".chunk-file ") {
		rest := strings.TrimSpace(input[len(".chunk-file "):])
		if len(rest) == 0 || rest[0] != '"' {
			return nil, fmt.Errorf(".chunk-file: expected a quoted path")
		}
		ep := &exprParser{input: rest}
		pathExpr, err := ep.parseString()
		if err != nil {
			return nil, fmt.Errorf(".chunk-file: parsing path: %w", err)
		}
		pathLit, ok := pathExpr.(*Literal)
		if !ok {
			return nil, fmt.Errorf(".chunk-file: expected a quoted string path")
		}
		path, _ := pathLit.Value.(string)

		remainder := strings.TrimSpace(rest[ep.pos:])
		if !strings.HasPrefix(strings.ToLower(remainder), "into ") {
			return nil, fmt.Errorf(".chunk-file: expected 'into TableName' after the path, got %q", remainder)
		}
		tableName := strings.TrimSpace(remainder[len("into "):])
		if tableName == "" {
			return nil, fmt.Errorf(".chunk-file: missing table name after 'into'")
		}
		return &ChunkFileCmd{Path: path, TableName: tableName}, nil
	}

	// .embed-into T <| query — bulk-embeds a Text column via Ollama's
	// batch API in one HTTP round trip per batch instead of one per
	// row, then writes (Id, Embedding, Model, Provenance) in a single
	// flushBatch. See engine/embed_bulk.go.
	if strings.HasPrefix(lower, ".embed-into ") {
		rest := input[len(".embed-into "):]
		idx := strings.Index(rest, "<|")
		if idx < 0 {
			return nil, fmt.Errorf(".embed-into requires <| delimiter")
		}
		tableName := strings.TrimSpace(rest[:idx])
		queryStr := strings.TrimSpace(rest[idx+2:])
		query, err := parseQuery(queryStr)
		if err != nil {
			return nil, fmt.Errorf(".embed-into query: %w", err)
		}
		return &EmbedIntoCmd{TableName: tableName, Query: query.(*Query)}, nil
	}

	if strings.HasPrefix(lower, ".set ") {
		rest := input[len(".set "):]
		idx := strings.Index(rest, "<|")
		if idx < 0 {
			return nil, fmt.Errorf(".set requires <| delimiter")
		}
		tableName := strings.TrimSpace(rest[:idx])
		queryStr := strings.TrimSpace(rest[idx+2:])
		query, err := parseQuery(queryStr)
		if err != nil {
			return nil, fmt.Errorf(".set query: %w", err)
		}
		return &SetCmd{TableName: tableName, Query: query.(*Query)}, nil
	}

	// .compact table T [where <predicate>] — discovery mode only.
	// Merges T's current extents into one, optionally dropping rows
	// the predicate excludes (see CompactTableCmd's doc comment).
	// Genuinely unsafe under concurrent access unlike every other
	// discovery-mode operation in this codebase — see engine/compact.go
	// for why, and run only when the scope is quiescent (no other
	// session reading or writing this table).
	if strings.HasPrefix(lower, ".compact table ") {
		rest := strings.TrimSpace(input[len(".compact table "):])
		if rest == "" {
			return nil, fmt.Errorf(".compact table: missing table name")
		}
		// " where " must be matched case-insensitively but split on the
		// ORIGINAL (not lowercased) rest, so the predicate text itself
		// keeps its original casing (string literals, etc.) — the same
		// approach the pipeline `| where` segment split already uses.
		name := rest
		var whereExpr Expr
		restLower := strings.ToLower(rest)
		if idx := strings.Index(restLower, " where "); idx >= 0 {
			name = strings.TrimSpace(rest[:idx])
			predStr := rest[idx+len(" where "):]
			expr, err := ParseExpr(predStr)
			if err != nil {
				return nil, fmt.Errorf(".compact table: where: %w", err)
			}
			whereExpr = expr
		}
		if name == "" {
			return nil, fmt.Errorf(".compact table: missing table name")
		}
		return &CompactTableCmd{TableName: name, Where: whereExpr}, nil
	}

	// .gc table T — physically removes .superseded files for T, left
	// behind by .compact. Safe to run at any time (see engine/compact.go).
	if strings.HasPrefix(lower, ".gc table ") {
		name := strings.TrimSpace(input[len(".gc table "):])
		if name == "" {
			return nil, fmt.Errorf(".gc table: missing table name")
		}
		return &GCTableCmd{TableName: name}, nil
	}

	// .compact database / .gc database — every table, INCLUDING
	// _Dictionaries (hidden from .show tables by design, but not from
	// this — see Catalog.ListAllTables's doc comment for why that
	// distinction exists and the real incident that motivated it).
	if lower == ".compact database" {
		return &CompactDatabaseCmd{}, nil
	}
	if lower == ".gc database" {
		return &GCDatabaseCmd{}, nil
	}

	// .merge table T extents
	if strings.HasPrefix(lower, ".merge table ") && strings.HasSuffix(lower, " extents") {
		name := strings.TrimSpace(input[len(".merge table ") : len(input)-len(" extents")])
		if name == "" {
			return nil, fmt.Errorf("expected table name in .merge table ... extents")
		}
		return &MergeExtentsCmd{TableName: name}, nil
	}

	// .create-or-alter function / .create function [ifnotexists] — see
	// CreateFunctionCmd's doc comment. Checked with a space after
	// "function" required, so this doesn't accidentally prefix-match
	// something like a hypothetical future ".create functionality"
	// command.
	if strings.HasPrefix(lower, ".create-or-alter function ") {
		return parseCreateFunction(input[len(".create-or-alter function "):], true, false)
	}
	if strings.HasPrefix(lower, ".create function ifnotexists ") {
		return parseCreateFunction(input[len(".create function ifnotexists "):], false, true)
	}
	if strings.HasPrefix(lower, ".create function ") {
		return parseCreateFunction(input[len(".create function "):], false, false)
	}

	if lower == ".show functions" {
		return &ShowFunctionsCmd{}, nil
	}
	if strings.HasPrefix(lower, ".show function ") {
		name := strings.TrimSpace(input[len(".show function "):])
		// Real ADX allows "with (PropertyName=PropertyValue)" here
		// (e.g. ShowObfuscatedStrings) — not built in this first
		// version; strip and ignore a trailing with(...) clause rather
		// than error, so the common case (just the name) works and an
		// unused property clause is silently a no-op instead of a hard
		// failure over an option this implementation doesn't need yet.
		if idx := strings.Index(strings.ToLower(name), " with "); idx >= 0 {
			name = strings.TrimSpace(name[:idx])
		}
		if name == "" {
			return nil, fmt.Errorf(".show function: missing function name")
		}
		return &ShowFunctionCmd{Name: name}, nil
	}

	// .drop function Name  |  .drop functions (A, B, C)
	// Real ADX gives these genuinely different semantics (verified,
	// not assumed): singular errors if the function doesn't exist;
	// plural silently tolerates missing ones. ExplicitPlural tracks
	// which FORM was used, not just the resulting name count, since a
	// plural-form call naming exactly one function still gets plural
	// (tolerant) semantics in real ADX.
	if strings.HasPrefix(lower, ".drop function ") {
		name := strings.TrimSpace(input[len(".drop function "):])
		if name == "" {
			return nil, fmt.Errorf(".drop function: missing function name")
		}
		return &DropFunctionCmd{Names: []string{name}, ExplicitPlural: false}, nil
	}
	if strings.HasPrefix(lower, ".drop functions ") {
		rest := strings.TrimSpace(input[len(".drop functions "):])
		rest = strings.TrimPrefix(rest, "(")
		rest = strings.TrimSuffix(rest, ")")
		var names []string
		for _, part := range strings.Split(rest, ",") {
			if n := strings.TrimSpace(part); n != "" {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			return nil, fmt.Errorf(".drop functions: missing function name(s)")
		}
		return &DropFunctionCmd{Names: names, ExplicitPlural: true}, nil
	}

	// .create [ifnotexists] materialized-view [with (...)] Name on table Source { Query }
	if strings.HasPrefix(lower, ".create ifnotexists materialized-view ") {
		return parseCreateMaterializedView(input[len(".create ifnotexists materialized-view "):], true)
	}
	if strings.HasPrefix(lower, ".create materialized-view ") {
		return parseCreateMaterializedView(input[len(".create materialized-view "):], false)
	}
	if lower == ".show materialized-views" {
		return &ShowMaterializedViewsCmd{}, nil
	}
	if strings.HasPrefix(lower, ".drop materialized-view ") {
		name := strings.TrimSpace(input[len(".drop materialized-view "):])
		if name == "" {
			return nil, fmt.Errorf(".drop materialized-view: missing name")
		}
		return &DropMaterializedViewCmd{Name: name}, nil
	}

	return nil, fmt.Errorf("unknown command: %s", input)
}

// parseCreateMaterializedView parses:
//   [with (docstring = "...", folder = "...")] Name on table SourceTable { Query }
// Verified against real ADX's own .create materialized-view syntax
// before adopting this shape. Query-shape validation (single
// summarize, last operator, only supported aggregate functions) is
// NOT done here — this function stays purely syntactic, matching
// every other command in this parser; see
// engine/materialized_views.go's validateMaterializedViewQuery for the
// semantic checks, which need the source table's real schema and so
// can't run at parse time at all.
func parseCreateMaterializedView(rest string, ifNotExists bool) (Statement, error) {
	rest = strings.TrimSpace(rest)
	cmd := &CreateMaterializedViewCmd{IfNotExists: ifNotExists}

	if strings.HasPrefix(strings.ToLower(rest), "with (") || strings.HasPrefix(strings.ToLower(rest), "with(") {
		openIdx := strings.Index(rest, "(")
		closeIdx := findMatchingParen(rest, openIdx)
		if closeIdx < 0 {
			return nil, fmt.Errorf("create materialized-view: unterminated with(...) clause")
		}
		props := rest[openIdx+1 : closeIdx]
		for _, kv := range splitAndTrim(props, ',') {
			eqIdx := strings.Index(kv, "=")
			if eqIdx < 0 {
				return nil, fmt.Errorf("create materialized-view: malformed property %q, expected name=value", kv)
			}
			key := strings.ToLower(strings.TrimSpace(kv[:eqIdx]))
			val := strings.TrimSpace(kv[eqIdx+1:])
			val = strings.Trim(val, `"'`)
			switch key {
			case "docstring":
				cmd.DocString = val
			case "folder":
				cmd.Folder = val
			case "backfill", "effectivedatetime", "lookback", "lookback_column",
				"autoupdateschema", "dimensiontables", "allowmaterializedviewswithoutrowlevelsecurity":
				// Real ADX properties this first version doesn't
				// implement — accepted and silently ignored, matching
				// the same "don't fail purely over an unsupported
				// option" choice already made for .create function's
				// view/skipvalidation properties.
			default:
				return nil, fmt.Errorf("create materialized-view: unknown property %q", key)
			}
		}
		rest = strings.TrimSpace(rest[closeIdx+1:])
	}

	nameEnd := strings.IndexAny(rest, " 	")
	if nameEnd < 0 {
		return nil, fmt.Errorf("create materialized-view: expected Name on table Source { Query }")
	}
	cmd.Name = strings.TrimSpace(rest[:nameEnd])
	if cmd.Name == "" {
		return nil, fmt.Errorf("create materialized-view: missing name")
	}
	rest = strings.TrimSpace(rest[nameEnd:])

	const onTablePrefix = "on table "
	if !strings.HasPrefix(strings.ToLower(rest), onTablePrefix) {
		return nil, fmt.Errorf("create materialized-view %q: expected 'on table SourceTable'", cmd.Name)
	}
	rest = strings.TrimSpace(rest[len(onTablePrefix):])

	sourceEnd := strings.IndexAny(rest, " 	{")
	if sourceEnd < 0 {
		return nil, fmt.Errorf("create materialized-view %q: expected source table name", cmd.Name)
	}
	cmd.SourceTable = strings.TrimSpace(rest[:sourceEnd])
	if cmd.SourceTable == "" {
		return nil, fmt.Errorf("create materialized-view %q: missing source table name", cmd.Name)
	}
	rest = strings.TrimSpace(rest[sourceEnd:])

	if !strings.HasPrefix(rest, "{") {
		return nil, fmt.Errorf("create materialized-view %q: expected { Query }", cmd.Name)
	}
	closeIdx := findMatchingBrace(rest, 0)
	if closeIdx < 0 {
		return nil, fmt.Errorf("create materialized-view %q: unterminated { Query }", cmd.Name)
	}
	queryText := strings.TrimSpace(rest[1:closeIdx])
	if queryText == "" {
		return nil, fmt.Errorf("create materialized-view %q: empty query", cmd.Name)
	}

	stmt, err := Parse(queryText)
	if err != nil {
		return nil, fmt.Errorf("create materialized-view %q: query does not parse: %w", cmd.Name, err)
	}
	q, ok := stmt.(*Query)
	if !ok {
		return nil, fmt.Errorf("create materialized-view %q: body must be a tabular query, not a management command", cmd.Name)
	}
	cmd.Query = q
	cmd.QueryText = queryText

	return cmd, nil
}

// parseCreateFunction parses the shared shape behind .create-or-alter
// function and .create function [ifnotexists]:
//
//	[with (docstring = "...", folder = "...")] Name() { body }
//
// Parameterless only in this first version — an empty parameter list
// is required; anything else is a clear, immediate error rather than
// silently ignored parameters.
func parseCreateFunction(rest string, orAlter, ifNotExists bool) (Statement, error) {
	rest = strings.TrimSpace(rest)

	cmd := &CreateFunctionCmd{OrAlter: orAlter, IfNotExists: ifNotExists}

	if strings.HasPrefix(strings.ToLower(rest), "with (") || strings.HasPrefix(strings.ToLower(rest), "with(") {
		openIdx := strings.Index(rest, "(")
		closeIdx := findMatchingParen(rest, openIdx)
		if closeIdx < 0 {
			return nil, fmt.Errorf("create function: unterminated with(...) clause")
		}
		props := rest[openIdx+1 : closeIdx]
		for _, kv := range splitAndTrim(props, ',') {
			eqIdx := strings.Index(kv, "=")
			if eqIdx < 0 {
				return nil, fmt.Errorf("create function: malformed property %q, expected name=value", kv)
			}
			key := strings.ToLower(strings.TrimSpace(kv[:eqIdx]))
			val := strings.TrimSpace(kv[eqIdx+1:])
			val = strings.Trim(val, `"'`)
			switch key {
			case "docstring":
				cmd.DocString = val
			case "folder":
				cmd.Folder = val
			case "view", "skipvalidation":
				// Real ADX properties this first version doesn't
				// implement — accepted and silently ignored rather
				// than rejected, so a definition copied from a real
				// ADX script doesn't fail purely over an unsupported
				// option it doesn't strictly need here.
			default:
				return nil, fmt.Errorf("create function: unknown property %q", key)
			}
		}
		rest = strings.TrimSpace(rest[closeIdx+1:])
	}

	nameEnd := strings.IndexAny(rest, "( 	")
	if nameEnd < 0 {
		return nil, fmt.Errorf("create function: expected Name() { body }")
	}
	cmd.Name = strings.TrimSpace(rest[:nameEnd])
	if cmd.Name == "" {
		return nil, fmt.Errorf("create function: missing function name")
	}
	rest = strings.TrimSpace(rest[nameEnd:])

	if !strings.HasPrefix(rest, "(") {
		return nil, fmt.Errorf("create function %q: expected (parameters) after function name", cmd.Name)
	}
	paramCloseIdx := findMatchingParen(rest, 0)
	if paramCloseIdx < 0 {
		return nil, fmt.Errorf("create function %q: unterminated parameter list", cmd.Name)
	}
	paramText := rest[1:paramCloseIdx]
	params, paramErr := ParseFunctionParams(paramText)
	if paramErr != nil {
		return nil, fmt.Errorf("create function %q: %w", cmd.Name, paramErr)
	}
	cmd.Parameters = params
	cmd.ParametersText = strings.TrimSpace(paramText)
	rest = strings.TrimSpace(rest[paramCloseIdx+1:])

	if !strings.HasPrefix(rest, "{") {
		return nil, fmt.Errorf("create function %q: expected { body }", cmd.Name)
	}
	closeIdx := findMatchingBrace(rest, 0)
	if closeIdx < 0 {
		return nil, fmt.Errorf("create function %q: unterminated { body }", cmd.Name)
	}
	cmd.Body = strings.TrimSpace(rest[1:closeIdx])
	if cmd.Body == "" {
		return nil, fmt.Errorf("create function %q: empty body", cmd.Name)
	}

	// Validate the body parses NOW, at definition time — matching real
	// ADX's own default (skipvalidation=true opts out; not offered in
	// this first version, so validation always runs). Catches a typo
	// in the definition immediately rather than deferring the failure
	// to whenever the function is first called, possibly much later
	// and in a confusing context.
	if _, err := Parse(cmd.Body); err != nil {
		return nil, fmt.Errorf("create function %q: body does not parse: %w", cmd.Name, err)
	}

	return cmd, nil
}

// findMatchingBrace finds the index of the '}' matching the '{' at
// s[start] (which must itself be '{'), respecting quoted strings so a
// literal brace character inside a string doesn't miscount depth.
// Separate from findMatchingBracket ([/]), deliberately — kept
// distinct rather than generalizing the existing bracket-matcher to
// avoid any risk to its own, already-working callers.
func findMatchingBrace(s string, start int) int {
	depth := 0
	inStr := false
	for i := start; i < len(s); i++ {
		if s[i] == '"' && !precededByOddBackslashes(s, i) {
			inStr = !inStr
		}
		if inStr {
			continue
		}
		if s[i] == '{' {
			depth++
		} else if s[i] == '}' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// parseCreateTable parses "TableName (col1: type1, col2: type2, ...)"
// parseAlterMergeTable parses .alter-merge table T (col1:type1, ...)
// [with (docstring=..., folder=...)] — verified against real ADX's
// own .alter-merge table syntax before adopting this shape. Semantic
// checks (table must already exist, no type change for an existing
// column) happen at execution time (engine/alter_table.go), not
// here — parsing stays purely syntactic, matching every other command
// in this parser.
func parseAlterMergeTable(rest string) (Statement, error) {
	rest = strings.TrimSpace(rest)

	parenIdx := strings.Index(rest, "(")
	if parenIdx < 0 {
		return nil, fmt.Errorf("alter-merge table: expected (column definitions)")
	}
	tableName := strings.TrimSpace(rest[:parenIdx])
	if tableName == "" {
		return nil, fmt.Errorf("alter-merge table: expected table name")
	}

	closeIdx := findMatchingParen(rest, parenIdx)
	if closeIdx < 0 {
		return nil, fmt.Errorf("alter-merge table %q: expected closing ) in column definitions", tableName)
	}
	schema, err := parseColumnDefs(rest[parenIdx+1 : closeIdx])
	if err != nil {
		return nil, err
	}

	cmd := &AlterMergeTableCmd{TableName: tableName, NewColumns: *schema}

	afterCols := strings.TrimSpace(rest[closeIdx+1:])
	lowerAfter := strings.ToLower(afterCols)
	if strings.HasPrefix(lowerAfter, "with (") || strings.HasPrefix(lowerAfter, "with(") {
		wOpenIdx := strings.Index(afterCols, "(")
		wCloseIdx := findMatchingParen(afterCols, wOpenIdx)
		if wCloseIdx < 0 {
			return nil, fmt.Errorf("alter-merge table %q: unterminated with(...) clause", tableName)
		}
		for _, kv := range splitAndTrim(afterCols[wOpenIdx+1:wCloseIdx], ',') {
			eqIdx := strings.Index(kv, "=")
			if eqIdx < 0 {
				return nil, fmt.Errorf("alter-merge table %q: malformed property %q, expected name=value", tableName, kv)
			}
			key := strings.ToLower(strings.TrimSpace(kv[:eqIdx]))
			val := strings.TrimSpace(kv[eqIdx+1:])
			val = strings.Trim(val, `"'`)
			switch key {
			case "docstring":
				cmd.DocString = val
			case "folder":
				cmd.Folder = val
			default:
				return nil, fmt.Errorf("alter-merge table %q: unknown property %q", tableName, key)
			}
		}
	}

	return cmd, nil
}

func parseCreateTable(rest string, isMerge bool) (Statement, error) {
	rest = strings.TrimSpace(rest)

	// Find opening parenthesis
	parenIdx := strings.Index(rest, "(")
	if parenIdx < 0 {
		return nil, fmt.Errorf("expected (column definitions) after table name")
	}

	tableName := strings.TrimSpace(rest[:parenIdx])
	if tableName == "" {
		return nil, fmt.Errorf("expected table name")
	}

	// Find the column-defs' OWN closing parenthesis via matching, not
	// strings.LastIndex — a trailing with(...) clause (added for the
	// notimereceived property) has its own closing paren that would
	// otherwise be mistaken for the column list's, silently absorbing
	// "with (notimereceived=true)" into colDefs as a bogus extra
	// "column".
	closeIdx := findMatchingParen(rest, parenIdx)
	if closeIdx < 0 {
		return nil, fmt.Errorf("expected closing ) in column definitions")
	}

	colDefs := rest[parenIdx+1 : closeIdx]
	schema, err := parseColumnDefs(colDefs)
	if err != nil {
		return nil, err
	}

	// Optional trailing with (...) — currently only notimereceived,
	// but kept as a general key=value block (matching .create function
	// and .create materialized-view's own with(...) shape) rather than
	// a single dedicated flag, so a second table-level property later
	// doesn't need its own new syntax.
	noTimeReceived := false
	afterCols := strings.TrimSpace(rest[closeIdx+1:])
	lowerAfter := strings.ToLower(afterCols)
	if strings.HasPrefix(lowerAfter, "with (") || strings.HasPrefix(lowerAfter, "with(") {
		wOpenIdx := strings.Index(afterCols, "(")
		wCloseIdx := findMatchingParen(afterCols, wOpenIdx)
		if wCloseIdx < 0 {
			return nil, fmt.Errorf("create table %q: unterminated with(...) clause", tableName)
		}
		for _, kv := range splitAndTrim(afterCols[wOpenIdx+1:wCloseIdx], ',') {
			eqIdx := strings.Index(kv, "=")
			if eqIdx < 0 {
				return nil, fmt.Errorf("create table %q: malformed property %q, expected name=value", tableName, kv)
			}
			key := strings.ToLower(strings.TrimSpace(kv[:eqIdx]))
			val := strings.ToLower(strings.TrimSpace(kv[eqIdx+1:]))
			switch key {
			case "notimereceived":
				noTimeReceived = val == "true"
			default:
				return nil, fmt.Errorf("create table %q: unknown property %q", tableName, key)
			}
		}
	}

	if isMerge {
		return &CreateMergeTableCmd{TableName: tableName, Schema: *schema}, nil
	}
	return &CreateTableCmd{TableName: tableName, Schema: *schema, NoTimeReceived: noTimeReceived}, nil
}

// parseColumnDefs parses "col1: type1, col2: type2, ..."
func parseColumnDefs(s string) (*types.Schema, error) {
	parts := splitRespectingParens(s, ',')
	schema := &types.Schema{}

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		colonIdx := strings.Index(part, ":")
		if colonIdx < 0 {
			return nil, fmt.Errorf("expected 'name: type' in column definition, got %q", part)
		}

		name := strings.TrimSpace(part[:colonIdx])
		typStr := strings.TrimSpace(part[colonIdx+1:])

		typ, err := types.ParseType(typStr)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", name, err)
		}

		schema.Columns = append(schema.Columns, types.Column{Name: name, Type: typ})
	}

	if len(schema.Columns) == 0 {
		return nil, fmt.Errorf("no columns defined")
	}
	return schema, nil
}

// --- Query Parsing ---

func parseQuery(input string) (Statement, error) {
	// Split on pipe, respecting strings and parentheses
	segments := splitPipe(input)
	if len(segments) == 0 {
		return nil, fmt.Errorf("empty query")
	}

	// First segment is the table name
	source := strings.TrimSpace(segments[0])
	if source == "" {
		return nil, fmt.Errorf("expected table name")
	}

	// Standalone union: "union T1, T2, T3 | where ..."
	if strings.HasPrefix(strings.ToLower(source), "union ") {
		return parseStandaloneUnion(source[6:], segments[1:])
	}

	// print expr1, expr2, ...
	if strings.HasPrefix(strings.ToLower(source), "print ") {
		return parsePrintStatement(source[6:], segments[1:])
	}

	// search "term" or search in (T1, T2) "term"
	if strings.HasPrefix(strings.ToLower(source), "search ") {
		return parseSearchStatement(source[7:], segments[1:])
	}

	// find [withsource=X] [in (T1, T2)] where Predicate [project ...]
	// or find Predicate [project ...] (no "where" in this shorter form)
	if strings.HasPrefix(strings.ToLower(source), "find ") {
		return parseFindStatement(source[5:], segments[1:])
	}

	// datatable (Col1: type, Col2: type) [val1, val2, ...]
	if strings.HasPrefix(strings.ToLower(source), "datatable") {
		return parseDataTableStatement(source, segments[1:])
	}

	// range columnName from start to stop step step — verified against
	// real ADX's own range operator docs before adopting this shape.
	// Word boundary matches isTabularSource's own check just above in
	// this file: the next character after "range" must not be an
	// identifier character, so a real table literally named
	// "rangeEvents" isn't misparsed as this operator.
	if rangeLower := strings.ToLower(source); strings.HasPrefix(rangeLower, "range") &&
		(len(rangeLower) == len("range") || !isIdentChar(rangeLower[len("range")])) {
		return parseRangeStatement(source, segments[1:])
	}

	// Cross-database source: database('alias').TableName — see
	// DatabaseTableRef's doc comment for why this real-ADX-conformant
	// syntax was chosen. Checked before the table-valued-function case
	// below since both are prefix-recognized off the same source
	// string and don't overlap, but ordering them consistently avoids
	// ever having to reason about which check would "win" if that ever
	// changed.
	if dbRef := parseDatabaseTableRef(source); dbRef != nil {
		query := &Query{SourceDB: dbRef}
		for i := 1; i < len(segments); i++ {
			seg := strings.TrimSpace(segments[i])
			if seg == "" {
				continue
			}
			op, err := parseOperator(seg)
			if err != nil {
				return nil, fmt.Errorf("operator %d: %w", i, err)
			}
			query.Operators = append(query.Operators, op)
		}
		return query, nil
	}

	// Table-valued function: csv("path"), json("path"), ndjson("path"), parquet("path"), vortex("path")
	if tf := parseTableFunc(source); tf != nil {
		query := &Query{SourceFunc: tf}
		for i := 1; i < len(segments); i++ {
			seg := strings.TrimSpace(segments[i])
			if seg == "" {
				continue
			}
			op, err := parseOperator(seg)
			if err != nil {
				return nil, fmt.Errorf("operator %d: %w", i, err)
			}
			query.Operators = append(query.Operators, op)
		}
		return query, nil
	}

	// Stored (persisted, tabular) function call: FuncName() or
	// FuncName(arg1, arg2, ...) — checked AFTER the known
	// table-valued-function names above, deliberately, so a stored
	// function can never shadow a built-in name like csv()/json() even
	// if someone defines one with that name; the built-in always wins.
	// parseStoredFunctionCall itself distinguishes "not a call at all"
	// (nil, nil — falls through to the plain Source case below,
	// surfacing later as an ordinary "table not found") from "looked
	// like a call but the argument list itself doesn't parse" (a real
	// error, returned immediately rather than silently falling through
	// to a confusing, unrelated error).
	call, err := parseStoredFunctionCall(source)
	if err != nil {
		return nil, err
	}
	if call != nil {
		query := &Query{SourceFuncCall: call}
		for i := 1; i < len(segments); i++ {
			seg := strings.TrimSpace(segments[i])
			if seg == "" {
				continue
			}
			op, err := parseOperator(seg)
			if err != nil {
				return nil, fmt.Errorf("operator %d: %w", i, err)
			}
			query.Operators = append(query.Operators, op)
		}
		return query, nil
	}

	query := &Query{Source: source}

	// Remaining segments are operators
	for i := 1; i < len(segments); i++ {
		seg := strings.TrimSpace(segments[i])
		if seg == "" {
			continue
		}

		op, err := parseOperator(seg)
		if err != nil {
			return nil, fmt.Errorf("operator %d: %w", i, err)
		}
		query.Operators = append(query.Operators, op)
	}

	return query, nil
}

func parseOperator(seg string) (Operator, error) {
	lower := strings.ToLower(seg)

	// | count
	if lower == "count" {
		return &CountOp{}, nil
	}

	// | where <predicate>
	if strings.HasPrefix(lower, "where ") {
		predStr := seg[len("where "):]
		expr, err := ParseExpr(predStr)
		if err != nil {
			return nil, fmt.Errorf("where: %w", err)
		}
		return &WhereOp{Predicate: expr}, nil
	}

	// | getschema
	if lower == "getschema" || lower == "get-schema" {
		return &GetSchemaOp{}, nil
	}

	// | as Name — binds a name to the input tabular expression
	if strings.HasPrefix(lower, "as ") {
		name := strings.TrimSpace(seg[3:])
		if name == "" || !isValidIdentifier(name) {
			return nil, fmt.Errorf("as: expected a valid name, got %q", name)
		}
		return &AsOp{Name: name}, nil
	}

	// | scan [declare (...)] with (step Name [output=...] : Cond => Col=Expr[,...];)
	if strings.HasPrefix(lower, "scan ") || strings.HasPrefix(lower, "scan\t") {
		return parseScan(seg[len("scan "):])
	}

	// | invoke FunctionName(args...)
	if strings.HasPrefix(lower, "invoke ") {
		call, err := parseStoredFunctionCall(strings.TrimSpace(seg[len("invoke "):]))
		if err != nil {
			return nil, fmt.Errorf("invoke: %w", err)
		}
		if call == nil {
			return nil, fmt.Errorf("invoke: expected 'FunctionName(args...)'")
		}
		return &InvokeOp{Call: call}, nil
	}

	// | evaluate [evaluateParameters] PluginName(args...) [: (schema)]
	if strings.HasPrefix(lower, "evaluate ") {
		return parseEvaluate(seg[len("evaluate "):])
	}

	// | partition [hint.strategy=X] by Column (SubQuery)
	if strings.HasPrefix(lower, "partition ") {
		return parsePartition(seg[len("partition "):])
	}

	// | mv-apply [Name =] ArrayCol [to typeof(T)] on ( subquery )
	if strings.HasPrefix(lower, "mv-apply ") || strings.HasPrefix(lower, "mvapply ") {
		var rest string
		if strings.HasPrefix(lower, "mv-apply ") {
			rest = seg[len("mv-apply "):]
		} else {
			rest = seg[len("mvapply "):]
		}
		return parseMvApply(rest)
	}

	// | make-graph Source --> Target [with Nodes on NodeId]
	if strings.HasPrefix(lower, "make-graph ") {
		return parseMakeGraph(seg[len("make-graph "):])
	}

	// | graph-to-table [nodes|edges]  (default: edges)
	if lower == "graph-to-table" || strings.HasPrefix(lower, "graph-to-table ") {
		rest := strings.ToLower(strings.TrimSpace(seg[len("graph-to-table"):]))
		switch rest {
		case "", "edges":
			return &GraphToTableOp{Output: "edges"}, nil
		case "nodes":
			return &GraphToTableOp{Output: "nodes"}, nil
		default:
			return nil, fmt.Errorf("graph-to-table: expected 'nodes' or 'edges', got %q", rest)
		}
	}

	// | graph-match (a)-[e]->(b) [where <expr>] project <items>
	if strings.HasPrefix(lower, "graph-match ") {
		return parseGraphMatch(seg[len("graph-match "):])
	}

	// | render <visualization> [with (...)]  — metadata only, engine pass-through
	if strings.HasPrefix(lower, "render ") || lower == "render" {
		rest := strings.TrimSpace(seg[len("render"):])
		if rest == "" {
			return nil, fmt.Errorf("render: expected visualization name")
		}
		op := &RenderOp{}
		withIdx := findKeyword(rest, " with ")
		if withIdx >= 0 {
			op.Visualization = strings.TrimSpace(rest[:withIdx])
			withPart := strings.TrimSpace(rest[withIdx+len(" with "):])
			// Strip surrounding parens if present
			if strings.HasPrefix(withPart, "(") && strings.HasSuffix(withPart, ")") {
				withPart = strings.TrimSpace(withPart[1 : len(withPart)-1])
			}
			op.With = withPart
		} else {
			op.Visualization = rest
		}
		if op.Visualization == "" {
			return nil, fmt.Errorf("render: expected visualization name")
		}
		return op, nil
	}

	// | make-series [kind=nonempty] Agg[, ...] on AxisCol [from X] [to Y] step S [by ...]
	if strings.HasPrefix(lower, "make-series ") {
		return parseMakeSeries(seg[len("make-series "):])
	}

	// | parse-where [kind=simple|regex|relaxed] Column with Pattern...
	// Checked before "parse " below since "parse-where " does not start
	// with "parse " (the hyphen means the prefix check for plain parse
	// would never match it anyway, but ordering it first keeps the
	// relationship between the two obvious to a future reader).
	if strings.HasPrefix(lower, "parse-where ") {
		return parseParseWhereOperator(seg[len("parse-where "):])
	}

	// | parse-kv Expression as ( KeysList ) with ( pair_delimiter=..., kv_delimiter=... )
	if strings.HasPrefix(lower, "parse-kv ") {
		return parseParseKVOperator(seg[len("parse-kv "):])
	}

	// | parse [kind=simple|regex|relaxed] Column with Pattern...
	if strings.HasPrefix(lower, "parse ") {
		return parseParseOperator(seg[6:])
	}

	// | project-away col1, col2, ...
	if strings.HasPrefix(lower, "project-away ") || strings.HasPrefix(lower, "projectaway ") {
		var colStr string
		if strings.HasPrefix(lower, "project-away ") {
			colStr = seg[len("project-away "):]
		} else {
			colStr = seg[len("projectaway "):]
		}
		cols := splitAndTrim(colStr, ',')
		if len(cols) == 0 {
			return nil, fmt.Errorf("project-away: no columns specified")
		}
		return &ProjectAwayOp{Columns: cols}, nil
	}

	// | project-rename NewName = OldName, ...
	if strings.HasPrefix(lower, "project-rename ") || strings.HasPrefix(lower, "projectrename ") {
		var renStr string
		if strings.HasPrefix(lower, "project-rename ") {
			renStr = seg[len("project-rename "):]
		} else {
			renStr = seg[len("projectrename "):]
		}
		parts := splitAndTrim(renStr, ',')
		var renames []RenameSpec
		for _, part := range parts {
			eqIdx := strings.Index(part, "=")
			if eqIdx < 0 {
				return nil, fmt.Errorf("project-rename: expected 'NewName = OldName', got %q", part)
			}
			newName := strings.TrimSpace(part[:eqIdx])
			oldName := strings.TrimSpace(part[eqIdx+1:])
			renames = append(renames, RenameSpec{NewName: newName, OldName: oldName})
		}
		return &ProjectRenameOp{Renames: renames}, nil
	}

	// | project-reorder col1, col2, ...
	if strings.HasPrefix(lower, "project-reorder ") || strings.HasPrefix(lower, "projectreorder ") {
		var colStr string
		if strings.HasPrefix(lower, "project-reorder ") {
			colStr = seg[len("project-reorder "):]
		} else {
			colStr = seg[len("projectreorder "):]
		}
		cols := splitAndTrim(colStr, ',')
		if len(cols) == 0 {
			return nil, fmt.Errorf("project-reorder: no columns specified")
		}
		return &ProjectReorderOp{Columns: cols}, nil
	}

	// | project-keep col1, col2, ... (supports wildcards like col*)
	if strings.HasPrefix(lower, "project-keep ") || strings.HasPrefix(lower, "projectkeep ") {
		var colStr string
		if strings.HasPrefix(lower, "project-keep ") {
			colStr = seg[len("project-keep "):]
		} else {
			colStr = seg[len("projectkeep "):]
		}
		patterns := splitAndTrim(colStr, ',')
		if len(patterns) == 0 {
			return nil, fmt.Errorf("project-keep: no columns specified")
		}
		return &ProjectKeepOp{Patterns: patterns}, nil
	}

	// | project-by-names ColumnSpecifier[, ...]
	if strings.HasPrefix(lower, "project-by-names ") {
		return parseProjectByNames(seg[len("project-by-names "):])
	}

	// | project col1, NewCol = expr, ...
	if strings.HasPrefix(lower, "project ") {
		items, err := parseProjectItems(seg[len("project "):])
		if err != nil {
			return nil, fmt.Errorf("project: %w", err)
		}
		return &ProjectOp{Items: items}, nil
	}

	// | extend Name = expr, ...
	if strings.HasPrefix(lower, "extend ") {
		assignStr := seg[len("extend "):]
		assignments, err := parseAssignments(assignStr)
		if err != nil {
			return nil, fmt.Errorf("extend: %w", err)
		}
		return &ExtendOp{Assignments: assignments}, nil
	}

	// | take N / | limit N
	if strings.HasPrefix(lower, "take ") || strings.HasPrefix(lower, "limit ") {
		var numStr string
		if strings.HasPrefix(lower, "take ") {
			numStr = strings.TrimSpace(seg[len("take "):])
		} else {
			numStr = strings.TrimSpace(seg[len("limit "):])
		}
		n, err := strconv.ParseInt(numStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("take: expected number, got %q", numStr)
		}
		return &TakeOp{Count: n}, nil
	}

	// | sample N
	if strings.HasPrefix(lower, "sample ") {
		numStr := strings.TrimSpace(seg[len("sample "):])
		n, err := strconv.ParseInt(numStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("sample: expected number, got %q", numStr)
		}
		return &SampleOp{Count: n}, nil
	}

	// | serialize [col1 = expr, ...]
	if lower == "serialize" || strings.HasPrefix(lower, "serialize ") {
		rest := ""
		if strings.HasPrefix(lower, "serialize ") {
			rest = strings.TrimSpace(seg[len("serialize "):])
		}
		var cols []Assignment
		if rest != "" {
			var err error
			cols, err = parseAssignments(rest)
			if err != nil {
				return nil, fmt.Errorf("serialize: %w", err)
			}
		}
		return &SerializeOp{Columns: cols}, nil
	}

	// | distinct col1, col2, ...
	if strings.HasPrefix(lower, "distinct ") {
		colStr := seg[len("distinct "):]
		cols := splitAndTrim(colStr, ',')
		return &DistinctOp{Columns: cols}, nil
	}

	// | sample-distinct NumberOfValues of ColumnName
	if strings.HasPrefix(lower, "sample-distinct ") {
		return parseSampleDistinct(seg[len("sample-distinct "):])
	}

	// | top N by col [asc|desc]
	if strings.HasPrefix(lower, "top ") {
		return parseTop(seg[len("top "):])
	}

	// | order by col [asc|desc], ... / | sort by ...
	if strings.HasPrefix(lower, "order by ") || strings.HasPrefix(lower, "sort by ") {
		var clauseStr string
		if strings.HasPrefix(lower, "order by ") {
			clauseStr = seg[len("order by "):]
		} else {
			clauseStr = seg[len("sort by "):]
		}
		return parseOrderBy(clauseStr)
	}

	// | summarize agg() [by col, ...]
	if strings.HasPrefix(lower, "summarize ") {
		return parseSummarize(seg[len("summarize "):])
	}

	// | join kind=X (subquery) on col1[, col2]
	if strings.HasPrefix(lower, "join ") {
		return parseJoin(seg[len("join "):])
	}

	// | lookup [kind=X] TableName on col1[, col2]
	if strings.HasPrefix(lower, "lookup ") {
		return parseLookup(seg[len("lookup "):])
	}

	// | mv-expand Col or | mv-expand NewName = Expr, ...
	if strings.HasPrefix(lower, "mv-expand ") || strings.HasPrefix(lower, "mvexpand ") {
		var rest string
		if strings.HasPrefix(lower, "mv-expand ") {
			rest = seg[len("mv-expand "):]
		} else {
			rest = seg[len("mvexpand "):]
		}
		return parseMvExpand(rest)
	}

	// | union T2, T3 or | union (T2 | where ...), (T3 | where ...)
	if strings.HasPrefix(lower, "union ") || lower == "union" {
		rest := ""
		if len(seg) > 6 {
			rest = strings.TrimSpace(seg[6:])
		}
		return parseUnion(rest)
	}

	return nil, fmt.Errorf("unknown operator: %q", seg)
}

// parseUnion parses the union operator arguments: table names and subqueries.
// Formats: "T2, T3" or "(T2 | where ...), T3"
func parseUnion(s string) (*UnionOp, error) {
	sources, err := parseUnionSources(s)
	if err != nil {
		return nil, fmt.Errorf("union: %w", err)
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("union: no tables specified")
	}
	return &UnionOp{Sources: sources}, nil
}

// parseStandaloneUnion handles "union T1, T2, T3 | where ..."
// Rewrites as Query{Source: T1, Operators: [UnionOp{T2, T3}, remaining...]}
func parseStandaloneUnion(sourcesPart string, remainingSegments []string) (Statement, error) {
	sources, err := parseUnionSources(strings.TrimSpace(sourcesPart))
	if err != nil {
		return nil, fmt.Errorf("union: %w", err)
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("union: no tables specified")
	}

	// First source becomes the query source
	firstSource := sources[0]
	if len(firstSource.Operators) > 0 {
		return nil, fmt.Errorf("union: first source in standalone union cannot have operators (use pipe form)")
	}

	var operators []Operator
	// If more than one source, prepend a union op for the rest
	if len(sources) > 1 {
		operators = append(operators, &UnionOp{Sources: sources[1:]})
	}

	// Parse remaining pipe operators
	for i, seg := range remainingSegments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		op, err := parseOperator(seg)
		if err != nil {
			return nil, fmt.Errorf("operator %d: %w", i+1, err)
		}
		operators = append(operators, op)
	}

	return &Query{
		Source:    firstSource.Source,
		Operators: operators,
	}, nil
}

// parseUnionSources parses comma-separated table names and subqueries.
// Each source can be: TableName or (TableName | operators...)
func parseUnionSources(s string) ([]*Query, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}

	var sources []*Query
	// Split on commas at depth 0 (respecting parentheses)
	parts := splitRespectingParens(s, ',')

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Subquery in parentheses: (Table | where ...)
		if part[0] == '(' && part[len(part)-1] == ')' {
			inner := strings.TrimSpace(part[1 : len(part)-1])
			stmt, err := parseQuery(inner)
			if err != nil {
				return nil, fmt.Errorf("subquery %q: %w", inner, err)
			}
			q, ok := stmt.(*Query)
			if !ok {
				return nil, fmt.Errorf("subquery must be a tabular query")
			}
			sources = append(sources, q)
		} else {
			// Plain table name
			sources = append(sources, &Query{Source: part})
		}
	}
	return sources, nil
}

// parseMvExpand parses mv-expand column specifications.
// Formats: "Col", "NewName = Col", "Col1, Col2", "NewName = Expr"
func parseMvExpand(s string) (*MvExpandOp, error) {
	parts := splitRespectingParens(s, ',')
	if len(parts) == 0 {
		return nil, fmt.Errorf("mv-expand: no columns specified")
	}

	var columns []MvExpandColumn
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Check for "Name = Expr" form
		eqIdx := strings.Index(part, "=")
		if eqIdx > 0 && (eqIdx+1 >= len(part) || part[eqIdx+1] != '=') {
			// Ensure it's not == comparison
			name := strings.TrimSpace(part[:eqIdx])
			exprStr := strings.TrimSpace(part[eqIdx+1:])
			expr, err := ParseExpr(exprStr)
			if err != nil {
				return nil, fmt.Errorf("mv-expand %q: %w", name, err)
			}
			columns = append(columns, MvExpandColumn{Name: name, Source: expr})
		} else {
			// Plain column name
			expr, err := ParseExpr(part)
			if err != nil {
				return nil, fmt.Errorf("mv-expand %q: %w", part, err)
			}
			// Derive name from expression
			name := part
			if ref, ok := expr.(*ColumnRef); ok {
				name = ref.Name
			}
			columns = append(columns, MvExpandColumn{Name: name, Source: expr})
		}
	}

	return &MvExpandOp{Columns: columns}, nil
}

// parseMvApply parses: [Name =] ArrayCol [to typeof(T)] on ( op1 | op2 | ... )
// The parenthesized subquery is a sourceless operator pipeline that runs
// against the per-row expanded table.
func parseMvApply(s string) (*MvApplyOp, error) {
	s = strings.TrimSpace(s)
	op := &MvApplyOp{ElementType: types.TypeDynamic}

	// Locate ' on ' at depth 0 (the subquery follows)
	onIdx := findKeyword(s, " on ")
	if onIdx < 0 {
		return nil, fmt.Errorf("mv-apply: expected 'on ( subquery )'")
	}
	head := strings.TrimSpace(s[:onIdx])
	rest := strings.TrimSpace(s[onIdx+len(" on "):])

	// Optional 'to typeof(T)' at the end of head
	if toIdx := findKeyword(head, " to "); toIdx >= 0 {
		typePart := strings.TrimSpace(head[toIdx+len(" to "):])
		lower := strings.ToLower(typePart)
		if !strings.HasPrefix(lower, "typeof(") || !strings.HasSuffix(typePart, ")") {
			return nil, fmt.Errorf("mv-apply: expected 'to typeof(type)', got %q", typePart)
		}
		typeName := strings.TrimSpace(typePart[len("typeof(") : len(typePart)-1])
		t, err := types.ParseType(typeName)
		if err != nil {
			return nil, fmt.Errorf("mv-apply: %w", err)
		}
		op.ElementType = t
		head = strings.TrimSpace(head[:toIdx])
	}

	// [Name =] ArrayCol
	if eqIdx := assignmentEqIndex(head); eqIdx >= 0 {
		op.Name = strings.TrimSpace(head[:eqIdx])
		op.SourceCol = strings.TrimSpace(head[eqIdx+1:])
	} else {
		op.Name = head
		op.SourceCol = head
	}
	if op.Name == "" || op.SourceCol == "" {
		return nil, fmt.Errorf("mv-apply: expected array column before 'on'")
	}

	// ( subquery )
	if len(rest) == 0 || rest[0] != '(' {
		return nil, fmt.Errorf("mv-apply: expected '(' after 'on'")
	}
	depth := 0
	closeIdx := -1
	for i := 0; i < len(rest); i++ {
		if rest[i] == '(' {
			depth++
		} else if rest[i] == ')' {
			depth--
			if depth == 0 {
				closeIdx = i
				break
			}
		}
	}
	if closeIdx < 0 {
		return nil, fmt.Errorf("mv-apply: unmatched parenthesis in subquery")
	}
	if trailing := strings.TrimSpace(rest[closeIdx+1:]); trailing != "" {
		return nil, fmt.Errorf("mv-apply: unexpected trailing content %q", trailing)
	}
	inner := strings.TrimSpace(rest[1:closeIdx])
	if inner == "" {
		return nil, fmt.Errorf("mv-apply: empty subquery")
	}

	// Sourceless operator pipeline: split on pipes, parse each segment
	for _, segPart := range splitPipe(inner) {
		segPart = strings.TrimSpace(segPart)
		if segPart == "" {
			continue
		}
		subOp, err := parseOperator(segPart)
		if err != nil {
			return nil, fmt.Errorf("mv-apply subquery: %w", err)
		}
		op.Operators = append(op.Operators, subOp)
	}
	if len(op.Operators) == 0 {
		return nil, fmt.Errorf("mv-apply: empty subquery")
	}
	return op, nil
}

// parsePartition parses "[hint.strategy=X] [hint.other=Y] by Column
// (SubQuery)" — verified against real ADX's own partition operator
// docs before adopting this shape. See PartitionOp's own doc comment
// (ast.go) for exactly what real-ADX partition capability is
// deliberately out of scope here (the braces "{SubQueryWithSource}"
// legacy-only form) and why — a query using that form gets a clear,
// explicit error here, not silent mis-parsing into something else.
func parsePartition(s string) (Operator, error) {
	s = strings.TrimSpace(s)

	// Skip any leading "hint.xxx=yyy" (or the equally valid, spaced
	// "hint.xxx = yyy" — confirmed as real, valid ADX syntax by one of
	// the docs' own worked examples using exactly that spacing, not
	// assumed) tokens — see PartitionOp's own doc comment for why
	// these are recognized and silently ignored rather than rejected
	// or acted on.
	for {
		fields := strings.Fields(s)
		if len(fields) == 0 {
			return nil, fmt.Errorf("partition: expected 'by Column (SubQuery)'")
		}
		if !strings.HasPrefix(strings.ToLower(fields[0]), "hint.") {
			break
		}
		// Consume "hint.xxx", then an optional "=" and its value,
		// whether or not either is separated by whitespace from what
		// precedes it.
		s = strings.TrimSpace(s[len(fields[0]):])
		if strings.HasPrefix(s, "=") {
			s = strings.TrimSpace(s[1:])
			valFields := strings.Fields(s)
			if len(valFields) > 0 {
				s = strings.TrimSpace(s[len(valFields[0]):])
			}
		}
	}

	if !strings.HasPrefix(strings.ToLower(s), "by ") {
		return nil, fmt.Errorf("partition: expected 'by Column (SubQuery)', got %q", s)
	}
	s = strings.TrimSpace(s[len("by "):])

	openIdx := strings.IndexAny(s, "({")
	if openIdx < 0 {
		return nil, fmt.Errorf("partition: expected '(' after the partition column")
	}
	byColumn := strings.TrimSpace(s[:openIdx])
	if byColumn == "" || !isValidIdentifier(byColumn) {
		return nil, fmt.Errorf("partition: expected a valid column name, got %q", byColumn)
	}

	if s[openIdx] == '{' {
		return nil, fmt.Errorf("partition: the '{SubQueryWithSource}' explicit-source form (legacy strategy only in real ADX) isn't supported — use the '(SubQuery)' implicit-source form instead, which covers native, shuffle, and legacy strategies alike here")
	}

	closeIdx := findMatchingParen(s, openIdx)
	if closeIdx < 0 {
		return nil, fmt.Errorf("partition %s: unmatched '(' in subquery", byColumn)
	}
	if trailing := strings.TrimSpace(s[closeIdx+1:]); trailing != "" {
		return nil, fmt.Errorf("partition %s: unexpected trailing content %q", byColumn, trailing)
	}
	inner := strings.TrimSpace(s[openIdx+1 : closeIdx])
	if inner == "" {
		return nil, fmt.Errorf("partition %s: empty subquery", byColumn)
	}

	op := &PartitionOp{ByColumn: byColumn}
	for _, segPart := range splitPipe(inner) {
		segPart = strings.TrimSpace(segPart)
		if segPart == "" {
			continue
		}
		subOp, err := parseOperator(segPart)
		if err != nil {
			return nil, fmt.Errorf("partition %s subquery: %w", byColumn, err)
		}
		op.Operators = append(op.Operators, subOp)
	}
	if len(op.Operators) == 0 {
		return nil, fmt.Errorf("partition %s: empty subquery", byColumn)
	}
	return op, nil
}

// parsePrintStatement parses "print Name1 = Expr1, Name2 = Expr2, ..."
// and any subsequent pipeline operators.
func parsePrintStatement(s string, remaining []string) (Statement, error) {
	assignments, err := parseAssignments(s)
	if err != nil {
		return nil, fmt.Errorf("print: %w", err)
	}
	query := &Query{
		Source:    "",
		Operators: []Operator{&PrintOp{Expressions: assignments}},
	}
	for i, seg := range remaining {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		op, err := parseOperator(seg)
		if err != nil {
			return nil, fmt.Errorf("operator %d: %w", i+1, err)
		}
		query.Operators = append(query.Operators, op)
	}
	return query, nil
}

// parseDataTableStatement parses: datatable (Col1: type, ...) [val1, val2, ...]
func parseDataTableStatement(source string, remaining []string) (Statement, error) {
	// Strip "datatable" prefix (case-insensitive)
	s := strings.TrimSpace(source[len("datatable"):])

	// Find schema between ( and )
	schemaStart := strings.IndexByte(s, '(')
	if schemaStart < 0 {
		return nil, fmt.Errorf("datatable: expected '(' after datatable")
	}
	schemaEnd := findMatchingParen(s, schemaStart)
	if schemaEnd < 0 {
		return nil, fmt.Errorf("datatable: unmatched '('")
	}
	schemaPart := s[schemaStart+1 : schemaEnd]

	// Parse column definitions: "Name: string, Age: long"
	var schema types.Schema
	for _, colDef := range splitRespectingParens(schemaPart, ',') {
		colDef = strings.TrimSpace(colDef)
		if colDef == "" {
			continue
		}
		parts := strings.SplitN(colDef, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("datatable: expected 'ColName: type', got %q", colDef)
		}
		name := strings.TrimSpace(parts[0])
		typeName := strings.TrimSpace(parts[1])
		kt, err := types.ParseType(typeName)
		if err != nil {
			return nil, fmt.Errorf("datatable: unknown type %q for column %q", typeName, name)
		}
		schema.Columns = append(schema.Columns, types.Column{Name: name, Type: kt})
	}

	if len(schema.Columns) == 0 {
		return nil, fmt.Errorf("datatable: no columns defined")
	}

	// Find values between [ and ]
	rest := strings.TrimSpace(s[schemaEnd+1:])
	valStart := strings.IndexByte(rest, '[')
	if valStart < 0 {
		return nil, fmt.Errorf("datatable: expected '[' for values")
	}
	valEnd := findMatchingBracket(rest, valStart)
	if valEnd < 0 {
		return nil, fmt.Errorf("datatable: unmatched '['")
	}
	valuesPart := rest[valStart+1 : valEnd]

	// Split values respecting strings
	values := splitDataTableValues(valuesPart)

	dt := &DataTableOp{Schema: schema, Values: values}
	query := &Query{
		Source:    "",
		Operators: []Operator{dt},
	}

	for i, seg := range remaining {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		op, err := parseOperator(seg)
		if err != nil {
			return nil, fmt.Errorf("operator %d: %w", i+1, err)
		}
		query.Operators = append(query.Operators, op)
	}
	return query, nil
}

// findTopLevelKeyword finds the start index of the first standalone
// occurrence of keyword in s, respecting parens/brackets and quoted
// strings (so a paren-nested or quoted "to"/"step" — e.g. inside a
// string literal or a nested function call argument — is never
// mistaken for the range operator's own from/to/step separators), and
// requiring word boundaries on both sides (so "tomorrow" doesn't match
// a search for "to"). Returns -1 if not found.
func findTopLevelKeyword(s, keyword string) int {
	lower := strings.ToLower(s)
	kw := strings.ToLower(keyword)
	depth := 0
	inStr := byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inStr != 0 {
			if ch == inStr && !precededByOddBackslashes(s, i) {
				inStr = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inStr = ch
			continue
		}
		if ch == '(' || ch == '[' {
			depth++
		}
		if (ch == ')' || ch == ']') && depth > 0 {
			depth--
		}
		if depth != 0 {
			continue
		}
		if i+len(kw) <= len(lower) && lower[i:i+len(kw)] == kw {
			leftOK := i == 0 || !isIdentChar(s[i-1])
			rightIdx := i + len(kw)
			rightOK := rightIdx == len(s) || !isIdentChar(s[rightIdx])
			if leftOK && rightOK {
				return i
			}
		}
	}
	return -1
}

// parseRangeStatement parses "range columnName from start to stop step
// step" and any subsequent pipeline operators — verified against real
// ADX's own range operator docs before adopting this shape: "This
// operator doesn't take a tabular input... The values can't reference
// the columns of any table." Start/stop/step are parsed as ordinary
// scalar expressions (via ParseExpr), not restricted to bare numeric
// literals, matching real ADX's own documented example computing them
// from function calls (range LastWeek from ago(7d) to now() step 1d).
func parseRangeStatement(source string, remaining []string) (Statement, error) {
	s := strings.TrimSpace(source[len("range"):])

	fromIdx := findTopLevelKeyword(s, "from")
	if fromIdx < 0 {
		return nil, fmt.Errorf("range: expected 'columnName from start to stop step step'")
	}
	colName := strings.TrimSpace(s[:fromIdx])
	if colName == "" || !isValidIdentifier(colName) {
		return nil, fmt.Errorf("range: expected a column name before 'from', got %q", colName)
	}
	rest := s[fromIdx+len("from"):]

	toIdx := findTopLevelKeyword(rest, "to")
	if toIdx < 0 {
		return nil, fmt.Errorf("range %s: expected 'to stop' after 'from start'", colName)
	}
	startText := strings.TrimSpace(rest[:toIdx])
	rest = rest[toIdx+len("to"):]

	stepIdx := findTopLevelKeyword(rest, "step")
	if stepIdx < 0 {
		return nil, fmt.Errorf("range %s: expected 'step step' after 'to stop'", colName)
	}
	stopText := strings.TrimSpace(rest[:stepIdx])
	stepText := strings.TrimSpace(rest[stepIdx+len("step"):])

	if startText == "" || stopText == "" || stepText == "" {
		return nil, fmt.Errorf("range %s: start, stop, and step must all be non-empty expressions", colName)
	}

	start, err := ParseExpr(startText)
	if err != nil {
		return nil, fmt.Errorf("range %s: start %q: %w", colName, startText, err)
	}
	stop, err := ParseExpr(stopText)
	if err != nil {
		return nil, fmt.Errorf("range %s: stop %q: %w", colName, stopText, err)
	}
	step, err := ParseExpr(stepText)
	if err != nil {
		return nil, fmt.Errorf("range %s: step %q: %w", colName, stepText, err)
	}

	query := &Query{
		Source:    "",
		Operators: []Operator{&RangeOp{ColumnName: colName, Start: start, Stop: stop, Step: step}},
	}

	for i, seg := range remaining {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		op, err := parseOperator(seg)
		if err != nil {
			return nil, fmt.Errorf("operator %d: %w", i+1, err)
		}
		query.Operators = append(query.Operators, op)
	}
	return query, nil
}

// parseEvaluate parses "[evaluateParameters] PluginName(args...)
// [: (schema)]" — verified against real ADX's own evaluate operator
// docs before adopting this shape.
//
// evaluateParameters (hint.distribution=..., hint.num_partitions=...,
// real ADX's own distribution hints for a distributed query planner)
// are recognized and silently skipped, not rejected: this engine has
// no distributed execution to hint at all, so a query written for a
// real ADX cluster that happens to include one of these still parses
// and runs correctly here, just without any distribution behavior to
// change (which is the only correct outcome for a single-node
// engine — erroring on a hint that's genuinely meaningless here would
// be pedantic, not helpful).
func parseEvaluate(s string) (Operator, error) {
	s = strings.TrimSpace(s)

	// Skip any leading "hint.xxx=yyy" tokens.
	for {
		fields := strings.Fields(s)
		if len(fields) == 0 {
			return nil, fmt.Errorf("evaluate: expected a plugin name")
		}
		if strings.HasPrefix(strings.ToLower(fields[0]), "hint.") {
			s = strings.TrimSpace(s[len(fields[0]):])
			continue
		}
		break
	}

	openIdx := strings.Index(s, "(")
	if openIdx < 0 {
		return nil, fmt.Errorf("evaluate: expected 'PluginName(args...)'")
	}
	pluginName := strings.TrimSpace(s[:openIdx])
	if pluginName == "" || !isValidIdentifier(pluginName) {
		return nil, fmt.Errorf("evaluate: expected a valid plugin name, got %q", pluginName)
	}

	closeIdx := findMatchingParen(s, openIdx)
	if closeIdx < 0 {
		return nil, fmt.Errorf("evaluate %s(...): unterminated argument list", pluginName)
	}
	inner := strings.TrimSpace(s[openIdx+1 : closeIdx])

	op := &EvaluateOp{PluginName: pluginName}
	if inner != "" {
		for _, part := range splitRespectingParens(inner, ',') {
			part = strings.TrimSpace(part)
			if part == "" {
				return nil, fmt.Errorf("evaluate %s(...): empty argument in call", pluginName)
			}
			op.ArgTexts = append(op.ArgTexts, part)
		}
	}

	rest := strings.TrimSpace(s[closeIdx+1:])
	if strings.HasPrefix(rest, ":") {
		schemaText := strings.TrimSpace(rest[1:])
		if !strings.HasPrefix(schemaText, "(") || !strings.HasSuffix(schemaText, ")") {
			return nil, fmt.Errorf("evaluate %s: expected '(Name: type, ...)' after ':', got %q", pluginName, schemaText)
		}
		schema, err := parseColumnDefs(schemaText[1 : len(schemaText)-1])
		if err != nil {
			return nil, fmt.Errorf("evaluate %s: output schema: %w", pluginName, err)
		}
		op.OutputSchema = schema
	} else if rest != "" {
		return nil, fmt.Errorf("evaluate %s: unexpected trailing text %q", pluginName, rest)
	}

	return op, nil
}

// findMatchingParen finds the closing ')' matching the '(' at position start.
func findMatchingParen(s string, start int) int {
	depth := 0
	inStr := false
	for i := start; i < len(s); i++ {
		if s[i] == '"' && !precededByOddBackslashes(s, i) {
			inStr = !inStr
		}
		if inStr {
			continue
		}
		if s[i] == '(' {
			depth++
		} else if s[i] == ')' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// findMatchingBracket finds the closing ']' matching the '[' at position start.
func findMatchingBracket(s string, start int) int {
	depth := 0
	inStr := false
	for i := start; i < len(s); i++ {
		if s[i] == '"' && !precededByOddBackslashes(s, i) {
			inStr = !inStr
		}
		if inStr {
			continue
		}
		if s[i] == '[' {
			depth++
		} else if s[i] == ']' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitDataTableValues splits a comma-separated value list, respecting quoted strings.
func splitDataTableValues(s string) []string {
	// bracketDepth tracks [...] nesting so a bracketed literal — a JSON
	// array value for a dynamic-typed column, e.g. ["x", "y"] — is kept
	// as ONE value token, not split on its internal comma into two.
	//
	// Found live, not hypothetical: without this, datatable(Tags:
	// dynamic) ["a", ["x", "y"], "b", ["z"]] silently misaligned every
	// value from that point on — the comma inside ["x", "y"] split it
	// into two flat tokens ("["x"" and ""y"]"), producing 3 rows
	// instead of 2 with values shifted and mangled (a row with
	// Id=`"y"]` — the literal closing bracket becoming part of an Id),
	// no error anywhere. datatable's OWN operator is genuinely flat in
	// real ADX too (values assigned round-robin to columns, no nested
	// row structure) — but a single dynamic-typed CELL holding a JSON
	// array is a different thing entirely from a nested ROW, and
	// TypeDynamic is already stored as raw JSON text everywhere else in
	// this codebase (embedding vectors are the existing example) — so
	// treating a bracketed value as one token is completing existing
	// behavior, not inventing new syntax.
	// bracketDepth ALSO tracks {...} and (...) nesting, not just [...]
	// as an earlier version of this function did — found live, not
	// hypothetical, the same class of bug already found and fixed once
	// this session in a DIFFERENT function (splitStatements not
	// tracking brace depth): datatable(d:dynamic)
	// [dynamic({"Name": "John", "Age":20})] silently misaligned every
	// value the exact same way the bracket-only fix's own doc comment
	// above describes for [...] — the comma INSIDE the JSON object
	// split it into two flat, truncated, invalid-JSON tokens
	// (`dynamic({"Name": "John"` and `"Age":20})`), no error anywhere,
	// only surfacing downstream as a plugin (evaluate bag_unpack)
	// silently seeing malformed dynamic values and producing an empty
	// result with zero columns. {}/() nesting shares the SAME depth
	// counter as []: for deciding whether a comma here is a genuine
	// value separator, which bracket kind is nesting doesn't matter,
	// only whether nesting is happening at all — the identical
	// reasoning already applied to splitStatements' own fix.
	var values []string
	var current strings.Builder
	inStr := false
	bracketDepth := 0
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' && !precededByOddBackslashes(s, i) {
			inStr = !inStr
			current.WriteByte(ch)
			continue
		}
		if !inStr {
			if ch == '[' || ch == '{' || ch == '(' {
				bracketDepth++
			} else if (ch == ']' || ch == '}' || ch == ')') && bracketDepth > 0 {
				bracketDepth--
			}
		}
		if ch == ',' && !inStr && bracketDepth == 0 {
			v := strings.TrimSpace(current.String())
			if v != "" {
				values = append(values, v)
			}
			current.Reset()
			continue
		}
		current.WriteByte(ch)
	}
	if v := strings.TrimSpace(current.String()); v != "" {
		values = append(values, v)
	}
	return values
}

// parseTop parses "N by Expression [asc|desc]". Real ADX's own top
// operator only ever supports a SINGLE ranking Expression — verified
// directly against Microsoft's own docs before fixing what was here,
// which included a stated equivalence not to be missed: "top 5 by
// name is equivalent to the expression sort by name | take 5 both
// from semantic and performance perspectives." No multi-key form
// exists in real ADX at all (top-nested is the separate, dedicated
// operator for hierarchical ranking needs).
//
// An earlier version of this function used strings.Fields (a bare
// whitespace split) and silently took only parts[2] as the column,
// with no check for anything unexpected following it at all — so
// `top 1 by X desc, Y desc` neither errored nor behaved as a genuine
// two-key sort; it silently parsed as top 1 by X, with "desc," (the
// trailing comma) failing the exact asc/desc string match and falling
// through to top's own default (desc) with no error or warning
// anywhere. Found live via a different model's testing (Kimi), on
// real data: the silently-dropped second key meant top disagreed with
// the semantically equivalent sort by | take on tied first-key rows,
// returning an effectively arbitrary row instead of the documented,
// correct tie-break.
//
// Fixed by explicitly detecting and rejecting a comma in the by
// clause with a clear, actionable error — matching real ADX's own
// genuine restriction with an honest failure instead of silent
// mis-parsing, and pointing at the two real, correct alternatives:
// combine multiple criteria into one ranking expression (e.g.
// coalesce(a, b)), or use sort by (which DOES support multiple keys)
// followed by take.
func parseTop(s string) (*TopOp, error) {
	s = strings.TrimSpace(s)

	// "N by col [asc|desc]"
	parts := strings.Fields(s)
	if len(parts) < 3 || strings.ToLower(parts[1]) != "by" {
		return nil, fmt.Errorf("expected 'top N by column [asc|desc]'")
	}

	n, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("top: expected number, got %q", parts[0])
	}

	if strings.Contains(s, ",") {
		return nil, fmt.Errorf("top: only a single ranking expression is supported (matching real ADX's own restriction) — combine multiple criteria into one expression (e.g. coalesce(a, b)), or use 'sort by col1, col2 | take N' instead, which supports multiple keys")
	}

	col := parts[2]
	desc := true // KQL default for top is desc
	if len(parts) > 3 {
		switch strings.ToLower(parts[3]) {
		case "asc":
			desc = false
		case "desc":
			desc = true
		default:
			return nil, fmt.Errorf("top: expected 'asc' or 'desc', got %q", parts[3])
		}
	}

	return &TopOp{Count: n, By: col, Desc: desc}, nil
}

func parseOrderBy(s string) (*OrderByOp, error) {
	parts := splitAndTrim(s, ',')
	var clauses []OrderClause

	for _, part := range parts {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		clause := OrderClause{
			Column: fields[0],
			Desc:   true, // KQL default is desc
		}
		if len(fields) > 1 {
			switch strings.ToLower(fields[1]) {
			case "asc":
				clause.Desc = false
			case "desc":
				clause.Desc = true
			}
		}
		clauses = append(clauses, clause)
	}

	return &OrderByOp{Clauses: clauses}, nil
}

func parseSummarize(s string) (*SummarizeOp, error) {
	s = strings.TrimSpace(s)

	// Split on " by " to separate aggregations from group-by columns
	var aggStr, byStr string
	byIdx := findKeyword(s, " by ")
	if byIdx >= 0 {
		aggStr = strings.TrimSpace(s[:byIdx])
		byStr = strings.TrimSpace(s[byIdx+4:])
	} else {
		aggStr = s
	}

	// Parse aggregations
	aggParts := splitRespectingParens(aggStr, ',')
	var aggs []Aggregation

	for _, part := range aggParts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		agg, err := parseAggregation(part)
		if err != nil {
			return nil, err
		}
		aggs = append(aggs, *agg)
	}

	if len(aggs) == 0 {
		return nil, fmt.Errorf("summarize: no aggregations specified")
	}

	// Parse group-by expressions (columns or function calls like bin())
	var byExprs []ByExpr
	if byStr != "" {
		byParts := splitRespectingParens(byStr, ',')
		for _, part := range byParts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}

			// Check for "Name = expr" alias syntax
			var name string
			exprStr := part
			eqIdx := strings.Index(part, "=")
			if eqIdx > 0 && (eqIdx+1 >= len(part) || part[eqIdx+1] != '=') &&
				(eqIdx == 0 || part[eqIdx-1] != '!') {
				name = strings.TrimSpace(part[:eqIdx])
				exprStr = strings.TrimSpace(part[eqIdx+1:])
			}

			expr, err := ParseExpr(exprStr)
			if err != nil {
				return nil, fmt.Errorf("summarize by: %w", err)
			}

			// Auto-derive output column name if not aliased
			if name == "" {
				name = deriveByName(expr)
			}

			byExprs = append(byExprs, ByExpr{Name: name, Expr: expr})
		}
	}

	return &SummarizeOp{Aggregations: aggs, ByExprs: byExprs}, nil
}

// deriveByName extracts a display name from a group-by expression.
// For ColumnRef, returns the column name.
// For bin(Col, step), returns the first arg's column name.
// For other expressions, returns the expression as a string.
func deriveByName(expr Expr) string {
	switch e := expr.(type) {
	case *ColumnRef:
		return e.Name
	case *FuncCall:
		// For bin/floor/startofday etc., use the first arg's column name
		if len(e.Args) > 0 {
			if ref, ok := e.Args[0].(*ColumnRef); ok {
				return ref.Name
			}
		}
		return e.Name + "_"
	case *AccessExpr:
		// For Properties.tags.env → use leaf property name
		if len(e.Path) > 0 {
			last := e.Path[len(e.Path)-1]
			if last.Name != "" {
				return last.Name
			}
		}
		return deriveByName(e.Object)
	default:
		return "expr_"
	}
}

func parseAggregation(s string) (*Aggregation, error) {
	s = strings.TrimSpace(s)
	agg := &Aggregation{}

	// Check for "Name = func(...)" pattern
	eqIdx := strings.Index(s, "=")
	if eqIdx > 0 {
		// Make sure '=' is not part of '==' or '!='
		if eqIdx+1 < len(s) && s[eqIdx+1] == '=' {
			// It's ==, not assignment
		} else if eqIdx > 0 && s[eqIdx-1] == '!' {
			// It's !=
		} else {
			agg.Name = strings.TrimSpace(s[:eqIdx])
			s = strings.TrimSpace(s[eqIdx+1:])
		}
	}

	// Parse function call: func(args...)
	parenIdx := strings.Index(s, "(")
	if parenIdx < 0 {
		return nil, fmt.Errorf("expected aggregation function call, got %q", s)
	}

	agg.Function = strings.ToLower(strings.TrimSpace(s[:parenIdx]))

	closeIdx := strings.LastIndex(s, ")")
	if closeIdx < 0 {
		return nil, fmt.Errorf("expected closing ) in %q", s)
	}

	argStr := strings.TrimSpace(s[parenIdx+1 : closeIdx])
	if argStr != "" {
		argParts := splitRespectingParens(argStr, ',')
		for _, ap := range argParts {
			ap = strings.TrimSpace(ap)
			if ap == "" {
				continue
			}
			// Bare `*` — real ADX's arg_max/arg_min wildcard ("return
			// all columns"), recognized here rather than in ParseExpr
			// since it's specific to this argument-list context, not a
			// general expression. Checked before ParseExpr, which
			// would otherwise reject `*` outright (it's only valid as
			// a binary multiplication operator, never a standalone
			// token) — found live: arg_max(Version, *) failed to parse
			// at all before this.
			if ap == "*" {
				agg.Args = append(agg.Args, &StarExpr{})
				continue
			}
			expr, err := ParseExpr(ap)
			if err != nil {
				return nil, fmt.Errorf("aggregation arg: %w", err)
			}
			agg.Args = append(agg.Args, expr)
		}
	}

	// Auto-generate output name if not specified. arg_max/arg_min are
	// a deliberate exception to the general function_argname[0] rule,
	// verified against real ADX examples before diverging: arg_max(
	// BeginLat, BeginLocation) by State names its output column
	// BeginLocation (the SECOND argument -- what's actually returned),
	// not arg_max_BeginLat. Every other aggregate (max, sum, count,
	// ...) keeps the function_argname[0] convention, since for those
	// the first argument IS what's returned. Found live via a real
	// cross-model conformance report before this was in scope to fix:
	// the old naming meant any hand-written query or stored function
	// projecting the "obvious" column name would silently get nothing
	// (a column that doesn't exist) rather than the intended value.
	if agg.Name == "" {
		nameSourceIdx := 0
		if (agg.Function == "arg_max" || agg.Function == "arg_min") && len(agg.Args) >= 2 {
			nameSourceIdx = 1
		}
		// percentiles_array's own auto-name prefix is "percentiles",
		// not the literal function name "percentiles_array" — verified
		// directly against real ADX's own worked example before fixing
		// this, not assumed: summarize percentiles_array(Value, ...)
		// names its output column "percentiles_Value", matching
		// percentiles() itself using "percentile_" (singular) despite
		// its own plural function name — real ADX's column-naming
		// convention for this whole family is independent of each
		// function's own exact literal name.
		namePrefix := agg.Function
		if agg.Function == "percentiles_array" {
			namePrefix = "percentiles"
		}
		if len(agg.Args) > nameSourceIdx {
			if ref, ok := agg.Args[nameSourceIdx].(*ColumnRef); ok {
				agg.Name = ref.Name
				if nameSourceIdx == 0 {
					agg.Name = namePrefix + "_" + ref.Name
				}
			} else {
				agg.Name = namePrefix + "_"
			}
		} else {
			agg.Name = namePrefix + "_"
		}
	}

	return agg, nil
}

// AssignmentEqIndex is the exported form of assignmentEqIndex, for
// cross-package reuse of the same named-argument detection this
// parser already relies on internally — first needed by the engine
// package's evaluate plugins (evaluate.go's bag_unpack, and any
// future plugin taking named arguments like columnsConflict=... will
// need the identical detection, not a duplicated, potentially
// drifting copy of it).
func AssignmentEqIndex(s string) int { return assignmentEqIndex(s) }

// assignmentEqIndex returns the index of a top-level assignment '=' in s,
// or -1 if s is not an assignment. It skips '==', '!=', '<=', '>=', '=~' and
// any '=' inside quotes or parentheses, so expressions like
// "X = iff(A == 1, \"y\", \"n\")" are detected correctly while a bare
// comparison is not.
func assignmentEqIndex(s string) int {
	depth := 0
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inQuote != 0 {
			if ch == inQuote && !precededByOddBackslashes(s, i) {
				inQuote = 0
			}
			continue
		}
		switch ch {
		case '"', '\'':
			inQuote = ch
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case '=':
			if depth != 0 {
				continue
			}
			// Not an assignment if part of ==, !=, <=, >=, =~
			if i+1 < len(s) && (s[i+1] == '=' || s[i+1] == '~') {
				i++ // skip the pair
				continue
			}
			if i > 0 && (s[i-1] == '=' || s[i-1] == '!' || s[i-1] == '<' || s[i-1] == '>') {
				continue
			}
			return i
		}
	}
	return -1
}

// parseAssignments parses a comma-separated list of "Name = expr" or
// bare "expr" items — shared by print, extend, and project.
//
// Verified against real ADX's own extend/project docs before adding
// bare-expression support: "if ColumnName is omitted, the output
// column name of Expression is automatically generated" — confirmed
// for BOTH operators explicitly, not just print, so this fix belongs
// here, in the shared parsing function, not as a print-specific
// special case. An earlier version rejected any part without a
// top-level '=' outright ("expected 'Name = expr'"), which real ADX
// never required at all. Found live via a different model's testing
// (Kimi): even a query as simple as `print 5` (no toscalar()
// involved) failed the same way, confirming this was a real,
// pre-existing gap, not something introduced by toscalar() work.
//
// Uses assignmentEqIndex (not a naive strings.Index) to find the
// top-level '=' — the same helper join's kind=X parsing and others
// already rely on, which correctly skips ==, !=, <=, >=, =~ and any
// '=' nested inside quotes or parentheses. The previous, ad-hoc
// "guard against == being treated as assignment" check only handled
// the single, exact `==` case; assignmentEqIndex is the more general,
// already-proven fix for the same underlying problem.
//
// A bare expression (no top-level '=' found at all) gets the
// expression's own raw text as its auto-generated name, matching real
// ADX's own convention of using the expression text itself when no
// name is given — not a synthetic placeholder like "print_0", which
// an earlier version of applyPrint's own display fallback used only
// because the parser never allowed a name-less Assignment to reach it
// at all until now.
func parseAssignments(s string) ([]Assignment, error) {
	parts := splitRespectingParens(s, ',')
	var result []Assignment

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		eqIdx := assignmentEqIndex(part)
		if eqIdx < 0 {
			// Bare expression, no name given — real ADX auto-generates
			// one from the expression's own text.
			expr, err := ParseExpr(part)
			if err != nil {
				return nil, fmt.Errorf("%q: %w", part, err)
			}
			result = append(result, Assignment{Name: part, Expr: expr})
			continue
		}

		name := strings.TrimSpace(part[:eqIdx])
		exprStr := strings.TrimSpace(part[eqIdx+1:])
		expr, err := ParseExpr(exprStr)
		if err != nil {
			return nil, fmt.Errorf("extend %q: %w", name, err)
		}

		result = append(result, Assignment{Name: name, Expr: expr})
	}
	return result, nil
}

// parseJoin parses: kind=X (subquery) on col1[, col2]
// or: kind=X (subquery) on $left.A == $right.B[, ...]
func parseJoin(s string) (*JoinOp, error) {
	s = strings.TrimSpace(s)

	// Parse join kind (optional, defaults to innerunique — real ADX's own
	// default, not okql's own choice; see join.go's applyJoin for the
	// full reasoning). Verified against Microsoft's own docs before
	// changing this: "innerunique (default) — Inner join with left
	// side deduplication." A bare `join` used to default to plain
	// inner instead, silently, with no error — the exact class of
	// trap this whole codebase has spent a lot of effort closing
	// elsewhere, just discovered here via cross-model conformance
	// testing rather than a live incident. Checked blast radius before
	// changing: zero uses of bare (kind-less) join exist anywhere in
	// this repo's own tests or in either memory-scope skill's
	// documented recipes — every one already specifies kind=
	// explicitly, so this is a zero-risk correction, not a breaking
	// change to anything that currently exists.
	kind := JoinInnerUnique
	if strings.HasPrefix(strings.ToLower(s), "kind=") {
		eqIdx := strings.Index(s, "=")
		// Find the end of the kind value (next space)
		spaceIdx := strings.Index(s[eqIdx+1:], " ")
		if spaceIdx < 0 {
			return nil, fmt.Errorf("join: expected (subquery) after kind=X")
		}
		kindStr := strings.TrimSpace(s[eqIdx+1 : eqIdx+1+spaceIdx])
		var err error
		kind, err = parseJoinKind(kindStr)
		if err != nil {
			return nil, err
		}
		s = strings.TrimSpace(s[eqIdx+1+spaceIdx:])
	}

	// Parse (subquery) — find matching parentheses
	if len(s) == 0 || s[0] != '(' {
		return nil, fmt.Errorf("join: expected (subquery)")
	}

	depth := 0
	closeIdx := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '(' {
			depth++
		} else if s[i] == ')' {
			depth--
			if depth == 0 {
				closeIdx = i
				break
			}
		}
	}
	if closeIdx < 0 {
		return nil, fmt.Errorf("join: unmatched parenthesis in subquery")
	}

	subqueryStr := strings.TrimSpace(s[1:closeIdx])
	rest := strings.TrimSpace(s[closeIdx+1:])

	// Parse the subquery as a full query (table | operators)
	subStmt, err := parseQuery(subqueryStr)
	if err != nil {
		return nil, fmt.Errorf("join subquery: %w", err)
	}
	rightQuery, ok := subStmt.(*Query)
	if !ok {
		return nil, fmt.Errorf("join: subquery must be a tabular expression")
	}

	// Parse on clause
	if !strings.HasPrefix(strings.ToLower(rest), "on ") {
		return nil, fmt.Errorf("join: expected 'on' clause after subquery")
	}
	onStr := strings.TrimSpace(rest[3:])

	clauses, err := parseJoinOnClauses(onStr)
	if err != nil {
		return nil, fmt.Errorf("join on: %w", err)
	}

	return &JoinOp{
		Kind:      kind,
		Right:     rightQuery,
		OnClauses: clauses,
	}, nil
}

func parseJoinKind(s string) (JoinKind, error) {
	switch strings.ToLower(s) {
	case "inner":
		return JoinInner, nil
	case "innerunique":
		return JoinInnerUnique, nil
	case "leftouter":
		return JoinLeftOuter, nil
	case "rightouter":
		return JoinRightOuter, nil
	case "fullouter":
		return JoinFullOuter, nil
	case "leftanti", "anti":
		return JoinLeftAnti, nil
	case "leftsemi":
		return JoinLeftSemi, nil
	case "rightanti":
		return JoinRightAnti, nil
	case "rightsemi":
		return JoinRightSemi, nil
	default:
		return 0, fmt.Errorf("unknown join kind: %q", s)
	}
}

// parseJoinOnClauses parses join conditions:
//   - Simple: "Key1, Key2" (same column name on both sides)
//   - Explicit: "$left.A == $right.B, $left.C == $right.D"
func parseJoinOnClauses(s string) ([]JoinCondition, error) {
	parts := splitAndTrim(s, ',')
	var clauses []JoinCondition

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Check for $left.X == $right.Y syntax
		if strings.Contains(part, "$left.") || strings.Contains(part, "$right.") {
			eqIdx := strings.Index(part, "==")
			if eqIdx < 0 {
				return nil, fmt.Errorf("expected == in explicit join condition: %q", part)
			}
			leftExpr := strings.TrimSpace(part[:eqIdx])
			rightExpr := strings.TrimSpace(part[eqIdx+2:])

			leftCol := strings.TrimPrefix(leftExpr, "$left.")
			rightCol := strings.TrimPrefix(rightExpr, "$right.")

			// Handle reversed order: $right.X == $left.Y
			if strings.HasPrefix(leftExpr, "$right.") && strings.HasPrefix(rightExpr, "$left.") {
				leftCol = strings.TrimPrefix(rightExpr, "$left.")
				rightCol = strings.TrimPrefix(leftExpr, "$right.")
			}

			clauses = append(clauses, JoinCondition{LeftColumn: leftCol, RightColumn: rightCol})
		} else {
			// Simple: column name is the same on both sides
			clauses = append(clauses, JoinCondition{LeftColumn: part, RightColumn: part})
		}
	}

	if len(clauses) == 0 {
		return nil, fmt.Errorf("no join conditions specified")
	}
	return clauses, nil
}

// parseProjectItems parses a project item list: col1, NewCol = expr, ...
// Shared by the project operator and graph-match's project clause.
func parseProjectItems(colStr string) ([]ProjectItem, error) {
	parts := splitAndTrim(colStr, ',')
	if len(parts) == 0 {
		return nil, fmt.Errorf("no columns specified")
	}
	items := make([]ProjectItem, 0, len(parts))
	for _, part := range parts {
		eqIdx := assignmentEqIndex(part)
		if eqIdx < 0 {
			// A part with no top-level '=' is either a genuine bare
			// column reference (project X — the common case, kept
			// exactly as before: item.Expr stays nil, resolved by
			// name lookup against the input schema) or an unnamed
			// COMPUTED expression (project x + 1 — real ADX's own,
			// verified-before-adopting rule: "if ColumnName is
			// omitted, the output column name of Expression is
			// automatically generated"). isValidIdentifier
			// distinguishes the two: a real column name can never
			// itself contain an operator, space, or anything else
			// that would also make it a syntactically valid, longer
			// expression -- "X" passes, "x + 1" doesn't. Found live,
			// not hypothetical: parseAssignments (this function's own
			// sibling, shared by print/extend) already fixed to accept
			// a bare expression using this same real ADX rule; project
			// has its OWN, separate parsing function and needed the
			// identical fix applied separately, or `project x + 1`
			// would have kept failing with "column "x + 1" not
			// found" even after that fix landed.
			if isValidIdentifier(part) {
				items = append(items, ProjectItem{Name: part})
				continue
			}
			expr, err := ParseExpr(part)
			if err != nil {
				return nil, fmt.Errorf("%q: %w", part, err)
			}
			items = append(items, ProjectItem{Name: part, Expr: expr})
			continue
		}
		name := strings.TrimSpace(part[:eqIdx])
		exprStr := strings.TrimSpace(part[eqIdx+1:])
		if name == "" || exprStr == "" {
			return nil, fmt.Errorf("expected 'Name = expr', got %q", part)
		}
		expr, err := ParseExpr(exprStr)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", name, err)
		}
		items = append(items, ProjectItem{Name: name, Expr: expr})
	}
	return items, nil
}

// parseMakeGraph parses: SourceCol --> TargetCol [with NodeTable on NodeIdCol]
// (the "make-graph " prefix has already been stripped by the caller).
func parseMakeGraph(s string) (*MakeGraphOp, error) {
	s = strings.TrimSpace(s)

	arrowIdx := strings.Index(s, "-->")
	if arrowIdx < 0 {
		return nil, fmt.Errorf("make-graph: expected 'Source --> Target'")
	}

	op := &MakeGraphOp{SourceColumn: strings.TrimSpace(s[:arrowIdx])}
	if op.SourceColumn == "" {
		return nil, fmt.Errorf("make-graph: missing source column before -->")
	}

	rest := strings.TrimSpace(s[arrowIdx+len("-->"):])

	// Optional: with NodeTable on NodeIdCol
	withIdx := findKeyword(rest, " with ")
	if withIdx >= 0 {
		op.TargetColumn = strings.TrimSpace(rest[:withIdx])
		withPart := strings.TrimSpace(rest[withIdx+len(" with "):])
		onIdx := findKeyword(withPart, " on ")
		if onIdx < 0 {
			return nil, fmt.Errorf("make-graph: expected 'with NodeTable on NodeIdColumn'")
		}
		op.NodesTable = strings.TrimSpace(withPart[:onIdx])
		op.NodeIdColumn = strings.TrimSpace(withPart[onIdx+len(" on "):])
		if op.NodesTable == "" || op.NodeIdColumn == "" {
			return nil, fmt.Errorf("make-graph: expected 'with NodeTable on NodeIdColumn'")
		}
	} else {
		op.TargetColumn = strings.TrimSpace(rest)
	}

	if op.TargetColumn == "" {
		return nil, fmt.Errorf("make-graph: missing target column after -->")
	}
	return op, nil
}

// parseGraphMatch parses: (a)-[e]->(b)... [where <expr>] project <items>
// (the "graph-match " prefix has already been stripped by the caller).
func parseGraphMatch(s string) (*GraphMatchOp, error) {
	s = strings.TrimSpace(s)

	// The project clause is required and terminates the operator.
	projIdx := findKeyword(s, " project ")
	if projIdx < 0 {
		return nil, fmt.Errorf("graph-match: project clause is required")
	}
	projStr := strings.TrimSpace(s[projIdx+len(" project "):])
	head := strings.TrimSpace(s[:projIdx])

	op := &GraphMatchOp{}

	// Optional where clause between pattern and project.
	if whereIdx := findKeyword(head, " where "); whereIdx >= 0 {
		whereStr := strings.TrimSpace(head[whereIdx+len(" where "):])
		expr, err := ParseExpr(whereStr)
		if err != nil {
			return nil, fmt.Errorf("graph-match where: %w", err)
		}
		op.Where = expr
		head = strings.TrimSpace(head[:whereIdx])
	}

	nodes, edges, err := parseGraphPattern(head)
	if err != nil {
		return nil, err
	}
	op.Nodes = nodes
	op.Edges = edges

	items, err := parseProjectItems(projStr)
	if err != nil {
		return nil, fmt.Errorf("graph-match project: %w", err)
	}
	op.Project = &ProjectOp{Items: items}

	return op, nil
}

// parseGraphPattern parses a path pattern: (a)-[e]->(b)-[f*1..3]->(c)
// Nodes are parenthesized variable names (empty = anonymous); edges are
// -[name]-> with an optional *min..max hop range.
func parseGraphPattern(s string) ([]string, []GraphMatchEdge, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil, fmt.Errorf("graph-match: missing pattern")
	}

	var nodes []string
	var edges []GraphMatchEdge
	i := 0
	expectNode := true

	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}

		if expectNode {
			if s[i] != '(' {
				return nil, nil, fmt.Errorf("graph-match: expected '(' in pattern at %q", s[i:])
			}
			closeIdx := strings.IndexByte(s[i:], ')')
			if closeIdx < 0 {
				return nil, nil, fmt.Errorf("graph-match: unmatched '(' in pattern")
			}
			nodes = append(nodes, strings.TrimSpace(s[i+1:i+closeIdx]))
			i += closeIdx + 1
			expectNode = false
			continue
		}

		// Edge: -[name[*min..max]]-> forward, <-[...]- backward,
		// -[...]- undirected.
		direction := EdgeForward
		start := i
		if strings.HasPrefix(s[i:], "<-[") {
			direction = EdgeBackward
			start = i + 1
		} else if !strings.HasPrefix(s[i:], "-[") {
			return nil, nil, fmt.Errorf("graph-match: expected '-[', '<-[' in pattern at %q", s[i:])
		}
		closeRel := strings.IndexByte(s[start:], ']')
		if closeRel < 0 {
			return nil, nil, fmt.Errorf("graph-match: unmatched '[' in pattern")
		}
		inner := strings.TrimSpace(s[start+2 : start+closeRel])
		i = start + closeRel + 1
		switch {
		case direction == EdgeBackward:
			if !strings.HasPrefix(s[i:], "-") || strings.HasPrefix(s[i:], "->") {
				return nil, nil, fmt.Errorf("graph-match: expected '-' closing a backward edge at %q", s[i:])
			}
			i++
		case strings.HasPrefix(s[i:], "->"):
			i += 2
		case strings.HasPrefix(s[i:], "-"):
			direction = EdgeAny
			i++
		default:
			return nil, nil, fmt.Errorf("graph-match: expected '->' or '-' after edge at %q", s[i:])
		}

		edge := GraphMatchEdge{MinHops: 1, MaxHops: 1, Direction: direction}
		if starIdx := strings.IndexByte(inner, '*'); starIdx >= 0 {
			edge.Name = strings.TrimSpace(inner[:starIdx])
			rangeStr := strings.TrimSpace(inner[starIdx+1:])

			// Optional trailing " distinct" after the hop range:
			// -[e*1..3 distinct]-> requests BFS reachable-node
			// semantics instead of path enumeration.
			rangeFields := strings.Fields(rangeStr)
			if len(rangeFields) == 2 && strings.EqualFold(rangeFields[1], "distinct") {
				edge.Distinct = true
				rangeStr = rangeFields[0]
			} else if len(rangeFields) > 1 {
				return nil, nil, fmt.Errorf("graph-match: unexpected trailing text in hop range %q (only 'distinct' is recognized)", inner)
			}

			dotIdx := strings.Index(rangeStr, "..")
			if dotIdx < 0 {
				return nil, nil, fmt.Errorf("graph-match: expected '*min..max' hop range, got %q", inner)
			}
			minVal, err1 := strconv.Atoi(strings.TrimSpace(rangeStr[:dotIdx]))
			maxVal, err2 := strconv.Atoi(strings.TrimSpace(rangeStr[dotIdx+2:]))
			if err1 != nil || err2 != nil || minVal < 1 || maxVal < minVal {
				return nil, nil, fmt.Errorf("graph-match: invalid hop range %q (expected *min..max with 1 <= min <= max)", inner)
			}
			if maxVal > MaxGraphHops {
				return nil, nil, fmt.Errorf("graph-match: hop range max %d exceeds limit %d", maxVal, MaxGraphHops)
			}
			edge.MinHops = minVal
			edge.MaxHops = maxVal
		} else {
			edge.Name = inner
		}
		edges = append(edges, edge)
		expectNode = true
	}

	if expectNode {
		return nil, nil, fmt.Errorf("graph-match: pattern must end with a node")
	}
	if len(edges) == 0 {
		return nil, nil, fmt.Errorf("graph-match: pattern needs at least one edge")
	}
	return nodes, edges, nil
}

// parseLookup parses: lookup [kind=X] TableName on col1[, col2]
func parseLookup(s string) (*LookupOp, error) {
	s = strings.TrimSpace(s)
	op := &LookupOp{Kind: JoinLeftOuter} // default kind for lookup

	// Optional kind=X
	if strings.HasPrefix(strings.ToLower(s), "kind=") {
		eqEnd := strings.Index(s, " ")
		if eqEnd < 0 {
			return nil, fmt.Errorf("lookup: expected table name after kind")
		}
		kindStr := s[len("kind="):eqEnd]
		k, err := parseJoinKind(kindStr)
		if err != nil {
			return nil, fmt.Errorf("lookup: %w", err)
		}
		if k != JoinLeftOuter && k != JoinInner {
			return nil, fmt.Errorf("lookup: kind must be leftouter or inner, got %s", kindStr)
		}
		op.Kind = k
		s = strings.TrimSpace(s[eqEnd:])
	}

	// TableName on ...
	onIdx := strings.Index(strings.ToLower(s), " on ")
	if onIdx < 0 {
		return nil, fmt.Errorf("lookup: expected 'on' clause")
	}
	tableName := strings.TrimSpace(s[:onIdx])
	// Accept a parenthesized table reference — lookup (Entities) on ...
	// — same as a bare one, not just the bare form. Found live: this
	// previously took the raw substring verbatim with no paren-
	// stripping at all, so lookup (Entities) on ... failed with
	// table "(Entities)" not found (parens literally included in the
	// looked-up name), even though the bare form (lookup Entities on
	// ...) already worked correctly. join already accepts a
	// parenthesized right side (in fact requires it — see join's own
	// parsing) and users reasonably expect the same flexibility here.
	if len(tableName) >= 2 && tableName[0] == '(' && tableName[len(tableName)-1] == ')' {
		tableName = strings.TrimSpace(tableName[1 : len(tableName)-1])
	}
	op.TableName = tableName
	onPart := strings.TrimSpace(s[onIdx+4:])

	clauses, err := parseJoinOnClauses(onPart)
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	op.OnClauses = clauses

	return op, nil
}

// parseSearchStatement parses "search [in (T1, T2, ...)] "term"" and
// any subsequent pipeline operators, mirroring parsePrintStatement's
// structure (Source: "", first operator carries the payload).
func parseSearchStatement(s string, remaining []string) (Statement, error) {
	s = strings.TrimSpace(s)
	op := &SearchOp{}

	if strings.HasPrefix(strings.ToLower(s), "in ") || strings.HasPrefix(strings.ToLower(s), "in(") {
		rest := strings.TrimSpace(s[2:])
		if !strings.HasPrefix(rest, "(") {
			return nil, fmt.Errorf("search: expected '(' after 'in'")
		}
		closeIdx := strings.IndexByte(rest, ')')
		if closeIdx < 0 {
			return nil, fmt.Errorf("search: unmatched '(' in 'in (...)'")
		}
		tableList := rest[1:closeIdx]
		for _, t := range strings.Split(tableList, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				op.Tables = append(op.Tables, t)
			}
		}
		s = strings.TrimSpace(rest[closeIdx+1:])
	}

	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' {
		return nil, fmt.Errorf("search: expected a quoted term, got %q", s)
	}
	termExpr, err := ParseExpr(s)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	lit, ok := termExpr.(*Literal)
	if !ok {
		return nil, fmt.Errorf("search: expected a quoted string literal, got %q", s)
	}
	term, ok := lit.Value.(string)
	if !ok {
		return nil, fmt.Errorf("search: expected a string literal")
	}
	op.Term = term

	query := &Query{Source: "", Operators: []Operator{op}}
	for i, seg := range remaining {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		subOp, err := parseOperator(seg)
		if err != nil {
			return nil, fmt.Errorf("search: pipeline operator %d: %w", i+1, err)
		}
		query.Operators = append(query.Operators, subOp)
	}
	return query, nil
}

// parseFindStatement parses find's two real-ADX documented grammar
// forms — verified against Microsoft's own find operator docs before
// adopting this shape, including a real, easy-to-miss distinction:
// "find [withsource=ColumnName] [in (Tables)] where Predicate
// [project-smart | project ...]" REQUIRES the where keyword whenever
// withsource or in(...) is present, but the shorter
// "find Predicate [project ...]" form has NO where keyword at all.
func parseFindStatement(s string, remaining []string) (Statement, error) {
	s = strings.TrimSpace(s)
	op := &FindOp{ProjectSmart: true}

	sawWithsourceOrIn := false

	if strings.HasPrefix(strings.ToLower(s), "withsource=") {
		rest := s[len("withsource="):]
		fields := strings.Fields(rest)
		if len(fields) == 0 || !isValidIdentifier(fields[0]) {
			return nil, fmt.Errorf("find: expected a column name after 'withsource='")
		}
		op.WithSource = fields[0]
		s = strings.TrimSpace(rest[len(fields[0]):])
		sawWithsourceOrIn = true
	}

	lowerS := strings.ToLower(s)
	if strings.HasPrefix(lowerS, "in ") || strings.HasPrefix(lowerS, "in(") {
		rest := strings.TrimSpace(s[2:])
		if !strings.HasPrefix(rest, "(") {
			return nil, fmt.Errorf("find: expected '(' after 'in'")
		}
		closeIdx := findMatchingParen(rest, 0)
		if closeIdx < 0 {
			return nil, fmt.Errorf("find: unmatched '(' in 'in (...)'")
		}
		for _, t := range splitRespectingParens(rest[1:closeIdx], ',') {
			t = strings.TrimSpace(t)
			if t != "" {
				op.Tables = append(op.Tables, t)
			}
		}
		s = strings.TrimSpace(rest[closeIdx+1:])
		sawWithsourceOrIn = true
	}

	if sawWithsourceOrIn {
		if !strings.HasPrefix(strings.ToLower(s), "where ") {
			return nil, fmt.Errorf("find: expected 'where' after withsource=/in(...)")
		}
		s = strings.TrimSpace(s[len("where "):])
	} else if strings.HasPrefix(strings.ToLower(s), "where ") {
		// Lenient extension: an explicit "where" is also accepted even
		// without withsource=/in(...), a harmless superset of the
		// shorter documented form rather than a strict rejection of it.
		s = strings.TrimSpace(s[len("where "):])
	}

	predText := s
	projectText := ""
	if idx := findTopLevelKeyword(s, "project-smart"); idx >= 0 {
		predText = strings.TrimSpace(s[:idx])
		projectText = strings.TrimSpace(s[idx:])
	} else if idx := findTopLevelKeyword(s, "project"); idx >= 0 {
		predText = strings.TrimSpace(s[:idx])
		projectText = strings.TrimSpace(s[idx:])
	}

	if predText == "" {
		return nil, fmt.Errorf("find: expected a predicate")
	}

	// The "* has term" / bare-term forms search every column of each
	// row rather than evaluate a normal, column-bound boolean
	// expression — not representable as an ordinary Expr (there's no
	// valid ColumnRef for the literal "*" token), so detected and
	// handled specially here rather than via ParseExpr at all.
	lowerPred := strings.ToLower(predText)
	if strings.HasPrefix(lowerPred, "* has ") {
		termExpr, err := ParseExpr(strings.TrimSpace(predText[len("* has "):]))
		if err != nil {
			return nil, fmt.Errorf("find: %w", err)
		}
		lit, ok := termExpr.(*Literal)
		if !ok {
			return nil, fmt.Errorf("find: expected a quoted string literal after '* has'")
		}
		term, ok := lit.Value.(string)
		if !ok {
			return nil, fmt.Errorf("find: expected a string literal after '* has'")
		}
		op.AnyColumnTerm = term
	} else if len(predText) >= 2 && predText[0] == '"' {
		// Bare quoted term with no comparison operator at all
		// (find "Hernandez") — same any-column search as "* has term".
		termExpr, err := ParseExpr(predText)
		if err != nil {
			return nil, fmt.Errorf("find: %w", err)
		}
		if lit, ok := termExpr.(*Literal); ok {
			if term, ok := lit.Value.(string); ok {
				op.AnyColumnTerm = term
			}
		}
		if op.AnyColumnTerm == "" {
			return nil, fmt.Errorf("find: expected a quoted string literal")
		}
	} else {
		predExpr, err := ParseExpr(predText)
		if err != nil {
			return nil, fmt.Errorf("find: predicate: %w", err)
		}
		op.Predicate = predExpr
	}

	if projectText != "" {
		if strings.HasPrefix(strings.ToLower(projectText), "project-smart") {
			op.ProjectSmart = true
		} else {
			op.ProjectSmart = false
			listText := strings.TrimSpace(projectText[len("project"):])
			for _, part := range splitRespectingParens(listText, ',') {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				if strings.EqualFold(part, "pack_all()") {
					op.PackAll = true
					continue
				}
				name := part
				var colType types.KQLType
				if colonIdx := strings.Index(part, ":"); colonIdx >= 0 {
					name = strings.TrimSpace(part[:colonIdx])
					typeName := strings.TrimSpace(part[colonIdx+1:])
					t, err := types.ParseType(typeName)
					if err != nil {
						return nil, fmt.Errorf("find: project %q: %w", part, err)
					}
					colType = t
				}
				op.ProjectItems = append(op.ProjectItems, FindProjectItem{Name: name, Type: colType})
			}
		}
	}

	query := &Query{Source: "", Operators: []Operator{op}}
	for i, seg := range remaining {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		subOp, err := parseOperator(seg)
		if err != nil {
			return nil, fmt.Errorf("find: pipeline operator %d: %w", i+1, err)
		}
		query.Operators = append(query.Operators, subOp)
	}
	return query, nil
}
