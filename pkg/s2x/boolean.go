package s2x

// Port of S2BooleanOperation (s2boolean_operation.{h,cc}). The EXACT
// precision mode is the only one (as in C++, SNAPPED is not implemented);
// source-id lexicons and memory tracking are omitted.

import (
	"math"
	"sort"

	"github.com/golang/geo/s2"
)

// OpType is S2BooleanOperation::OpType.
type OpType uint8

const (
	OpUnion OpType = iota
	OpIntersection
	OpDifference
	OpSymmetricDifference
)

// PolygonModel and PolylineModel say whether boundaries are contained.
type PolygonModel uint8

const (
	PolygonOpen PolygonModel = iota
	PolygonSemiOpen
	PolygonClosed
)

type PolylineModel uint8

const (
	PolylineOpen PolylineModel = iota
	PolylineSemiOpen
	PolylineClosed
)

// BooleanOptions is S2BooleanOperation::Options.
type BooleanOptions struct {
	SnapFunction                  SnapFunction
	PolygonModel                  PolygonModel
	PolylineModel                 PolylineModel
	PolylineLoopsHaveBoundaries   bool
	SplitAllCrossingPolylineEdges bool
}

// DefaultBooleanOptions returns the C++ defaults: identity snapping with a
// zero radius, semi-open polygons, closed polylines.
func DefaultBooleanOptions() BooleanOptions {
	return BooleanOptions{SnapFunction: IdentitySnapFunction{}, PolygonModel: PolygonSemiOpen,
		PolylineModel: PolylineClosed, PolylineLoopsHaveBoundaries: true}
}

// BooleanOperation is S2BooleanOperation.
type BooleanOperation struct {
	opType  OpType
	opts    BooleanOptions
	layers  []Layer
	regions [2]*s2.ShapeIndex
	// resultEmpty is non-nil for IsEmpty-style boolean output.
	resultEmpty *bool
}

// NewBooleanOperation builds output into one layer (or three layers, one
// per dimension 0, 1, 2).
func NewBooleanOperation(op OpType, opts BooleanOptions, layers ...Layer) *BooleanOperation {
	if opts.SnapFunction == nil {
		opts.SnapFunction = IdentitySnapFunction{}
	}
	return &BooleanOperation{opType: op, opts: opts, layers: layers}
}

// Build computes the operation on regions a and b.
func (op *BooleanOperation) Build(a, b *s2.ShapeIndex) error {
	op.regions = [2]*s2.ShapeIndex{a, b}
	impl := &boolImpl{op: op, indexCrossingsFirstRegionID: -1}
	return impl.build()
}

// IsEmpty reports whether the result of the operation is empty.
func IsEmpty(op OpType, a, b *s2.ShapeIndex, opts BooleanOptions) bool {
	var empty bool
	o := NewBooleanOperation(op, opts)
	o.resultEmpty = &empty
	o.Build(a, b)
	return empty
}

// Intersects, Contains and Equals are the S2BooleanOperation predicates.
func Intersects(a, b *s2.ShapeIndex, opts BooleanOptions) bool {
	return !IsEmpty(OpIntersection, b, a, opts)
}
func Contains(a, b *s2.ShapeIndex, opts BooleanOptions) bool {
	return IsEmpty(OpDifference, b, a, opts)
}
func Equals(a, b *s2.ShapeIndex, opts BooleanOptions) bool {
	return IsEmpty(OpSymmetricDifference, b, a, opts)
}

const (
	setInside   InputEdgeID = -1
	setInvertB  InputEdgeID = -2
	setReverseA InputEdgeID = -3
)

type crossingInputEdge struct {
	inputID     InputEdgeID
	leftToRight bool
}

type inputEdgeCrossing struct {
	id InputEdgeID
	c  crossingInputEdge
}

type crossingGraphEdge struct {
	id       EdgeID
	aIndex   int
	outgoing bool
	dst      VertexID
}

func getInputEdgeChainOrder(g *Graph, inputIDs []InputEdgeID) []EdgeID {
	order := g.GetInputEdgeOrder(inputIDs)
	type vm struct {
		v VertexID
		e EdgeID
	}
	var vmap []vm
	indegree := make([]int, g.NumVertices())
	for begin, end := 0, 0; begin < len(order); begin = end {
		inputID := inputIDs[order[begin]]
		for end = begin; end < len(order); end++ {
			if inputIDs[order[end]] != inputID {
				break
			}
		}
		if end-begin == 1 {
			continue
		}
		for i := begin; i < end; i++ {
			e := order[i]
			vmap = append(vmap, vm{g.edges[e].V0, e})
			indegree[g.edges[e].V1]++
		}
		sort.Slice(vmap, func(i, j int) bool {
			if vmap[i].v != vmap[j].v {
				return vmap[i].v < vmap[j].v
			}
			return vmap[i].e < vmap[j].e
		})
		next := EdgeID(g.NumEdges())
		for i := begin; i < end; i++ {
			e := order[i]
			if indegree[g.edges[e].V0] == 0 {
				next = e
			}
		}
		for i := begin; ; {
			order[i] = next
			v := g.edges[next].V1
			indegree[v] = 0
			i++
			if i == end {
				break
			}
			k := sort.Search(len(vmap), func(j int) bool { return vmap[j].v >= v })
			next = vmap[k].e
		}
		vmap = vmap[:0]
	}
	return order
}

type graphEdgeClipper struct {
	g               *Graph
	in              *VertexInMap
	out             *VertexOutMap
	inputDimensions []int8
	inputCrossings  []inputEdgeCrossing
	newEdges        []Edge
	newInputIDs     []InputEdgeIDSetID
	inputIDs        []InputEdgeIDSetID
	order           []EdgeID
	rank            []int
}

func newGraphEdgeClipper(g *Graph, dims []int8, crossings []inputEdgeCrossing) *graphEdgeClipper {
	c := &graphEdgeClipper{g: g, in: NewVertexInMap(g), out: NewVertexOutMap(g), inputDimensions: dims,
		inputCrossings: crossings, inputIDs: g.inputEdgeIDSetIDs}
	c.order = getInputEdgeChainOrder(g, c.inputIDs)
	c.rank = make([]int, len(c.order))
	for i, e := range c.order {
		c.rank[e] = i
	}
	return c
}

func (c *graphEdgeClipper) addEdge(e Edge, id InputEdgeID) {
	c.newEdges = append(c.newEdges, e)
	c.newInputIDs = append(c.newInputIDs, id)
}

func (c *graphEdgeClipper) run() {
	var aVertices []VertexID
	var aNumCrossings []int
	var aIsolated []bool
	var bInputEdges []crossingInputEdge
	var bEdges [][]crossingGraphEdge
	inside, invertB, reverseA := false, false, false
	next := 0
	for i := 0; i < len(c.order); i++ {
		aInputID := c.inputIDs[c.order[i]]
		edge0 := c.g.edges[c.order[i]]
		bInputEdges = bInputEdges[:0]
		for ; next < len(c.inputCrossings); next++ {
			x := c.inputCrossings[next]
			if x.id != aInputID {
				break
			}
			switch {
			case x.c.inputID >= 0:
				bInputEdges = append(bInputEdges, x.c)
			case x.c.inputID == setInside:
				inside = x.c.leftToRight
			case x.c.inputID == setInvertB:
				invertB = x.c.leftToRight
			default:
				reverseA = x.c.leftToRight
			}
		}
		if edge0.V0 == edge0.V1 {
			if len(bInputEdges)&1 != 0 {
				inside = !inside
			}
			c.addEdge(edge0, aInputID)
			continue
		}
		if len(bInputEdges) == 0 {
			if inside {
				if reverseA {
					c.addEdge(edge0.reverse(), aInputID)
				} else {
					c.addEdge(edge0, aInputID)
				}
			}
			continue
		}
		aVertices = append(aVertices[:0], edge0.V0)
		bEdges = make([][]crossingGraphEdge, len(bInputEdges))
		c.gatherIncidentEdges(aVertices, 0, bInputEdges, bEdges)
		for ; i < len(c.order) && c.inputIDs[c.order[i]] == aInputID; i++ {
			aVertices = append(aVertices, c.g.edges[c.order[i]].V1)
			c.gatherIncidentEdges(aVertices, len(aVertices)-1, bInputEdges, bEdges)
		}
		i--
		aNumCrossings = make([]int, len(aVertices))
		aIsolated = make([]bool, len(aVertices))
		for bi := range bInputEdges {
			ltr := bInputEdges[bi].leftToRight
			aIndex := c.getCrossedVertexIndex(aVertices, bEdges[bi], ltr)
			if aIndex >= 0 {
				isLine := c.inputDimensions[bInputEdges[bi].inputID] == 1
				sgn := 1
				if isLine {
					sgn = 0
				} else if ltr == invertB {
					sgn = -1
				}
				aNumCrossings[aIndex] += sgn
				aIsolated[aIndex] = true
			}
		}
		multiplicity := aNumCrossings[0]
		if inside {
			multiplicity++
		}
		for ai := 1; ai < len(aVertices); ai++ {
			if multiplicity != 0 {
				aIsolated[ai-1], aIsolated[ai] = false, false
			}
			edgeCount := multiplicity
			if reverseA {
				edgeCount = -multiplicity
			}
			for k := 0; k < edgeCount; k++ {
				c.addEdge(Edge{aVertices[ai-1], aVertices[ai]}, aInputID)
			}
			for k := edgeCount; k < 0; k++ {
				c.addEdge(Edge{aVertices[ai], aVertices[ai-1]}, aInputID)
			}
			multiplicity += aNumCrossings[ai]
		}
		inside = multiplicity != 0
		if c.inputDimensions[aInputID] != 0 {
			for ai := range aVertices {
				if aIsolated[ai] {
					c.addEdge(Edge{aVertices[ai], aVertices[ai]}, aInputID)
				}
			}
		}
	}
}

func searchCrossing(b []crossingInputEdge, id InputEdgeID) int {
	return sort.Search(len(b), func(i int) bool { return b[i].inputID >= id })
}

func (c *graphEdgeClipper) gatherIncidentEdges(a []VertexID, ai int, bInput []crossingInputEdge, bEdges [][]crossingGraphEdge) {
	for _, e := range c.in.EdgeIDs(a[ai]) {
		id := c.inputIDs[e]
		k := searchCrossing(bInput, id)
		if k < len(bInput) && bInput[k].inputID == id {
			bEdges[k] = append(bEdges[k], crossingGraphEdge{e, ai, false, c.g.edges[e].V0})
		}
	}
	lo, hi := c.out.EdgeIDs(a[ai])
	for e := lo; e < hi; e++ {
		id := c.inputIDs[e]
		k := searchCrossing(bInput, id)
		if k < len(bInput) && bInput[k].inputID == id {
			bEdges[k] = append(bEdges[k], crossingGraphEdge{e, ai, true, c.g.edges[e].V1})
		}
	}
}

func (c *graphEdgeClipper) vertexRank(e crossingGraphEdge) int {
	r := c.rank[e.id]
	if !e.outgoing {
		r++
	}
	return r
}

func (c *graphEdgeClipper) getCrossedVertexIndex(a []VertexID, b []crossingGraphEdge, leftToRight bool) int {
	if len(a) == 0 || len(b) == 0 {
		return -1
	}
	n := len(a)
	if n == 1 {
		return 0
	}
	if b[0].aIndex == b[len(b)-1].aIndex {
		return b[0].aIndex
	}
	bReversed := c.vertexRank(b[0]) > c.vertexRank(b[len(b)-1])
	lo, hi := -1, len(c.order)
	bFirst, bLast := EdgeID(-1), EdgeID(-1)
	for _, e := range b {
		ai := e.aIndex
		if ai == 0 {
			if e.outgoing != bReversed && e.dst != a[1] {
				bFirst = e.id
			}
		} else if ai == n-1 {
			if e.outgoing == bReversed && e.dst != a[n-2] {
				bLast = e.id
			}
		} else {
			if e.dst == a[ai-1] || e.dst == a[ai+1] {
				continue
			}
			onLeft := orderedCCW(c.g.vertices[a[ai+1]], c.g.vertices[e.dst], c.g.vertices[a[ai-1]], c.g.vertices[a[ai]])
			if leftToRight == onLeft {
				lo = max(lo, c.rank[e.id]+1)
			} else {
				hi = min(hi, c.rank[e.id])
			}
		}
	}
	if bFirst >= 0 && bLast >= 0 {
		if bReversed {
			bFirst, bLast = bLast, bFirst
		}
		hasInterior := false
		for _, e := range b {
			if e.aIndex > 0 && e.aIndex < n-1 && c.rank[e.id] >= c.rank[bFirst] && c.rank[e.id] <= c.rank[bLast] {
				hasInterior = true
				break
			}
		}
		if !hasInterior {
			onLeft := c.edgeChainOnLeft(a, bFirst, bLast)
			if leftToRight == onLeft {
				lo = max(lo, c.rank[bLast]+1)
			} else {
				hi = min(hi, c.rank[bFirst])
			}
		}
	}
	best := -1
	for _, e := range b {
		ai := e.aIndex
		vr := c.vertexRank(e)
		if vr >= lo && vr <= hi && (best < 0 || a[ai] < a[best]) {
			best = ai
		}
	}
	return best
}

func (c *graphEdgeClipper) edgeChainOnLeft(a []VertexID, bFirst, bLast EdgeID) bool {
	var loop []VertexID
	for i := c.rank[bFirst]; i < c.rank[bLast]; i++ {
		loop = append(loop, c.g.edges[c.order[i]].V1)
	}
	if c.g.edges[bLast].V1 != a[0] {
		for i, j := 0, len(loop)-1; i < j; i, j = i+1, j-1 {
			loop[i], loop[j] = loop[j], loop[i]
		}
	}
	loop = append(loop, a...)
	loop = append(loop, loop[0], loop[1])
	sum := 0.0
	for i := 2; i < len(loop); i++ {
		sum += float64(s2.TurnAngle(c.g.vertices[loop[i-2]], c.g.vertices[loop[i-1]], c.g.vertices[loop[i]]))
	}
	return sum > 0
}

type edgeClippingLayer struct {
	layers          []Layer
	inputDimensions *[]int8
	inputCrossings  *[]inputEdgeCrossing
}

func (l *edgeClippingLayer) GraphOptions() GraphOptions {
	return NewGraphOptions(EdgeDirected, DegenerateKeep, DuplicateKeep, SiblingKeep)
}

func (l *edgeClippingLayer) Build(g *Graph) error {
	c := newGraphEdgeClipper(g, *l.inputDimensions, *l.inputCrossings)
	c.run()
	lex := g.inputEdgeIDSetLex
	if len(l.layers) == 1 {
		ng, err := g.MakeSubgraph(l.layers[0].GraphOptions(), &c.newEdges, &c.newInputIDs, lex, g.isFullPolygonPred)
		if e2 := l.layers[0].Build(ng); err == nil {
			err = e2
		}
		return err
	}
	var le [3][]Edge
	var li [3][]InputEdgeIDSetID
	for i, e := range c.newEdges {
		d := (*l.inputDimensions)[c.newInputIDs[i]]
		le[d] = append(le[d], e)
		li[d] = append(li[d], c.newInputIDs[i])
	}
	var firstErr error
	for d := 0; d < 3; d++ {
		ng, err := g.MakeSubgraph(l.layers[d].GraphOptions(), &le[d], &li[d], lex, g.isFullPolygonPred)
		if err == nil {
			err = l.layers[d].Build(ng)
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type shapeEdgeID struct{ shape, edge int32 }

func shapeEdgeIDLess(a, b shapeEdgeID) bool {
	return a.shape < b.shape || (a.shape == b.shape && a.edge < b.edge)
}

var sentinelID = shapeEdgeID{math.MaxInt32, 0}

type shapeEdge struct {
	id     shapeEdgeID
	v0, v1 s2.Point
}

type indexCrossing struct {
	a, b                                      shapeEdgeID
	isInteriorCrossing, leftToRight, isVertex bool
}

func indexCrossingLess(x, y indexCrossing) bool {
	if x.a != y.a {
		return shapeEdgeIDLess(x.a, y.a)
	}
	return shapeEdgeIDLess(x.b, y.b)
}

type sourceID struct {
	region int8
	shape  int32
	edge   int32
}

func specialSourceID(e InputEdgeID) sourceID { return sourceID{0, 0, e} }

type sourceEdgeCrossing struct {
	src         sourceID
	leftToRight bool
}

type boolImpl struct {
	op                          *BooleanOperation
	builderOpts                 BuilderOptions
	builder                     *Builder
	inputDimensions             []int8
	inputCrossings              []inputEdgeCrossing
	indexCrossings              []indexCrossing
	indexCrossingsFirstRegionID int
	tmpCrossings                []indexCrossing
}

func (b *boolImpl) isBooleanOutput() bool { return b.op.resultEmpty != nil }

type crossingIterator struct {
	bIndex            *s2.ShapeIndex
	crossings         []indexCrossing
	pos               int
	bShape            s2.Shape
	bShapeID          int32
	bDimension        int
	crossingsComplete bool
	chainID           int
	chainStart        int
	chainLimit        int
}

func newCrossingIterator(bIndex *s2.ShapeIndex, crossings []indexCrossing, complete bool) *crossingIterator {
	it := &crossingIterator{bIndex: bIndex, crossings: crossings, bShapeID: -1, crossingsComplete: complete}
	it.update()
	return it
}

func (it *crossingIterator) next()                    { it.pos++; it.update() }
func (it *crossingIterator) done(id shapeEdgeID) bool { return it.aID() != id }
func (it *crossingIterator) aID() shapeEdgeID         { return it.crossings[it.pos].a }
func (it *crossingIterator) bID() shapeEdgeID         { return it.crossings[it.pos].b }
func (it *crossingIterator) isInteriorCrossing() bool { return it.crossings[it.pos].isInteriorCrossing }
func (it *crossingIterator) isVertexCrossing() bool   { return it.crossings[it.pos].isVertex }
func (it *crossingIterator) leftToRight() bool        { return it.crossings[it.pos].leftToRight }
func (it *crossingIterator) bEdgeID() int             { return int(it.bID().edge) }
func (it *crossingIterator) bEdge() s2.Edge           { return it.bShape.Edge(it.bEdgeID()) }

func (it *crossingIterator) bChainInfo() (chainID, start, limit int) {
	if it.chainID < 0 {
		it.chainID = it.bShape.ChainPosition(it.bEdgeID()).ChainID
		ch := it.bShape.Chain(it.chainID)
		it.chainStart = ch.Start
		it.chainLimit = ch.Start + ch.Length
	}
	return it.chainID, it.chainStart, it.chainLimit
}

func (it *crossingIterator) update() {
	if it.aID() != sentinelID && it.bID().shape != it.bShapeID {
		it.bShapeID = it.bID().shape
		it.bShape = it.bIndex.Shape(it.bShapeID)
		it.bDimension = it.bShape.Dimension()
		it.chainID = -1
	}
}

type crossingProcessor struct {
	polygonModel                PolygonModel
	polylineModel               PolylineModel
	polylineLoopsHaveBoundaries bool
	builder                     *Builder
	inputDimensions             *[]int8
	inputCrossings              *[]inputEdgeCrossing

	aRegionID, bRegionID            int
	invertA, invertB, invertResult  bool
	isUnion                         bool
	aShape                          s2.Shape
	aDimension                      int
	chainID, chainStart, chainLimit int
	sourceEdgeCrossings             []struct {
		id InputEdgeID
		c  sourceEdgeCrossing
	}
	pendingSourceEdgeCrossings []sourceEdgeCrossing
	sourceIDMap                map[sourceID]InputEdgeID
	isDegenerateHole           map[shapeEdgeID]bool
	inside, prevInside         bool
	v0EmittedMaxEdgeID         int
	chainV0Emitted             bool
}

func (cp *crossingProcessor) inputEdgeID() InputEdgeID { return InputEdgeID(len(*cp.inputDimensions)) }

func (cp *crossingProcessor) isV0Isolated(a shapeEdgeID) bool {
	return !cp.inside && cp.v0EmittedMaxEdgeID < int(a.edge)
}

func (cp *crossingProcessor) isChainLastVertexIsolated(a shapeEdgeID) bool {
	return int(a.edge) == cp.chainLimit-1 && !cp.chainV0Emitted && cp.v0EmittedMaxEdgeID <= int(a.edge)
}

func (cp *crossingProcessor) polylineContainsV0(edgeID, chainStart int) bool {
	return cp.polylineModel != PolylineOpen || edgeID > chainStart
}

func (cp *crossingProcessor) isDegenerate(a shapeEdgeID) bool {
	_, ok := cp.isDegenerateHole[a]
	return ok
}

func (cp *crossingProcessor) addCrossing(c sourceEdgeCrossing) {
	cp.sourceEdgeCrossings = append(cp.sourceEdgeCrossings, struct {
		id InputEdgeID
		c  sourceEdgeCrossing
	}{cp.inputEdgeID(), c})
}

func (cp *crossingProcessor) setClippingState(param InputEdgeID, state bool) {
	cp.addCrossing(sourceEdgeCrossing{specialSourceID(param), state})
}

func (cp *crossingProcessor) addEdge(aID shapeEdgeID, a s2.Edge, dimension int, interiorCrossings int) bool {
	if cp.builder == nil {
		return false
	}
	if interiorCrossings > 0 {
		for _, c := range cp.pendingSourceEdgeCrossings {
			cp.addCrossing(c)
		}
		cp.sourceIDMap[sourceID{int8(cp.aRegionID), aID.shape, aID.edge}] = cp.inputEdgeID()
	}
	if cp.inside != cp.prevInside {
		cp.setClippingState(setInside, cp.inside)
	}
	*cp.inputDimensions = append(*cp.inputDimensions, int8(dimension))
	cp.builder.AddEdge(a.V0, a.V1)
	if interiorCrossings&1 != 0 {
		cp.inside = !cp.inside
	}
	cp.prevInside = cp.inside
	return true
}

func (cp *crossingProcessor) addPointEdge(p s2.Point, dimension int) bool {
	if cp.builder == nil {
		return false
	}
	if !cp.prevInside {
		cp.setClippingState(setInside, true)
	}
	*cp.inputDimensions = append(*cp.inputDimensions, int8(dimension))
	cp.builder.AddEdge(p, p)
	cp.prevInside = true
	return true
}

func (cp *crossingProcessor) startBoundary(aRegionID int, invertA, invertB, invertResult bool) {
	cp.aRegionID = aRegionID
	cp.bRegionID = 1 - aRegionID
	cp.invertA, cp.invertB, cp.invertResult = invertA, invertB, invertResult
	cp.isUnion = invertB && invertResult
	cp.setClippingState(setReverseA, invertA != invertResult)
	cp.setClippingState(setInvertB, invertB)
}

func (cp *crossingProcessor) startShape(s s2.Shape) {
	cp.aShape = s
	cp.aDimension = s.Dimension()
}

func (cp *crossingProcessor) startChain(chainID int, chain s2.Chain, inside bool) {
	cp.chainID = chainID
	cp.chainStart = chain.Start
	cp.chainLimit = chain.Start + chain.Length
	cp.inside = inside
	cp.v0EmittedMaxEdgeID = chain.Start - 1
	cp.chainV0Emitted = false
}

func (cp *crossingProcessor) processEdge(aID shapeEdgeID, it *crossingIterator) bool {
	a := cp.aShape.ChainEdge(cp.chainID, int(aID.edge)-cp.chainStart)
	switch cp.aDimension {
	case 0:
		return cp.processEdge0(aID, a, it)
	case 1:
		return cp.processEdge1(aID, a, it)
	}
	return cp.processEdge2(aID, a, it)
}

type pointCrossingResult struct{ matchesPoint, matchesPolyline, matchesPolygon bool }

func (cp *crossingProcessor) processEdge0(aID shapeEdgeID, a s2.Edge, it *crossingIterator) bool {
	if cp.invertA != cp.invertResult {
		cp.skipCrossings(aID, it)
		return true
	}
	r := cp.processPointCrossings(aID, a.V0, it)
	contained := cp.inside != cp.invertB
	if r.matchesPolygon && cp.polygonModel != PolygonSemiOpen {
		contained = cp.polygonModel == PolygonClosed
	}
	if r.matchesPolyline {
		contained = true
	}
	if r.matchesPoint && !cp.isUnion {
		contained = true
	}
	if contained == cp.invertB {
		return true
	}
	return cp.addPointEdge(a.V0, 0)
}

func (cp *crossingProcessor) skipCrossings(aID shapeEdgeID, it *crossingIterator) {
	for !it.done(aID) {
		it.next()
	}
}

func (cp *crossingProcessor) processPointCrossings(aID shapeEdgeID, a0 s2.Point, it *crossingIterator) pointCrossingResult {
	var r pointCrossingResult
	for ; !it.done(aID); it.next() {
		switch it.bDimension {
		case 0:
			r.matchesPoint = true
		case 1:
			if cp.polylineEdgeContainsVertex(a0, it, 0) {
				r.matchesPolyline = true
			}
		default:
			r.matchesPolygon = true
		}
	}
	return r
}

type edgeCrossingResult struct {
	matchesPolyline, a0MatchesPolyline, a1MatchesPolyline bool
	a0MatchesPolygon, a1MatchesPolygon                    bool
	polygonMatchID, siblingMatchID, a0LoopMatchID         shapeEdgeID
	a0Crossings, a1Crossings, interiorCrossings           int
}

func newEdgeCrossingResult() edgeCrossingResult {
	none := shapeEdgeID{0, -1}
	return edgeCrossingResult{polygonMatchID: none, siblingMatchID: none, a0LoopMatchID: none}
}

func (r *edgeCrossingResult) matchesPolygon() bool { return r.polygonMatchID.edge >= 0 }
func (r *edgeCrossingResult) matchesSibling() bool { return r.siblingMatchID.edge >= 0 }
func (r *edgeCrossingResult) loopMatchesA0() bool  { return r.a0LoopMatchID.edge >= 0 }

func (cp *crossingProcessor) processEdge1(aID shapeEdgeID, a s2.Edge, it *crossingIterator) bool {
	if cp.invertA != cp.invertResult {
		cp.skipCrossings(aID, it)
		return true
	}
	r := cp.processEdgeCrossings(aID, a, it)
	a0Inside := cp.isPolylineVertexInside(r.a0MatchesPolyline, r.a0MatchesPolygon)
	isDegen := a.V0 == a.V1
	if r.a0Crossings&1 != 0 {
		cp.inside = !cp.inside
	}
	if cp.inside != cp.isPolylineEdgeInside(&r, isDegen) {
		cp.inside = !cp.inside
		r.a1Crossings++
	}
	if !cp.polylineLoopsHaveBoundaries && int(aID.edge) == cp.chainStart &&
		a.V0 == cp.aShape.ChainEdge(cp.chainID, cp.chainLimit-cp.chainStart-1).V1 {
		cp.chainV0Emitted = cp.inside
	} else if cp.isV0Isolated(aID) && !isDegen && cp.polylineContainsV0(int(aID.edge), cp.chainStart) && a0Inside {
		if !cp.addPointEdge(a.V0, 1) {
			return false
		}
	}
	if cp.inside || r.interiorCrossings > 0 {
		if !cp.addEdge(aID, a, 1, r.interiorCrossings) {
			return false
		}
	}
	if cp.inside {
		cp.v0EmittedMaxEdgeID = int(aID.edge) + 1
	}
	if r.a1Crossings&1 != 0 {
		cp.inside = !cp.inside
	}
	if it.crossingsComplete && !isDegen && cp.isChainLastVertexIsolated(aID) &&
		(cp.polylineModel == PolylineClosed ||
			(!cp.polylineLoopsHaveBoundaries && a.V1 == cp.aShape.ChainEdge(cp.chainID, cp.chainStart).V0)) &&
		cp.isPolylineVertexInside(r.a1MatchesPolyline, r.a1MatchesPolygon) {
		if !cp.addPointEdge(a.V1, 1) {
			return false
		}
	}
	return true
}

func (cp *crossingProcessor) isPolylineVertexInside(matchesPolyline, matchesPolygon bool) bool {
	contained := cp.inside != cp.invertB
	if matchesPolyline && !cp.isUnion {
		contained = true
	} else if matchesPolygon && cp.polygonModel != PolygonSemiOpen {
		contained = cp.polygonModel == PolygonClosed
	}
	return contained != cp.invertB
}

func (cp *crossingProcessor) isPolylineEdgeInside(r *edgeCrossingResult, isDegen bool) bool {
	contained := cp.inside != cp.invertB
	if r.matchesPolyline && !cp.isUnion {
		contained = true
	} else if isDegen {
		if cp.polygonModel != PolygonSemiOpen && r.a0MatchesPolygon {
			contained = cp.polygonModel == PolygonClosed
		}
		if r.a0MatchesPolyline && !cp.isUnion {
			contained = true
		}
	} else if r.matchesPolygon() {
		if !(cp.polygonModel == PolygonSemiOpen && r.matchesSibling()) {
			contained = cp.polygonModel != PolygonOpen
		}
	} else if r.matchesSibling() {
		contained = cp.polygonModel == PolygonClosed
	}
	return contained != cp.invertB
}

func (cp *crossingProcessor) processEdge2(aID shapeEdgeID, a s2.Edge, it *crossingIterator) bool {
	emitShared := cp.aRegionID == 1
	createDegen := (cp.polygonModel == PolygonClosed && !cp.invertA && !cp.invertB) ||
		(cp.polygonModel == PolygonOpen && cp.invertA && cp.invertB)
	keepDegenA := cp.polygonModel == PolygonOpen && cp.invertB
	keepDegenB := cp.polygonModel == PolygonOpen && cp.invertA
	r := cp.processEdgeCrossings(aID, a, it)
	if cp.invertA != cp.invertB {
		r.polygonMatchID, r.siblingMatchID = r.siblingMatchID, r.polygonMatchID
	}
	isPoint := a.V0 == a.V1
	if !emitShared {
		if r.loopMatchesA0() {
			cp.isDegenerateHole[r.a0LoopMatchID] = cp.inside
			if isPoint {
				return true
			}
		}
		if cp.polygonModel != PolygonSemiOpen {
			if isPoint && r.a0MatchesPolygon {
				return true
			}
		}
	}
	if r.a0Crossings&1 != 0 {
		cp.inside = !cp.inside
	}
	if !emitShared {
		if r.matchesPolygon() || r.matchesSibling() {
			if r.matchesPolygon() && r.matchesSibling() {
				cp.isDegenerateHole[r.polygonMatchID] = cp.inside
				cp.isDegenerateHole[r.siblingMatchID] = cp.inside
			}
			if r.a1Crossings&1 != 0 {
				cp.inside = !cp.inside
			}
			return true
		}
	}
	isBHole := r.matchesPolygon() && r.matchesSibling() && cp.inside
	semiOpenInside := cp.inside
	if isPoint {
		if r.loopMatchesA0() {
			cp.inside = createDegen || keepDegenA || (cp.inside == cp.isDegenerateHole[r.a0LoopMatchID])
		} else if r.a0MatchesPolygon {
			if cp.polygonModel != PolygonSemiOpen {
				cp.inside = createDegen || keepDegenA
			}
		}
	} else if r.matchesPolygon() {
		if cp.isDegenerate(aID) {
			cp.inside = createDegen || keepDegenA || ((!r.matchesSibling() || cp.inside) == cp.isDegenerateHole[aID])
		} else if !r.matchesSibling() || createDegen || keepDegenB {
			cp.inside = true
		}
	} else if r.matchesSibling() {
		if cp.isDegenerate(aID) {
			cp.inside = (createDegen || keepDegenA) && !cp.isDegenerateHole[aID]
		} else {
			cp.inside = createDegen
		}
	}
	if cp.inside != semiOpenInside {
		r.a1Crossings++
	}
	if emitShared && r.a0MatchesPolygon && !cp.inside && (createDegen || (keepDegenB && r.loopMatchesA0())) {
		if !cp.addPointEdge(a.V0, 2) {
			return false
		}
	}
	if r.matchesSibling() && (createDegen || keepDegenB) && !cp.isDegenerate(aID) && !isBHole {
		if !cp.addEdge(r.siblingMatchID, s2.Edge{V0: a.V1, V1: a.V0}, 2, 0) {
			return false
		}
	}
	if cp.inside || r.interiorCrossings > 0 {
		if !cp.addEdge(aID, a, 2, r.interiorCrossings) {
			return false
		}
	}
	if r.a1Crossings&1 != 0 {
		cp.inside = !cp.inside
	}
	return true
}

func (cp *crossingProcessor) processEdgeCrossings(aID shapeEdgeID, a s2.Edge, it *crossingIterator) edgeCrossingResult {
	cp.pendingSourceEdgeCrossings = cp.pendingSourceEdgeCrossings[:0]
	r := newEdgeCrossingResult()
	if it.done(aID) {
		return r
	}
	for ; !it.done(aID); it.next() {
		if it.bDimension == 0 {
			continue
		}
		b := it.bEdge()
		if it.isInteriorCrossing() {
			if cp.aDimension <= it.bDimension && !(cp.invertB != cp.invertResult && it.bDimension == 1) {
				src := sourceID{int8(cp.bRegionID), it.bShapeID, int32(it.bEdgeID())}
				cp.pendingSourceEdgeCrossings = append(cp.pendingSourceEdgeCrossings, sourceEdgeCrossing{src, it.leftToRight()})
			}
			if it.bDimension == 1 {
				r.interiorCrossings += 2
			} else {
				r.interiorCrossings++
			}
		} else if it.bDimension == 1 {
			if cp.aDimension == 2 {
				continue
			}
			if (a.V0 == b.V0 && a.V1 == b.V1) || (a.V0 == b.V1 && a.V1 == b.V0) {
				r.matchesPolyline = true
			}
			if (a.V0 == b.V0 || a.V0 == b.V1) && cp.polylineEdgeContainsVertex(a.V0, it, 1) {
				r.a0MatchesPolyline = true
			}
			if (a.V1 == b.V0 || a.V1 == b.V1) && cp.polylineEdgeContainsVertex(a.V1, it, 1) {
				r.a1MatchesPolyline = true
			}
		} else {
			if a.V0 == a.V1 || b.V0 == b.V1 {
				if a.V0 == b.V0 && a.V0 == b.V1 {
					r.a0LoopMatchID = it.bID()
				}
			} else if a.V0 == b.V0 && a.V1 == b.V1 {
				r.a0Crossings++
				r.polygonMatchID = it.bID()
			} else if a.V0 == b.V1 && a.V1 == b.V0 {
				r.a0Crossings++
				r.siblingMatchID = it.bID()
			} else if it.isVertexCrossing() {
				if a.V0 == b.V0 || a.V0 == b.V1 {
					r.a0Crossings++
				} else {
					r.a1Crossings++
				}
			}
			if a.V0 == b.V0 || a.V0 == b.V1 {
				r.a0MatchesPolygon = true
			}
			if a.V1 == b.V0 || a.V1 == b.V1 {
				r.a1MatchesPolygon = true
			}
		}
	}
	return r
}

func (cp *crossingProcessor) polylineEdgeContainsVertex(v s2.Point, it *crossingIterator, dimension int) bool {
	if cp.polylineModel == PolylineClosed {
		return true
	}
	chainID, start, limit := it.bChainInfo()
	bEdgeID := it.bEdgeID()
	be := it.bEdge()
	if bEdgeID == limit-1 && v == be.V1 && (dimension == 0 || bEdgeID > 0 || v != be.V0) {
		return false
	}
	if cp.polylineContainsV0(bEdgeID, start) {
		return true
	}
	if v != be.V0 {
		return true
	}
	if cp.polylineLoopsHaveBoundaries {
		return false
	}
	return v == it.bShape.ChainEdge(chainID, limit-start-1).V1
}

func (cp *crossingProcessor) doneBoundaryPair() {
	cp.sourceIDMap[specialSourceID(setInside)] = setInside
	cp.sourceIDMap[specialSourceID(setInvertB)] = setInvertB
	cp.sourceIDMap[specialSourceID(setReverseA)] = setReverseA
	for _, t := range cp.sourceEdgeCrossings {
		id := cp.sourceIDMap[t.c.src]
		*cp.inputCrossings = append(*cp.inputCrossings, inputEdgeCrossing{t.id, crossingInputEdge{id, t.c.leftToRight}})
	}
	cp.sourceEdgeCrossings = cp.sourceEdgeCrossings[:0]
	cp.sourceIDMap = map[sourceID]InputEdgeID{}
}

func numShapeIDs(idx *s2.ShapeIndex) int32 { return int32(idx.Len()) }

func (b *boolImpl) addBoundary(aRegionID int, invertA, invertB, invertResult bool, aChainStarts []shapeEdgeID, cp *crossingProcessor) bool {
	aIndex := b.op.regions[aRegionID]
	bIndex := b.op.regions[1-aRegionID]
	if !b.initIndexCrossings(aRegionID) {
		return false
	}
	cp.startBoundary(aRegionID, invertA, invertB, invertResult)
	csi := 0
	it := newCrossingIterator(bIndex, b.indexCrossings, true)
	minID := func(x, y shapeEdgeID) shapeEdgeID {
		if shapeEdgeIDLess(y, x) {
			return y
		}
		return x
	}
	nextID := minID(aChainStarts[csi], it.aID())
	for nextID != sentinelID {
		aShapeID := nextID.shape
		aShape := aIndex.Shape(aShapeID)
		cp.startShape(aShape)
		for nextID.shape == aShapeID {
			edgeID := int(nextID.edge)
			chainID := aShape.ChainPosition(edgeID).ChainID
			chain := aShape.Chain(chainID)
			startInside := nextID == aChainStarts[csi]
			if startInside {
				csi++
			}
			cp.startChain(chainID, chain, startInside)
			chainLimit := chain.Start + chain.Length
			for edgeID < chainLimit {
				aID := shapeEdgeID{aShapeID, int32(edgeID)}
				if !cp.processEdge(aID, it) {
					return false
				}
				if cp.inside {
					edgeID++
				} else if it.aID().shape == aShapeID && int(it.aID().edge) < chainLimit {
					edgeID = int(it.aID().edge)
				} else {
					break
				}
			}
			nextID = minID(aChainStarts[csi], it.aID())
		}
	}
	return true
}

func hasInterior(idx *s2.ShapeIndex) bool {
	for s := numShapeIDs(idx) - 1; s >= 0; s-- {
		if sh := idx.Shape(s); sh != nil && sh.Dimension() == 2 {
			return true
		}
	}
	return false
}

func (b *boolImpl) getChainStarts(aRegionID int, invertA, invertB, invertResult bool, cp *crossingProcessor) ([]shapeEdgeID, bool) {
	aIndex := b.op.regions[aRegionID]
	bIndex := b.op.regions[1-aRegionID]
	var starts []shapeEdgeID
	if b.isBooleanOutput() {
		cp.startBoundary(aRegionID, invertA, invertB, invertResult)
	}
	bHasInterior := hasInterior(bIndex)
	if bHasInterior || invertB || b.isBooleanOutput() {
		query := s2.NewContainsPointQuery(bIndex, s2.VertexModelSemiOpen)
		n := numShapeIDs(aIndex)
		for shapeID := int32(0); shapeID < n; shapeID++ {
			aShape := aIndex.Shape(shapeID)
			if aShape == nil {
				continue
			}
			if invertA != invertResult && aShape.Dimension() < 2 {
				continue
			}
			if b.isBooleanOutput() {
				cp.startShape(aShape)
			}
			for chainID := 0; chainID < aShape.NumChains(); chainID++ {
				chain := aShape.Chain(chainID)
				if chain.Length == 0 {
					continue
				}
				e := aShape.ChainEdge(chainID, 0)
				a := shapeEdge{shapeEdgeID{shapeID, int32(chain.Start)}, e.V0, e.V1}
				inside := (bHasInterior && query.Contains(a.v0)) != invertB
				if inside {
					starts = append(starts, shapeEdgeID{shapeID, int32(chain.Start)})
				}
				if b.isBooleanOutput() {
					cp.startChain(chainID, chain, inside)
					if !b.processIncidentEdges(a, bIndex, cp) {
						return nil, false
					}
				}
			}
		}
	}
	starts = append(starts, sentinelID)
	return starts, true
}

func (b *boolImpl) processIncidentEdges(a shapeEdge, bIndex *s2.ShapeIndex, cp *crossingProcessor) bool {
	b.tmpCrossings = b.tmpCrossings[:0]
	visitIncidentEdges(bIndex, a.v0, func(e shapeEdge) {
		b.tmpCrossings = b.addIndexCrossing(a, e, false, b.tmpCrossings)
	})
	if len(b.tmpCrossings) == 0 {
		return !cp.inside
	}
	sort.Slice(b.tmpCrossings, func(i, j int) bool { return indexCrossingLess(b.tmpCrossings[i], b.tmpCrossings[j]) })
	b.tmpCrossings = append(b.tmpCrossings, indexCrossing{a: sentinelID, b: sentinelID})
	it := newCrossingIterator(bIndex, b.tmpCrossings, false)
	return cp.processEdge(a.id, it)
}

func (b *boolImpl) addIndexCrossing(a, e shapeEdge, isInterior bool, crossings []indexCrossing) []indexCrossing {
	c := indexCrossing{a: a.id, b: e.id}
	if isInterior {
		c.isInteriorCrossing = true
		if sign(a.v0, a.v1, e.v0) > 0 {
			c.leftToRight = true
		}
		b.builder.AddIntersection(s2.Intersection(a.v0, a.v1, e.v0, e.v1))
	} else if s2.VertexCrossing(a.v0, a.v1, e.v0, e.v1) {
		c.isVertex = true
	}
	return append(crossings, c)
}

func (b *boolImpl) initIndexCrossings(regionID int) bool {
	if regionID == b.indexCrossingsFirstRegionID {
		return true
	}
	if b.indexCrossingsFirstRegionID < 0 {
		stop := false
		visitCrossingEdgePairs(b.op.regions[0], b.op.regions[1], func(a, e shapeEdge, isInterior bool) bool {
			if isInterior && b.isBooleanOutput() {
				stop = true
				return false
			}
			b.indexCrossings = b.addIndexCrossing(a, e, isInterior, b.indexCrossings)
			return true
		})
		if stop {
			return false
		}
		if len(b.indexCrossings) > 1 {
			sort.Slice(b.indexCrossings, func(i, j int) bool { return indexCrossingLess(b.indexCrossings[i], b.indexCrossings[j]) })
			out := b.indexCrossings[:1]
			for _, c := range b.indexCrossings[1:] {
				last := out[len(out)-1]
				if c.a == last.a && c.b == last.b {
					continue
				}
				out = append(out, c)
			}
			b.indexCrossings = out
		}
		b.indexCrossings = append(b.indexCrossings, indexCrossing{a: sentinelID, b: sentinelID})
		b.indexCrossingsFirstRegionID = 0
	}
	if regionID != b.indexCrossingsFirstRegionID {
		for i := range b.indexCrossings {
			c := &b.indexCrossings[i]
			c.a, c.b = c.b, c.a
			c.leftToRight = !c.leftToRight
			c.isVertex = !c.isVertex
		}
		sort.Slice(b.indexCrossings, func(i, j int) bool { return indexCrossingLess(b.indexCrossings[i], b.indexCrossings[j]) })
		b.indexCrossingsFirstRegionID = regionID
	}
	return true
}

func (b *boolImpl) addBoundaryPair(invertA, invertB, invertResult bool, cp *crossingProcessor) bool {
	t := b.op.opType
	if t == OpDifference || t == OpSymmetricDifference {
		if b.areRegionsIdentical() {
			return true
		}
	}
	aStarts, ok := b.getChainStarts(0, invertA, invertB, invertResult, cp)
	if !ok {
		return false
	}
	bStarts, ok := b.getChainStarts(1, invertB, invertA, invertResult, cp)
	if !ok {
		return false
	}
	if !b.addBoundary(0, invertA, invertB, invertResult, aStarts, cp) ||
		!b.addBoundary(1, invertB, invertA, invertResult, bStarts, cp) {
		return false
	}
	if !b.isBooleanOutput() {
		cp.doneBoundaryPair()
	}
	return true
}

func (b *boolImpl) buildOpType(t OpType) bool {
	o := b.op.opts
	cp := &crossingProcessor{
		polygonModel: o.PolygonModel, polylineModel: o.PolylineModel,
		polylineLoopsHaveBoundaries: o.PolylineLoopsHaveBoundaries,
		builder:                     b.builder, inputDimensions: &b.inputDimensions, inputCrossings: &b.inputCrossings,
		sourceIDMap: map[sourceID]InputEdgeID{}, isDegenerateHole: map[shapeEdgeID]bool{},
	}
	switch t {
	case OpUnion:
		return b.addBoundaryPair(true, true, true, cp)
	case OpIntersection:
		return b.addBoundaryPair(false, false, false, cp)
	case OpDifference:
		return b.addBoundaryPair(false, true, false, cp)
	}
	return b.addBoundaryPair(false, true, false, cp) && b.addBoundaryPair(true, false, false, cp)
}

const allFacesMask = 0x3f

func faceMask(idx *s2.ShapeIndex) uint8 {
	var mask uint8
	it := idx.Iterator()
	for it.Begin(); !it.Done(); it.Next() {
		mask |= 1 << it.CellID().Face()
	}
	return mask
}

func (b *boolImpl) isFullPolygonResult() bool {
	a, c := b.op.regions[0], b.op.regions[1]
	switch b.op.opType {
	case OpUnion:
		if faceMask(a)|faceMask(c) != allFacesMask {
			return false
		}
		aa, ca := indexArea(a), indexArea(c)
		minA := math.Max(aa, ca)
		maxA := math.Min(4*math.Pi, aa+ca)
		return minA > 4*math.Pi-maxA
	case OpIntersection:
		if faceMask(a)&faceMask(c) != allFacesMask {
			return false
		}
		aa, ca := indexArea(a), indexArea(c)
		minA := math.Max(0, aa+ca-4*math.Pi)
		maxA := math.Min(aa, ca)
		return minA > 4*math.Pi-maxA
	case OpDifference:
		if faceMask(a) != allFacesMask {
			return false
		}
		aa, ca := indexArea(a), indexArea(c)
		minA := math.Max(0, aa-ca)
		maxA := math.Min(aa, 4*math.Pi-ca)
		return minA > 4*math.Pi-maxA
	}
	am, cm := faceMask(a), faceMask(c)
	if am|cm != allFacesMask {
		return false
	}
	aa, ca := indexArea(a), indexArea(c)
	minA := math.Abs(aa - ca)
	maxA := 4*math.Pi - math.Abs(4*math.Pi-(aa+ca))
	hemi := 2*math.Pi*b.builderOpts.edgeSnapRadius().Radians() + 40*dblEpsilon
	errSign := minA - (4*math.Pi - maxA)
	if math.Abs(errSign) <= hemi {
		return am&cm != allFacesMask
	}
	return errSign > 0
}

func (b *boolImpl) areRegionsIdentical() bool {
	a, c := b.op.regions[0], b.op.regions[1]
	if a == c {
		return true
	}
	n := numShapeIDs(a)
	if n != numShapeIDs(c) {
		return false
	}
	for s := int32(0); s < n; s++ {
		as, cs := a.Shape(s), c.Shape(s)
		d := as.Dimension()
		if d != cs.Dimension() || as.NumChains() != cs.NumChains() || as.NumEdges() != cs.NumEdges() {
			return false
		}
		if d == 0 {
			continue
		}
		for ch := 0; ch < as.NumChains(); ch++ {
			if as.Chain(ch).Length != cs.Chain(ch).Length {
				return false
			}
		}
	}
	for s := int32(0); s < n; s++ {
		as, cs := a.Shape(s), c.Shape(s)
		for ch := 0; ch < as.NumChains(); ch++ {
			l := as.Chain(ch).Length
			for i := 0; i < l; i++ {
				if as.ChainEdge(ch, i) != cs.ChainEdge(ch, i) {
					return false
				}
			}
		}
	}
	return true
}

func (b *boolImpl) build() error {
	o := NewBuilderOptions(b.op.opts.SnapFunction)
	o.IntersectionTolerance = intersectionError
	if b.op.opts.SplitAllCrossingPolylineEdges {
		o.SplitCrossingEdges = true
	}
	o.Idempotent = false
	b.builderOpts = o
	if b.isBooleanOutput() {
		empty := b.buildOpType(b.op.opType) && !b.isFullPolygonResult()
		*b.op.resultEmpty = empty
		return nil
	}
	b.builder = NewBuilder(o)
	b.builder.StartLayer(&edgeClippingLayer{layers: b.op.layers, inputDimensions: &b.inputDimensions,
		inputCrossings: &b.inputCrossings})
	b.builder.AddIsFullPolygonPredicate(func(*Graph) (bool, error) { return b.isFullPolygonResult(), nil })
	b.buildOpType(b.op.opType)
	b.indexCrossings = nil
	return b.builder.Build()
}
