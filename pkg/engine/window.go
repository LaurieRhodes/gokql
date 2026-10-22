package engine

// window.go — window functions over a serialized row set: row_number,
// prev, next, row_cumsum, row_rank_dense, row_rank_min,
// row_window_session.
//
// A window call can sit anywhere inside an expression and inside
// another window call's arguments (row_cumsum(x, name != prev(name))).
// Evaluation for row i rewrites the expression, replacing each window
// call with its value at i as a literal, then evaluates the result
// against row i as usual:
//   - prev/next read their argument at another row;
//   - the stateful functions are computed once over the whole row set,
//     in order, and cached per call node, their own arguments evaluated
//     through the same machinery.
//
// The engine keeps rows in order, so window functions are accepted
// after any operator; ADX requires the input to be serialized (sort,
// top, range, serialize, ...) and a query written for ADX already is.

import (
	"fmt"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

var windowFuncNames = map[string]bool{
	"row_number": true, "prev": true, "next": true, "row_cumsum": true,
	"row_rank_dense": true, "row_rank_min": true, "row_window_session": true,
}

// containsWindow reports whether expr calls a window function.
func containsWindow(expr parser.Expr) bool {
	found := false
	_, _ = rewriteExpr(expr, func(e parser.Expr) (parser.Expr, error) {
		if fc, ok := e.(*parser.FuncCall); ok && windowFuncNames[fc.Name] {
			found = true
		}
		return nil, nil
	})
	return found
}

// rewriteExpr rebuilds expr bottom-up; fn may return a replacement for
// a node (nil keeps it). Unchanged subtrees are shared, not copied.
func rewriteExpr(expr parser.Expr, fn func(parser.Expr) (parser.Expr, error)) (parser.Expr, error) {
	if expr == nil {
		return nil, nil
	}
	if r, err := fn(expr); err != nil || r != nil {
		return r, err
	}
	sub := func(e parser.Expr) (parser.Expr, error) { return rewriteExpr(e, fn) }
	subs := func(es []parser.Expr) ([]parser.Expr, bool, error) {
		out := make([]parser.Expr, len(es))
		changed := false
		for i, e := range es {
			r, err := sub(e)
			if err != nil {
				return nil, false, err
			}
			out[i] = r
			changed = changed || r != e
		}
		return out, changed, nil
	}
	switch ex := expr.(type) {
	case *parser.BinaryExpr:
		l, err := sub(ex.Left)
		if err != nil {
			return nil, err
		}
		r, err := sub(ex.Right)
		if err != nil {
			return nil, err
		}
		if l == ex.Left && r == ex.Right {
			return expr, nil
		}
		return &parser.BinaryExpr{Left: l, Op: ex.Op, Right: r}, nil
	case *parser.UnaryExpr:
		in, err := sub(ex.Expr)
		if err != nil || in == ex.Expr {
			return expr, err
		}
		return &parser.UnaryExpr{Op: ex.Op, Expr: in}, nil
	case *parser.FuncCall:
		args, changed, err := subs(ex.Args)
		if err != nil || !changed {
			return expr, err
		}
		return &parser.FuncCall{Name: ex.Name, RawName: ex.RawName, Args: args, ArgNames: ex.ArgNames}, nil
	case *parser.AccessExpr:
		obj, err := sub(ex.Object)
		if err != nil {
			return nil, err
		}
		path, changed := ex.Path, false
		for i, k := range ex.Path {
			if k.Expr == nil {
				continue
			}
			r, err := sub(k.Expr)
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
		col, err := sub(ex.Column)
		if err != nil {
			return nil, err
		}
		vals, changed, err := subs(ex.Values)
		if err != nil {
			return nil, err
		}
		if col == ex.Column && !changed {
			return expr, nil
		}
		c := *ex
		c.Column, c.Values = col, vals
		return &c, nil
	case *parser.HasAnyAllExpr:
		col, err := sub(ex.Column)
		if err != nil {
			return nil, err
		}
		vals, changed, err := subs(ex.Values)
		if err != nil {
			return nil, err
		}
		if col == ex.Column && !changed {
			return expr, nil
		}
		return &parser.HasAnyAllExpr{Column: col, Values: vals, All: ex.All}, nil
	case *parser.BetweenExpr:
		v, err := sub(ex.Expr)
		if err != nil {
			return nil, err
		}
		lo, err := sub(ex.Low)
		if err != nil {
			return nil, err
		}
		hi, err := sub(ex.High)
		if err != nil {
			return nil, err
		}
		if v == ex.Expr && lo == ex.Low && hi == ex.High {
			return expr, nil
		}
		return &parser.BetweenExpr{Expr: v, Low: lo, High: hi, Negated: ex.Negated}, nil
	}
	return expr, nil
}

// windowEval evaluates expressions containing window calls over rows.
type windowEval struct {
	schema *types.Schema
	rows   []types.Row
	cols   map[*parser.FuncCall][]types.Value
}

func newWindowEval(schema *types.Schema, rows []types.Row) *windowEval {
	return &windowEval{schema: schema, rows: rows, cols: map[*parser.FuncCall][]types.Value{}}
}

// valueAt evaluates expr at row i.
func (w *windowEval) valueAt(expr parser.Expr, i int) (types.Value, error) {
	rewritten, err := rewriteExpr(expr, func(e parser.Expr) (parser.Expr, error) {
		fc, ok := e.(*parser.FuncCall)
		if !ok || !windowFuncNames[fc.Name] {
			return nil, nil
		}
		v, err := w.windowAt(fc, i)
		if err != nil {
			return nil, err
		}
		return &parser.Literal{Value: v, Type: inferExprType(fc, w.schema)}, nil
	})
	if err != nil {
		return nil, err
	}
	return evalExpr(rewritten, w.schema, w.rows[i])
}

func (w *windowEval) windowAt(fc *parser.FuncCall, i int) (types.Value, error) {
	switch fc.Name {
	case "prev", "next":
		if len(fc.Args) < 1 || len(fc.Args) > 3 {
			return nil, fmt.Errorf("%s(expr [, offset [, default]]) takes 1 to 3 arguments", fc.Name)
		}
		offset := int64(1)
		if len(fc.Args) >= 2 {
			v, err := w.valueAt(fc.Args[1], i)
			if err != nil {
				return nil, err
			}
			n, ok := toInt64(v)
			if !ok {
				return nil, fmt.Errorf("%s: offset must be an integer", fc.Name)
			}
			offset = n
		}
		j := i - int(offset)
		if fc.Name == "next" {
			j = i + int(offset)
		}
		if j < 0 || j >= len(w.rows) {
			if len(fc.Args) == 3 {
				return w.valueAt(fc.Args[2], i)
			}
			return nil, nil
		}
		return w.valueAt(fc.Args[0], j)
	}
	col, ok := w.cols[fc]
	if !ok {
		var err error
		if col, err = w.computeColumn(fc); err != nil {
			return nil, err
		}
		w.cols[fc] = col
	}
	return col[i], nil
}

// computeColumn evaluates a stateful window function over every row.
func (w *windowEval) computeColumn(fc *parser.FuncCall) ([]types.Value, error) {
	n := len(w.rows)
	out := make([]types.Value, n)
	arg := func(k, i int) (types.Value, error) { return w.valueAt(fc.Args[k], i) }
	restartAt := func(k, i int) (bool, error) {
		if len(fc.Args) <= k {
			return false, nil
		}
		v, err := arg(k, i)
		b, _ := v.(bool)
		return b, err
	}
	argc := func(lo, hi int) error {
		if len(fc.Args) < lo || len(fc.Args) > hi {
			return fmt.Errorf("%s takes %d to %d arguments, got %d", fc.Name, lo, hi, len(fc.Args))
		}
		return nil
	}

	switch fc.Name {
	case "row_number":
		// row_number([StartingIndex [, Restart]])
		if err := argc(0, 2); err != nil {
			return nil, err
		}
		start := int64(1)
		if len(fc.Args) >= 1 && n > 0 {
			v, err := arg(0, 0)
			if err != nil {
				return nil, err
			}
			s, ok := toInt64(v)
			if !ok {
				return nil, fmt.Errorf("row_number: StartingIndex must be an integer")
			}
			start = s
		}
		cur := start
		for i := 0; i < n; i++ {
			r, err := restartAt(1, i)
			if err != nil {
				return nil, err
			}
			if i == 0 || r {
				cur = start
			} else {
				cur++
			}
			out[i] = cur
		}

	case "row_cumsum":
		// row_cumsum(term [, restart]): restart sets the sum back to 0
		// before the current row is added.
		if err := argc(1, 2); err != nil {
			return nil, err
		}
		integral := sumIsIntegral(fc.Args[0], w.schema)
		var isum int64
		var fsum float64
		for i := 0; i < n; i++ {
			r, err := restartAt(1, i)
			if err != nil {
				return nil, err
			}
			if r {
				isum, fsum = 0, 0
			}
			v, err := arg(0, i)
			if err != nil {
				return nil, err
			}
			if v != nil {
				if integral {
					x, _ := toInt64(v)
					isum += x
				} else {
					fsum += types.ToFloat64(v)
				}
			}
			if integral {
				out[i] = isum
			} else {
				out[i] = fsum
			}
		}

	case "row_rank_dense", "row_rank_min":
		// dense: +1 whenever Term differs from the previous row's.
		// min: the position (within the partition) at which the
		// current run of equal Term values began.
		if err := argc(1, 2); err != nil {
			return nil, err
		}
		termType := inferExprType(fc.Args[0], w.schema)
		var prev types.Value
		var rank, pos int64
		for i := 0; i < n; i++ {
			v, err := arg(0, i)
			if err != nil {
				return nil, err
			}
			r, err := restartAt(1, i)
			if err != nil {
				return nil, err
			}
			switch {
			case i == 0 || r:
				rank, pos = 1, 1
			default:
				pos++
				if !sameValue(v, prev, termType) {
					if fc.Name == "row_rank_dense" {
						rank++
					} else {
						rank = pos
					}
				}
			}
			out[i] = rank
			prev = v
		}

	case "row_window_session":
		// row_window_session(Expr, MaxDistanceFromFirst,
		// MaxDistanceBetweenNeighbors [, Restart]): the session start
		// value for each row. A new session starts when there is no
		// open session, when Expr reaches start + MaxDistanceFromFirst
		// or previous + MaxDistanceBetweenNeighbors, or on Restart. A
		// null Expr closes the session.
		if err := argc(3, 4); err != nil {
			return nil, err
		}
		var start, prevV *int64
		for i := 0; i < n; i++ {
			v, err := arg(0, i)
			if err != nil {
				return nil, err
			}
			d1v, err := arg(1, i)
			if err != nil {
				return nil, err
			}
			d2v, err := arg(2, i)
			if err != nil {
				return nil, err
			}
			r, err := restartAt(3, i)
			if err != nil {
				return nil, err
			}
			if v == nil {
				start, prevV = nil, nil
				out[i] = nil
				continue
			}
			x := types.ToInt64(v)
			// datetime values are nanoseconds; timespans are 100 ns ticks.
			d1, d2 := types.ToInt64(d1v)*100, types.ToInt64(d2v)*100
			if start == nil || r || x >= *start+d1 || (prevV != nil && x >= *prevV+d2) {
				s := x
				start = &s
			}
			p := x
			prevV = &p
			out[i] = *start
		}
	default:
		return nil, fmt.Errorf("unknown window function %s", fc.Name)
	}
	return out, nil
}

// sameValue compares two cell values for rank changes.
func sameValue(a, b types.Value, t types.KQLType) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return types.CompareValues(a, b, t) == 0
}

// rowEvaluator evaluates per-row expressions for an operator, switching
// to window evaluation only for expressions that need it.
type rowEvaluator struct {
	schema *types.Schema
	w      *windowEval
	has    map[parser.Expr]bool
}

func newRowEvaluator(schema *types.Schema, rows []types.Row, exprs ...parser.Expr) *rowEvaluator {
	re := &rowEvaluator{schema: schema, has: map[parser.Expr]bool{}}
	for _, e := range exprs {
		if e != nil && containsWindow(e) {
			re.has[e] = true
			if re.w == nil {
				re.w = newWindowEval(schema, rows)
			}
		}
	}
	return re
}

func (re *rowEvaluator) eval(expr parser.Expr, i int, row types.Row) (types.Value, error) {
	if re.has[expr] {
		return re.w.valueAt(expr, i)
	}
	return evalExpr(expr, re.schema, row)
}
