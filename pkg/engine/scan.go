package engine

import (
	"fmt"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// applyScan implements | scan, the multi-step state machine of the
// reference (scan-operator.md, "Scan logic"):
//
// Each step k holds a state: empty, or a sequence carrying a match id
// and the extended record of every step 1..k. Each input record r is
// evaluated against the steps from last to first:
//
//   - Check 1: if step k-1 has a sequence and r meets step k's
//     condition (evaluated against that sequence), step k's state is
//     replaced by step k-1's sequence plus r extended with step k's
//     assignments, and step k-1 becomes empty.
//   - Check 2 (only if Check 1 did not match): if step k has a
//     sequence, or k is the first step, and r meets the condition
//     (evaluated against step k's own sequence), r's extension replaces
//     step k's record in it. The first step starting from an empty
//     state begins a new sequence with the next match id.
//
// A match is output according to the step's output mode: all, none,
// or last (only the last of a run of matches of that step, emitted
// when the sequence moves on or the input ends).
//
// In conditions and assignments, StepName.Column reads the sequence's
// record for that step; a step with no record gives the declared
// default for a declared column and null for an input column. Plain
// declared names read the record being extended: the sequence's latest
// declared values, or their defaults. Declared columns a step does not
// assign keep those values.
func (e *Engine) applyScan(input *types.Table, op *parser.ScanOp) (*types.Table, error) {
	emptySchema := types.Schema{}
	emptyRow := types.Row{}

	nIn := len(input.Schema.Columns)
	nDecl := len(op.Declares)
	width := nIn + nDecl // an extended record: input columns, then declared

	declTypes := make([]types.KQLType, nDecl)
	defaults := make([]types.Value, nDecl)
	for i, d := range op.Declares {
		t, err := types.ParseType(d.Type)
		if err != nil {
			return nil, fmt.Errorf("scan declare %q: %w", d.Name, err)
		}
		declTypes[i] = t
		if d.Default != nil {
			v, err := evalExpr(e, d.Default, &emptySchema, emptyRow)
			if err != nil {
				return nil, fmt.Errorf("scan declare %q: default: %w", d.Name, err)
			}
			defaults[i] = coerceScanValue(v, t)
		}
	}

	// Evaluation schema: input columns, declared columns, then for
	// every step a copy of both, read through StepName.Column (rewritten
	// to the hidden names).
	evalCols := make([]types.Column, 0, width*(1+len(op.Steps)))
	evalCols = append(evalCols, input.Schema.Columns...)
	for i, d := range op.Declares {
		evalCols = append(evalCols, types.Column{Name: d.Name, Type: declTypes[i]})
	}
	refs := map[string]map[string]string{}
	for _, st := range op.Steps {
		m := map[string]string{}
		for c := 0; c < width; c++ {
			hidden := "\x00scan." + st.Name + "." + evalCols[c].Name
			m[evalCols[c].Name] = hidden
			evalCols = append(evalCols, types.Column{Name: hidden, Type: evalCols[c].Type})
		}
		refs[st.Name] = m
	}
	evalSchema := &types.Schema{Columns: evalCols}

	type compiled struct {
		cond    parser.Expr
		assigns []parser.Expr
		target  []int // declared index of each assignment
	}
	steps := make([]compiled, len(op.Steps))
	for k, st := range op.Steps {
		c := compiled{cond: rewriteScanRefs(st.Condition, refs)}
		for _, a := range st.Assignments {
			idx := -1
			for i, d := range op.Declares {
				if d.Name == a.Column {
					idx = i
				}
			}
			if idx < 0 {
				return nil, fmt.Errorf("scan step %s: assignment to undeclared column %q", st.Name, a.Column)
			}
			c.assigns = append(c.assigns, rewriteScanRefs(a.Expr, refs))
			c.target = append(c.target, idx)
		}
		steps[k] = c
	}

	// Output: input columns, declared columns, then the match id.
	outCols := append([]types.Column{}, evalCols[:width]...)
	if op.MatchIDColumn != "" {
		outCols = append(outCols, types.Column{Name: op.MatchIDColumn, Type: types.TypeLong})
	}
	output := types.NewTable(input.Name, types.Schema{Columns: outCols})

	type sequence struct {
		id   int64
		recs []types.Row // recs[j]: step j's extended record
	}
	state := make([]*sequence, len(op.Steps))
	pending := make([]types.Row, len(op.Steps)) // output=last
	var nextID int64

	emit := func(rec types.Row, id int64) {
		out := make(types.Row, len(outCols))
		copy(out, rec)
		if op.MatchIDColumn != "" {
			out[width] = id
		}
		output.AddRow(out)
	}
	flush := func(k int) {
		if pending[k] != nil {
			output.AddRow(pending[k])
			pending[k] = nil
		}
	}
	record := func(k int, rec types.Row, id int64) {
		switch op.Steps[k].Output {
		case "all":
			emit(rec, id)
		case "last":
			out := make(types.Row, len(outCols))
			copy(out, rec)
			if op.MatchIDColumn != "" {
				out[width] = id
			}
			pending[k] = out
		}
	}

	// evalRow builds the evaluation row for r against sequence seq (nil
	// for none), whose latest record supplies the declared values.
	evalRow := func(r types.Row, seq *sequence) types.Row {
		row := make(types.Row, len(evalCols))
		copy(row, r)
		copy(row[nIn:width], defaults)
		if seq != nil && len(seq.recs) > 0 {
			copy(row[nIn:width], seq.recs[len(seq.recs)-1][nIn:width])
		}
		for j := range op.Steps {
			base := width * (1 + j)
			if seq != nil && j < len(seq.recs) {
				copy(row[base:base+width], seq.recs[j])
			} else {
				copy(row[base+nIn:base+width], defaults)
			}
		}
		return row
	}
	// extend evaluates step k's assignments into r's extended record.
	extend := func(k int, r types.Row, row types.Row) (types.Row, error) {
		ext := make(types.Row, width)
		copy(ext, r)
		copy(ext[nIn:], row[nIn:width])
		for i, a := range steps[k].assigns {
			v, err := evalExpr(e, a, evalSchema, row)
			if err != nil {
				return nil, fmt.Errorf("scan step %s: assignment %s: %w", op.Steps[k].Name, op.Declares[steps[k].target[i]].Name, err)
			}
			ext[nIn+steps[k].target[i]] = coerceScanValue(v, declTypes[steps[k].target[i]])
		}
		return ext, nil
	}
	matches := func(k int, row types.Row) (bool, error) {
		v, err := evalExpr(e, steps[k].cond, evalSchema, row)
		if err != nil {
			return false, fmt.Errorf("scan step %s: condition: %w", op.Steps[k].Name, err)
		}
		b, _ := v.(bool)
		return b, nil
	}

	for _, r := range input.Rows {
		for k := len(op.Steps) - 1; k >= 0; k-- {
			// Check 1: promote step k-1's sequence.
			if k > 0 && state[k-1] != nil {
				prev := state[k-1]
				row := evalRow(r, prev)
				ok, err := matches(k, row)
				if err != nil {
					return nil, err
				}
				if ok {
					ext, err := extend(k, r, row)
					if err != nil {
						return nil, err
					}
					flush(k - 1)
					flush(k)
					recs := append(append([]types.Row{}, prev.recs...), ext)
					state[k] = &sequence{id: prev.id, recs: recs}
					state[k-1] = nil
					record(k, ext, prev.id)
					continue
				}
			}
			// Check 2: advance step k's own sequence.
			if k > 0 && state[k] == nil {
				continue
			}
			row := evalRow(r, state[k])
			ok, err := matches(k, row)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			ext, err := extend(k, r, row)
			if err != nil {
				return nil, err
			}
			if state[k] == nil { // first step, new sequence
				state[k] = &sequence{id: nextID, recs: []types.Row{ext}}
				nextID++
			} else {
				state[k].recs[k] = ext
			}
			record(k, ext, state[k].id)
		}
	}
	for k := range pending {
		flush(k)
	}
	return output, nil
}

// rewriteScanRefs replaces StepName.Column with the hidden column that
// holds it in the evaluation row.
func rewriteScanRefs(e parser.Expr, refs map[string]map[string]string) parser.Expr {
	switch x := e.(type) {
	case *parser.AccessExpr:
		if ref, ok := x.Object.(*parser.ColumnRef); ok && len(x.Path) > 0 && x.Path[0].Expr == nil && x.Path[0].Name != "" {
			if m, isStep := refs[ref.Name]; isStep {
				if hidden, ok := m[x.Path[0].Name]; ok {
					col := &parser.ColumnRef{Name: hidden}
					if len(x.Path) == 1 {
						return col
					}
					return &parser.AccessExpr{Object: col, Path: x.Path[1:]}
				}
			}
		}
		c := *x
		c.Object = rewriteScanRefs(x.Object, refs)
		c.Path = make([]parser.AccessKey, len(x.Path))
		for i, k := range x.Path {
			c.Path[i] = k
			if k.Expr != nil {
				c.Path[i].Expr = rewriteScanRefs(k.Expr, refs)
			}
		}
		return &c
	case *parser.FuncCall:
		c := *x
		c.Args = make([]parser.Expr, len(x.Args))
		for i, a := range x.Args {
			c.Args[i] = rewriteScanRefs(a, refs)
		}
		return &c
	case *parser.BinaryExpr:
		c := *x
		c.Left, c.Right = rewriteScanRefs(x.Left, refs), rewriteScanRefs(x.Right, refs)
		return &c
	case *parser.UnaryExpr:
		c := *x
		c.Expr = rewriteScanRefs(x.Expr, refs)
		return &c
	case *parser.BetweenExpr:
		c := *x
		c.Expr, c.Low, c.High = rewriteScanRefs(x.Expr, refs), rewriteScanRefs(x.Low, refs), rewriteScanRefs(x.High, refs)
		return &c
	case *parser.InExpr:
		c := *x
		c.Column = rewriteScanRefs(x.Column, refs)
		c.Values = make([]parser.Expr, len(x.Values))
		for i, v := range x.Values {
			c.Values[i] = rewriteScanRefs(v, refs)
		}
		return &c
	case *parser.HasAnyAllExpr:
		c := *x
		c.Column = rewriteScanRefs(x.Column, refs)
		c.Values = make([]parser.Expr, len(x.Values))
		for i, v := range x.Values {
			c.Values[i] = rewriteScanRefs(v, refs)
		}
		return &c
	}
	return e
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
	case types.TypeLong, types.TypeInt, types.TypeDatetime, types.TypeTimespan:
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
