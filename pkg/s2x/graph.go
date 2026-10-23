package s2x

// Port of S2Builder::Graph (s2builder_graph.{h,cc}).

import (
	"errors"
	"math"
	"slices"
	"sort"

	"github.com/golang/geo/s2"
)

// EdgeType says whether input edges are directed.
type EdgeType uint8

const (
	EdgeDirected EdgeType = iota
	EdgeUndirected
)

// DegenerateEdges, DuplicateEdges and SiblingPairs are GraphOptions
// settings.
type DegenerateEdges uint8

const (
	DegenerateDiscard DegenerateEdges = iota
	DegenerateDiscardExcess
	DegenerateKeep
)

type DuplicateEdges uint8

const (
	DuplicateMerge DuplicateEdges = iota
	DuplicateKeep
)

type SiblingPairs uint8

const (
	SiblingDiscard SiblingPairs = iota
	SiblingDiscardExcess
	SiblingKeep
	SiblingRequire
	SiblingCreate
)

// GraphOptions is S2Builder::GraphOptions.
type GraphOptions struct {
	EdgeType             EdgeType
	DegenerateEdges      DegenerateEdges
	DuplicateEdges       DuplicateEdges
	SiblingPairs         SiblingPairs
	AllowVertexFiltering bool
}

// DefaultGraphOptions is GraphOptions(): directed, everything kept.
func DefaultGraphOptions() GraphOptions {
	return GraphOptions{EdgeDirected, DegenerateKeep, DuplicateKeep, SiblingKeep, true}
}

// NewGraphOptions mirrors the four-argument C++ constructor.
func NewGraphOptions(et EdgeType, de DegenerateEdges, du DuplicateEdges, sp SiblingPairs) GraphOptions {
	return GraphOptions{et, de, du, sp, true}
}

// Builder errors (S2Error codes).
var (
	ErrEdgesDoNotFormLoops         = errors.New("s2builder: given edges do not form loops (indegree != outdegree)")
	ErrUndirectedEdgesDoNotForm    = errors.New("s2builder: given undirected edges do not form loops")
	ErrMissingExpectedSiblingEdges = errors.New("s2builder: expected all input edges to have siblings, but some were missing")
	ErrSnapRadiusTooSmall          = errors.New("s2builder: snap function moved a vertex by more than the snap radius")
	ErrIsFullPredicateNotSpecified = errors.New("s2builder: a degenerate polygon was found, but no predicate was specified to determine whether the polygon is empty or full")
)

// VertexID, EdgeID, InputEdgeID and set ids are int32 as in C++.
type (
	VertexID         = int32
	EdgeID           = int32
	InputEdgeID      = int32
	InputEdgeIDSetID = int32
	LabelSetID       = int32
	Label            = int32
)

// Edge is a pair of vertex ids.
type Edge struct{ V0, V1 VertexID }

func (e Edge) reverse() Edge { return Edge{e.V1, e.V0} }

func edgeLess(a, b Edge) bool { return a.V0 < b.V0 || (a.V0 == b.V0 && a.V1 < b.V1) }

func edgeCmp(a, b Edge) int {
	if a.V0 != b.V0 {
		if a.V0 < b.V0 {
			return -1
		}
		return 1
	}
	if a.V1 != b.V1 {
		if a.V1 < b.V1 {
			return -1
		}
		return 1
	}
	return 0
}

func minEdge(a, b Edge) Edge {
	if edgeLess(b, a) {
		return b
	}
	return a
}

func stableLessThan(a, b Edge, ai, bi int32) bool {
	if c := edgeCmp(a, b); c != 0 {
		return c < 0
	}
	return ai < bi
}

const (
	maxInputEdgeID InputEdgeID = math.MaxInt32
	noInputEdgeID  InputEdgeID = maxInputEdgeID - 1
)

// IsFullPolygonPredicate decides whether a degenerate polygon is full.
type IsFullPolygonPredicate func(g *Graph) (bool, error)

// IsFullPolygon returns a predicate with a fixed answer.
func IsFullPolygon(full bool) IsFullPolygonPredicate {
	return func(*Graph) (bool, error) { return full, nil }
}

// Graph is S2Builder::Graph: a read-only view of a layer's snapped edges.
type Graph struct {
	opts              GraphOptions
	vertices          []s2.Point
	edges             []Edge
	inputEdgeIDSetIDs []InputEdgeIDSetID
	inputEdgeIDSetLex *idSetLexicon
	labelSetIDs       []LabelSetID
	labelSetLex       *idSetLexicon
	isFullPolygonPred IsFullPolygonPredicate
}

func newGraph(opts GraphOptions, vertices []s2.Point, edges []Edge, inputIDs []InputEdgeIDSetID,
	inputLex *idSetLexicon, labelSetIDs []LabelSetID, labelLex *idSetLexicon, pred IsFullPolygonPredicate) *Graph {
	return &Graph{opts, vertices, edges, inputIDs, inputLex, labelSetIDs, labelLex, pred}
}

func (g *Graph) Options() GraphOptions           { return g.opts }
func (g *Graph) NumVertices() int                { return len(g.vertices) }
func (g *Graph) Vertex(v VertexID) s2.Point      { return g.vertices[v] }
func (g *Graph) Vertices() []s2.Point            { return g.vertices }
func (g *Graph) NumEdges() int                   { return len(g.edges) }
func (g *Graph) Edge(e EdgeID) Edge              { return g.edges[e] }
func (g *Graph) Edges() []Edge                   { return g.edges }
func (g *Graph) InputEdgeIDSetID(e EdgeID) int32 { return g.inputEdgeIDSetIDs[e] }

// InputEdgeIDs returns the sorted input edge ids of graph edge e.
func (g *Graph) InputEdgeIDs(e EdgeID) []InputEdgeID {
	return g.inputEdgeIDSetLex.idSet(g.inputEdgeIDSetIDs[e])
}

// LabelSetID returns the label set id of input edge e.
func (g *Graph) LabelSetID(e InputEdgeID) LabelSetID {
	if len(g.labelSetIDs) == 0 {
		return emptySetID
	}
	return g.labelSetIDs[e]
}

// Labels returns the labels of input edge e.
func (g *Graph) Labels(e InputEdgeID) []Label { return g.labelSetLex.idSet(g.LabelSetID(e)) }

// IsFullPolygon evaluates the layer's is-full predicate.
func (g *Graph) IsFullPolygon() (bool, error) { return g.isFullPolygonPred(g) }

// GetInEdgeIDs returns edge ids sorted by (destination, origin).
func (g *Graph) GetInEdgeIDs() []EdgeID {
	ids := make([]EdgeID, len(g.edges))
	for i := range ids {
		ids[i] = EdgeID(i)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := ids[i], ids[j]
		return stableLessThan(g.edges[a].reverse(), g.edges[b].reverse(), a, b)
	})
	return ids
}

// GetSiblingMap maps each edge to its sibling (reverse) edge.
func (g *Graph) GetSiblingMap() []EdgeID {
	ids := g.GetInEdgeIDs()
	g.makeSiblingMap(ids)
	return ids
}

func (g *Graph) makeSiblingMap(inEdgeIDs []EdgeID) {
	if g.opts.EdgeType == EdgeDirected {
		return
	}
	if g.opts.DegenerateEdges == DegenerateDiscard {
		return
	}
	for e := 0; e < len(g.edges); e++ {
		v := g.edges[e].V0
		if g.edges[e].V1 == v {
			inEdgeIDs[e] = EdgeID(e + 1)
			inEdgeIDs[e+1] = EdgeID(e)
			e++
		}
	}
}

// VertexOutMap gives the outgoing edges of each vertex.
type VertexOutMap struct {
	edges      []Edge
	edgeBegins []EdgeID
}

func NewVertexOutMap(g *Graph) *VertexOutMap {
	m := &VertexOutMap{edges: g.edges, edgeBegins: make([]EdgeID, 0, g.NumVertices()+1)}
	e := 0
	for v := 0; v <= g.NumVertices(); v++ {
		for e < len(g.edges) && int(g.edges[e].V0) < v {
			e++
		}
		m.edgeBegins = append(m.edgeBegins, EdgeID(e))
	}
	return m
}

func (m *VertexOutMap) Degree(v VertexID) int { return int(m.edgeBegins[v+1] - m.edgeBegins[v]) }

// EdgeIDs returns the range [begin, end) of edge ids leaving v.
func (m *VertexOutMap) EdgeIDs(v VertexID) (EdgeID, EdgeID) {
	return m.edgeBegins[v], m.edgeBegins[v+1]
}

// EdgeIDsBetween returns the range of edge ids from v0 to v1.
func (m *VertexOutMap) EdgeIDsBetween(v0, v1 VertexID) (EdgeID, EdgeID) {
	b, e := m.edgeBegins[v0], m.edgeBegins[v0+1]
	s := m.edges[b:e]
	lo := sort.Search(len(s), func(i int) bool { return !edgeLess(s[i], Edge{v0, v1}) })
	hi := sort.Search(len(s), func(i int) bool { return edgeLess(Edge{v0, v1}, s[i]) })
	return b + EdgeID(lo), b + EdgeID(hi)
}

// VertexInMap gives the incoming edges of each vertex.
type VertexInMap struct {
	inEdgeIDs    []EdgeID
	inEdgeBegins []EdgeID
}

func NewVertexInMap(g *Graph) *VertexInMap {
	m := &VertexInMap{inEdgeIDs: g.GetInEdgeIDs(), inEdgeBegins: make([]EdgeID, 0, g.NumVertices()+1)}
	e := 0
	for v := 0; v <= g.NumVertices(); v++ {
		for e < len(g.edges) && int(g.edges[m.inEdgeIDs[e]].V1) < v {
			e++
		}
		m.inEdgeBegins = append(m.inEdgeBegins, EdgeID(e))
	}
	return m
}

func (m *VertexInMap) Degree(v VertexID) int { return int(m.inEdgeBegins[v+1] - m.inEdgeBegins[v]) }

func (m *VertexInMap) EdgeIDs(v VertexID) []EdgeID {
	return m.inEdgeIDs[m.inEdgeBegins[v]:m.inEdgeBegins[v+1]]
}

func (m *VertexInMap) InEdgeIDs() []EdgeID { return m.inEdgeIDs }

// LabelFetcher fetches the labels of graph edges.
type LabelFetcher struct {
	g          *Graph
	edgeType   EdgeType
	siblingMap []EdgeID
}

func NewLabelFetcher(g *Graph, et EdgeType) *LabelFetcher {
	f := &LabelFetcher{g: g, edgeType: et}
	if et == EdgeUndirected {
		f.siblingMap = g.GetSiblingMap()
	}
	return f
}

func (f *LabelFetcher) Fetch(e EdgeID, labels []Label) []Label {
	labels = labels[:0]
	for _, id := range f.g.InputEdgeIDs(e) {
		labels = append(labels, f.g.Labels(id)...)
	}
	if f.edgeType == EdgeUndirected {
		for _, id := range f.g.InputEdgeIDs(f.siblingMap[e]) {
			labels = append(labels, f.g.Labels(id)...)
		}
	}
	if len(labels) > 1 {
		slices.Sort(labels)
		labels = slices.Compact(labels)
	}
	return labels
}

func (g *Graph) minInputEdgeID(e EdgeID) InputEdgeID {
	ids := g.InputEdgeIDs(e)
	if len(ids) == 0 {
		return noInputEdgeID
	}
	return ids[0]
}

// GetMinInputEdgeIDs returns the minimum input edge id of every edge.
func (g *Graph) GetMinInputEdgeIDs() []InputEdgeID {
	out := make([]InputEdgeID, len(g.edges))
	for e := range out {
		out[e] = g.minInputEdgeID(EdgeID(e))
	}
	return out
}

// GetInputEdgeOrder returns edge ids ordered by (input id, edge id).
func (g *Graph) GetInputEdgeOrder(inputIDs []InputEdgeID) []EdgeID {
	order := make([]EdgeID, len(inputIDs))
	for i := range order {
		order[i] = EdgeID(i)
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if inputIDs[a] != inputIDs[b] {
			return inputIDs[a] < inputIDs[b]
		}
		return a < b
	})
	return order
}

type vertexEdge struct {
	incoming bool
	index    EdgeID
	endpoint VertexID
	rank     int32
}

func addVertexEdges(outBegin, outEnd, inBegin, inEnd EdgeID, v1 VertexID, v0Edges []vertexEdge) []vertexEdge {
	rank := int32(0)
	for inEnd-inBegin > outEnd-outBegin {
		inEnd--
		v0Edges = append(v0Edges, vertexEdge{true, inEnd, v1, rank})
		rank++
	}
	for inEnd > inBegin {
		v0Edges = append(v0Edges, vertexEdge{false, outBegin, v1, rank})
		outBegin++
		rank++
		inEnd--
		v0Edges = append(v0Edges, vertexEdge{true, inEnd, v1, rank})
		rank++
	}
	for outEnd > outBegin {
		v0Edges = append(v0Edges, vertexEdge{false, outBegin, v1, rank})
		outBegin++
		rank++
	}
	return v0Edges
}

// GetLeftTurnMap maps each incoming edge to the outgoing edge that makes
// the sharpest left turn at its destination.
func (g *Graph) GetLeftTurnMap(inEdgeIDs []EdgeID) ([]EdgeID, error) {
	ltm := make([]EdgeID, len(g.edges))
	for i := range ltm {
		ltm[i] = -1
	}
	if len(g.edges) == 0 {
		return ltm, nil
	}
	var err error
	var v0Edges []vertexEdge
	var eIn, eOut []EdgeID
	out, in := 0, 0
	n := len(g.edges)
	sentinel := Edge{VertexID(g.NumVertices()), VertexID(g.NumVertices())}
	outEdge := g.edges[out]
	inEdge := g.edges[inEdgeIDs[in]]
	me := minEdge(outEdge, inEdge.reverse())
	for me != sentinel {
		v0 := me.V0
		for ; me.V0 == v0; me = minEdge(outEdge, inEdge.reverse()) {
			v1 := me.V1
			outBegin, inBegin := out, in
			for outEdge == me {
				out++
				if out == n {
					outEdge = sentinel
				} else {
					outEdge = g.edges[out]
				}
			}
			for inEdge.reverse() == me {
				in++
				if in == n {
					inEdge = sentinel
				} else {
					inEdge = g.edges[inEdgeIDs[in]]
				}
			}
			if v0 != v1 {
				v0Edges = addVertexEdges(EdgeID(outBegin), EdgeID(out), EdgeID(inBegin), EdgeID(in), v1, v0Edges)
			} else {
				for ; inBegin < in; inBegin++ {
					ltm[inBegin] = EdgeID(inBegin)
				}
			}
		}
		if len(v0Edges) == 0 {
			continue
		}
		minEndpoint := v0Edges[0].endpoint
		rest := v0Edges[1:]
		sort.SliceStable(rest, func(i, j int) bool {
			a, b := rest[i], rest[j]
			if a.endpoint == b.endpoint {
				return a.rank < b.rank
			}
			if a.endpoint == minEndpoint {
				return true
			}
			if b.endpoint == minEndpoint {
				return false
			}
			return !s2.OrderedCCW(g.vertices[a.endpoint], g.vertices[b.endpoint], g.vertices[minEndpoint], g.vertices[v0])
		})
		for _, e := range v0Edges {
			if e.incoming {
				eIn = append(eIn, inEdgeIDs[e.index])
			} else if len(eIn) > 0 {
				ltm[eIn[len(eIn)-1]] = e.index
				eIn = eIn[:len(eIn)-1]
			} else {
				eOut = append(eOut, e.index)
			}
		}
		slices.Reverse(eOut)
		for len(eOut) > 0 && len(eIn) > 0 {
			ltm[eIn[len(eIn)-1]] = eOut[len(eOut)-1]
			eOut = eOut[:len(eOut)-1]
			eIn = eIn[:len(eIn)-1]
		}
		if len(eIn) > 0 && err == nil {
			err = ErrEdgesDoNotFormLoops
		}
		eIn, eOut, v0Edges = eIn[:0], eOut[:0], v0Edges[:0]
	}
	return ltm, err
}

// CanonicalizeLoopOrder rotates a loop to start at the edge with the
// smallest input id (respecting runs).
func CanonicalizeLoopOrder(minInputIDs []InputEdgeID, loop []EdgeID) {
	if len(loop) == 0 {
		return
	}
	pos := 0
	sawGap := false
	for i := 1; i < len(loop); i++ {
		cmp := int64(minInputIDs[loop[i]]) - int64(minInputIDs[loop[pos]])
		if cmp < 0 {
			sawGap = true
		} else if cmp > 0 || !sawGap {
			pos = i
			sawGap = false
		}
	}
	pos++
	if pos == len(loop) {
		pos = 0
	}
	rotateLeft(loop, pos)
}

func rotateLeft(s []EdgeID, k int) {
	if k == 0 || len(s) == 0 {
		return
	}
	slices.Reverse(s[:k])
	slices.Reverse(s[k:])
	slices.Reverse(s)
}

// CanonicalizeVectorOrder sorts chains by their first edge's input id.
func CanonicalizeVectorOrder(minInputIDs []InputEdgeID, chains [][]EdgeID) {
	sort.SliceStable(chains, func(i, j int) bool {
		a, b := chains[i][0], chains[j][0]
		if minInputIDs[a] != minInputIDs[b] {
			return minInputIDs[a] < minInputIDs[b]
		}
		return a < b
	})
}

// LoopType selects GetDirectedLoops behaviour.
type LoopType uint8

const (
	LoopSimple LoopType = iota
	LoopCircuit
)

// GetDirectedLoops builds loops from a directed graph.
func (g *Graph) GetDirectedLoops(lt LoopType) ([][]EdgeID, error) {
	ltm, err := g.GetLeftTurnMap(g.GetInEdgeIDs())
	if err != nil {
		return nil, err
	}
	minIDs := g.GetMinInputEdgeIDs()
	var pathIndex []int
	if lt == LoopSimple {
		pathIndex = make([]int, g.NumVertices())
		for i := range pathIndex {
			pathIndex[i] = -1
		}
	}
	var loops [][]EdgeID
	var path []EdgeID
	for start := 0; start < len(g.edges); start++ {
		if ltm[start] < 0 {
			continue
		}
		for e := EdgeID(start); ltm[e] >= 0; {
			path = append(path, e)
			next := ltm[e]
			ltm[e] = -1
			if lt == LoopSimple {
				pathIndex[g.edges[e].V0] = len(path) - 1
				loopStart := pathIndex[g.edges[e].V1]
				if loopStart >= 0 {
					loop := slices.Clone(path[loopStart:])
					path = path[:loopStart]
					for _, e2 := range loop {
						pathIndex[g.edges[e2].V0] = -1
					}
					CanonicalizeLoopOrder(minIDs, loop)
					loops = append(loops, loop)
				}
			}
			e = next
		}
		if lt != LoopSimple {
			CanonicalizeLoopOrder(minIDs, path)
			loops = append(loops, path)
			path = nil
		}
	}
	CanonicalizeVectorOrder(minIDs, loops)
	return loops, nil
}

// DegenerateBoundaries selects GetDirectedComponents behaviour.
type DegenerateBoundaries uint8

const (
	DegenerateBoundariesDiscard DegenerateBoundaries = iota
	DegenerateBoundariesKeep
)

// GetDirectedComponents builds the connected components of a directed
// graph whose edges come in sibling pairs.
func (g *Graph) GetDirectedComponents(db DegenerateBoundaries) ([][][]EdgeID, error) {
	siblingMap := g.GetSiblingMap()
	ltm, err := g.GetLeftTurnMap(siblingMap)
	if err != nil {
		return nil, err
	}
	minIDs := g.GetMinInputEdgeIDs()
	var frontier []EdgeID
	var pathIndex []int
	if db == DegenerateBoundariesDiscard {
		pathIndex = make([]int, len(g.edges))
		for i := range pathIndex {
			pathIndex[i] = -1
		}
	}
	var components [][][]EdgeID
	for start := 0; start < len(g.edges); start++ {
		if ltm[start] < 0 {
			continue
		}
		var component [][]EdgeID
		frontier = append(frontier, EdgeID(start))
		for len(frontier) > 0 {
			e := frontier[len(frontier)-1]
			frontier = frontier[:len(frontier)-1]
			if ltm[e] < 0 {
				continue
			}
			var path []EdgeID
			for ltm[e] >= 0 {
				path = append(path, e)
				next := ltm[e]
				ltm[e] = -1
				sibling := siblingMap[e]
				if ltm[sibling] >= 0 {
					frontier = append(frontier, sibling)
				}
				if db == DegenerateBoundariesDiscard {
					pathIndex[e] = len(path) - 1
					sibIdx := pathIndex[sibling]
					if sibIdx >= 0 {
						if sibIdx == len(path)-2 {
							path = path[:sibIdx]
						} else {
							loop := slices.Clone(path[sibIdx+1 : len(path)-1])
							path = path[:sibIdx]
							for _, e2 := range loop {
								pathIndex[e2] = -1
							}
							CanonicalizeLoopOrder(minIDs, loop)
							component = append(component, loop)
						}
					}
				}
				e = next
			}
			if db == DegenerateBoundariesDiscard {
				for _, e2 := range path {
					pathIndex[e2] = -1
				}
			}
			CanonicalizeLoopOrder(minIDs, path)
			component = append(component, path)
		}
		CanonicalizeVectorOrder(minIDs, component)
		components = append(components, component)
	}
	sort.SliceStable(components, func(i, j int) bool {
		return minIDs[components[i][0][0]] < minIDs[components[j][0][0]]
	})
	return components, nil
}

// GetUndirectedComponents builds the components of an undirected graph;
// each component has two complementary sets of loops.
func (g *Graph) GetUndirectedComponents(lt LoopType) ([][2][][]EdgeID, error) {
	siblingMap := g.GetInEdgeIDs()
	ltm, err := g.GetLeftTurnMap(siblingMap)
	if err != nil {
		return nil, err
	}
	g.makeSiblingMap(siblingMap)
	minIDs := g.GetMinInputEdgeIDs()
	type fe struct {
		e    EdgeID
		slot int
	}
	markUsed := func(slot int) EdgeID { return EdgeID(-1 - slot) }
	var frontier []fe
	var pathIndex []int
	if lt == LoopSimple {
		pathIndex = make([]int, g.NumVertices())
		for i := range pathIndex {
			pathIndex[i] = -1
		}
	}
	var components [][2][][]EdgeID
	for minStart := 0; minStart < len(g.edges); minStart++ {
		if ltm[minStart] < 0 {
			continue
		}
		var component [2][][]EdgeID
		frontier = append(frontier, fe{EdgeID(minStart), 0})
		for len(frontier) > 0 {
			start, slot := frontier[len(frontier)-1].e, frontier[len(frontier)-1].slot
			frontier = frontier[:len(frontier)-1]
			if ltm[start] < 0 {
				continue
			}
			var path []EdgeID
			for e := start; ltm[e] >= 0; {
				path = append(path, e)
				next := ltm[e]
				ltm[e] = markUsed(slot)
				sibling := siblingMap[e]
				if ltm[sibling] >= 0 {
					frontier = append(frontier, fe{sibling, 1 - slot})
				} else if ltm[sibling] != markUsed(1-slot) {
					return nil, ErrUndirectedEdgesDoNotForm
				}
				if lt == LoopSimple {
					pathIndex[g.edges[e].V0] = len(path) - 1
					loopStart := pathIndex[g.edges[e].V1]
					if loopStart >= 0 {
						loop := slices.Clone(path[loopStart:])
						path = path[:loopStart]
						for _, e2 := range loop {
							pathIndex[g.edges[e2].V0] = -1
						}
						CanonicalizeLoopOrder(minIDs, loop)
						component[slot] = append(component[slot], loop)
					}
				}
				e = next
			}
			if lt != LoopSimple {
				CanonicalizeLoopOrder(minIDs, path)
				component[slot] = append(component[slot], path)
			}
		}
		CanonicalizeVectorOrder(minIDs, component[0])
		CanonicalizeVectorOrder(minIDs, component[1])
		if minIDs[component[0][0][0]] > minIDs[component[1][0][0]] {
			component[0], component[1] = component[1], component[0]
		}
		components = append(components, component)
	}
	sort.SliceStable(components, func(i, j int) bool {
		return minIDs[components[i][0][0][0]] < minIDs[components[j][0][0][0]]
	})
	return components, nil
}

// PolylineType selects GetPolylines behaviour.
type PolylineType uint8

const (
	PolylinePath PolylineType = iota
	PolylineWalk
)

type polylineBuilder struct {
	g           *Graph
	in          *VertexInMap
	out         *VertexOutMap
	siblingMap  []EdgeID
	minInputIDs []InputEdgeID
	directed    bool
	edgesLeft   int
	used        []bool
	excessUsed  map[VertexID]int
}

// GetPolylines assembles the edges into polylines.
func (g *Graph) GetPolylines(pt PolylineType) [][]EdgeID {
	b := &polylineBuilder{
		g: g, in: NewVertexInMap(g), out: NewVertexOutMap(g),
		minInputIDs: g.GetMinInputEdgeIDs(),
		directed:    g.opts.EdgeType == EdgeDirected,
		used:        make([]bool, len(g.edges)),
		excessUsed:  map[VertexID]int{},
	}
	if b.directed {
		b.edgesLeft = len(g.edges)
	} else {
		b.edgesLeft = len(g.edges) / 2
		b.siblingMap = slices.Clone(b.in.InEdgeIDs())
		g.makeSiblingMap(b.siblingMap)
	}
	if pt == PolylinePath {
		return b.buildPaths()
	}
	return b.buildWalks()
}

func (b *polylineBuilder) isInterior(v VertexID) bool {
	if b.directed {
		return b.in.Degree(v) == 1 && b.out.Degree(v) == 1
	}
	return b.out.Degree(v) == 2
}

func (b *polylineBuilder) excessDegree(v VertexID) int {
	if b.directed {
		return b.out.Degree(v) - b.in.Degree(v)
	}
	return b.out.Degree(v) % 2
}

func (b *polylineBuilder) buildPaths() [][]EdgeID {
	var polylines [][]EdgeID
	edges := b.g.GetInputEdgeOrder(b.minInputIDs)
	for _, e := range edges {
		if !b.used[e] && !b.isInterior(b.g.edges[e].V0) {
			polylines = append(polylines, b.buildPath(e))
		}
	}
	for _, e := range edges {
		if b.edgesLeft == 0 {
			break
		}
		if b.used[e] {
			continue
		}
		p := b.buildPath(e)
		CanonicalizeLoopOrder(b.minInputIDs, p)
		polylines = append(polylines, p)
	}
	CanonicalizeVectorOrder(b.minInputIDs, polylines)
	return polylines
}

func (b *polylineBuilder) buildPath(e EdgeID) []EdgeID {
	var p []EdgeID
	start := b.g.edges[e].V0
	for {
		p = append(p, e)
		b.used[e] = true
		if !b.directed {
			b.used[b.siblingMap[e]] = true
		}
		b.edgesLeft--
		v := b.g.edges[e].V1
		if !b.isInterior(v) || v == start {
			break
		}
		lo, hi := b.out.EdgeIDs(v)
		if b.directed {
			e = lo
		} else {
			for e2 := lo; e2 < hi; e2++ {
				if !b.used[e2] {
					e = e2
				}
			}
		}
	}
	return p
}

func (b *polylineBuilder) buildWalks() [][]EdgeID {
	var polylines [][]EdgeID
	edges := b.g.GetInputEdgeOrder(b.minInputIDs)
	for _, e := range edges {
		if b.used[e] {
			continue
		}
		v := b.g.edges[e].V0
		excess := b.excessDegree(v)
		if excess <= 0 {
			continue
		}
		excess -= b.excessUsed[v]
		if b.directed {
			if excess <= 0 {
				continue
			}
		} else if excess%2 == 0 {
			continue
		}
		b.excessUsed[v]++
		p := b.buildWalk(v)
		polylines = append(polylines, p)
		b.excessUsed[b.g.edges[p[len(p)-1]].V1]--
	}
	if b.edgesLeft > 0 {
		for i := range polylines {
			polylines[i] = b.maximizeWalk(polylines[i])
		}
	}
	for i := 0; i < len(edges) && b.edgesLeft > 0; i++ {
		e := edges[i]
		if b.used[e] {
			continue
		}
		v := b.g.edges[e].V0
		id := b.minInputIDs[e]
		excess := 0
		for j := i; j < len(edges) && b.minInputIDs[edges[j]] == id; j++ {
			e2 := edges[j]
			if b.used[e2] {
				continue
			}
			if b.g.edges[e2].V0 == v {
				excess++
			}
			if b.g.edges[e2].V1 == v {
				excess--
			}
		}
		if excess == 1 || b.g.edges[e].V1 == v {
			p := b.buildWalk(v)
			p = b.maximizeWalk(p)
			polylines = append(polylines, p)
		}
	}
	CanonicalizeVectorOrder(b.minInputIDs, polylines)
	return polylines
}

func (b *polylineBuilder) buildWalk(v VertexID) []EdgeID {
	var p []EdgeID
	for {
		bestEdge := EdgeID(-1)
		bestOutID := InputEdgeID(math.MaxInt32)
		lo, hi := b.out.EdgeIDs(v)
		for e := lo; e < hi; e++ {
			if b.used[e] || b.minInputIDs[e] >= bestOutID {
				continue
			}
			bestOutID = b.minInputIDs[e]
			bestEdge = e
		}
		if bestEdge < 0 {
			return p
		}
		excess := b.excessDegree(v) - b.excessUsed[v]
		cond := false
		if b.directed {
			cond = excess < 0
		} else {
			cond = excess%2 == 1
		}
		if cond {
			for _, e := range b.in.EdgeIDs(v) {
				if !b.used[e] && b.minInputIDs[e] <= bestOutID {
					return p
				}
			}
		}
		p = append(p, bestEdge)
		b.used[bestEdge] = true
		if !b.directed {
			b.used[b.siblingMap[bestEdge]] = true
		}
		b.edgesLeft--
		v = b.g.edges[bestEdge].V1
	}
}

func (b *polylineBuilder) maximizeWalk(p []EdgeID) []EdgeID {
	for i := 0; i <= len(p); i++ {
		var v VertexID
		if i == 0 {
			v = b.g.edges[p[i]].V0
		} else {
			v = b.g.edges[p[i-1]].V1
		}
		lo, hi := b.out.EdgeIDs(v)
		for e := lo; e < hi; e++ {
			if !b.used[e] {
				loop := b.buildWalk(v)
				p = slices.Insert(p, i, loop...)
				break
			}
		}
	}
	return p
}

// ProcessEdges applies the GraphOptions to a layer's edges (sorting them
// and handling degenerate, duplicate and sibling edges). It may change
// opts.EdgeType to directed.
func ProcessEdges(opts *GraphOptions, edges *[]Edge, inputIDs *[]InputEdgeIDSetID, lex *idSetLexicon) error {
	p := &edgeProcessor{opts: *opts, edges: *edges, inputIDs: *inputIDs, lex: lex}
	err := p.run()
	*edges, *inputIDs = p.edges, p.inputIDs
	if opts.SiblingPairs == SiblingRequire || opts.SiblingPairs == SiblingCreate {
		opts.EdgeType = EdgeDirected
	}
	return err
}

type edgeProcessor struct {
	opts        GraphOptions
	edges       []Edge
	inputIDs    []InputEdgeIDSetID
	lex         *idSetLexicon
	outEdges    []EdgeID
	inEdges     []EdgeID
	newEdges    []Edge
	newInputIDs []InputEdgeIDSetID
	tmpIDs      []InputEdgeID
}

func (p *edgeProcessor) addEdge(e Edge, id InputEdgeIDSetID) {
	p.newEdges = append(p.newEdges, e)
	p.newInputIDs = append(p.newInputIDs, id)
}

func (p *edgeProcessor) addEdges(n int, e Edge, id InputEdgeIDSetID) {
	for i := 0; i < n; i++ {
		p.addEdge(e, id)
	}
}

func (p *edgeProcessor) copyEdges(b, e int) {
	for i := b; i < e; i++ {
		p.addEdge(p.edges[p.outEdges[i]], p.inputIDs[p.outEdges[i]])
	}
}

func (p *edgeProcessor) mergeInputIDs(b, e int) InputEdgeIDSetID {
	if e-b == 1 {
		return p.inputIDs[p.outEdges[b]]
	}
	p.tmpIDs = p.tmpIDs[:0]
	for i := b; i < e; i++ {
		p.tmpIDs = append(p.tmpIDs, p.lex.idSet(p.inputIDs[p.outEdges[i]])...)
	}
	return p.lex.add(p.tmpIDs)
}

func (p *edgeProcessor) run() error {
	n := len(p.edges)
	if n == 0 {
		return nil
	}
	p.outEdges = make([]EdgeID, n)
	p.inEdges = make([]EdgeID, n)
	for i := range p.outEdges {
		p.outEdges[i] = EdgeID(i)
		p.inEdges[i] = EdgeID(i)
	}
	sort.Slice(p.outEdges, func(i, j int) bool {
		a, b := p.outEdges[i], p.outEdges[j]
		return stableLessThan(p.edges[a], p.edges[b], a, b)
	})
	sort.Slice(p.inEdges, func(i, j int) bool {
		a, b := p.inEdges[i], p.inEdges[j]
		return stableLessThan(p.edges[a].reverse(), p.edges[b].reverse(), a, b)
	})
	p.newEdges = make([]Edge, 0, n)
	p.newInputIDs = make([]InputEdgeIDSetID, 0, n)
	var err error
	out, in := 0, 0
	sentinel := Edge{math.MaxInt32, math.MaxInt32}
	outEdge := p.edges[p.outEdges[out]]
	inEdge := p.edges[p.inEdges[in]]
	for {
		edge := minEdge(outEdge, inEdge.reverse())
		if edge == sentinel {
			break
		}
		outBegin, inBegin := out, in
		for outEdge == edge {
			out++
			if out == n {
				outEdge = sentinel
			} else {
				outEdge = p.edges[p.outEdges[out]]
			}
		}
		for inEdge.reverse() == edge {
			in++
			if in == n {
				inEdge = sentinel
			} else {
				inEdge = p.edges[p.inEdges[in]]
			}
		}
		nOut := out - outBegin
		nIn := in - inBegin
		o := p.opts
		if edge.V0 == edge.V1 {
			if o.DegenerateEdges == DegenerateDiscard {
				continue
			}
			if o.DegenerateEdges == DegenerateDiscardExcess &&
				((outBegin > 0 && p.edges[p.outEdges[outBegin-1]].V0 == edge.V0) ||
					(out < n && p.edges[p.outEdges[out]].V0 == edge.V0) ||
					(inBegin > 0 && p.edges[p.inEdges[inBegin-1]].V1 == edge.V0) ||
					(in < n && p.edges[p.inEdges[in]].V1 == edge.V0)) {
				continue
			}
			merge := o.DuplicateEdges == DuplicateMerge || o.DegenerateEdges == DegenerateDiscardExcess
			if o.EdgeType == EdgeUndirected && (o.SiblingPairs == SiblingRequire || o.SiblingPairs == SiblingCreate) {
				k := nOut / 2
				if merge {
					k = 1
				}
				p.addEdges(k, edge, p.mergeInputIDs(outBegin, out))
			} else if merge {
				k := 1
				if o.EdgeType == EdgeUndirected {
					k = 2
				}
				p.addEdges(k, edge, p.mergeInputIDs(outBegin, out))
			} else if o.SiblingPairs == SiblingDiscard || o.SiblingPairs == SiblingDiscardExcess {
				p.addEdges(nOut, edge, p.mergeInputIDs(outBegin, out))
			} else {
				p.copyEdges(outBegin, out)
			}
		} else if o.SiblingPairs == SiblingKeep {
			if nOut > 1 && o.DuplicateEdges == DuplicateMerge {
				p.addEdge(edge, p.mergeInputIDs(outBegin, out))
			} else {
				p.copyEdges(outBegin, out)
			}
		} else if o.SiblingPairs == SiblingDiscard {
			if o.EdgeType == EdgeDirected {
				if nOut <= nIn {
					continue
				}
				k := nOut - nIn
				if o.DuplicateEdges == DuplicateMerge {
					k = 1
				}
				p.addEdges(k, edge, p.mergeInputIDs(outBegin, out))
			} else {
				if nOut&1 == 0 {
					continue
				}
				p.addEdge(edge, p.mergeInputIDs(outBegin, out))
			}
		} else if o.SiblingPairs == SiblingDiscardExcess {
			if o.EdgeType == EdgeDirected {
				if nOut < nIn {
					continue
				}
				k := max(1, nOut-nIn)
				if o.DuplicateEdges == DuplicateMerge {
					k = 1
				}
				p.addEdges(k, edge, p.mergeInputIDs(outBegin, out))
			} else {
				k := 2
				if nOut&1 != 0 {
					k = 1
				}
				p.addEdges(k, edge, p.mergeInputIDs(outBegin, out))
			}
		} else {
			mismatch := nOut&1 != 0
			if o.EdgeType == EdgeDirected {
				mismatch = nOut != nIn
			}
			if err == nil && o.SiblingPairs == SiblingRequire && mismatch {
				err = ErrMissingExpectedSiblingEdges
			}
			if o.DuplicateEdges == DuplicateMerge {
				p.addEdge(edge, p.mergeInputIDs(outBegin, out))
			} else if o.EdgeType == EdgeUndirected {
				p.addEdges((nOut+1)/2, edge, p.mergeInputIDs(outBegin, out))
			} else {
				p.copyEdges(outBegin, out)
				if nIn > nOut {
					p.addEdges(nIn-nOut, edge, emptySetID)
				}
			}
		}
	}
	p.edges, p.inputIDs = p.newEdges, p.newInputIDs
	return err
}

// FilterVertices keeps only the vertices used by edges, renumbering them.
func FilterVertices(vertices []s2.Point, edges []Edge) []s2.Point {
	used := make([]VertexID, 0, 2*len(edges))
	for _, e := range edges {
		used = append(used, e.V0, e.V1)
	}
	slices.Sort(used)
	used = slices.Compact(used)
	vmap := make([]VertexID, len(vertices))
	nv := make([]s2.Point, len(used))
	for i, u := range used {
		nv[i] = vertices[u]
		vmap[u] = VertexID(i)
	}
	for i := range edges {
		edges[i].V0 = vmap[edges[i].V0]
		edges[i].V1 = vmap[edges[i].V1]
	}
	return nv
}

// MakeSubgraph returns a graph over the same vertices with new edges
// processed under newOpts.
func (g *Graph) MakeSubgraph(newOpts GraphOptions, newEdges *[]Edge, newInputIDs *[]InputEdgeIDSetID,
	newLex *idSetLexicon, pred IsFullPolygonPredicate) (*Graph, error) {
	if g.opts.EdgeType == EdgeDirected && newOpts.EdgeType == EdgeUndirected {
		n := len(*newEdges)
		for i := 0; i < n; i++ {
			*newEdges = append(*newEdges, (*newEdges)[i].reverse())
			*newInputIDs = append(*newInputIDs, emptySetID)
		}
	}
	err := ProcessEdges(&newOpts, newEdges, newInputIDs, newLex)
	return newGraph(newOpts, g.vertices, *newEdges, *newInputIDs, newLex, g.labelSetIDs, g.labelSetLex, pred), err
}
