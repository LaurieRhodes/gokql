// file_sources.go implements table-valued functions for reading external files
// directly as query sources: csv(), json(), ndjson().
//
// These let users query files without first ingesting them into a database:
//   csv("logs.csv") | where Status == 500 | count

package engine

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// executeTableFunc reads a file via a table-valued function and applies operators.
func (e *Engine) executeTableFunc(q *parser.Query) (*types.Table, error) {
	tf := q.SourceFunc

	var result *types.Table
	var err error

	switch tf.Name {
	case "csv":
		result, err = readCSVFile(tf.Path)
	case "json":
		result, err = readJSONFile(tf.Path)
	case "ndjson":
		result, err = readNDJSONFile(tf.Path)
	case "parquet":
		result, err = readParquetFile(tf.Path)
	default:
		return nil, fmt.Errorf("unsupported table function %q", tf.Name)
	}
	if err != nil {
		return nil, fmt.Errorf("%s(%q): %w", tf.Name, tf.Path, err)
	}

	// Apply operators
	return e.applyPipeline(result, q.Operators)
}

// --- CSV Reader ---

func readCSVFile(path string) (*types.Table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true

	// Read header
	headers, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}

	// Read all records
	var records [][]string
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			break // Tolerate partial reads
		}
		records = append(records, record)
	}

	// Infer types from sample (up to 100 rows)
	sampleSize := len(records)
	if sampleSize > 100 {
		sampleSize = 100
	}

	colTypes := make([]types.KQLType, len(headers))
	for i := range headers {
		colTypes[i] = inferColumnType(records[:sampleSize], i)
	}

	// Build schema
	schema := types.Schema{
		Columns: make([]types.Column, len(headers)),
	}
	for i, h := range headers {
		schema.Columns[i] = types.Column{
			Name: strings.TrimSpace(h),
			Type: colTypes[i],
		}
	}

	// Build table
	name := filepath.Base(path)
	if ext := filepath.Ext(name); ext != "" {
		name = name[:len(name)-len(ext)]
	}
	table := types.NewTable(name, schema)

	for _, rec := range records {
		row := make(types.Row, len(headers))
		for i, col := range schema.Columns {
			if i < len(rec) {
				row[i] = convertCSVValue(strings.TrimSpace(rec[i]), col.Type)
			}
		}
		table.Rows = append(table.Rows, row)
	}

	return table, nil
}

// inferColumnType examines sample values for a column and picks the best KQL type.
func inferColumnType(records [][]string, colIdx int) types.KQLType {
	if len(records) == 0 {
		return types.TypeString
	}

	hasInt, hasFloat, hasDatetime, hasBool := true, true, true, true
	nonEmpty := 0

	for _, rec := range records {
		if colIdx >= len(rec) {
			continue
		}
		v := strings.TrimSpace(rec[colIdx])
		if v == "" {
			continue
		}
		nonEmpty++

		if hasInt {
			if _, err := strconv.ParseInt(v, 10, 64); err != nil {
				hasInt = false
			}
		}
		if hasFloat && !hasInt {
			if _, err := strconv.ParseFloat(v, 64); err != nil {
				hasFloat = false
			}
		}
		if hasDatetime {
			if !looksLikeDatetime(v) {
				hasDatetime = false
			}
		}
		if hasBool {
			lower := strings.ToLower(v)
			if lower != "true" && lower != "false" {
				hasBool = false
			}
		}
	}

	if nonEmpty == 0 {
		return types.TypeString
	}

	// Priority: datetime > bool > int > float > string
	if hasDatetime && nonEmpty > 0 {
		return types.TypeDatetime
	}
	if hasBool && nonEmpty > 0 {
		return types.TypeBool
	}
	if hasInt && nonEmpty > 0 {
		return types.TypeLong
	}
	if hasFloat && nonEmpty > 0 {
		return types.TypeReal
	}
	return types.TypeString
}

// looksLikeDatetime checks if a string value could be a datetime.
func looksLikeDatetime(s string) bool {
	formats := []string{
		time.RFC3339, time.RFC3339Nano,
		"2006-01-02T15:04:05Z", "2006-01-02T15:04:05",
		"2006-01-02 15:04:05", "2006-01-02",
		"01/02/2006 15:04:05", "01/02/2006",
		"02/01/2006 15:04:05", "02/01/2006",
	}
	for _, f := range formats {
		if _, err := time.Parse(f, s); err == nil {
			return true
		}
	}
	return false
}

// convertCSVValue converts a string to the appropriate Go value for a KQL type.
func convertCSVValue(s string, kt types.KQLType) interface{} {
	if s == "" {
		return nil
	}
	switch kt {
	case types.TypeLong:
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			return v
		}
		return nil
	case types.TypeReal:
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			return v
		}
		return nil
	case types.TypeBool:
		return strings.EqualFold(s, "true")
	case types.TypeDatetime:
		for _, f := range []string{
			time.RFC3339, time.RFC3339Nano,
			"2006-01-02T15:04:05Z", "2006-01-02T15:04:05",
			"2006-01-02 15:04:05", "2006-01-02",
			"01/02/2006 15:04:05", "01/02/2006",
		} {
			if t, err := time.Parse(f, s); err == nil {
				return t.UTC()
			}
		}
		return s // Fall back to string
	default:
		return s
	}
}

// --- JSON Reader ---

func readJSONFile(path string) (*types.Table, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	data = []byte(strings.TrimSpace(string(data)))

	// Try JSON array first
	var records []map[string]interface{}
	if err := json.Unmarshal(data, &records); err != nil {
		// Try single object
		var single map[string]interface{}
		if err2 := json.Unmarshal(data, &single); err2 != nil {
			return nil, fmt.Errorf("not valid JSON array or object: %w", err)
		}
		records = []map[string]interface{}{single}
	}

	return jsonRecordsToTable(path, records)
}

func readNDJSONFile(path string) (*types.Table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var records []map[string]interface{}
	dec := json.NewDecoder(f)
	for {
		var obj map[string]interface{}
		if err := dec.Decode(&obj); err == io.EOF {
			break
		} else if err != nil {
			break // Tolerate partial reads
		}
		records = append(records, obj)
	}

	return jsonRecordsToTable(path, records)
}

func jsonRecordsToTable(path string, records []map[string]interface{}) (*types.Table, error) {
	if len(records) == 0 {
		name := filepath.Base(path)
		return types.NewTable(name, types.Schema{}), nil
	}

	// Discover columns from all records (union of keys)
	colSet := make(map[string]bool)
	var colOrder []string
	for _, rec := range records {
		for k := range rec {
			if !colSet[k] {
				colSet[k] = true
				colOrder = append(colOrder, k)
			}
		}
	}

	// Infer types from values
	colTypes := make(map[string]types.KQLType)
	for _, col := range colOrder {
		colTypes[col] = inferJSONColumnType(records, col)
	}

	// Build schema
	schema := types.Schema{
		Columns: make([]types.Column, len(colOrder)),
	}
	for i, col := range colOrder {
		schema.Columns[i] = types.Column{
			Name: col,
			Type: colTypes[col],
		}
	}

	name := filepath.Base(path)
	if ext := filepath.Ext(name); ext != "" {
		name = name[:len(name)-len(ext)]
	}
	table := types.NewTable(name, schema)

	for _, rec := range records {
		row := make(types.Row, len(colOrder))
		for i, col := range colOrder {
			row[i] = convertJSONValue(rec[col], colTypes[col])
		}
		table.Rows = append(table.Rows, row)
	}

	return table, nil
}

func inferJSONColumnType(records []map[string]interface{}, col string) types.KQLType {
	for _, rec := range records {
		v, ok := rec[col]
		if !ok || v == nil {
			continue
		}
		switch v.(type) {
		case float64:
			// Check if all values are integral
			allInt := true
			for _, r := range records {
				fv, ok := r[col].(float64)
				if !ok {
					continue
				}
				if fv != float64(int64(fv)) {
					allInt = false
					break
				}
			}
			if allInt {
				return types.TypeLong
			}
			return types.TypeReal
		case bool:
			return types.TypeBool
		case string:
			// Check if it's a datetime string
			s := v.(string)
			if looksLikeDatetime(s) {
				return types.TypeDatetime
			}
			return types.TypeString
		case map[string]interface{}, []interface{}:
			return types.TypeDynamic
		}
	}
	return types.TypeString
}

func convertJSONValue(v interface{}, kt types.KQLType) interface{} {
	if v == nil {
		return nil
	}
	switch kt {
	case types.TypeLong:
		if f, ok := v.(float64); ok {
			return int64(f)
		}
		return v
	case types.TypeReal:
		if f, ok := v.(float64); ok {
			return f
		}
		return v
	case types.TypeBool:
		if b, ok := v.(bool); ok {
			return b
		}
		return v
	case types.TypeDatetime:
		if s, ok := v.(string); ok {
			return convertCSVValue(s, types.TypeDatetime)
		}
		return v
	case types.TypeDynamic:
		if data, err := json.Marshal(v); err == nil {
			return string(data)
		}
		return fmt.Sprintf("%v", v)
	default:
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", v)
	}
}
