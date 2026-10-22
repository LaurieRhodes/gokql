package engine

// graph_components.go — graph-mark-components.

import (
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// setNodeProperty sets property name on every node to vals[i] (indexed
// like g.NodeIDs), adding the property when it does not exist. A graph
// without node tables is first given explicit node rows holding its id
// property, so the new property sits beside it.
func (g *Graph) setNodeProperty(name string, typ types.KQLType, vals []types.Value) {
	if !g.hasNodeTable {
		g.NodeSchema = types.Schema{Columns: []types.Column{{Name: g.NodeIdName, Type: g.derivedNodeIdType()}}}
		g.NodeIdIdx = 0
		if g.NodeIdName != "NodeId" {
			g.withNodeIdIdx = 0
		}
		for _, id := range g.NodeIDs {
			g.NodeRows[graphKey(id)] = types.Row{g.derivedNodeIdValue(id)}
		}
		g.hasNodeTable = true
	}
	col := g.NodeSchema.ColumnIndex(name)
	if col < 0 {
		col = len(g.NodeSchema.Columns)
		g.NodeSchema.Columns = append(g.NodeSchema.Columns, types.Column{Name: name, Type: typ})
	} else {
		g.NodeSchema.Columns[col].Type = typ
	}
	width := len(g.NodeSchema.Columns)
	for i, id := range g.NodeIDs {
		key := graphKey(id)
		row := g.nodeElemRow(id)
		if len(row) < width {
			grown := make(types.Row, width)
			copy(grown, row)
			row = grown
		}
		row[col] = vals[i]
		g.NodeRows[key] = row
	}
	g.elemMu.Lock()
	g.elemSchemas = nil
	g.elemMu.Unlock()
}

// markComponents assigns each node a zero-based component index,
// numbered in first-node order. weak: edges taken as undirected;
// strong: mutually reachable nodes (Tarjan's algorithm, iterative).
func markComponents(g *Graph, op *parser.GraphMarkComponentsOp) {
	n := len(g.NodeIDs)
	index := make(map[string]int, n)
	for i, id := range g.NodeIDs {
		index[graphKey(id)] = i
	}
	comp := make([]int, n)
	if op.Kind == "strong" {
		comp = strongComponents(g, index)
	} else {
		parent := make([]int, n)
		for i := range parent {
			parent[i] = i
		}
		var find func(int) int
		find = func(x int) int {
			for parent[x] != x {
				parent[x] = parent[parent[x]]
				x = parent[x]
			}
			return x
		}
		for _, e := range g.Edges {
			a, b := find(index[graphKey(e.Source)]), find(index[graphKey(e.Target)])
			if a != b {
				if a < b {
					parent[b] = a
				} else {
					parent[a] = b
				}
			}
		}
		for i := range comp {
			comp[i] = find(i)
		}
	}
	// Renumber consecutively in first-node order.
	ids := make(map[int]int)
	vals := make([]types.Value, n)
	for i, c := range comp {
		id, ok := ids[c]
		if !ok {
			id = len(ids)
			ids[c] = id
		}
		vals[i] = int64(id)
	}
	g.setNodeProperty(op.ComponentId, types.TypeLong, vals)
}

// strongComponents labels nodes by strongly connected component
// (arbitrary labels; markComponents renumbers them).
func strongComponents(g *Graph, index map[string]int) []int {
	n := len(g.NodeIDs)
	adj := make([][]int, n)
	for _, e := range g.Edges {
		a, b := index[graphKey(e.Source)], index[graphKey(e.Target)]
		adj[a] = append(adj[a], b)
	}
	const unvisited = -1
	order := make([]int, n)
	low := make([]int, n)
	onStack := make([]bool, n)
	comp := make([]int, n)
	for i := range order {
		order[i] = unvisited
	}
	var stack []int
	counter, label := 0, 0
	type frame struct{ v, next int }
	for root := 0; root < n; root++ {
		if order[root] != unvisited {
			continue
		}
		call := []frame{{root, 0}}
		order[root], low[root] = counter, counter
		counter++
		stack = append(stack, root)
		onStack[root] = true
		for len(call) > 0 {
			f := &call[len(call)-1]
			if f.next < len(adj[f.v]) {
				w := adj[f.v][f.next]
				f.next++
				if order[w] == unvisited {
					order[w], low[w] = counter, counter
					counter++
					stack = append(stack, w)
					onStack[w] = true
					call = append(call, frame{w, 0})
				} else if onStack[w] && order[w] < low[f.v] {
					low[f.v] = order[w]
				}
				continue
			}
			v := f.v
			call = call[:len(call)-1]
			if len(call) > 0 {
				if p := call[len(call)-1].v; low[v] < low[p] {
					low[p] = low[v]
				}
			}
			if low[v] == order[v] {
				for {
					w := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					onStack[w] = false
					comp[w] = label
					if w == v {
						break
					}
				}
				label++
			}
		}
	}
	return comp
}
