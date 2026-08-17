package engine

import (
	"fmt"
	"regexp"
	"strings"

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
		return &parser.FuncCall{Name: ex.Name, Args: newArgs}, nil

	case *parser.AccessExpr:
		// Path ([]AccessKey) is static metadata (property names / array
		// indices, both plain string/int, never a parser.Expr) — never
		// needs rewriting, only Object itself might.
		obj, err := substituteToScalars(e, ex.Object)
		if err != nil {
			return nil, err
		}
		if obj == ex.Object {
			return expr, nil
		}
		return &parser.AccessExpr{Object: obj, Path: ex.Path}, nil

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
func evalToScalarOnce(e *Engine, ex *parser.ToScalarExpr) (types.Value, types.KQLType, error) {
	stmt, err := parser.Parse(ex.QueryText)
	if err != nil {
		return nil, types.TypeDynamic, fmt.Errorf("toscalar(%s): %w", ex.QueryText, err)
	}
	result, err := e.Execute(stmt)
	if err != nil {
		return nil, types.TypeDynamic, fmt.Errorf("toscalar(%s): %w", ex.QueryText, err)
	}
	if result == nil || len(result.Rows) == 0 || len(result.Schema.Columns) == 0 {
		return nil, types.TypeDynamic, nil
	}
	return result.Rows[0][0], result.Schema.Columns[0].Type, nil
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

func evalExpr(expr parser.Expr, schema *types.Schema, row types.Row) (types.Value, error) {
	switch e := expr.(type) {
	case *parser.Literal:
		return e.Value, nil

	case *parser.ColumnRef:
		idx := schema.ColumnIndex(e.Name)
		if idx >= 0 {
			return row[idx], nil
		}
		// Check scalar let bindings — walks the parent chain, not
		// just the innermost context's own local map.
		if val, ok := activeLetContext.LookupScalar(e.Name); ok {
			return val, nil
		}
		return nil, fmt.Errorf("column %q not found", e.Name)

	case *parser.BinaryExpr:
		left, err := evalExpr(e.Left, schema, row)
		if err != nil {
			return nil, err
		}
		right, err := evalExpr(e.Right, schema, row)
		if err != nil {
			return nil, err
		}
		return evalBinaryOp(left, e.Op, right, schema, e)

	case *parser.UnaryExpr:
		val, err := evalExpr(e.Expr, schema, row)
		if err != nil {
			return nil, err
		}
		if e.Op == "not" {
			if b, ok := val.(bool); ok {
				return !b, nil
			}
			return false, nil
		}
		return nil, fmt.Errorf("unsupported unary op: %s", e.Op)

	case *parser.FuncCall:
		return evalFunc(e, schema, row)

	case *parser.AccessExpr:
		return evalAccess(e, schema, row)

	case *parser.InExpr:
		colVal, err := evalExpr(e.Column, schema, row)
		if err != nil {
			return nil, err
		}
		if colVal == nil {
			return false, nil
		}

		found := false
		colStr := fmt.Sprintf("%v", colVal)

		if e.TableRef != "" {
			// Check against a let-bound table's first column — walks
			// the parent chain, not just the innermost context's own
			// local map.
			if tbl, ok := activeLetContext.LookupTable(e.TableRef); ok {
				for _, row := range tbl.Rows {
					if len(row) > 0 {
						rowStr := fmt.Sprintf("%v", row[0])
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
		} else {
			// Check against literal value list
			for _, valExpr := range e.Values {
				val, err := evalExpr(valExpr, schema, row)
				if err != nil {
					return nil, err
				}
				valStr := fmt.Sprintf("%v", val)
				if e.CaseInsensitive {
					found = strings.EqualFold(colStr, valStr)
				} else {
					found = colStr == valStr
				}
				if found {
					break
				}
			}
		}

		if e.Negated {
			return !found, nil
		}
		return found, nil

	case *parser.HasAnyAllExpr:
		colVal, err := evalExpr(e.Column, schema, row)
		if err != nil {
			return nil, err
		}
		if colVal == nil {
			return false, nil
		}
		colStr := fmt.Sprintf("%v", colVal)

		if e.All {
			// has_all: every term must be found (whole-term, case-insensitive)
			for _, valExpr := range e.Values {
				val, err := evalExpr(valExpr, schema, row)
				if err != nil {
					return nil, err
				}
				term := fmt.Sprintf("%v", val)
				if !hasTerm(colStr, term, false) {
					return false, nil
				}
			}
			return true, nil
		}
		// has_any: at least one term must be found (whole-term, case-insensitive)
		for _, valExpr := range e.Values {
			val, err := evalExpr(valExpr, schema, row)
			if err != nil {
				return nil, err
			}
			term := fmt.Sprintf("%v", val)
			if hasTerm(colStr, term, false) {
				return true, nil
			}
		}
		return false, nil

	case *parser.BetweenExpr:
		val, err := evalExpr(e.Expr, schema, row)
		if err != nil || val == nil {
			return false, err
		}
		low, err := evalExpr(e.Low, schema, row)
		if err != nil || low == nil {
			return false, err
		}
		high, err := evalExpr(e.High, schema, row)
		if err != nil || high == nil {
			return false, err
		}
		// Infer type from the value being tested
		vt := inferValType(val)
		inRange := types.CompareValues(val, low, vt) >= 0 && types.CompareValues(val, high, vt) <= 0
		if e.Negated {
			return !inRange, nil
		}
		return inRange, nil

	default:
		return nil, fmt.Errorf("unsupported expression type: %T", expr)
	}
}

func evalBinaryOp(left types.Value, op parser.BinaryOp, right types.Value, schema *types.Schema, expr *parser.BinaryExpr) (types.Value, error) {
	// Logical operators
	if op == parser.OpAnd {
		lb, _ := left.(bool)
		rb, _ := right.(bool)
		return lb && rb, nil
	}
	if op == parser.OpOr {
		lb, _ := left.(bool)
		rb, _ := right.(bool)
		return lb || rb, nil
	}

	// Null handling for comparisons
	if left == nil || right == nil {
		if op == parser.OpEQ {
			return left == nil && right == nil, nil
		}
		return false, nil
	}

	// String operators (case-insensitive by default, _cs variants are case-sensitive)
	if op == parser.OpContains || op == parser.OpNotContains ||
		op == parser.OpContainsCS || op == parser.OpNotContainsCS ||
		op == parser.OpHas || op == parser.OpNotHas ||
		op == parser.OpHasCS || op == parser.OpNotHasCS ||
		op == parser.OpStartsWith || op == parser.OpNotStartsWith ||
		op == parser.OpStartsWithCS || op == parser.OpNotStartsWithCS ||
		op == parser.OpEndsWith || op == parser.OpNotEndsWith ||
		op == parser.OpEndsWithCS || op == parser.OpNotEndsWithCS ||
		op == parser.OpMatchesRegex ||
		op == parser.OpCIEQ || op == parser.OpCINEQ ||
		op == parser.OpLike || op == parser.OpNotLike ||
		op == parser.OpHasPrefix || op == parser.OpNotHasPrefix ||
		op == parser.OpHasPrefixCS || op == parser.OpNotHasPrefixCS ||
		op == parser.OpHasSuffix || op == parser.OpNotHasSuffix ||
		op == parser.OpHasSuffixCS || op == parser.OpNotHasSuffixCS {
		ls := fmt.Sprintf("%v", left)
		rs := fmt.Sprintf("%v", right)
		switch op {
		case parser.OpContains:
			return strings.Contains(strings.ToLower(ls), strings.ToLower(rs)), nil
		case parser.OpNotContains:
			return !strings.Contains(strings.ToLower(ls), strings.ToLower(rs)), nil
		case parser.OpContainsCS:
			return strings.Contains(ls, rs), nil
		case parser.OpNotContainsCS:
			return !strings.Contains(ls, rs), nil
		case parser.OpHas:
			return hasTerm(ls, rs, false), nil
		case parser.OpNotHas:
			return !hasTerm(ls, rs, false), nil
		case parser.OpHasCS:
			return hasTerm(ls, rs, true), nil
		case parser.OpNotHasCS:
			return !hasTerm(ls, rs, true), nil
		case parser.OpStartsWith:
			return strings.HasPrefix(strings.ToLower(ls), strings.ToLower(rs)), nil
		case parser.OpNotStartsWith:
			return !strings.HasPrefix(strings.ToLower(ls), strings.ToLower(rs)), nil
		case parser.OpStartsWithCS:
			return strings.HasPrefix(ls, rs), nil
		case parser.OpNotStartsWithCS:
			return !strings.HasPrefix(ls, rs), nil
		case parser.OpEndsWith:
			return strings.HasSuffix(strings.ToLower(ls), strings.ToLower(rs)), nil
		case parser.OpNotEndsWith:
			return !strings.HasSuffix(strings.ToLower(ls), strings.ToLower(rs)), nil
		case parser.OpEndsWithCS:
			return strings.HasSuffix(ls, rs), nil
		case parser.OpNotEndsWithCS:
			return !strings.HasSuffix(ls, rs), nil
		case parser.OpMatchesRegex:
			re, err := regexp.Compile(rs)
			if err != nil {
				return nil, fmt.Errorf("matches regex: invalid pattern %q: %w", rs, err)
			}
			return re.MatchString(ls), nil
		case parser.OpCIEQ:
			return strings.EqualFold(ls, rs), nil
		case parser.OpCINEQ:
			return !strings.EqualFold(ls, rs), nil
		case parser.OpLike:
			return matchLike(ls, rs, false), nil
		case parser.OpNotLike:
			return !matchLike(ls, rs, false), nil
		case parser.OpHasPrefix:
			return hasTermPrefix(ls, rs, false), nil
		case parser.OpNotHasPrefix:
			return !hasTermPrefix(ls, rs, false), nil
		case parser.OpHasPrefixCS:
			return hasTermPrefix(ls, rs, true), nil
		case parser.OpNotHasPrefixCS:
			return !hasTermPrefix(ls, rs, true), nil
		case parser.OpHasSuffix:
			return hasTermSuffix(ls, rs, false), nil
		case parser.OpNotHasSuffix:
			return !hasTermSuffix(ls, rs, false), nil
		case parser.OpHasSuffixCS:
			return hasTermSuffix(ls, rs, true), nil
		case parser.OpNotHasSuffixCS:
			return !hasTermSuffix(ls, rs, true), nil
		}
	}

	// Determine comparison type from left operand
	switch left.(type) {
	case string:
		ls, rs := left.(string), fmt.Sprintf("%v", right)
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
			lt := inferExprType(expr.Left, schema)
			rt := inferExprType(expr.Right, schema)

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
			return li % ri, nil
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
			if rf == 0 {
				return nil, nil
			}
			return lf / rf, nil
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
func inferExprType(expr parser.Expr, schema *types.Schema) types.KQLType {
	switch e := expr.(type) {
	case *parser.Literal:
		return e.Type
	case *parser.ColumnRef:
		idx := schema.ColumnIndex(e.Name)
		if idx >= 0 {
			return schema.Columns[idx].Type
		}
		return types.TypeString
	case *parser.FuncCall:
		switch e.Name {
		case "now", "ago", "datetime", "todatetime", "make_datetime",
			"startofday", "startofweek", "startofmonth", "startofyear",
			"endofday", "endofweek", "endofmonth",
			"unixtime_seconds_todatetime", "unixtime_milliseconds_todatetime":
			return types.TypeDatetime
		case "bin", "floor":
			// bin returns the same type as its first argument
			if len(e.Args) > 0 {
				return inferExprType(e.Args[0], schema)
			}
			return types.TypeLong
		case "dayofweek":
			return types.TypeTimespan
		case "datetime_diff", "dayofmonth", "getmonth", "getyear",
			"hourofday", "monthofyear", "strlen", "toint", "tolong":
			return types.TypeLong
		case "todouble", "toreal", "series_cosine_similarity", "series_dot_product":
			return types.TypeReal
		case "tolower", "toupper", "tostring", "strcat", "format_datetime",
			"substring", "replace_string", "replace_regex", "trim", "trim_start", "trim_end",
			"reverse", "hash_sha256", "base64_encode_tostring", "base64_decode_tostring",
			"url_encode_component", "url_encode", "strcat_array":
			return types.TypeString
		case "split", "extract_all", "parse_url", "parse_urlquery", "parse_path", "embed_text":
			return types.TypeDynamic
		case "extract":
			return types.TypeString
		case "indexof", "countof", "parse_ipv4", "array_length", "array_index_of", "has_any_index":
			return types.TypeLong
		case "array_concat", "array_reverse", "array_slice",
			"array_sort_asc", "array_sort_desc", "bag_keys",
			"pack", "pack_array", "set_difference", "set_intersect",
			"set_union", "treepath":
			return types.TypeDynamic
		case "isnull", "isnotnull", "isempty", "isnotempty",
			"ipv4_is_private", "ipv4_is_in_range", "bag_has_key":
			return types.TypeBool
		case "iff", "iif", "case", "coalesce", "max_of", "min_of":
			// Return type depends on arguments — default to string
			return types.TypeString
		default:
			return types.TypeString
		}
	case *parser.BinaryExpr:
		switch e.Op {
		case parser.OpAdd, parser.OpSub:
			lt := inferExprType(e.Left, schema)
			rt := inferExprType(e.Right, schema)
			// datetime - datetime = timespan
			if e.Op == parser.OpSub && lt == types.TypeDatetime && rt == types.TypeDatetime {
				return types.TypeTimespan
			}
			// datetime +/- timespan = datetime
			if lt == types.TypeDatetime || rt == types.TypeDatetime {
				return types.TypeDatetime
			}
			// timespan +/- timespan = timespan
			if lt == types.TypeTimespan && rt == types.TypeTimespan {
				return types.TypeTimespan
			}
			return lt
		case parser.OpMul, parser.OpDiv:
			lt := inferExprType(e.Left, schema)
			if lt == types.TypeTimespan {
				return types.TypeTimespan
			}
			return lt
		case parser.OpEQ, parser.OpNEQ, parser.OpLT, parser.OpLTE,
			parser.OpGT, parser.OpGTE, parser.OpAnd, parser.OpOr,
			parser.OpContains, parser.OpNotContains, parser.OpHas, parser.OpNotHas,
			parser.OpStartsWith, parser.OpEndsWith:
			return types.TypeBool
		default:
			return inferExprType(e.Left, schema)
		}
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

// evalExprWithWindow evaluates an expression with window-function awareness.
// Window functions (row_number, prev, next) use rowIdx and allRows.
func evalExprWithWindow(expr parser.Expr, schema *types.Schema, row types.Row, rowIdx int, allRows []types.Row) (interface{}, error) {
	// Check if this is a window function call
	if fc, ok := expr.(*parser.FuncCall); ok {
		switch fc.Name {
		case "row_number":
			// row_number() — 1-based row index
			return int64(rowIdx + 1), nil

		case "prev":
			// prev(col [, offset [, default_value]])
			if len(fc.Args) < 1 {
				return nil, fmt.Errorf("prev requires at least 1 argument")
			}
			offset := 1
			if len(fc.Args) >= 2 {
				offsetVal, err := evalExpr(fc.Args[1], schema, row)
				if err != nil {
					return nil, err
				}
				if n, ok2 := offsetVal.(int64); ok2 {
					offset = int(n)
				}
			}
			var defaultVal interface{}
			if len(fc.Args) >= 3 {
				var err error
				defaultVal, err = evalExpr(fc.Args[2], schema, row)
				if err != nil {
					return nil, err
				}
			}
			prevIdx := rowIdx - offset
			if prevIdx < 0 || prevIdx >= len(allRows) {
				return defaultVal, nil
			}
			return evalExpr(fc.Args[0], schema, allRows[prevIdx])

		case "next":
			// next(col [, offset [, default_value]])
			if len(fc.Args) < 1 {
				return nil, fmt.Errorf("next requires at least 1 argument")
			}
			offset := 1
			if len(fc.Args) >= 2 {
				offsetVal, err := evalExpr(fc.Args[1], schema, row)
				if err != nil {
					return nil, err
				}
				if n, ok2 := offsetVal.(int64); ok2 {
					offset = int(n)
				}
			}
			var defaultVal interface{}
			if len(fc.Args) >= 3 {
				var err error
				defaultVal, err = evalExpr(fc.Args[2], schema, row)
				if err != nil {
					return nil, err
				}
			}
			nextIdx := rowIdx + offset
			if nextIdx < 0 || nextIdx >= len(allRows) {
				return defaultVal, nil
			}
			return evalExpr(fc.Args[0], schema, allRows[nextIdx])
		}
	}

	// Not a window function — use standard eval
	return evalExpr(expr, schema, row)
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
