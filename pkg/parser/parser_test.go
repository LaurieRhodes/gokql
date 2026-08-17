package parser

import (
	"testing"
)

// --- Basic Statement Parsing ---

func TestParseQuery(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"simple table", `TableName`},
		{"where", `T | where X > 5`},
		{"project", `T | project A, B`},
		{"extend", `T | extend Y = X * 2`},
		{"take", `T | take 10`},
		{"count", `T | count`},
		{"distinct", `T | distinct A, B`},
		{"order by", `T | order by X desc`},
		{"top", `T | top 5 by X`},
		{"summarize", `T | summarize count() by G`},
		{"project-away", `T | project-away B, C`},
		{"project-rename", `T | project-rename NewA = A`},
		{"project-reorder", `T | project-reorder C, A`},
		{"project-keep", `T | project-keep *IP`},
		{"sample", `T | sample 5`},
		{"serialize", `T | serialize N = row_number()`},
		{"mv-expand", `T | mv-expand X`},
		{"getschema", `T | getschema`},
		{"print", `print X = 42`},
		{"datatable", `datatable (A: long) [1, 2, 3]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tt.input, err)
			}
			if stmt == nil {
				t.Fatalf("Parse(%q) returned nil", tt.input)
			}
		})
	}
}

func TestParsePipeline(t *testing.T) {
	input := `T | where X > 5 | project A, B | summarize count() by A | order by count_ desc | take 10`
	stmt, err := Parse(input)
	if err != nil {
		t.Fatalf("parse pipeline: %v", err)
	}
	q, ok := stmt.(*Query)
	if !ok {
		t.Fatal("expected *Query")
	}
	if len(q.Operators) != 5 {
		t.Fatalf("expected 5 operators, got %d", len(q.Operators))
	}
}

func TestParseManagementCommands(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"create table", `.create table T (A: string, B: long)`},
		{"create-merge table", `.create-merge table T (C: real)`},
		{"drop table", `.drop table T`},
		{"show tables", `.show tables`},
		{"show table extents", `.show table T extents`},
		{"show database", `.show database`},
		{"ingest csv", `.ingest csv into table T from "file.csv"`},
		{"ingest inline", `.ingest inline into table T <| a,1\nb,2`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tt.input, err)
			}
			if stmt == nil {
				t.Fatalf("Parse(%q) returned nil", tt.input)
			}
		})
	}
}

// --- Let Statements ---

func TestParseLet(t *testing.T) {
	input := `let x = 42; print V = x`
	stmt, err := Parse(input)
	if err != nil {
		t.Fatalf("parse let: %v", err)
	}
	cs, ok := stmt.(*CompoundStatement)
	if !ok {
		t.Fatal("expected CompoundStatement for let")
	}
	if len(cs.Lets) != 1 {
		t.Fatalf("expected 1 let, got %d", len(cs.Lets))
	}
	if cs.Lets[0].Name != "x" {
		t.Errorf("let name: expected x, got %s", cs.Lets[0].Name)
	}
}

func TestParseLetTabular(t *testing.T) {
	stmt, err := Parse(`let T = datatable (X: long) [1, 2, 3]; T | count`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	cs, ok := stmt.(*CompoundStatement)
	if !ok {
		t.Fatal("expected CompoundStatement for let")
	}
	if len(cs.Lets) != 1 {
		t.Fatalf("expected 1 let, got %d", len(cs.Lets))
	}
	if cs.Lets[0].Name != "T" {
		t.Errorf("let name: expected T, got %s", cs.Lets[0].Name)
	}
	if _, ok := cs.Lets[0].Value.(*Query); !ok {
		t.Errorf("let value: expected *Query (tabular), got %T", cs.Lets[0].Value)
	}
}

// --- Expression Parsing ---

func TestParseExprArithmetic(t *testing.T) {
	tests := []string{
		"1 + 2",
		"3 * 4 + 5",
		"(1 + 2) * 3",
		"-5",
		"10 % 3",
		"2.5 * 4.0",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			expr, err := ParseExpr(input)
			if err != nil {
				t.Fatalf("ParseExpr(%q): %v", input, err)
			}
			if expr == nil {
				t.Fatalf("nil expression for %q", input)
			}
		})
	}
}

func TestParseExprComparisons(t *testing.T) {
	tests := []string{
		`X == 5`,
		`X != "hello"`,
		`X > 3`,
		`X <= 10`,
		`X =~ "test"`,
		`X !~ "test"`,
		`X contains "hello"`,
		`X !contains "hello"`,
		`X contains_cs "hello"`,
		`X has "word"`,
		`X !has "word"`,
		`X startswith "pre"`,
		`X endswith "suf"`,
		`X matches regex "^test"`,
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			expr, err := ParseExpr(input)
			if err != nil {
				t.Fatalf("ParseExpr(%q): %v", input, err)
			}
			if expr == nil {
				t.Fatalf("nil expression for %q", input)
			}
		})
	}
}

func TestParseExprLogical(t *testing.T) {
	tests := []string{
		`X > 5 and X < 10`,
		`X == 1 or X == 2`,
		`X > 5 and Y < 10 or Z == 1`,
		`not (X > 5)`,
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := ParseExpr(input)
			if err != nil {
				t.Fatalf("ParseExpr(%q): %v", input, err)
			}
		})
	}
}

func TestParseExprIn(t *testing.T) {
	tests := []string{
		`X in ("a", "b", "c")`,
		`X !in (1, 2, 3)`,
		`X in~ ("ALLOW", "allow")`,
		`X !in~ ("deny")`,
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := ParseExpr(input)
			if err != nil {
				t.Fatalf("ParseExpr(%q): %v", input, err)
			}
		})
	}
}

func TestParseExprBetween(t *testing.T) {
	tests := []string{
		`X between (1 .. 10)`,
		`X !between (5 .. 20)`,
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := ParseExpr(input)
			if err != nil {
				t.Fatalf("ParseExpr(%q): %v", input, err)
			}
		})
	}
}

func TestParseExprFunctionCalls(t *testing.T) {
	tests := []string{
		`strlen("hello")`,
		`strcat("a", "b", "c")`,
		`iff(X > 5, "big", "small")`,
		`parse_json("{}")`,
		`bin(Timestamp, 1h)`,
		`format_datetime(now(), "yyyy-MM-dd")`,
		`has_ipv4("text", "10.0.0.0/8")`,
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := ParseExpr(input)
			if err != nil {
				t.Fatalf("ParseExpr(%q): %v", input, err)
			}
		})
	}
}

func TestParseExprTimespanLiterals(t *testing.T) {
	tests := []string{
		"1h",
		"30m",
		"1d",
		"7d",
		"500ms",
		"1tick",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := ParseExpr(input)
			if err != nil {
				t.Fatalf("ParseExpr(%q): %v", input, err)
			}
		})
	}
}

func TestParseExprDotAccess(t *testing.T) {
	tests := []string{
		`X.property`,
		`X["key"]`,
		`X.a.b.c`,
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := ParseExpr(input)
			if err != nil {
				t.Fatalf("ParseExpr(%q): %v", input, err)
			}
		})
	}
}

// --- Table Functions ---

func TestParseTableFunctions(t *testing.T) {
	tests := []struct {
		name string
		input string
	}{
		{"csv", `csv("file.csv") | where X > 5`},
		{"json", `json("file.json") | take 10`},
		{"ndjson", `ndjson("file.jsonl") | count`},
		{"parquet", `parquet("file.parquet") | getschema`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.input, err)
			}
			q, ok := stmt.(*Query)
			if !ok {
				t.Fatal("expected *Query")
			}
			if q.SourceFunc == nil {
				t.Fatal("expected SourceFunc to be set")
			}
			if q.SourceFunc.Name != tt.name {
				t.Errorf("expected func name %q, got %q", tt.name, q.SourceFunc.Name)
			}
		})
	}
}

// --- Datatable Parsing ---

func TestParseDatatable(t *testing.T) {
	input := `datatable (Name: string, Score: long, Active: bool) ["Alice", 90, true, "Bob", 85, false]`
	stmt, err := Parse(input)
	if err != nil {
		t.Fatalf("parse datatable: %v", err)
	}
	q, ok := stmt.(*Query)
	if !ok {
		t.Fatal("expected *Query")
	}
	// datatable is parsed as the first operator, not as Source
	if len(q.Operators) < 1 {
		t.Fatal("expected at least 1 operator for datatable")
	}
	dt, ok := q.Operators[0].(*DataTableOp)
	if !ok {
		t.Fatalf("expected DataTableOp, got %T", q.Operators[0])
	}
	if len(dt.Schema.Columns) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(dt.Schema.Columns))
	}
	if len(dt.Values) != 6 {
		t.Fatalf("expected 6 values, got %d", len(dt.Values))
	}
}

// --- Join Parsing ---

func TestParseJoin(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"inner", `T1 | join kind=inner (T2) on K`},
		{"leftouter", `T1 | join kind=leftouter (T2) on K`},
		{"leftanti", `T1 | join kind=leftanti (T2) on K`},
		{"leftsemi", `T1 | join kind=leftsemi (T2) on K`},
		{"rightouter", `T1 | join kind=rightouter (T2) on K`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.input, err)
			}
		})
	}
}

// --- Parse Operator ---

func TestParseParseOperator(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"simple", `T | parse S with "Error: " Code " " Message`},
		{"relaxed", `T | parse kind=relaxed S with "Error: " Code`},
		{"regex", `T | parse kind=regex S with "(?P<Code>\\d+)"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.input, err)
			}
		})
	}
}

// --- Error Cases ---

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"unclosed paren", `print X = (1 + 2`},
		{"bad operator", `T | badop`},
		{"unclosed string", `print X = "hello`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.input)
			if err == nil {
				t.Errorf("expected error for %q", tt.input)
			}
		})
	}
}

// --- Summarize Parsing ---

func TestParseSummarize(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"count", `T | summarize count()`},
		{"count_by", `T | summarize count() by G`},
		{"multi_agg", `T | summarize sum(V), avg(V) by G`},
		{"named_agg", `T | summarize Total = sum(V) by G`},
		{"bin_by", `T | summarize count() by bin(T, 1h)`},
		{"multi_by", `T | summarize count() by A, B`},
		{"percentiles", `T | summarize percentiles(V, 25, 50, 75)`},
		{"make_set", `T | summarize make_set(X)`},
		{"arg_max", `T | summarize arg_max(Score, Name)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.input, err)
			}
		})
	}
}

// --- Render Parsing ---

func TestParseRender(t *testing.T) {
	stmt, err := Parse(`T | render timechart with (title="My Chart")`)
	if err != nil {
		t.Fatalf("parse render: %v", err)
	}
	q, ok := stmt.(*Query)
	if !ok {
		t.Fatalf("expected *Query, got %T", stmt)
	}
	last := q.Operators[len(q.Operators)-1]
	r, ok := last.(*RenderOp)
	if !ok {
		t.Fatalf("expected *RenderOp, got %T", last)
	}
	if r.Visualization != "timechart" {
		t.Errorf("visualization: expected timechart, got %q", r.Visualization)
	}
	if r.With != `title="My Chart"` {
		t.Errorf("with clause: got %q", r.With)
	}
}

// --- has_any / has_all Parsing ---

func TestParseHasAnyAll(t *testing.T) {
	tests := []string{
		`T | where S has_any ("ssh", "rdp", "smb")`,
		`T | where S has_all ("login", "failed")`,
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := Parse(input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", input, err)
			}
		})
	}
}

// --- .compact table T [where <predicate>] Parsing ---

func TestParseCompactTable(t *testing.T) {
	stmt, err := Parse(`.compact table T`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cmd, ok := stmt.(*CompactTableCmd)
	if !ok {
		t.Fatalf("expected *CompactTableCmd, got %T", stmt)
	}
	if cmd.TableName != "T" {
		t.Errorf("TableName: got %q", cmd.TableName)
	}
	if cmd.Where != nil {
		t.Errorf("expected nil Where for plain .compact, got %v", cmd.Where)
	}
}

func TestParseCompactTableWithWhere(t *testing.T) {
	stmt, err := Parse(`.compact table Findings where N > 2`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cmd, ok := stmt.(*CompactTableCmd)
	if !ok {
		t.Fatalf("expected *CompactTableCmd, got %T", stmt)
	}
	if cmd.TableName != "Findings" {
		t.Errorf("TableName: got %q", cmd.TableName)
	}
	if cmd.Where == nil {
		t.Fatal("expected non-nil Where")
	}
}

func TestParseCompactTableWithLetBoundAntiJoin(t *testing.T) {
	// The actual motivating syntax: a compound statement whose final
	// segment is .compact table T where Id !in (letBoundTable).
	stmt, err := Parse(`let superseded = Edges | where Rel == "supersedes" | project Dst; .compact table Findings where Id !in (superseded)`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cs, ok := stmt.(*CompoundStatement)
	if !ok {
		t.Fatalf("expected *CompoundStatement, got %T", stmt)
	}
	if len(cs.Lets) != 1 {
		t.Fatalf("expected 1 let binding, got %d", len(cs.Lets))
	}
	cmd, ok := cs.Final.(*CompactTableCmd)
	if !ok {
		t.Fatalf("expected final statement *CompactTableCmd, got %T", cs.Final)
	}
	if cmd.TableName != "Findings" {
		t.Errorf("TableName: got %q", cmd.TableName)
	}
	if cmd.Where == nil {
		t.Fatal("expected non-nil Where")
	}
	inExpr, ok := cmd.Where.(*InExpr)
	if !ok {
		t.Fatalf("expected Where to be *InExpr, got %T", cmd.Where)
	}
	if !inExpr.Negated {
		t.Error("expected Negated (!in), got plain in")
	}
	if inExpr.TableRef != "superseded" {
		t.Errorf("expected TableRef %q, got %q", "superseded", inExpr.TableRef)
	}
}

func TestParseCompactTableMissingName(t *testing.T) {
	_, err := Parse(`.compact table `)
	if err == nil {
		t.Fatal("expected error for missing table name")
	}
}

// TestSplitStatementsRespectsBraces directly guards the fix for a
// real, live bug found via a different model's testing (Kimi): a
// semicolon inside a {...} block (a stored function or materialized
// view body) must never be treated as a top-level statement
// separator, the same way one inside (...) already wasn't.
func TestSplitStatementsRespectsBraces(t *testing.T) {
	parts := splitStatements(`.create-or-alter function T() { let s = "open"; Body | where Status == s | count }`)
	if len(parts) != 1 {
		t.Fatalf("expected the semicolon inside { } to NOT split the statement, got %d parts: %q", len(parts), parts)
	}
}

// TestSplitStatementsStillSplitsAtTopLevel guards that an ACTUAL
// top-level semicolon (outside any braces or parens) still splits
// correctly — this fix must not have made splitting too conservative.
func TestSplitStatementsStillSplitsAtTopLevel(t *testing.T) {
	parts := splitStatements(`let x = 5; print x`)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts for a genuine top-level split, got %d: %q", len(parts), parts)
	}
}
