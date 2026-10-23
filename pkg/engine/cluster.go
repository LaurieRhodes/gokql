package engine

// cluster.go — gokql answering as a cluster. The databases of this
// cluster are the local scope plus the aliases of its
// .okql-federation.json (read-only); the file may also name the cluster
// ("cluster", default "localhost"), the principal current_principal()
// reports, and other clusters ("clusters": name -> the primary scope
// directory of another cluster, whose own config lists its databases).
//
// On top of that: database('*') and wildcard tables (database('x').T*,
// cluster('c').database('*').*) in union, find and search; qualified
// source labels (database("x").T) whenever more than the default
// database is involved, as ADX labels them; .show databases;
// current_cluster_endpoint() and current_principal().

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func (e *Engine) federation() *federationConfig {
	if e.Catalog == nil || !e.Catalog.IsDiscovery() {
		return &federationConfig{}
	}
	cfg, err := loadFederationConfig(e.Catalog.DatabasePath())
	if err != nil || cfg == nil {
		return &federationConfig{}
	}
	return cfg
}

// clusterName is this cluster's name.
func (e *Engine) clusterName() string {
	if e.hostCluster != "" {
		return e.hostCluster
	}
	if c := e.federation().Cluster; c != "" {
		return c
	}
	return "localhost"
}

// principal is what current_principal() reports.
func (e *Engine) principal() string {
	if e.hostPrincipal != "" {
		return e.hostPrincipal
	}
	if p := e.federation().Principal; p != "" {
		return p
	}
	return "<null identity>"
}

func (e *Engine) isThisCluster(name string) bool {
	return strings.EqualFold(name, e.clusterName()) || strings.EqualFold(name, "localhost") && e.clusterName() == "localhost"
}

func (e *Engine) localDatabaseName() string {
	if e.Catalog != nil && e.Catalog.Database != nil {
		return e.Catalog.Database.Name
	}
	return ""
}

// clusterDB is one database of a cluster: the local scope (eng set) or
// a directory.
type clusterDB struct {
	name  string
	path  string
	local bool
}

// databasesOf lists a cluster's databases: for this cluster the local
// scope and its aliases; for another, its primary scope and that
// scope's aliases.
func (e *Engine) databasesOf(cluster string) ([]clusterDB, error) {
	if cluster == "" || e.isThisCluster(cluster) {
		dbs := []clusterDB{{name: e.localDatabaseName(), path: e.Catalog.DatabasePath(), local: true}}
		for _, a := range e.federation().Aliases {
			dbs = append(dbs, clusterDB{name: a.Alias, path: a.Path})
		}
		return dbs, nil
	}
	root, err := e.otherClusterPath(cluster)
	if err != nil {
		return nil, err
	}
	dbs := []clusterDB{{name: filepath.Base(filepath.Clean(root)), path: root}}
	cfg, err := loadFederationConfig(root)
	if err != nil {
		return nil, fmt.Errorf("cluster(%q): %w", cluster, err)
	}
	for _, a := range cfg.Aliases {
		dbs = append(dbs, clusterDB{name: a.Alias, path: a.Path})
	}
	return dbs, nil
}

func (e *Engine) otherClusterPath(cluster string) (string, error) {
	for _, c := range e.federation().Clusters {
		if strings.EqualFold(c.Alias, cluster) {
			if !filepath.IsAbs(c.Path) {
				return "", fmt.Errorf("cluster(%q): path %q must be absolute", cluster, c.Path)
			}
			return c.Path, nil
		}
	}
	return "", fmt.Errorf("cluster(%q): not this cluster (%s) and not listed under \"clusters\" in %s",
		cluster, e.clusterName(), federationConfigFileName)
}

// resolveOtherClusterTable reads cluster('c').database('d').T: the
// database is the other cluster's primary scope or one of its aliases;
// the read is read-only like any alias.
func (e *Engine) resolveOtherClusterTable(ref *parser.DatabaseTableRef, operators []parser.Operator) (*types.Table, []parser.Operator, error) {
	dbs, err := e.databasesOf(ref.Cluster)
	if err != nil {
		return nil, nil, err
	}
	for _, db := range dbs {
		if !strings.EqualFold(db.name, ref.Alias) {
			continue
		}
		cat, err := catalog.Open(db.path)
		if err != nil {
			return nil, nil, fmt.Errorf("cluster(%q).database(%q): opening %s: %w", ref.Cluster, ref.Alias, db.path, err)
		}
		remote := e.forNested(cat)
		remote.hostCluster = ref.Cluster
		if c, _ := loadFederationConfig(dbs[0].path); c != nil && c.Cluster != "" {
			remote.hostCluster = c.Cluster
		}
		if cat.GetTable(ref.TableName) == nil {
			return nil, nil, fmt.Errorf("cluster(%q).database(%q).%s: table not found", ref.Cluster, ref.Alias, ref.TableName)
		}
		pushable, remaining := splitPushableFederationOps(operators)
		t, err := remote.executeQuery(&parser.Query{Source: ref.TableName, Operators: pushable})
		if err != nil {
			return nil, nil, fmt.Errorf("cluster(%q).database(%q).%s: %w", ref.Cluster, ref.Alias, ref.TableName, err)
		}
		return t, remaining, nil
	}
	return nil, nil, fmt.Errorf("cluster(%q): no database %q", ref.Cluster, ref.Alias)
}

// qualifiedName renders a table as ADX labels it across databases.
func (e *Engine) qualifiedName(cluster, db, table string) string {
	s := fmt.Sprintf("database(%q).%s", db, table)
	if cluster != "" && !e.isThisCluster(cluster) {
		s = fmt.Sprintf("cluster(%q).", cluster) + s
	}
	return s
}

// expandQualified expands [cluster('c').]database('d').Pattern, where d
// may be '*' and Pattern may hold '*', into qualified table names that
// executeQuery resolves. ok is false when spec is not qualified.
func (e *Engine) expandQualified(spec string) (names []string, ok bool, err error) {
	cluster, db, pattern, ok := parser.SplitQualifiedName(spec)
	if !ok {
		return nil, false, nil
	}
	dbs, err := e.databasesOf(cluster)
	if err != nil {
		return nil, true, err
	}
	matchedDB := false
	for _, d := range dbs {
		if db != "*" && !strings.EqualFold(db, d.name) {
			continue
		}
		matchedDB = true
		var tables []string
		if d.local {
			tables = e.Catalog.ListTables()
		} else {
			cat, err := catalog.Open(d.path)
			if err != nil {
				return nil, true, fmt.Errorf("database(%q): opening %s: %w", d.name, d.path, err)
			}
			tables = cat.ListTables()
		}
		sort.Strings(tables)
		for _, t := range tables {
			if wildcardMatch(pattern, t) {
				names = append(names, e.qualifiedName(cluster, d.name, t))
			}
		}
	}
	if !matchedDB {
		return nil, true, fmt.Errorf("database(%q): not a database of cluster %s", db, e.clusterName())
	}
	return names, true, nil
}

// expandTableSpecs expands a find / search / union table list: qualified
// specs through expandQualified, others unchanged. qualified reports
// whether any spec was qualified, in which case unqualified local tables
// are labelled with the default database too.
func (e *Engine) expandTableSpecs(specs []string) (out []string, qualified bool, err error) {
	for _, s := range specs {
		names, ok, err := e.expandQualified(s)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			out = append(out, s)
			continue
		}
		qualified = true
		out = append(out, names...)
	}
	return out, qualified, nil
}

// labelFor is a table's source label: qualified names as they are, and
// local tables qualified with the default database when qualify is set.
func (e *Engine) labelFor(name string, qualify bool) string {
	if !qualify || strings.HasPrefix(name, "database(") || strings.HasPrefix(name, "cluster(") {
		return name
	}
	if e.Catalog != nil && e.Catalog.GetTable(name) != nil {
		return e.qualifiedName("", e.localDatabaseName(), name)
	}
	return name
}

// applyShowDatabases implements .show databases: this cluster's
// databases.
func (e *Engine) applyShowDatabases() (*types.Table, error) {
	out := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "DatabaseName", Type: types.TypeString},
		{Name: "PersistentStorage", Type: types.TypeString},
		{Name: "Version", Type: types.TypeString},
		{Name: "IsCurrent", Type: types.TypeBool},
		{Name: "DatabaseAccessMode", Type: types.TypeString},
		{Name: "PrettyName", Type: types.TypeString},
		{Name: "TableCount", Type: types.TypeLong},
	}})
	dbs, err := e.databasesOf("")
	if err != nil {
		return nil, err
	}
	for _, d := range dbs {
		mode, n := "ReadWrite", int64(0)
		if d.local {
			n = int64(len(e.Catalog.ListTables()))
		} else {
			mode = "ReadOnly"
			if _, err := os.Stat(d.path); err != nil {
				mode = "Unavailable"
			} else if cat, err := catalog.Open(d.path); err == nil {
				n = int64(len(cat.ListTables()))
			}
		}
		out.AddRow(types.Row{d.name, d.path, "v1.0", d.local, mode, d.name, n})
	}
	return out, nil
}
