package engine

import (
	"testing"
)

// searchEngine builds two tables shaped like the reference examples'
// Customers and Products.
func searchEngine(t *testing.T) *Engine {
	t.Helper()
	eng := discoverEngine(t, t.TempDir())
	diskExec(t, eng, `.create table Customers (CustomerKey: long, FirstName: string, LastName: string, CityName: string)`)
	diskExec(t, eng, ".ingest inline into table Customers <| 1,Mason,Green,Ballard\n2,Aaron,Sharma,Bellingham\n3,Riley,Hughes,Burbank\n4,Sara,green,Bellingham")
	diskExec(t, eng, `.create table Products (ProductName: string, ColorName: string, ClassName: string, EventID: int)`)
	diskExec(t, eng, ".ingest inline into table Products <| Contoso MP3 Green,Green,Deluxe,4625\nProseware Scanner Green,Green,Regular,1\nLitware Fan blue,blue,Economy,4624\nLabXPC unit,Grey,Lab,7")
	return eng
}

// The predicate forms of the reference syntax table, by row count.
func TestSearchPredicateForms(t *testing.T) {
	eng := searchEngine(t)
	for q, want := range map[string]int{
		`search "Green"`: 4, // has, case-insensitive
		`search "Green" and ("Deluxe" or "Proseware")`:     2,
		`search in (Products) "Green"`:                     2,
		`search in (P*) "Green"`:                           2, // wildcard table
		`search kind=case_sensitive "green"`:               1,
		`search FirstName:"Aaron" or LastName:"Hughes"`:    2,
		`search CityName=="Bellingham"`:                    2,
		`search "Pros*"`:                                   1, // hasprefix
		`search "*ware"`:                                   2, // hassuffix
		`search "*ontos*"`:                                 1, // contains
		`search "Lab*PC"`:                                  1, // matches regex \bLab.*PC\b
		`search "4625"`:                                    1, // numeric column
		`search *`:                                         8,
		`search "Green" and CustomerKey > 0`:               2, // Products lacks CustomerKey
		`search in (Products) not("Green")`:                2,
		`search ClassName matches regex "^(Deluxe|Lab)$"`:  2,
		`Customers | search "bellingham"`:                  2, // piped
		`Customers | where CustomerKey > 2 | search "*n*"`: 2,
	} {
		if got := len(diskQuery(t, eng, q).Rows); got != want {
			t.Errorf("%s: %d rows, want %d", q, got, want)
		}
	}
}

// Output: $table, then the outer union of the searched tables'
// columns; piped search labels rows with its source table.
func TestSearchOutputShape(t *testing.T) {
	eng := searchEngine(t)
	tbl := diskQuery(t, eng, `search "Hughes"`)
	expectColNames(t, tbl, "$table", "CustomerKey", "FirstName", "LastName", "CityName", "ProductName", "ColorName", "ClassName", "EventID")
	expectCell(t, tbl, 0, 0, "Customers")
	expectColNames(t, diskQuery(t, eng, `search in (Products) "blue"`), "$table", "ProductName", "ColorName", "ClassName", "EventID")
	tbl = diskQuery(t, eng, `Customers | search "Burbank"`)
	expectColNames(t, tbl, "$table", "CustomerKey", "FirstName", "LastName", "CityName")
	expectCell(t, tbl, 0, 0, "Customers")
}

// Same-named columns of different types are unified as union does
// (Kusto.Language: $table, a, b_long, b_string, n, c).
func TestSearchSchemaMatchesKustoLanguage(t *testing.T) {
	eng := discoverEngine(t, t.TempDir())
	diskExec(t, eng, `.create table T1 (a: string, b: long, n: int)`)
	diskExec(t, eng, `.create table T2 (a: string, c: datetime, b: string)`)
	diskExec(t, eng, ".ingest inline into table T1 <| x,1,2")
	diskExec(t, eng, ".ingest inline into table T2 <| x,2020-01-01,y")
	expectColNames(t, diskQuery(t, eng, `search "x"`), "$table", "a", "b_long", "b_string", "n", "c")
}

func TestSearchErrors(t *testing.T) {
	eng := searchEngine(t)
	for _, q := range []string{
		`search kind=sometimes "x"`,
		`Customers | search in (Products) "x"`,
		`search`,
	} {
		if _, err := runStmt(t, eng, q); err == nil {
			t.Errorf("%s: expected an error", q)
		}
	}
}

func TestSortByQuotedIdentifier(t *testing.T) {
	tbl := queryResult(t, `datatable(['a b']:long, c:long)[2, 20, 1, 10] | sort by ['a b'] asc | project c`)
	expectCell(t, tbl, 0, 0, "10")
	expectCell(t, tbl, 1, 0, "20")
}
