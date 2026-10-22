package engine

// plugin_sequence_detect.go — evaluate sequence_detect:
//
//	T | evaluate sequence_detect(TimelineColumn, MaxSequenceStepWindow, MaxSequenceSpan,
//	        [Name1 =] Predicate1, [Name2 =] Predicate2, ..., Dim1, Dim2, ...)
//
// Within each combination of the dimension columns, every chain of
// rows r1, r2, ..., rN (ordered by the timeline) where row ri
// satisfies predicate i, each step follows the previous by at most
// MaxSequenceStepWindow, and the whole chain spans at most
// MaxSequenceSpan, is one output row: the dims, the timeline value of
// each step (column <Name>_<TimelineColumn>, unnamed predicates "_"
// <TimelineColumn> as Kusto.Language names them, made unique), and
// Duration (last minus first step). Every chain is reported, so one
// row can take part in several (the reference shows a heat event
// paired with two wildfires). Rows are ordered by the step times. A
// later step must be strictly later than the one before.
//
// Trailing plain column references are the dimensions, except bool
// columns, which are taken as predicates.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["sequence_detect"] = applySequenceDetect
}

func applySequenceDetect(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	const name = "sequence_detect"
	if len(args) < 5 {
		return nil, fmt.Errorf("%s(TimelineColumn, MaxSequenceStepWindow, MaxSequenceSpan, Expr1, Expr2, ..., Dim1, ...) needs at least two step predicates", name)
	}
	tCol, err := pluginColumn(in, name, "timeline", args[0])
	if err != nil {
		return nil, err
	}
	axis := in.Schema.Columns[tCol].Type
	var lim [2]int64
	for i, a := range args[1:3] {
		v, vt, err := pluginScalar(e, a)
		if err != nil || v == nil {
			return nil, fmt.Errorf("%s: argument %d: invalid value", name, i+2)
		}
		lim[i] = axisInt(v, vt, axis)
	}
	stepWin, span := lim[0], lim[1]

	rest := args[3:]
	nDims := 0
	for i := len(rest) - 1; i >= 0; i-- {
		a := strings.TrimSpace(rest[i])
		if !isPlainColumnArg(a) {
			break
		}
		c := in.Schema.ColumnIndex(a)
		if c < 0 || in.Schema.Columns[c].Type == types.TypeBool {
			break
		}
		nDims++
	}
	predArgs, dimArgs := rest[:len(rest)-nDims], rest[len(rest)-nDims:]
	if len(predArgs) < 1 {
		return nil, fmt.Errorf("%s: no step predicates", name)
	}
	dims, err := dimColumns(in, name, dimArgs)
	if err != nil {
		return nil, err
	}
	preds := make([]parser.Expr, len(predArgs))
	stepNames := make([]string, len(predArgs))
	for i, a := range predArgs {
		a = strings.TrimSpace(a)
		label := ""
		if eq := assignmentEq(a); eq > 0 {
			label = strings.TrimSpace(a[:eq])
			a = strings.TrimSpace(a[eq+1:])
		}
		ex, err := parser.ParseExpr(a)
		if err != nil {
			return nil, fmt.Errorf("%s: predicate %d: %w", name, i+1, err)
		}
		preds[i] = ex
		stepNames[i] = label + "_" + in.Schema.Columns[tCol].Name
	}

	schema := types.Schema{}
	used := map[string]bool{}
	add := func(c types.Column) {
		n := c.Name
		for i := 1; used[strings.ToLower(n)]; i++ {
			n = c.Name + strconv.Itoa(i)
		}
		used[strings.ToLower(n)] = true
		c.Name = n
		schema.Columns = append(schema.Columns, c)
	}
	for _, d := range dims {
		add(in.Schema.Columns[d])
	}
	for _, n := range stepNames {
		add(types.Column{Name: n, Type: axis})
	}
	durType := types.TypeTimespan
	if axis != types.TypeDatetime && axis != types.TypeTimespan {
		durType = axis
	}
	add(types.Column{Name: "Duration", Type: durType})

	// Rows per dimension group, ordered by time, with the steps each satisfies.
	type ev struct {
		t     int64
		tv    types.Value
		steps []bool
	}
	groups := map[string][]ev{}
	dimRows := map[string]types.Row{}
	var order []string
	for _, r := range in.Rows {
		if r[tCol] == nil {
			continue
		}
		x := ev{t: types.ToInt64(r[tCol]), tv: r[tCol], steps: make([]bool, len(preds))}
		anyStep := false
		for i, p := range preds {
			v, err := evalExpr(e, p, &in.Schema, r)
			if err != nil {
				return nil, fmt.Errorf("%s: predicate %d: %w", name, i+1, err)
			}
			if b, ok := v.(bool); ok && b {
				x.steps[i], anyStep = true, true
			}
		}
		if !anyStep {
			continue
		}
		k := dimKey(r, dims)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
			dr := make(types.Row, len(dims))
			for i, d := range dims {
				dr[i] = r[d]
			}
			dimRows[k] = dr
		}
		groups[k] = append(groups[k], x)
	}

	out := &types.Table{Schema: schema}
	type match struct {
		row   types.Row
		times []int64
	}
	var matches []match
	for _, k := range order {
		evs := groups[k]
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].t < evs[j].t })
		chain := make([]int, len(preds))
		var walk func(step, from int)
		walk = func(step, from int) {
			if step == len(preds) {
				row := append(types.Row(nil), dimRows[k]...)
				times := make([]int64, len(preds))
				for i, c := range chain {
					row = append(row, evs[c].tv)
					times[i] = evs[c].t
				}
				d := times[len(times)-1] - times[0]
				if axis == types.TypeDatetime {
					d /= 100 // nanoseconds to ticks
				}
				row = append(row, d)
				matches = append(matches, match{row, times})
				return
			}
			for j := from; j < len(evs); j++ {
				if !evs[j].steps[step] {
					continue
				}
				if step > 0 {
					prev := evs[chain[step-1]].t
					if evs[j].t <= prev {
						continue
					}
					if evs[j].t-prev > stepWin {
						break
					}
					if evs[j].t-evs[chain[0]].t > span {
						break
					}
				}
				chain[step] = j
				walk(step+1, j+1)
			}
		}
		walk(0, 0)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i].times, matches[j].times
		for s := range a {
			if a[s] != b[s] {
				return a[s] < b[s]
			}
		}
		return false
	})
	for _, m := range matches {
		out.Rows = append(out.Rows, m.row)
	}
	return out, nil
}

// isPlainColumnArg reports whether a is a bare or bracket-quoted name.
func isPlainColumnArg(a string) bool {
	ex, err := parser.ParseExpr(a)
	if err != nil {
		return false
	}
	_, ok := ex.(*parser.ColumnRef)
	return ok && assignmentEq(a) < 0
}

// assignmentEq is the index of a top-level "Name =" assignment's "=",
// or -1 (== and other operators are not assignments).
func assignmentEq(a string) int {
	depth := 0
	for i := 0; i < len(a); i++ {
		switch c := a[i]; c {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case '\'', '"':
			for i++; i < len(a) && a[i] != c; i++ {
				if a[i] == '\\' {
					i++
				}
			}
		case '=':
			if depth != 0 {
				continue
			}
			if i+1 < len(a) && (a[i+1] == '=' || a[i+1] == '~') {
				return -1
			}
			if i > 0 && strings.ContainsRune("!<>=", rune(a[i-1])) {
				return -1
			}
			if !isPlainColumnArg(strings.TrimSpace(a[:i])) {
				return -1
			}
			return i
		}
	}
	return -1
}
