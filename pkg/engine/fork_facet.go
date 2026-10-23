package engine

// fork_facet.go — operators that return several result tables.
//
// fork runs each subquery on its input; facet returns its with-clause
// table (when given) and one table per column: the column's values
// and count_<Column>, in order of first appearance. Both must be the
// last operator of their pipeline. Execute returns the first table;
// ExecuteAll returns all of them, each named (fork: Name = or a final
// `as Name` in the subquery, otherwise GenericResult, as the
// reference's result tabs show; facet: the column name, and
// GenericResult for the with table).

import (
	"fmt"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// ExecuteAll executes stmt and returns every result table: the
// primary one (nil results omitted) followed by any extra tables fork
// or facet produced.
func (e *Engine) ExecuteAll(stmt parser.Statement) ([]*types.Table, error) {
	e.extraResults = nil
	defer func() { e.extraResults = nil }()
	t, err := e.Execute(stmt)
	if err != nil {
		return nil, err
	}
	var out []*types.Table
	if t != nil && !isNoResult(t) {
		out = append(out, t)
	}
	for _, x := range e.extraResults {
		out = append(out, stringNullsToEmpty(x))
	}
	return out, nil
}

func isMultiResultOp(op parser.Operator) bool {
	switch op.(type) {
	case *parser.ForkOp, *parser.FacetOp:
		return true
	}
	return false
}

// withName returns a shallow copy of t carrying name.
func withName(t *types.Table, name string) *types.Table {
	c := *t
	c.Name = name
	return &c
}

func (e *Engine) applyFork(input *types.Table, op *parser.ForkOp) (*types.Table, error) {
	var results []*types.Table
	for i, b := range op.Branches {
		t, err := e.applyPipeline(input, b.Operators)
		if err != nil {
			return nil, fmt.Errorf("fork subquery %d: %w", i+1, err)
		}
		name := b.Name
		if name == "" {
			if as, ok := b.Operators[len(b.Operators)-1].(*parser.AsOp); ok {
				name = as.Name
			} else {
				name = "GenericResult"
			}
		}
		results = append(results, withName(t, name))
	}
	e.extraResults = append(e.extraResults, results[1:]...)
	return results[0], nil
}

func (e *Engine) applyFacet(input *types.Table, op *parser.FacetOp) (*types.Table, error) {
	var results []*types.Table
	if op.With != nil {
		t, err := e.applyPipeline(input, op.With)
		if err != nil {
			return nil, fmt.Errorf("facet with: %w", err)
		}
		results = append(results, withName(t, "GenericResult"))
	}
	for _, c := range op.Columns {
		i := input.Schema.ColumnIndex(c)
		if i < 0 {
			return nil, fmt.Errorf("facet: column %q not found", c)
		}
		name := input.Schema.Columns[i].Name
		q := "__facet | summarize " + bracketName("count_"+name) + " = count() by " + bracketName(name)
		stmt, err := parser.Parse(q)
		if err != nil {
			return nil, fmt.Errorf("facet: %w", err)
		}
		t, err := e.applyPipeline(input, stmt.(*parser.Query).Operators)
		if err != nil {
			return nil, fmt.Errorf("facet by %s: %w", name, err)
		}
		results = append(results, withName(t, name))
	}
	e.extraResults = append(e.extraResults, results[1:]...)
	return results[0], nil
}

// multiResultNotLast reports a fork or facet followed by other operators.
func multiResultNotLast(ops []parser.Operator) error {
	for i, op := range ops {
		if isMultiResultOp(op) && i != len(ops)-1 {
			kw := "fork"
			if _, ok := op.(*parser.FacetOp); ok {
				kw = "facet"
			}
			return fmt.Errorf("%s must be the last operator of the query: its result tables cannot be piped further", kw)
		}
	}
	return nil
}
