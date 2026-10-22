package engine

// func_column_ifexists.go — column_ifexists(columnName, defaultValue)
// and its deprecated alias columnifexists: the named column's value
// when the input has that column, otherwise defaultValue. columnName
// must be a constant string (Kusto.Language: ArgumentKind.Constant).
//
// Unnamed, the result takes the column's name when it exists
// (Kusto.Language: ResultNameKind.FirstArgumentValueIfColumn), and the
// default column's name when the default is a column reference, as in
// the reference example (project column_ifexists("Capital", State)
// gives a column named State).

import (
	"fmt"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func isColumnIfExists(name string) bool {
	return name == "column_ifexists" || name == "columnifexists"
}

// columnIfExistsName returns the constant column name argument.
func columnIfExistsName(fc *parser.FuncCall) (string, error) {
	if len(fc.Args) != 2 {
		return "", fmt.Errorf("%s(columnName, defaultValue) takes 2 arguments", fc.Name)
	}
	lit, ok := fc.Args[0].(*parser.Literal)
	if !ok {
		return "", fmt.Errorf("%s: columnName must be a constant string", fc.Name)
	}
	s, ok := lit.Value.(string)
	if !ok {
		return "", fmt.Errorf("%s: columnName must be a string", fc.Name)
	}
	return s, nil
}

func evalColumnIfExists(fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	if !isColumnIfExists(fc.Name) {
		return nil, false, nil
	}
	name, err := columnIfExistsName(fc)
	if err != nil {
		return nil, true, err
	}
	if idx := schema.ColumnIndex(name); idx >= 0 {
		if idx < len(row) {
			return row[idx], true, nil
		}
		return nil, true, nil
	}
	v, err := evalExpr(fc.Args[1], schema, row)
	return v, true, err
}

// columnIfExistsType: the column's type when it exists, otherwise the
// default's.
func columnIfExistsType(fc *parser.FuncCall, schema *types.Schema) types.KQLType {
	if name, err := columnIfExistsName(fc); err == nil {
		if c := schema.ColumnByName(name); c != nil {
			return c.Type
		}
	}
	if len(fc.Args) == 2 {
		return inferExprType(fc.Args[1], schema)
	}
	return types.TypeDynamic
}

// autoColumnName refines the parser's generated name (the expression
// text) for an unnamed column_ifexists item.
func autoColumnName(name string, expr parser.Expr, schema *types.Schema) string {
	fc, ok := expr.(*parser.FuncCall)
	if !ok || !isColumnIfExists(fc.Name) || isPlainIdentifier(name) {
		return name
	}
	if col, err := columnIfExistsName(fc); err == nil && schema.ColumnIndex(col) >= 0 {
		return col
	}
	if len(fc.Args) == 2 {
		if ref, ok := fc.Args[1].(*parser.ColumnRef); ok {
			return ref.Name
		}
	}
	return name
}

func isPlainIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || (i > 0 && c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}
