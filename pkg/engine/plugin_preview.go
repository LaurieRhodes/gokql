package engine

// plugin_preview.go — evaluate preview(N): the first N input rows, and
// a second result table holding the input's row count (Count:long).

import (
	"fmt"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["preview"] = applyPreview
}

func applyPreview(e *Engine, input *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("preview: expected (NumberOfRows)")
	}
	v, err := evalConstExpr(e, args[0])
	if err != nil {
		return nil, fmt.Errorf("preview: %w", err)
	}
	n := types.ToInt64(v)
	if n < 0 {
		return nil, fmt.Errorf("preview: NumberOfRows must not be negative")
	}
	out := types.NewTable(input.Name, input.Schema)
	if int64(len(input.Rows)) < n {
		n = int64(len(input.Rows))
	}
	out.Rows = input.Rows[:n]
	count := types.NewTable("", types.Schema{Columns: []types.Column{{Name: "Count", Type: types.TypeLong}}})
	count.AddRow(types.Row{int64(len(input.Rows))})
	e.extraResults = append(e.extraResults, count)
	return out, nil
}
