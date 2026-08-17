package engine

// stored_functions.go — stored (persisted, tabular) functions via
// .create-or-alter function / .create function [ifnotexists],
// resolved when a query source is FuncName(...)
// (parser.Query.SourceFuncCall, recognized in parseQuery). Distinct
// from parser.FunctionDef, the existing query-scoped `let` function
// mechanism, which is unaffected by anything in this file. Also
// distinct from functions.go (this package's pre-existing file for
// built-in SCALAR function evaluation, e.g. strcat/case) — named
// separately, deliberately, after an earlier version of this work
// briefly overwrote that file entirely via a naive `cat >` (caught
// immediately by go build failing on now-undefined built-in-function
// symbols, reverted via git checkout before anything was committed).
//
// Verified against Microsoft's own .create-or-alter function /
// .create function / .show functions / .drop function /
// user-defined-functions docs before building each increment of this
// — see the design conversations this responds to. Supports scalar
// parameters (with optional defaults) and tabular parameters
// (Name:(col:type,...) or Name:(*) for "any schema", real ADX's own
// syntax) — no argument-substitution into stored body text anywhere:
// both kinds bind via synthetic let statements prepended to the
// body's own CompoundStatement, resolved through the exact same
// machinery an ordinary `let x = 5; ...` or `let T = SomeQuery; ...`
// already uses. Not built: the `invoke` operator (a separate, real
// ADX syntax for piping a table directly into a tabular function's
// first argument), and cross-cluster invocation (real ADX explicitly
// disallows this for any function taking a tabular argument anyway).
//
// Storage: an append-only _Functions system table (Name, Body,
// DocString, Folder, Deleted, CreatedAt), same shape and same
// latest-wins-by-name-via-max(CreatedAt) convention this whole
// session's Claude-Memory schema work already established elsewhere —
// a redefinition (.create-or-alter) or a drop is a new row, never an
// update to or deletion of an existing one. .drop function appends a
// Deleted=true tombstone row rather than removing anything.

import (
	"fmt"
	"strings"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

const functionsTableName = "_Functions"

func storedFunctionsSchema() types.Schema {
	return types.Schema{Columns: []types.Column{
		{Name: "Name", Type: types.TypeString},
		{Name: "Parameters", Type: types.TypeString}, // raw parameter-list text, e.g. "x: long, y: string = \"d\"" — re-parsed via parser.ParseFunctionParams at call time, same as Body already is
		{Name: "Body", Type: types.TypeString},
		{Name: "DocString", Type: types.TypeString},
		{Name: "Folder", Type: types.TypeString},
		{Name: "Deleted", Type: types.TypeBool},
		{Name: "CreatedAt", Type: types.TypeDatetime},
	}}
}

// ensureFunctionsTable creates _Functions if it doesn't exist yet —
// mirrors how every other system/user table in this codebase is
// lazily created on first real use, not eagerly at Engine construction.
func (e *Engine) ensureFunctionsTable() error {
	if e.Catalog.GetTable(functionsTableName) != nil {
		return nil
	}
	if err := e.Catalog.CreateTable(functionsTableName, storedFunctionsSchema()); err != nil {
		return err
	}
	return e.persistDiscoverySchema(functionsTableName, storedFunctionsSchema())
}

// storedFunction is the resolved, latest-wins-by-name view of one
// _Functions entry.
type storedFunction struct {
	Name       string
	Parameters string // raw text, re-parsed via parser.ParseFunctionParams at call time
	Body       string
	DocString  string
	Folder     string
	Deleted    bool
}

// lookupStoredFunction scans _Functions for every row matching name
// and returns the one with the latest CreatedAt — the same arg_max-
// style "latest by key" resolution this whole session's memory-scope
// work already relies on elsewhere, applied to function definitions
// instead of Findings/Tasks rows. everDefined=false covers "never
// defined at all"; everDefined=true with fn.Deleted=true means
// "existed, currently dropped" — callers that need to distinguish the
// two (e.g. .show function's error message) check fn.Deleted.
func (e *Engine) lookupStoredFunction(name string) (fn storedFunction, everDefined bool, err error) {
	if e.Catalog.GetTable(functionsTableName) == nil {
		return storedFunction{}, false, nil
	}
	all, err := e.executeQuery(&parser.Query{Source: functionsTableName})
	if err != nil {
		return storedFunction{}, false, fmt.Errorf("reading %s: %w", functionsTableName, err)
	}
	nameIdx := all.Schema.ColumnIndex("Name")
	paramsIdx := all.Schema.ColumnIndex("Parameters")
	bodyIdx := all.Schema.ColumnIndex("Body")
	docIdx := all.Schema.ColumnIndex("DocString")
	folderIdx := all.Schema.ColumnIndex("Folder")
	delIdx := all.Schema.ColumnIndex("Deleted")
	createdIdx := all.Schema.ColumnIndex("CreatedAt")

	// CreatedAt is stored as UnixNano (int64) — matching
	// types.ParseValue's own TypeDatetime representation exactly (it
	// returns t.UnixNano(), not a time.Time), which every write in
	// this file must match too. A first version of this code wrote
	// time.Time values directly into the row instead, which the
	// storage layer didn't recognize for a datetime column and
	// silently stored as the zero value for every single row — caught
	// live: every _Functions row showed CreatedAt=1970-01-01, so
	// "latest by CreatedAt" degenerated into "first row encountered
	// wins" (every comparison tied, After() never returned true),
	// meaning a dropped function's tombstone could never actually beat
	// its own original definition. Fixed by storing and comparing
	// int64 UnixNano throughout, matching the real, working convention
	// the rest of this codebase already uses.
	var latest *types.Row
	var latestCreated int64
	for i := range all.Rows {
		row := all.Rows[i]
		if fmt.Sprintf("%v", row[nameIdx]) != name {
			continue
		}
		everDefined = true
		created, _ := row[createdIdx].(int64)
		if latest == nil || created > latestCreated {
			latest = &all.Rows[i]
			latestCreated = created
		}
	}
	if latest == nil {
		return storedFunction{}, false, nil
	}
	r := *latest
	deleted, _ := r[delIdx].(bool)
	return storedFunction{
		Name:       fmt.Sprintf("%v", r[nameIdx]),
		Parameters: fmt.Sprintf("%v", r[paramsIdx]),
		Body:       fmt.Sprintf("%v", r[bodyIdx]),
		DocString:  fmt.Sprintf("%v", r[docIdx]),
		Folder:     fmt.Sprintf("%v", r[folderIdx]),
		Deleted:    deleted,
	}, everDefined, nil
}

// listStoredFunctions returns the latest, non-deleted definition for
// every distinct name in _Functions — the basis for .show functions.
func (e *Engine) listStoredFunctions() ([]storedFunction, error) {
	if e.Catalog.GetTable(functionsTableName) == nil {
		return nil, nil
	}
	all, err := e.executeQuery(&parser.Query{Source: functionsTableName})
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", functionsTableName, err)
	}
	nameIdx := all.Schema.ColumnIndex("Name")
	seen := make(map[string]bool)
	var names []string
	for _, row := range all.Rows {
		n := fmt.Sprintf("%v", row[nameIdx])
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	var out []storedFunction
	for _, n := range names {
		fn, _, err := e.lookupStoredFunction(n)
		if err != nil {
			return nil, err
		}
		if !fn.Deleted {
			out = append(out, fn)
		}
	}
	return out, nil
}

// applyCreateFunction handles both .create-or-alter function and
// .create function [ifnotexists] — same underlying append, different
// pre-checks. Also enforces a namespace-collision check against
// existing TABLE names: inferred from real ADX's own materialized-view
// docs, which state a view name "can't conflict with table or
// function names in the same database" — implying tables, functions,
// and views already share one namespace in real ADX, even though this
// specific rule wasn't independently verified from the function-only
// docs. Kept as an explicit, honest inference rather than presented as
// directly confirmed.
func (e *Engine) applyCreateFunction(cmd *parser.CreateFunctionCmd) (*types.Table, error) {
	if td := e.Catalog.GetTable(cmd.Name); td != nil {
		return nil, fmt.Errorf("create function %q: a table with this name already exists", cmd.Name)
	}

	existing, everDefined, err := e.lookupStoredFunction(cmd.Name)
	if err != nil {
		return nil, err
	}
	if everDefined && !existing.Deleted {
		if cmd.IfNotExists {
			return okResult("function already exists, ifnotexists: no change"), nil
		}
		if !cmd.OrAlter {
			return nil, fmt.Errorf("create function %q: already exists (use .create-or-alter function to redefine, or .create function ifnotexists)", cmd.Name)
		}
		// OrAlter: fall through and append the new definition.
	}

	if err := e.ensureFunctionsTable(); err != nil {
		return nil, err
	}
	tableDef := e.Catalog.GetTable(functionsTableName)
	row := types.Row{cmd.Name, cmd.ParametersText, cmd.Body, cmd.DocString, cmd.Folder, false, time.Now().UTC().UnixNano()}
	if _, err := e.flushBatch(functionsTableName, tableDef, []types.Row{row}); err != nil {
		return nil, fmt.Errorf("create function %q: %w", cmd.Name, err)
	}
	return okResult("OK"), nil
}

// applyShowFunctions implements .show functions — same output shape
// as real ADX, verified against a real example row before matching it:
// Name, Parameters, Body, Folder, DocString, with Parameters formatted
// as "(name: type, ...)" matching the real example row exactly
// ("(myLimit: long)").
func (e *Engine) applyShowFunctions() (*types.Table, error) {
	fns, err := e.listStoredFunctions()
	if err != nil {
		return nil, err
	}
	result := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "Name", Type: types.TypeString},
		{Name: "Parameters", Type: types.TypeString},
		{Name: "Body", Type: types.TypeString},
		{Name: "Folder", Type: types.TypeString},
		{Name: "DocString", Type: types.TypeString},
	}})
	for _, fn := range fns {
		result.AddRow(types.Row{fn.Name, "(" + fn.Parameters + ")", "{" + fn.Body + "}", fn.Folder, fn.DocString})
	}
	return result, nil
}

// applyShowFunction implements .show function Name — same columns as
// applyShowFunctions, a single matching row.
func (e *Engine) applyShowFunction(cmd *parser.ShowFunctionCmd) (*types.Table, error) {
	fn, everDefined, err := e.lookupStoredFunction(cmd.Name)
	if err != nil {
		return nil, err
	}
	if !everDefined || fn.Deleted {
		return nil, fmt.Errorf(".show function: %q does not exist", cmd.Name)
	}
	result := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "Name", Type: types.TypeString},
		{Name: "Parameters", Type: types.TypeString},
		{Name: "Body", Type: types.TypeString},
		{Name: "Folder", Type: types.TypeString},
		{Name: "DocString", Type: types.TypeString},
	}})
	result.AddRow(types.Row{fn.Name, "(" + fn.Parameters + ")", "{" + fn.Body + "}", fn.Folder, fn.DocString})
	return result, nil
}

// applyDropFunction implements both .drop function Name (errors if
// missing) and .drop functions (A, B, C) (silently tolerates missing
// ones) — genuinely different semantics per real ADX, tracked via
// cmd.ExplicitPlural, not just the name count. A drop appends a
// Deleted=true tombstone row rather than removing anything, matching
// _Functions' append-only design throughout this file.
func (e *Engine) applyDropFunction(cmd *parser.DropFunctionCmd) (*types.Table, error) {
	if err := e.ensureFunctionsTable(); err != nil {
		return nil, err
	}
	tableDef := e.Catalog.GetTable(functionsTableName)

	var dropped []string
	for _, name := range cmd.Names {
		fn, everDefined, err := e.lookupStoredFunction(name)
		if err != nil {
			return nil, err
		}
		// Found live: everDefined alone isn't "currently exists" — a
		// function that was already dropped is everDefined=true (its
		// tombstone row is real history in _Functions) but fn.Deleted
		// is also true, and from the user's perspective it does NOT
		// currently exist. Dropping an already-dropped function must
		// be treated the same as dropping one that was never defined
		// at all — checked here explicitly after this exact case
		// produced a wrong "succeeded" result during testing instead
		// of the expected error.
		if !everDefined || fn.Deleted {
			if !cmd.ExplicitPlural {
				return nil, fmt.Errorf("drop function %q: does not exist", name)
			}
			continue // plural form: silently tolerate a missing/already-dropped name
		}
		row := types.Row{name, "", "", "", "", true, time.Now().UTC().UnixNano()}
		if _, err := e.flushBatch(functionsTableName, tableDef, []types.Row{row}); err != nil {
			return nil, fmt.Errorf("drop function %q: %w", name, err)
		}
		dropped = append(dropped, name)
	}

	if !cmd.ExplicitPlural {
		// Singular form: real ADX returns the details of the removed function.
		result := types.NewTable("", types.Schema{Columns: []types.Column{
			{Name: "Name", Type: types.TypeString},
		}})
		if len(dropped) > 0 {
			result.AddRow(types.Row{dropped[0]})
		}
		return result, nil
	}

	// Plural form: real ADX returns the list of REMAINING functions.
	return e.applyShowFunctions()
}

// resolveStoredFunction is called from executeQuery when
// q.SourceFuncCall names a function — looks up its latest, non-deleted
// definition, re-parses the stored body text, executes it, and
// returns the result for the calling executeQuery to hand to
// applyPipeline exactly as federation's resolveFederatedTable already
// does for its own source kind — same "resolve at the source, every
// operator after it is unaware anything special happened" shape.
//
// Recursion guarded via Engine.resolvingFunctions — real ADX disallows
// function recursion outright (verified, not assumed); here it's not
// just conformance, an unguarded cycle would be genuine infinite
// recursion through repeated Parse+executeQuery calls.
func (e *Engine) resolveStoredFunction(call *parser.StoredFunctionCall) (*types.Table, error) {
	name := call.Name

	// ExternalFunctionResolver (external_resolver.go) is checked FIRST,
	// before this engine's own catalog lookup at all -- same
	// precedence rule this codebase already applies to its own
	// built-in table-valued functions (csv(), json(), ndjson(),
	// parquet(), vortex() are recognized ahead of a stored-function
	// call with the same name too, so a stored function can never
	// shadow a built-in even if someone defines one with that name;
	// the built-in always wins). A host-registered external resolver
	// gets that identical guarantee for its own function names.
	//
	// ok=true and err != nil is propagated directly here, NOT allowed
	// to fall through to the normal lookup below -- see
	// ExternalFunctionResolver's own doc comment for why: falling
	// through would silently swap a real, specific external-resolution
	// error for a confusing, unrelated "no such stored function" for a
	// name the resolver just claimed as its own.
	if e.ExternalResolver != nil {
		result, ok, err := e.ExternalResolver.ResolveExternalFunction(name, call.ArgTexts)
		if ok {
			if err != nil {
				return nil, fmt.Errorf("function %q: external resolution: %w", name, err)
			}
			return result, nil
		}
	}

	if e.resolvingFunctions[name] {
		return nil, fmt.Errorf("function %q: recursive call detected (not supported, matching real ADX)", name)
	}

	fn, everDefined, err := e.lookupStoredFunction(name)
	if err != nil {
		return nil, err
	}
	if !everDefined || fn.Deleted {
		return nil, fmt.Errorf("%s(): no such stored function", name)
	}

	declared, err := parser.ParseFunctionParams(fn.Parameters)
	if err != nil {
		return nil, fmt.Errorf("function %q: stored parameter list no longer parses: %w", name, err)
	}

	letBindings, err := e.bindStoredFunctionArgs(name, declared, call.ArgTexts)
	if err != nil {
		return nil, err
	}

	stmt, err := parser.Parse(fn.Body)
	if err != nil {
		return nil, fmt.Errorf("function %q: stored body no longer parses: %w", name, err)
	}

	// Fold the parameter-binding let statements into whatever the body
	// already is: if it's a bare Query (no internal lets), wrap it into
	// a synthetic CompoundStatement; if it's already a CompoundStatement
	// (the body itself contains let statements — real ADX explicitly
	// allows "zero or more let statements followed by a valid
	// expression"), prepend the bindings so both sets of lets resolve
	// through the SAME LetContext when executeCompound runs. Either way,
	// no text substitution into the stored body at all — parameter
	// references resolve through the same ColumnRef-falls-back-to-
	// activeLetContext.Scalars path a normal `let x = 5; ...` already
	// uses, not by rewriting the body's text.
	var toExecute parser.Statement
	switch s := stmt.(type) {
	case *parser.CompoundStatement:
		s.Lets = append(letBindings, s.Lets...)
		toExecute = s
	default:
		if len(letBindings) == 0 {
			toExecute = stmt
		} else {
			toExecute = &parser.CompoundStatement{Lets: letBindings, Final: stmt}
		}
	}

	e.resolvingFunctions[name] = true
	defer delete(e.resolvingFunctions, name)

	result, err := e.Execute(toExecute)
	if err != nil {
		return nil, fmt.Errorf("function %q: %w", name, err)
	}
	return result, nil
}

// bindStoredFunctionArgs matches call-site argument TEXT against a
// function's declared parameters — positionally, matching real ADX's
// own call semantics (verified before relying on it: f(10), f(Column),
// never name: value at the call site). Omitted TRAILING arguments use
// their declared default if one exists (scalar parameters only — real
// ADX never allows a default for a tabular parameter, verified before
// relying on this); a genuinely missing REQUIRED argument, or too many
// arguments, is a clear error rather than silently truncating or
// padding with nulls.
//
// Each argument's raw text is parsed HERE, using whichever parser
// (Parse vs ParseExpr) matches the DECLARED parameter's kind at that
// position — see StoredFunctionCall's own doc comment (ast.go) for why
// this couldn't be decided any earlier, at call-parse time.
//
// Scalar arguments are type-checked against the ARGUMENT EXPRESSION
// via inferExprType — the same expression-based type inferrer already
// reused to fix print's datetime bug earlier this session — not the
// evaluated runtime value. This matters concretely for datetime/
// timespan specifically: both are represented internally as a plain
// int64, indistinguishable from an ordinary long at the value level,
// so a value-based check could never catch CascadeCheck(42) being
// passed where a datetime parameter was declared.
//
// Tabular arguments are executed exactly ONCE HERE, while the caller's
// own LetContext is still current, and their result is captured
// directly as a *parser.PrecomputedTable — see that type's own doc
// comment (ast.go) for the real, live correctness bug this fixes
// (2026-08-15): an earlier version carried the argument's own AST
// forward instead, to be executed a SECOND time later inside the
// callee's own executeCompound call, by which point a caller-scope
// table reference (a `let`-bound or `as`-bound name) had already
// become invisible, since executeCompound installs its own fresh
// LetContext before evaluating its Lets. Schema validation against the
// parameter's declared TabularSchema still only runs for a non-(*)
// parameter (an IsAnySchema/(*) parameter accepts anything), but the
// EXECUTION itself is now unconditional for every tabular argument,
// not skipped for (*) parameters the way validation is — needed since
// this is now the only execution of a tabular argument, not one of two.
//
// Returns the bindings as []*parser.LetStatement, ready to prepend to
// a CompoundStatement's Lets — a scalar argument's value is wrapped
// directly as a *parser.Literal (the value was already evaluated, not
// re-serialized to text and re-parsed); a tabular argument's ALREADY-
// COMPUTED result is carried directly via *parser.PrecomputedTable.
// Neither path ever touches the stored body's own text.
func (e *Engine) bindStoredFunctionArgs(fnName string, declared []parser.FunctionParam, argTexts []string) ([]*parser.LetStatement, error) {
	if len(argTexts) > len(declared) {
		return nil, fmt.Errorf("function %q: too many arguments (got %d, expected at most %d)", fnName, len(argTexts), len(declared))
	}

	emptySchema := types.Schema{}
	emptyRow := types.Row{}

	var lets []*parser.LetStatement
	for i, param := range declared {
		haveArg := i < len(argTexts)

		if param.IsTabular {
			if !haveArg {
				// Real ADX never allows a default for a tabular
				// parameter -- verified before relying on this, so
				// this is always a hard error, never a fallback.
				return nil, fmt.Errorf("function %q: missing required tabular argument %d (%q)", fnName, i+1, param.Name)
			}
			argStmt, err := parseTabularArgument(argTexts[i])
			if err != nil {
				return nil, fmt.Errorf("function %q: tabular argument %d (%q): %w", fnName, i+1, param.Name, err)
			}
			// Executed exactly ONCE, here, while the CALLER's own
			// LetContext is still current -- not deferred to later
			// re-execution inside the callee's own executeCompound
			// call, which installs its own fresh LetContext before
			// its Lets run. See PrecomputedTable's own doc comment
			// (ast.go) for the real, live bug this fixes (a
			// caller-scope table reference like `let T = ...;
			// MyFunc((T))` silently failing to resolve) and why this
			// single-execution approach is strictly better than the
			// earlier two-execution design, not just a workaround.
			result, err := e.Execute(argStmt)
			if err != nil {
				return nil, fmt.Errorf("function %q: tabular argument %d (%q): evaluating: %w", fnName, i+1, param.Name, err)
			}
			if !param.IsAnySchema {
				if err := validateTabularArgSchema(param, result.Schema); err != nil {
					return nil, fmt.Errorf("function %q: tabular argument %d (%q): %w", fnName, i+1, param.Name, err)
				}
			}
			lets = append(lets, &parser.LetStatement{Name: param.Name, Value: &parser.PrecomputedTable{Table: result}})
			continue
		}

		var valueExpr parser.Expr
		if haveArg {
			expr, err := parser.ParseExpr(argTexts[i])
			if err != nil {
				return nil, fmt.Errorf("function %q: argument %d (%q): %w", fnName, i+1, param.Name, err)
			}
			argType := inferExprType(expr, &emptySchema)
			if !kqlTypesCompatible(argType, param.Type) {
				return nil, fmt.Errorf("function %q: argument %d (%q): expected %s, got %s",
					fnName, i+1, param.Name, param.Type, argType)
			}
			valueExpr = expr
		} else if param.HasDefault {
			valueExpr = param.Default
		} else {
			return nil, fmt.Errorf("function %q: missing required argument %d (%q: %s)",
				fnName, i+1, param.Name, param.Type)
		}

		val, err := evalExpr(valueExpr, &emptySchema, emptyRow)
		if err != nil {
			return nil, fmt.Errorf("function %q: evaluating argument %q: %w", fnName, param.Name, err)
		}
		// Coerce the VALUE itself to match param.Type, not just tag it
		// — kqlTypesCompatible's own long/int→real widening (above)
		// means val can genuinely be an int64 here for a real-typed
		// parameter (e.g. a bare `5` literal argument for a
		// `lowPercentile:real` parameter), and wrapping that
		// int64 in a Literal tagged Type:TypeReal without converting
		// the underlying Go value would leave a real KQL-type/Go-type
		// mismatch for anything downstream that assumes a real-typed
		// value is always float64. coerceNumeric (make_series.go) is
		// reused here rather than duplicated — the same real, general
		// need this session already built it for.
		val = coerceNumeric(val, param.Type)

		lets = append(lets, &parser.LetStatement{
			Name:  param.Name,
			Value: &parser.ScalarExpr{Expr: &parser.Literal{Value: val, Type: param.Type}},
		})
	}
	return lets, nil
}

// parseTabularArgument parses a tabular argument's raw text as a
// tabular statement — stripping one layer of redundant outer parens if
// the WHOLE argument is wrapped in one, matching real ADX's own call
// syntax (MyFilter((range x from 1 to 10 step 1), 9)), since
// parser.Parse expects a bare query (a source name optionally followed
// by | operators), not one wrapped in parens.
//
// Returns parser.Statement, not *parser.Query specifically — widened
// from an earlier version that only accepted *parser.Query, found live
// during systematic review to reject a real, valid case outright: a
// tabular argument whose own text has its own let binding(s), e.g.
// MyFilter((let x = 10; T | where Y >= x), 9). parser.Parse returns a
// *parser.CompoundStatement for that shape (Lets + a final Query), not
// a bare *parser.Query — the earlier version's exact type assertion
// rejected it with a genuinely misleading error ("must be a tabular
// expression, not a management command" — it's neither of those, it's
// a perfectly ordinary compound tabular expression). The caller
// (bindStoredFunctionArgs) stores the result directly as a
// LetStatement's own Value, which is already typed as the general
// Statement interface for exactly this reason — engine.go's
// executeCompound gained a matching CompoundStatement case
// specifically to make this combination work end to end, not just
// parse without erroring.
func parseTabularArgument(argText string) (parser.Statement, error) {
	text := strings.TrimSpace(argText)
	if strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")") {
		text = strings.TrimSpace(text[1 : len(text)-1])
	}
	stmt, err := parser.Parse(text)
	if err != nil {
		return nil, fmt.Errorf("does not parse as a tabular expression: %w", err)
	}
	switch stmt.(type) {
	case *parser.Query, *parser.CompoundStatement:
		return stmt, nil
	default:
		return nil, fmt.Errorf("must be a tabular expression, not a management command")
	}
}

// validateTabularArgSchema checks that every column the parameter
// declares is present in the argument's actual result schema, with a
// matching type — real ADX's own tabular-argument-type-checking
// behavior for a declared (non-(*)) schema.
func validateTabularArgSchema(param parser.FunctionParam, actual types.Schema) error {
	for _, col := range param.TabularSchema {
		actualCol := actual.ColumnByName(col.Name)
		if actualCol == nil {
			return fmt.Errorf("declared column %q not found in the argument's schema", col.Name)
		}
		if actualCol.Type != col.Type {
			return fmt.Errorf("declared column %q: expected type %s, argument has %s", col.Name, col.Type, actualCol.Type)
		}
	}
	return nil
}

// kqlTypesCompatible is a deliberately permissive equality-or-close
// check, not full ADX-style implicit-conversion rules — real Kusto has
// a genuinely large implicit-conversion table this doesn't attempt to
// fully replicate. Exact match is always accepted; long/int are
// treated as interchangeable (this codebase already parses TypeInt
// identically to TypeLong elsewhere, per operators.go's own documented
// note); long/int widen to real (the standard, universally-expected
// direction — real does NOT narrow to long/int); and dynamic accepts
// anything, matching its role as KQL's untyped/JSON catch-all. The
// long/int→real widening was added 2026-08-15, found missing while
// testing invoke against real ADX's own clipped_average worked
// example: a bare integer literal argument (`invoke
// clipped_average(5, 99)`) for a real-typed parameter
// (lowPercentile:real) was rejected outright — a real, previously
// undocumented gap in what the original doc comment already
// acknowledged was a deliberately incomplete conversion table, not a
// new limitation this introduces.
func kqlTypesCompatible(argType, paramType types.KQLType) bool {
	if argType == paramType {
		return true
	}
	if paramType == types.TypeDynamic {
		return true
	}
	if (argType == types.TypeLong || argType == types.TypeInt) &&
		(paramType == types.TypeLong || paramType == types.TypeInt) {
		return true
	}
	if (argType == types.TypeLong || argType == types.TypeInt) &&
		paramType == types.TypeReal {
		return true
	}
	return false
}

// okResult is a small, shared "OK"-shaped single-row table, used by
// command handlers whose result is a status message rather than
// tabular data.
func okResult(message string) *types.Table {
	t := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "Result", Type: types.TypeString},
	}})
	t.AddRow(types.Row{message})
	return t
}
