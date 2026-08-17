package engine

// search.go — search "term": a cross-table, cross-column lexical scan.
// Backlog P1 item 8. Motivated by the retrieval-quality assessment
// finding that every lexical query had to name its table and column
// explicitly — a real ergonomic gap for the memory-scope use case
// ("where does this term appear anywhere in my scope").
//
// Deliberately scoped, not a full clone of real Kusto's search: real
// Kusto unions full heterogeneous row schemas across tables with
// null-padding for columns absent in a given source table. That's a
// harder problem this doesn't attempt to solve. Instead, this returns
// a normalized hit list (TableName, Column, RowKey, Value) — every
// string-typed column in every scanned table whose value has the term
// (word-bounded, case-insensitive — identical semantics to `has`,
// verified correctly implemented elsewhere in this codebase). RowKey
// is the row's first column, which by this project's own table-design
// convention is always the row's identifying key (Id, Name, Src) —
// good enough to jump to the full row with a follow-up query, without
// solving the general heterogeneous-union problem.

import (
	"fmt"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// searchTermMatches implements search's wildcard handling: a `*` at
// either end of the term is a genuine wildcard (matching ADX's own
// mapping — a trailing `*` becomes a prefix match, a leading `*`
// becomes a suffix match), not a literal character to find. Before
// this, `*` had no special handling at all: it was passed straight
// into hasTerm as literal text, so `search "Tammu*"` looked for the
// literal six-character sequence "Tammu*" (with an asterisk) in the
// content, silently matched nothing, and returned zero rows with no
// error — a real, confirmed live bug, not a hypothetical gap.
//
// A `*` on BOTH ends is genuinely unbounded substring containment, NOT
// word-boundary-aware has — "*Tamm*" must match "Tammuz" even though
// "Tamm" isn't a whole word or boundary-aligned within it. A first
// version of this fix collapsed the both-wildcard case to hasTerm
// (word-bounded), reasoning it matched this engine's existing
// unwildcarded search semantics — that was wrong, caught live: search
// "*Tamm*" against a real corpus containing "Tammuz" returned nothing,
// the exact silent-miss shape this whole fix exists to close. Fixed to
// plain strings.Contains (case-insensitive), the same logic the
// separately-verified `contains` operator already uses.
func searchTermMatches(s, term string) bool {
	hasPrefix := strings.HasSuffix(term, "*")
	hasSuffix := strings.HasPrefix(term, "*")
	switch {
	case hasPrefix && hasSuffix && len(term) >= 2:
		return strings.Contains(strings.ToLower(s), strings.ToLower(term[1:len(term)-1]))
	case hasPrefix:
		return hasTermPrefix(s, term[:len(term)-1], false)
	case hasSuffix:
		return hasTermSuffix(s, term[1:], false)
	default:
		return hasTerm(s, term, false)
	}
}

func (e *Engine) applySearch(op *parser.SearchOp) (*types.Table, error) {
	if op.Term == "" {
		return nil, fmt.Errorf("search: empty term")
	}

	tableNames := op.Tables
	if len(tableNames) == 0 {
		tableNames = e.Catalog.ListTables()
	}

	outSchema := types.Schema{Columns: []types.Column{
		{Name: "TableName", Type: types.TypeString},
		{Name: "Column", Type: types.TypeString},
		{Name: "RowKey", Type: types.TypeString},
		{Name: "Value", Type: types.TypeString},
	}}
	result := types.NewTable("", outSchema)

	for _, tableName := range tableNames {
		tableDef := e.Catalog.GetTable(tableName)
		if tableDef == nil {
			return nil, fmt.Errorf("search: table %q not found", tableName)
		}
		if len(tableDef.Schema.Columns) == 0 {
			continue
		}

		var stringCols []int
		for i, col := range tableDef.Schema.Columns {
			if col.Type == types.TypeString || col.Type == types.TypeGUID || col.Type == types.TypeDynamic {
				stringCols = append(stringCols, i)
			}
		}
		if len(stringCols) == 0 {
			continue
		}

		data, err := e.executeQuery(&parser.Query{Source: tableName})
		if err != nil {
			return nil, fmt.Errorf("search: scanning %q: %w", tableName, err)
		}

		for _, row := range data.Rows {
			rowKey := ""
			if len(row) > 0 && row[0] != nil {
				rowKey = fmt.Sprintf("%v", row[0])
			}
			for _, ci := range stringCols {
				if row[ci] == nil {
					continue
				}
				sval := fmt.Sprintf("%v", row[ci])
				if searchTermMatches(sval, op.Term) {
					result.AddRow(types.Row{
						tableName,
						tableDef.Schema.Columns[ci].Name,
						rowKey,
						sval,
					})
				}
			}
		}
	}

	return result, nil
}
