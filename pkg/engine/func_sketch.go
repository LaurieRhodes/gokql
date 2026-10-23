package engine

// func_sketch.go — the hll and tdigest sketch families, implemented
// exactly: this engine's dcount and percentiles are already exact, so
// the sketches carry the full distinct set (hll) or the full weighted
// sample (tdigest) and every estimate built on them is exact too. The
// values are opaque dynamic values, as in ADX, but their content is
// this engine's own and not interchangeable with ADX's binary
// sketches.
//
//	hll(x [, accuracy]), hll_if(x, pred [, accuracy]), hll_merge(h) — aggregates
//	dcount_hll(h), hll_merge(h1, h2, ...) — scalars
//	tdigest(x [, weight]), tdigest_merge(t) / merge_tdigest(t) — aggregates
//	percentile_tdigest(t, p [, typeof(T)]), percentiles_array_tdigest(t, p1, ...),
//	percentrank_tdigest(t, v), rank_tdigest(t, v), tdigest_merge(t1, ...) — scalars
//	evaluate dcount_intersect(h1, h2, ...) — s0 = |S1|, s1 = |S1 ∩ S2|, ...
//
// rank_tdigest(t, v) counts the sample (by weight) less than or equal
// to v; percentrank_tdigest is that as a percentage of the total;
// percentile_tdigest is nearest-rank, like percentile().

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["dcount_intersect"] = applyDcountIntersect
}

type hllSketch struct {
	Keys []string `json:"hll_exact"`
}

// tdigestSketch holds the weighted sample as JSON numbers, so integer
// types (datetime nanoseconds, timespan ticks, long) stay exact.
type tdigestSketch struct {
	Type    string           `json:"tdigest_type"`
	Samples [][2]json.Number `json:"tdigest_exact"` // value, weight
}

func (t *tdigestSketch) isInt() bool {
	switch t.kqlType() {
	case types.TypeLong, types.TypeInt, types.TypeDatetime, types.TypeTimespan:
		return true
	}
	return false
}

func numF(n json.Number) float64 { f, _ := n.Float64(); return f }

func (t *tdigestSketch) less(i, j int) bool {
	if t.isInt() {
		a, _ := t.Samples[i][0].Int64()
		b, _ := t.Samples[j][0].Int64()
		return a < b
	}
	return numF(t.Samples[i][0]) < numF(t.Samples[j][0])
}

// leq reports whether sample i's value is <= v.
func (t *tdigestSketch) leq(i int, v types.Value) bool {
	if t.isInt() {
		if _, isF := v.(float64); !isF {
			a, _ := t.Samples[i][0].Int64()
			return a <= types.ToInt64(v)
		}
	}
	return numF(t.Samples[i][0]) <= types.ToFloat64(v)
}

func sampleNumber(v types.Value, isInt bool) json.Number {
	if isInt {
		return json.Number(strconv.FormatInt(types.ToInt64(v), 10))
	}
	return json.Number(strconv.FormatFloat(types.ToFloat64(v), 'g', -1, 64))
}

func encodeSketch(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func decodeHLL(v types.Value) (map[string]bool, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("not an hll value")
	}
	var h hllSketch
	if err := json.Unmarshal([]byte(s), &h); err != nil || h.Keys == nil && !strings.Contains(s, "hll_exact") {
		return nil, fmt.Errorf("not an hll value")
	}
	set := make(map[string]bool, len(h.Keys))
	for _, k := range h.Keys {
		set[k] = true
	}
	return set, nil
}

func encodeHLL(set map[string]bool) string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return encodeSketch(hllSketch{Keys: keys})
}

func decodeTDigest(v types.Value) (*tdigestSketch, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("not a tdigest value")
	}
	var t tdigestSketch
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&t); err != nil || t.Type == "" {
		return nil, fmt.Errorf("not a tdigest value")
	}
	return &t, nil
}

func (t *tdigestSketch) normalize() {
	sort.SliceStable(t.Samples, t.less)
}

func (t *tdigestSketch) kqlType() types.KQLType {
	k, err := types.ParseType(t.Type)
	if err != nil {
		return types.TypeReal
	}
	return k
}

// typed converts a stored sample value back to its KQL type.
func (t *tdigestSketch) typed(n json.Number) types.Value {
	if t.isInt() {
		i, _ := n.Int64()
		return i
	}
	return numF(n)
}

func (t *tdigestSketch) total() float64 {
	var n float64
	for _, s := range t.Samples {
		n += numF(s[1])
	}
	return n
}

func (t *tdigestSketch) percentile(p float64) types.Value {
	if len(t.Samples) == 0 {
		return nil
	}
	rank := math.Ceil(p / 100 * t.total())
	if rank < 1 {
		rank = 1
	}
	var cum float64
	for _, s := range t.Samples {
		cum += numF(s[1])
		if cum >= rank {
			return t.typed(s[0])
		}
	}
	return t.typed(t.Samples[len(t.Samples)-1][0])
}

func (t *tdigestSketch) rank(v types.Value) float64 {
	var n float64
	for i, s := range t.Samples {
		if t.leq(i, v) {
			n += numF(s[1])
		}
	}
	return n
}

// dynamicScalar renders a typed value as a dynamic (JSON) value.
func dynamicScalar(v types.Value, t types.KQLType) string {
	switch t {
	case types.TypeDatetime:
		return encodeSketch(formatDatetimeISO7(types.ToInt64(v)))
	case types.TypeTimespan:
		return encodeSketch(formatTimespanTostring(types.ToInt64(v)))
	}
	return encodeSketch(v)
}

func mergeTDigests(vals []types.Value) (types.Value, error) {
	out := &tdigestSketch{}
	for _, v := range vals {
		if v == nil {
			continue
		}
		t, err := decodeTDigest(v)
		if err != nil {
			return nil, err
		}
		if out.Type == "" {
			out.Type = t.Type
		}
		out.Samples = append(out.Samples, t.Samples...)
	}
	if out.Type == "" {
		return nil, nil
	}
	out.normalize()
	return encodeSketch(out), nil
}

func mergeHLLs(vals []types.Value) (types.Value, error) {
	set := map[string]bool{}
	any := false
	for _, v := range vals {
		if v == nil {
			continue
		}
		s, err := decodeHLL(v)
		if err != nil {
			return nil, err
		}
		any = true
		for k := range s {
			set[k] = true
		}
	}
	if !any {
		return nil, nil
	}
	return encodeHLL(set), nil
}

// computeSketchAgg implements the sketch aggregates; ok is false for
// any other function.
func computeSketchAgg(eng *Engine, agg parser.Aggregation, rows []types.Row, schema *types.Schema) (types.Value, bool, error) {
	switch agg.Function {
	case "buildschema":
		v, err := computeBuildSchema(eng, agg, rows, schema)
		return v, true, err
	case "hll", "hll_if":
		min, max := 1, 2
		if agg.Function == "hll_if" {
			min, max = 2, 3
		}
		if len(agg.Args) < min || len(agg.Args) > max {
			return nil, true, fmt.Errorf("%s: expected %d-%d arguments", agg.Function, min, max)
		}
		set := map[string]bool{}
		for _, r := range rows {
			if agg.Function == "hll_if" {
				p, err := evalExpr(eng, agg.Args[1], schema, r)
				if err != nil {
					return nil, true, err
				}
				if b, _ := p.(bool); !b {
					continue
				}
			}
			v, err := evalExpr(eng, agg.Args[0], schema, r)
			if err != nil {
				return nil, true, err
			}
			if v != nil {
				set[fmt.Sprintf("%v", v)] = true // dcount's distinctness
			}
		}
		return encodeHLL(set), true, nil
	case "hll_merge":
		if len(agg.Args) != 1 {
			return nil, true, fmt.Errorf("hll_merge: expected 1 argument")
		}
		vals, err := aggArgValues(eng, agg.Args[0], rows, schema)
		if err != nil {
			return nil, true, err
		}
		v, err := mergeHLLs(vals)
		return v, true, err
	case "tdigest":
		if len(agg.Args) < 1 || len(agg.Args) > 2 {
			return nil, true, fmt.Errorf("tdigest: expected 1-2 arguments")
		}
		t := inferExprType(eng, agg.Args[0], schema)
		if t == types.TypeInt {
			t = types.TypeLong
		}
		sk := &tdigestSketch{Type: t.String(), Samples: [][2]json.Number{}}
		isInt := sk.isInt()
		for _, r := range rows {
			v, err := evalExpr(eng, agg.Args[0], schema, r)
			if err != nil {
				return nil, true, err
			}
			if v == nil {
				continue
			}
			w := 1.0
			if len(agg.Args) == 2 {
				wv, err := evalExpr(eng, agg.Args[1], schema, r)
				if err != nil {
					return nil, true, err
				}
				if wv == nil {
					continue
				}
				if w = math.Floor(types.ToFloat64(wv)); w <= 0 {
					continue
				}
			}
			sk.Samples = append(sk.Samples, [2]json.Number{sampleNumber(v, isInt), sampleNumber(w, false)})
		}
		sk.normalize()
		return encodeSketch(sk), true, nil
	case "tdigest_merge", "merge_tdigest":
		if len(agg.Args) != 1 {
			return nil, true, fmt.Errorf("%s: expected 1 argument", agg.Function)
		}
		vals, err := aggArgValues(eng, agg.Args[0], rows, schema)
		if err != nil {
			return nil, true, err
		}
		v, err := mergeTDigests(vals)
		return v, true, err
	}
	return nil, false, nil
}

func aggArgValues(eng *Engine, e parser.Expr, rows []types.Row, schema *types.Schema) ([]types.Value, error) {
	out := make([]types.Value, 0, len(rows))
	for _, r := range rows {
		v, err := evalExpr(eng, e, schema, r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func evalSketchFunc(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "dcount_hll", "hll_merge", "percentile_tdigest", "percentiles_array_tdigest",
		"percentrank_tdigest", "rank_tdigest", "tdigest_merge", "merge_tdigest":
	default:
		return nil, false, nil
	}
	args := make([]types.Value, len(fc.Args))
	for i, a := range fc.Args {
		if fc.Name == "percentile_tdigest" && i == 2 {
			continue // typeof(T)
		}
		v, err := evalExpr(eng, a, schema, row)
		if err != nil {
			return nil, true, err
		}
		args[i] = v
	}
	need := func(min int) error {
		if len(args) < min {
			return fmt.Errorf("%s: expected at least %d arguments", fc.Name, min)
		}
		return nil
	}
	switch fc.Name {
	case "dcount_hll":
		if err := need(1); err != nil || args[0] == nil {
			return nil, true, err
		}
		set, err := decodeHLL(args[0])
		if err != nil {
			return nil, true, fmt.Errorf("dcount_hll: %w", err)
		}
		return int64(len(set)), true, nil
	case "hll_merge":
		v, err := mergeHLLs(args)
		return v, true, err
	case "tdigest_merge", "merge_tdigest":
		v, err := mergeTDigests(args)
		return v, true, err
	}
	if err := need(2); err != nil {
		return nil, true, err
	}
	if args[0] == nil {
		return nil, true, nil
	}
	t, err := decodeTDigest(args[0])
	if err != nil {
		return nil, true, fmt.Errorf("%s: %w", fc.Name, err)
	}
	switch fc.Name {
	case "percentile_tdigest":
		if args[1] == nil {
			return nil, true, nil
		}
		v := t.percentile(types.ToFloat64(args[1]))
		if v == nil {
			return nil, true, nil
		}
		if len(fc.Args) == 3 {
			return coerceToDeclared(v, typeofArg(fc.Args[2])), true, nil
		}
		return dynamicScalar(v, t.kqlType()), true, nil
	case "percentiles_array_tdigest":
		// (digest, p1 [, p2 ...] | dynamic array [, typeof(T)]): a
		// trailing typeof(T) is the result element type, not a
		// percentile.
		outType := t.kqlType()
		if n := len(fc.Args); n >= 3 {
			if isTypeofArg(fc.Args[n-1]) {
				outType = typeofArg(fc.Args[n-1])
				args = args[:n-1]
			}
		}
		var ps []float64
		if len(args) == 2 {
			if arr, ok := parseJSONArray(args[1]); ok {
				for _, x := range arr {
					ps = append(ps, types.ToFloat64(jsonToKQLValue(x)))
				}
			}
		}
		if ps == nil {
			for _, a := range args[1:] {
				ps = append(ps, types.ToFloat64(a))
			}
		}
		parts := make([]string, len(ps))
		for i, p := range ps {
			v := t.percentile(p)
			if v == nil {
				parts[i] = "null"
			} else {
				parts[i] = dynamicScalar(v, outType)
			}
		}
		return "[" + strings.Join(parts, ",") + "]", true, nil
	case "rank_tdigest", "percentrank_tdigest":
		if args[1] == nil {
			return nil, true, nil
		}
		r := t.rank(args[1])
		if fc.Name == "rank_tdigest" {
			return r, true, nil
		}
		n := t.total()
		if n == 0 {
			return nil, true, nil
		}
		return r / n * 100, true, nil
	}
	return nil, false, nil
}

// isTypeofArg reports whether e is a typeof(T) argument.
func isTypeofArg(e parser.Expr) bool {
	if fc, ok := e.(*parser.FuncCall); ok && fc.Name == "typeof" {
		return true
	}
	if lit, ok := e.(*parser.Literal); ok && lit.Type == types.TypeString {
		if n, ok := lit.Value.(string); ok {
			_, err := types.ParseType(n)
			return err == nil
		}
	}
	return false
}

// typeofArg reads typeof(T) as a type; real when it is not one.
func typeofArg(e parser.Expr) types.KQLType {
	if lit, ok := e.(*parser.Literal); ok { // typeof(T) parses as the type name
		if n, ok := lit.Value.(string); ok {
			if t, err := types.ParseType(n); err == nil {
				return t
			}
		}
	}
	if fc, ok := e.(*parser.FuncCall); ok && fc.Name == "typeof" && len(fc.Args) == 1 {
		if ref, ok := fc.Args[0].(*parser.ColumnRef); ok {
			if t, err := types.ParseType(ref.Name); err == nil {
				return t
			}
		}
	}
	return types.TypeReal
}

func applyDcountIntersect(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
	if len(args) < 2 || len(args) > 16 {
		return nil, fmt.Errorf("dcount_intersect(hll_1, hll_2 [, ...]) takes 2 to 16 hll arguments")
	}
	exprs := make([]parser.Expr, len(args))
	for i, a := range args {
		ex, err := parser.ParseExpr(strings.TrimSpace(a))
		if err != nil {
			return nil, fmt.Errorf("dcount_intersect: argument %d: %w", i+1, err)
		}
		exprs[i] = ex
	}
	schema := types.Schema{Columns: append([]types.Column(nil), in.Schema.Columns...)}
	for i := range args {
		schema.Columns = append(schema.Columns, types.Column{Name: fmt.Sprintf("s%d", i), Type: types.TypeLong})
	}
	out := &types.Table{Schema: schema}
	for _, r := range in.Rows {
		row := append(types.Row(nil), r...)
		var inter map[string]bool
		for i, ex := range exprs {
			v, err := evalExpr(e, ex, &in.Schema, r)
			if err != nil {
				return nil, err
			}
			set := map[string]bool{}
			if v != nil {
				if set, err = decodeHLL(v); err != nil {
					return nil, fmt.Errorf("dcount_intersect: argument %d: %w", i+1, err)
				}
			}
			if i == 0 {
				inter = set
			} else {
				next := map[string]bool{}
				for k := range inter {
					if set[k] {
						next[k] = true
					}
				}
				inter = next
			}
			row = append(row, int64(len(inter)))
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}
