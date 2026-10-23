package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// --- Expression Evaluation ---

// substituteToScalars walks expr ONCE and returns a new expression
// tree with every *parser.ToScalarExpr replaced by a *parser.Literal
// holding its already-computed value — called once per OPERATOR
// invocation (applyWhere, applyExtend, executeCompound's scalar lets,
// ...), BEFORE any per-row loop begins, never from within evalExpr
// itself. See ToScalarExpr's own doc comment (ast.go) for the full
// design rationale, including the real, live concurrency bug in an
// earlier version of this feature (a shared, package-level Engine
// reference) that this design exists specifically to avoid: e here is
// a plain, local, unshared parameter — the same *Engine the calling
// operator method already had in scope, never touching any package
// state at all.
//
// Structurally mirrors evalExpr's own switch/recursion exactly (same
// cases, same nesting), except REBUILDING the tree instead of
// evaluating it. Any expression with no ToScalarExpr anywhere in it
// (the overwhelming majority) is returned via cheap, shallow
// reconstruction; nil/no-op cases (Literal, ColumnRef, StarExpr) are
// returned completely unchanged, not even shallow-copied.
//
// A *parser.ToScalarExpr that survives all the way to evalExpr itself
// (evalExpr has no case for this type, by design) means some call
// path skipped this substitution step — an internal error, not a
// silent wrong-value one: evalExpr's own default case already returns
// a clear "unsupported expression type" error for exactly this,
// rather than misbehaving quietly.
// exprReferencesAnyColumn reports whether expr reads a column (or a
// parameter, which a UDF body sees as a column).
func exprReferencesAnyColumn(e *Engine, expr parser.Expr) bool {
	found := false
	_, _ = rewriteExpr(expr, func(x parser.Expr) (parser.Expr, error) {
		if ref, ok := x.(*parser.ColumnRef); ok {
			if _, isLet := e.letContext.LookupScalar(ref.Name); !isLet {
				found = true
			}
		}
		return nil, nil
	})
	return found
}

func substituteToScalars(e *Engine, expr parser.Expr) (parser.Expr, error) {
	switch ex := expr.(type) {
	case nil:
		return nil, nil

	case *parser.ToScalarExpr:
		val, valType, err := evalToScalarOnce(e, ex)
		if err != nil {
			return nil, err
		}
		return &parser.Literal{Value: val, Type: valType}, nil

	case *parser.Literal, *parser.ColumnRef, *parser.StarExpr:
		return expr, nil // base cases: nothing to substitute, returned unchanged

	case *parser.BinaryExpr:
		left, err := substituteToScalars(e, ex.Left)
		if err != nil {
			return nil, err
		}
		right, err := substituteToScalars(e, ex.Right)
		if err != nil {
			return nil, err
		}
		if left == ex.Left && right == ex.Right {
			return expr, nil
		}
		return &parser.BinaryExpr{Left: left, Op: ex.Op, Right: right}, nil

	case *parser.UnaryExpr:
		inner, err := substituteToScalars(e, ex.Expr)
		if err != nil {
			return nil, err
		}
		if inner == ex.Expr {
			return expr, nil
		}
		return &parser.UnaryExpr{Op: ex.Op, Expr: inner}, nil

	case *parser.FuncCall:
		// assert() with constant arguments is evaluated here, once per
		// operator before any row: ADX checks it at query analysis, so
		// it fails even when no rows reach it.
		if ex.Name == "assert" && len(ex.Args) == 2 && !exprReferencesAnyColumn(e, ex) {
			if _, _, err := evalAssert(e, ex, &types.Schema{}, types.Row{}); err != nil {
				return nil, err
			}
			return &parser.Literal{Value: true, Type: types.TypeBool}, nil
		}
		newArgs := make([]parser.Expr, len(ex.Args))
		changed := false
		for i, arg := range ex.Args {
			na, err := substituteToScalars(e, arg)
			if err != nil {
				return nil, err
			}
			newArgs[i] = na
			if na != arg {
				changed = true
			}
		}
		if !changed {
			return expr, nil
		}
		return &parser.FuncCall{Name: ex.Name, RawName: ex.RawName, Args: newArgs, ArgNames: ex.ArgNames}, nil

	case *parser.AccessExpr:
		// Object and any computed bracket keys (d[expr]) may contain
		// toscalar().
		obj, err := substituteToScalars(e, ex.Object)
		if err != nil {
			return nil, err
		}
		path, changed := ex.Path, false
		for i, k := range ex.Path {
			if k.Expr == nil {
				continue
			}
			r, err := substituteToScalars(e, k.Expr)
			if err != nil {
				return nil, err
			}
			if r != k.Expr {
				if !changed {
					path = append([]parser.AccessKey(nil), ex.Path...)
					changed = true
				}
				path[i].Expr = r
			}
		}
		if obj == ex.Object && !changed {
			return expr, nil
		}
		return &parser.AccessExpr{Object: obj, Path: path}, nil

	case *parser.InExpr:
		col, err := substituteToScalars(e, ex.Column)
		if err != nil {
			return nil, err
		}

		// SubqueryText (real ADX's own "X in (subquery)" form — see
		// InExpr's own doc comment, ast.go) is resolved HERE, once per
		// operator invocation, exactly like ToScalarExpr itself:
		// executed via e (a real, local, unshared *Engine — never
		// shared package state, same reasoning as evalToScalarOnce),
		// its first column's value from EVERY row becomes an ordinary
		// Values list of Literals — reusing the existing, already-
		// working Values-based membership-check logic in evalExpr
		// completely unchanged; evalExpr itself never needs to know
		// this InExpr originated from a subquery at all.
		if ex.SubqueryText != "" {
			vals, err := evalInSubqueryOnce(e, ex.SubqueryText)
			if err != nil {
				return nil, err
			}
			return &parser.InExpr{Column: col, Values: vals, CaseInsensitive: ex.CaseInsensitive, Negated: ex.Negated}, nil
		}

		newVals := make([]parser.Expr, len(ex.Values))
		changed := col != ex.Column
		for i, v := range ex.Values {
			nv, err := substituteToScalars(e, v)
			if err != nil {
				return nil, err
			}
			newVals[i] = nv
			if nv != v {
				changed = true
			}
		}
		if !changed {
			return expr, nil
		}
		return &parser.InExpr{Column: col, Values: newVals, TableRef: ex.TableRef, CaseInsensitive: ex.CaseInsensitive, Negated: ex.Negated}, nil

	case *parser.HasAnyAllExpr:
		col, err := substituteToScalars(e, ex.Column)
		if err != nil {
			return nil, err
		}
		// Terms from a subquery, or from a let-bound table named alone.
		subq := ex.SubqueryText
		if subq == "" && len(ex.Values) == 1 {
			if ref, ok := ex.Values[0].(*parser.ColumnRef); ok {
				if _, isTable := e.letContext.LookupTable(ref.Name); isTable {
					subq = ref.Name
				}
			}
		}
		if subq != "" {
			vals, err := evalInSubqueryOnce(e, subq)
			if err != nil {
				return nil, err
			}
			return &parser.HasAnyAllExpr{Column: col, Values: vals, All: ex.All}, nil
		}
		newVals := make([]parser.Expr, len(ex.Values))
		changed := col != ex.Column
		for i, v := range ex.Values {
			nv, err := substituteToScalars(e, v)
			if err != nil {
				return nil, err
			}
			newVals[i] = nv
			if nv != v {
				changed = true
			}
		}
		if !changed {
			return expr, nil
		}
		return &parser.HasAnyAllExpr{Column: col, Values: newVals, All: ex.All}, nil

	case *parser.BetweenExpr:
		exp, err := substituteToScalars(e, ex.Expr)
		if err != nil {
			return nil, err
		}
		low, err := substituteToScalars(e, ex.Low)
		if err != nil {
			return nil, err
		}
		high, err := substituteToScalars(e, ex.High)
		if err != nil {
			return nil, err
		}
		if exp == ex.Expr && low == ex.Low && high == ex.High {
			return expr, nil
		}
		return &parser.BetweenExpr{Expr: exp, Low: low, High: high, Negated: ex.Negated}, nil

	default:
		// Any other expression type has no ToScalarExpr-bearing
		// children this rewriter knows how to walk into (matching
		// evalExpr's own coverage, checked against the identical case
		// list) — returned unchanged, not defensively rejected: a
		// genuinely new expression type without a ToScalarExpr nested
		// inside it needs no rewriting at all, and one WITH a nested
		// ToScalarExpr this switch doesn't yet cover would surface as
		// evalExpr's own clear "unsupported expression type" error
		// later, per this function's own doc comment, rather than a
		// silent miss here.
		return expr, nil
	}
}

// evalToScalarOnce executes ToScalarExpr's tabular argument via e (a
// plain, local, unshared parameter — see substituteToScalars' own doc
// comment for why this is never done through shared package state),
// taking the first column of the first row, matching real ADX exactly
// ("if the result is a tabular, then the first column and first row is
// taken"). Returns (nil, types.TypeDynamic, nil) for an empty result —
// a legitimate outcome (e.g. a filter matching nothing), not an error.
// columnNamesOf implements column_names_of(expr): the column names of
// a tabular expression as a dynamic array of strings. The expression
// runs with take 0, so only its schema is produced.
func columnNamesOf(e *Engine, queryText string) (types.Value, types.KQLType, error) {
	stmt, err := parser.Parse(queryText + " | take 0")
	if err != nil {
		return nil, types.TypeDynamic, fmt.Errorf("column_names_of(%s): %w", queryText, err)
	}
	result, err := e.Execute(stmt)
	if err != nil {
		return nil, types.TypeDynamic, fmt.Errorf("column_names_of(%s): %w", queryText, err)
	}
	names := make([]string, len(result.Schema.Columns))
	for i, c := range result.Schema.Columns {
		names[i] = c.Name
	}
	b, _ := json.Marshal(names)
	return string(b), types.TypeDynamic, nil
}

func evalToScalarOnce(e *Engine, ex *parser.ToScalarExpr) (types.Value, types.KQLType, error) {
	if ex.ColumnNames {
		return columnNamesOf(e, ex.QueryText)
	}
	stmt, err := parser.Parse(ex.QueryText)
	var result *types.Table
	if err == nil {
		result, err = e.Execute(stmt)
	}
	if err != nil {
		// toscalar() of a scalar expression (`toscalar(2)`,
		// `toscalar(new_guid())`) is that value.
		if v, t, ok := evalToScalarScalar(e, ex.QueryText); ok {
			return v, t, nil
		}
		return nil, types.TypeDynamic, fmt.Errorf("toscalar(%s): %w", ex.QueryText, err)
	}
	if result == nil || len(result.Rows) == 0 || len(result.Schema.Columns) == 0 {
		return nil, types.TypeDynamic, nil
	}
	return result.Rows[0][0], result.Schema.Columns[0].Type, nil
}

// evalToScalarScalar evaluates toscalar()'s argument as a scalar
// expression, for arguments that are not tabular.
func evalToScalarScalar(e *Engine, text string) (types.Value, types.KQLType, bool) {
	if strings.Contains(text, "|") {
		return nil, types.TypeDynamic, false
	}
	expr, err := parser.ParseExpr(text)
	if err != nil {
		return nil, types.TypeDynamic, false
	}
	expr, err = substituteToScalars(e, expr)
	if err != nil {
		return nil, types.TypeDynamic, false
	}
	v, err := evalExpr(e, expr, &types.Schema{}, types.Row{})
	if err != nil {
		return nil, types.TypeDynamic, false
	}
	return v, inferExprType(e, expr, &types.Schema{}), true
}

// evalInSubqueryOnce executes "X in (subquery)"'s tabular argument via
// e (a plain, local, unshared parameter — same reasoning as
// evalToScalarOnce immediately above), taking the FIRST COLUMN OF
// EVERY ROW (unlike toscalar's first-row-only rule — "in" needs the
// whole set for membership testing, not a single value), wrapped as
// Literal expressions ready to slot directly into an ordinary
// InExpr.Values list.
func evalInSubqueryOnce(e *Engine, queryText string) ([]parser.Expr, error) {
	stmt, err := parser.Parse(queryText)
	if err != nil {
		return nil, fmt.Errorf("in (%s): %w", queryText, err)
	}
	result, err := e.Execute(stmt)
	if err != nil {
		return nil, fmt.Errorf("in (%s): %w", queryText, err)
	}
	if result == nil || len(result.Schema.Columns) == 0 {
		return nil, nil
	}
	colType := result.Schema.Columns[0].Type
	vals := make([]parser.Expr, len(result.Rows))
	for i, row := range result.Rows {
		vals[i] = &parser.Literal{Value: row[0], Type: colType}
	}
	return vals, nil
}

func evalExpr(eng *Engine, expr parser.Expr, schema *types.Schema, row types.Row) (types.Value, error) {
	switch e := expr.(type) {
	case *parser.Literal:
		return e.Value, nil

	case *parser.ColumnRef:
		idx := schema.ColumnIndex(e.Name)
		if idx >= 0 {
			var v types.Value
			if idx < len(row) {
				v = row[idx]
			}
			// A string is never null in KQL: a missing cell (an
			// extent from before the column existed, an unmatched
			// outer-join or union row) reads as "".
			if v == nil && schema.Columns[idx].Type == types.TypeString {
				return "", nil
			}
			return v, nil
		}
		// Check scalar let bindings — walks the parent chain, not
		// just the innermost context's own local map.
		if val, ok := eng.lets().LookupScalar(e.Name); ok {
			return val, nil
		}
		// A parameterless scalar function may be named without
		// parentheses: `let x = () {42}; print x`.
		if fn, ok := eng.lets().LookupFunction(e.Name); ok && !fn.Tabular && len(fn.Params) == 0 {
			return evalExpr(eng, &parser.FuncCall{Name: e.Name}, schema, row)
		}
		return nil, fmt.Errorf("column %q not found", e.Name)

	case *parser.BinaryExpr:
		if _, ok := e.Left.(*parser.StarExpr); ok {
			return evalStarPredicate(eng, e, schema, row)
		}
		left, err := evalExpr(eng, e.Left, schema, row)
		if err != nil {
			return nil, err
		}
		right, err := evalExpr(eng, e.Right, schema, row)
		if err != nil {
			return nil, err
		}
		return evalBinaryOp(eng, left, e.Op, right, schema, e)

	case *parser.UnaryExpr:
		val, err := evalExpr(eng, e.Expr, schema, row)
		if err != nil {
			return nil, err
		}
		if e.Op == "not" {
			if b, ok := val.(bool); ok {
				return !b, nil
			}
			return nil, nil // not(null) is null
		}
		if e.Op == "-" {
			switch x := val.(type) {
			case nil:
				return nil, nil
			case int64: // long, timespan (ticks)
				return -x, nil
			case int32:
				return -x, nil
			case float64:
				return -x, nil
			case types.Decimal:
				return x.Neg(), nil
			}
			return nil, fmt.Errorf("unary minus: unsupported operand %v", val)
		}
		return nil, fmt.Errorf("unsupported unary op: %s", e.Op)

	case *parser.FuncCall:
		return evalFunc(eng, e, schema, row)

	case *parser.AccessExpr:
		return evalAccess(eng, e, schema, row)

	case *parser.InExpr:
		colVal, err := evalExpr(eng, e.Column, schema, row)
		if err != nil {
			return nil, err
		}
		if colVal == nil {
			return false, nil
		}

		found := false
		colStr := fmtV(colVal)

		if e.TableRef != "" {
			// Check against a let-bound table's first column — walks
			// the parent chain, not just the innermost context's own
			// local map.
			if tbl, ok := eng.lets().LookupTable(e.TableRef); ok {
				for _, row := range tbl.Rows {
					if len(row) > 0 {
						rowStr := fmtV(row[0])
						if e.CaseInsensitive {
							found = strings.EqualFold(colStr, rowStr)
						} else {
							found = colStr == rowStr
						}
						if found {
							break
						}
					}
				}
			}
		}
		values := e.Values
		if e.TableRef != "" {
			if _, isTable := eng.lets().LookupTable(e.TableRef); !isTable {
				// A scalar let (a dynamic array, typically): in (L).
				values = []parser.Expr{&parser.ColumnRef{Name: e.TableRef}}
			}
		}
		if e.TableRef == "" || len(values) > 0 {
			// The value list; a dynamic array contributes its elements:
			// in (dynamic(["a", "b"])).
			match := func(valStr string) bool {
				if e.CaseInsensitive {
					return strings.EqualFold(colStr, valStr)
				}
				return colStr == valStr
			}
			for _, valExpr := range values {
				val, err := evalExpr(eng, valExpr, schema, row)
				if err != nil {
					return nil, err
				}
				if inferExprType(eng, valExpr, schema) == types.TypeDynamic {
					if arr, ok := parseJSONArray(val); ok {
						for _, el := range arr {
							if el != nil && match(fmtV(el)) {
								found = true
								break
							}
						}
						if found {
							break
						}
						continue
					}
				}
				if match(fmtV(val)) {
					found = true
					break
				}
			}
		}

		if e.Negated {
			return !found, nil
		}
		return found, nil

	case *parser.HasAnyAllExpr:
		colVal, err := evalExpr(eng, e.Column, schema, row)
		if err != nil {
			return nil, err
		}
		if colVal == nil {
			return false, nil
		}
		colStr := fmtV(colVal)

		// Terms: each value, a dynamic array contributing its elements
		// (has_any (dynamic(["a", "b"])), or a let-bound array).
		var terms []string
		for _, valExpr := range e.Values {
			val, err := evalExpr(eng, valExpr, schema, row)
			if err != nil {
				return nil, err
			}
			if inferExprType(eng, valExpr, schema) == types.TypeDynamic {
				if arr, ok := parseJSONArray(val); ok {
					for _, el := range arr {
						if el != nil {
							terms = append(terms, fmtV(el))
						}
					}
					continue
				}
			}
			terms = append(terms, fmtV(val))
		}
		if e.All {
			// has_all: every term must be found (whole-term, case-insensitive)
			for _, term := range terms {
				if !hasTerm(colStr, term, false) {
					return false, nil
				}
			}
			return true, nil
		}
		// has_any: at least one term must be found (whole-term, case-insensitive)
		for _, term := range terms {
			if hasTerm(colStr, term, false) {
				return true, nil
			}
		}
		return false, nil

	case *parser.BetweenExpr:
		val, err := evalExpr(eng, e.Expr, schema, row)
		if err != nil || val == nil {
			return false, err
		}
		low, err := evalExpr(eng, e.Low, schema, row)
		if err != nil || low == nil {
			return false, err
		}
		high, err := evalExpr(eng, e.High, schema, row)
		if err != nil || high == nil {
			return false, err
		}
		// `between (dt .. 3d)`: a timespan upper bound is an offset
		// from a datetime lower bound (datetimes and timespans are
		// both int64 ticks here, so the types decide).
		span := atomic.LoadInt32(&e.SpanHigh)
		if span == 0 {
			span = 2
			if inferExprType(eng, e.High, schema) == types.TypeTimespan && inferExprType(eng, e.Low, schema) == types.TypeDatetime {
				span = 1
			}
			atomic.StoreInt32(&e.SpanHigh, span)
		}
		if span == 1 { // datetimes are Unix nanoseconds, timespans 100ns ticks
			high = types.ToInt64(low) + types.ToInt64(high)*100
		}
		// Infer type from the value being tested
		vt := inferValType(val)
		inRange := types.CompareValues(val, low, vt) >= 0 && types.CompareValues(val, high, vt) <= 0
		if e.Negated {
			return !inRange, nil
		}
		return inRange, nil

	case *parser.ToScalarExpr:
		// Reached inside a user-function body (operators substitute
		// toscalar before rows are read): the subquery may use the
		// function's parameters, which are this row's columns, so they
		// are bound as lets while it runs.
		if eng == nil {
			return nil, fmt.Errorf("toscalar: no engine")
		}
		ctx := &LetContext{
			Scalars:     make(map[string]types.Value, len(schema.Columns)),
			ScalarTypes: make(map[string]types.KQLType, len(schema.Columns)),
			Functions:   make(map[string]*parser.FunctionDef),
			Tables:      make(map[string]*types.Table),
			Parent:      eng.letContext,
		}
		for i, c := range schema.Columns {
			if i < len(row) {
				ctx.Scalars[c.Name] = row[i]
				ctx.ScalarTypes[c.Name] = c.Type
			}
		}
		prev := eng.letContext
		eng.setLetContext(ctx)
		val, _, err := toScalarInExpr(eng, e)
		eng.setLetContext(prev)
		return val, err

	default:
		return nil, fmt.Errorf("unsupported expression type: %T", expr)
	}
}

func isStringOperator(op parser.BinaryOp) bool {
	switch op {
	case parser.OpContains, parser.OpNotContains, parser.OpContainsCS, parser.OpNotContainsCS,
		parser.OpHas, parser.OpNotHas, parser.OpHasCS, parser.OpNotHasCS,
		parser.OpStartsWith, parser.OpNotStartsWith, parser.OpStartsWithCS, parser.OpNotStartsWithCS,
		parser.OpEndsWith, parser.OpNotEndsWith, parser.OpEndsWithCS, parser.OpNotEndsWithCS,
		parser.OpHasPrefix, parser.OpNotHasPrefix, parser.OpHasPrefixCS, parser.OpNotHasPrefixCS,
		parser.OpHasSuffix, parser.OpNotHasSuffix, parser.OpHasSuffixCS, parser.OpNotHasSuffixCS,
		parser.OpLike, parser.OpNotLike, parser.OpMatchesRegex:
		return true
	}
	return false
}

func evalBinaryOp(eng *Engine, left types.Value, op parser.BinaryOp, right types.Value, schema *types.Schema, expr *parser.BinaryExpr) (types.Value, error) {
	// Logical operators
	// and/or are three-valued: null and false is false, null or true
	// is true, and otherwise a null operand gives null.
	if op == parser.OpAnd {
		lb, lok := left.(bool)
		rb, rok := right.(bool)
		if (lok && !lb) || (rok && !rb) {
			return false, nil
		}
		if lok && rok {
			return true, nil
		}
		return nil, nil
	}
	if op == parser.OpOr {
		lb, lok := left.(bool)
		rb, rok := right.(bool)
		if (lok && lb) || (rok && rb) {
			return true, nil
		}
		if lok && rok {
			return false, nil
		}
		return nil, nil
	}

	// String operators take their operands as strings, and null
	// converts to the empty string (tostring(null) is ""), so
	// null !startswith "555" is true. The mv-apply reference example
	// that strips null-valued properties from a bag depends on this.
	if isStringOperator(op) {
		if left == nil {
			left = ""
		}
		if right == nil {
			right = ""
		}
	}

	// Null operands: == and != against a value are false and true;
	// everything else (two nulls, ordering, arithmetic) is null.
	if left == nil || right == nil {
		if left != nil || right != nil {
			switch op {
			case parser.OpEQ:
				return false, nil
			case parser.OpNEQ:
				return true, nil
			}
		}
		return nil, nil
	}

	// String operators (case-insensitive by default, _cs variants are
	// case-sensitive). One implementation, shared with the columnar
	// path (string_ops.go), so the two cannot drift apart.
	if isStringOpFamily(op) {
		ok, err := stringOpMatch(op, fmtV(left), fmtV(right))
		if err != nil {
			return nil, err
		}
		return ok, nil
	}

	if v, ok := evalDecimalOp(left, op, right); ok {
		return v, nil
	}

	if v, ok, err := evalMixedNumeric(eng, left, op, right, schema, expr); ok {
		return v, err
	}

	// Determine comparison type from left operand
	switch left.(type) {
	case string:
		ls, rs := left.(string), fmtV(right)
		switch op {
		case parser.OpEQ:
			return ls == rs, nil
		case parser.OpNEQ:
			return ls != rs, nil
		case parser.OpLT:
			return ls < rs, nil
		case parser.OpLTE:
			return ls <= rs, nil
		case parser.OpGT:
			return ls > rs, nil
		case parser.OpGTE:
			return ls >= rs, nil
		}
	case int64:
		li := types.ToInt64(left)
		ri := types.ToInt64(right)

		// For arithmetic involving datetime/timespan, normalise units.
		// Datetime values are nanos; timespan values are 100ns ticks.
		if op == parser.OpAdd || op == parser.OpSub {
			lt := inferExprType(eng, expr.Left, schema)
			rt := inferExprType(eng, expr.Right, schema)

			hasDT := lt == types.TypeDatetime || rt == types.TypeDatetime
			hasTS := lt == types.TypeTimespan || rt == types.TypeTimespan

			if hasDT && hasTS {
				// Convert timespan operand from ticks to nanos
				liN := toNanos(li, lt)
				riN := toNanos(ri, rt)
				if op == parser.OpAdd {
					return liN + riN, nil
				}
				return liN - riN, nil
			}
			if op == parser.OpSub && lt == types.TypeDatetime && rt == types.TypeDatetime {
				// datetime - datetime = timespan (result in ticks)
				return (li - ri) / 100, nil
			}
			if lt == types.TypeTimespan && rt == types.TypeTimespan {
				// timespan +/- timespan = timespan (both already ticks)
				if op == parser.OpAdd {
					return li + ri, nil
				}
				return li - ri, nil
			}
		}

		switch op {
		case parser.OpEQ:
			return li == ri, nil
		case parser.OpNEQ:
			return li != ri, nil
		case parser.OpLT:
			return li < ri, nil
		case parser.OpLTE:
			return li <= ri, nil
		case parser.OpGT:
			return li > ri, nil
		case parser.OpGTE:
			return li >= ri, nil
		case parser.OpAdd:
			return li + ri, nil
		case parser.OpSub:
			return li - ri, nil
		case parser.OpMul:
			return li * ri, nil
		case parser.OpDiv:
			if ri == 0 {
				return nil, nil
			}
			return li / ri, nil
		case parser.OpMod:
			if ri == 0 {
				return nil, nil
			}
			return euclidMod(li, ri), nil
		}
	case int32:
		li := int32(types.ToInt64(left))
		ri := int32(types.ToInt64(right))
		switch op {
		case parser.OpEQ:
			return li == ri, nil
		case parser.OpNEQ:
			return li != ri, nil
		case parser.OpLT:
			return li < ri, nil
		case parser.OpLTE:
			return li <= ri, nil
		case parser.OpGT:
			return li > ri, nil
		case parser.OpGTE:
			return li >= ri, nil
		}
	case float64:
		lf := types.ToFloat64(left)
		rf := types.ToFloat64(right)
		switch op {
		case parser.OpEQ:
			return lf == rf, nil
		case parser.OpNEQ:
			return lf != rf, nil
		case parser.OpLT:
			return lf < rf, nil
		case parser.OpLTE:
			return lf <= rf, nil
		case parser.OpGT:
			return lf > rf, nil
		case parser.OpGTE:
			return lf >= rf, nil
		case parser.OpAdd:
			return lf + rf, nil
		case parser.OpSub:
			return lf - rf, nil
		case parser.OpMul:
			return lf * rf, nil
		case parser.OpDiv:
			return lf / rf, nil // IEEE: x/0 is ±Inf, 0/0 NaN (real(+inf), per the real type reference)
		}
	case bool:
		lb := left.(bool)
		rb, _ := right.(bool)
		switch op {
		case parser.OpEQ:
			return lb == rb, nil
		case parser.OpNEQ:
			return lb != rb, nil
		}
	}

	return nil, fmt.Errorf("unsupported operation: %T %s %T", left, op, right)
}

// inferExprType determines the KQL type an expression will produce at runtime.
// Used by arithmetic operators to distinguish datetime (nanos) from timespan (ticks).
func inferExprType(eng *Engine, expr parser.Expr, schema *types.Schema) types.KQLType {
	switch e := expr.(type) {
	case *parser.Literal:
		return e.Type
	case *parser.ToScalarExpr:
		return toScalarType(eng, e, schema)
	case *parser.ColumnRef:
		idx := schema.ColumnIndex(e.Name)
		if idx >= 0 {
			return schema.Columns[idx].Type
		}
		// Not a schema column — check for a let-bound scalar before
		// falling back to the TypeString default. Added 2026-08-18,
		// see LookupScalarType's own doc comment (engine.go) for the
		// full story: this is what closes the real, documented gap
		// where a tabular-argument parameter with a declared column
		// type (T:(v:long)) rejected a `print v = <scalar let>`
		// argument outright, since this exact case (a ColumnRef to a
		// let-bound name, not a real schema column) previously always
		// fell through to TypeString regardless of the let's actual
		// type.
		if t, ok := eng.lets().LookupScalarType(e.Name); ok {
			return t
		}
		if fn, ok := eng.lets().LookupFunction(e.Name); ok && !fn.Tabular && len(fn.Params) == 0 {
			return inferExprType(eng, &parser.FuncCall{Name: e.Name}, schema)
		}
		return types.TypeString
	case *parser.FuncCall:
		if t, ok := userFuncResultType(eng, e, schema); ok {
			return t
		}
		// Declared, argument-independent return types (Kusto.Language;
		// func_signatures.go) take precedence over the hand-written
		// cases below, which remain for argument-dependent functions.
		if t, ok := graphPathFuncType(e, schema); ok {
			return t
		}
		if e.Name == "percentile_tdigest" {
			if len(e.Args) == 3 {
				return typeofArg(e.Args[2])
			}
			return types.TypeDynamic
		}
		if e.Name == "datetime_diff" && len(e.Args) > 0 {
			if lit, ok := e.Args[0].(*parser.Literal); ok && strings.EqualFold(fmt.Sprint(lit.Value), "week") {
				return types.TypeReal // the emulator: the week count is real
			}
		}
		if t, ok := overrideReturnType(e.Name); ok {
			return t
		}
		if t, ok := scalarReturnTypes[e.Name]; ok {
			return t
		}
		switch e.Name {
		case "now", "ago", "datetime", "todatetime", "make_datetime",
			"startofday", "startofweek", "startofmonth", "startofyear",
			"endofday", "endofweek", "endofmonth",
			"unixtime_seconds_todatetime", "unixtime_milliseconds_todatetime",
			"unixtime_microseconds_todatetime", "unixtime_nanoseconds_todatetime",
			"datetime_local_to_utc", "datetime_utc_to_local", "ingestion_time":
			return types.TypeDatetime
		case "bin", "floor", "bin_at":
			return binResultType(eng, e.Args, schema)
		case "bin_auto":
			if len(e.Args) == 1 && eng != nil && eng.binAutoSize != nil {
				return binResultType(eng, []parser.Expr{e.Args[0], eng.binAutoSize}, schema)
			}
			return types.TypeDynamic
		case "abs", "round", "ceiling":
			// abs: the argument's type, except abs(int) is long.
			if len(e.Args) == 0 {
				return types.TypeReal
			}
			// The emulator: round/ceiling of int or long are real.
			t := inferExprType(eng, e.Args[0], schema)
			if e.Name != "abs" && t != types.TypeDecimal {
				return types.TypeReal
			}
			if t == types.TypeInt && e.Name == "abs" {
				return types.TypeLong
			}
			return t
		case "column_ifexists", "columnifexists":
			return columnIfExistsType(eng, e, schema)
		case "prev", "next":
			if len(e.Args) > 0 {
				return inferExprType(eng, e.Args[0], schema)
			}
			return types.TypeDynamic
		case "row_cumsum":
			if len(e.Args) > 0 && sumIsIntegral(eng, e.Args[0], schema) {
				return types.TypeLong
			}
			return types.TypeReal
		case "row_window_session":
			return types.TypeDatetime
		case "time", "totime", "timespan":
			return types.TypeTimespan
		case "dayofweek":
			return types.TypeTimespan
		case "datetime_diff", "dayofmonth", "getmonth", "getyear",
			"hourofday", "monthofyear", "strlen", "tolong", "long", "datetime_part", "datepart":
			return types.TypeLong
		case "toint", "int":
			// Found and fixed 2026-08-18 while implementing tohex():
			// toint() was previously grouped with tolong() here and
			// both returned TypeLong, silently erasing the int/long
			// distinction for anything downstream that needs the
			// STATIC type (not just the runtime int64 value both
			// share internally). This is exactly the kind of gap
			// that mattered concretely for tohex()'s own real,
			// documented behavior: a negative value's minimum
			// two's-complement hex width depends on whether it's an
			// `int` (32-bit, 8 hex chars) or a `long` (64-bit, 16 hex
			// chars) -- tohex(toint(-256), 8) == "ffffff00" only
			// works correctly once toint() is actually distinguishable
			// from tolong() via inferExprType, not just by name.
			return types.TypeInt
		case "todouble", "toreal", "real", "double", "series_cosine_similarity", "series_dot_product", "array_sum", "jaccard_index":
			return types.TypeReal
		case "tolower", "toupper", "tostring", "strcat", "format_datetime",
			"substring", "replace_string", "replace_regex", "trim", "trim_start", "trim_end",
			"reverse", "hash_sha256", "base64_encode_tostring", "base64_decode_tostring",
			"url_encode_component", "url_encode", "strcat_array",
			"base64_encode_fromarray", "gzip_compress_to_base64_string",
			"gzip_decompress_from_base64_string", "zlib_compress_to_base64_string",
			"zlib_decompress_from_base64_string", "regex_quote", "replace_strings",
			"unicode_codepoints_to_string", "format_bytes":
			return types.TypeString
		case "base64_decode_toarray":
			return types.TypeDynamic
		case "base64_decode_toguid":
			return types.TypeGUID
		case "split", "extract_all", "parse_url", "parse_urlquery", "parse_path", "embed_text",
			"unicode_codepoints_from_string", "to_utf8", "repeat":
			return types.TypeDynamic
		case "extract", "extract_json", "extractjson":
			// extract(re, g, s, typeof(T)) / extract_json(path, s, typeof(T))
			if n := len(e.Args); (e.Name == "extract" && n == 4) || (e.Name != "extract" && n == 3) {
				if t, ok := typeofArgOK(e.Args[n-1]); ok {
					return t
				}
			}
			return types.TypeString
		case "toguid":
			return types.TypeGUID
		case "__gql_size":
			return types.TypeLong
		case "indexof", "indexof_regex", "countof", "parse_ipv4", "array_length", "array_index_of", "has_any_index", "hash", "hash_xxhash64",
			"binary_and", "binary_or", "binary_xor", "binary_not",
			"binary_shift_left", "binary_shift_right", "bitset_count_ones":
			return types.TypeLong
		case "array_concat", "array_reverse", "array_slice",
			"array_sort_asc", "array_sort_desc", "bag_keys",
			"pack", "pack_array", "set_difference", "set_intersect",
			"set_union", "treepath", "array_rotate_left", "array_rotate_right",
			"array_split", "array_iff", "array_iif", "bag_zip", "bag_set_key",
			"bag_pack_columns", "parse_json", "todynamic", "__gql_interleave", "__gql_labels":
			// parse_json/todynamic found missing from this switch
			// entirely 2026-08-18 while implementing gettype(): with
			// no case here, both fell through to the FuncCall
			// switch's own TypeString default, so
			// gettype(parse_json(...)) always reported "string"
			// regardless of the parsed value's own actual shape --
			// silently wrong for every dynamic-typed case gettype()
			// exists to distinguish (int/dictionary/array/etc), found
			// live by testing gettype's own worked example against
			// this engine, not hypothetically.
			return types.TypeDynamic
		case "dynamic_to_json":
			return types.TypeString
		case "isnull", "isnotnull", "isempty", "isnotempty",
			"ipv4_is_private", "ipv4_is_in_range", "bag_has_key", "set_has_element",
			"isascii", "isutf8":
			return types.TypeBool
		case "iff", "iif", "case", "coalesce", "max_of", "min_of":
			return unifyResultTypes(eng, resultArgs(e.Name, e.Args), schema)
		default:
			return types.TypeString
		}
	case *parser.BinaryExpr:
		switch e.Op {
		case parser.OpAdd, parser.OpSub:
			lt := inferExprType(eng, e.Left, schema)
			rt := inferExprType(eng, e.Right, schema)
			// datetime - datetime = timespan
			if e.Op == parser.OpSub && lt == types.TypeDatetime && rt == types.TypeDatetime {
				return types.TypeTimespan
			}
			// datetime +/- timespan = datetime
			if lt == types.TypeDatetime || rt == types.TypeDatetime {
				return types.TypeDatetime
			}
			// timespan +/- timespan = timespan
			if lt == types.TypeTimespan || rt == types.TypeTimespan {
				return types.TypeTimespan
			}
			return numericPromote(lt, rt)
		case parser.OpMul, parser.OpDiv, parser.OpMod:
			lt := inferExprType(eng, e.Left, schema)
			rt := inferExprType(eng, e.Right, schema)
			if lt == types.TypeTimespan || rt == types.TypeTimespan {
				if e.Op == parser.OpDiv && lt == types.TypeTimespan && rt == types.TypeTimespan {
					return types.TypeReal // 90m / 1h is 1.5
				}
				return types.TypeTimespan
			}
			return numericPromote(lt, rt)
		case parser.OpEQ, parser.OpNEQ, parser.OpLT, parser.OpLTE,
			parser.OpGT, parser.OpGTE, parser.OpAnd, parser.OpOr,
			parser.OpContains, parser.OpNotContains, parser.OpHas, parser.OpNotHas,
			parser.OpStartsWith, parser.OpEndsWith:
			return types.TypeBool
		default:
			return inferExprType(eng, e.Left, schema)
		}
	case *parser.UnaryExpr:
		if e.Op == "not" {
			return types.TypeBool
		}
		return inferExprType(eng, e.Expr, schema)
	case *parser.AccessExpr:
		// A fully-consumed literal dotted column (graph-match pattern
		// variable) keeps its true type; any residual JSON descent —
		// or plain property access — returns dynamic.
		if idx, rest := dottedColumnPrefix(e, schema); idx >= 0 && len(rest) == 0 {
			return schema.Columns[idx].Type
		}
		return types.TypeDynamic
	default:
		return types.TypeString
	}
}

// toNanos converts a value to nanoseconds based on its KQL type.
// Datetime values are already in nanos; timespan values (ticks) are multiplied by 100.
func toNanos(val int64, typ types.KQLType) int64 {
	if typ == types.TypeTimespan {
		return val * 100
	}
	return val
}

// matchLike implements KQL's like operator.
// Pattern uses * for any chars (including none) and ? for exactly one char.
// If caseSensitive is false, comparison is case-insensitive.
func matchLike(s, pattern string, caseSensitive bool) bool {
	if !caseSensitive {
		s = strings.ToLower(s)
		pattern = strings.ToLower(pattern)
	}
	// Convert KQL like pattern to regex
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '.', '+', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			b.WriteByte('\\')
			b.WriteByte(pattern[i])
		default:
			b.WriteByte(pattern[i])
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(s)
}

// isTermByte reports whether b is part of a KQL term (alphanumeric).
// Non-alphanumeric characters act as term separators.
func isTermByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// hasTerm reports whether term occurs in s bounded by term boundaries on
// both sides (start/end of string or a non-alphanumeric character). This is
// KQL `has` semantics: whole-term match, so "hello world" has "hell" is
// false while "hello world" has "hello" is true. A multi-term RHS such as
// "cmd.exe" matches as a phrase at term boundaries.
func hasTerm(s, term string, caseSensitive bool) bool {
	if term == "" {
		return false
	}
	if !caseSensitive {
		s = strings.ToLower(s)
		term = strings.ToLower(term)
	}
	for i := 0; ; {
		idx := strings.Index(s[i:], term)
		if idx < 0 {
			return false
		}
		pos := i + idx
		end := pos + len(term)
		startOK := pos == 0 || !isTermByte(s[pos-1])
		endOK := end == len(s) || !isTermByte(s[end])
		if startOK && endOK {
			return true
		}
		i = pos + 1
	}
}

// hasTermPrefix reports whether prefix occurs in s starting at a term
// boundary (start of string or preceded by a non-alphanumeric character).
// This is KQL hasprefix semantics: "North-Western" hasprefix "west" is true
// because "west" is a prefix of the term "Western".
func hasTermPrefix(s, prefix string, caseSensitive bool) bool {
	if prefix == "" {
		return false
	}
	if !caseSensitive {
		s = strings.ToLower(s)
		prefix = strings.ToLower(prefix)
	}
	for i := 0; ; {
		idx := strings.Index(s[i:], prefix)
		if idx < 0 {
			return false
		}
		pos := i + idx
		if pos == 0 || !isTermByte(s[pos-1]) {
			return true
		}
		i = pos + 1
	}
}

// hasTermSuffix reports whether suffix occurs in s ending at a term
// boundary (end of string or followed by a non-alphanumeric character).
// KQL hassuffix semantics: "North-Western trail" hassuffix "ern" is true
// because "ern" is a suffix of the term "Western".
func hasTermSuffix(s, suffix string, caseSensitive bool) bool {
	if suffix == "" {
		return false
	}
	if !caseSensitive {
		s = strings.ToLower(s)
		suffix = strings.ToLower(suffix)
	}
	for i := 0; ; {
		idx := strings.Index(s[i:], suffix)
		if idx < 0 {
			return false
		}
		pos := i + idx
		end := pos + len(suffix)
		if end == len(s) || !isTermByte(s[end]) {
			return true
		}
		i = pos + 1
	}
}

// numericPromote is the type of arithmetic on two numeric operands:
// real if either is real, otherwise long (int arithmetic widens to
// long, as in ADX). A non-numeric left operand is returned unchanged.
func numericPromote(lt, rt types.KQLType) types.KQLType {
	isNum := func(t types.KQLType) bool {
		return t == types.TypeInt || t == types.TypeLong || t == types.TypeReal || t == types.TypeDecimal
	}
	if !isNum(lt) {
		return lt
	}
	if !isNum(rt) {
		return lt
	}
	// decimal dominates, real included (Kusto.Language: decimal + 1.0
	// is decimal)
	if lt == types.TypeDecimal || rt == types.TypeDecimal {
		return types.TypeDecimal
	}
	if lt == types.TypeReal || rt == types.TypeReal {
		return types.TypeReal
	}
	return types.TypeLong
}

// resultArgs returns the arguments whose values a conditional or
// choice function can return: iff's two branches, case's values and
// else, and every argument of coalesce, max_of and min_of.
func resultArgs(name string, args []parser.Expr) []parser.Expr {
	switch name {
	case "iff", "iif":
		if len(args) == 3 {
			return args[1:]
		}
	case "case":
		var out []parser.Expr
		for i := 1; i < len(args); i += 2 {
			out = append(out, args[i])
		}
		if len(args)%2 == 1 && len(args) > 0 {
			out = append(out, args[len(args)-1])
		}
		return out
	}
	return args
}

// unifyResultTypes is Kusto.Language's common type for a conditional's
// results: one type stays that type; numerics promote (int and long to
// long, then real, then decimal); dynamic defers to the others; any
// other mix is dynamic.
func unifyResultTypes(eng *Engine, args []parser.Expr, schema *types.Schema) types.KQLType {
	var ts []types.KQLType
	for _, a := range args {
		if lit, ok := a.(*parser.Literal); ok && lit.Value == nil && lit.Type == types.TypeString {
			continue // untyped null
		}
		if t := inferExprType(eng, a, schema); t != types.TypeDynamic {
			ts = append(ts, t)
		}
	}
	if len(ts) == 0 {
		if len(args) > 0 {
			return types.TypeDynamic
		}
		return types.TypeString
	}
	out := ts[0]
	isNum := func(t types.KQLType) bool {
		return t == types.TypeInt || t == types.TypeLong || t == types.TypeReal || t == types.TypeDecimal
	}
	for _, t := range ts[1:] {
		switch {
		case t == out:
		case isNum(t) && isNum(out):
			out = numericPromote(out, t)
		default:
			return types.TypeDynamic
		}
	}
	return out
}

// evalDecimalOp evaluates arithmetic and comparison when either operand
// is a decimal and the other is a number: the result is decimal
// (decimal dominates, as in Kusto.Language's typing), or null when out
// of range or dividing by zero.
func evalDecimalOp(left types.Value, op parser.BinaryOp, right types.Value) (types.Value, bool) {
	_, ld := left.(types.Decimal)
	_, rd := right.(types.Decimal)
	if (!ld && !rd) || !isArithOrCompare(op) {
		return nil, false
	}
	isNum := func(v types.Value) bool {
		switch v.(type) {
		case types.Decimal, int64, int32, int, float64:
			return true
		}
		return false
	}
	if !isNum(left) || !isNum(right) {
		return nil, false
	}
	a, ok1 := types.ToDecimal(left)
	b, ok2 := types.ToDecimal(right)
	if !ok1 || !ok2 {
		return nil, true // NaN or infinity: no decimal value
	}
	var r types.Decimal
	var ok bool
	switch op {
	case parser.OpEQ:
		return a.Cmp(b) == 0, true
	case parser.OpNEQ:
		return a.Cmp(b) != 0, true
	case parser.OpLT:
		return a.Cmp(b) < 0, true
	case parser.OpLTE:
		return a.Cmp(b) <= 0, true
	case parser.OpGT:
		return a.Cmp(b) > 0, true
	case parser.OpGTE:
		return a.Cmp(b) >= 0, true
	case parser.OpAdd:
		r, ok = a.Add(b)
	case parser.OpSub:
		r, ok = a.Sub(b)
	case parser.OpMul:
		r, ok = a.Mul(b)
	case parser.OpDiv:
		r, ok = a.Quo(b)
	case parser.OpMod:
		// ADX's remainder is never negative: modulo |b|.
		if r, ok = a.Rem(b); ok && r.Sign() < 0 {
			r, ok = r.Add(b.Abs())
		}
	}
	if !ok {
		return nil, true
	}
	return r, true
}

func isArithOrCompare(op parser.BinaryOp) bool {
	switch op {
	case parser.OpAdd, parser.OpSub, parser.OpMul, parser.OpDiv, parser.OpMod,
		parser.OpEQ, parser.OpNEQ, parser.OpLT, parser.OpLTE, parser.OpGT, parser.OpGTE:
		return true
	}
	return false
}

// evalMixedNumeric handles the numeric operand combinations the
// int64-only and float64-only paths below get wrong: an int with a
// real (2 * 1.5 is 3.0, x < 1.5 compares as real), int (32-bit)
// operands (widened to long), timespan scaled by a real (1h * 1.5 is
// 01:30:00), and timespan / timespan (a real ratio). ok is false when
// the operands are left to the paths below.
func evalMixedNumeric(eng *Engine, left types.Value, op parser.BinaryOp, right types.Value, schema *types.Schema, expr *parser.BinaryExpr) (types.Value, bool, error) {
	if !isArithOrCompare(op) {
		return nil, false, nil
	}
	kind := func(v types.Value) int { // 0 other, 1 integer, 2 float
		switch v.(type) {
		case int64, int32, int:
			return 1
		case float64:
			return 2
		}
		return 0
	}
	lk, rk := kind(left), kind(right)
	if lk == 0 || rk == 0 {
		return nil, false, nil
	}
	_, l32 := left.(int32)
	_, r32 := right.(int32)
	var lt, rt types.KQLType = types.TypeLong, types.TypeLong
	if expr != nil {
		lt, rt = inferExprType(eng, expr.Left, schema), inferExprType(eng, expr.Right, schema)
	}
	lts, rts := lt == types.TypeTimespan, rt == types.TypeTimespan

	// timespan / timespan → real
	if op == parser.OpDiv && lts && rts && lk == 1 && rk == 1 {
		r := types.ToFloat64(right)
		if r == 0 {
			return nil, true, nil
		}
		return types.ToFloat64(left) / r, true, nil
	}
	if lk == 2 || rk == 2 {
		lf, rf := types.ToFloat64(left), types.ToFloat64(right)
		// timespan scaled by a real stays a timespan (ticks)
		if (op == parser.OpMul && (lts && rk == 2 || rts && lk == 2)) || (op == parser.OpDiv && lts && rk == 2) {
			if op == parser.OpDiv {
				if rf == 0 {
					return nil, true, nil
				}
				return int64(lf / rf), true, nil
			}
			return int64(lf * rf), true, nil
		}
		switch op {
		case parser.OpEQ:
			return lf == rf, true, nil
		case parser.OpNEQ:
			return lf != rf, true, nil
		case parser.OpLT:
			return lf < rf, true, nil
		case parser.OpLTE:
			return lf <= rf, true, nil
		case parser.OpGT:
			return lf > rf, true, nil
		case parser.OpGTE:
			return lf >= rf, true, nil
		case parser.OpAdd:
			return lf + rf, true, nil
		case parser.OpSub:
			return lf - rf, true, nil
		case parser.OpMul:
			return lf * rf, true, nil
		case parser.OpDiv:
			return lf / rf, true, nil // IEEE: x/0 is ±Inf, 0/0 NaN
		case parser.OpMod:
			if rf == 0 {
				return nil, true, nil
			}
			m := math.Mod(lf, rf)
			if m < 0 {
				m += math.Abs(rf)
			}
			return m, true, nil
		}
		return nil, false, nil
	}
	if l32 || r32 {
		v, err := evalBinaryOp(eng, types.ToInt64(left), op, types.ToInt64(right), schema, expr)
		return v, true, err
	}
	return nil, false, nil
}

// userFuncResultType types a call to a let-bound scalar function: its
// body typed against the declared parameter types.
func userFuncResultType(eng *Engine, fc *parser.FuncCall, _ *types.Schema) (types.KQLType, bool) {
	if eng == nil || eng.letContext == nil {
		return 0, false
	}
	fn, ok := eng.lets().LookupFunction(fc.RawName)
	if !ok {
		fn, ok = eng.lets().LookupFunction(fc.Name)
	}
	if !ok || fn.Tabular || fn.Body == nil {
		return 0, false
	}
	if eng.udfTypeDepth.Add(1) > 16 {
		eng.udfTypeDepth.Add(-1)
		return types.TypeDynamic, true
	}
	defer eng.udfTypeDepth.Add(-1)
	ps := &types.Schema{Columns: make([]types.Column, len(fn.Params))}
	for i, p := range fn.Params {
		ps.Columns[i] = types.Column{Name: p.Name, Type: p.Type}
	}
	return inferExprType(eng, fn.Body, ps), true
}

// evalStarPredicate evaluates * <op> value (where * has "x"): true
// when any column, as a string, satisfies the operator.
func evalStarPredicate(eng *Engine, e *parser.BinaryExpr, schema *types.Schema, row types.Row) (types.Value, error) {
	if !isStringOperator(e.Op) && e.Op != parser.OpEQ {
		return nil, fmt.Errorf("* is only supported with a string operator (has, contains, ==, ...)")
	}
	right, err := evalExpr(eng, e.Right, schema, row)
	if err != nil {
		return nil, err
	}
	for i, col := range schema.Columns {
		if i >= len(row) {
			break
		}
		if col.Name == timeReceivedColumnName {
			continue // hidden
		}
		v := kqlToString(row[i], col.Type)
		got, err := evalBinaryOp(eng, v, e.Op, right, schema, e)
		if err != nil {
			return nil, err
		}
		if b, ok := got.(bool); ok && b {
			return true, nil
		}
	}
	return false, nil
}

// toScalarInExpr is evalToScalarOnce, reached through a variable:
// evalExpr calling it directly makes an initialization cycle
// (Execute -> ... -> the evaluate plugin table -> evalExpr).
var toScalarInExpr func(*Engine, *parser.ToScalarExpr) (types.Value, types.KQLType, error)

func init() { toScalarInExpr = evalToScalarOnce }

// toScalarType is the type of a toscalar() reached unsubstituted (in a
// user-function body): the subquery's first column type, found by
// running it once with the schema's columns (the parameters) bound to
// zero values of their types, and cached per expression.
func toScalarType(eng *Engine, ex *parser.ToScalarExpr, schema *types.Schema) types.KQLType {
	if eng == nil {
		return types.TypeDynamic
	}
	if t, ok := eng.toScalarTypes.Load(ex); ok {
		return t.(types.KQLType)
	}
	ctx := &LetContext{
		Scalars:     make(map[string]types.Value),
		ScalarTypes: make(map[string]types.KQLType),
		Functions:   make(map[string]*parser.FunctionDef),
		Tables:      make(map[string]*types.Table),
		Parent:      eng.letContext,
	}
	for _, c := range schema.Columns {
		ctx.Scalars[c.Name] = zeroValueOf(c.Type)
		ctx.ScalarTypes[c.Name] = c.Type
	}
	prev := eng.letContext
	eng.setLetContext(ctx)
	_, t, err := toScalarInExpr(eng, ex)
	eng.setLetContext(prev)
	if err != nil {
		t = types.TypeDynamic
	}
	eng.toScalarTypes.Store(ex, t)
	return t
}

func zeroValueOf(t types.KQLType) types.Value {
	switch t {
	case types.TypeLong, types.TypeDatetime, types.TypeTimespan:
		return int64(0)
	case types.TypeInt:
		return int32(0)
	case types.TypeReal:
		return float64(0)
	case types.TypeBool:
		return false
	case types.TypeDynamic:
		return "null"
	}
	return ""
}

// euclidMod is ADX's integer remainder: never negative, modulo |b|
// (-14 % 12 and -14 % -12 are both 10; -90m % 1h is 30m).
func euclidMod(a, b int64) int64 {
	m := a % b
	if m < 0 {
		if b < 0 {
			m -= b
		} else {
			m += b
		}
	}
	return m
}
