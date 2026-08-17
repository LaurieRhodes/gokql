package engine

import (
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

// evaluate_test.go — the evaluate plugin-invocation operator and its
// first plugin, bag_unpack. Verified against real ADX's own docs for
// both before implementing; each test below traces to a specific,
// documented behavior, not an assumption about what "should" happen.

func TestEvaluateUnknownPluginClearError(t *testing.T) {
	_, err := runStmt(t, diskEngineEmpty(t), `print x=1 | evaluate nosuchplugin(x)`)
	if err == nil {
		t.Fatal("expected an error for an unregistered plugin name")
	}
}

// TestBagUnpackBasic guards real ADX's own primary worked example
// directly, including the exact expected output values.
func TestBagUnpackBasic(t *testing.T) {
	tbl := queryResult(t, `datatable(d:dynamic) [dynamic({"Name": "John", "Age":20}), dynamic({"Name": "Dave", "Age":40})] | evaluate bag_unpack(d) | sort by Name asc`)
	expectRows(t, tbl, 2)
	nameIdx := tbl.Schema.ColumnIndex("Name")
	ageIdx := tbl.Schema.ColumnIndex("Age")
	if tbl.Schema.ColumnIndex("d") >= 0 {
		t.Error("expected the source column d to be removed from the output")
	}
	if tbl.Rows[0][nameIdx] != "Dave" || tbl.Rows[0][ageIdx] != int64(40) {
		t.Errorf("expected Dave/40, got %v/%v", tbl.Rows[0][nameIdx], tbl.Rows[0][ageIdx])
	}
	if tbl.Rows[1][nameIdx] != "John" || tbl.Rows[1][ageIdx] != int64(20) {
		t.Errorf("expected John/20, got %v/%v", tbl.Rows[1][nameIdx], tbl.Rows[1][ageIdx])
	}
}

func TestBagUnpackOutputColumnPrefix(t *testing.T) {
	tbl := queryResult(t, `datatable(d:dynamic) [dynamic({"Name": "John", "Age":20})] | evaluate bag_unpack(d, "Property_")`)
	if tbl.Schema.ColumnIndex("Property_Name") < 0 || tbl.Schema.ColumnIndex("Property_Age") < 0 {
		t.Fatalf("expected prefixed columns, got: %v", tbl.Schema.Columns)
	}
}

func TestBagUnpackIgnoredProperties(t *testing.T) {
	tbl := queryResult(t, `datatable(d:dynamic) [dynamic({"Name": "John", "Age":20, "Secret":"x"})] | evaluate bag_unpack(d, ignoredProperties=dynamic(["Secret"]))`)
	if tbl.Schema.ColumnIndex("Secret") >= 0 {
		t.Error("expected Secret to be excluded by ignoredProperties")
	}
	if tbl.Schema.ColumnIndex("Name") < 0 || tbl.Schema.ColumnIndex("Age") < 0 {
		t.Errorf("expected Name and Age to survive, got: %v", tbl.Schema.Columns)
	}
}

func TestBagUnpackColumnsConflictDefaultErrors(t *testing.T) {
	_, err := runStmt(t, diskEngineEmpty(t), `datatable(Name:string, d:dynamic) ["Old", dynamic({"Name": "John", "Age":20})] | evaluate bag_unpack(d)`)
	if err == nil {
		t.Fatal("expected a conflict error with the default columnsConflict='error'")
	}
}

func TestBagUnpackColumnsConflictReplaceSource(t *testing.T) {
	tbl := queryResult(t, `datatable(Name:string, d:dynamic) ["Old", dynamic({"Name": "John", "Age":20})] | evaluate bag_unpack(d, columnsConflict="replace_source")`)
	nameIdx := tbl.Schema.ColumnIndex("Name")
	if tbl.Rows[0][nameIdx] != "John" {
		t.Errorf("expected replace_source to use the unpacked value 'John', got %v", tbl.Rows[0][nameIdx])
	}
	// Exactly one Name column, not two.
	count := 0
	for _, c := range tbl.Schema.Columns {
		if c.Name == "Name" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one Name column after replace_source, got %d", count)
	}
}

func TestBagUnpackColumnsConflictKeepSource(t *testing.T) {
	tbl := queryResult(t, `datatable(Name:string, d:dynamic) ["Old", dynamic({"Name": "John", "Age":20})] | evaluate bag_unpack(d, columnsConflict="keep_source")`)
	nameIdx := tbl.Schema.ColumnIndex("Name")
	if tbl.Rows[0][nameIdx] != "Old" {
		t.Errorf("expected keep_source to keep the original value 'Old', got %v", tbl.Rows[0][nameIdx])
	}
	ageIdx := tbl.Schema.ColumnIndex("Age")
	if ageIdx < 0 || tbl.Rows[0][ageIdx] != int64(20) {
		t.Errorf("expected the non-conflicting Age key to still unpack, got %v", tbl.Rows[0][ageIdx])
	}
}

// TestBagUnpackDifferingKeySets guards the union-of-columns behavior
// across rows whose bags don't share the same keys — a row missing a
// given key gets null for that column, not an error.
func TestBagUnpackDifferingKeySets(t *testing.T) {
	tbl := queryResult(t, `datatable(d:dynamic) [dynamic({"a":1}), dynamic({"b":2})] | evaluate bag_unpack(d) | sort by a asc`)
	aIdx := tbl.Schema.ColumnIndex("a")
	bIdx := tbl.Schema.ColumnIndex("b")
	if aIdx < 0 || bIdx < 0 {
		t.Fatalf("expected both a and b columns (union across rows), got: %v", tbl.Schema.Columns)
	}
}

// TestBagUnpackOutputSchemaOverridesInferredType guards the
// ": (Name: type, ...)" suffix forcing an exact output type rather
// than whatever gets inferred from the data.
func TestBagUnpackOutputSchemaOverridesInferredType(t *testing.T) {
	tbl := queryResult(t, `datatable(d:dynamic) [dynamic({"Age":20})] | evaluate bag_unpack(d) : (Age: real)`)
	ageIdx := tbl.Schema.ColumnIndex("Age")
	if tbl.Schema.Columns[ageIdx].Type != types.TypeReal {
		t.Errorf("expected Age forced to real by the OutputSchema suffix, got %s", tbl.Schema.Columns[ageIdx].Type)
	}
}

// TestBagUnpackNestedValueTypesDynamic guards that a nested
// object/array property unpacks as TypeDynamic, not TypeString --
// jsonToKQLValue itself returns a Go string for both (this engine's
// own on-the-wire dynamic representation), so this specifically
// exercises checking the ORIGINAL JSON value's shape before that
// conversion collapses the distinction.
func TestBagUnpackNestedValueTypesDynamic(t *testing.T) {
	tbl := queryResult(t, `datatable(d:dynamic) [dynamic({"tags":["a","b"]})] | evaluate bag_unpack(d) | getschema`)
	nameIdx := tbl.Schema.ColumnIndex("ColumnName")
	typeIdx := tbl.Schema.ColumnIndex("ColumnType")
	found := false
	for _, row := range tbl.Rows {
		if row[nameIdx] == "tags" {
			found = true
			if row[typeIdx] != "dynamic" {
				t.Errorf("expected tags typed as dynamic, got %v", row[typeIdx])
			}
		}
	}
	if !found {
		t.Fatal("expected a tags column in the unpacked output")
	}
}

// --- datatable(dynamic) parsing bugs found and fixed alongside evaluate/bag_unpack ---

// TestDataTableDynamicObjectLiteralValueSplit directly guards the
// first of two real, live bugs found while testing bag_unpack against
// real data: splitDataTableValues (parser.go) tracked [...] nesting
// (for a dynamic array value) but not {...} or (...) at all, so the
// comma INSIDE a dynamic({"a":1,"b":2}) object literal was
// misread as a datatable value separator, silently misaligning every
// value from that point on.
func TestDataTableDynamicObjectLiteralValueSplit(t *testing.T) {
	tbl := queryResult(t, `datatable(d:dynamic) [dynamic({"Name": "John", "Age":20}), dynamic({"Name": "Dave", "Age":40})] | count`)
	expectCell(t, tbl, 0, 0, "2") // 2 rows, not 4 from a silent mis-split
}

// TestDataTableDynamicFunctionCallEvaluated directly guards the
// second, deeper bug: convertDataTableValue delegated to
// types.ParseValue for EVERY type including TypeDynamic, which is
// correct for its other caller (CSV ingest, where a dynamic field's
// raw text genuinely already IS bare JSON) but wrong here — a
// datatable dynamic-typed token can be a real KQL expression
// (dynamic({...})), which ParseValue passed through completely
// unevaluated, literal "dynamic(...)" text and all.
func TestDataTableDynamicFunctionCallEvaluated(t *testing.T) {
	tbl := queryResult(t, `datatable(d:dynamic) [dynamic({"a":1})] | project d`)
	dIdx := tbl.Schema.ColumnIndex("d")
	got, ok := tbl.Rows[0][dIdx].(string)
	if !ok {
		t.Fatalf("expected d to hold a JSON string, got %T: %v", tbl.Rows[0][dIdx], tbl.Rows[0][dIdx])
	}
	if got == `dynamic({"a":1})` {
		t.Fatalf("d still holds the literal, unevaluated text %q instead of the evaluated JSON value", got)
	}
}
