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
	if err := os.WriteFile(filepath.Join(local, ".okql-federation.json"), b, 0o644); err != nil {
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

func TestAliasDatabase(t *testing.T) {
	eng, otherDB := clusterScopes(t)
	expectWrRows(t, eng, `alias database m = cluster('odm').database('mstar'); database('m').Ent | count`, "1")
	expectWrRows(t, eng, `alias database ['far db'] = cluster('lab').database('`+otherDB+`'); database("far db").Far | project Name`, "far Kusto")
	expectErrContains(t, eng, `alias database x = 1; print 1`, "expected cluster")
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

// extent_id() is the empty guid and extent_tags() null, as the emulator
// answers them per row.
func TestExtentIntrospection(t *testing.T) {
	eng := discoverEngine(t, t.TempDir())
	diskExec(t, eng, `.set-or-append T <| print x=1`)
	expectWrRows(t, eng, `T | project e = extent_id(), t = isnull(extent_tags())`,
		"00000000-0000-0000-0000-000000000000 | true")
}

// gokql.yaml: the database's own readonly, and the cluster sections.
func TestConfigYAML(t *testing.T) {
	local, other := t.TempDir(), t.TempDir()
	diskExec(t, discoverEngine(t, other), `.set-or-append Policies <| datatable(Title:string)["Leave policy"]`)
	diskExec(t, discoverEngine(t, local), `.set-or-append Notes <| print x=1`)
	yml := "cluster: corp\nreadonly: true\ndatabases:\n  - name: hr\n    path: " + other + "\n"
	if err := os.WriteFile(filepath.Join(local, "gokql.yaml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := discoverEngine(t, local)
	expectWrRows(t, eng, `database("hr").Policies | project Title`, "Leave policy")
	expectWrRows(t, eng, `print current_cluster_endpoint()`, "corp")
	expectErrContains(t, eng, `.set-or-append Notes <| print x=2`, "readonly: true")
	expectWrRows(t, eng, `Notes | count`, "1")
}
