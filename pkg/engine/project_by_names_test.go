package engine

import "testing"

// project_by_names_test.go — the project-by-names operator. Every
// test below is checked against a real, worked example from real
// ADX's own docs (project-by-names-operator.md) with exact expected
// values, not just "does it run".

// TestProjectByNamesExactColumns guards the docs' own primary worked
// example: quoted exact column names, reordered.
func TestProjectByNamesExactColumns(t *testing.T) {
	result := queryResult(t, `datatable(Name:string, Age:int, City:string, Country:string)
		["Peter", 39, "New York", "USA"]
		| project-by-names "Name", "City"`)
	if result.RowCount() != 1 {
		t.Fatalf("expected 1 row, got %d", result.RowCount())
	}
	if len(result.Schema.Columns) != 2 || result.Schema.Columns[0].Name != "Name" || result.Schema.Columns[1].Name != "City" {
		t.Fatalf("expected columns [Name, City] in that order, got %+v", result.Schema.Columns)
	}
	nameIdx := result.Schema.ColumnIndex("Name")
	cityIdx := result.Schema.ColumnIndex("City")
	if result.Rows[0][nameIdx] != "Peter" || result.Rows[0][cityIdx] != "New York" {
		t.Errorf("got Name=%v City=%v, want Peter/New York", result.Rows[0][nameIdx], result.Rows[0][cityIdx])
	}
}

// TestProjectByNamesDynamicArray guards the docs' own dynamic-array
// worked example.
func TestProjectByNamesDynamicArray(t *testing.T) {
	result := queryResult(t, `datatable(Name:string, Age:int, City:string, Country:string)
		["Peter", 39, "New York", "USA"]
		| project-by-names dynamic(["Name", "Country"])`)
	if len(result.Schema.Columns) != 2 || result.Schema.Columns[0].Name != "Name" || result.Schema.Columns[1].Name != "Country" {
		t.Fatalf("expected columns [Name, Country] in that order, got %+v", result.Schema.Columns)
	}
	nameIdx := result.Schema.ColumnIndex("Name")
	countryIdx := result.Schema.ColumnIndex("Country")
	if result.Rows[0][nameIdx] != "Peter" || result.Rows[0][countryIdx] != "USA" {
		t.Errorf("got Name=%v Country=%v, want Peter/USA", result.Rows[0][nameIdx], result.Rows[0][countryIdx])
	}
}

// TestProjectByNamesWildcard guards the docs' own wildcard-pattern
// worked example: "C*" matches City and Country, in the INPUT
// table's own column order (City before Country), matching the docs'
// own expected output.
func TestProjectByNamesWildcard(t *testing.T) {
	result := queryResult(t, `datatable(Name:string, Age:int, City:string, Country:string)
		["Peter", 39, "New York", "USA"]
		| project-by-names "C*"`)
	if len(result.Schema.Columns) != 2 || result.Schema.Columns[0].Name != "City" || result.Schema.Columns[1].Name != "Country" {
		t.Fatalf("expected columns [City, Country] in that order, got %+v", result.Schema.Columns)
	}
	cityIdx := result.Schema.ColumnIndex("City")
	countryIdx := result.Schema.ColumnIndex("Country")
	if result.Rows[0][cityIdx] != "New York" || result.Rows[0][countryIdx] != "USA" {
		t.Errorf("got City=%v Country=%v, want New York/USA", result.Rows[0][cityIdx], result.Rows[0][countryIdx])
	}
}

// TestProjectByNamesColumnNamesOfAndParamReference guards the docs'
// own most complex worked example: column_names_of(Source) combined
// with a dynamic-array-typed stored-function parameter, in a
// lookup+project-by-names pattern. Uses .create-or-alter function
// (this engine's own mechanism for a tabular-parameter, pipeline-body
// function) rather than `let ... { }` + `invoke` (real ADX's own
// worked example's exact syntax) since neither tabular let-lambdas
// nor invoke are implemented in this engine -- both genuinely
// separate, pre-existing scope gaps unrelated to project-by-names
// itself, found while adapting this test.
func TestProjectByNamesColumnNamesOfAndParamReference(t *testing.T) {
	eng := diskEngineEmpty(t)
	diskExec(t, eng, `.create table LookupTable (Name:string, Age:int, City:string, Country:string)`)
	diskExec(t, eng, `.ingest inline into table LookupTable <| Peter,39,New York,USA`)
	diskExec(t, eng, `.create-or-alter function LookupColumns(Source:(Name:string), lookup_columns: dynamic) {
		Source
		| lookup LookupTable on Name
		| project-by-names column_names_of(Source), lookup_columns
	}`)

	result := diskExec(t, eng, `LookupColumns((datatable(Name:string, Data:string) ["Peter", "Source-data"]), dynamic(["Country"]))`)
	if len(result.Schema.Columns) != 3 {
		t.Fatalf("expected 3 columns (Name, Data, Country), got %+v", result.Schema.Columns)
	}
	wantOrder := []string{"Name", "Data", "Country"}
	for i, w := range wantOrder {
		if result.Schema.Columns[i].Name != w {
			t.Errorf("column %d = %q, want %q", i, result.Schema.Columns[i].Name, w)
		}
	}
	countryIdx := result.Schema.ColumnIndex("Country")
	if result.Rows[0][countryIdx] != "USA" {
		t.Errorf("Country = %v, want USA", result.Rows[0][countryIdx])
	}

	// "Ignore nonexisting columns" worked example: a dynamic array
	// naming a real column plus a nonexistent one must silently drop
	// the nonexistent one, not error.
	result2 := diskExec(t, eng, `LookupColumns((datatable(Name:string, Data:string) ["Peter", "Source-data"]), dynamic(["Country", "NonExistent"]))`)
	if len(result2.Schema.Columns) != 3 {
		t.Fatalf("expected 3 columns (NonExistent silently ignored), got %+v", result2.Schema.Columns)
	}
}

// TestProjectByNamesIgnoresNonMatchingName confirms a column name
// with no match in the input schema is safely ignored, not an error
// — "Column names that don't match any existing column are safely
// ignored" per real ADX docs.
func TestProjectByNamesIgnoresNonMatchingName(t *testing.T) {
	result := queryResult(t, `datatable(Name:string)["Peter"] | project-by-names "Name", "DoesNotExist"`)
	if len(result.Schema.Columns) != 1 || result.Schema.Columns[0].Name != "Name" {
		t.Fatalf("expected only [Name], got %+v", result.Schema.Columns)
	}
}

