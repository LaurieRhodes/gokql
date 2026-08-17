package engine

// setappend.go — .set-or-append T <| query: appends a query's result
// rows onto an existing table as a new extent. Creates the table with
// the query's schema first if it doesn't exist (matching real Kusto
// set-or-append semantics). The common case is a literal
// datatable(Col: Type, ...) [v1, v2, ...] query, giving typed-literal
// row insertion with none of CSV's escaping surface: KQL string
// literals use quote/backslash rules the model already knows from
// training, rather than comma-escaping rules specific to this ingest
// path.

import (
	"fmt"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// setCreate implements .set T <| query: real Kusto semantics are
// create-only — fails clearly if T already exists, rather than
// silently appending or replacing. (.set was previously parsed into a
// SetCmd AST node with no engine dispatch case at all — any use of it
// hit "unsupported statement type" — found live during the backlog
// pass.) Reuses setOrAppendCore for the actual write once the
// create-only precondition is checked.
func (e *Engine) setCreate(cmd *parser.SetCmd) (*types.Table, error) {
	if e.Catalog.GetTable(cmd.TableName) != nil {
		return nil, fmt.Errorf(".set: table %q already exists — .set creates a new table only; "+
			"use .set-or-append to add rows to an existing table", cmd.TableName)
	}
	return e.setOrAppendCore(cmd.TableName, cmd.Query)
}

func (e *Engine) setOrAppend(cmd *parser.SetOrAppendCmd) (*types.Table, error) {
	return e.setOrAppendCore(cmd.TableName, cmd.Query)
}

func (e *Engine) setOrAppendCore(tableName string, query *parser.Query) (*types.Table, error) {
	cmd := &parser.SetOrAppendCmd{TableName: tableName, Query: query}
	return e.setOrAppendImpl(cmd)
}

func (e *Engine) setOrAppendImpl(cmd *parser.SetOrAppendCmd) (*types.Table, error) {
	result, err := e.executeQuery(cmd.Query)
	if err != nil {
		return nil, fmt.Errorf(".set-or-append: evaluating query: %w", err)
	}

	tableDef := e.Catalog.GetTable(cmd.TableName)
	if tableDef == nil {
		// Implicit table creation (.set-or-append into a table that
		// doesn't exist yet) is still real, user-facing table
		// creation -- gets the automatic _TimeReceived column same as
		// an explicit .create table, per the scope's own default (no
		// per-table override possible here, since there's no .create
		// table statement to attach a with(...) clause to at all).
		schema := e.withTimeReceivedColumn(result.Schema, false)
		if err := e.Catalog.CreateTable(cmd.TableName, schema); err != nil {
			return nil, err
		}
		if err := e.persistDiscoverySchema(cmd.TableName, schema); err != nil {
			return nil, err
		}
		tableDef = e.Catalog.GetTable(cmd.TableName)
	} else if err := schemaAppendCompatible(&tableDef.Schema, &result.Schema); err != nil {
		return nil, fmt.Errorf(".set-or-append into %q: %w", cmd.TableName, err)
	}

	if len(result.Rows) == 0 {
		return nil, fmt.Errorf(".set-or-append: query returned no rows")
	}

	// Reorder result rows to the TABLE's column order (the query may
	// have declared columns in a different order — matched by name,
	// not position).
	colIdx := make([]int, len(tableDef.Schema.Columns))
	for i, tc := range tableDef.Schema.Columns {
		colIdx[i] = result.Schema.ColumnIndex(tc.Name)
	}
	rows := make([]types.Row, len(result.Rows))
	for ri, r := range result.Rows {
		row := make(types.Row, len(tableDef.Schema.Columns))
		for i, ci := range colIdx {
			if ci >= 0 {
				row[i] = r[ci]
			}
		}
		rows[ri] = row
	}

	extID, err := e.flushBatch(cmd.TableName, tableDef, rows)
	if err != nil {
		return nil, fmt.Errorf(".set-or-append: %w", err)
	}

	// Trigger materialized-view maintenance with EXACTLY the delta
	// just written — see mv_maintenance.go's own doc comment for the
	// full design. Deliberately after flushBatch succeeds, not before:
	// a write that fails must never trigger maintenance for data that
	// was never actually committed.
	e.triggerMaterializedViewMaintenance(cmd.TableName, rows, tableDef.Schema)

	out := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "Result", Type: types.TypeString},
		{Name: "ExtentId", Type: types.TypeString},
		{Name: "RowsAppended", Type: types.TypeLong},
	}})
	out.AddRow(types.Row{"OK", extID, int64(len(rows))})
	return out, nil
}

// schemaAppendCompatible requires every target column to be present in
// src by name with a matching type. src may have extra columns
// (ignored) but not fewer.
func schemaAppendCompatible(target, src *types.Schema) error {
	for _, tc := range target.Columns {
		if tc.Name == timeReceivedColumnName {
			// Exempt, deliberately: _TimeReceived (see timereceived.go)
			// is engine-generated at write time, never something an
			// appending query itself produces -- requiring it here
			// would reject every ordinary .set-or-append into any
			// table that has this column at all, which is now most
			// tables by default. setOrAppendImpl's own row-building
			// already leaves this column's position nil for a fresh
			// write regardless of whether src happens to carry a
			// value for it or not.
			continue
		}
		idx := src.ColumnIndex(tc.Name)
		if idx < 0 {
			return fmt.Errorf("query result is missing column %q (table schema requires it)", tc.Name)
		}
		if src.Columns[idx].Type != tc.Type {
			return fmt.Errorf("column %q: query produces type %v, table schema expects %v",
				tc.Name, src.Columns[idx].Type, tc.Type)
		}
	}
	return nil
}
