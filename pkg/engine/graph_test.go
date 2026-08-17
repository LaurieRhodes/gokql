package engine

import "testing"

// Graph operator tests: make-graph + graph-to-table (Sprint 5, step 1).
// All end-to-end via the standard harness; graphFixture provides an edge
// table (a→b, b→c, a→c) and a node table with one isolated node (z) and
// one edge-only node (c, absent from N).

const graphEdges = `let E = datatable (Src: string, Dst: string) ["a","b","b","c","a","c"]; `
const graphNodes = `let N = datatable (Id: string, Kind: string) ["a","External","b","Server","z","Isolated"]; `

func TestMakeGraphToTableEdges(t *testing.T) {
	tbl := queryResult(t, graphEdges+`E | make-graph Src --> Dst | graph-to-table edges`)
	expectRows(t, tbl, 3)
	expectColNames(t, tbl, "Src", "Dst")
	expectCell(t, tbl, 0, 0, "a")
	expectCell(t, tbl, 2, 1, "c")
}

func TestGraphToTableDefaultIsEdges(t *testing.T) {
	tbl := queryResult(t, graphEdges+`E | make-graph Src --> Dst | graph-to-table`)
	expectRows(t, tbl, 3)
	expectColNames(t, tbl, "Src", "Dst")
}

func TestMakeGraphToTableNodesDerived(t *testing.T) {
	// No node table: nodes derived from edge endpoints, first-seen order.
	tbl := queryResult(t, graphEdges+`E | make-graph Src --> Dst | graph-to-table nodes`)
	expectRows(t, tbl, 3)
	expectColNames(t, tbl, "NodeId")
	expectCell(t, tbl, 0, 0, "a")
	expectCell(t, tbl, 1, 0, "b")
	expectCell(t, tbl, 2, 0, "c")
}

func TestMakeGraphWithNodeTable(t *testing.T) {
	// Node table attaches properties; edge-only node c gets null Kind;
	// isolated node z (no edges) joins the node set.
	tbl := queryResult(t, graphEdges+graphNodes+
		`E | make-graph Src --> Dst with N on Id | graph-to-table nodes`)
	expectRows(t, tbl, 4)
	expectColNames(t, tbl, "Id", "Kind")
	expectCell(t, tbl, 0, 1, "External") // a
	expectCell(t, tbl, 1, 1, "Server")   // b
	if v := cellVal(t, tbl, 2, 1); v != nil {
		t.Fatalf("edge-only node c: expected null Kind, got %v", v)
	}
	expectCell(t, tbl, 3, 0, "z")
}

func TestMakeGraphNullEndpointsDropped(t *testing.T) {
	tbl := queryResult(t,
		`let E = datatable (Src: string, Dst: string) ["a","b","a","","","c"]; `+
			`E | where isnotempty(Src) and isnotempty(Dst) | make-graph Src --> Dst | graph-to-table edges`)
	expectRows(t, tbl, 1)
}

func TestGraphPipelineContinuesTabular(t *testing.T) {
	// Tabular operators resume normally after graph-to-table.
	tbl := queryResult(t, graphEdges+
		`E | make-graph Src --> Dst | graph-to-table nodes | where NodeId != "b" | count`)
	expectCell(t, tbl, 0, 0, "2")
}

func TestGraphErrors(t *testing.T) {
	// graph-to-table without a preceding make-graph
	queryError(t, graphEdges+`E | graph-to-table nodes`)
	// tabular operator applied to a graph
	queryError(t, graphEdges+`E | make-graph Src --> Dst | where Src == "a"`)
	// pipeline ends on a graph
	queryError(t, graphEdges+`E | make-graph Src --> Dst`)
	// unknown columns
	queryError(t, graphEdges+`E | make-graph Missing --> Dst | graph-to-table edges`)
	queryError(t, graphEdges+`E | make-graph Src --> Missing | graph-to-table edges`)
	// unknown node table / node id column
	queryError(t, graphEdges+`E | make-graph Src --> Dst with Nope on Id | graph-to-table nodes`)
	queryError(t, graphEdges+graphNodes+`E | make-graph Src --> Dst with N on Nope | graph-to-table nodes`)
	// unknown graph-to-table output
	queryError(t, graphEdges+`E | make-graph Src --> Dst | graph-to-table paths`)
}

func TestParseMakeGraphErrors(t *testing.T) {
	queryError(t, graphEdges+`E | make-graph Src Dst`)               // missing arrow
	queryError(t, graphEdges+`E | make-graph --> Dst`)               // missing source
	queryError(t, graphEdges+`E | make-graph Src -->`)               // missing target
	queryError(t, graphEdges+`E | make-graph Src --> Dst with N Id`) // missing on
}

// --- graph-match (fixed-length patterns) ---

const graphNodesFull = `let N = datatable (Id: string, Kind: string) ["a","External","b","Server","c","CriticalAsset"]; `

func TestGraphMatchSingleHop(t *testing.T) {
	tbl := queryResult(t, graphEdges+graphNodesFull+
		`E | make-graph Src --> Dst with N on Id | graph-match (x)-[e]->(y) project x.Id, y.Id`)
	expectRows(t, tbl, 3) // a->b, b->c, a->c (traversal order: from a, then b)
	expectColNames(t, tbl, "x.Id", "y.Id")
}

func TestGraphMatchWhereNodeProperty(t *testing.T) {
	tbl := queryResult(t, graphEdges+graphNodesFull+
		`E | make-graph Src --> Dst with N on Id | graph-match (x)-[e]->(y) where x.Kind == "External" project x.Id, y.Id, y.Kind`)
	expectRows(t, tbl, 2)
	expectCell(t, tbl, 0, 0, "a")
	expectCell(t, tbl, 0, 2, "Server")
	expectCell(t, tbl, 1, 2, "CriticalAsset")
}

func TestGraphMatchWhereEdgeProperty(t *testing.T) {
	tbl := queryResult(t, graphEdges+
		`E | make-graph Src --> Dst | graph-match (x)-[e]->(y) where e.Dst == "c" project x.NodeId`)
	expectRows(t, tbl, 2) // b->c and a->c
}

func TestGraphMatchTwoHop(t *testing.T) {
	tbl := queryResult(t, graphEdges+graphNodesFull+
		`E | make-graph Src --> Dst with N on Id | graph-match (x)-[e1]->(m)-[e2]->(y) project Path = strcat(x.Id, "->", m.Id, "->", y.Id)`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "a->b->c")
}

func TestGraphMatchAnonymousElements(t *testing.T) {
	tbl := queryResult(t, graphEdges+
		`E | make-graph Src --> Dst | graph-match (x)-[]->() project x.NodeId`)
	expectRows(t, tbl, 3)
	expectColNames(t, tbl, "x.NodeId")
}

func TestGraphMatchDerivedNodesExposeNodeId(t *testing.T) {
	tbl := queryResult(t, graphEdges+
		`E | make-graph Src --> Dst | graph-match (x)-[]->(y) where y.NodeId == "c" project x.NodeId`)
	expectRows(t, tbl, 2)
}

func TestGraphMatchComputedProject(t *testing.T) {
	tbl := queryResult(t, graphEdges+
		`E | make-graph Src --> Dst | graph-match (x)-[e]->(y) project Upper = toupper(x.NodeId), y.NodeId`)
	expectCell(t, tbl, 0, 0, "A")
}

func TestGraphMatchErrors(t *testing.T) {
	// graph-match without a graph
	queryError(t, graphEdges+`E | graph-match (x)-[e]->(y) project x.NodeId`)
	// project clause is required
	queryError(t, graphEdges+`E | make-graph Src --> Dst | graph-match (x)-[e]->(y)`)
	// duplicate pattern variable
	queryError(t, graphEdges+`E | make-graph Src --> Dst | graph-match (x)-[x]->(y) project y.NodeId`)
	// malformed patterns
	queryError(t, graphEdges+`E | make-graph Src --> Dst | graph-match (x)-[e]<(y) project x.NodeId`)
	queryError(t, graphEdges+`E | make-graph Src --> Dst | graph-match (x) project x.NodeId`)
	queryError(t, graphEdges+`E | make-graph Src --> Dst | graph-match (x)-[e]-> project x.NodeId`)
	// unknown projected column
	queryError(t, graphEdges+`E | make-graph Src --> Dst | graph-match (x)-[e]->(y) project x.Missing`)
}

// --- graph-match (variable-length paths) ---

// Diamond + chain: a->b->c->d plus shortcut a->d.
const graphChain = `let E = datatable (Src: string, Dst: string) ["a","b","b","c","c","d","a","d"]; `
const graphChainNodes = `let N = datatable (Id: string, Kind: string) ["a","External","b","Server","c","Server","d","CriticalAsset"]; `

func TestGraphMatchVariableLengthAttackPath(t *testing.T) {
	// The roadmap's motivating query shape.
	tbl := queryResult(t, graphChain+graphChainNodes+
		`E | make-graph Src --> Dst with N on Id | graph-match (attacker)-[e*1..5]->(target) `+
		`where attacker.Kind == "External" and target.Kind == "CriticalAsset" `+
		`project attacker.Id, target.Id, PathLength = array_length(e)`)
	expectRows(t, tbl, 2) // a->b->c->d (3 hops) and a->d (1 hop)
	expectColNames(t, tbl, "attacker.Id", "target.Id", "PathLength")
}

func TestGraphMatchVariableLengthMinHops(t *testing.T) {
	// min=2 excludes the direct a->d edge.
	tbl := queryResult(t, graphChain+
		`E | make-graph Src --> Dst | graph-match (x)-[e*2..5]->(y) `+
		`where x.NodeId == "a" and y.NodeId == "d" project Hops = array_length(e)`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "3")
}

func TestGraphMatchVariableLengthEdgeElementAccess(t *testing.T) {
	tbl := queryResult(t,
		`let E = datatable (Src: string, Dst: string) ["a","b","b","c"]; `+
			`E | make-graph Src --> Dst | graph-match (x)-[e*2..2]->(y) project First = e[0].Dst, Last = e[1].Dst`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "b")
	expectCell(t, tbl, 0, 1, "c")
}

func TestGraphMatchCycleTermination(t *testing.T) {
	// Cycle a->b->c->a: unique-edges bounds expansion; the full loop
	// matches exactly once even with a generous MaxHops.
	tbl := queryResult(t,
		`let E = datatable (Src: string, Dst: string) ["a","b","b","c","c","a"]; `+
			`E | make-graph Src --> Dst | graph-match (x)-[e*1..10]->(y) `+
			`where x.NodeId == "a" and y.NodeId == "a" project Hops = array_length(e)`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "3")
}

func TestGraphMatchVariableThenFixed(t *testing.T) {
	// Mixed pattern: variable segment followed by a fixed edge.
	tbl := queryResult(t, graphChain+
		`E | make-graph Src --> Dst | graph-match (x)-[e*1..2]->(m)-[f]->(y) `+
		`where x.NodeId == "a" and y.NodeId == "d" `+
		`project m.NodeId, Hops = array_length(e)`)
	// a-[a->b]->b-[b->c... no: fixed f must land on d: paths are
	// a=e(1)=>c? none. Valid: a->b->c (e, 2 hops) then c->d (f); a->? (e=a->b, 1 hop) then b->? f=b->c lands on c != d.
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "c")
	expectCell(t, tbl, 0, 1, "2")
}

// --- Dotted-prefix resolution through dynamic edge/node columns ---
// (fixes: "e.Tags.sev" errored with column "e" not found; variable-length
// e[0].Tags.sev returned null due to double-encoded dynamic values)

const graphDynEdges = `let E = datatable (Src: string, Dst: string, Tags: dynamic) ` +
	`["a","b","{\"sev\": 5}","b","c","{\"sev\": 9}"]; `

func TestGraphMatchFixedEdgeDynamicDeepAccess(t *testing.T) {
	// Longest dotted prefix "e.Tags" resolves as a column; ".sev"
	// descends into its JSON value.
	tbl := queryResult(t, graphDynEdges+
		`E | make-graph Src --> Dst | graph-match (x)-[e]->(y) `+
		`where x.NodeId == "a" project Sev = e.Tags.sev`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "5")
}

func TestGraphMatchFixedEdgeDynamicIndexAccess(t *testing.T) {
	// A numeric index ends prefix growth but the prefix matched before
	// it still resolves: e.Tags[0].
	tbl := queryResult(t,
		`let E = datatable (Src: string, Dst: string, Tags: dynamic) ["a","b","[10, 20]"]; `+
			`E | make-graph Src --> Dst | graph-match (x)-[e]->(y) project First = e.Tags[0]`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "10")
}

func TestGraphMatchVariableLengthDynamicDeepAccess(t *testing.T) {
	// Dynamic edge columns embed as raw JSON in the edge list, so
	// e[i].Tags.sev reaches the nested value.
	tbl := queryResult(t, graphDynEdges+
		`E | make-graph Src --> Dst | graph-match (x)-[e*2..2]->(y) `+
		`project First = e[0].Tags.sev, Last = e[1].Tags.sev`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "5")
	expectCell(t, tbl, 0, 1, "9")
}

func TestGraphMatchNodeDynamicDeepAccess(t *testing.T) {
	tbl := queryResult(t,
		`let E = datatable (Src: string, Dst: string) ["a","b"]; `+
			`let N = datatable (Id: string, Meta: dynamic) `+
			`["a","{\"role\": \"admin\"}","b","{\"role\": \"user\"}"]; `+
			`E | make-graph Src --> Dst with N on Id | graph-match (x)-[e]->(y) `+
			`project R = x.Meta.role`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "admin")
}

func TestDynamicColumnAccessUnaffectedByPrefixResolution(t *testing.T) {
	// Base-column precedence: a real dynamic column keeps plain JSON
	// access semantics; prefix resolution never engages.
	tbl := queryResult(t, `datatable (D: dynamic) ["{\"a\": {\"b\": 7}}"] | project V = D.a.b`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "7")
}

// --- Typed node keys and derived NodeId widening ---
// (fixes: fmt "%v" keys merged string "2" with long 2; derived NodeId
// column was typed from Src alone even when Dst differed)

func TestMakeGraphMixedIdTypesStayDistinct(t *testing.T) {
	// Src string / Dst long: "2" and 2 are different nodes; the derived
	// NodeId column widens to string with formatted values.
	tbl := queryResult(t,
		`let E = datatable (Src: string, Dst: long) ["1", 2, "2", 3]; `+
			`E | make-graph Src --> Dst | graph-to-table nodes`)
	expectRows(t, tbl, 4)
	expectColNames(t, tbl, "NodeId")
}

func TestGraphMatchMixedIdTypesTraversal(t *testing.T) {
	// No path threads "1"->2 then "2"->3 because node 2 (long) and
	// node "2" (string) are distinct: only single-hop matches exist.
	tbl := queryResult(t,
		`let E = datatable (Src: string, Dst: long) ["1", 2, "2", 3]; `+
			`E | make-graph Src --> Dst | graph-match (x)-[e*2..2]->(y) project x.NodeId`)
	expectRows(t, tbl, 0)
}

// --- Predicate pushdown, hop cap, result cap ---
// (fixes: where ran only on completed paths — no pruning during DFS,
// unbounded MaxHops)

func TestGraphMatchOrPredicateNotSplit(t *testing.T) {
	// A top-level `or` is a single conjunct scheduled at the final
	// position; results must match full-enumeration semantics.
	tbl := queryResult(t, graphChain+graphChainNodes+
		`E | make-graph Src --> Dst with N on Id | graph-match (x)-[e*1..5]->(y) `+
		`where x.Kind == "External" or y.Kind == "CriticalAsset" `+
		`project x.Id, y.Id | count`)
	// All 7 simple paths minus b->c (neither side qualifies) = 6.
	expectCell(t, tbl, 0, 0, "6")
}

func TestGraphMatchPushdownPrunesHandoffNotExpansion(t *testing.T) {
	// where y.NodeId == "d" fails at depths 1 and 2 of the variable
	// segment; expansion must continue so the 3-hop path still matches.
	tbl := queryResult(t, graphChain+
		`E | make-graph Src --> Dst | graph-match (x)-[e*1..5]->(y) `+
		`where x.NodeId == "a" and y.NodeId == "d" `+
		`project Hops = array_length(e) | sort by Hops asc`)
	expectRows(t, tbl, 2) // direct a->d and a->b->c->d
	expectCell(t, tbl, 0, 0, "1")
	expectCell(t, tbl, 1, 0, "3")
}

func TestGraphMatchMixedPositionConjuncts(t *testing.T) {
	// Conjuncts bind at different positions (x at 0, m at 1, y at 2);
	// all must apply. Fixed 2-hop paths are a->b->c and b->c->d; only
	// a->b->c passes all three conjuncts.
	tbl := queryResult(t, graphChain+graphChainNodes+
		`E | make-graph Src --> Dst with N on Id | graph-match (x)-[e]->(m)-[f]->(y) `+
		`where x.Kind == "External" and m.Kind == "Server" and y.Kind != "External" `+
		`project x.Id, m.Id, y.Id`)
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "a")
	expectCell(t, tbl, 0, 1, "b")
	expectCell(t, tbl, 0, 2, "c")
}

func TestGraphMatchHopRangeCap(t *testing.T) {
	queryError(t, graphChain+
		`E | make-graph Src --> Dst | graph-match (x)-[e*1..100000]->(y) project x.NodeId`)
	// Boundary: exactly the cap parses.
	tbl := queryResult(t, graphChain+
		`E | make-graph Src --> Dst | graph-match (x)-[e*1..64]->(y) project x.NodeId | count`)
	expectRows(t, tbl, 1)
}

func TestGraphMatchInvalidHopRanges(t *testing.T) {
	queryError(t, graphChain+`E | make-graph Src --> Dst | graph-match (x)-[e*0..3]->(y) project x.NodeId`)
	queryError(t, graphChain+`E | make-graph Src --> Dst | graph-match (x)-[e*3..1]->(y) project x.NodeId`)
	queryError(t, graphChain+`E | make-graph Src --> Dst | graph-match (x)-[e*..3]->(y) project x.NodeId`)
	queryError(t, graphChain+`E | make-graph Src --> Dst | graph-match (x)-[e*2]->(y) project x.NodeId`)
}

// TestMakeGraphNodeTableTypeCoercion is the fix for backlog P3 item
// 15: a node table whose id column disagrees in type with the edge
// endpoints (long edges, string node-table ids here) must unify a
// matching id as ONE node with its properties attached — previously
// they silently became two different graph nodes with no error,
// since typedKey's cross-type distinctness (correct for join
// semantics) meant long 1 and string "1" never matched.
func TestMakeGraphNodeTableTypeCoercion(t *testing.T) {
	q := `
let E = datatable (Src: long, Dst: long) [1, 2, 2, 3];
let N = datatable (Id: string, Kind: string) ["1", "Root", "2", "Middle", "3", "Leaf"];
E | make-graph Src --> Dst with N on Id | graph-to-table nodes | sort by Id asc
`
	tbl := queryResult(t, q)
	expectRows(t, tbl, 3) // NOT 6 — ids must unify, not double up
	expectCell(t, tbl, 0, 0, "1")
	expectCell(t, tbl, 0, 1, "Root")
	expectCell(t, tbl, 1, 1, "Middle")
	expectCell(t, tbl, 2, 1, "Leaf")

	// Traversal must also work correctly across the coerced keys.
	// With a node table present, graph-match exposes the node table'''s
	// OWN columns (Id, Kind — not a synthetic NodeId; that only exists
	// in the derived, no-node-table case), matching established
	// convention (see TestGraphMatchWhereNodeProperty above).
	tbl2 := queryResult(t, `
let E = datatable (Src: long, Dst: long) [1, 2, 2, 3];
let N = datatable (Id: string, Kind: string) ["1", "Root", "2", "Middle", "3", "Leaf"];
E | make-graph Src --> Dst with N on Id | graph-match (a)-[e]->(b) where a.Id == "1" project b.Id
`)
	expectRows(t, tbl2, 1)
	expectCell(t, tbl2, 0, 0, "2")
}

// TestGraphMatchReachabilityPruningCompleteness is the correctness
// proof for backlog P3 item 16's reverse-reachability pruning: a hand-
// built graph with (1) a dead-end branch that goes deep but never
// reaches the target — pruning must cut it, but it was never part of
// the answer set either way, so this alone doesn't prove much — and
// (2) critically, a LONGER-than-shortest valid route to the target
// that pruning must NOT discard just because a shorter route exists.
// Graph: a->b->c->TARGET (shortest, 3 hops), and a->x->y->z->TARGET
// (longer, 4 hops, still within *1..4). Also a->dead1->dead2->dead3
// (never reaches TARGET at all, pure dead end).
func TestGraphMatchReachabilityPruningCompleteness(t *testing.T) {
	q := `
let E = datatable (Src: string, Dst: string) [
	"a","b", "b","c", "c","TARGET",
	"a","x", "x","y", "y","z", "z","TARGET",
	"a","dead1", "dead1","dead2", "dead2","dead3"];
E | make-graph Src --> Dst
| graph-match (s)-[e*1..4]->(t) where s.NodeId == "a" and t.NodeId == "TARGET"
  project Hops = array_length(e)
| sort by Hops asc
`
	tbl := queryResult(t, q)
	// Both the 3-hop and 4-hop routes must be found — pruning must not
	// have discarded the longer valid route just because a shorter one
	// exists, and the dead-end branch (which never reaches TARGET at
	// all) correctly contributes zero matches either way.
	expectRows(t, tbl, 2)
	expectCell(t, tbl, 0, 0, "3")
	expectCell(t, tbl, 1, 0, "4")
}

// TestGraphMatchReachabilityPruningRespectsMinHops: MinHops filtering
// must remain unaffected by the new pruning path (which only touches
// MaxHops-based branch cuts, not the separate MinHops emission gate).
func TestGraphMatchReachabilityPruningRespectsMinHops(t *testing.T) {
	q := `
let E = datatable (Src: string, Dst: string) ["a","TARGET", "a","m", "m","TARGET"];
E | make-graph Src --> Dst
| graph-match (s)-[e*2..3]->(t) where s.NodeId == "a" and t.NodeId == "TARGET"
  project Hops = array_length(e)
`
	tbl := queryResult(t, q)
	// The direct 1-hop a->TARGET edge must be excluded (MinHops=2);
	// only the 2-hop a->m->TARGET route qualifies.
	expectRows(t, tbl, 1)
	expectCell(t, tbl, 0, 0, "2")
}
