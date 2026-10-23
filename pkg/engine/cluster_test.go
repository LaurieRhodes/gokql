package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// clusterScopes: a local scope (table Loc) with one alias "mstar"
// (table Ent) and another cluster "lab" (primary scope with table Far).
func clusterScopes(t *testing.T) (*Engine, string) {
	t.Helper()
	local, remote, other := t.TempDir(), t.TempDir(), t.TempDir()
	diskExec(t, discoverEngine(t, remote), `.set-or-append Ent <| datatable(Name:string)["Enki has Kusto"]`)
	diskExec(t, discoverEngine(t, other), `.set-or-append Far <| datatable(Name:string)["far Kusto"]`)
	diskExec(t, discoverEngine(t, local), `.set-or-append Loc <| datatable(Name:string)["local Kusto"]`)
	cfg := map[string]interface{}{
		"cluster":   "odm",
		"principal": "laurie",
		"aliases":   []map[string]string{{"alias": "mstar", "path": remote}},
		"clusters":  []map[string]string{{"alias": "lab", "path": other}},
	}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(local, federationConfigFileName), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return discoverEngine(t, local), filepath.Base(other)
}

func TestClusterQueries(t *testing.T) {
	eng, otherDB := clusterScopes(t)
	local := eng.localDatabaseName()
	expectWrRows(t, eng, `union withsource=T database('*').* | order by T asc`,
		`database("`+local+`").Loc | local Kusto`, `database("mstar").Ent | Enki has Kusto`)
	expectWrRows(t, eng, `union withsource=T Loc, database('mstar').Ent | order by T asc`,
		`database("`+local+`").Loc | local Kusto`, `database("mstar").Ent | Enki has Kusto`)
	// Only the default database: labels stay plain.
	expectWrRows(t, eng, `union withsource=T Loc`, `Loc | local Kusto`)
	expectWrRows(t, eng, `find in (database('*').*) where * has 'Kusto' | project source_ | order by source_ asc`,
		`database("`+local+`").Loc`, `database("mstar").Ent`)
	expectWrRows(t, eng, `search in (database('mstar').E*) 'Enki' | project $table`, `database("mstar").Ent`)
	expectWrRows(t, eng, `cluster('odm').database('mstar').Ent | count`, "1")
	expectWrRows(t, eng, `cluster('lab').database('`+otherDB+`').Far | project Name`, "far Kusto")
	expectWrRows(t, eng, `union withsource=T cluster('lab').database('*').* | project T`,
		`cluster("lab").database("`+otherDB+`").Far`)
	expectWrRows(t, eng, `print current_cluster_endpoint(), current_principal()`, "odm | laurie")
	expectWrRows(t, eng, `.show databases | project DatabaseName, IsCurrent, DatabaseAccessMode`,
		local+" | true | ReadWrite", "mstar | false | ReadOnly")
	expectErrContains(t, eng, `cluster('nope').database('x').T`, "not listed")
}

// macro-expand runs the subquery per entity and unions the results;
// the emulator rejects duplicate entities.
func TestMacroExpand(t *testing.T) {
	eng, otherDB := clusterScopes(t)
	local := eng.localDatabaseName()
	expectWrRows(t, eng, `macro-expand entity_group [database('`+local+`'), database('mstar')] as X (`+
		`union X.* | project Name, db = X.$current_database) | order by db asc`,
		"local Kusto | "+local, "Enki has Kusto | mstar")
	expectWrRows(t, eng, `let G = entity_group [cluster('lab').database('`+otherDB+`')]; `+
		`macro-expand G as X (X.Far | project Name, e = X.$current_cluster_endpoint)`, "far Kusto | lab")
	expectWrRows(t, eng, `let G = entity_group [database('mstar')]; macro-expand X in G (X.Ent | count)`, "1")
	expectWrRows(t, eng, `macro-expand entity_group [database('mstar')] as X (X.Ent | count | extend e = X.$current_cluster_endpoint)`, "1 | odm")
	expectWrRows(t, eng, `print s = 'macro-expand X'`, "macro-expand X")
	expectErrContains(t, eng, `macro-expand entity_group [database('mstar'), database('mstar')] as X (X.Ent)`, "twice")
	expectErrContains(t, eng, `macro-expand Nope as X (X.Ent)`, "unknown entity group")
}

func TestCursors(t *testing.T) {
	eng := discoverEngine(t, t.TempDir())
	diskExec(t, eng, `.create table T (x:long)`)
	diskExec(t, eng, `.set-or-append T <| print x=1`)
	c := cellString(diskExec(t, eng, `print cursor_current()`).Rows[0][0])
	diskExec(t, eng, `.set-or-append T <| print x=2`)
	expectWrRows(t, eng, `T | where cursor_after('`+c+`') | project x`, "2")
	expectWrRows(t, eng, `T | where cursor_before_or_at('`+c+`') | project x`, "1")
	expectWrRows(t, eng, `T | where cursor_after('') | count`, "2")
	expectWrRows(t, eng, `T | where ingestion_time() > datetime(2000-01-01) | project x | count`, "2")
	expectErrContains(t, eng, `T | where cursor_after('abc')`, "not a cursor")
}
