package engine

// graph_bfs.go — distinct-node BFS for graph-match, opt in via
// -[e*min..max distinct]->.
//
// The default variable-length graph-match walk (graph.go) enumerates
// every distinct PATH within the hop range: correct and necessary for
// evidence-path rendering (strcat_array(e.Rel, " / ") over all routes
// between two entities), but combinatorial in hub-node degree — a
// node with degree D reachable at depth k can be re-entered via every
// one of D's incoming routes, multiplying path count hop over hop.
// Measured on a 100K-edge synthetic benchmark: a degree-395 hub at
// 1..3 hops enumerated 398,370 paths while ~100ms was spent on the
// graph build itself — the traversal, not the storage layer, is what
// doesn't scale for disclosure-shaped queries.
//
// Disclosure queries ("what's near this entity") want reachable NODES,
// not path variants. This is a real breadth-first search: each node
// is visited at most once, at its shallowest discovered depth, with
// one arbitrary shortest witness path retained per node for `e.Rel`/
// `array_length(e)` style projections. Cost is O(nodes + edges)
// instead of O(paths).

import (
	"fmt"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func (e *Engine) applyGraphMatchDistinctBFS(g *Graph, op *parser.GraphMatchOp, lay *matchLayout, preds []matchPredicate) (*types.Table, error) {
	spec := op.Edges[0]

	matches := types.NewTable("", lay.schema)
	row := make(types.Row, len(lay.schema.Columns))

	checkPreds := func(pos int) (bool, error) {
		for _, p := range preds {
			if p.pos != pos {
				continue
			}
			keep, evalErr := evalExpr(p.expr, &lay.schema, row)
			if evalErr != nil {
				return false, fmt.Errorf("graph-match where: %w", evalErr)
			}
			if b, ok := keep.(bool); !ok || !b {
				return false, nil
			}
		}
		return true, nil
	}

	emit := func() error {
		if len(matches.Rows) >= maxGraphMatches {
			return fmt.Errorf("graph-match: result exceeds %d rows; narrow the pattern or where clause", maxGraphMatches)
		}
		out := make(types.Row, len(row))
		copy(out, row)
		matches.AddRow(out)
		return nil
	}

	// Reverse adjacency, built once, only if this edge ever traverses
	// backward or undirected.
	var radj map[string][]int
	if spec.Direction != parser.EdgeForward {
		radj = make(map[string][]int, len(g.Adjacency))
		for ei, edge := range g.Edges {
			k := graphKey(edge.Target)
			radj[k] = append(radj[k], ei)
		}
	}
	neighbors := func(key string, fn func(ei int, next types.Value)) {
		if spec.Direction != parser.EdgeBackward {
			for _, ei := range g.Adjacency[key] {
				fn(ei, g.Edges[ei].Target)
			}
		}
		if spec.Direction != parser.EdgeForward {
			for _, ei := range radj[key] {
				fn(ei, g.Edges[ei].Source)
			}
		}
	}

	type frontierNode struct {
		key string
		id  types.Value
	}

	for _, startID := range g.NodeIDs {
		lay.writeNode(g, row, 0, startID)
		keep, err := checkPreds(0)
		if err != nil {
			return nil, err
		}
		if !keep {
			continue
		}

		startKey := graphKey(startID)
		visited := map[string]bool{startKey: true}
		pathTo := map[string][]int{startKey: nil}
		frontier := []frontierNode{{startKey, startID}}

		for depth := 1; depth <= spec.MaxHops && len(frontier) > 0; depth++ {
			var next []frontierNode
			for _, cur := range frontier {
				curPath := pathTo[cur.key]
				neighbors(cur.key, func(ei int, nx types.Value) {
					nk := graphKey(nx)
					if visited[nk] {
						return
					}
					visited[nk] = true
					p := make([]int, len(curPath)+1)
					copy(p, curPath)
					p[len(curPath)] = ei
					pathTo[nk] = p
					next = append(next, frontierNode{nk, nx})
				})
			}

			if depth >= spec.MinHops {
				for _, item := range next {
					if listErr := lay.writeEdgeList(g, row, 0, pathTo[item.key]); listErr != nil {
						return nil, listErr
					}
					lay.writeNode(g, row, 1, item.id)
					keep, checkErr := checkPreds(1)
					if checkErr != nil {
						return nil, checkErr
					}
					if keep {
						if err := emit(); err != nil {
							return nil, err
						}
					}
				}
			}
			frontier = next
		}
	}

	return e.applyProject(matches, op.Project)
}
