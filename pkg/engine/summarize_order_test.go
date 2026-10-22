package engine

import "testing"

// summarize_order_test.go — ADX output order: group-by columns, then
// aggregates (summarize-operator reference: "summarize EventCount =
// count() by EventType, Length = bin(Duration, 1d)" gives EventType,
// Length, EventCount).
func TestSummarizeGroupByColumnsFirst(t *testing.T) {
	r := queryResult(t, `datatable(EventType:string, Duration:long)
		["Flood", 2, "Flood", 2, "Heat", 3]
		| summarize EventCount = count() by EventType, Length = bin(Duration, 1)
		| sort by EventType asc`)
	assertColumns(t, columnNames(&r.Schema), "EventType", "Length", "EventCount")
	if got := r.Rows[0][2]; got != int64(2) {
		t.Errorf("Flood EventCount = %v, want 2", got)
	}
}

// arg_max(expr, *) by key: Fruit, Version, Color in the arg_max
// reference example.
func TestSummarizeArgMaxStarOrder(t *testing.T) {
	r := queryResult(t, `datatable(Fruit:string, Color:string, Version:long)
		["Apple", "Red", 1, "Apple", "Green", 2]
		| summarize arg_max(Version, *) by Fruit`)
	assertColumns(t, columnNames(&r.Schema), "Fruit", "Version", "Color")
}
