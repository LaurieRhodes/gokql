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
	"os"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
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
	if err := e.checkDirectWrite(cmd.TableName, cmd.Mode); err != nil {
		return nil, err
	}
	p, err := e.prepareAppend(cmd)
	if err != nil {
		return nil, err
	}
	if p.replace {
		return e.commitAppend(p)
	}
	return e.commitWithPolicies([]*pendingAppend{p})
}

func (e *Engine) setOrAppendCore(tableName string, query *parser.Query) (*types.Table, error) {
	return e.setOrAppend(&parser.SetOrAppendCmd{TableName: tableName, Query: query})
}

// pendingAppend is an evaluated .set-or-append / .append /
// .set-or-replace: everything checked, nothing written yet.
type pendingAppend struct {
	cmdName  string
	table    string
	create   bool           // create the table with schema
	recreate bool           // drop the existing table, then create
	extend   []types.Column // columns to add at the end of the schema
	replace  bool           // supersede the table's current extents
	schema   types.Schema   // the table's schema after the write
	rows     []types.Row    // in schema order
}

// prepareAppend evaluates the command's query and checks it against
// the target table, writing nothing. A query column the table lacks is
// an error unless with (extend_schema=true) adds it; a missing column
// or a type mismatch is always an error.
func (e *Engine) prepareAppend(cmd *parser.SetOrAppendCmd) (*pendingAppend, error) {
	mode := cmd.Mode
	if mode == "" {
		mode = "set-or-append"
	}
	result, err := e.executeQuery(cmd.Query)
	if err != nil {
		return nil, fmt.Errorf(".%s: evaluating query: %w", mode, err)
	}
	return e.prepareFromResult(cmd, result)
}

// prepareFromResult is prepareAppend for an already evaluated result.
func (e *Engine) prepareFromResult(cmd *parser.SetOrAppendCmd, result *types.Table) (*pendingAppend, error) {
	mode := cmd.Mode
	if mode == "" {
		mode = "set-or-append"
	}
	name := "." + mode
	p := &pendingAppend{cmdName: name, table: cmd.TableName, replace: mode == "set-or-replace"}
	tableDef := e.Catalog.GetTable(cmd.TableName)
	switch {
	case tableDef == nil && mode == "append":
		return nil, fmt.Errorf(".append: table %q does not exist (.set-or-append creates it)", cmd.TableName)
	case tableDef == nil || (p.replace && cmd.Options["recreate_schema"] == "true"):
		// A new table (or a recreated one) takes the query's schema.
		p.create = true
		p.recreate = tableDef != nil
		p.replace = false
		p.schema = e.withTimeReceivedColumn(result.Schema, false)
	default:
		if err := schemaAppendCompatible(&tableDef.Schema, &result.Schema); err != nil {
			return nil, fmt.Errorf("%s into %q: %w", name, cmd.TableName, err)
		}
		var extra []string
		for _, c := range result.Schema.Columns {
			if c.Name != timeReceivedColumnName && tableDef.Schema.ColumnIndex(c.Name) < 0 {
				p.extend = append(p.extend, c)
				extra = append(extra, c.Name)
			}
		}
		if len(extra) > 0 && cmd.Options["extend_schema"] != "true" {
			return nil, fmt.Errorf("%s into %q: the query has column(s) %s that the table does not; "+
				"add with (extend_schema=true) to add them, or project them away", name, cmd.TableName, strings.Join(extra, ", "))
		}
		p.schema = types.Schema{Columns: append(append([]types.Column{}, tableDef.Schema.Columns...), p.extend...)}
	}
	if len(result.Rows) == 0 && !p.replace && !p.create {
		return nil, fmt.Errorf("%s: query returned no rows", name)
	}
	if len(result.Rows) == 0 && p.create && !p.recreate {
		return nil, fmt.Errorf("%s: query returned no rows", name)
	}

	// Reorder result rows to the table's column order (matched by
	// name, not position).
	colIdx := make([]int, len(p.schema.Columns))
	for i, tc := range p.schema.Columns {
		colIdx[i] = result.Schema.ColumnIndex(tc.Name)
	}
	p.rows = make([]types.Row, len(result.Rows))
	for ri, r := range result.Rows {
		row := make(types.Row, len(p.schema.Columns))
		for i, ci := range colIdx {
			if ci >= 0 {
				row[i] = r[ci]
			}
		}
		p.rows[ri] = row
	}
	return p, nil
}

// commitAppend writes a prepared append.
func (e *Engine) commitAppend(p *pendingAppend) (*types.Table, error) {
	if p.recreate {
		if e.Catalog.IsDiscovery() {
			if err := e.dropTableComplete(p.table); err != nil {
				return nil, fmt.Errorf("%s: %w", p.cmdName, err)
			}
		} else if err := e.Catalog.DropTable(p.table); err != nil {
			return nil, fmt.Errorf("%s: %w", p.cmdName, err)
		}
	}
	if p.create {
		if err := e.Catalog.CreateTable(p.table, p.schema); err != nil {
			return nil, err
		}
		if err := e.persistDiscoverySchema(p.table, p.schema); err != nil {
			return nil, err
		}
	} else if len(p.extend) > 0 {
		if err := e.Catalog.CreateMergeTable(p.table, types.Schema{Columns: p.extend}); err != nil {
			return nil, fmt.Errorf("%s: extending %q: %w", p.cmdName, p.table, err)
		}
		if err := e.persistDiscoverySchema(p.table, e.Catalog.GetTable(p.table).Schema); err != nil {
			return nil, err
		}
	}
	tableDef := e.Catalog.GetTable(p.table)
	if tableDef == nil {
		return nil, fmt.Errorf("%s: table %q vanished", p.cmdName, p.table)
	}
	// The catalog decides where new columns go (_TimeReceived stays
	// last): map the prepared rows onto its column order by name.
	if !sameColumnOrder(p.schema, tableDef.Schema) {
		idx := make([]int, len(tableDef.Schema.Columns))
		for i, c := range tableDef.Schema.Columns {
			idx[i] = p.schema.ColumnIndex(c.Name)
		}
		for ri, r := range p.rows {
			row := make(types.Row, len(idx))
			for i, j := range idx {
				if j >= 0 {
					row[i] = r[j]
				}
			}
			p.rows[ri] = row
		}
		p.schema = tableDef.Schema
	}

	out := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "Result", Type: types.TypeString},
		{Name: "ExtentId", Type: types.TypeString},
		{Name: "RowsAppended", Type: types.TypeLong},
	}})

	if p.replace {
		ids, superseded, err := e.replaceTableRows(p.table, tableDef, p.rows)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.cmdName, err)
		}
		out.AddRow(types.Row{fmt.Sprintf("OK (replaced %d extents)", superseded), strings.Join(ids, ","), int64(len(p.rows))})
		e.recomputeViewsOn(p.table)
		return out, nil
	}

	if len(p.rows) == 0 {
		out.AddRow(types.Row{"OK", "", int64(0)})
		return out, nil
	}
	extID, err := e.flushBatch(p.table, tableDef, p.rows)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.cmdName, err)
	}

	// Trigger materialized-view maintenance with EXACTLY the delta
	// just written — see mv_maintenance.go's own doc comment for the
	// full design. Deliberately after flushBatch succeeds, not before:
	// a write that fails must never trigger maintenance for data that
	// was never actually committed.
	e.triggerMaterializedViewMaintenance(p.table, p.rows, tableDef.Schema)

	out.AddRow(types.Row{"OK", extID, int64(len(p.rows))})
	return out, nil
}

// replaceTableRows makes rows the table's entire content: on disk, new
// extents are written and the old ones superseded (as .compact does);
// in memory, the table is dropped and recreated.
func (e *Engine) replaceTableRows(table string, tableDef *catalog.Table, rows []types.Row) ([]string, int, error) {
	if !e.Catalog.IsDiscovery() {
		schema := tableDef.Schema
		n := len(tableDef.Extents)
		if err := e.Catalog.DropTable(table); err != nil {
			return nil, 0, err
		}
		if err := e.Catalog.CreateTable(table, schema); err != nil {
			return nil, 0, err
		}
		if len(rows) == 0 {
			return nil, n, nil
		}
		id, err := e.flushBatch(table, e.Catalog.GetTable(table), rows)
		if err != nil {
			return nil, 0, err
		}
		return []string{id}, n, nil
	}
	snapshot := make([]catalog.ExtentEntry, len(tableDef.Extents))
	copy(snapshot, tableDef.Extents)
	shells, err := findActiveShellFiles(e.Catalog.DatabasePath(), table, snapshot)
	if err != nil {
		return nil, 0, err
	}
	ids, superseded, renameErrs, err := e.rewriteExtents(table, tableDef, snapshot, shells, rows)
	if err != nil {
		return nil, 0, err
	}
	if len(renameErrs) > 0 {
		return ids, superseded, fmt.Errorf("new rows written, but %d old extent file(s) could not be superseded and are now "+
			"counted twice: %v", len(renameErrs), renameErrs)
	}
	return ids, superseded, nil
}

// recomputeViewsOn recomputes, synchronously, every materialized view
// over table: after rows are replaced or deleted an incremental merge
// cannot remove them.
func (e *Engine) recomputeViewsOn(table string) {
	mvs, err := e.listMaterializedViews()
	if err != nil {
		return
	}
	for _, mv := range mvs {
		if mv.SourceTable != table {
			continue
		}
		e.waitForMaterializedView(mv.Name)
		stmt, err := parser.Parse(mv.Query)
		if err != nil {
			continue
		}
		if q, ok := stmt.(*parser.Query); ok {
			if err := e.recomputeMaterializedView(mv, q); err != nil && e.Verbose {
				fmt.Fprintf(os.Stderr, "[mv] materialized view %q: recompute failed, view left stale: %v\n", mv.Name, err)
			}
		}
	}
}

// applyDeleteRecords implements .delete table T records <| T | where P:
// only the extents holding matching rows are rewritten, so other rows
// keep their extents and every kept row its original _TimeReceived.
func (e *Engine) applyDeleteRecords(cmd *parser.DeleteRecordsCmd) (*types.Table, error) {
	if !e.Catalog.IsDiscovery() {
		return nil, fmt.Errorf(".delete table %s records needs an on-disk database", cmd.TableName)
	}
	tableDef := e.Catalog.GetTable(cmd.TableName)
	if tableDef == nil {
		return nil, fmt.Errorf(".delete table %s records: table not found", cmd.TableName)
	}
	allCols := make([]string, len(tableDef.Schema.Columns))
	for i, c := range tableDef.Schema.Columns {
		allCols[i] = c.Name
	}
	snapshot := make([]catalog.ExtentEntry, len(tableDef.Extents))
	copy(snapshot, tableDef.Extents)
	var affected []catalog.ExtentEntry
	var kept []types.Row
	deleted := 0
	for _, ext := range snapshot {
		data, err := e.ScanExtent(tableDef.Name, ext.FilePath, &tableDef.Schema, allCols, nil, 0)
		if err != nil {
			return nil, fmt.Errorf(".delete table %s records: reading extent %s: %w", cmd.TableName, ext.ID, err)
		}
		var keep []types.Row
		for _, row := range data.Rows {
			v, err := evalExpr(e, cmd.Where, &tableDef.Schema, row)
			if err != nil {
				return nil, fmt.Errorf(".delete table %s records: %w", cmd.TableName, err)
			}
			if b, ok := v.(bool); ok && b {
				deleted++
				continue
			}
			keep = append(keep, row)
		}
		if len(keep) != len(data.Rows) {
			affected = append(affected, ext)
			kept = append(kept, keep...)
		}
	}
	out := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "Result", Type: types.TypeString},
		{Name: "RecordsMatched", Type: types.TypeLong},
		{Name: "ExtentsRewritten", Type: types.TypeLong},
	}})
	if cmd.WhatIf || deleted == 0 {
		res := "WhatIf"
		if !cmd.WhatIf {
			res = "OK"
		}
		out.AddRow(types.Row{res, int64(deleted), int64(0)})
		return out, nil
	}
	_, superseded, renameErrs, err := e.rewriteExtents(cmd.TableName, tableDef, affected, nil, kept)
	if err != nil {
		return nil, fmt.Errorf(".delete table %s records: %w", cmd.TableName, err)
	}
	if len(renameErrs) > 0 {
		return nil, fmt.Errorf(".delete table %s records: kept rows written, but %d extent file(s) could not be superseded "+
			"and are now counted twice: %v", cmd.TableName, len(renameErrs), renameErrs)
	}
	e.recomputeViewsOn(cmd.TableName)
	out.AddRow(types.Row{"OK", int64(deleted), int64(superseded)})
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

func sameColumnOrder(a, b types.Schema) bool {
	if len(a.Columns) != len(b.Columns) {
		return false
	}
	for i := range a.Columns {
		if a.Columns[i].Name != b.Columns[i].Name {
			return false
		}
	}
	return true
}
