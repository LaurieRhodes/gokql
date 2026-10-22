package engine

// kql_tostring.go — the string form of a scalar as tostring() and
// strcat() produce it. Runtime values do not carry their KQL type
// (datetime is int64 nanoseconds, timespan int64 100ns ticks, like long), so the
// caller passes the static type of the expression.
//
// datetime: ISO 8601 UTC, "2020-01-01T10:00:00.5000000Z" — seven
// fractional digits, omitted when the value is a whole second
// ("2020-01-01T00:00:00Z"). timespan: .NET's constant format,
// "[-][d.]hh:mm:ss[.fffffff]".

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func kqlToString(v types.Value, t types.KQLType) string {
	if v == nil {
		return ""
	}
	switch t {
	case types.TypeDatetime:
		if n, ok := asInt64(v); ok {
			return formatDatetimeTostring(n)
		}
		if tm, ok := v.(time.Time); ok {
			return formatDatetimeTostring(tm.UnixNano())
		}
	case types.TypeTimespan:
		if n, ok := asInt64(v); ok {
			return formatTimespanTostring(n)
		}
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return types.FormatReal(x)
		}
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	return fmt.Sprintf("%v", v)
}

// exprToString evaluates expr and renders it with kqlToString.
func exprToString(eng *Engine, expr parser.Expr, schema *types.Schema, row types.Row) (string, bool, error) {
	v, err := evalExpr(eng, expr, schema, row)
	if err != nil || v == nil {
		return "", v != nil, err
	}
	return kqlToString(v, inferExprType(eng, expr, schema)), true, nil
}

func asInt64(v types.Value) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int32:
		return int64(x), true
	case int:
		return int64(x), true
	}
	return 0, false
}

func formatDatetimeTostring(nanos int64) string {
	t := time.Unix(0, nanos).UTC()
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05Z")
	}
	return t.Format("2006-01-02T15:04:05.0000000Z")
}

func formatTimespanTostring(ticks int64) string {
	neg := ticks < 0
	if neg {
		ticks = -ticks
	}
	const tps = 10_000_000
	frac := ticks % tps
	secs := ticks / tps
	d, h, m, s := secs/86400, (secs/3600)%24, (secs/60)%60, secs%60
	out := ""
	if neg {
		out = "-"
	}
	if d > 0 {
		out += strconv.FormatInt(d, 10) + "."
	}
	out += fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	if frac > 0 {
		out += fmt.Sprintf(".%07d", frac)
	}
	return out
}
