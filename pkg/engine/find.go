package engine

// find.go — the find operator, the older, cross-table search
// predecessor to search (search.go). Verified against real ADX's own
// find operator docs before implementing.
//
// Deliberately scoped, not a full clone of every real-ADX find
// capability — see FindOp's own doc comment (ast.go) for exactly
// which real-ADX capabilities are out of scope here (wildcard table
// names, cross-database/cluster) and why.

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// collectColumnRefs walks expr and adds every referenced column name
// to out — mirrors exprReferencesColumn's own recursive structure
// (timereceived.go) exactly, but COLLECTS every name into a set
// rather than checking for one specific name, since find's own
// project-smart output-schema logic needs the full set of columns a
// predicate references, not a yes/no answer about a single column.
func collectColumnRefs(expr parser.Expr, out map[string]bool) {
	switch e := expr.(type) {
	case *parser.ColumnRef:
		out[e.Name] = true
	case *parser.BinaryExpr:
		collectColumnRefs(e.Left, out)
		collectColumnRefs(e.Right, out)
	case *parser.UnaryExpr:
		collectColumnRefs(e.Expr, out)
	case *parser.FuncCall:
		for _, arg := range e.Args {
			collectColumnRefs(arg, out)
		}
	case *parser.InExpr:
		collectColumnRefs(e.Column, out)
		for _, v := range e.Values {
			collectColumnRefs(v, out)
		}
	case *parser.BetweenExpr:
		collectColumnRefs(e.Expr, out)
		collectColumnRefs(e.Low, out)
		collectColumnRefs(e.High, out)
	case *parser.AccessExpr:
		collectColumnRefs(e.Object, out)
	case *parser.HasAnyAllExpr:
		collectColumnRefs(e.Column, out)
		for _, v := range e.Values {
			collectColumnRefs(v, out)
		}
	}
}

// findMatchedRow pairs a matching row with the schema of the table it
// came from — find scans tables with genuinely different schemas, so
// unlike every other operator in this engine, output-row construction
// needs each row's own, specific source schema, not one shared input
// schema.
type findMatchedRow struct {
	tableName string
	schema    *types.Schema
	row       types.Row
}

func (e *Engine) applyFind(op *parser.FindOp) (*types.Table, error) {
	sourceCol := "source_"
	if op.WithSource != "" {
		sourceCol = op.WithSource
	}

	tableNames := op.Tables
	if len(tableNames) == 0 {
		tableNames = e.Catalog.ListTables()
	}

	var predicateCols map[string]bool
	if op.Predicate != nil {
		predicateCols = map[string]bool{}
		collectColumnRefs(op.Predicate, predicateCols)
	}

	var matched []findMatchedRow

	for _, tableName := range tableNames {
		tableDef := e.Catalog.GetTable(tableName)
		if tableDef == nil {
			return nil, fmt.Errorf("find: table %q not found", tableName)
		}
		schema := tableDef.Schema

		if predicateCols != nil {
			// "Source tables that don't contain any column used by the
			// predicate evaluation, are filtered out" — real ADX's own
			// stated wording, verified directly against a real,
			// concrete worked example before trusting it, not just the
			// prose: a first version of this required EVERY predicate-
			// referenced column present in a table before scanning it
			// at all, which is too strict for an OR predicate spanning
			// columns from different tables (find Version == 'v1.0.0'
			// or EventName == 'Event1' — real ADX's own documented
			// example, whose own docs explicitly explain "EventsTable1
			// rows are filtered with Version == 'v1.0.0' [alone] and
			// EventsTable2 rows are filtered with EventName == 'Event1'
			// [alone]") — that first version silently returned zero
			// rows for this exact real-ADX documented example, caught
			// by testing against it directly rather than assumed
			// correct from the prose alone. Fixed to the weaker,
			// correct condition: skip a table only if NONE of the
			// predicate's referenced columns exist in it at all (zero
			// overlap) — a table with SOME but not all of them still
			// gets scanned, with the missing ones padded as null before
			// evaluation (below), so `EventName == 'Event1'` correctly
			// evaluates to false (not an error) for a table that
			// simply doesn't have an EventName column, letting the
			// surrounding OR fall through correctly to whichever
			// column that specific table DOES have.
			anyOverlap := false
			for name := range predicateCols {
				if schema.ColumnIndex(name) >= 0 {
					anyOverlap = true
					break
				}
			}
			if !anyOverlap {
				goto nextTable
			}
		}

		{
			data, err := e.executeQuery(&parser.Query{Source: tableName})
			if err != nil {
				return nil, fmt.Errorf("find: scanning %q: %w", tableName, err)
			}

			// Pad data.Schema/each row with a null-valued column for
			// any predicate-referenced name this table doesn't
			// actually have — see the comment above for why: without
			// this, evalExpr's own ColumnRef case (eval.go) errors
			// outright ("column %q not found") rather than treating a
			// genuinely absent column as null/false the way real ADX's
			// own cross-table predicate evaluation does.
			// evalSchema.Columns is a genuinely independent copy of
			// data.Schema.Columns, not a struct-copy sharing the same
			// backing array — appending to it below must never risk
			// aliasing into (and silently mutating) data.Schema's own
			// slice, even though nothing else reads data after this
			// point in practice; explicit safety here costs nothing.
			evalSchema := types.Schema{Columns: append([]types.Column{}, data.Schema.Columns...)}
			padCount := 0
			if predicateCols != nil {
				for name := range predicateCols {
					if data.Schema.ColumnIndex(name) < 0 {
						evalSchema.Columns = append(evalSchema.Columns, types.Column{Name: name, Type: types.TypeDynamic})
						padCount++
					}
				}
			}

			for _, row := range data.Rows {
				isMatch := false
				if op.AnyColumnTerm != "" {
					isMatch = rowHasTermAnyColumn(row, op.AnyColumnTerm)
				} else {
					evalRow := row
					if padCount > 0 {
						evalRow = make(types.Row, len(row)+padCount)
						copy(evalRow, row)
					}
					val, err := evalExpr(op.Predicate, &evalSchema, evalRow)
					if err != nil {
						return nil, fmt.Errorf("find: predicate against %q: %w", tableName, err)
					}
					b, _ := val.(bool)
					isMatch = b
				}
				if isMatch {
					matched = append(matched, findMatchedRow{tableName: tableName, schema: &data.Schema, row: row})
				}
			}
		}
	nextTable:
	}

	if op.ProjectSmart {
		return buildFindProjectSmart(sourceCol, matched)
	}
	return buildFindProjectExplicit(sourceCol, op.ProjectItems, op.PackAll, matched)
}

// rowHasTermAnyColumn implements the "* has term" / bare-term
// predicate forms — searches every column of THIS row (whatever
// columns this row's own table happens to have), reusing
// searchTermMatches (search.go) for the actual term-matching logic
// rather than a second, separate implementation of the same
// wildcard-aware, word-bounded matching rules.
func rowHasTermAnyColumn(row types.Row, term string) bool {
	for _, col := range row {
		if col == nil {
			continue
		}
		s := fmt.Sprintf("%v", col)
		if searchTermMatches(s, term) {
			return true
		}
	}
	return false
}

// buildFindProjectSmart implements real ADX's own documented
// project-smart output schema: "columns that appear explicitly in the
// predicate" (empty set for the any-column-term forms, which
// reference no specific column at all) plus "columns that are common
// to all the filtered tables" appear as real, typed output columns;
// everything else is packed into an extra pack_ column.
//
// Simplified from real ADX in one stated way: a column name present
// in multiple filtered tables with DIFFERING types is treated as not
// common at all here (packed instead), rather than real ADX's own
// type-split behavior (a separate ColumnName_type output column per
// distinct type). Real ADX's own type-split logic exists for a
// distributed, heterogeneous-schema-at-scale scenario this engine's
// single-node, small-scope use case is unlikely to hit in practice;
// building it was judged not worth the complexity for a first
// version.
func buildFindProjectSmart(sourceCol string, matched []findMatchedRow) (*types.Table, error) {
	// Determine the set of distinct tables actually present among
	// matched rows (a table filtered out entirely up front never
	// contributes here; a table that had zero MATCHING rows also
	// doesn't count toward "common to all the filtered tables" —
	// real ADX's own wording is about tables that survived filtering,
	// which in practice means tables that contributed at least one row).
	tableSchemas := map[string]*types.Schema{}
	for _, m := range matched {
		tableSchemas[m.tableName] = m.schema
	}

	commonCols := map[string]types.KQLType{}
	first := true
	for _, schema := range tableSchemas {
		colTypes := map[string]types.KQLType{}
		for _, c := range schema.Columns {
			colTypes[c.Name] = c.Type
		}
		if first {
			for n, t := range colTypes {
				commonCols[n] = t
			}
			first = false
			continue
		}
		for n, t := range commonCols {
			if ot, ok := colTypes[n]; !ok || ot != t {
				delete(commonCols, n)
			}
		}
	}

	names := make([]string, 0, len(commonCols))
	for n := range commonCols {
		names = append(names, n)
	}
	sort.Strings(names)

	outCols := []types.Column{{Name: sourceCol, Type: types.TypeString}}
	for _, n := range names {
		outCols = append(outCols, types.Column{Name: n, Type: commonCols[n]})
	}
	outCols = append(outCols, types.Column{Name: "pack_", Type: types.TypeDynamic})
	outSchema := types.Schema{Columns: outCols}
	result := types.NewTable("", outSchema)

	for _, m := range matched {
		newRow := make(types.Row, len(outCols))
		newRow[0] = m.tableName
		packed := map[string]interface{}{}
		for i, c := range m.schema.Columns {
			if idx := outSchema.ColumnIndex(c.Name); idx > 0 && idx <= len(names) {
				newRow[idx] = m.row[i]
				continue
			}
			packed[c.Name] = jsonToKQLValue(m.row[i])
		}
		b, _ := json.Marshal(packed)
		newRow[len(outCols)-1] = string(b)
		result.AddRow(newRow)
	}
	return result, nil
}

// buildFindProjectExplicit implements find's explicit
// "project ColumnName[:ColumnType], ... [, pack_all()]" form — real
// ADX's own documented behavior, verified before implementing: "the
// result table includes the columns specified in the list. If a
// source table doesn't contain a certain column, the values in the
// corresponding rows are null." pack_all() additionally packs EVERY
// column (including the projected ones) into an extra output column.
func buildFindProjectExplicit(sourceCol string, items []parser.FindProjectItem, packAll bool, matched []findMatchedRow) (*types.Table, error) {
	outCols := []types.Column{{Name: sourceCol, Type: types.TypeString}}
	for _, item := range items {
		t := item.Type
		if t == 0 {
			t = types.TypeString // no declared type and this column may be absent from some source tables entirely — default to string rather than guessing from data
		}
		outCols = append(outCols, types.Column{Name: item.Name, Type: t})
	}
	if packAll {
		// Named "pack_", not "column1" — real ADX's own docs are
		// directly self-inconsistent here (prose states the default
		// name is "column1", but BOTH of the docs' own concrete worked
		// examples using pack_all() show the resulting column named
		// "pack_" instead) — verified by checking, not assumed:
		// trusting the concrete, repeated worked-example evidence over
		// what reads as a prose documentation error.
		outCols = append(outCols, types.Column{Name: "pack_", Type: types.TypeDynamic})
	}
	outSchema := types.Schema{Columns: outCols}
	result := types.NewTable("", outSchema)

	for _, m := range matched {
		newRow := make(types.Row, len(outCols))
		newRow[0] = m.tableName
		for i, item := range items {
			if idx := m.schema.ColumnIndex(item.Name); idx >= 0 {
				newRow[i+1] = m.row[idx]
			}
		}
		if packAll {
			packed := map[string]interface{}{}
			for i, c := range m.schema.Columns {
				packed[c.Name] = jsonToKQLValue(m.row[i])
			}
			b, _ := json.Marshal(packed)
			newRow[len(outCols)-1] = string(b)
		}
		result.AddRow(newRow)
	}
	return result, nil
}
