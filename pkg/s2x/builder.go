package s2x

// Port of S2Builder (s2builder.{h,cc}): snap rounding of edges on the
// sphere with guaranteed topology preservation. Memory tracking is omitted.

import (
	"math"
	"slices"
	"sort"

	"github.com/golang/geo/r3"
	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

// maxSnapRadius is SnapFunction::kMaxSnapRadius().
const maxSnapRadius = 70 * s1.Degree

// Layer receives a built Graph (S2Builder::Layer).
type Layer interface {
	GraphOptions() GraphOptions
	Build(g *Graph) error
}

// BuilderOptions is S2Builder::Options.
type BuilderOptions struct {
	SnapFunction          s2.Snapper
	SplitCrossingEdges    bool
	IntersectionTolerance s1.Angle
	SimplifyEdgeChains    bool
	Idempotent            bool
}

// DefaultBuilderOptions is S2Builder::Options() (identity snapping with a
// zero radius, idempotent).
func DefaultBuilderOptions() BuilderOptions {
	return BuilderOptions{SnapFunction: IdentitySnapFunction{}, Idempotent: true}
}

// NewBuilderOptions is S2Builder::Options(snap_function).
func NewBuilderOptions(sf s2.Snapper) BuilderOptions {
	o := DefaultBuilderOptions()
	o.SnapFunction = sf
	return o
}

// SetSimplifyEdgeChains also clears Idempotent, as in C++.
func (o *BuilderOptions) SetSimplifyEdgeChains(v bool) {
	o.SimplifyEdgeChains = v
	o.Idempotent = false
}

func (o *BuilderOptions) intersectionTolerance() s1.Angle {
	if !o.SplitCrossingEdges {
		return o.IntersectionTolerance
	}
	return max(o.IntersectionTolerance, intersectionError)
}

func (o *BuilderOptions) edgeSnapRadius() s1.Angle {
	return o.SnapFunction.SnapRadius() + o.intersectionTolerance()
}

func (o *BuilderOptions) maxEdgeDeviation() s1.Angle {
	return 1.1 * o.edgeSnapRadius()
}

type inputVertexKey struct {
	cell s2.CellID
	id   int32
}

// Builder is S2Builder.
type Builder struct {
	opts BuilderOptions

	siteSnapRadiusCA           s1.ChordAngle
	edgeSnapRadiusCA           s1.ChordAngle
	checkAllSiteCrossings      bool
	maxEdgeDeviation           s1.Angle
	edgeSiteQueryRadiusCA      s1.ChordAngle
	minEdgeLengthToSplitCA     s1.ChordAngle
	minSiteSeparation          s1.Angle
	minSiteSeparationCA        s1.ChordAngle
	minEdgeSiteSeparationCA    s1.ChordAngle
	minEdgeSiteSeparationLimit s1.ChordAngle
	maxAdjacentSiteSepCA       s1.ChordAngle
	edgeSnapRadiusSin2         float64

	err               error
	snappingRequested bool
	snappingNeeded    bool
	labelSetModified  bool

	inputVertices []s2.Point
	inputEdges    [][2]int32

	layers          []Layer
	layerOptions    []GraphOptions
	layerBegins     []int32
	layerIsFullPred []IsFullPolygonPredicate

	labelSetIDs []LabelSetID
	labelSetLex *idSetLexicon
	labelSet    []Label
	labelSetID  LabelSetID

	numForcedSites int32
	sites          []s2.Point
	edgeSites      [][]int32
}

func roundUp(a s1.Angle) s1.ChordAngle {
	ca := chordFromAngle(a)
	return plusError(ca, s1AngleConstructorMaxError(ca))
}

func addPointToPointError(ca s1.ChordAngle) s1.ChordAngle {
	return plusError(ca, s2PointConstructorMaxError(ca))
}

func addPointToEdgeError(ca s1.ChordAngle) s1.ChordAngle {
	return plusError(ca, updateMinDistanceMaxError(ca))
}

// NewBuilder is S2Builder(options).
func NewBuilder(opts BuilderOptions) *Builder {
	if opts.SnapFunction == nil {
		opts.SnapFunction = IdentitySnapFunction{}
	}
	b := &Builder{opts: opts, labelSetLex: newIDSetLexicon()}
	sf := opts.SnapFunction
	snapRadius := sf.SnapRadius()
	b.siteSnapRadiusCA = chordFromAngle(snapRadius)
	edgeSnapRadius := opts.edgeSnapRadius()
	b.edgeSnapRadiusCA = roundUp(edgeSnapRadius)
	b.snappingRequested = edgeSnapRadius > 0
	b.maxEdgeDeviation = opts.maxEdgeDeviation()
	b.edgeSiteQueryRadiusCA = chordFromAngle(b.maxEdgeDeviation + sf.MinEdgeVertexSeparation())
	if !b.snappingRequested {
		b.minEdgeLengthToSplitCA = s1.InfChordAngle()
	} else {
		b.minEdgeLengthToSplitCA = chordFromAngle(s1.Angle(
			2 * math.Acos(math.Sin(edgeSnapRadius.Radians())/math.Sin(b.maxEdgeDeviation.Radians()))))
	}
	b.checkAllSiteCrossings = opts.maxEdgeDeviation() > opts.edgeSnapRadius()+sf.MinEdgeVertexSeparation()
	b.minSiteSeparation = sf.MinVertexSeparation()
	b.minSiteSeparationCA = chordFromAngle(b.minSiteSeparation)
	b.minEdgeSiteSeparationCA = chordFromAngle(sf.MinEdgeVertexSeparation())
	b.minEdgeSiteSeparationLimit = addPointToEdgeError(b.minEdgeSiteSeparationCA)
	b.maxAdjacentSiteSepCA = addPointToPointError(roundUp(2 * edgeSnapRadius))
	d := math.Sin(edgeSnapRadius.Radians())
	b.edgeSnapRadiusSin2 = d * d
	b.edgeSnapRadiusSin2 += ((9.5*d+2.5+2*sqrt3)*d + 9*dblEpsilon) * dblEpsilon
	b.labelSetID = emptySetID
	return b
}

// Options returns the builder's options.
func (b *Builder) Options() BuilderOptions { return b.opts }

func (b *Builder) ClearLabels() { b.labelSet = b.labelSet[:0]; b.labelSetModified = true }
func (b *Builder) PushLabel(l Label) {
	b.labelSet = append(b.labelSet, l)
	b.labelSetModified = true
}
func (b *Builder) PopLabel() { b.labelSet = b.labelSet[:len(b.labelSet)-1]; b.labelSetModified = true }
func (b *Builder) SetLabel(l Label) {
	b.labelSet = append(b.labelSet[:0], l)
	b.labelSetModified = true
}

// IsFullPolygonUnspecified is the default predicate for layers that can
// produce degenerate polygons: it reports an error and assumes empty.
func IsFullPolygonUnspecified(*Graph) (bool, error) { return false, ErrIsFullPredicateNotSpecified }

// StartLayer begins a new output layer.
func (b *Builder) StartLayer(l Layer) {
	b.layerOptions = append(b.layerOptions, l.GraphOptions())
	b.layerBegins = append(b.layerBegins, int32(len(b.inputEdges)))
	b.layerIsFullPred = append(b.layerIsFullPred, IsFullPolygon(false))
	b.layers = append(b.layers, l)
}

func (b *Builder) addVertex(v s2.Point) int32 {
	if len(b.inputVertices) == 0 || v != b.inputVertices[len(b.inputVertices)-1] {
		b.inputVertices = append(b.inputVertices, v)
	}
	return int32(len(b.inputVertices) - 1)
}

// AddIntersection adds a vertex computed as an edge intersection.
func (b *Builder) AddIntersection(v s2.Point) {
	b.snappingNeeded = true
	b.addVertex(v)
}

// AddEdge adds an input edge to the current layer.
func (b *Builder) AddEdge(v0, v1 s2.Point) {
	if v0 == v1 && b.layerOptions[len(b.layerOptions)-1].DegenerateEdges == DegenerateDiscard {
		return
	}
	j0 := b.addVertex(v0)
	j1 := b.addVertex(v1)
	b.inputEdges = append(b.inputEdges, [2]int32{j0, j1})
	if b.labelSetModified {
		if len(b.labelSetIDs) == 0 {
			for i := 0; i < len(b.inputEdges)-1; i++ {
				b.labelSetIDs = append(b.labelSetIDs, b.labelSetID)
			}
		}
		b.labelSetID = b.labelSetLex.add(slices.Clone(b.labelSet))
		b.labelSetIDs = append(b.labelSetIDs, b.labelSetID)
		b.labelSetModified = false
	} else if len(b.labelSetIDs) > 0 {
		b.labelSetIDs = append(b.labelSetIDs, b.labelSetID)
	}
}

// AddPoint adds a degenerate edge.
func (b *Builder) AddPoint(v s2.Point) { b.AddEdge(v, v) }

// AddPolyline adds the edges of a polyline.
func (b *Builder) AddPolyline(pl []s2.Point) {
	for i := 1; i < len(pl); i++ {
		b.AddEdge(pl[i-1], pl[i])
	}
}

// AddLoopPoints adds a closed loop given by its vertices.
func (b *Builder) AddLoopPoints(loop []s2.Point) {
	for i := range loop {
		b.AddEdge(loop[i], loop[(i+1)%len(loop)])
	}
}

// AddLoop adds an S2Loop (oriented so its interior is on the left).
func (b *Builder) AddLoop(l *s2.Loop) {
	if l.IsEmpty() || l.IsFull() {
		return
	}
	n := l.NumVertices()
	for i := 0; i < n; i++ {
		b.AddEdge(l.OrientedVertex(i), l.OrientedVertex(i+1))
	}
}

// AddPolygon adds all loops of a polygon.
func (b *Builder) AddPolygon(p *s2.Polygon) {
	for i := 0; i < p.NumLoops(); i++ {
		b.AddLoop(p.Loop(i))
	}
}

// AddShape adds all edges of a shape.
func (b *Builder) AddShape(sh s2.Shape) {
	for e, n := 0, sh.NumEdges(); e < n; e++ {
		ed := sh.Edge(e)
		b.AddEdge(ed.V0, ed.V1)
	}
}

// AddIsFullPolygonPredicate sets the current layer's predicate.
func (b *Builder) AddIsFullPolygonPredicate(p IsFullPolygonPredicate) {
	b.layerIsFullPred[len(b.layerIsFullPred)-1] = p
}

// ForceVertex adds a site that is kept in the output.
func (b *Builder) ForceVertex(v s2.Point) { b.sites = append(b.sites, v) }

// NumInputEdges returns the number of edges added so far.
func (b *Builder) NumInputEdges() int { return len(b.inputEdges) }

// InputEdge returns input edge i.
func (b *Builder) InputEdge(i int) s2.Edge {
	e := b.inputEdges[i]
	return s2.Edge{V0: b.inputVertices[e[0]], V1: b.inputVertices[e[1]]}
}

// Build snaps the input and builds every layer.
func (b *Builder) Build() error {
	b.err = nil
	b.layerBegins = append(b.layerBegins, int32(len(b.inputEdges)))
	if b.snappingRequested && !b.opts.Idempotent {
		b.snappingNeeded = true
	}
	b.chooseSites()
	b.buildLayers()
	b.Reset()
	return b.err
}

// Reset clears all input and layers.
func (b *Builder) Reset() {
	b.inputVertices = nil
	b.inputEdges = nil
	b.layers = nil
	b.layerOptions = nil
	b.layerBegins = nil
	b.layerIsFullPred = nil
	b.labelSetIDs = nil
	b.labelSetLex.clear()
	b.labelSet = nil
	b.labelSetModified = false
	b.sites = nil
	b.edgeSites = nil
	b.snappingNeeded = false
}

func (b *Builder) setError(err error) {
	if b.err == nil {
		b.err = err
	}
}

func (b *Builder) inputEdgeIndex() *s2.ShapeIndex {
	ev := make([]s2.Edge, len(b.inputEdges))
	for i, e := range b.inputEdges {
		ev[i] = s2.Edge{V0: b.inputVertices[e[0]], V1: b.inputVertices[e[1]]}
	}
	idx := s2.NewShapeIndex()
	idx.Add(newEdgeVectorShape(ev))
	return idx
}

func (b *Builder) chooseSites() {
	if len(b.inputVertices) == 0 {
		return
	}
	var idx *s2.ShapeIndex
	if b.opts.SplitCrossingEdges {
		idx = b.inputEdgeIndex()
		b.addEdgeCrossings(idx)
	}
	if b.snappingRequested {
		grid := newSiteGrid(&b.sites, b.minSiteSeparationCA)
		b.addForcedSites(grid)
		b.chooseInitialSites(grid)
		b.collectSiteEdges()
	}
	if b.snappingNeeded {
		if idx == nil {
			idx = b.inputEdgeIndex()
		}
		b.addExtraSites(idx)
	} else {
		b.chooseAllVerticesAsSites()
	}
}

func (b *Builder) chooseAllVerticesAsSites() {
	b.sites = b.sites[:0]
	sorted := b.sortInputVertices()
	vmap := make([]int32, len(b.inputVertices))
	for in := 0; in < len(sorted); {
		site := b.inputVertices[sorted[in].id]
		vmap[sorted[in].id] = int32(len(b.sites))
		for in++; in < len(sorted) && b.inputVertices[sorted[in].id] == site; in++ {
			vmap[sorted[in].id] = int32(len(b.sites))
		}
		b.sites = append(b.sites, site)
	}
	b.inputVertices = slices.Clone(b.sites)
	for i := range b.inputEdges {
		b.inputEdges[i][0] = vmap[b.inputEdges[i][0]]
		b.inputEdges[i][1] = vmap[b.inputEdges[i][1]]
	}
}

func (b *Builder) sortInputVertices() []inputVertexKey {
	keys := make([]inputVertexKey, len(b.inputVertices))
	for i := range keys {
		keys[i] = inputVertexKey{cellIDOf(b.inputVertices[i]), int32(i)}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, c := keys[i], keys[j]
		if a.cell != c.cell {
			return a.cell < c.cell
		}
		pa, pc := b.inputVertices[a.id], b.inputVertices[c.id]
		if cmp := pa.Cmp(pc.Vector); cmp != 0 {
			return cmp < 0
		}
		return a.id < c.id
	})
	return keys
}

// addEdgeCrossings adds a vertex at every interior crossing of input edges.
func (b *Builder) addEdgeCrossings(idx *s2.ShapeIndex) {
	var newVertices []s2.Point
	visitInteriorCrossings(idx, func(a, c s2.Edge) {
		newVertices = append(newVertices, s2.Intersection(a.V0, a.V1, c.V0, c.V1))
	})
	if len(newVertices) == 0 {
		return
	}
	b.snappingNeeded = true
	b.inputVertices = append(b.inputVertices, newVertices...)
}

func (b *Builder) addForcedSites(grid *siteGrid) {
	slices.SortFunc(b.sites, func(x, y s2.Point) int { return x.Cmp(y.Vector) })
	b.sites = slices.Compact(b.sites)
	for id := range b.sites {
		grid.add(b.sites[id], int32(id))
	}
	b.numForcedSites = int32(len(b.sites))
}

func (b *Builder) isForced(v int32) bool { return v < b.numForcedSites }

func (b *Builder) chooseInitialSites(grid *siteGrid) {
	var results []int32
	for _, key := range b.sortInputVertices() {
		vertex := b.inputVertices[key.id]
		site := b.snapSite(vertex)
		b.snappingNeeded = b.snappingNeeded || site != vertex
		addSite := true
		if b.siteSnapRadiusCA == 0 {
			addSite = len(b.sites) == 0 || site != b.sites[len(b.sites)-1]
		} else {
			results = grid.near(site, results)
			for _, id := range results {
				p := b.sites[id]
				if compareDistance(site, p, b.minSiteSeparationCA) <= 0 {
					addSite = false
					b.snappingNeeded = b.snappingNeeded || site != p
				}
			}
		}
		if addSite {
			grid.add(site, int32(len(b.sites)))
			b.sites = append(b.sites, site)
		}
	}
}

func (b *Builder) snapSite(p s2.Point) s2.Point {
	if !b.snappingRequested {
		return p
	}
	site := b.opts.SnapFunction.SnapPoint(p)
	if chordBetween(site, p) > b.siteSnapRadiusCA {
		b.setError(ErrSnapRadiusTooSmall)
	}
	return site
}

func (b *Builder) collectSiteEdges() {
	tree := newSiteTree(b.sites)
	b.edgeSites = make([][]int32, len(b.inputEdges))
	var results []int32
	for e, edge := range b.inputEdges {
		v0, v1 := b.inputVertices[edge[0]], b.inputVertices[edge[1]]
		results = tree.nearEdge(v0, v1, b.edgeSiteQueryRadiusCA, results)
		sites := make([]int32, 0, len(results))
		for _, id := range results {
			sites = append(sites, id)
			p := b.sites[id]
			if !b.snappingNeeded {
				d, _ := s2.UpdateMinDistance(p, v0, v1, s1.InfChordAngle())
				if d < b.minEdgeSiteSeparationLimit && p != v0 && p != v1 &&
					compareEdgeDistance(p, v0, v1, b.minEdgeSiteSeparationCA) < 0 {
					b.snappingNeeded = true
				}
			}
		}
		b.sortSitesByDistance(v0, sites)
		b.edgeSites[e] = sites
	}
}

func (b *Builder) sortSitesByDistance(x s2.Point, sites []int32) {
	sort.Slice(sites, func(i, j int) bool {
		a, c := sites[i], sites[j]
		if a == c {
			return false
		}
		d := compareDistances(x, b.sites[a], b.sites[c])
		if d != 0 {
			return d < 0
		}
		return a < c
	})
}

func (b *Builder) insertSiteByDistance(newID int32, x s2.Point, sites []int32) []int32 {
	pos := sort.Search(len(sites), func(i int) bool {
		return !(compareDistances(x, b.sites[sites[i]], b.sites[newID]) < 0)
	})
	return slices.Insert(sites, pos, newID)
}

func (b *Builder) addExtraSites(idx *s2.ShapeIndex) {
	resnap := map[int32]bool{}
	var chain []int32
	check := func(e int32) {
		chain = b.snapEdge(e, chain)
		delete(resnap, e)
		b.maybeAddExtraSites(e, chain, idx, resnap)
	}
	for e := range b.inputEdges {
		check(int32(e))
	}
	for len(resnap) > 0 {
		toSnap := make([]int32, 0, len(resnap))
		for e := range resnap {
			toSnap = append(toSnap, e)
		}
		slices.Sort(toSnap)
		clear(resnap)
		for _, e := range toSnap {
			check(e)
		}
	}
}

func (b *Builder) maybeAddExtraSites(edgeID int32, chain []int32, idx *s2.ShapeIndex, resnap map[int32]bool) {
	if len(chain) == 0 {
		return
	}
	edge := b.inputEdges[edgeID]
	a0, a1 := b.inputVertices[edge[0]], b.inputVertices[edge[1]]
	nearby := b.edgeSites[edgeID]
	for i, j := 0, 0; j < len(nearby); j++ {
		id := nearby[j]
		if id == chain[i] {
			i++
			if i == len(chain) {
				break
			}
			v0, v1 := b.sites[chain[i-1]], b.sites[chain[i]]
			if chordBetween(v0, v1) < b.minEdgeLengthToSplitCA {
				continue
			}
			if !isEdgeBNearEdgeA(a0, a1, v0, v1, b.maxEdgeDeviation) {
				mid := unit(projectAB(v0, a0, a1).Add(projectAB(v1, a0, a1).Vector))
				newSite := b.getSeparationSite(mid, v0, v1, edgeID)
				b.addExtraSite(newSite, idx, resnap)
				return
			}
		} else {
			if i == 0 {
				continue
			}
			siteToAvoid := b.sites[id]
			v0, v1 := b.sites[chain[i-1]], b.sites[chain[i]]
			addSep := false
			if !b.isForced(id) && b.minEdgeSiteSeparationCA > 0 &&
				compareEdgeDistance(siteToAvoid, v0, v1, b.minEdgeSiteSeparationCA) < 0 {
				addSep = true
			}
			if !addSep && (b.isForced(id) || b.checkAllSiteCrossings) &&
				sign(a0, a1, siteToAvoid) != sign(v0, v1, siteToAvoid) &&
				compareEdgeDirections(a0, a1, a0, siteToAvoid) > 0 &&
				compareEdgeDirections(a0, a1, siteToAvoid, a1) > 0 &&
				compareEdgeDirections(a0, a1, v0, siteToAvoid) > 0 &&
				compareEdgeDirections(a0, a1, siteToAvoid, v1) > 0 {
				addSep = true
			}
			if addSep {
				newSite := b.getSeparationSite(siteToAvoid, v0, v1, edgeID)
				b.addExtraSite(newSite, idx, resnap)
				// The site list of this edge may have changed; re-read it.
				nearby = b.edgeSites[edgeID]
				for ; nearby[j+1] != chain[i]; j++ {
				}
			}
		}
	}
}

func (b *Builder) addExtraSite(newSite s2.Point, idx *s2.ShapeIndex, resnap map[int32]bool) {
	newID := int32(len(b.sites))
	b.sites = append(b.sites, newSite)
	limit := conservativeLimit(b.edgeSiteQueryRadiusCA)
	q := s2.NewClosestEdgeQuery(idx, s2.NewClosestEdgeQueryOptions().DistanceLimit(limit).IncludeInteriors(false))
	for _, r := range q.FindEdges(s2.NewMinDistanceToPointTarget(newSite)) {
		e := int32(r.EdgeID())
		v0 := b.inputVertices[b.inputEdges[e][0]]
		b.edgeSites[e] = b.insertSiteByDistance(newID, v0, b.edgeSites[e])
		resnap[e] = true
	}
}

func (b *Builder) getSeparationSite(siteToAvoid, v0, v1 s2.Point, inputEdgeID int32) s2.Point {
	edge := b.inputEdges[inputEdgeID]
	x, y := b.inputVertices[edge[0]], b.inputVertices[edge[1]]
	xyDir := y.Sub(x.Vector)
	n := robustCrossProd(x, y)
	newSite := project(siteToAvoid, x, y, n)
	gapMin := b.getCoverageEndpoint(v0, n)
	gapMax := b.getCoverageEndpoint(v1, n.Mul(-1))
	if newSite.Sub(gapMin.Vector).Dot(xyDir) < 0 {
		newSite = gapMin
	} else if gapMax.Sub(newSite.Vector).Dot(xyDir) < 0 {
		newSite = gapMax
	}
	return b.snapSite(newSite)
}

func (b *Builder) getCoverageEndpoint(p s2.Point, n r3.Vector) s2.Point {
	n2 := n.Norm2()
	nDp := n.Dot(p.Vector)
	nXp := n.Cross(p.Vector)
	nXpXn := p.Mul(n2).Sub(n.Mul(nDp))
	om := nXpXn.Mul(math.Sqrt(1 - b.edgeSnapRadiusSin2))
	mr2 := b.edgeSnapRadiusSin2*n2 - nDp*nDp
	mr := nXp.Mul(math.Sqrt(math.Max(0, mr2)))
	return unit(om.Add(mr))
}

// snapEdge computes the chain of sites an input edge snaps to.
func (b *Builder) snapEdge(e int32, chain []int32) []int32 {
	chain = chain[:0]
	edge := b.inputEdges[e]
	if !b.snappingNeeded {
		return append(chain, edge[0], edge[1])
	}
	x, y := b.inputVertices[edge[0]], b.inputVertices[edge[1]]
	for _, siteID := range b.edgeSites[e] {
		c := b.sites[siteID]
		if compareEdgeDistance(c, x, y, b.edgeSnapRadiusCA) > 0 {
			continue
		}
		addC := true
		for len(chain) > 0 {
			bp := b.sites[chain[len(chain)-1]]
			if chordBetween(bp, c) >= b.maxAdjacentSiteSepCA {
				break
			}
			res := getVoronoiSiteExclusion(bp, c, x, y, b.edgeSnapRadiusCA)
			if res == excludedFirst {
				chain = chain[:len(chain)-1]
				continue
			}
			if res == excludedSecond {
				addC = false
				break
			}
			if len(chain) < 2 {
				break
			}
			a := b.sites[chain[len(chain)-2]]
			if chordBetween(a, c) >= b.maxAdjacentSiteSepCA {
				break
			}
			xyb := sign(x, y, bp)
			if sign(a, bp, c) == xyb {
				break
			}
			if edgeCircumcenterSign(x, y, a, bp, c) != xyb {
				break
			}
			chain = chain[:len(chain)-1]
		}
		if addC {
			chain = append(chain, siteID)
		}
	}
	return chain
}

func (b *Builder) buildLayers() {
	inputLex := newIDSetLexicon()
	layerEdges, layerInputIDs := b.buildLayerEdges(inputLex)
	var layerVertices [][]s2.Point
	const minLayersForVertexFiltering = 10
	if len(b.layers) >= minLayersForVertexFiltering {
		allow := true
		for _, o := range b.layerOptions {
			allow = allow && o.AllowVertexFiltering
		}
		if allow {
			layerVertices = make([][]s2.Point, len(b.layers))
			for i := range b.layers {
				layerVertices[i] = FilterVertices(b.sites, layerEdges[i])
			}
		}
	}
	for i, l := range b.layers {
		verts := b.sites
		if layerVertices != nil {
			verts = layerVertices[i]
		}
		g := newGraph(b.layerOptions[i], verts, layerEdges[i], layerInputIDs[i], inputLex,
			b.labelSetIDs, b.labelSetLex, b.layerIsFullPred[i])
		if err := l.Build(g); err != nil {
			b.setError(err)
		}
	}
}

func (b *Builder) buildLayerEdges(inputLex *idSetLexicon) ([][]Edge, [][]InputEdgeIDSetID) {
	var siteVertices [][]int32
	simplify := b.snappingNeeded && b.opts.SimplifyEdgeChains
	if simplify {
		siteVertices = make([][]int32, len(b.sites))
	}
	layerEdges := make([][]Edge, len(b.layers))
	layerInputIDs := make([][]InputEdgeIDSetID, len(b.layers))
	for i := range b.layers {
		layerEdges[i], layerInputIDs[i] = b.addSnappedEdges(b.layerBegins[i], b.layerBegins[i+1],
			b.layerOptions[i], inputLex, siteVertices)
	}
	if simplify {
		b.simplifyEdgeChains(siteVertices, layerEdges, layerInputIDs, inputLex)
	}
	b.edgeSites = nil
	for i := range b.layers {
		if err := ProcessEdges(&b.layerOptions[i], &layerEdges[i], &layerInputIDs[i], inputLex); err != nil {
			b.setError(err)
		}
	}
	return layerEdges, layerInputIDs
}

func (b *Builder) addSnappedEdges(begin, end int32, opts GraphOptions, inputLex *idSetLexicon,
	siteVertices [][]int32) ([]Edge, []InputEdgeIDSetID) {
	discardDegenerate := opts.DegenerateEdges == DegenerateDiscard
	var edges []Edge
	var ids []InputEdgeIDSetID
	var chain []int32
	for e := begin; e < end; e++ {
		id := inputLex.addSingleton(e)
		chain = b.snapEdge(e, chain)
		if len(chain) == 0 {
			continue
		}
		b.maybeAddInputVertex(b.inputEdges[e][0], chain[0], siteVertices)
		if len(chain) == 1 {
			if discardDegenerate {
				continue
			}
			edges, ids = addSnappedEdge(chain[0], chain[0], id, opts.EdgeType, edges, ids)
		} else {
			b.maybeAddInputVertex(b.inputEdges[e][1], chain[len(chain)-1], siteVertices)
			for i := 1; i < len(chain); i++ {
				edges, ids = addSnappedEdge(chain[i-1], chain[i], id, opts.EdgeType, edges, ids)
			}
		}
	}
	return edges, ids
}

func (b *Builder) maybeAddInputVertex(v, id int32, siteVertices [][]int32) {
	if siteVertices == nil {
		return
	}
	vs := siteVertices[id]
	if len(vs) == 0 || vs[len(vs)-1] != v {
		siteVertices[id] = append(vs, v)
	}
}

func addSnappedEdge(src, dst int32, id InputEdgeIDSetID, et EdgeType, edges []Edge, ids []InputEdgeIDSetID) ([]Edge, []InputEdgeIDSetID) {
	edges = append(edges, Edge{src, dst})
	ids = append(ids, id)
	if et == EdgeUndirected {
		edges = append(edges, Edge{dst, src})
		ids = append(ids, emptySetID)
	}
	return edges, ids
}
