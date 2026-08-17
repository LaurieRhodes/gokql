package engine

import (
	"encoding/json"
	"fmt"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// applyScan implements | scan — see ScanOp's own doc comment
// (pkg/parser/ast.go) for exactly which parts of real ADX's documented
// grammar this covers (a single step; state persists across rows in
// input order).
//
// StepName.ColumnName references (e.g. `s1.cumulative_x`) inside the
// step's own Condition/Assignment expressions are resolved by reusing
// this engine's EXISTING JSON dot-access machinery (evalAccess,
// functions.go — the same code path `col.property` already uses for a
// dynamic column) rather than adding a new expression-evaluation case:
// before evaluating Condition/Assignments for a row, a synthetic
// column literally named op.StepName is appended to the row, holding
// the CURRENT state (the declared columns' values before this row's
// update) JSON-encoded as an object — `s1.cumulative_x` then resolves
// exactly the way `SomeDynamicCol.property` already does, for free.
// All of a step's own Assignments see the SAME pre-update state (not
// chained against each other), matching how the docs' own worked
// examples read a step as one atomic transition, not a sequential
// extend-like accumulation within itself.
func (e *Engine) applyScan(input *types.Table, op *parser.ScanOp) (*types.Table, error) {
	emptySchema := types.Schema{}
	emptyRow := types.Row{}

	// Resolve each declared column's type and initial (pre-first-match)
	// state value.
	declTypes := make([]types.KQLType, len(op.Declares))
	state := make(map[string]types.Value, len(op.Declares))
	for i, d := range op.Declares {
		t, err := types.ParseType(d.Type)
		if err != nil {
			return nil, fmt.Errorf("scan declare %q: %w", d.Name, err)
		}
		declTypes[i] = t
		if d.Default != nil {
			v, err := evalExpr(d.Default, &emptySchema, emptyRow)
			if err != nil {
				return nil, fmt.Errorf("scan declare %q: default: %w", d.Name, err)
			}
			state[d.Name] = coerceScanValue(v, t)
		} else {
			state[d.Name] = coerceScanValue(int64(0), t) // KQL's own implicit zero-value default
		}
	}

	// Output schema: input columns + declared columns (declare(...) is
	// how real ADX's own docs describe the output shape: "The schema
	// of the output is the schema of the source extended with the
	// column in the declare clause").
	outCols := make([]types.Column, len(input.Schema.Columns))
	copy(outCols, input.Schema.Columns)
	declOffset := len(outCols)
	for i, d := range op.Declares {
		outCols = append(outCols, types.Column{Name: d.Name, Type: declTypes[i]})
	}
	output := types.NewTable(input.Name, types.Schema{Columns: outCols})

	// Augmented schema/row used only for evaluating this step's own
	// Condition/Assignments — input columns + one synthetic StepName
	// column holding the current state as a JSON object.
	augCols := make([]types.Column, len(input.Schema.Columns)+1)
	copy(augCols, input.Schema.Columns)
	stateColIdx := len(input.Schema.Columns)
	augCols[stateColIdx] = types.Column{Name: op.StepName, Type: types.TypeDynamic}
	augSchema := types.Schema{Columns: augCols}

	for _, row := range input.Rows {
		stateJSON, err := marshalScanState(state, op.Declares)
		if err != nil {
			return nil, fmt.Errorf("scan: encoding state: %w", err)
		}
		augRow := make(types.Row, len(augCols))
		copy(augRow, row)
		augRow[stateColIdx] = stateJSON

		condVal, err := evalExpr(op.Condition, &augSchema, augRow)
		if err != nil {
			return nil, fmt.Errorf("scan step %s: condition: %w", op.StepName, err)
		}
		matched, _ := condVal.(bool)
		if !matched {
			continue // no state change, no output row — "A record for
			// each MATCH of a record from the input to a step" per
			// real ADX docs; a non-matching row produces nothing.
		}

		newState := make(map[string]types.Value, len(state))
		for k, v := range state {
			newState[k] = v
		}
		for _, a := range op.Assignments {
			v, err := evalExpr(a.Expr, &augSchema, augRow)
			if err != nil {
				return nil, fmt.Errorf("scan step %s: assignment %s: %w", op.StepName, a.Column, err)
			}
			declIdx := -1
			for i, d := range op.Declares {
				if d.Name == a.Column {
					declIdx = i
					break
				}
			}
			if declIdx < 0 {
				return nil, fmt.Errorf("scan step %s: assignment to undeclared column %q", op.StepName, a.Column)
			}
			newState[a.Column] = coerceScanValue(v, declTypes[declIdx])
		}
		state = newState

		if op.Output == "none" {
			continue
		}
		outRow := make(types.Row, len(outCols))
		copy(outRow, row)
		for i, d := range op.Declares {
			outRow[declOffset+i] = state[d.Name]
		}
		output.AddRow(outRow)
	}

	return output, nil
}

// marshalScanState encodes the current declared-column state as a JSON
// object string, in declare order, for evalAccess's own JSON dot-path
// walk to consume via the synthetic StepName column.
func marshalScanState(state map[string]types.Value, declares []parser.ScanDeclare) (string, error) {
	obj := make(map[string]interface{}, len(declares))
	for _, d := range declares {
		obj[d.Name] = state[d.Name]
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// coerceScanValue converts v to the Go representation matching t —
// shared shape with make_series.go's coerceNumeric, but broader (scan's
// declared columns aren't restricted to numeric types the way
// make-series's aggregation outputs are).
func coerceScanValue(v types.Value, t types.KQLType) types.Value {
	if v == nil {
		return nil
	}
	switch t {
	case types.TypeReal:
		return types.ToFloat64(v)
	case types.TypeLong, types.TypeInt:
		return types.ToInt64(v)
	case types.TypeBool:
		if b, ok := v.(bool); ok {
			return b
		}
		return v
	case types.TypeString, types.TypeGUID, types.TypeDynamic:
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", v)
	default:
		return v
	}
}

