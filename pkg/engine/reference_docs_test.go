package engine

// reference_docs_test.go — docs/reference is generated from the engine.
//
// docs/reference/catalog.json lists the KQL language (operators,
// functions, aggregations, plugins, statements) as Microsoft's
// documentation does (tools/gen-reference/extract_catalog.py).
// TestReferenceDocs probes this engine for every entry and renders the
// reference pages and support.json; it fails when a page differs from
// what the engine now supports. Regenerate with
//
//	go test ./pkg/engine -run TestReferenceDocs -update-reference
//
// docs/reference/notes.json holds the hand-written gokql notes shown
// beside an entry ("kind:name" -> note).

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
)

var updateReference = flag.Bool("update-reference", false, "rewrite docs/reference from the engine")

type refEntry struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

// refProbes are queries that run (without error) exactly when the entry
// is supported. Functions, aggregations, window functions and plugins
// without one are probed generically.
var refProbes = map[string]string{
	// tabular operators
	"operator:as":                    `range x from 1 to 2 step 1 | as T | count`,
	"operator:consume":               `range x from 1 to 2 step 1 | consume`,
	"operator:count":                 `range x from 1 to 2 step 1 | count`,
	"operator:datatable":             `datatable(a:long)[1, 2] | count`,
	"operator:distinct":              `range x from 1 to 2 step 1 | distinct x`,
	"operator:evaluate":              `range x from 1 to 2 step 1 | evaluate rows_near(x == 1, 1)`,
	"operator:extend":                `range x from 1 to 2 step 1 | extend y = x * 2`,
	"operator:externaldata":          `externaldata(a:string)['./no-such-file.csv'] | count`,
	"operator:facet":                 `range x from 1 to 2 step 1 | facet by x`,
	"operator:find":                  `let T = datatable(a:string)['x']; find in (T) where a == 'x'`,
	"operator:fork":                  `range x from 1 to 2 step 1 | fork (count) (take 1)`,
	"operator:getschema":             `range x from 1 to 2 step 1 | getschema`,
	"operator:invoke":                `let f = (T:(x:long)) { T }; range x from 1 to 2 step 1 | invoke f()`,
	"operator:join":                  `range x from 1 to 2 step 1 | join kind=inner (range x from 1 to 2 step 1) on x`,
	"operator:lookup":                `range x from 1 to 2 step 1 | lookup (datatable(x:long, y:string)[1, 'a']) on x`,
	"operator:macro-expand":          `macro-expand entity_group [database('memory')] as X (print 1)`,
	"operator:make-series":           `range x from 1 to 3 step 1 | extend t = datetime(2020-01-01) + x * 1d | make-series c = count() on t step 1d`,
	"operator:mv-apply":              `print d = dynamic([1, 2]) | mv-apply d to typeof(long) on (take 1)`,
	"operator:mv-expand":             `print d = dynamic([1, 2]) | mv-expand d`,
	"operator:parse":                 `print s = 'a=1' | parse s with 'a=' v:long`,
	"operator:parse-kv":              `print s = 'a=1' | parse-kv s as (a:long) with (pair_delimiter=',', kv_delimiter='=')`,
	"operator:parse-where":           `print s = 'a=1' | parse-where s with 'a=' v:long`,
	"operator:partition":             `range x from 1 to 2 step 1 | partition hint.strategy=native by x (take 1)`,
	"operator:print":                 `print 1`,
	"operator:project":               `range x from 1 to 2 step 1 | project x`,
	"operator:project-away":          `range x from 1 to 2 step 1 | extend y = 1 | project-away y`,
	"operator:project-by-names":      `range x from 1 to 2 step 1 | project-by-names 'x'`,
	"operator:project-keep":          `range x from 1 to 2 step 1 | extend y = 1 | project-keep x`,
	"operator:project-rename":        `range x from 1 to 2 step 1 | project-rename y = x`,
	"operator:project-reorder":       `range x from 1 to 2 step 1 | extend y = 1 | project-reorder y`,
	"operator:range":                 `range x from 1 to 2 step 1`,
	"operator:reduce":                `print s = 'a b' | reduce by s`,
	"operator:render":                `range x from 1 to 2 step 1 | render table`,
	"operator:sample":                `range x from 1 to 2 step 1 | sample 1`,
	"operator:sample-distinct":       `range x from 1 to 2 step 1 | sample-distinct 1 of x`,
	"operator:scan":                  `range x from 1 to 2 step 1 | scan declare (c:long=0) with (step s: true => c = s.c + 1;)`,
	"operator:search":                `let T = datatable(a:string)['x']; search in (T) 'x'`,
	"operator:serialize":             `range x from 1 to 2 step 1 | serialize`,
	"operator:sort":                  `range x from 1 to 2 step 1 | sort by x`,
	"operator:summarize":             `range x from 1 to 2 step 1 | summarize count() by x`,
	"operator:take":                  `range x from 1 to 2 step 1 | take 1`,
	"operator:top":                   `range x from 1 to 2 step 1 | top 1 by x`,
	"operator:top-hitters":           `range x from 1 to 2 step 1 | top-hitters 1 of x`,
	"operator:top-nested":            `range x from 1 to 2 step 1 | top-nested 1 of x by count()`,
	"operator:union":                 `union (print a = 1), (print a = 2)`,
	"operator:where":                 `range x from 1 to 2 step 1 | where x > 1`,
	"operator:make-graph":            `datatable(s:string, d:string)['a', 'b'] | make-graph s --> d | graph-to-table edges`,
	"operator:graph-to-table":        `datatable(s:string, d:string)['a', 'b'] | make-graph s --> d | graph-to-table nodes`,
	"operator:graph-match":           `let n = datatable(id:string)['a', 'b']; datatable(s:string, d:string)['a', 'b'] | make-graph s --> d with n on id | graph-match (x)-->(y) project x = x.id, y = y.id`,
	"operator:graph-shortest-paths":  `let n = datatable(id:string)['a', 'b']; datatable(s:string, d:string)['a', 'b'] | make-graph s --> d with n on id | graph-shortest-paths (x)-[*1..3]->(y) where x.id == 'a' project y = y.id`,
	"operator:graph-mark-components": `let n = datatable(id:string)['a', 'b']; datatable(s:string, d:string)['a', 'b'] | make-graph s --> d with n on id | graph-mark-components | graph-to-table nodes`,
	// graph-match functions
	"function:all":             `let n = datatable(id:string)['a', 'b', 'c']; datatable(s:string, d:string)['a', 'b', 'b', 'c'] | make-graph s --> d with n on id | graph-match (x)-[e*1..2]->(y) project v = all(e, true)`,
	"function:any":             `let n = datatable(id:string)['a', 'b', 'c']; datatable(s:string, d:string)['a', 'b', 'b', 'c'] | make-graph s --> d with n on id | graph-match (x)-[e*1..2]->(y) project v = any(e, true)`,
	"function:inner_nodes":     `let n = datatable(id:string)['a', 'b', 'c']; datatable(s:string, d:string)['a', 'b', 'b', 'c'] | make-graph s --> d with n on id | graph-match (x)-[e*1..2]->(y) project v = map(inner_nodes(e), id)`,
	"function:labels":          `let n = datatable(id:string)['a', 'b', 'c']; datatable(s:string, d:string)['a', 'b', 'b', 'c'] | make-graph s --> d with n on id | graph-match (x)-[e*1..2]->(y) project v = labels(x)`,
	"function:map":             `let n = datatable(id:string)['a', 'b', 'c']; datatable(s:string, d:string)['a', 'b', 'b', 'c'] | make-graph s --> d with n on id | graph-match (x)-[e*1..2]->(y) project v = map(e, 1)`,
	"function:node_degree_in":  `let n = datatable(id:string)['a', 'b', 'c']; datatable(s:string, d:string)['a', 'b', 'b', 'c'] | make-graph s --> d with n on id | graph-match (x)-[e*1..2]->(y) project v = node_degree_in(x)`,
	"function:node_degree_out": `let n = datatable(id:string)['a', 'b', 'c']; datatable(s:string, d:string)['a', 'b', 'b', 'c'] | make-graph s --> d with n on id | graph-match (x)-[e*1..2]->(y) project v = node_degree_out(x)`,
	"function:node_id":         `let n = datatable(id:string)['a', 'b', 'c']; datatable(s:string, d:string)['a', 'b', 'b', 'c'] | make-graph s --> d with n on id | graph-match (x)-[e*1..2]->(y) project v = node_id(x)`,
	// scalar operators
	"scalar-operator:between":       `print 1 between (0 .. 2)`,
	"scalar-operator:!between":      `print 1 !between (2 .. 3)`,
	"scalar-operator:in":            `print 1 in (1, 2)`,
	"scalar-operator:!in":           `print 1 !in (2)`,
	"scalar-operator:in~":           `print 'A' in~ ('a')`,
	"scalar-operator:!in~":          `print 'A' !in~ ('b')`,
	"scalar-operator:has_any":       `print 'a b' has_any ('a')`,
	"scalar-operator:has_all":       `print 'a b' has_all ('a', 'b')`,
	"scalar-operator:matches regex": `print 'abc' matches regex 'b'`,
	// query statements
	"statement:Alias":                         `alias database x = cluster('localhost').database('memory'); print 1`,
	"statement:Batches":                       `print 1; print 2`,
	"statement:Let":                           `let a = 1; print a`,
	"statement:Pattern":                       `declare pattern P = (k:string)[*] { ('a').x = { print 1 }; }; print 1`,
	"statement:Query parameters":              `declare query_parameters(p:long = 1); print p`,
	"statement:Restrict":                      `restrict access to (); print 1`,
	"statement:Set":                           `set query_now = datetime(2020-01-01); print now()`,
	"statement:Tabular expression statements": `print 1`,
	// special functions
	"function:cluster":             `union cluster('localhost').database('*').T0 | count`,
	"function:database":            `union database('*').T0 | count`,
	"function:table":               `table('T0') | count`,
	"function:materialize":         `let m = materialize(range x from 1 to 2 step 1); m | count`,
	"function:materialized_view":   `materialized_view('T0')`,
	"function:toscalar":            `print toscalar(range x from 1 to 2 step 1 | count)`,
	"function:graph":               `graph('g')`,
	"function:external_table":      `external_table('t')`,
	"function:stored_query_result": `print stored_query_result('r')`,
}

// a probe that names a stored object which does not exist fails on
// the object, not the function: these count as supported.
var refProbeMissingObject = map[string]string{
	"function:graph":        "graph model",
	"operator:externaldata": "no-such-file",
}

func refEngine(t *testing.T) *Engine {
	cat := catalog.NewMemory()
	eng := New(cat)
	stmt, err := parser.Parse(`.set-or-append T0 <| print x = 1`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Execute(stmt); err != nil {
		t.Fatal(err)
	}
	return eng
}

// refPanics collects probes that crashed the engine: each is a bug.
var refPanics []string

func refRun(t *testing.T, q string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			refPanics = append(refPanics, fmt.Sprintf("%s: %v", q, r))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	eng := refEngine(t)
	stmt, err := parser.Parse(q)
	if err != nil {
		return err
	}
	_, err = eng.Execute(stmt)
	return err
}

func refSupported(t *testing.T, e refEntry) (bool, error) {
	key := e.Kind + ":" + e.Name
	if q, ok := refProbes[key]; ok {
		err := refRun(t, q)
		if err != nil {
			if s, ok := refProbeMissingObject[key]; ok && strings.Contains(err.Error(), s) {
				return true, nil
			}
		}
		return err == nil, err
	}
	switch e.Kind {
	case "function":
		err := refRun(t, `print __p = `+e.Name+`()`)
		if err != nil && strings.Contains(err.Error(), "unsupported function: "+e.Name) {
			return false, err
		}
		return true, nil
	case "window":
		err := refRun(t, `range x from 1 to 3 step 1 | serialize | extend y = `+e.Name+`(x)`)
		if err != nil && strings.Contains(err.Error(), "unsupported function: "+e.Name) {
			return false, err
		}
		return true, nil
	case "aggregation":
		err := refRun(t, `range x from 1 to 3 step 1 | summarize `+e.Name+`(x)`)
		if err != nil && (strings.Contains(err.Error(), "contains no aggregation function") ||
			strings.Contains(err.Error(), "unsupported function: "+e.Name) || strings.Contains(err.Error(), "unknown aggregation")) {
			return false, err
		}
		return true, nil
	case "plugin":
		_, ok := evaluatePlugins[e.Name]
		return ok, nil
	case "scalar-operator":
		err := refRun(t, `print 'abc' `+e.Name+` 'b'`)
		return err == nil, err
	}
	return false, fmt.Errorf("no probe for %s", key)
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	return strings.ReplaceAll(s, "\n", " ")
}

func TestReferenceDocs(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "reference")
	raw, err := os.ReadFile(filepath.Join(dir, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []refEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	notes := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(dir, "notes.json")); err == nil {
		if err := json.Unmarshal(b, &notes); err != nil {
			t.Fatalf("notes.json: %v", err)
		}
	}
	type row struct {
		refEntry
		Supported bool   `json:"supported"`
		Note      string `json:"note,omitempty"`
	}
	var rows []row
	known := map[string]bool{}
	for _, e := range entries {
		ok, _ := refSupported(t, e)
		key := e.Kind + ":" + e.Name
		known[key] = true
		rows = append(rows, row{refEntry: e, Supported: ok, Note: notes[key]})
	}
	for _, p := range refPanics {
		t.Errorf("engine panic: %s", p)
	}
	for k := range notes {
		if !known[k] {
			t.Errorf("notes.json: %q is not in catalog.json", k)
		}
	}

	count := func(kinds ...string) (int, int) {
		n, m := 0, 0
		for _, r := range rows {
			for _, k := range kinds {
				if r.Kind == k {
					m++
					if r.Supported {
						n++
					}
				}
			}
		}
		return n, m
	}
	section := func(b *strings.Builder, kinds []string, nameSuffix string) {
		cats := map[string][]row{}
		var order []string
		for _, r := range rows {
			for _, k := range kinds {
				if r.Kind == k {
					if _, ok := cats[r.Category]; !ok {
						order = append(order, r.Category)
					}
					cats[r.Category] = append(cats[r.Category], r)
				}
			}
		}
		sort.Strings(order)
		for _, c := range order {
			fmt.Fprintf(b, "\n### %s\n\n| Name | gokql | Description | Notes |\n|---|---|---|---|\n", c)
			rs := cats[c]
			sort.Slice(rs, func(i, j int) bool { return rs[i].Name < rs[j].Name })
			for _, r := range rs {
				st := "yes"
				if !r.Supported {
					st = "**no**"
				}
				fmt.Fprintf(b, "| [%s%s](%s) | %s | %s | %s |\n", mdCell(r.Name), nameSuffix, r.URL, st, mdCell(r.Description), mdCell(r.Note))
			}
		}
	}
	const head = "<!-- Generated by TestReferenceDocs (pkg/engine/reference_docs_test.go) from catalog.json, notes.json and the engine. Do not edit. -->\n\n"
	attribution := "\nDescriptions are the first sentence of each entry's page in Microsoft's [KQL documentation](https://learn.microsoft.com/kusto/query/) (CC BY 4.0); the names link there. \"gokql\" says whether this engine accepts the entry, probed from the code; Notes are gokql's own.\n"
	pages := map[string]string{}

	var b strings.Builder
	n, m := count("operator")
	fmt.Fprintf(&b, "%s# Operators and statements\n\nTabular operators: %d of %d supported.%s", head, n, m, attribution)
	section(&b, []string{"operator"}, "")
	n, m = count("scalar-operator")
	fmt.Fprintf(&b, "\n## Scalar operators\n\n%d of %d supported. Arithmetic, comparison, logical and datetime operators are supported throughout.\n", n, m)
	section(&b, []string{"scalar-operator"}, "")
	n, m = count("statement")
	fmt.Fprintf(&b, "\n## Query statements\n\n%d of %d supported.\n", n, m)
	section(&b, []string{"statement"}, "")
	pages["operators.md"] = b.String()

	b.Reset()
	n, m = count("function")
	fmt.Fprintf(&b, "%s# Scalar functions\n\n%d of %d supported.%s", head, n, m, attribution)
	section(&b, []string{"function"}, "()")
	n, m = count("window")
	fmt.Fprintf(&b, "\n## Window functions\n\n%d of %d supported. They need serialized input (`serialize`, `order by`, `sort`, `top`, ...).\n", n, m)
	section(&b, []string{"window"}, "()")
	pages["functions.md"] = b.String()

	b.Reset()
	n, m = count("aggregation")
	fmt.Fprintf(&b, "%s# Aggregation functions\n\n%d of %d supported. Used in `summarize`, `make-series` and `top-nested`.%s", head, n, m, attribution)
	section(&b, []string{"aggregation"}, "()")
	pages["aggregations.md"] = b.String()

	b.Reset()
	n, m = count("plugin")
	fmt.Fprintf(&b, "%s# Plugins\n\n%d of %d supported, called with `evaluate`. Plugins that reach outside the process go through the [callout policy](../guide/security.md).%s", head, n, m, attribution)
	section(&b, []string{"plugin"}, "")
	pages["plugins.md"] = b.String()

	b.Reset()
	fmt.Fprintf(&b, "%s# Management commands\n\nThe dot-commands gokql accepts, as `.help` lists them. Commands that answer with a table can be piped (`.show tables | where ...`).\n\n| Command | What it does |\n|---|---|\n", head)
	for _, h := range helpEntries {
		fmt.Fprintf(&b, "| `%s` | %s |\n", strings.ReplaceAll(h.cmd, "|", `\|`), mdCell(h.desc))
	}
	pages["commands.md"] = b.String()

	js, _ := json.MarshalIndent(rows, "", " ")
	pages["support.json"] = string(js) + "\n"

	var stale []string
	for name, content := range pages {
		p := filepath.Join(dir, name)
		if *updateReference {
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		old, err := os.ReadFile(p)
		if err != nil || string(old) != content {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("docs/reference is stale (%s): run go test ./pkg/engine -run TestReferenceDocs -update-reference", strings.Join(stale, ", "))
	}
}
