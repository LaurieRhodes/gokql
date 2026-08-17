package engine

// graph_reach.go — reverse-reachability pruning for path-enumeration
// graph-match (backlog P3 item 16).
//
// Measured live before this fix: a single-edge-spec pattern with BOTH
// endpoints pinned by equality predicates — the canonical evidence-
// path shape, `graph-match (a)-[e*1..3]-(b) where a.NodeId == "X" and
// b.NodeId == "Y"` — took 1.3s on a 100K-edge synthetic benchmark, no
// faster than leaving the target unconstrained. The terminal predicate
// was only ever CHECKED after full expansion reached that depth; it
// never PRUNED anything, so fixing the target bought nothing.
//
// Fix: when both endpoints are equality-pinned, BFS once from the
// target over the REVERSED graph (up to the pattern's max hop budget)
// to compute, for every node, its minimum hop-distance back to the
// target. During the forward DFS, a branch is pruned the moment
// (remaining hop budget) < (that node's distance to target) — it is
// then provably impossible for continuing that branch to reach the
// target in time, so nothing is lost by cutting it. This changes
// nothing about WHICH paths are found (still exhaustive over the true
// answer set), only how much of the search space is explored to find
// them.
//
// Scoped to single-edge-spec patterns, matching the same boundary
// `distinct` mode uses: reachability-to-a-fixed-target doesn't compose
// cleanly with continuing a longer multi-hop pattern shape.

import (
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// detectFixedEndpoints inspects preds for equality conjuncts pinning
// the start (pos 0) and end (pos len(op.Edges)) node identity fields
// to literal values. Returns ok=false if the pattern isn't a single
// edge spec, or either endpoint isn't pinned this way — the caller
// falls back to unpruned search in that case, which is always correct,
// just potentially slower.
func detectFixedEndpoints(op *parser.GraphMatchOp, preds []matchPredicate) (startVal, endVal string, ok bool) {
	if len(op.Edges) != 1 {
		return "", "", false
	}
	endPos := len(op.Edges)
	for _, p := range preds {
		if p.pos != 0 && p.pos != endPos {
			continue
		}
		be, isBinary := p.expr.(*parser.BinaryExpr)
		if !isBinary || be.Op != parser.OpEQ {
			continue
		}
		lit, litOnRight := be.Right.(*parser.Literal)
		field := be.Left
		if !litOnRight {
			lit, ok = be.Left.(*parser.Literal)
			if !ok {
				continue
			}
			field = be.Right
		}
		access, isAccess := field.(*parser.AccessExpr)
		if !isAccess || len(access.Path) != 1 {
			continue
		}
		strVal, isStr := lit.Value.(string)
		if !isStr {
			continue
		}
		if p.pos == 0 {
			startVal = strVal
		} else {
			endVal = strVal
		}
	}
	return startVal, endVal, startVal != "" && endVal != ""
}

// reachabilityToTarget BFS's the REVERSED graph from targetKey, up to
// maxHops deep, returning each reached node's minimum distance back to
// the target. A node absent from the map is unreachable to the target
// within maxHops at all.
func reachabilityToTarget(g *Graph, targetKey string, direction parser.EdgeDirection, maxHops int) map[string]int {
	// Reachability over the reversed graph under `direction` is
	// reachability-TO the target under `direction` in the forward
	// graph: a forward edge under `direction` is a reverse edge under
	// the opposite direction, so BFS-ing backward from the target
	// using the FLIPPED direction directly gives forward distance-to-
	// target, without needing a separate reverse-adjacency structure
	// beyond what neighbors() already builds internally for backward/
	// undirected edges.
	flipped := direction
	switch direction {
	case parser.EdgeForward:
		flipped = parser.EdgeBackward
	case parser.EdgeBackward:
		flipped = parser.EdgeForward
	} // EdgeAny stays EdgeAny — undirected reachability is symmetric

	var radj map[string][]int
	if flipped != parser.EdgeForward {
		radj = make(map[string][]int, len(g.Adjacency))
		for ei, edge := range g.Edges {
			k := graphKey(edge.Target)
			radj[k] = append(radj[k], ei)
		}
	}
	neighbors := func(key string, fn func(next types.Value)) {
		if flipped != parser.EdgeBackward {
			for _, ei := range g.Adjacency[key] {
				fn(g.Edges[ei].Target)
			}
		}
		if flipped != parser.EdgeForward {
			for _, ei := range radj[key] {
				fn(g.Edges[ei].Source)
			}
		}
	}

	dist := map[string]int{targetKey: 0}
	frontier := []string{targetKey}
	for depth := 1; depth <= maxHops && len(frontier) > 0; depth++ {
		var next []string
		for _, key := range frontier {
			neighbors(key, func(nx types.Value) {
				nk := graphKey(nx)
				if _, seen := dist[nk]; seen {
					return
				}
				dist[nk] = depth
				next = append(next, nk)
			})
		}
		frontier = next
	}
	return dist
}
