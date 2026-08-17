package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// applyMakeSeries implements | make-series — see MakeSeriesOp's own doc
// comment (pkg/parser/ast.go) for exactly which parts of real ADX's
// documented grammar this covers. Verified against real ADX's own
// worked example (make-series-operator.md) with exact expected values
// before writing this, including the bin_at (not bin) grouping rule and
// the half-open [start, end) bin range.
func (e *Engine) applyMakeSeries(input *types.Table, op *parser.MakeSeriesOp) (*types.Table, error) {
	emptySchema := types.Schema{}
	emptyRow := types.Row{}

	axisCol := input.Schema.ColumnByName(op.AxisColumn)
	if axisCol == nil {
		return nil, fmt.Errorf("make-series: column %q not found", op.AxisColumn)
	}
	axisType := axisCol.Type
	if !axisType.IsNumeric() {
		return nil, fmt.Errorf("make-series: AxisColumn %q must be numeric (int, long, real, datetime, or timespan), got %s", op.AxisColumn, axisType)
	}
	axisIdx := input.Schema.ColumnIndex(op.AxisColumn)

	fromVal, err := evalExpr(op.From, &emptySchema, emptyRow)
	if err != nil {
		return nil, fmt.Errorf("make-series: from: %w", err)
	}
	toVal, err := evalExpr(op.To, &emptySchema, emptyRow)
	if err != nil {
		return nil, fmt.Errorf("make-series: to: %w", err)
	}
	stepVal, err := evalExpr(op.Step, &emptySchema, emptyRow)
	if err != nil {
		return nil, fmt.Errorf("make-series: step: %w", err)
	}

	// Validate aggregation result types up front — "Only aggregation
	// functions that return numeric results can be used with the
	// make-series operator" per real ADX docs.
	aggOutTypes := make([]types.KQLType, len(op.Aggregations))
	for i, msAgg := range op.Aggregations {
		t := inferAggType(msAgg.Agg.Function, msAgg.Agg.Args, &input.Schema)
		if !t.IsNumeric() {
			return nil, fmt.Errorf("make-series: aggregation %q does not return a numeric result (got %s) — only numeric aggregations are supported", msAgg.Agg.Function, t)
		}
		aggOutTypes[i] = t
	}

	// Resolve each aggregation's default value, coerced to that
	// aggregation's own output type — real ADX's own documented
	// default is 0 (not null); double(null) is explicitly the
	// documented way to opt into a real null gap for the
	// series_fill_* interpolation functions instead.
	defaults := make([]types.Value, len(op.Aggregations))
	for i, msAgg := range op.Aggregations {
		if msAgg.Default == nil {
			defaults[i] = coerceNumeric(int64(0), aggOutTypes[i])
			continue
		}
		dv, err := evalExpr(msAgg.Default, &emptySchema, emptyRow)
		if err != nil {
			return nil, fmt.Errorf("make-series: default for %q: %w", msAgg.Agg.Function, err)
		}
		if dv == nil {
			defaults[i] = nil
		} else {
			defaults[i] = coerceNumeric(dv, aggOutTypes[i])
		}
	}

	// Build the bin axis (the array of AxisColumn values) — the float
	// path (real-typed axis) and the int64 path (int/long/datetime/
	// timespan, all int64-backed internally) are kept separate rather
	// than forcing one through the other's arithmetic, same split
	// applyRange already uses for the same underlying reason.
	const maxSeriesLen = 1048576 // 2^20, real ADX's own documented cap

	var axisValsInt []int64
	var axisValsFloat []float64
	var startInt, stepInt, endInt int64
	var startFloat, stepFloat, endFloat float64
	useFloat := axisType == types.TypeReal

	if useFloat {
		startFloat = types.ToFloat64(fromVal)
		endFloat = types.ToFloat64(toVal)
		stepFloat = types.ToFloat64(stepVal)
		if stepFloat <= 0 {
			return nil, fmt.Errorf("make-series: step must be a positive value")
		}
		n := int(math.Ceil((endFloat - startFloat) / stepFloat))
		if n < 0 {
			n = 0
		}
		if n > maxSeriesLen {
			return nil, fmt.Errorf("make-series: generated series would have %d elements, exceeding the %d (2^20) limit", n, maxSeriesLen)
		}
		for v := startFloat; v < endFloat; v += stepFloat {
			axisValsFloat = append(axisValsFloat, v)
		}
	} else {
		startInt = types.ToInt64(fromVal)
		endInt = types.ToInt64(toVal)
		rawStep := types.ToInt64(stepVal)
		// Scale step by the AXIS column's own type, not by re-inferring
		// op.Step's own expression type (inferExprType has no way to
		// recover a let-bound scalar's original declared type -- it
		// only checks a schema, and op.Step here is frequently a bare
		// identifier like `interval` referencing `let interval = 1d`,
		// which inferExprType silently defaulted to TypeString for,
		// silently skipping the tick->nanosecond conversion below and
		// producing a step 100x too small. Found live: the real ADX
		// avg(metric) worked example produced 14m24s-spaced bins
        // instead of 1-day-spaced ones -- exactly a missing *100.
		// Keying off axisType instead is also more semantically
		// correct, not just a workaround: real ADX's own grammar
		// requires step's DIMENSION to match the axis (a timespan step
		// for a datetime axis; see the docs' own step parameter
		// description), so the axis column's type is the authoritative
		// source for what unit-conversion step needs, independent of
		// how step's own value happened to be spelled in the query.
		stepInt = rawStep
		if axisType == types.TypeDatetime {
			// datetime axis (nanoseconds) but step is a timespan
			// (100ns ticks, same representation as a `timespan`-typed
			// value/let-bound scalar always uses) -- bridge the units.
			stepInt = rawStep * 100
		}
		// axisType == TypeTimespan: start/end/step are all already
		// ticks (same representation) -- no scaling needed.
		// axisType == TypeInt/TypeLong: all plain integers -- no
		// scaling needed.
		if stepInt <= 0 {
			return nil, fmt.Errorf("make-series: step must be a positive value")
		}
		if endInt > startInt {
			n64 := (endInt - startInt + stepInt - 1) / stepInt
			if n64 > maxSeriesLen {
				return nil, fmt.Errorf("make-series: generated series would have %d elements, exceeding the %d (2^20) limit", n64, maxSeriesLen)
			}
		}
		for v := startInt; v < endInt; v += stepInt {
			axisValsInt = append(axisValsInt, v)
		}
	}
	numBins := len(axisValsInt)
	if useFloat {
		numBins = len(axisValsFloat)
	}

	// Group rows: by-key -> bin index -> rows in that bin. Mirrors
	// applySummarize's own by-key grouping exactly (same key-join
	// convention) so the two operators' grouping semantics can't
	// silently drift, with the bin_at(AxisColumn, step, start) term
	// folded into the same key, per real ADX's own documented grouping
	// rule ("input rows are arranged into groups having the same
	// values of the by expressions AND the bin_at(...) expression").
	type groupEntry struct {
		byVals  []types.Value
		buckets [][]types.Row // len == numBins
	}
	groups := make(map[string]*groupEntry)
	var groupOrder []string

	for _, row := range input.Rows {
		axisRaw := row[axisIdx]
		if axisRaw == nil {
			continue // a null axis value can't be binned; matches this
			// engine's general nil-skip convention for aggregation
			// inputs elsewhere (e.g. make_list's own nil handling).
		}

		var binIdx int
		outOfRange := false
		if useFloat {
			av := types.ToFloat64(axisRaw)
			bin := binAtFloat64(av, stepFloat, startFloat)
			if bin < startFloat || bin >= endFloat {
				outOfRange = true
			} else {
				binIdx = int(math.Round((bin - startFloat) / stepFloat))
			}
		} else {
			av := types.ToInt64(axisRaw)
			bin := binAtInt64(av, stepInt, startInt)
			if bin < startInt || bin >= endInt {
				outOfRange = true
			} else {
				binIdx = int((bin - startInt) / stepInt)
			}
		}
		if outOfRange {
			continue
		}

		byVals := make([]types.Value, len(op.ByExprs))
		keyParts := make([]string, len(op.ByExprs))
		for i, byExpr := range op.ByExprs {
			val, err := evalExpr(byExpr.Expr, &input.Schema, row)
			if err != nil {
				return nil, fmt.Errorf("make-series by %s: %w", byExpr.Name, err)
			}
			byVals[i] = val
			keyParts[i] = fmt.Sprintf("%v", val)
		}
		key := strings.Join(keyParts, "\x00")

		g, exists := groups[key]
		if !exists {
			g = &groupEntry{byVals: byVals, buckets: make([][]types.Row, numBins)}
			groups[key] = g
			groupOrder = append(groupOrder, key)
		}
		g.buckets[binIdx] = append(g.buckets[binIdx], row)
	}

	// kind=nonempty: "Produces default result when the input of
	// make-series operator is empty" per real ADX docs — verified
	// against the exact worked example (a `take 0`'d, therefore fully
	// empty, input; without kind=nonempty the result is 0 rows, with
	// it, exactly 1 row of all-default values). Scoped here to the
	// genuinely unambiguous case only: zero input rows AND no `by`
	// clause (with a `by` clause and zero input rows, there's no
	// group-key value this engine could invent — real ADX's own
	// precise behavior for that combination isn't covered by any
	// worked example found, so it's deliberately left as the ordinary
	// zero-row result rather than guessed).
	if op.KindNonEmpty && len(input.Rows) == 0 && len(op.ByExprs) == 0 && len(groupOrder) == 0 {
		groupOrder = append(groupOrder, "")
		groups[""] = &groupEntry{byVals: nil, buckets: make([][]types.Row, numBins)}
	}

	// Build output schema: aggregation array columns, then by columns,
	// then AxisColumn array last — the by-vs-aggregation column order
	// is this engine's own established summarize convention (see
	// applySummarize's own doc comment on the identical, deliberate
	// divergence from real ADX's column order there), not a real-ADX
	// requirement; AxisColumn last IS a real, explicitly documented
	// rule ("The last column is an array containing the values of
	// AxisColumn"), preserved exactly.
	var outCols []types.Column
	for i, msAgg := range op.Aggregations {
		outCols = append(outCols, types.Column{Name: msAgg.Agg.Name, Type: types.TypeDynamic})
		_ = aggOutTypes[i]
	}
	byOffset := len(outCols)
	for _, byExpr := range op.ByExprs {
		byType := inferExprType(byExpr.Expr, &input.Schema)
		outCols = append(outCols, types.Column{Name: byExpr.Name, Type: byType})
	}
	axisOutIdx := len(outCols)
	outCols = append(outCols, types.Column{Name: op.AxisColumn, Type: types.TypeDynamic})

	output := types.NewTable(input.Name, types.Schema{Columns: outCols})

	// Pre-build the shared AxisColumn array (identical for every output
	// row — the bin axis doesn't vary per group).
	axisArr := make([]interface{}, numBins)
	for i := 0; i < numBins; i++ {
		if useFloat {
			axisArr[i] = axisValsFloat[i]
		} else {
			axisArr[i] = valueForJSONArray(axisValsInt[i], axisType)
		}
	}
	axisJSON, err := json.Marshal(axisArr)
	if err != nil {
		return nil, fmt.Errorf("make-series: encoding axis array: %w", err)
	}

	for _, key := range groupOrder {
		g := groups[key]
		outRow := make(types.Row, len(outCols))

		for i, msAgg := range op.Aggregations {
			arr := make([]interface{}, numBins)
			for b := 0; b < numBins; b++ {
				bucketRows := g.buckets[b]
				if len(bucketRows) == 0 {
					arr[b] = valueForJSONArray(defaults[i], aggOutTypes[i])
					continue
				}
				val, err := computeAgg(msAgg.Agg, bucketRows, &input.Schema)
				if err != nil {
					return nil, fmt.Errorf("make-series %s: %w", msAgg.Agg.Function, err)
				}
				arr[b] = valueForJSONArray(val, aggOutTypes[i])
			}
			b, err := json.Marshal(arr)
			if err != nil {
				return nil, fmt.Errorf("make-series: encoding %q array: %w", msAgg.Agg.Name, err)
			}
			outRow[i] = string(b)
		}

		for i := range op.ByExprs {
			outRow[byOffset+i] = g.byVals[i]
		}

		outRow[axisOutIdx] = string(axisJSON)
		output.AddRow(outRow)
	}

	return output, nil
}

// binAtInt64 computes bin_at(value, step, start) — floor((value-start)/step)*step
// + start — using proper floor division (not Go's truncating integer
// division), which matters specifically for a value before start: a
// naive value/step truncates toward zero, landing one bin too high for
// a negative delta. Verified against real ADX's own worked example
// (a 2016-12-31T06:00 data point, before a 2017-01-01 start, correctly
// falling in the 2016-12-31 bin -- one step before start -- and
// therefore outside the [start,end) output range entirely).
func binAtInt64(value, step, start int64) int64 {
	delta := value - start
	q := delta / step
	if delta%step != 0 && (delta < 0) != (step < 0) {
		q--
	}
	return start + q*step
}

// binAtFloat64 is binAtInt64's real-axis counterpart, using math.Floor
// directly since floating-point division has no truncation-direction
// ambiguity to correct for.
func binAtFloat64(value, step, start float64) float64 {
	return start + math.Floor((value-start)/step)*step
}

// coerceNumeric converts v to the Go representation matching outType,
// so a make-series default value (or a bare literal like `default=0`)
// JSON-encodes with the right shape (0 vs 0.0) for its column's own
// aggregation type, rather than whatever literal type the query
// happened to write it as.
func coerceNumeric(v types.Value, outType types.KQLType) types.Value {
	if v == nil {
		return nil
	}
	switch outType {
	case types.TypeReal:
		return types.ToFloat64(v)
	case types.TypeLong, types.TypeInt:
		return types.ToInt64(v)
	default:
		return v
	}
}

// valueForJSONArray converts a typed engine Value into the Go value
// json.Marshal should encode for a make-series output array element --
// numeric types stay numeric (matching real ADX's own worked example
// output, e.g. "[ 4.0, 3.0, 5.0, ... ]", not quoted strings), datetime
// gets real ADX's own exact 7-fractional-digit UTC ISO-8601 format
// (matching the worked example's own "2017-01-01T00:00:00.0000000Z").
func valueForJSONArray(v types.Value, t types.KQLType) interface{} {
	if v == nil {
		return nil
	}
	switch t {
	case types.TypeReal:
		return types.ToFloat64(v)
	case types.TypeLong, types.TypeInt:
		return types.ToInt64(v)
	case types.TypeDatetime:
		return formatDatetimeISO7(types.ToInt64(v))
	default:
		return v
	}
}

// formatDatetimeISO7 matches real ADX's own exact datetime display
// format used throughout its make-series (and general dynamic-array)
// output: a fixed 7-digit fractional-second UTC ISO-8601 string, e.g.
// "2017-01-01T00:00:00.0000000Z". This engine's general-purpose
// types.FormatValue uses time.RFC3339Nano instead, which trims
// trailing zero digits (producing "2017-01-01T00:00:00Z" for a whole-
// second value) -- correct and unambiguous, but not a byte-for-byte
// match to real ADX's own documented worked example, which this
// function exists specifically to reproduce for make-series's array
// output.
func formatDatetimeISO7(nanos int64) string {
	t := time.Unix(0, nanos).UTC()
	return t.Format("2006-01-02T15:04:05.0000000Z")
}

