package engine

// search.go — the ADX search operator:
//
//	[T |] search [kind=case_sensitive] [in (T1, T2*)] Predicate
//
// The predicate is a KQL boolean expression. A bare string term means
// "any column has the term" and Col:"term" means "Col has the term";
// the parser rewrites both into SearchAnyFunc / SearchColFunc calls.
// A term's wildcards choose the operator (reference table):
//
//	billg   has          *billg   hassuffix
//	billg*  hasprefix    *billg*  contains
//	bi*lg   matches regex @"\bbi.*lg\b"
//
// Matching is case-insensitive unless kind=case_sensitive. Every
// column is searched through its tostring form (search "4625" finds an
// EventID of 4625), except the hidden _TimeReceived.
//
// Output: $table (the source table's name), then the union kind=outer
// of the searched tables' columns, which is the schema Kusto.Language
// reports. A column the predicate names but a table lacks is null for
// that table, so `search "x" and DateKey > ...` over a database
// matches only tables that have DateKey.
//
// This replaced a gokql-specific form that returned a hit list
// (TableName, Column, RowKey, Value) and accepted only one term.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

const searchTableColumn = "$table"

// searchTermMatches applies one search term to one value.
func searchTermMatches(s, term string, cs bool) bool {
	lead := strings.HasPrefix(term, "*")
	trail := strings.HasSuffix(term, "*") && len(term) > 1
	inner := term
	if lead {
		inner = inner[1:]
	}
	if trail {
		inner = inner[:len(inner)-1]
	}
	if strings.Contains(inner, "*") {
		re, err := searchWildcardRegex(term, cs)
		return err == nil && re.MatchString(s)
	}
	switch {
	case lead && trail:
		if cs {
			return strings.Contains(s, inner)
		}
		return strings.Contains(strings.ToLower(s), strings.ToLower(inner))
	case trail:
		return hasTermPrefix(s, inner, cs)
	case lead:
		return hasTermSuffix(s, inner, cs)
	default:
		return hasTerm(s, inner, cs)
	}
}

var searchRegexCache = struct {
	m map[string]*regexp.Regexp
}{m: map[string]*regexp.Regexp{}}

// searchWildcardRegex compiles a term with an inner wildcard, Lab*PC,
// to \bLab.*PC\b (leading/trailing * drop the boundary on that side).
func searchWildcardRegex(term string, cs bool) (*regexp.Regexp, error) {
	parts := strings.Split(term, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	pat := strings.Join(parts, ".*")
	if !strings.HasPrefix(term, "*") {
		pat = `\b` + pat
	}
	if !strings.HasSuffix(term, "*") {
		pat += `\b`
	}
	if !cs {
		pat = "(?i)" + pat
	}
	return regexp.Compile(pat)
}

// evalSearchFunc evaluates the predicate rewrite's internal functions.
func evalSearchFunc(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case parser.SearchAnyFunc:
		term, cs, err := searchTermArgs(eng, fc.Args, 0, schema, row)
		if err != nil {
			return nil, true, err
		}
		for i, c := range schema.Columns {
			if c.Name == timeReceivedColumnName || c.Name == searchTableColumn || i >= len(row) || row[i] == nil {
				continue
			}
			if searchTermMatches(kqlToString(row[i], c.Type), term, cs) {
				return true, true, nil
			}
		}
		return false, true, nil
	case parser.SearchColFunc:
		ref, ok := fc.Args[0].(*parser.ColumnRef)
		if !ok || len(fc.Args) != 3 {
			return nil, true, fmt.Errorf("search: Column:\"term\" needs a column name")
		}
		term, cs, err := searchTermArgs(eng, fc.Args, 1, schema, row)
		if err != nil {
			return nil, true, err
		}
		idx := schema.ColumnIndex(ref.Name)
		if idx < 0 || idx >= len(row) || row[idx] == nil {
			return false, true, nil
		}
		return searchTermMatches(kqlToString(row[idx], schema.Columns[idx].Type), term, cs), true, nil
	}
	return nil, false, nil
}

func searchTermArgs(eng *Engine, args []parser.Expr, at int, schema *types.Schema, row types.Row) (string, bool, error) {
	if len(args) != at+2 {
		return "", false, fmt.Errorf("search: malformed term")
	}
	tv, err := evalExpr(eng, args[at], schema, row)
	if err != nil {
		return "", false, err
	}
	cv, _ := args[at+1].(*parser.Literal)
	cs, _ := cv.Value.(bool)
	return fmtV(tv), cs, nil
}

// searchFilter keeps the rows of input the predicate matches. Columns
// the predicate names but input lacks are evaluated as null.
func (e *Engine) searchFilter(input *types.Table, pred parser.Expr) (*types.Table, error) {
	pred, err := substituteToScalars(e, pred)
	if err != nil {
		return nil, err
	}
	refs := map[string]bool{}
	collectColumnRefs(pred, refs)
	evalSchema := input.Schema
	var missing int
	for name := range refs {
		if input.Schema.ColumnIndex(name) < 0 {
			if missing == 0 {
				evalSchema = types.Schema{Columns: append([]types.Column(nil), input.Schema.Columns...)}
			}
			evalSchema.Columns = append(evalSchema.Columns, types.Column{Name: name, Type: types.TypeString})
			missing++
		}
	}
	out := types.NewTable(input.Name, input.Schema)
	buf := make(types.Row, len(evalSchema.Columns))
	for _, row := range input.Rows {
		evalRow := row
		if missing > 0 {
			copy(buf, row)
			for i := len(row); i < len(buf); i++ {
				buf[i] = nil
			}
			evalRow = buf
		}
		v, err := evalExpr(e, pred, &evalSchema, evalRow)
		if err != nil {
			return nil, err
		}
		if b, ok := v.(bool); ok && b {
			out.AddRow(row)
		}
	}
	return out, nil
}

// searchTables resolves in (...) (names and wildcards) or, without it,
// every user table, sorted for a deterministic result.
func (e *Engine) searchTables(patterns []string) ([]string, error) {
	all := e.Catalog.ListTables()
	sort.Strings(all)
	if len(patterns) == 0 {
		return all, nil
	}
	var names []string
	seen := map[string]bool{}
	for _, p := range patterns {
		if !strings.Contains(p, "*") {
			if !seen[p] {
				seen[p] = true
				names = append(names, p)
			}
			continue
		}
		for _, n := range all {
			if wildcardMatch(p, n) && !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	return names, nil
}

// applySearch is the source form: search the named (or all) tables.
func (e *Engine) applySearch(op *parser.SearchOp) (*types.Table, error) {
	names, err := e.searchTables(op.Tables)
	if err != nil {
		return nil, err
	}
	var legs []*types.Table
	for _, name := range names {
		data, err := e.executeQuery(&parser.Query{Source: name})
		if err != nil {
			return nil, fmt.Errorf("search: %s: %w", name, err)
		}
		hits, err := e.searchFilter(data, op.Predicate)
		if err != nil {
			return nil, fmt.Errorf("search: %s: %w", name, err)
		}
		legs = append(legs, hits)
	}
	return withSearchTable(legs, names), nil
}

// applySearchPiped is the operator form: search the input.
func (e *Engine) applySearchPiped(input *types.Table, op *parser.SearchOp) (*types.Table, error) {
	hits, err := e.searchFilter(input, op.Predicate)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	return withSearchTable([]*types.Table{hits}, []string{e.pipelineSource}), nil
}

// withSearchTable unions legs (kind=outer) behind a $table column.
func withSearchTable(legs []*types.Table, names []string) *types.Table {
	schema, colMap := buildUnionSchema(legs, "outer")
	cols := append([]types.Column{{Name: searchTableColumn, Type: types.TypeString}}, schema.Columns...)
	out := types.NewTable("", types.Schema{Columns: cols})
	for li, leg := range legs {
		for _, row := range leg.Rows {
			r := make(types.Row, len(cols))
			r[0] = names[li]
			for ci, src := range colMap[li] {
				if src >= 0 {
					r[ci+1] = row[src]
				}
			}
			out.AddRow(r)
		}
	}
	return out
}
