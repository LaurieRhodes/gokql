package engine

// reduce.go — | reduce [kind=source] by Expr [with [threshold=T]
// [, characters=C]].
//
// ADX's reduce does not document its algorithm (the reference cites
// SLCT; probing the Kusto emulator shows hashed-term grouping that is
// not practical to reproduce). This is a Drain-style log-template
// clusterer (He et al., ICWS 2017; the Drain pass of ADX's own
// log_reduce_fl), keeping ADX's output shape and conventions:
//
//   - each value is split into terms at separator characters: by
//     default every character that is not a letter or digit; with
//     `characters` only those characters;
//   - values are grouped by term count and first term (a first term
//     holding a digit groups as a wildcard); within a group a value
//     joins the template it shares the most terms with when at least
//     half its terms equal the template's fixed terms, and differing
//     terms become *; otherwise it starts a template;
//   - a template with at least two values whose count exceeds
//     Threshold x rows (default 0.1) is a pattern row: Pattern, Count, Representative (its first
//     value). The rest are summed into one "others" row with an empty
//     Representative, as ADX reports them;
//   - a pattern keeps the representative's default separators; custom
//     `characters` before a * are dropped ("MachineLearningX7" with
//     characters="X" reduces to MachineLearning*, as in ADX);
//   - kind=source keeps the input rows and appends a Pattern column.
//
// Rows are ordered by Count descending, others last.

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

const reduceSimilarity = 0.5

type reduceValue struct {
	terms []string
	seps  []string // seps[i] precedes terms[i]; seps[len(terms)] trails
}

func reduceSplit(s string, isSep func(r rune) bool) reduceValue {
	var v reduceValue
	var term, sep strings.Builder
	for _, r := range s {
		if isSep(r) {
			if term.Len() > 0 {
				v.terms = append(v.terms, term.String())
				term.Reset()
			}
			sep.WriteRune(r)
			continue
		}
		if term.Len() == 0 {
			v.seps = append(v.seps, sep.String())
			sep.Reset()
		}
		term.WriteRune(r)
	}
	if term.Len() > 0 {
		v.terms = append(v.terms, term.String())
	}
	v.seps = append(v.seps, sep.String())
	return v
}

type reduceCluster struct {
	template []string // "" marks a wildcard (terms are never empty)
	count    int64
	rep      reduceValue
	repText  string
	rows     []int
}

func (e *Engine) applyReduce(input *types.Table, op *parser.ReduceOp) (*types.Table, error) {
	isSep := func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }
	if op.HasChars {
		set := op.Characters
		isSep = func(r rune) bool { return strings.ContainsRune(set, r) }
	}
	groups := map[string][]*reduceCluster{}
	var clusters []*reduceCluster
	rowCluster := make([]*reduceCluster, len(input.Rows))
	for ri, row := range input.Rows {
		v, err := evalExpr(e, op.By, &input.Schema, row)
		if err != nil {
			return nil, fmt.Errorf("reduce: %w", err)
		}
		text := ""
		if v != nil {
			text = fmt.Sprint(v)
		}
		rv := reduceSplit(text, isSep)
		first := ""
		if len(rv.terms) > 0 {
			first = rv.terms[0]
			if strings.IndexFunc(first, unicode.IsDigit) >= 0 {
				first = "\x00*"
			}
		}
		key := fmt.Sprintf("%d\x00%s", len(rv.terms), first)
		var best *reduceCluster
		bestSim := -1.0
		for _, c := range groups[key] {
			same := 0
			for i, t := range rv.terms {
				if c.template[i] != "" && c.template[i] == t {
					same++
				}
			}
			sim := 1.0
			if len(rv.terms) > 0 {
				sim = float64(same) / float64(len(rv.terms))
			}
			if sim > bestSim {
				best, bestSim = c, sim
			}
		}
		if best == nil || bestSim < reduceSimilarity {
			best = &reduceCluster{template: append([]string(nil), rv.terms...), rep: rv, repText: text}
			groups[key] = append(groups[key], best)
			clusters = append(clusters, best)
		} else {
			for i, t := range rv.terms {
				if best.template[i] != t {
					best.template[i] = ""
				}
			}
		}
		best.count++
		best.rows = append(best.rows, ri)
		rowCluster[ri] = best
	}
	n := float64(len(input.Rows))
	pattern := func(c *reduceCluster) string {
		var b strings.Builder
		for i, t := range c.template {
			sep := c.rep.seps[i]
			if t == "" {
				if op.HasChars {
					sep = ""
				}
				b.WriteString(sep)
				b.WriteString("*")
				continue
			}
			b.WriteString(sep)
			b.WriteString(t)
		}
		b.WriteString(c.rep.seps[len(c.template)])
		return b.String()
	}
	qualifies := func(c *reduceCluster) bool { return c.count >= 2 && float64(c.count) > op.Threshold*n }

	if op.Source {
		schema := types.Schema{Columns: append(append([]types.Column(nil), input.Schema.Columns...),
			types.Column{Name: "Pattern", Type: types.TypeString})}
		out := types.NewTable(input.Name, schema)
		for ri, row := range input.Rows {
			p := "others"
			if c := rowCluster[ri]; qualifies(c) {
				p = pattern(c)
			}
			out.AddRow(append(append(types.Row(nil), row...), p))
		}
		return out, nil
	}
	schema := types.Schema{Columns: []types.Column{{Name: "Pattern", Type: types.TypeString},
		{Name: "Count", Type: types.TypeLong}, {Name: "Representative", Type: types.TypeString}}}
	out := types.NewTable(input.Name, schema)
	var kept []*reduceCluster
	var others int64
	for _, c := range clusters {
		if qualifies(c) {
			kept = append(kept, c)
		} else {
			others += c.count
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].count > kept[j].count })
	for _, c := range kept {
		out.AddRow(types.Row{pattern(c), c.count, c.repText})
	}
	if others > 0 {
		out.AddRow(types.Row{"others", others, ""})
	}
	return out, nil
}
