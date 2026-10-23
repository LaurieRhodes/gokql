package engine

// graph_index.go — integer node ordinals for traversal.
//
// A graph is built keyed by formatted node ids (graphKey), which is
// what make-graph's merge semantics need. Traversal must not format a
// key per hop, so the first traversal builds an index: each node's
// ordinal is its position in NodeIDs, each edge's endpoints are
// ordinals, and adjacency is compressed (CSR: offsets into one flat
// edge list). It holds no pointers, which keeps it out of the
// garbage collector's way on large graphs.

import (
	"encoding/binary"
	"sync"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

type graphIndex struct {
	ords     map[string]int32 // graphKey → ordinal
	src, dst []int32          // per edge; -1 when the endpoint is not a node
	outOff   []int32          // ordinal o's out-edges: out[outOff[o]:outOff[o+1]]
	out      []int32

	inOnce sync.Once
	inOff  []int32
	in     []int32
}

// appendGraphKey appends graphKey(v) to b. The encodings are distinct
// per type, as typedKey's are: long 1 and string "1" are different
// nodes.
func appendGraphKey(b []byte, v types.Value) []byte {
	switch x := v.(type) {
	case int64:
		b = append(b, 'l')
		return binary.LittleEndian.AppendUint64(b, uint64(x))
	case int32:
		b = append(b, 'i')
		return binary.LittleEndian.AppendUint32(b, uint32(x))
	case string:
		b = append(b, 's')
		return append(b, x...)
	}
	b = append(b, 'x')
	return append(b, typedKey(v)...)
}

// graphKey formats a node id value as a map key.
func graphKey(v types.Value) string {
	var buf [24]byte
	return string(appendGraphKey(buf[:0], v))
}

// idx returns the graph's traversal index, building it on first use.
// A graph derived from another with the same nodes and edges (a GQL
// view, a node-property clone) shares its source's index.
func (g *Graph) idx() *graphIndex {
	g.idxOnce.Do(func() {
		if g.index == nil {
			g.index = buildGraphIndex(g)
		}
	})
	return g.index
}

func buildGraphIndex(g *Graph) *graphIndex {
	n := len(g.NodeIDs)
	ix := &graphIndex{ords: make(map[string]int32, n)}
	for i, id := range g.NodeIDs {
		ix.ords[graphKey(id)] = int32(i)
	}
	m := len(g.Edges)
	ix.src = make([]int32, m)
	ix.dst = make([]int32, m)
	var buf [24]byte
	lookup := func(v types.Value) int32 {
		if v == nil {
			return -1
		}
		if o, ok := ix.ords[string(appendGraphKey(buf[:0], v))]; ok {
			return o
		}
		return -1
	}
	for ei := range g.Edges {
		ix.src[ei] = lookup(g.Edges[ei].Source)
		ix.dst[ei] = lookup(g.Edges[ei].Target)
	}
	ix.outOff, ix.out = csr(n, ix.src)
	return ix
}

// csr groups edge indices by endpoint ordinal, in edge order.
func csr(n int, ends []int32) (off, flat []int32) {
	off = make([]int32, n+1)
	for _, o := range ends {
		if o >= 0 {
			off[o+1]++
		}
	}
	for i := 0; i < n; i++ {
		off[i+1] += off[i]
	}
	flat = make([]int32, off[n])
	next := make([]int32, n)
	copy(next, off[:n])
	for ei, o := range ends {
		if o >= 0 {
			flat[next[o]] = int32(ei)
			next[o]++
		}
	}
	return off, flat
}

// outEdges / inEdges are the edges leaving / entering node ordinal o.
func (ix *graphIndex) outEdges(o int32) []int32 { return ix.out[ix.outOff[o]:ix.outOff[o+1]] }

func (ix *graphIndex) inEdges(o int32) []int32 {
	ix.inOnce.Do(func() { ix.inOff, ix.in = csr(len(ix.outOff)-1, ix.dst) })
	return ix.in[ix.inOff[o]:ix.inOff[o+1]]
}

// ordOf is the ordinal of node id v (ok=false: not a node).
func (g *Graph) ordOf(v types.Value) (int32, bool) {
	if v == nil {
		return -1, false
	}
	var buf [24]byte
	o, ok := g.idx().ords[string(appendGraphKey(buf[:0], v))]
	return o, ok
}

// nodeRowsByOrd is NodeRows by ordinal (nil = edge-only node), built
// on first use; setNodeProperty resets it.
func (g *Graph) nodeRowsByOrd() []types.Row {
	g.rowsMu.Lock()
	defer g.rowsMu.Unlock()
	if g.rowsByOrd == nil {
		rows := make([]types.Row, len(g.NodeIDs))
		if len(g.NodeRows) > 0 {
			for i, id := range g.NodeIDs {
				rows[i] = g.NodeRows[graphKey(id)]
			}
		}
		g.rowsByOrd = rows
	}
	return g.rowsByOrd
}

// nodeLabelsByOrd is nodeLabels by ordinal (nil when the graph has
// none).
func (g *Graph) nodeLabelsByOrd() [][]string {
	if g.nodeLabels == nil {
		return nil
	}
	g.rowsMu.Lock()
	defer g.rowsMu.Unlock()
	if g.labelsByOrd == nil {
		ls := make([][]string, len(g.NodeIDs))
		for i, id := range g.NodeIDs {
			ls[i] = g.nodeLabels[graphKey(id)]
		}
		g.labelsByOrd = ls
	}
	return g.labelsByOrd
}

// nodeElemRowOrd is nodeElemRow for node ordinal o.
func (g *Graph) nodeElemRowOrd(o int32) types.Row {
	id := g.NodeIDs[o]
	if !g.hasNodeTable {
		return types.Row{g.derivedNodeIdValue(id)}
	}
	if row := g.nodeRowsByOrd()[o]; row != nil {
		return row
	}
	return g.edgeOnlyNodeRow(id)
}

// nodeLabelsJSONOrd is nodeLabelsJSON for node ordinal o.
func (g *Graph) nodeLabelsJSONOrd(o int32) string {
	if g.nodeLabelsJS != nil {
		return g.nodeLabelsJS[o]
	}
	ls := g.nodeLabelsByOrd()
	if ls == nil {
		return "[]"
	}
	return labelsJSON(ls[o])
}
