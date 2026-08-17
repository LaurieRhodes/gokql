package parser

import (
	"fmt"
	"strings"
)

// parseMakeSeries parses:
//   [kind=nonempty] [hint.shufflekey=...] [Column=]Aggregation[default=DefaultValue][, ...]
//   on AxisColumn [from start] [to end] step step [by [Column=]GroupExpr[, ...]]
//
// Verified against real ADX docs (make-series-operator.md). See
// MakeSeriesOp's own doc comment for exactly which parts of the real
// grammar this covers (main syntax only; from/to both required here,
// unlike real ADX where either may be auto-detected from the data).
func parseMakeSeries(s string) (Operator, error) {
	s = strings.TrimSpace(s)

	op := &MakeSeriesOp{}

	// kind=nonempty and hint.shufflekey=... are both optional, leading,
	// space-separated tokens before the aggregation list — same pattern
	// already used by parseEvaluate/parsePartition for hint.xxx=yyy.
	for {
		fields := strings.Fields(s)
		if len(fields) == 0 {
			return nil, fmt.Errorf("make-series: expected at least one aggregation")
		}
		lowerField := strings.ToLower(fields[0])
		if lowerField == "kind=nonempty" {
			op.KindNonEmpty = true
			s = strings.TrimSpace(s[len(fields[0]):])
			continue
		}
		if strings.HasPrefix(lowerField, "hint.") {
			s = strings.TrimSpace(s[len(fields[0]):])
			continue
		}
		break
	}

	onIdx := findKeyword(s, " on ")
	if onIdx < 0 {
		return nil, fmt.Errorf("make-series: expected 'on AxisColumn'")
	}
	aggStr := strings.TrimSpace(s[:onIdx])
	rest := strings.TrimSpace(s[onIdx+len(" on "):])

	// Aggregations: comma-separated [Column=]Aggregation[default=DefaultValue]
	for _, part := range splitRespectingParens(aggStr, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		msAgg, err := parseMakeSeriesAggregation(part)
		if err != nil {
			return nil, err
		}
		op.Aggregations = append(op.Aggregations, *msAgg)
	}
	if len(op.Aggregations) == 0 {
		return nil, fmt.Errorf("make-series: no aggregations specified")
	}

	// Split off the "by ..." tail before parsing AxisColumn/from/to/step,
	// same approach as parseSummarize — findKeyword is paren/quote aware
	// so a `by` inside an aggregation's own args (already consumed above)
	// can't false-match here anyway, but ordering this after aggregation
	// parsing keeps the two concerns cleanly separated regardless.
	var byStr string
	byIdx := findKeyword(rest, " by ")
	if byIdx >= 0 {
		byStr = strings.TrimSpace(rest[byIdx+len(" by "):])
		rest = strings.TrimSpace(rest[:byIdx])
	}

	// AxisColumn, then optional "from X", optional "to Y", required "step S" —
	// real ADX's own fixed grammar order, verified before assuming it.
	fromIdx := findKeyword(rest, " from ")
	toIdx := findKeyword(rest, " to ")
	stepIdx := findKeyword(rest, " step ")
	axisEnd := -1
	for _, idx := range []int{fromIdx, toIdx, stepIdx} {
		if idx >= 0 && (axisEnd < 0 || idx < axisEnd) {
			axisEnd = idx
		}
	}
	if axisEnd < 0 {
		return nil, fmt.Errorf("make-series: expected 'on AxisColumn ... step step'")
	}
	op.AxisColumn = strings.TrimSpace(rest[:axisEnd])
	if op.AxisColumn == "" || !isValidIdentifier(op.AxisColumn) {
		return nil, fmt.Errorf("make-series: expected a column name after 'on', got %q", op.AxisColumn)
	}
	cursor := strings.TrimSpace(rest[axisEnd:])

	if strings.HasPrefix(strings.ToLower(cursor), "from ") {
		cursor = cursor[len("from "):]
		toPos := findKeyword(cursor, " to ")
		stepPos := findKeyword(cursor, " step ")
		end := -1
		for _, idx := range []int{toPos, stepPos} {
			if idx >= 0 && (end < 0 || idx < end) {
				end = idx
			}
		}
		if end < 0 {
			return nil, fmt.Errorf("make-series: expected 'to' or 'step' after 'from'")
		}
		fromExpr, err := ParseExpr(strings.TrimSpace(cursor[:end]))
		if err != nil {
			return nil, fmt.Errorf("make-series: from: %w", err)
		}
		op.From = fromExpr
		cursor = strings.TrimSpace(cursor[end:])
	}

	if strings.HasPrefix(strings.ToLower(cursor), "to ") {
		cursor = cursor[len("to "):]
		stepPos := findKeyword(cursor, " step ")
		if stepPos < 0 {
			return nil, fmt.Errorf("make-series: expected 'step' after 'to'")
		}
		toExpr, err := ParseExpr(strings.TrimSpace(cursor[:stepPos]))
		if err != nil {
			return nil, fmt.Errorf("make-series: to: %w", err)
		}
		op.To = toExpr
		cursor = strings.TrimSpace(cursor[stepPos:])
	}

	if !strings.HasPrefix(strings.ToLower(cursor), "step ") {
		return nil, fmt.Errorf("make-series: expected 'step step' clause (step is required)")
	}
	cursor = strings.TrimSpace(cursor[len("step "):])
	if cursor == "" {
		return nil, fmt.Errorf("make-series: expected a step value")
	}
	stepExpr, err := ParseExpr(cursor)
	if err != nil {
		return nil, fmt.Errorf("make-series: step: %w", err)
	}
	op.Step = stepExpr

	if op.From == nil || op.To == nil {
		return nil, fmt.Errorf("make-series: 'from' and 'to' are both required here — real ADX's own auto-detect-from-data behavior for an omitted from/to is not implemented")
	}

	// By clause: identical grammar/auto-naming to summarize's own by
	// clause — reuses ByExpr and deriveByName directly.
	if byStr != "" {
		for _, part := range splitRespectingParens(byStr, ',') {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			var name string
			exprStr := part
			eqIdx := strings.Index(part, "=")
			if eqIdx > 0 && (eqIdx+1 >= len(part) || part[eqIdx+1] != '=') &&
				(eqIdx == 0 || part[eqIdx-1] != '!') {
				name = strings.TrimSpace(part[:eqIdx])
				exprStr = strings.TrimSpace(part[eqIdx+1:])
			}
			expr, err := ParseExpr(exprStr)
			if err != nil {
				return nil, fmt.Errorf("make-series by: %w", err)
			}
			if name == "" {
				name = deriveByName(expr)
			}
			op.ByExprs = append(op.ByExprs, ByExpr{Name: name, Expr: expr})
		}
	}

	return op, nil
}

// parseMakeSeriesAggregation parses one "[Column=]Aggregation[default=DefaultValue]"
// entry — the Column=Aggregation part is identical grammar to
// parseAggregation (summarize's own), reused directly rather than
// duplicated; default=... is the one genuinely new piece.
func parseMakeSeriesAggregation(s string) (*MakeSeriesAggregation, error) {
	s = strings.TrimSpace(s)

	// Find a top-level " default=" or " default =" following the
	// aggregation call's own closing paren, not inside it — scan from
	// the end since default, if present, is always the trailing clause.
	defaultIdx := -1
	lower := strings.ToLower(s)
	searchFrom := 0
	for {
		idx := strings.Index(lower[searchFrom:], "default")
		if idx < 0 {
			break
		}
		absIdx := searchFrom + idx
		// Must be preceded by whitespace/paren-close and followed by
		// optional whitespace then '='.
		after := strings.TrimLeft(s[absIdx+len("default"):], " \t")
		if strings.HasPrefix(after, "=") && (absIdx == 0 || s[absIdx-1] == ' ' || s[absIdx-1] == ')') {
			defaultIdx = absIdx
			break
		}
		searchFrom = absIdx + len("default")
	}

	aggText := s
	var defaultText string
	if defaultIdx >= 0 {
		aggText = strings.TrimSpace(s[:defaultIdx])
		rest := strings.TrimLeft(s[defaultIdx+len("default"):], " \t")
		rest = strings.TrimPrefix(rest, "=")
		defaultText = strings.TrimSpace(rest)
	}

	agg, err := parseAggregation(aggText)
	if err != nil {
		return nil, fmt.Errorf("make-series: %w", err)
	}

	msAgg := &MakeSeriesAggregation{Agg: *agg}
	if defaultText != "" {
		defExpr, err := ParseExpr(defaultText)
		if err != nil {
			return nil, fmt.Errorf("make-series: default: %w", err)
		}
		msAgg.Default = defExpr
	}
	return msAgg, nil
}

