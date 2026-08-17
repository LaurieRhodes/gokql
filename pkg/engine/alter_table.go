package engine

// alter_table.go — .alter-merge table T (col1:type1, ...). Verified
// against real ADX's own .alter-merge table docs before building
// this: adds new columns to an EXISTING table's schema, appended at
// the end; existing data is never modified or deleted (matching this
// storage layer's own, already-built support for reading a column
// that doesn't exist in a given extent file — storage.go's ScanExtent
// already, deliberately, leaves such a cell null, labeled "schema
// evolution" in its own comment, confirmed directly before relying on
// it rather than assumed); a column that already exists but is given
// a DIFFERENT type here is a hard error, not silently accepted.
//
// Built directly to unblock a real, concrete need: retrofitting the
// automatic _TimeReceived column (timereceived.go) onto tables
// created before that feature existed -- .create table only adds it
// going forward, never retroactively, so an already-populated table
// (like a different model's, Kimi's, existing girsu-paper tables) had
// no way to gain the column at all without this.
//
// The actual column-adding mechanics reuse catalog.CreateMergeTable
// unchanged -- already proven, tested code behind .create-merge table
// (parser.CreateMergeTableCmd) -- rather than duplicating that logic.
// This command adds exactly two things that shared helper doesn't
// itself enforce, since .create-merge table's own semantics differ
// deliberately: the target table must already exist (.alter-merge
// table never creates one, unlike .create-merge table), and a
// same-named column given a conflicting type is rejected outright
// (silently ignored, per CreateMergeTable's own "add columns that
// don't exist yet" comment) rather than left silently unenforced.

import (
	"fmt"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func (e *Engine) applyAlterMergeTable(cmd *parser.AlterMergeTableCmd) (*types.Table, error) {
	tableDef := e.Catalog.GetTable(cmd.TableName)
	if tableDef == nil {
		return nil, fmt.Errorf("alter-merge table %q: table does not exist (.alter-merge table never creates one — use .create-merge table for that)", cmd.TableName)
	}

	for _, newCol := range cmd.NewColumns.Columns {
		if existingIdx := tableDef.Schema.ColumnIndex(newCol.Name); existingIdx >= 0 {
			existingType := tableDef.Schema.Columns[existingIdx].Type
			if existingType != newCol.Type {
				return nil, fmt.Errorf("alter-merge table %q: column %q already exists with type %s — cannot alter to %s (use .alter column instead, matching real ADX's own restriction)",
					cmd.TableName, newCol.Name, existingType, newCol.Type)
			}
		}
	}

	if err := e.Catalog.CreateMergeTable(cmd.TableName, cmd.NewColumns); err != nil {
		return nil, fmt.Errorf("alter-merge table %q: %w", cmd.TableName, err)
	}

	updated := e.Catalog.GetTable(cmd.TableName)
	if err := e.persistDiscoverySchema(cmd.TableName, updated.Schema); err != nil {
		return nil, fmt.Errorf("alter-merge table %q: %w", cmd.TableName, err)
	}

	return okResult(fmt.Sprintf("OK (%d columns)", len(updated.Schema.Columns))), nil
}
