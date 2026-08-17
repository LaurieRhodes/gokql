package engine

import (
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
	vortex "github.com/LaurieRhodes/vortex-go"
)

// extractZoneFilter extracts simple predicates suitable for pushdown from
// the pipeline's LEADING consecutive where clauses only. Extraction must
// stop at the first non-where operator: anything later may reference
// columns whose names no longer map to storage (project-rename can alias
// a stored column's name onto different data, extend can shadow it), and
// pushing such a predicate against the stored column of the same name
// silently drops matching rows. Only numeric comparisons against literals
// are extractable (zone maps store min/max for primitive types only).
func extractZoneFilter(operators []parser.Operator, schema *types.Schema) *vortex.RowFilter {
	var preds []vortex.ColumnPredicate

	for _, op := range operators {
		where, ok := op.(*parser.WhereOp)
		if !ok {
			break
		}
		// Extract predicates from this where clause
		extractPredicates(where.Predicate, schema, &preds)
	}

	if len(preds) == 0 {
		return nil
	}
	return vortex.NewRowFilter(preds...)
}

// extractPredicates recursively walks a predicate expression and collects
// simple column-op-literal comparisons suitable for zone map pruning.
// Supports numeric and datetime columns. Constant expressions (e.g. ago(24h))
// are evaluated at plan time to extract the pushdown value.
func extractPredicates(expr parser.Expr, schema *types.Schema, preds *[]vortex.ColumnPredicate) {
	switch e := expr.(type) {
	case *parser.BinaryExpr:
		// AND: recurse both sides
		if e.Op == parser.OpAnd {
			extractPredicates(e.Left, schema, preds)
			extractPredicates(e.Right, schema, preds)
			return
		}

		// Extract column reference and constant value from either side
		col, val, colLeft := extractColAndConst(e, schema)
		if col == nil || val == nil {
			return
		}

		// Only push down numeric and datetime columns (zone maps track min/max for primitives)
		colIdx := schema.ColumnIndex(col.Name)
		if colIdx < 0 {
			return
		}
		colType := schema.Columns[colIdx].Type
		if colType != types.TypeLong && colType != types.TypeInt && colType != types.TypeReal && colType != types.TypeDatetime {
			return
		}

		// Map KQL operator to Vortex comparison
		var pred vortex.ColumnPredicate
		switch e.Op {
		case parser.OpEQ:
			pred = vortex.EQ(col.Name, val)
		case parser.OpNEQ:
			pred = vortex.NEQ(col.Name, val)
		case parser.OpGT:
			if colLeft {
				pred = vortex.GT(col.Name, val)
			} else {
				pred = vortex.LT(col.Name, val)
			}
		case parser.OpGTE:
			if colLeft {
				pred = vortex.GTE(col.Name, val)
			} else {
				pred = vortex.LTE(col.Name, val)
			}
		case parser.OpLT:
			if colLeft {
				pred = vortex.LT(col.Name, val)
			} else {
				pred = vortex.GT(col.Name, val)
			}
		case parser.OpLTE:
			if colLeft {
				pred = vortex.LTE(col.Name, val)
			} else {
				pred = vortex.GTE(col.Name, val)
			}
		default:
			return // contains, has, startswith etc. not pushable to zone maps
		}

		*preds = append(*preds, pred)
	}
}

// extractColAndConst extracts a column reference and a constant value from a
// binary comparison expression. The constant can be a literal or a constant
// function like ago(24h), now(), datetime(...).
// Returns (column, value, columnIsOnLeft) or (nil, nil, false) if not extractable.
func extractColAndConst(e *parser.BinaryExpr, schema *types.Schema) (*parser.ColumnRef, interface{}, bool) {
	if col, ok := e.Left.(*parser.ColumnRef); ok {
		if val, ok := tryEvalConstant(e.Right, schema); ok {
			return col, val, true
		}
	}
	if col, ok := e.Right.(*parser.ColumnRef); ok {
		if val, ok := tryEvalConstant(e.Left, schema); ok {
			return col, val, false
		}
	}
	return nil, nil, false
}

// tryEvalConstant attempts to evaluate an expression as a constant (no row data needed).
// Returns the value and true if successful, or nil and false if the expression
// references columns or cannot be evaluated statically.
func tryEvalConstant(expr parser.Expr, schema *types.Schema) (interface{}, bool) {
	switch e := expr.(type) {
	case *parser.Literal:
		return e.Value, true
	case *parser.FuncCall:
		// Try to evaluate constant functions (now, ago, datetime)
		if isConstantFunc(e) {
			val, err := evalFunc(e, schema, nil)
			if err == nil && val != nil {
				return val, true
			}
		}
	case *parser.BinaryExpr:
		// Constant arithmetic: e.g. now() - 1h
		lv, lok := tryEvalConstant(e.Left, schema)
		rv, rok := tryEvalConstant(e.Right, schema)
		if lok && rok {
			result, err := evalBinaryOp(lv, e.Op, rv, schema, e)
			if err == nil {
				return result, true
			}
		}
	}
	return nil, false
}

// isConstantFunc returns true if a function call produces a constant value
// (no column references in arguments).
func isConstantFunc(fc *parser.FuncCall) bool {
	switch fc.Name {
	case "now":
		return true
	case "ago", "datetime", "todatetime":
		// Constant if all arguments are constant
		for _, arg := range fc.Args {
			if !isConstantExpr(arg) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// isConstantExpr returns true if an expression has no column references.
func isConstantExpr(expr parser.Expr) bool {
	switch e := expr.(type) {
	case *parser.Literal:
		return true
	case *parser.ColumnRef:
		return false
	case *parser.FuncCall:
		for _, arg := range e.Args {
			if !isConstantExpr(arg) {
				return false
			}
		}
		return true
	case *parser.BinaryExpr:
		return isConstantExpr(e.Left) && isConstantExpr(e.Right)
	case *parser.UnaryExpr:
		return isConstantExpr(e.Expr)
	default:
		return false
	}
}
