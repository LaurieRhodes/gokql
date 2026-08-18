// Package parser implements a KQL parser producing an AST for execution.
//
// KQL's pipe-forward syntax maps directly to a linear chain of operators:
//
//	Table | where Pred | project Cols | summarize Aggs by Groups
//
// Management commands start with a dot:
//
//	.create table T (col: type, ...)
//	.show tables
package parser

import (
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// Statement is the top-level AST node — either a Query or a Command.
type Statement interface {
	statementNode()
}

// --- Queries ---

// Query is a KQL tabular query: a source table followed by pipe operators.
type Query struct {
	Source         string              // Table name
	SourceFunc     *TableFunc          // Table-valued function source (e.g. csv("path"))
	SourceDB       *DatabaseTableRef   // Cross-database source (e.g. database('alias').Table) — see engine/federation.go
	SourceFuncCall *StoredFunctionCall // Stored (persisted) tabular function call, e.g. LiveEdges() or CascadeCheck("F123"). See engine/stored_functions.go's resolveStoredFunction.
	Operators      []Operator          // Pipe-delimited operators in order
}

// StoredFunctionCall is FuncName(arg1, arg2, ...) recognized as a
// query source. ArgTexts holds each argument's RAW, unparsed text —
// deliberately, not eagerly parsed into Expr at parse time, because a
// tabular argument (e.g. "(range x from 1 to 10 step 1)") and a
// scalar argument use genuinely different parsers (Parse vs ParseExpr)
// and this parser has no way to know, purely syntactically, which
// kind a given position is without knowing the callee's declared
// signature — which isn't available at parse time at all (parsing
// stays purely syntactic throughout this parser, matching every other
// command). Kind-specific parsing is deferred to resolution time
// (engine/stored_functions.go's resolveStoredFunction), which already
// has the declared parameter list (fn.Parameters) in hand by then and
// so can parse each argument the right way without guessing.
//
// Scalar arguments are evaluated in a scalar (empty-row) context —
// matching real ADX's own restriction that a stored function invoked
// as a table source takes no row-context reference (there IS no
// current row yet at the point a query's SOURCE is being resolved),
// verified against Microsoft's own user-defined-functions docs before
// relying on this rather than assumed.
type StoredFunctionCall struct {
	Name     string
	ArgTexts []string
}

// DatabaseTableRef is a database('alias').TableName source — real
// ADX's own same-cluster cross-database syntax (verified against
// Microsoft's docs before adopting it, not invented: database()
// "changes the reference of the query to a specific database"), here
// repurposed for filesystem-based federation rather than a real
// remote cluster/database — "alias" resolves to a local directory via
// the scope's own federation config, not a network address. See
// engine/federation.go for the resolution and read-only-only
// enforcement.
type DatabaseTableRef struct {
	Alias     string
	TableName string
}

// TableFunc represents a table-valued function call like csv("path") or json("path").
type TableFunc struct {
	Name string // "csv", "json", "ndjson", "vortex"
	Path string // File path argument
}

func (*Query) statementNode() {}

// Operator is a single pipe operator (where, project, summarize, etc).
type Operator interface {
	operatorNode()
}

// UnrecognizedTestOperator exists solely so a different package's
// tests (specifically engine/planner_test.go) can construct a
// genuinely unknown Operator — one that structurally cannot have a
// case in planner.go's collectOperator switch, since Go's unexported
// marker-method pattern (operatorNode/exprNode) prevents any type
// outside this package from satisfying Operator/Expr at all. Used to
// directly test that an unrecognized operator type fails SAFE (scans
// every column) rather than silently under-collecting required
// columns — the exact bug class independently found and fixed three
// separate times this session. Never produced by the parser itself;
// exists only as a test fixture.
type UnrecognizedTestOperator struct{}

func (*UnrecognizedTestOperator) operatorNode() {}

// UnrecognizedTestExpr is the same idea for Expr / collectExpr.
type UnrecognizedTestExpr struct{}

func (*UnrecognizedTestExpr) exprNode() {}

// WhereOp: | where <predicate>
type WhereOp struct {
	Predicate Expr
}

func (*WhereOp) operatorNode() {}

// ProjectOp: | project col1, NewCol = expr, ...
// Each item is either a passthrough column reference (Expr nil, Name is the
// source column) or a computed column (Name = Expr, like extend).
type ProjectOp struct {
	Items []ProjectItem
}

// ProjectItem is a single projection entry.
type ProjectItem struct {
	Name string
	Expr Expr // nil for a bare column reference
}

func (*ProjectOp) operatorNode() {}

// ExtendOp: | extend NewCol = <expr>
// ProjectAwayOp: | project-away col1, col2, ...
type ProjectAwayOp struct {
	Columns []string
}

func (*ProjectAwayOp) operatorNode() {}

// ProjectRenameOp: | project-rename NewName = OldName, ...
type ProjectRenameOp struct {
	Renames []RenameSpec
}

func (*ProjectRenameOp) operatorNode() {}

type RenameSpec struct {
	NewName string
	OldName string
}

// ProjectReorderOp: | project-reorder col1, col2, ... [asc|desc|granny_asc|granny_desc]
type ProjectReorderOp struct {
	Columns []string
}

func (*ProjectReorderOp) operatorNode() {}

// ProjectKeepOp: | project-keep col1, col2, ... (supports wildcards)
type ProjectKeepOp struct {
	Patterns []string
}

func (*ProjectKeepOp) operatorNode() {}

// ProjectByNamesOp: | project-by-names ColumnSpecifier[, ...]
//
// Verified against real ADX docs (project-by-names-operator.md) before
// building. Distinct from project-keep in the one way its own docs
// call out explicitly: "Columns in the result are ordered based on
// the sequence in which they're specified or matched" (project-keep,
// by contrast, preserves the INPUT table's own column order,
// regardless of the order patterns were given in) — REORDERING, not
// just filtering, is project-by-names's own defining feature.
//
// Each ColumnSpecifier is one of (all five of real ADX's own worked
// examples are covered): a quoted exact column name ("Name"), a
// quoted wildcard pattern ("C*"), a dynamic array literal
// (dynamic(["Name","Country"])), a bare identifier referencing a
// let-bound (or stored-function-parameter-bound) dynamic array value,
// or column_names_of(TableRef) — a small, special-cased recognized
// form (TableRef must be a bare identifier resolving to a let-bound
// or tabular-parameter-bound table; NOT a general scalar function
// usable anywhere else, matching real ADX's own note that "Subqueries
// or scalar expressions like toscalar() aren't supported in the
// ColumnSpecifier parameter" — this engine narrows even further,
// scoping column_names_of to ONLY this one context).
type ProjectByNamesOp struct {
	Specifiers []ProjectByNamesSpecifier
}

func (*ProjectByNamesOp) operatorNode() {}

// ProjectByNamesSpecifier is one comma-separated entry in
// project-by-names. Exactly one of ColumnNamesOfTable or Expr is set.
type ProjectByNamesSpecifier struct {
	ColumnNamesOfTable string // non-empty for column_names_of(TableRef)
	Expr               Expr   // non-nil otherwise
}

// SerializeOp: | serialize [col1 = expr, ...] — materializes row order
type SerializeOp struct {
	Columns []Assignment // optional computed columns (like extend)
}

func (*SerializeOp) operatorNode() {}

// PrintOp: print expr1, expr2, ...
type PrintOp struct {
	Expressions []Assignment
}

func (*PrintOp) operatorNode() {}

// DataTableOp: datatable (Col1: type, Col2: type) [val1, val2, ...]
// Values fill rows left-to-right based on column count.
type DataTableOp struct {
	Schema types.Schema
	Values []string // raw literal values in row-major order
}

func (*DataTableOp) operatorNode() {}

// RangeOp is the range operator: `range columnName from start to stop
// step step` — verified against real ADX's own range operator docs
// before adopting this shape: "generates a single-column table of
// values... This operator doesn't take a tabular input... The values
// can't reference the columns of any table." Start/Stop/Step are kept
// as Expr (not eagerly-evaluated literals) since real ADX's own
// documented example computes them from function calls
// (range LastWeek from ago(7d) to now() step 1d), not just bare
// numeric literals — evaluated once, at execution time, matching
// real ADX's restriction that these can never reference a table's own
// columns (there being no row context available for them at all, this
// operator being a source, not a per-row transform).
//
// Positioned as a source-position operator exactly like DataTableOp
// immediately above (Query.Source == "", this op first in
// Query.Operators) — the same, already-proven pattern for a tabular
// source that isn't a real catalog table reference.
type RangeOp struct {
	ColumnName string
	Start      Expr
	Stop       Expr
	Step       Expr
}

func (*RangeOp) operatorNode() {}

// MakeSeriesOp: | make-series [kind=nonempty] [Column=]Aggregation[default=DefaultValue][, ...]
//   on AxisColumn [from start] [to end] step step [by [Column=]GroupExpr[, ...]]
//
// Verified against real ADX docs (make-series-operator.md) before
// building. Only the "main syntax" is implemented (real ADX's own docs
// explicitly recommend it over the alternate "on AxisColumn in
// range(start, stop, step)" form, so that form is deliberately out of
// scope — see MakeSeriesOp's Not Yet Implemented note in
// kql_coverage.md). From/To are both optional in the real grammar
// ([from start] [to end]) but this engine requires both explicitly —
// real ADX's own "auto-detect from the data" behavior when either is
// omitted is NOT implemented; a query omitting either gets a clear
// parse error rather than a silently wrong bin range. Step is always
// required, matching real ADX's own grammar (no brackets around it).
type MakeSeriesOp struct {
	Aggregations []MakeSeriesAggregation
	AxisColumn   string
	From         Expr
	To           Expr
	Step         Expr
	ByExprs      []ByExpr // reused verbatim from SummarizeOp's own By clause
	KindNonEmpty bool     // kind=nonempty parameter
}

func (*MakeSeriesOp) operatorNode() {}

// MakeSeriesAggregation is one [Column=]Aggregation[default=DefaultValue]
// entry. Embeds the same Aggregation struct SummarizeOp/parseAggregation
// already use (identical grammar and auto-naming convention for the
// Column=Aggregation part, verified against real ADX's own make-series
// worked example: "avg(metric)" with no explicit Column names its
// output "avg_metric", the same rule summarize already implements) —
// Default is the one genuinely new piece make-series adds on top.
type MakeSeriesAggregation struct {
	Agg     Aggregation
	Default Expr // nil = use the real-ADX-documented default of 0
}

// ParseOp: | parse [kind=simple|regex|relaxed] Column with Pattern...
type ParseOp struct {
	Column   string
	Kind     string          // "simple" (default), "regex", "relaxed"
	Flags    string          // regexFlags (kind=regex only): U/m/s/i, real KQL's "flags=..." clause
	Patterns []ParseFragment // alternating literals and field captures
}

func (*ParseOp) operatorNode() {}

// ParseFragment represents either a literal string or a field capture in a parse pattern.
type ParseFragment struct {
	Literal string // non-empty for literal text
	Field   string // non-empty for field capture (column name or * for skip)
	Type    string // declared column type for a Field fragment (e.g. "long", "date"), "" = string (default)
}

// ParseWhereOp: | parse-where [kind=simple|regex|relaxed] Column with Pattern...
//
// Same pattern syntax and matching semantics as ParseOp -- verified
// against real ADX docs (parse-where-operator.md) before building this
// as a genuinely distinct operator rather than a ParseOp alias: "parse-where
// parses the strings in the same way as parse, and filters out strings
// that were not parsed successfully. See parse operator, which produces
// nulls for unsuccessfully parsed strings." The one real semantic
// difference is that a row whose Column value doesn't match the pattern
// is dropped from the output entirely, instead of being kept with the
// new columns left null (which is what ParseOp does for every kind,
// including plain "simple" -- a known, documented simplification in
// this engine, see applyParse's own comment). ParseWhereOp always drops
// unmatched rows, regardless of kind.
type ParseWhereOp struct {
	Column   string
	Kind     string
	Flags    string
	Patterns []ParseFragment
}

func (*ParseWhereOp) operatorNode() {}

// ParseKVOp: | parse-kv Expression as ( KeysList ) with ( pair_delimiter=...,
// kv_delimiter=... [, quote=...]* [, escape=...] [, greedy=true] )
//
// Verified against real ADX docs (parse-kv-operator.md) before building.
// This engine implements the "specified delimiter" mode only (the
// pair_delimiter/kv_delimiter form used in every worked example in the
// real docs) -- the "non-specified delimiter" mode (any nonalphanumeric
// character is a delimiter, no pair_delimiter/kv_delimiter given) and
// the "regex" mode (regex=RegexPattern) are real, documented alternate
// forms of the same operator but are NOT implemented here; a query using
// either gets a clear parse error, not silent mis-parsing. See
// docs/kql_coverage.md for the precise scope note.
type ParseKVOp struct {
	Column        string
	Keys          []ParseKVKey
	PairDelimiter string
	KVDelimiter   string
	Quotes        []string // each entry 1 or 2 chars: 1 = same open/close, 2 = distinct open/close
	Escape        string   // empty = no escape char configured
	Greedy        bool
}

func (*ParseKVOp) operatorNode() {}

// ParseKVKey is one requested key in a parse-kv KeysList: Name[: Type].
type ParseKVKey struct {
	Name string
	Type string // KQL type name, "" defaults to string
}

// GetSchemaOp: | getschema
type GetSchemaOp struct{}

func (*GetSchemaOp) operatorNode() {}

// AsOp: | as Name — verified against real ADX docs (as-operator.md):
// "Binds a name to the operator's input tabular expression. This
// operator allows the query to reference the value of the tabular
// expression multiple times without breaking the query and binding a
// name through the let statement." Scoped here to same-query,
// downstream reference only (a later join/union subquery within the
// SAME pipeline's own execution referencing Name as a source) — see
// applyAs's own doc comment (pkg/engine/operators.go) for exactly why
// cross-statement (semicolon-separated) reference isn't supported:
// this engine's CompoundStatement shape is "a list of let bindings
// then one final statement", not a general list of arbitrary
// statements, so there's no later top-level statement for a name bound
// here to be visible to in the first place. Real ADX's own
// withsource=/source_/$table column-naming integration (union, find,
// search) is also NOT implemented — a purely additive, separate piece
// of real ADX's own `as` behavior, out of scope here.
type AsOp struct {
	Name string
}

func (*AsOp) operatorNode() {}

// ScanOp: | scan [declare (ColumnDeclarations)] with (step StepName [output=all|last|none] : Condition => Column = Assignment[, ...] ;)
//
// Verified against real ADX docs (scan-operator.md) before scoping
// this deliberately to a SINGLE step — real ADX's own scan is a
// genuine multi-step state machine (steps evaluated in REVERSE order
// per record, cross-step state references via StepName.ColumnName,
// with_match_id sequence tracking across matches) — real, substantial
// state-machine semantics, not a small addition on top of something
// already built. A single step already covers the operator's most
// common real use (cumulative/running-value patterns — every worked
// example in real ADX's own docs is single-step) and is what's
// implemented here; a query with more than one step, or using
// with_match_id, gets a clear parse error naming exactly what's
// unsupported, not silent mis-parsing. See applyScan's own doc
// comment (pkg/engine/scan.go) for exactly how StepName.ColumnName
// references are resolved for the one supported step.
type ScanOp struct {
	Declares    []ScanDeclare
	StepName    string
	Output      string // "all" (default), "last", "none"
	Condition   Expr
	Assignments []ScanAssignment
}

func (*ScanOp) operatorNode() {}

// ScanDeclare is one Name:Type[=DefaultExpr] entry in scan's own
// declare(...) clause.
type ScanDeclare struct {
	Name    string
	Type    string
	Default Expr // nil if no default given (state starts as the type's zero value)
}

// ScanAssignment is one Column=Assignment entry in a scan step's own
// `=>` clause.
type ScanAssignment struct {
	Column string
	Expr   Expr
}

// InvokeOp: | invoke FunctionName(args...)
//
// Verified against real ADX docs (invoke-operator.md): "Invokes a
// lambda expression that receives the source of invoke as a tabular
// argument." Scoped here to STORED functions only (created via
// `.create-or-alter function`) — real ADX's own primary worked
// example calls an inline `let`-bound lambda instead
// (`let clipped_average = (T:(x:long), ...) { ... }; ... | invoke
// clipped_average(...)`), which this engine cannot support today: its
// `let name = (params) { body }` lambda syntax only parses SCALAR
// parameters and a scalar expression body (found and documented while
// researching this operator, a genuinely separate, pre-existing gap
// from real tabular-let-lambda support, not something this operator
// itself introduces or is blocked by fixing). A query using an inline
// `let`-lambda with `invoke` fails with that lambda's own existing
// parse error (an unrecognized tabular parameter shape), not a silent
// misinterpretation. Desugars to calling Call with the pipeline's
// current table implicitly prepended as its first (tabular) argument
// — see applyInvoke's own doc comment (pkg/engine/invoke.go) for how.
type InvokeOp struct {
	Call *StoredFunctionCall
}

func (*InvokeOp) operatorNode() {}

// EvaluateOp is the evaluate plugin-invocation operator — verified
// against real ADX's own evaluate operator docs before adopting this
// shape: "[T |] evaluate [ evaluateParameters ] PluginName
// ([ PluginArgs ]) [: OutputSchema]". Designed and built as a
// GENERAL dispatch mechanism (a plugin registry, see engine.go's
// evaluatePlugins map), not a bag_unpack-only special case — matching
// an explicit request to make future plugins (pivot, etc.) cheap to
// add: adding one is registering one new function in that map, not
// touching the parser or this AST node again.
//
// ArgTexts is raw, unparsed text per argument, the same "defer
// kind-specific parsing to the plugin itself" principle already used
// for StoredFunctionCall's own ArgTexts (see that type's own doc
// comment) — a plugin's arguments can be positional, named
// (columnsConflict='replace_source'), or even a whole nested dynamic
// literal (ignoredProperties=dynamic([...])), and which shape applies
// to which plugin isn't knowable generically at parse time.
//
// OutputSchema is the optional `: (Name: type, ...)` suffix — in real
// ADX, purely a performance hint for a distributed query planner (so
// it doesn't need to scan data to infer the output shape); this
// engine has no distributed planner to hint, so it's used instead to
// let a plugin's caller force specific, exact output column types
// rather than accept whatever gets inferred from the data — a real,
// useful capability here even though the real-ADX motivation
// (performance) doesn't apply the same way.
type EvaluateOp struct {
	PluginName   string
	ArgTexts     []string
	OutputSchema *types.Schema // nil if no ": (...)" suffix was given
}

func (*EvaluateOp) operatorNode() {}

type ExtendOp struct {
	Assignments []Assignment
}

func (*ExtendOp) operatorNode() {}

// Assignment is a named expression: Name = Expr
type Assignment struct {
	Name string
	Expr Expr
}

// TakeOp: | take N  (alias: | limit N)
type TakeOp struct {
	Count int64
}

func (*TakeOp) operatorNode() {}

// SampleOp: | sample N — random sampling of N rows
type SampleOp struct {
	Count int64
}

func (*SampleOp) operatorNode() {}

// CountOp: | count
type CountOp struct{}

func (*CountOp) operatorNode() {}

// DistinctOp: | distinct col1, col2, ...
type DistinctOp struct {
	Columns []string
}

func (*DistinctOp) operatorNode() {}

// SampleDistinctOp: | sample-distinct NumberOfValues of ColumnName
//
// Verified against real ADX docs (sample-distinct-operator.md):
// "Returns a single column that contains up to the specified number
// of distinct values of the requested column. The operator is
// optimized for performance rather than fairness; the results may be
// heavily biased and should not be used for any purpose requiring
// statistical accuracy." Implemented here as a deterministic
// first-N-distinct-encountered scan (stopping as soon as Count
// distinct values are found, matching the docs' own stated
// performance-over-fairness bias) rather than any randomized
// sampling — real ADX doesn't document a specific distribution either
// (only that it isn't statistically fair), so a plain, honest,
// deterministic first-N is a legitimate reading of "up to N distinct
// values," not a shortfall against some documented random-sampling
// contract that doesn't actually exist.
type SampleDistinctOp struct {
	Count  Expr
	Column string
}

func (*SampleDistinctOp) operatorNode() {}

// OrderByOp: | order by col [asc|desc], ...
// Also: | sort by ...
type OrderByOp struct {
	Clauses []OrderClause
}

func (*OrderByOp) operatorNode() {}

// OrderClause is a single column ordering.
type OrderClause struct {
	Column string
	Desc   bool // true = descending (KQL default)
}

// TopOp: | top N by col [asc|desc]
type TopOp struct {
	Count int64
	By    string
	Desc  bool
}

func (*TopOp) operatorNode() {}

// SummarizeOp: | summarize agg1(), agg2() [by groupCol1, groupCol2]
type SummarizeOp struct {
	Aggregations []Aggregation
	ByExprs      []ByExpr // Group-by expressions (empty = global aggregate)
}

// ByExpr is a group-by expression in a summarize operator.
// Name is the output column name; Expr is evaluated per row.
type ByExpr struct {
	Name string // Output column name (e.g. "Location" or "TimeGenerated")
	Expr Expr   // Expression to evaluate (ColumnRef for plain names, FuncCall for bin(), etc.)
}

func (*SummarizeOp) operatorNode() {}

// Aggregation is a named aggregation function call.
type Aggregation struct {
	Name     string // Output column name (auto-generated if empty)
	Function string // count, sum, avg, min, max, dcount, countif, arg_max, arg_min
	Args     []Expr // Arguments to the function
}

// JoinOp: join kind=X (subquery) on col1[, col2]
type JoinOp struct {
	Kind      JoinKind
	Right     *Query          // Right-side subquery
	OnClauses []JoinCondition // Join key pairs
}

func (*JoinOp) operatorNode() {}

// MvApplyOp applies a subquery to the expanded elements of an array column,
// per input row:
//
//	T | mv-apply Element = ArrayCol to typeof(long) on ( where Element > 1 | summarize ... )
//
// For each input row, the array is expanded to a per-row subtable (original
// columns copied onto every element row), the operator pipeline runs against
// it, and the per-row results are unioned into the output.
type MvApplyOp struct {
	Name        string        // element column name (== SourceCol when not renamed)
	SourceCol   string        // array column in the input
	ElementType types.KQLType // from optional 'to typeof(T)'; TypeDynamic default
	Operators   []Operator    // subquery pipeline (sourceless)
}

func (*MvApplyOp) operatorNode() {}

// PartitionOp is the partition operator — verified against real
// ADX's own partition operator docs before adopting this shape:
// "T | partition [hint.strategy=Strategy] [Hints] by Column
// (TransformationSubQuery)" groups T by Column's distinct values,
// runs SubQuery independently against each group's implicit subtable
// (matching MvApplyOp's own Operators []Operator shape immediately
// above — a sourceless subquery pipeline, not a full Query with its
// own Source), and returns the union of every partition's result.
//
// hint.strategy=/other hint.xxx= prefixes (native/shuffle/legacy —
// real ADX's own distributed-execution strategy hints) are recognized
// and silently skipped, the same principle already applied to
// evaluate's own evaluateParameters (evaluate.go/EvaluateOp): this
// engine has no distributed execution to hint at all, so the
// STRATEGY choice is meaningless here, only the semantics (group,
// run per group, union) matter and are preserved regardless of which
// hint, if any, was written.
//
// Deliberately scoped, not a full clone of every real-ADX partition
// capability: only the "(TransformationSubQuery)" implicit-source
// form is supported. Real ADX's OTHER form —
// "partition [hint.strategy=legacy] by Column {SubQueryWithSource}",
// braces, an explicit tabular source inside referencing the current
// partition's key via toscalar(Column) — is legacy-strategy-only and
// real ADX's own docs frame it as a fallback "in some scenarios...
// due to its support for including a tabular source", not the primary
// form; not built here. A query using it gets a clear parse error
// naming the gap, not silent mis-parsing.
type PartitionOp struct {
	ByColumn  string
	Operators []Operator
}

func (*PartitionOp) operatorNode() {}

// RenderOp: | render <visualization> [with (prop=val, ...)]
// Parsed and carried as metadata for KQL compatibility; the engine treats
// it as a pass-through (no visualization is produced). Many real-world
// queries end with "| render timechart" and must not fail to parse.
type RenderOp struct {
	Visualization string // e.g. timechart, barchart, piechart, table
	With          string // raw contents of the optional with (...) clause
}

func (*RenderOp) operatorNode() {}

// LookupOp: lookup [kind=X] TableName on col1[, col2]
// Simplified join for dimension table enrichment.
// Default kind is leftouter (unlike join which defaults to inner).
type LookupOp struct {
	Kind      JoinKind
	TableName string          // Right-side table name
	OnClauses []JoinCondition // Join key pairs
}

func (*LookupOp) operatorNode() {}

// MvExpandOp flattens array-valued columns into separate rows.
// Each element in the array becomes its own row, with other columns duplicated.
// Supports: | mv-expand Col or | mv-expand NewName = Col
type MvExpandOp struct {
	Columns []MvExpandColumn
}

func (*MvExpandOp) operatorNode() {}

// MvExpandColumn defines a single column to expand.
type MvExpandColumn struct {
	Name   string // Output column name (may differ from source if aliased)
	Source Expr   // Source expression (typically a ColumnRef)
}

// UnionOp stacks rows from additional tables onto the input.
// Supports both pipe form (T1 | union T2) and standalone (union T1, T2).
// Each source can be a plain table or a subquery with operators.
type UnionOp struct {
	Sources []*Query // Additional table sources to union
}

func (*UnionOp) operatorNode() {}

// JoinKind specifies the type of join.
type JoinKind int

const (
	JoinInner       JoinKind = iota // inner — only matching rows
	JoinInnerUnique                 // innerunique — inner join, left side deduplicated by join key first (real ADX's default — see join.go for why okql's default matches it too)
	JoinLeftOuter                   // leftouter — all left rows, nulls for unmatched right
	JoinRightOuter                  // rightouter — all right rows, nulls for unmatched left
	JoinFullOuter                   // fullouter — all rows from both sides
	JoinLeftAnti                    // leftanti — left rows with NO match
	JoinLeftSemi                    // leftsemi — left rows WITH a match (no right columns)
	JoinRightAnti                   // rightanti — right rows with NO match
	JoinRightSemi                   // rightsemi — right rows WITH a match (no left columns)
)

func (k JoinKind) String() string {
	switch k {
	case JoinInner:
		return "inner"
	case JoinInnerUnique:
		return "innerunique"
	case JoinLeftOuter:
		return "leftouter"
	case JoinRightOuter:
		return "rightouter"
	case JoinFullOuter:
		return "fullouter"
	case JoinLeftAnti:
		return "leftanti"
	case JoinLeftSemi:
		return "leftsemi"
	case JoinRightAnti:
		return "rightanti"
	case JoinRightSemi:
		return "rightsemi"
	default:
		return "inner"
	}
}

// JoinCondition specifies a pair of columns to join on.
type JoinCondition struct {
	LeftColumn  string // Column name from left side
	RightColumn string // Column name from right side (same as Left for simple `on Col` syntax)
}

// --- Expressions ---

// Expr is an expression node in a predicate or computed column.
type Expr interface {
	exprNode()
}

// ColumnRef references a column by name.
type ColumnRef struct {
	Name string
}

func (*ColumnRef) exprNode() {}

// ToScalarExpr is toscalar(tabular_expression) — real ADX's own
// bridge from the tabular world into a scalar context, verified
// against Microsoft's own toscalar() docs before adopting this shape:
// executes a tabular expression and takes the first column of the
// first row. QueryText is kept as raw, unparsed text (the same
// "defer kind-specific parsing to evaluation time" principle already
// used for stored functions' tabular arguments — StoredFunctionCall's
// own doc comment explains why this parser can't tell scalar from
// tabular purely syntactically).
//
// Deliberately never evaluated by evalExpr itself (eval.go has no
// case for this type at all, by design) — a first design pass tried
// exactly that, via a package-level "current engine" variable
// (matching activeLetContext's own existing pattern), and go test
// -race caught a real, live bug in it before it shipped: this
// engine's own tests (TestDiscoveryConcurrentIngestStress) prove
// multiple INDEPENDENT Engine instances run truly concurrently, each
// on its own goroutine, so a shared, package-level Engine reference
// written at the top of every Execute call meant one goroutine's
// toscalar() could genuinely execute against a DIFFERENT goroutine's
// engine -- not just a race-detector technicality, a real
// wrong-engine correctness bug. Making the write atomic would have
// only made that bug memory-safe, not correct.
//
// Fixed instead by leaning into toscalar()'s own real semantics: real
// ADX itself only ever evaluates it a bounded number of times per
// query, never per row (a hard restriction there). This engine
// exploits that same property structurally instead of enforcing it as
// an error: substituteToScalars (eval.go) walks an operator's
// expression ONCE, per operator invocation, BEFORE any per-row loop
// begins, replacing every ToScalarExpr with a Literal holding its
// already-computed value -- called from operator methods that already
// have a real, in-scope, unshared *Engine (applyWhere, applyExtend,
// executeCompound's own scalar lets, ...), never through shared
// package state at all. By the time evalExpr itself runs per row,
// every ToScalarExpr is already gone from the tree.
type ToScalarExpr struct {
	QueryText string
}

func (*ToScalarExpr) exprNode() {}

// StarExpr is the bare `*` wildcard argument recognized specifically
// within arg_max/arg_min's argument list (parseAggregation) — real
// ADX's own "use a wildcard * to return all columns" syntax, verified
// against Microsoft's own arg_max docs before adopting it. Not a
// general expression-language wildcard; * has no meaning anywhere
// else in this parser and this type is never produced outside
// aggregation-argument parsing.
type StarExpr struct{}

func (*StarExpr) exprNode() {}

// Literal is a constant value.
type Literal struct {
	Value interface{}
	Type  types.KQLType
}

func (*Literal) exprNode() {}

// BinaryExpr is a binary operation: left op right
type BinaryExpr struct {
	Left  Expr
	Op    BinaryOp
	Right Expr
}

func (*BinaryExpr) exprNode() {}

// UnaryExpr is a unary operation: op expr (e.g., not)
type UnaryExpr struct {
	Op   string // "not"
	Expr Expr
}

func (*UnaryExpr) exprNode() {}

// FuncCall is a function invocation: name(args...)
type FuncCall struct {
	Name string
	Args []Expr
}

func (*FuncCall) exprNode() {}

// AccessExpr represents property access on an expression:
//
//	col.property          — dot access
//	col["property"]       — bracket string access
//	col[0]                — bracket index access
//	col.nested.deep.path  — chained access
type AccessExpr struct {
	Object Expr        // The expression being accessed
	Path   []AccessKey // Chain of property accesses
}

func (*AccessExpr) exprNode() {}

// AccessKey is a single property or index access step.
type AccessKey struct {
	Name  string // Property name (for dot or bracket-string access)
	Index int    // Array index (for bracket-int access, -1 if not used)
}

// BetweenExpr represents "col between (low .. high)" or "col !between (low .. high)"
type BetweenExpr struct {
	Expr    Expr
	Low     Expr
	High    Expr
	Negated bool // true for !between
}

func (*BetweenExpr) exprNode() {}

// InExpr is "column in (values...)" or "column in (tabular_expr)".
type InExpr struct {
	Column          Expr   // Left side (typically a ColumnRef)
	Values          []Expr // Literal value list, or nil if using a table ref / subquery
	TableRef        string // Let-bound table name (if Values and SubqueryText are both empty)
	SubqueryText    string // Raw, unparsed tabular subquery text (e.g. "SomeTable | project Y") — real ADX's own "X in (subquery)" form, verified before adopting this shape. Set only when the content inside in(...) fails to parse as an ordinary scalar comma-list at all (see parseInExpr's own doc comment for why detection works this way, purely additively, with zero risk to any already-working case). Resolved at evaluation time via the same substituteToScalars mechanism (eval.go) toscalar() already uses — executed once per operator invocation, its first column's values for every row become an ordinary Values list, reusing the existing, working Values-based evaluation path unchanged.
	Negated         bool   // true for "!in" / "!in~"
	CaseInsensitive bool   // true for "in~" / "!in~"
}

func (*InExpr) exprNode() {}

// HasAnyAllExpr is "column has_any (terms...)" or "column has_all (terms...)".
type HasAnyAllExpr struct {
	Column Expr   // Left side
	Values []Expr // Term list
	All    bool   // true for has_all, false for has_any
}

func (*HasAnyAllExpr) exprNode() {}

// BinaryOp is a comparison or logical operator.
type BinaryOp int

const (
	OpEQ              BinaryOp = iota // ==
	OpNEQ                             // !=
	OpLT                              // <
	OpLTE                             // <=
	OpGT                              // >
	OpGTE                             // >=
	OpAnd                             // and
	OpOr                              // or
	OpAdd                             // +
	OpSub                             // -
	OpMul                             // *
	OpDiv                             // /
	OpMod                             // %
	OpContains                        // contains (case-insensitive substring)
	OpNotContains                     // !contains
	OpHas                             // has (word boundary match)
	OpNotHas                          // !has
	OpStartsWith                      // startswith
	OpEndsWith                        // endswith
	OpIn                              // in
	OpNotIn                           // !in
	OpContainsCS                      // contains_cs (case-sensitive substring)
	OpNotContainsCS                   // !contains_cs
	OpHasCS                           // has_cs (case-sensitive word boundary)
	OpNotHasCS                        // !has_cs
	OpStartsWithCS                    // startswith_cs
	OpNotStartsWith                   // !startswith
	OpNotStartsWithCS                 // !startswith_cs
	OpEndsWithCS                      // endswith_cs
	OpNotEndsWith                     // !endswith
	OpNotEndsWithCS                   // !endswith_cs
	OpMatchesRegex                    // matches regex
	OpCIEQ                            // =~ (case-insensitive equals)
	OpCINEQ                           // !~ (case-insensitive not equals)
	OpLike                            // like (wildcard: * and ?)
	OpNotLike                         // !like
	OpHasPrefix                       // hasprefix (case-insensitive)
	OpNotHasPrefix                    // !hasprefix
	OpHasPrefixCS                     // hasprefix_cs
	OpNotHasPrefixCS                  // !hasprefix_cs
	OpHasSuffix                       // hassuffix (case-insensitive)
	OpNotHasSuffix                    // !hassuffix
	OpHasSuffixCS                     // hassuffix_cs
	OpNotHasSuffixCS                  // !hassuffix_cs
)

func (op BinaryOp) String() string {
	switch op {
	case OpEQ:
		return "=="
	case OpNEQ:
		return "!="
	case OpLT:
		return "<"
	case OpLTE:
		return "<="
	case OpGT:
		return ">"
	case OpGTE:
		return ">="
	case OpAnd:
		return "and"
	case OpOr:
		return "or"
	case OpAdd:
		return "+"
	case OpSub:
		return "-"
	case OpMul:
		return "*"
	case OpDiv:
		return "/"
	case OpMod:
		return "%"
	case OpContains:
		return "contains"
	case OpNotContains:
		return "!contains"
	case OpHas:
		return "has"
	case OpNotHas:
		return "!has"
	case OpStartsWith:
		return "startswith"
	case OpEndsWith:
		return "endswith"
	case OpIn:
		return "in"
	case OpNotIn:
		return "!in"
	case OpContainsCS:
		return "contains_cs"
	case OpNotContainsCS:
		return "!contains_cs"
	case OpHasCS:
		return "has_cs"
	case OpNotHasCS:
		return "!has_cs"
	case OpStartsWithCS:
		return "startswith_cs"
	case OpNotStartsWith:
		return "!startswith"
	case OpNotStartsWithCS:
		return "!startswith_cs"
	case OpEndsWithCS:
		return "endswith_cs"
	case OpNotEndsWith:
		return "!endswith"
	case OpNotEndsWithCS:
		return "!endswith_cs"
	case OpMatchesRegex:
		return "matches regex"
	case OpCIEQ:
		return "=~"
	case OpCINEQ:
		return "!~"
	case OpLike:
		return "like"
	case OpNotLike:
		return "!like"
	case OpHasPrefix:
		return "hasprefix"
	case OpNotHasPrefix:
		return "!hasprefix"
	case OpHasPrefixCS:
		return "hasprefix_cs"
	case OpNotHasPrefixCS:
		return "!hasprefix_cs"
	case OpHasSuffix:
		return "hassuffix"
	case OpNotHasSuffix:
		return "!hassuffix"
	case OpHasSuffixCS:
		return "hassuffix_cs"
	case OpNotHasSuffixCS:
		return "!hassuffix_cs"
	default:
		return "?"
	}
}

// --- Management Commands ---

// LetStatement binds a name to a scalar expression or tabular query.
//
//	let threshold = 5;
//	let risky = SignInLogs | where RiskScore > 80;
type LetStatement struct {
	Name  string
	Value Statement // *Query for tabular, ScalarExpr for scalar
}

func (*LetStatement) statementNode() {}

// ScalarExpr wraps an Expr as a Statement for scalar let bindings.
type ScalarExpr struct {
	Expr Expr
}

func (*ScalarExpr) statementNode() {}

// PrecomputedTable wraps an ALREADY-EXECUTED *types.Table as a
// Statement, for a LetStatement.Value that should bind directly to a
// known result rather than being (re-)executed from its own AST later.
//
// Exists specifically to fix a real, live, pre-existing bug found
// while implementing the invoke operator (2026-08-15): a stored
// function's tabular argument previously carried its own PARSED AST
// forward as the LetStatement's Value (e.g. a bare Query{Source:
// "MyTable"} for a let-bound-table argument), to be executed a SECOND
// time later, inside the callee's own executeCompound call — by which
// point e.letContext has already been swapped to the callee's own
// fresh LetContext (executeCompound always installs one before
// evaluating its own Lets). A table bound in the CALLER's scope (a
// `let`, or an `as`-bound name) is invisible in that fresh context, so
// resolution failed with "table \"MyTable\" not found" for a call as
// ordinary as `let T = datatable(...)[...]; MyFunc((T))` -- confirmed
// live via a minimal repro before deciding this needed a real fix, not
// a workaround. bindStoredFunctionArgs now executes a tabular argument
// exactly ONCE, while the caller's own LetContext is still current,
// and carries the already-computed *types.Table forward via this type
// instead -- which also removes the double-execution cost the
// original code's own doc comment explicitly flagged as an accepted,
// deliberate inefficiency (it was never actually free of a correctness
// cost, just believed to be).
type PrecomputedTable struct {
	Table *types.Table
}

func (*PrecomputedTable) statementNode() {}

// FunctionDef is a user-defined scalar function bound via let:
//
//	let f = (x: long, y: long) { x * y };
//
// The body is a single scalar expression. Parameters are visible inside the
// body as column references; the caller's row scope is not visible (KQL
// semantics: UDFs see only their arguments and other let bindings).
type FunctionDef struct {
	Params []FunctionParam
	Body   Expr
}

func (*FunctionDef) statementNode() {}

// FunctionParam is a typed parameter — shared by BOTH the existing
// query-scoped `let` function mechanism (FunctionDef, above) and
// stored functions (CreateFunctionCmd, below), rather than
// maintaining two near-identical types. HasDefault/Default matter
// only for stored functions — real ADX's own
// "ParameterName: ParameterDataType[= default]" shape (verified
// before adopting it); FunctionDef's `let`-scoped functions simply
// never set them. Tabular parameters (T:(Col:type), a whole table as
// an argument — real ADX supports these too, for either kind of
// function) are deliberately not built here; every motivating use
// case for stored-function parameters
// (CascadeCheck("F123")-style single-value lookups) is scalar-only.
type FunctionParam struct {
	Name       string
	Type       types.KQLType
	HasDefault bool
	Default    Expr // nil unless HasDefault. Only ever set for scalar params -- real ADX restricts defaults to scalar parameters only, verified before relying on this.

	// Tabular parameter fields -- IsTabular distinguishes "T:(...)"
	// from a scalar "name:type". Verified against real ADX's own
	// user-defined-functions docs before adopting this shape: a
	// tabular parameter's ArgType has the same syntax as a table
	// definition (column name/type pairs), plus a solitary (*)
	// meaning "any tabular schema" (IsAnySchema=true, TabularSchema
	// left empty, no schema validation at call time at all). When
	// IsTabular is true, Type/HasDefault/Default are unused --
	// tabular parameters never have default values in real ADX,
	// verified rather than assumed.
	IsTabular     bool
	IsAnySchema   bool // T:(*) -- true means "any schema", TabularSchema is empty and unused
	TabularSchema []TabularColumn
}

// TabularColumn is one column-name/type pair in a tabular parameter's
// declared schema — deliberately a distinct, small type rather than
// reusing FunctionParam recursively for this, since a schema column
// has no name/type-only shape at all in common with a parameter
// (no defaults, no tabular-ness of its own).
type TabularColumn struct {
	Name string
	Type types.KQLType
}

// CompoundStatement is a sequence of let bindings followed by a final statement.
type CompoundStatement struct {
	Lets  []*LetStatement
	Final Statement // The query or command that produces output
}

func (*CompoundStatement) statementNode() {}

// CreateTableCmd: .create table T (col1: type1, col2: type2, ...)
type CreateTableCmd struct {
	TableName string
	Schema    types.Schema

	// NoTimeReceived opts THIS table out of the automatic _TimeReceived
	// column (see engine/timereceived.go) via .create table T (...)
	// with (notimereceived=true) -- per-table override of the scope's
	// own default (itself set via .okql-schema-options.json), for a
	// scope that wants the column on most tables but not this
	// specific one, or vice versa when the scope default is off.
	NoTimeReceived bool
}

func (*CreateTableCmd) statementNode() {}

// CreateMergeTableCmd: .create-merge table T (col1: type1, ...)
type CreateMergeTableCmd struct {
	TableName string
	Schema    types.Schema
}

func (*CreateMergeTableCmd) statementNode() {}

// DropTableCmd: .drop table T
type DropTableCmd struct {
	TableName string
}

func (*DropTableCmd) statementNode() {}

// ShowTablesCmd: .show tables
type ShowTablesCmd struct{}

func (*ShowTablesCmd) statementNode() {}

// ShowTableExtentsCmd: .show table T extents
type ShowTableExtentsCmd struct {
	TableName string
}

func (*ShowTableExtentsCmd) statementNode() {}

// ShowDatabaseCmd: .show database
type ShowDatabaseCmd struct{}

func (*ShowDatabaseCmd) statementNode() {}

// IngestInlineCmd: .ingest inline into table T <| data
type IngestInlineCmd struct {
	TableName string
	Data      string // Raw CSV data after <|
}

func (*IngestInlineCmd) statementNode() {}

// DropExtentCmd: .drop extent <guid>
type DropExtentCmd struct {
	ExtentID string
}

func (*DropExtentCmd) statementNode() {}

// IngestCSVCmd: .ingest csv into table T from "path"
type IngestCSVCmd struct {
	TableName string
	FilePath  string
}

func (*IngestCSVCmd) statementNode() {}

// SetCmd: .set T <| query
type SetCmd struct {
	TableName string
	Query     *Query
}

func (*SetCmd) statementNode() {}

// SetOrAppendCmd: .set-or-append T <| query — appends the query's
// result as a new extent onto an existing table (creates it first if
// absent, matching real Kusto set-or-append semantics). The common
// case is a literal datatable(...) [...] as the query, giving
// idiomatic typed-literal row insertion with no CSV involved at all.
type SetOrAppendCmd struct {
	TableName string
	Query     *Query
}

func (*SetOrAppendCmd) statementNode() {}

// MergeExtentsCmd: .merge table T extents
// Compacts all extents into optimally-sized files with zone maps.
type MergeExtentsCmd struct {
	TableName string
}

func (*MergeExtentsCmd) statementNode() {}

// MakeGraphOp constructs a directed graph from an edge table:
//
//	Edges | make-graph Source --> Target [with Nodes on NodeId]
//
// The result flowing down the pipeline is a graph, not a table; only
// graph operators (graph-to-table, graph-match) may follow it.
type MakeGraphOp struct {
	SourceColumn string // edge-table column holding the source node id
	TargetColumn string // edge-table column holding the target node id
	NodesTable   string // optional node table name ("" = nodes derived from edges)
	NodeIdColumn string // node id column in the node table (set iff NodesTable != "")
}

func (*MakeGraphOp) operatorNode() {}

// GraphToTableOp materializes a graph back into tabular form:
//
//	... | graph-to-table edges   (default when omitted)
//	... | graph-to-table nodes
//
// edges: the original edge rows (edge schema preserved).
// nodes: node-table rows when a node table was supplied (edge-only nodes get
// nulls for property columns); otherwise a single NodeId column.
type GraphToTableOp struct {
	Output string // "edges" or "nodes"
}

func (*GraphToTableOp) operatorNode() {}

// GraphMatchOp matches a path pattern against a graph:
//
//	... | graph-match (a)-[e]->(b) where a.Kind == "External" project a.Id, b.Id
//
// The pattern is a chain of nodes and directed edges; len(Nodes) == len(Edges)+1.
// Pattern variables surface as dotted columns in the match schema: node
// variables expose the node table's columns as "var.Col" (or "var.NodeId"
// when nodes were derived from edges), edge variables expose the edge
// table's columns as "var.Col". Anonymous elements ("()" / "-[]->") bind
// no columns. The project clause is required, per KQL.
type GraphMatchOp struct {
	Nodes   []string         // node variable names ("" = anonymous)
	Edges   []GraphMatchEdge // directed edges between consecutive nodes
	Where   Expr             // optional filter over pattern variables (nil = none)
	Project *ProjectOp       // required output projection
}

// GraphMatchEdge is one edge element of a graph-match pattern.
// MinHops == MaxHops == 1 for a fixed edge -[e]->; variable-length
// edges -[e*1..5]-> carry the hop range.
type GraphMatchEdge struct {
	Name    string // "" = anonymous
	MinHops int
	MaxHops int
	// Direction of traversal relative to stored edge direction:
	// forward (a)-[e]->(b), backward (a)<-[e]-(b), any (a)-[e]-(b).
	Direction EdgeDirection
	// Distinct requests BFS reachable-NODE semantics instead of the
	// default path-enumeration semantics, for variable-length edges:
	// -[e*1..3 distinct]-> visits each node at most once (its
	// shallowest discovered depth) instead of enumerating every path
	// to it. Path-enumeration is still the default because it's
	// needed for evidence-path rendering (distinct would silently
	// drop alternate routes); Distinct is the right choice for pure
	// reachability/disclosure queries, where hub-node fan-out makes
	// path enumeration combinatorial while node reachability stays
	// linear in graph size. Only valid on a single-edge-spec pattern
	// (start)-[e*min..max]-(end) — rejected at plan time otherwise,
	// since node-identity dedup doesn't compose with continuing the
	// rest of a longer pattern.
	Distinct bool
}

// EdgeDirection is a graph-match edge traversal direction.
type EdgeDirection int

const (
	EdgeForward EdgeDirection = iota
	EdgeBackward
	EdgeAny
)

// MaxGraphHops caps the max of a variable-length hop range. Traversal
// cost grows combinatorially with hops; real attack-path patterns sit
// well under this.
const MaxGraphHops = 64

func (*GraphMatchOp) operatorNode() {}

// HelpCmd: .help — lists supported dot-commands. Found live during
// the backlog pass: no way to discover .drop extent existed short of
// reading parser source.
type HelpCmd struct{}

func (*HelpCmd) statementNode() {}

// SearchOp: search "term" or search in (T1, T2) "term" — scans string
// columns across tables for a term (word-bounded, case-insensitive,
// same semantics as `has`). Tables empty = every table in the catalog.
// Deliberately scoped: real Kusto's search unions full heterogeneous
// row schemas across tables with null-padding for absent columns; this
// returns a normalized (TableName, Column, RowKey, Value) hit list
// instead, which answers the practical "where does this term appear
// anywhere in my scope" need without the harder exact-fidelity problem.
type SearchOp struct {
	Term   string
	Tables []string // nil/empty = all tables
}

func (*SearchOp) operatorNode() {}

// FindOp is the find operator — the older, cross-table search
// predecessor to search (SearchOp, immediately above), still seen in
// real-world queries and docs despite being the less modern form.
// Verified against real ADX's own find operator docs before adopting
// this shape: "find [withsource=ColumnName] [in (Tables)] where
// Predicate [project-smart | project ColumnName[:ColumnType,...]
// [, pack_all()]]" or, WITHOUT in(...) at all, the shorter
// "find Predicate [project ...]" form (no where keyword in that
// second form — a real, easy-to-miss grammar distinction, verified
// directly rather than assumed).
//
// Deliberately scoped, not a full clone of every real-ADX find
// capability: wildcard table-name matching (E*), cross-database, and
// cross-cluster scope are NOT supported (Tables is always an explicit
// list, or empty meaning every table in the CURRENT database only) —
// this engine's own catalog model has no multi-database/cluster
// concept to extend into in the first place, and real ADX's own docs
// note find already "falls back to a union query" (real ADX's own,
// separate, already-implemented operator here) once tabular
// expressions are involved rather than plain tables, so scoping to
// real, named tables in the current database covers find's own core,
// most common use case.
//
// AnyColumnTerm is set instead of Predicate for the bare-term/`* has
// term` forms (find "Hernandez", find in (T) where * has "Kusto") —
// these search every column of a row rather than evaluate a normal,
// column-bound boolean expression, which isn't representable as an
// ordinary Expr at all (there is no valid ColumnRef for the literal
// `*` token). Predicate is nil when AnyColumnTerm is set, and vice
// versa.
// FindProjectItem is one entry in find's own explicit
// "project ColumnName[:ColumnType], ..." clause — a dedicated type,
// not the ordinary ProjectItem (which has no field for an optional
// type annotation at all, since ordinary project always infers a
// computed column's type from its expression instead of accepting an
// explicit cast). Type is the zero value (meaning "not specified,
// infer/pass through the source column's own type") when no
// ":ColumnType" suffix was given.
type FindProjectItem struct {
	Name string
	Type types.KQLType
}

type FindOp struct {
	WithSource    string // renamed source_ column; "" means the real-ADX default "source_"
	Tables        []string
	AnyColumnTerm string
	Predicate     Expr
	ProjectSmart  bool // true (the real-ADX default) when no explicit project clause was given
	ProjectItems  []FindProjectItem
	PackAll       bool
}

func (*FindOp) operatorNode() {}

// EmbedIntoCmd: .embed-into T <| query — bulk-embeds a Text column
// through Ollama's batch endpoint (one HTTP call per batch, not one
// per row) and writes the result as a new extent on T. The query must
// project at least Id and Text columns; Model and Provenance columns
// are optional (defaults applied if absent).
type EmbedIntoCmd struct {
	TableName string
	Query     *Query
}

func (*EmbedIntoCmd) statementNode() {}

// ChunkFileCmd: .chunk-file "path" into T — reads a markdown file,
// splits it into paragraph-level chunks tagged with heading-trail
// context, auto-splitting oversized blocks, and writes into T.
type ChunkFileCmd struct {
	Path      string
	TableName string
}

func (*ChunkFileCmd) statementNode() {}

// CompactTableCmd: .compact table T [where <predicate>] —
// discovery-mode-only extent compaction. See engine/compact.go for the
// safety analysis: this is the one operation in the discovery-mode
// storage model that is NOT safe under concurrent access, and that has
// to stay true in the implementation and documentation both.
//
// Where, if non-nil, is evaluated per row (same evalExpr machinery as
// an ordinary | where operator, including access to any let-bound
// tables active from an enclosing compound statement) against the
// UNION of every extent being compacted; only rows for which it
// evaluates true survive into the new, compacted extent. This is what
// lets compaction selectively drop logically-obsolete rows (e.g. a
// memory scope's superseded Findings, identified by an anti-join
// against a `supersedes`-typed Edges row) instead of just merging
// every row from every extent verbatim, which is all a nil Where does.
type CompactTableCmd struct {
	TableName string
	Where     Expr
}

func (*CompactTableCmd) statementNode() {}

// GCTableCmd: .gc table T — physically removes files .compact
// superseded (renamed to *.vtx.superseded). Safe to run at any time;
// see engine/compact.go for why.
type GCTableCmd struct {
	TableName string
}

func (*GCTableCmd) statementNode() {}

// CreateFunctionCmd defines a stored (persisted, tabular) function —
// real ADX's own distinction from a query-scoped `let` function
// (parser.FunctionDef, already supported, unrelated). Verified against
// Microsoft's own .create-or-alter function / .create function docs
// before adopting this shape. Scalar parameters only — real ADX also
// supports tabular parameters, deliberately not built here.
type CreateFunctionCmd struct {
	Name           string
	Parameters     []FunctionParam
	ParametersText string // raw parameter-list text (between the parens), kept alongside the parsed Parameters above — same "keep the raw text, don't re-serialize a parsed form" approach Body already uses, since a default value's Expr can't be safely round-tripped back to text in general.
	Body           string // raw KQL text, re-parsed and executed at call time — see engine/stored_functions.go
	DocString      string
	Folder         string
	OrAlter        bool // .create-or-alter: upsert. false = .create: errors on redefinition unless IfNotExists.
	IfNotExists    bool
}

func (*CreateFunctionCmd) statementNode() {}

// ShowFunctionsCmd lists every stored function — .show functions.
type ShowFunctionsCmd struct{}

func (*ShowFunctionsCmd) statementNode() {}

// ShowFunctionCmd shows one stored function's details — .show function Name.
type ShowFunctionCmd struct {
	Name string
}

func (*ShowFunctionCmd) statementNode() {}

// DropFunctionCmd drops one or more stored functions. Verified real
// ADX semantics differ between the singular and plural forms, not
// just syntax: .drop function Name errors if it doesn't exist;
// .drop functions (A, B, C) silently tolerates missing ones. Plural
// tracked via len(Names) > 1 OR ExplicitPlural (the plural form is
// still valid syntax for a single name, per real ADX, so the FORM
// used — not just the count — determines which semantics apply).
type DropFunctionCmd struct {
	Names          []string
	ExplicitPlural bool
}

func (*DropFunctionCmd) statementNode() {}

// CreateMaterializedViewCmd defines a materialized view — real ADX's
// own maintained-state aggregation layer, verified against Microsoft's
// own .create materialized-view docs before adopting this shape.
// Query must be a single-summarize aggregation query over SourceTable
// (validated in engine/materialized_views.go, not here — parsing
// stays purely syntactic, matching every other command in this
// parser). Scoped deliberately: no async/backfill/lookback/
// dimensionTables/autoUpdateSchema/MV-over-MV in this first version —
// every one of those is real ADX, all deferred.
type CreateMaterializedViewCmd struct {
	Name        string
	SourceTable string
	Query       *Query
	QueryText   string // raw text of the query (between the braces) — kept alongside the parsed Query, same "keep raw text, don't reconstruct from the parsed AST" convention CreateFunctionCmd's Body/ParametersText already established, used for .show materialized-views' display so it shows the real, original text rather than an approximate reconstruction
	DocString   string
	Folder      string
	IfNotExists bool
}

func (*CreateMaterializedViewCmd) statementNode() {}

// ShowMaterializedViewsCmd lists every materialized view — .show materialized-views.
type ShowMaterializedViewsCmd struct{}

func (*ShowMaterializedViewsCmd) statementNode() {}

// DropMaterializedViewCmd drops one materialized view — .drop materialized-view Name.
type DropMaterializedViewCmd struct {
	Name string
}

func (*DropMaterializedViewCmd) statementNode() {}

// AlterMergeTableCmd is .alter-merge table T (col1:type1, ...) —
// verified against real ADX's own .alter-merge table docs before
// adopting this shape: adds new columns to an EXISTING table's
// schema, appended at the end; existing data is never modified or
// deleted; a column name that already exists but is given a
// DIFFERENT type here is a hard error ("if you try to alter a column
// type, the command will fail"), not silently accepted or coerced.
// Unlike CreateMergeTableCmd (.create-merge table), the target table
// must already exist -- this command never creates one.
type AlterMergeTableCmd struct {
	TableName  string
	NewColumns types.Schema
	DocString  string
	Folder     string
}

func (*AlterMergeTableCmd) statementNode() {}

// CompactDatabaseCmd compacts every table in the scope, including
// system tables like _Dictionaries that ListTables/.show tables
// deliberately excludes from generic enumeration — see
// Catalog.ListAllTables's doc comment for why a maintenance operation
// specifically must not use that same filtered listing.
type CompactDatabaseCmd struct{}

func (*CompactDatabaseCmd) statementNode() {}

// GCDatabaseCmd physically removes .superseded files for every table
// in the scope, including system tables — same reasoning as
// CompactDatabaseCmd.
type GCDatabaseCmd struct{}

func (*GCDatabaseCmd) statementNode() {}

// PipedCommand wraps a simple, self-contained dot-command (one with no
// internal query grammar of its own — see pipeableSimpleCommandPrefixes
// in parser.go) followed by ordinary pipeline operators, e.g.
// `.show tables | where TableName == "Findings"`. Execution runs Inner
// to get a result table, then applies Operators to it exactly like any
// other query pipeline.
type PipedCommand struct {
	Inner     Statement
	Operators []Operator
}

func (*PipedCommand) statementNode() {}
