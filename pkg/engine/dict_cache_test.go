package engine

import "testing"

// The _Dictionaries cache is reused only while its files are unchanged:
// a write adding dictionary values, by this engine or another, is seen
// by the next read of any column.
func TestDictionaryEntriesCache(t *testing.T) {
	dir := t.TempDir()
	a := discoverEngine(t, dir)
	diskExec(t, a, `.create table D (K: string, V: string)`)
	// Repetitive writes, so both columns are shared-dictionary encoded.
	diskExec(t, a, `.set-or-append D <| datatable(K:string, V:string)["k1","v1","k2","v2","k1","v1","k2","v2","k1","v1","k2","v2"]`)
	expectCell(t, diskQuery(t, a, `_Dictionaries | where TableName == "D" and ColumnName == "K" | count`), 0, 0, "3") // "", k1, k2
	expectCell(t, diskQuery(t, a, `D | summarize c = count() by K | order by K asc | project K`), 1, 0, "k2")

	// Same engine, new values; then a column this engine has not loaded.
	diskExec(t, a, `.set-or-append D <| datatable(K:string, V:string)["k9","v9","k9","v9","k9","v9","k1","v1"]`)
	expectCell(t, diskQuery(t, a, `D | where K == "k9" | summarize c = count()`), 0, 0, "3")
	expectCell(t, diskQuery(t, a, `D | summarize c = dcount(V)`), 0, 0, "3")

	// Another engine writes; a fresh engine sees both writers' values.
	b := discoverEngine(t, dir)
	diskExec(t, b, `.set-or-append D <| datatable(K:string, V:string)["k10","v10","k10","v10","k10","v10"]`)
	c := discoverEngine(t, dir)
	expectCell(t, diskQuery(t, c, `_Dictionaries | where TableName == "D" and ColumnName == "K" | count`), 0, 0, "5")
	expectCell(t, diskQuery(t, c, `D | summarize c = dcount(K)`), 0, 0, "4")
	expectCell(t, diskQuery(t, c, `D | where V == "v10" | summarize c = count()`), 0, 0, "3")
	// And the first engine, still alive, after the other's write.
	expectCell(t, diskQuery(t, a, `D | where K == "k10" | summarize c = count()`), 0, 0, "3")
}
