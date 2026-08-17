package engine

// graph_bfs_test.go — distinct-node BFS graph-match: correctness
// against the existing path-enumeration semantics, and the guard
// against combining Distinct with a multi-edge-spec pattern.

import "testing"

// TestGraphMatchDistinctMatchesPathEnumeration requires distinct BFS
// to produce the exact same (node, shallowest-depth) set as the
// default path-enumeration mode collapsed via summarize min(Hops) —
// the two must agree, since distinct is a performance path to the
// same disclosure answer, not a different one.
func TestGraphMatchDistinctMatchesPathEnumeration(t *testing.T) {
	base := `let E = datatable (Src: string, Dst: string) [
		"a","b", "b","c", "a","d", "d","c", "c","e", "b","e"];
		E | make-graph Src --> Dst | graph-match `

	distinct := queryResult(t, base+`(x)-[e*1..3 distinct]-(y) where x.NodeId == "a" and y.NodeId != "a" project Target = y.NodeId, Hops = array_length(e) | sort by Target asc`)
	enumerated := queryResult(t, base+`(x)-[e*1..3]-(y) where x.NodeId == "a" and y.NodeId != "a" project Target = y.NodeId, Hops = array_length(e) | summarize Depth = min(Hops) by Target | project Target, Hops = Depth | sort by Target asc`)

	dCSV := tableCSV(t, distinct)
	eCSV := tableCSV(t, enumerated)
	if dCSV != eCSV {
		t.Errorf("distinct BFS disagrees with path-enumeration + min(Hops):\ndistinct:\n%s\nenumerated:\n%s", dCSV, eCSV)
	}
}

// TestGraphMatchDistinctRejectsMultiEdgePattern: distinct only composes
// with a single edge spec (node-identity dedup doesn't compose with
// continuing a longer pattern).
func TestGraphMatchDistinctRejectsMultiEdgePattern(t *testing.T) {
	q := `let E = datatable (Src: string, Dst: string) ["a","b","b","c"];
		E | make-graph Src --> Dst
		| graph-match (x)-[e*1..2 distinct]-(y)-[f]->(z) project z.NodeId`
	queryError(t, q)
}

// TestGraphMatchDistinctUndirectedNoDoubleCount: an undirected distinct
// walk must not emit a node twice via two different routes.
func TestGraphMatchDistinctUndirectedNoDoubleCount(t *testing.T) {
	// Diamond: a->b, a->c, b->d, c->d. d is reachable from a via two
	// routes at depth 2; distinct must emit it exactly once.
	q := `let E = datatable (Src: string, Dst: string) ["a","b","a","c","b","d","c","d"];
		E | make-graph Src --> Dst
		| graph-match (x)-[e*1..2 distinct]-(y) where x.NodeId == "a" and y.NodeId == "d"
		  project y.NodeId`
	tbl := queryResult(t, q)
	expectRows(t, tbl, 1)
}
