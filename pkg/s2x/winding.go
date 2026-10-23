package s2x

// S2WindingOperation (s2winding_operation.cc) and the helpers from
// s2builderutil_get_snapped_winding_delta.cc.

import (
	"errors"
	"slices"

	"github.com/golang/geo/s2"
)

// signedVertexCrossing is S2::SignedVertexCrossing.
func signedVertexCrossing(a, b, c, d s2.Point) int {
	if a == b || c == d {
		return 0
	}
	if a == c {
		if b == d || s2.OrderedCCW(s2.Ortho(a), d, b, a) {
			return 1
		}
		return 0
	}
	if b == d {
		if s2.OrderedCCW(s2.Ortho(b), c, a, b) {
			return 1
		}
		return 0
	}
	if a == d {
		if b == c || s2.OrderedCCW(s2.Ortho(a), c, b, a) {
			return -1
		}
		return 0
	}
	if b == c {
		if s2.OrderedCCW(s2.Ortho(b), d, a, b) {
			return -1
		}
		return 0
	}
	return 0
}

// signedEdgeOrVertexCrossing is S2EdgeCrosser::SignedEdgeOrVertexCrossing:
// +1 when AB enters the region to the left of CD, -1 when it exits.
func signedEdgeOrVertexCrossing(a, b, c, d s2.Point) int {
	switch s2.CrossingSign(a, b, c, d) {
	case s2.DoNotCross:
		return 0
	case s2.Cross:
		return sign(a, b, c)
	}
	return signedVertexCrossing(a, b, c, d)
}

// containsBruteForce is s2shapeutil::ContainsBruteForce.
func containsBruteForce(shape s2.Shape, p s2.Point) bool {
	if shape.Dimension() < 2 {
		return false
	}
	ref := referencePointForShape(shape)
	if ref.Point == p {
		return ref.Contained
	}
	crosser := s2.NewEdgeCrosser(ref.Point, p)
	inside := ref.Contained
	for e := 0; e < shape.NumEdges(); e++ {
		ed := shape.Edge(e)
		inside = inside != crosser.EdgeOrVertexCrossing(ed.V0, ed.V1)
	}
	return inside
}

type edgeSnap struct {
	input     s2.Edge
	vIn, vOut VertexID
}

type vertexSnap struct {
	key  s2.Point
	snap edgeSnap
}

// buildChain consumes one chain of input edges from m (a multimap kept
// sorted by key, insertion order within equal keys).
func buildChain(refV VertexID, g *Graph, m *[]vertexSnap) (chainIn, chainOut []s2.Point, err error) {
	ms := *m
	i := 0
	for ; i < len(ms); i++ {
		if ms[i].snap.vIn >= 0 {
			chainOut = append(chainOut, g.vertices[ms[i].snap.vIn])
			break
		}
	}
	if i == len(ms) {
		i = 0
	}
	snap := ms[i].snap
	ms = slices.Delete(ms, i, i+1)
	*m = ms
	chainIn = append(chainIn, snap.input.V0, snap.input.V1)
	chainOut = append(chainOut, g.vertices[refV])
	if snap.vOut >= 0 {
		chainOut = append(chainOut, g.vertices[snap.vOut])
		return chainIn, chainOut, nil
	}
	for chainIn[len(chainIn)-1] != chainIn[0] {
		back := chainIn[len(chainIn)-1]
		j, found := slices.BinarySearchFunc(ms, back, func(x vertexSnap, t s2.Point) int { return x.key.Cmp(t.Vector) })
		if !found {
			return nil, nil, errors.New("input edges (after filtering) do not form loops")
		}
		snap := ms[j].snap
		ms = slices.Delete(ms, j, j+1)
		*m = ms
		chainIn = append(chainIn, snap.input.V1)
		if snap.vOut >= 0 {
			chainOut = append(chainOut, g.vertices[snap.vOut])
			break
		}
	}
	return chainIn, chainOut, nil
}

func edgeWindingDelta(a, b s2.Point, chain []s2.Point) int {
	delta := 0
	for i := 1; i < len(chain); i++ {
		delta += signedEdgeOrVertexCrossing(a, b, chain[i-1], chain[i])
	}
	return delta
}

func getConnector(b0, b1, b1Snapped s2.Point) s2.Point {
	if b1Snapped.Dot(b1.Vector) >= 0 {
		return b1
	}
	x := unit(robustCrossProd(b0, b1).Cross(b1Snapped.Vector))
	if x.Dot(s2.Interpolate(0.5, b0, b1).Vector) >= 0 {
		return x
	}
	return s2.Point{Vector: x.Mul(-1)}
}

// getSnappedWindingDelta is s2builderutil::GetSnappedWindingDelta with
// brute-force incident edges and no input edge filter.
func getSnappedWindingDelta(refIn s2.Point, refV VertexID, b *Builder, g *Graph) (int, error) {
	byID := map[InputEdgeID]*edgeSnap{}
	var ids []InputEdgeID
	for e, edge := range g.edges {
		if edge.V0 != refV && edge.V1 != refV {
			continue
		}
		for _, id := range g.InputEdgeIDs(EdgeID(e)) {
			s, ok := byID[id]
			if !ok {
				s = &edgeSnap{input: b.InputEdge(int(id)), vIn: -1, vOut: -1}
				byID[id] = s
				ids = append(ids, id)
			}
			if edge.V0 != refV {
				s.vIn = edge.V0
			}
			if edge.V1 != refV {
				s.vOut = edge.V1
			}
		}
	}
	// The C++ iterates a hash map here; input id order keeps it
	// deterministic.
	slices.Sort(ids)
	var m []vertexSnap
	for _, id := range ids {
		s := *byID[id]
		m = append(m, vertexSnap{s.input.V0, s})
	}
	slices.SortStableFunc(m, func(x, y vertexSnap) int { return x.key.Cmp(y.key.Vector) })
	refOut := g.vertices[refV]
	delta := 0
	for len(m) > 0 {
		chainIn, chainOut, err := buildChain(refV, g, &m)
		if err != nil {
			return 0, err
		}
		if len(chainOut) == 1 {
			z := s2.Ortho(refOut)
			delta -= edgeWindingDelta(z, refIn, chainIn)
			continue
		}
		n := len(chainIn)
		za := unit(robustCrossProd(chainIn[0], chainIn[1]))
		zb := unit(robustCrossProd(chainIn[n-2], chainIn[n-1]))
		if za.Dot(refOut.Vector) > 0 {
			za = s2.Point{Vector: za.Mul(-1)}
		}
		if zb.Dot(refOut.Vector) > 0 {
			zb = s2.Point{Vector: zb.Mul(-1)}
		}
		a0c := getConnector(chainIn[1], chainIn[0], chainOut[0])
		b1c := getConnector(chainIn[n-2], chainIn[n-1], chainOut[2])
		chainZ := []s2.Point{chainOut[0], chainOut[1], chainIn[1], chainIn[0], a0c, chainOut[0]}
		delta += edgeWindingDelta(za, zb, chainZ)
		diff := slices.Clone(chainOut)
		diff = append(diff, b1c)
		for i := n - 1; i >= 0; i-- {
			diff = append(diff, chainIn[i])
		}
		diff = append(diff, a0c, chainOut[0])
		delta += edgeWindingDelta(zb, refIn, diff)
		delta += edgeWindingDelta(refIn, refOut, chainOut)
	}
	return delta, nil
}

// findFirstVertexID is s2builderutil::FindFirstVertexId.
func findFirstVertexID(inputEdgeID InputEdgeID, g *Graph) VertexID {
	excess := map[VertexID]int{}
	for e, edge := range g.edges {
		if slices.Contains(g.InputEdgeIDs(EdgeID(e)), inputEdgeID) {
			excess[edge.V0]++
			excess[edge.V1]--
		}
	}
	if len(excess) == 0 {
		return -1
	}
	keys := make([]VertexID, 0, len(excess))
	for v := range excess {
		keys = append(keys, v)
	}
	slices.Sort(keys)
	for _, v := range keys {
		if excess[v] == 1 {
			return v
		}
	}
	return keys[0]
}

type windingOracle struct {
	g          *Graph
	refP       s2.Point
	refWinding int
}

func newWindingOracle(refInputEdgeID InputEdgeID, refWindingIn int, b *Builder, g *Graph) *windingOracle {
	refIn := b.InputEdge(int(refInputEdgeID)).V0
	refV := findFirstVertexID(refInputEdgeID, g)
	d, _ := getSnappedWindingDelta(refIn, refV, b, g)
	return &windingOracle{g: g, refP: g.vertices[refV], refWinding: refWindingIn + d}
}

// windingNumber walks from the previous reference point to p. (The C++
// switches to an index after the first query; the brute-force loop gives
// the same sums.)
func (o *windingOracle) windingNumber(p s2.Point) int {
	w := o.refWinding
	for _, e := range o.g.edges {
		w += signedEdgeOrVertexCrossing(o.refP, p, o.g.vertices[e.V0], o.g.vertices[e.V1])
	}
	o.refP = p
	o.refWinding = w
	return w
}

// WindingRule selects which winding numbers are inside.
type WindingRule uint8

const (
	WindingPositive WindingRule = iota
	WindingNegative
	WindingNonZero
	WindingOdd
)

// WindingOperation is S2WindingOperation.
type WindingOperation struct {
	builder             *Builder
	includeDegeneracies bool
	refInputEdgeID      InputEdgeID
	refWindingIn        int
	rule                WindingRule
}

// NewWindingOperation is S2WindingOperation::Init.
func NewWindingOperation(result Layer, snap SnapFunction, includeDegeneracies bool) *WindingOperation {
	op := &WindingOperation{includeDegeneracies: includeDegeneracies}
	opts := NewBuilderOptions(snap)
	opts.SplitCrossingEdges = true
	op.builder = NewBuilder(opts)
	op.builder.StartLayer(&windingLayer{op: op, result: result})
	return op
}

// AddLoop adds a loop of points.
func (op *WindingOperation) AddLoop(loop []s2.Point) { op.builder.AddLoopPoints(loop) }

// Build computes the region whose winding number matches the rule, given
// the winding number refWinding of refP.
func (op *WindingOperation) Build(refP s2.Point, refWinding int, rule WindingRule) error {
	op.refInputEdgeID = InputEdgeID(op.builder.NumInputEdges())
	op.builder.AddPoint(refP)
	op.refWindingIn = refWinding
	op.rule = rule
	return op.builder.Build()
}

type windingLayer struct {
	op          *WindingOperation
	result      Layer
	resultEdges []Edge
	resultIDs   []InputEdgeIDSetID
}

func (l *windingLayer) GraphOptions() GraphOptions {
	return NewGraphOptions(EdgeDirected, DegenerateKeep, DuplicateKeep, SiblingKeep)
}

func (l *windingLayer) Build(g *Graph) error {
	oracle := newWindingOracle(l.op.refInputEdgeID, l.op.refWindingIn, l.op.builder, g)
	var edges []Edge
	var ids []InputEdgeIDSetID
	for e := range g.edges {
		in := g.InputEdgeIDs(EdgeID(e))
		if len(in) > 0 && in[0] == l.op.refInputEdgeID {
			continue
		}
		edges = append(edges, g.edges[e])
		ids = append(ids, g.inputEdgeIDSetIDs[e])
	}
	ng, err := g.MakeSubgraph(NewGraphOptions(EdgeDirected, DegenerateDiscardExcess, DuplicateMerge, SiblingCreate),
		&edges, &ids, g.inputEdgeIDSetLex, nil)
	if err != nil {
		return err
	}
	if err := l.computeBoundary(ng, oracle); err != nil {
		return err
	}
	full := func(*Graph) (bool, error) { return l.matchesRule(oracle.refWinding), nil }
	rg, err := ng.MakeSubgraph(l.result.GraphOptions(), &l.resultEdges, &l.resultIDs, ng.inputEdgeIDSetLex, full)
	if err != nil {
		return err
	}
	return l.result.Build(rg)
}

func (l *windingLayer) computeBoundary(g *Graph, oracle *windingOracle) error {
	sibling := g.GetSiblingMap()
	leftTurn, err := g.GetLeftTurnMap(sibling)
	if err != nil {
		return err
	}
	edgeWinding := make([]int, len(g.edges))
	var frontier []EdgeID
	for eMin := range g.edges {
		if leftTurn[eMin] < 0 {
			continue
		}
		v0 := g.edges[eMin].V1
		e0 := l.containingLoopEdge(v0, EdgeID(eMin), g, leftTurn, sibling)
		edgeWinding[e0] = oracle.windingNumber(g.vertices[v0])
		frontier = append(frontier, e0)
		for len(frontier) > 0 {
			e := frontier[len(frontier)-1]
			frontier = frontier[:len(frontier)-1]
			if leftTurn[e] < 0 {
				continue
			}
			winding := edgeWinding[e]
			for leftTurn[e] >= 0 {
				sib := sibling[e]
				wMinus := len(g.InputEdgeIDs(e))
				wPlus := len(g.InputEdgeIDs(sib))
				sibWinding := winding - wMinus + wPlus
				if (l.matchesRule(winding) && !l.matchesRule(sibWinding)) || l.matchesDegeneracy(winding, wMinus, wPlus) {
					l.resultEdges = append(l.resultEdges, g.edges[e])
					l.resultIDs = append(l.resultIDs, g.inputEdgeIDSetIDs[e])
				}
				next := leftTurn[e]
				leftTurn[e] = -1
				if leftTurn[sib] >= 0 {
					edgeWinding[sib] = sibWinding
					frontier = append(frontier, sib)
				}
				e = next
			}
		}
	}
	return nil
}

func (l *windingLayer) containingLoopEdge(v VertexID, start EdgeID, g *Graph, leftTurn, sibling []EdgeID) EdgeID {
	if g.edges[start].V0 == v {
		return start
	}
	e0 := start
	for {
		e1 := leftTurn[e0]
		if g.edges[e0].V0 == g.edges[e1].V1 ||
			s2.AngleContainsVertex(g.vertices[g.edges[e0].V0], g.vertices[v], g.vertices[g.edges[e1].V1]) {
			return e0
		}
		e0 = sibling[e1]
	}
}

func (l *windingLayer) matchesRule(w int) bool {
	switch l.op.rule {
	case WindingPositive:
		return w > 0
	case WindingNegative:
		return w < 0
	case WindingNonZero:
		return w != 0
	}
	return w&1 != 0
}

func (l *windingLayer) matchesDegeneracy(w, wMinus, wPlus int) bool {
	if !l.op.includeDegeneracies || wMinus != wPlus {
		return false
	}
	if l.op.rule == WindingOdd {
		return wPlus&1 != 0
	}
	return w == 0
}
