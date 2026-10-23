package s2x

// Port of S2Builder::SimplifyEdgeChains and EdgeChainSimplifier.

import (
	"slices"
	"sort"
)

type layerEdgeID struct{ layer, edge int32 }

func (b *Builder) simplifyEdgeChains(siteVertices [][]int32, layerEdges [][]Edge, layerInputIDs [][]InputEdgeIDSetID, inputLex *idSetLexicon) {
	if len(b.layers) == 0 {
		return
	}
	mergedEdges, mergedIDs, mergedLayers := mergeLayerEdges(layerEdges, layerInputIDs)
	for i := range layerEdges {
		layerEdges[i] = layerEdges[i][:0]
		layerInputIDs[i] = layerInputIDs[i][:0]
	}
	opts := NewGraphOptions(EdgeDirected, DegenerateKeep, DuplicateKeep, SiblingKeep)
	g := newGraph(opts, b.sites, mergedEdges, mergedIDs, inputLex, nil, nil, nil)
	s := &edgeChainSimplifier{
		b: b, g: g, in: NewVertexInMap(g), out: NewVertexOutMap(g), edgeLayers: mergedLayers,
		siteVertices: siteVertices, layerEdges: layerEdges, layerInputIDs: layerInputIDs,
		lex: inputLex, layerBegins: b.layerBegins,
		isInterior: make([]bool, g.NumVertices()), used: make([]bool, g.NumEdges()),
	}
	s.run()
}

func mergeLayerEdges(layerEdges [][]Edge, layerInputIDs [][]InputEdgeIDSetID) ([]Edge, []InputEdgeIDSetID, []int32) {
	var order []layerEdgeID
	for i := range layerEdges {
		for e := range layerEdges[i] {
			order = append(order, layerEdgeID{int32(i), int32(e)})
		}
	}
	sort.Slice(order, func(i, j int) bool {
		ai, bi := order[i], order[j]
		a, c := layerEdges[ai.layer][ai.edge], layerEdges[bi.layer][bi.edge]
		if x := edgeCmp(a, c); x != 0 {
			return x < 0
		}
		if ai.layer != bi.layer {
			return ai.layer < bi.layer
		}
		return ai.edge < bi.edge
	})
	edges := make([]Edge, 0, len(order))
	ids := make([]InputEdgeIDSetID, 0, len(order))
	layers := make([]int32, 0, len(order))
	for _, id := range order {
		edges = append(edges, layerEdges[id.layer][id.edge])
		ids = append(ids, layerInputIDs[id.layer][id.edge])
		layers = append(layers, id.layer)
	}
	return edges, ids, layers
}

type edgeChainSimplifier struct {
	b             *Builder
	g             *Graph
	in            *VertexInMap
	out           *VertexOutMap
	edgeLayers    []int32
	siteVertices  [][]int32
	layerEdges    [][]Edge
	layerInputIDs [][]InputEdgeIDSetID
	lex           *idSetLexicon
	layerBegins   []int32
	isInterior    []bool
	used          []bool
	tmpVertices   []VertexID
	tmpEdges      []EdgeID
	newEdges      []Edge
	newInputIDs   []InputEdgeIDSetID
	newLayers     []int32
}

func (s *edgeChainSimplifier) run() {
	for v := 0; v < s.g.NumVertices(); v++ {
		s.isInterior[v] = s.computeIsInterior(VertexID(v))
	}
	for e := 0; e < s.g.NumEdges(); e++ {
		if s.used[e] {
			continue
		}
		edge := s.g.edges[e]
		if s.isInterior[edge.V0] {
			continue
		}
		if !s.isInterior[edge.V1] {
			s.outputEdge(EdgeID(e))
		} else {
			s.simplifyChain(edge.V0, edge.V1)
		}
	}
	for e := 0; e < s.g.NumEdges(); e++ {
		if s.used[e] {
			continue
		}
		edge := s.g.edges[e]
		if edge.V0 == edge.V1 {
			s.outputEdge(EdgeID(e))
		} else {
			s.simplifyChain(edge.V0, edge.V1)
		}
	}
	for e := range s.newEdges {
		l := s.newLayers[e]
		s.layerEdges[l] = append(s.layerEdges[l], s.newEdges[e])
		s.layerInputIDs[l] = append(s.layerInputIDs[l], s.newInputIDs[e])
	}
}

func (s *edgeChainSimplifier) outputEdge(e EdgeID) {
	s.newEdges = append(s.newEdges, s.g.edges[e])
	s.newInputIDs = append(s.newInputIDs, s.g.inputEdgeIDSetIDs[e])
	s.newLayers = append(s.newLayers, s.edgeLayers[e])
	s.used[e] = true
}

func (s *edgeChainSimplifier) inputEdgeLayer(id InputEdgeID) int {
	// upper_bound(layer_begins, id) - (begin + 1)
	i := sort.Search(len(s.layerBegins), func(k int) bool { return s.layerBegins[k] > id })
	return i - 1
}

type interiorVertexMatcher struct {
	v0, v1, v2       VertexID
	n0, n1, n2       int
	excessOut        int
	tooManyEndpoints bool
}

func (m *interiorVertexMatcher) startLayer() { m.excessOut, m.n0, m.n1, m.n2 = 0, 0, 0, 0 }

func (m *interiorVertexMatcher) tally(v VertexID, outgoing bool) {
	if outgoing {
		m.excessOut++
	} else {
		m.excessOut--
	}
	if v == m.v0 {
		m.n0++
	} else {
		if m.v1 < 0 {
			m.v1 = v
		}
		if m.v1 == v {
			m.n1++
		} else {
			if m.v2 < 0 {
				m.v2 = v
			}
			if m.v2 == v {
				m.n2++
			} else {
				m.tooManyEndpoints = true
			}
		}
	}
}

func (m *interiorVertexMatcher) matches() bool {
	return !m.tooManyEndpoints && m.excessOut == 0 && m.n1 == m.n2 && (m.n0 == 0 || m.n1 > 0)
}

func (s *edgeChainSimplifier) computeIsInterior(v VertexID) bool {
	if s.out.Degree(v) == 0 {
		return false
	}
	if s.out.Degree(v) != s.in.Degree(v) {
		return false
	}
	if s.b.isForced(v) {
		return false
	}
	edges := s.tmpEdges[:0]
	lo, hi := s.out.EdgeIDs(v)
	for e := lo; e < hi; e++ {
		edges = append(edges, e)
	}
	edges = append(edges, s.in.EdgeIDs(v)...)
	sort.SliceStable(edges, func(i, j int) bool { return s.edgeLayers[edges[i]] < s.edgeLayers[edges[j]] })
	s.tmpEdges = edges
	m := interiorVertexMatcher{v0: v, v1: -1, v2: -1}
	for i := 0; i < len(edges); {
		layer := s.edgeLayers[edges[i]]
		m.startLayer()
		for ; i < len(edges) && s.edgeLayers[edges[i]] == layer; i++ {
			edge := s.g.edges[edges[i]]
			if edge.V0 == v {
				m.tally(edge.V1, true)
			}
			if edge.V1 == v {
				m.tally(edge.V0, false)
			}
		}
		if !m.matches() {
			return false
		}
	}
	return true
}

func (s *edgeChainSimplifier) simplifyChain(v0, v1 VertexID) {
	chain := s.tmpVertices[:0]
	usedVertices := map[VertexID]bool{}
	var simp polylineSimplifier
	vstart := v0
	done := false
	for !done {
		chain = append(chain, v0)
		usedVertices[v0] = true
		simp.init(s.g.vertices[v0])
		simplify := s.avoidSites(v0, v0, v1, usedVertices, &simp)
		for {
			chain = append(chain, v1)
			usedVertices[v1] = true
			done = !s.isInterior[v1] || v1 == vstart
			if done {
				break
			}
			vprev := v0
			v0 = v1
			v1 = s.followChain(vprev, v0)
			if !(simplify && s.targetInputVertices(v0, &simp) &&
				s.avoidSites(chain[0], v0, v1, usedVertices, &simp) &&
				simp.extend(s.g.vertices[v1])) {
				break
			}
		}
		if len(chain) == 2 {
			s.outputAllEdges(chain[0], chain[1])
		} else {
			s.mergeChain(chain)
		}
		chain = chain[:0]
		clear(usedVertices)
	}
	s.tmpVertices = chain
}

func (s *edgeChainSimplifier) followChain(v0, v1 VertexID) VertexID {
	lo, hi := s.out.EdgeIDs(v1)
	for e := lo; e < hi; e++ {
		v := s.g.edges[e].V1
		if v != v0 && v != v1 {
			return v
		}
	}
	panic("s2builder: could not find next edge in edge chain")
}

func (s *edgeChainSimplifier) outputAllEdges(v0, v1 VertexID) {
	lo, hi := s.out.EdgeIDsBetween(v0, v1)
	for e := lo; e < hi; e++ {
		s.outputEdge(e)
	}
	lo, hi = s.out.EdgeIDsBetween(v1, v0)
	for e := lo; e < hi; e++ {
		s.outputEdge(e)
	}
}

func (s *edgeChainSimplifier) targetInputVertices(v VertexID, simp *polylineSimplifier) bool {
	for _, i := range s.siteVertices[v] {
		if !simp.targetDisc(s.b.inputVertices[i], s.b.edgeSnapRadiusCA) {
			return false
		}
	}
	return true
}

func (s *edgeChainSimplifier) avoidSites(v0, v1, v2 VertexID, used map[VertexID]bool, simp *polylineSimplifier) bool {
	p0, p1, p2 := s.g.vertices[v0], s.g.vertices[v1], s.g.vertices[v2]
	r1 := chordBetween(p0, p1)
	r2 := chordBetween(p0, p2)
	if r2 < r1 {
		return false
	}
	if r2 >= s.b.minEdgeLengthToSplitCA {
		return false
	}
	best := InputEdgeID(-1)
	es := s.b.edgeSites
	consider := func(lo, hi EdgeID) {
		for e := lo; e < hi; e++ {
			for _, id := range s.g.InputEdgeIDs(e) {
				if best < 0 || len(es[id]) < len(es[best]) {
					best = id
				}
			}
		}
	}
	consider(s.out.EdgeIDsBetween(v1, v2))
	consider(s.out.EdgeIDsBetween(v2, v1))
	for _, v := range es[best] {
		p := s.g.vertices[v]
		r := chordBetween(p0, p)
		if r >= r2 {
			continue
		}
		if used[v] {
			continue
		}
		used[v] = true
		var onLeft bool
		if v1 == v0 {
			onLeft = sign(p1, p2, p) > 0
		} else {
			onLeft = orderedCCW(p0, p2, p, p1)
		}
		if !simp.avoidDisc(p, s.b.minEdgeSiteSeparationCA, onLeft) {
			return false
		}
	}
	return true
}

func (s *edgeChainSimplifier) mergeChain(vertices []VertexID) {
	var merged [][]InputEdgeID
	var degenerate []InputEdgeID
	for i := 1; i < len(vertices); i++ {
		v0, v1 := vertices[i-1], vertices[i]
		olo, ohi := s.out.EdgeIDsBetween(v0, v1)
		ilo, ihi := s.out.EdgeIDsBetween(v1, v0)
		if i == 1 {
			merged = make([][]InputEdgeID, int(ohi-olo)+int(ihi-ilo))
		} else {
			dlo, dhi := s.out.EdgeIDsBetween(v0, v0)
			for e := dlo; e < dhi; e++ {
				degenerate = append(degenerate, s.g.InputEdgeIDs(e)...)
				s.used[e] = true
			}
		}
		j := 0
		for e := olo; e < ohi; e++ {
			merged[j] = append(merged[j], s.g.InputEdgeIDs(e)...)
			s.used[e] = true
			j++
		}
		for e := ilo; e < ihi; e++ {
			merged[j] = append(merged[j], s.g.InputEdgeIDs(e)...)
			s.used[e] = true
			j++
		}
	}
	if len(degenerate) > 0 {
		slices.Sort(degenerate)
		s.assignDegenerateEdges(degenerate, merged)
	}
	v0, v1, vb := vertices[0], vertices[1], vertices[len(vertices)-1]
	lo, hi := s.out.EdgeIDsBetween(v0, v1)
	for e := lo; e < hi; e++ {
		s.newEdges = append(s.newEdges, Edge{v0, vb})
		s.newLayers = append(s.newLayers, s.edgeLayers[e])
	}
	lo, hi = s.out.EdgeIDsBetween(v1, v0)
	for e := lo; e < hi; e++ {
		s.newEdges = append(s.newEdges, Edge{vb, v0})
		s.newLayers = append(s.newLayers, s.edgeLayers[e])
	}
	for _, ids := range merged {
		s.newInputIDs = append(s.newInputIDs, s.lex.add(slices.Clone(ids)))
	}
}

func (s *edgeChainSimplifier) assignDegenerateEdges(degenerate []InputEdgeID, merged [][]InputEdgeID) {
	for _, ids := range merged {
		slices.Sort(ids)
	}
	var order []int
	for i := range merged {
		if len(merged[i]) > 0 {
			order = append(order, i)
		}
	}
	sort.Slice(order, func(i, j int) bool { return merged[order[i]][0] < merged[order[j]][0] })
	for _, d := range degenerate {
		layer := s.inputEdgeLayer(d)
		it := sort.Search(len(order), func(k int) bool { return d < merged[order[k]][0] })
		if it != 0 {
			if merged[order[it-1]][0] >= s.layerBegins[layer] {
				it--
			}
		}
		merged[order[it]] = append(merged[order[it]], d)
	}
}
