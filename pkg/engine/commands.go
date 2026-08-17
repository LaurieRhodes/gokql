package engine

import (
	"fmt"
	"sort"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// --- Management Command Results ---

func (e *Engine) showTables() (*types.Table, error) {
	result := types.NewTable("", types.Schema{
		Columns: []types.Column{
			{Name: "TableName", Type: types.TypeString},
			{Name: "Columns", Type: types.TypeLong},
			{Name: "Rows", Type: types.TypeLong},
			{Name: "Extents", Type: types.TypeLong},
		},
	})

	names := e.Catalog.ListTables()
	sort.Strings(names)

	for _, name := range names {
		table := e.Catalog.GetTable(name)
		rowCount, _ := e.Catalog.TableRowCount(name)
		result.AddRow(types.Row{
			name,
			int64(len(table.Schema.Columns)),
			rowCount,
			int64(len(table.Extents)),
		})
	}
	return result, nil
}

func (e *Engine) showTableExtents(tableName string) (*types.Table, error) {
	table := e.Catalog.GetTable(tableName)
	if table == nil {
		return nil, fmt.Errorf("table %q not found", tableName)
	}

	result := types.NewTable("", types.Schema{
		Columns: []types.Column{
			{Name: "ExtentId", Type: types.TypeString},
			{Name: "RowCount", Type: types.TypeLong},
			{Name: "SizeBytes", Type: types.TypeLong},
			{Name: "CreatedAt", Type: types.TypeDatetime},
			{Name: "FilePath", Type: types.TypeString},
		},
	})

	for _, ext := range table.Extents {
		result.AddRow(types.Row{
			ext.ID,
			ext.RowCount,
			ext.SizeBytes,
			ext.CreatedAt.UnixNano(),
			ext.FilePath,
		})
	}
	return result, nil
}

func (e *Engine) showDatabase() (*types.Table, error) {
	result := types.NewTable("", types.Schema{
		Columns: []types.Column{
			{Name: "DatabaseName", Type: types.TypeString},
			{Name: "Tables", Type: types.TypeLong},
			{Name: "Path", Type: types.TypeString},
		},
	})

	result.AddRow(types.Row{
		e.Catalog.Database.Name,
		int64(len(e.Catalog.Database.Tables)),
		e.Catalog.DatabasePath(),
	})
	return result, nil
}
