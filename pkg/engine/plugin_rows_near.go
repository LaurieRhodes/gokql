package engine

// plugin_rows_near.go — evaluate rows_near(Condition, NumRows [, RowsAfter]):
// every input row within NumRows before, or NumRows (RowsAfter when
// given) after, a row where Condition is true, in input order.

import (
	"fmt"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["rows_near"] = applyRowsNear
}

func applyRowsNear(e *Engine, input *types.Table, argTexts []string, _ *types.Schema) (*types.Table, error) {
	if len(argTexts) < 2 || len(argTexts) > 3 {
		return nil, fmt.Errorf("rows_near: expected (Condition, NumRows [, RowsAfter])")
	}
	cond, err := parser.ParseExpr(argTexts[0])
	if err != nil {
		return nil, fmt.Errorf("rows_near: condition: %w", err)
	}
	count := func(text, what string) (int, error) {
		v, err := evalConstExpr(e, text)
		if err != nil {
			return 0, fmt.Errorf("rows_near: %s: %w", what, err)
		}
		n, ok := toInt64(v)
		if !ok || n < 0 {
			return 0, fmt.Errorf("rows_near: %s must be a non-negative integer", what)
		}
		return int(n), nil
	}
	before, err := count(argTexts[1], "NumRows")
	if err != nil {
		return nil, err
	}
	after := before
	if len(argTexts) == 3 {
		if after, err = count(argTexts[2], "RowsAfter"); err != nil {
			return nil, err
		}
	}

	keep := make([]bool, len(input.Rows))
	for i, row := range input.Rows {
		v, err := evalExpr(e, cond, &input.Schema, row)
		if err != nil {
			return nil, fmt.Errorf("rows_near: condition: %w", err)
		}
		if b, _ := v.(bool); !b {
			continue
		}
		for j := i - before; j <= i+after; j++ {
			if j >= 0 && j < len(keep) {
				keep[j] = true
			}
		}
	}
	out := types.NewTable(input.Name, input.Schema)
	for i, row := range input.Rows {
		if keep[i] {
			out.AddRow(row)
		}
	}
	return out, nil
}
