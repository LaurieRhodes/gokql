// file_parquet.go implements the parquet() table-valued function for reading
// Apache Parquet files directly as KQL query sources.
//
// Parquet is the standard columnar format for analytics exports from Sentinel,
// ADX, Databricks, Spark, and most data pipeline tools.
//
// Usage:
//   parquet("data.parquet") | where Region == "APAC" | summarize count() by Country

package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	parquet "github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

func readParquetFile(path string) (*types.Table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}

	pf, err := parquet.OpenFile(f, stat.Size())
	if err != nil {
		return nil, fmt.Errorf("open parquet: %w", err)
	}

	schema := pf.Schema()
	fields := schema.Fields()

	// Build leaf column info: we flatten nested schemas into dotted paths.
	var leaves []leafCol
	flattenFields(fields, "", &leaves, 0)

	if len(leaves) == 0 {
		name := baseNameNoExt(path)
		return types.NewTable(name, types.Schema{}), nil
	}

	// Build KQL schema
	kqlSchema := types.Schema{
		Columns: make([]types.Column, len(leaves)),
	}
	for i, leaf := range leaves {
		kqlSchema.Columns[i] = types.Column{
			Name: leaf.path,
			Type: leaf.kqlType,
		}
	}

	name := baseNameNoExt(path)
	table := types.NewTable(name, kqlSchema)

	// Read all row groups
	for _, rg := range pf.RowGroups() {
		rows := make([]parquet.Row, 512)
		reader := rg.Rows()
		defer reader.Close()

		for {
			n, err := reader.ReadRows(rows)
			if n == 0 && err != nil {
				break
			}
			for i := 0; i < n; i++ {
				kqlRow := parquetRowToKQL(rows[i], leaves)
				table.Rows = append(table.Rows, kqlRow)
			}
			if err != nil {
				break
			}
		}
	}

	return table, nil
}

// flattenFields walks the parquet schema tree and collects leaf columns.
// Nested groups produce dotted paths (e.g. "address.city").
type leafCol struct {
	path    string
	kqlType types.KQLType
	colIdx  int
}

func flattenFields(fields []parquet.Field, prefix string, leaves *[]leafCol, colIdx int) int {
	for _, field := range fields {
		name := field.Name()
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}

		if field.Leaf() {
			kt := parquetTypeToKQL(field)
			*leaves = append(*leaves, leafCol{
				path:    path,
				kqlType: kt,
				colIdx:  colIdx,
			})
			colIdx++
		} else {
			// Recurse into group
			colIdx = flattenFields(field.Fields(), path, leaves, colIdx)
		}
	}
	return colIdx
}

// parquetTypeToKQL maps a parquet leaf node type to a KQL type.
func parquetTypeToKQL(node parquet.Node) types.KQLType {
	typ := node.Type()
	lt := typ.LogicalType()

	// Check logical types first (they carry semantic meaning)
	if lt != nil {
		if lt.UTF8 != nil {
			return types.TypeString
		}
		if lt.Timestamp != nil {
			return types.TypeDatetime
		}
		if lt.Date != nil {
			return types.TypeDatetime
		}
		if lt.Time != nil {
			return types.TypeString // KQL has no time-only type
		}
		if lt.Integer != nil {
			return types.TypeLong
		}
		if lt.Decimal != nil {
			return types.TypeReal
		}
		if lt.Enum != nil {
			return types.TypeString
		}
		if lt.UUID != nil {
			return types.TypeGUID
		}
		if lt.Json != nil || lt.Bson != nil {
			return types.TypeDynamic
		}
		if lt.List != nil || lt.Map != nil {
			return types.TypeDynamic
		}
	}

	// Fall back to physical type
	switch typ.Kind() {
	case parquet.Boolean:
		return types.TypeBool
	case parquet.Int32:
		return types.TypeInt
	case parquet.Int64:
		return types.TypeLong
	case parquet.Float:
		return types.TypeReal
	case parquet.Double:
		return types.TypeReal
	case parquet.ByteArray, parquet.FixedLenByteArray:
		return types.TypeString
	case parquet.Int96:
		// Int96 is a legacy timestamp format
		return types.TypeDatetime
	default:
		return types.TypeString
	}
}

// parquetRowToKQL converts a parquet Row ([]Value) to a KQL row.
func parquetRowToKQL(row parquet.Row, leaves []leafCol) types.Row {
	kqlRow := make(types.Row, len(leaves))

	for _, val := range row {
		colIdx := val.Column()
		if colIdx < 0 || colIdx >= len(leaves) {
			continue
		}
		kqlRow[colIdx] = parquetValueToKQL(val, leaves[colIdx].kqlType)
	}

	return kqlRow
}

// parquetValueToKQL converts a single parquet Value to a KQL value.
func parquetValueToKQL(v parquet.Value, kt types.KQLType) interface{} {
	if v.IsNull() {
		return nil
	}

	switch kt {
	case types.TypeBool:
		return v.Boolean()

	case types.TypeInt:
		return int64(v.Int32())

	case types.TypeLong:
		// Handle both Int32 and Int64 physical types
		switch v.Kind() {
		case parquet.Int32:
			return int64(v.Int32())
		default:
			return v.Int64()
		}

	case types.TypeReal:
		switch v.Kind() {
		case parquet.Float:
			return float64(v.Float())
		case parquet.Double:
			return v.Double()
		case parquet.Int32:
			return float64(v.Int32())
		case parquet.Int64:
			return float64(v.Int64())
		default:
			return v.Double()
		}

	case types.TypeDatetime:
		return parquetToDatetime(v)

	case types.TypeGUID:
		return string(v.ByteArray())

	case types.TypeDynamic:
		return string(v.ByteArray())

	default: // TypeString
		switch v.Kind() {
		case parquet.ByteArray, parquet.FixedLenByteArray:
			return string(v.ByteArray())
		case parquet.Int32:
			return fmt.Sprintf("%d", v.Int32())
		case parquet.Int64:
			return fmt.Sprintf("%d", v.Int64())
		case parquet.Double:
			return fmt.Sprintf("%g", v.Double())
		case parquet.Float:
			return fmt.Sprintf("%g", v.Float())
		case parquet.Boolean:
			if v.Boolean() {
				return "true"
			}
			return "false"
		default:
			return string(v.ByteArray())
		}
	}
}

// parquetToDatetime converts a parquet timestamp value to time.Time.
func parquetToDatetime(v parquet.Value) interface{} {
	switch v.Kind() {
	case parquet.Int64:
		// Could be millis or micros since epoch
		n := v.Int64()
		if n > 1e15 {
			// Microseconds
			return time.Unix(n/1e6, (n%1e6)*1e3).UTC()
		} else if n > 1e12 {
			// Milliseconds
			return time.Unix(n/1e3, (n%1e3)*1e6).UTC()
		}
		// Seconds
		return time.Unix(n, 0).UTC()
	case parquet.Int96:
		// Legacy: 12 bytes = 8 bytes nanoseconds within day + 4 bytes Julian day
		b := v.ByteArray()
		if len(b) == 12 {
			nanos := int64(b[0]) | int64(b[1])<<8 | int64(b[2])<<16 | int64(b[3])<<24 |
				int64(b[4])<<32 | int64(b[5])<<40 | int64(b[6])<<48 | int64(b[7])<<56
			julianDay := int32(b[8]) | int32(b[9])<<8 | int32(b[10])<<16 | int32(b[11])<<24
			// Julian day to Unix: Julian epoch is 4713 BC January 1
			// Unix epoch (1970-01-01) = Julian day 2440588
			unixDays := int64(julianDay) - 2440588
			return time.Unix(unixDays*86400+nanos/1e9, nanos%1e9).UTC()
		}
		return nil
	case parquet.Int32:
		// Days since epoch (Date type)
		return time.Unix(int64(v.Int32())*86400, 0).UTC()
	case parquet.ByteArray:
		// String timestamp
		s := string(v.ByteArray())
		for _, f := range []string{
			time.RFC3339, time.RFC3339Nano,
			"2006-01-02T15:04:05Z", "2006-01-02T15:04:05",
			"2006-01-02 15:04:05", "2006-01-02",
		} {
			if t, err := time.Parse(f, s); err == nil {
				return t.UTC()
			}
		}
		return s
	default:
		return nil
	}
}

func baseNameNoExt(path string) string {
	name := filepath.Base(path)
	if ext := filepath.Ext(name); ext != "" {
		name = name[:len(name)-len(ext)]
	}
	return name
}

// parquetLogicalTypeInfo returns a descriptive string for the logical type (for debugging).
func parquetLogicalTypeInfo(lt *format.LogicalType) string {
	if lt == nil {
		return "none"
	}
	var parts []string
	if lt.UTF8 != nil {
		parts = append(parts, "UTF8")
	}
	if lt.Timestamp != nil {
		parts = append(parts, "Timestamp")
	}
	if lt.Date != nil {
		parts = append(parts, "Date")
	}
	if lt.Integer != nil {
		parts = append(parts, fmt.Sprintf("Int(%d,%v)", lt.Integer.BitWidth, lt.Integer.IsSigned))
	}
	if lt.Decimal != nil {
		parts = append(parts, fmt.Sprintf("Decimal(%d,%d)", lt.Decimal.Precision, lt.Decimal.Scale))
	}
	if lt.UUID != nil {
		parts = append(parts, "UUID")
	}
	if lt.Json != nil {
		parts = append(parts, "JSON")
	}
	if lt.List != nil {
		parts = append(parts, "List")
	}
	if lt.Map != nil {
		parts = append(parts, "Map")
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, ",")
}
