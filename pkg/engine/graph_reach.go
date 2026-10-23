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
)

// detectFixedEndpoints inspects preds for equality conjuncts pinning
// the start (pos 0) and end (pos endPos) node identity fields to
// literal values. Returns ok=false if either endpoint isn't pinned
// this way — the caller
// falls back to unpruned search in that case, which is always correct,
// just potentially slower.
func detectFixedEndpoints(preds []matchPredicate, endPos int) (startVal, endVal string, ok bool) {
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

// reachabilityToTarget BFS's the REVERSED graph from node ordinal
// target, up to maxHops deep, returning each node's minimum distance
// back to the target by ordinal (-1: unreachable within maxHops).
func reachabilityToTarget(g *Graph, target int32, direction parser.EdgeDirection, maxHops int) []int32 {
	// Reachability over the reversed graph under `direction` is
	// reachability-TO the target under `direction` in the forward
	// graph, so BFS backward from the target with the FLIPPED direction.
	flipped := direction
	switch direction {
	case parser.EdgeForward:
		flipped = parser.EdgeBackward
	case parser.EdgeBackward:
		flipped = parser.EdgeForward
	} // EdgeAny stays EdgeAny — undirected reachability is symmetric

	ix := g.idx()
	dist := make([]int32, len(g.NodeIDs))
	for i := range dist {
		dist[i] = -1
	}
	visit := func(nx int32, depth int, next []int32) []int32 {
		if nx < 0 || dist[nx] >= 0 {
			return next
		}
		dist[nx] = int32(depth)
		return append(next, nx)
	}
	dist[target] = 0
	frontier := []int32{target}
	for depth := 1; depth <= maxHops && len(frontier) > 0; depth++ {
		var next []int32
		for _, o := range frontier {
			if flipped != parser.EdgeBackward {
				for _, ei := range ix.outEdges(o) {
					next = visit(ix.dst[ei], depth, next)
				}
			}
			if flipped != parser.EdgeForward {
				for _, ei := range ix.inEdges(o) {
					next = visit(ix.src[ei], depth, next)
				}
			}
		}
		frontier = next
	}
	return dist
}
