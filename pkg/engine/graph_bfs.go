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

func (e *Engine) applyGraphMatchDistinctBFS(g *Graph, op *parser.GraphMatchOp, plan *gmPlan, preds []matchPredicate) (*types.Table, error) {
	spec := plan.elems[0].spec
	dir := plan.steps[0].dir
	endVar := plan.steps[0].to

	matches := types.NewTable("", plan.schema)
	row := make(types.Row, len(plan.schema.Columns))

	checkPreds := func(pos int) (bool, error) {
		for _, p := range preds {
			if p.pos != pos {
				continue
			}
			keep, evalErr := evalExpr(e, p.expr, &plan.schema, row)
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

	ix := g.idx()
	neighbors := func(o int32, fn func(ei int, nx int32)) {
		if dir != parser.EdgeBackward {
			for _, ei := range ix.outEdges(o) {
				if nx := ix.dst[ei]; nx >= 0 {
					fn(int(ei), nx)
				}
			}
		}
		if dir != parser.EdgeForward {
			for _, ei := range ix.inEdges(o) {
				if nx := ix.src[ei]; nx >= 0 {
					fn(int(ei), nx)
				}
			}
		}
	}

	// Per-start visited marks: stamp[o] == gen means visited in this
	// search, so starting over costs nothing.
	stamp := make([]int32, len(g.NodeIDs))
	var gen int32
	for so := range g.NodeIDs {
		start := int32(so)
		startID := g.NodeIDs[so]
		plan.writeNodeOrd(g, row, 0, start)
		keep, err := checkPreds(0)
		if err != nil {
			return nil, err
		}
		if !keep {
			continue
		}

		gen++
		stamp[start] = gen
		pathTo := map[int32][]int{start: nil}
		frontier := []int32{start}

		if spec.MinHops == 0 { // zero-length: the start node itself
			if err := plan.writePath(g, row, 0, startID, nil); err != nil {
				return nil, err
			}
			plan.writeNodeOrd(g, row, endVar, start)
			keep, err := checkPreds(1)
			if err != nil {
				return nil, err
			}
			if keep {
				if err := emit(); err != nil {
					return nil, err
				}
			}
		}

		for depth := 1; depth <= spec.MaxHops && len(frontier) > 0; depth++ {
			var next []int32
			for _, cur := range frontier {
				curPath := pathTo[cur]
				neighbors(cur, func(ei int, nx int32) {
					if stamp[nx] == gen {
						return
					}
					stamp[nx] = gen
					p := make([]int, len(curPath)+1)
					copy(p, curPath)
					p[len(curPath)] = ei
					pathTo[nx] = p
					next = append(next, nx)
				})
			}

			if depth >= spec.MinHops {
				for _, item := range next {
					if listErr := plan.writePath(g, row, 0, startID, pathTo[item]); listErr != nil {
						return nil, listErr
					}
					plan.writeNodeOrd(g, row, endVar, item)
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

	return e.applyProject(matches, plan.projectWithGraphNames(op.Project))
}
