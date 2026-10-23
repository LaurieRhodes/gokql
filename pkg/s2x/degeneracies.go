package s2x

// Port of s2builderutil_find_polygon_degeneracies and the
// LaxPolygonLayer (s2builderutil_lax_polygon_layer).

import (
	"errors"
	"sort"

	"github.com/golang/geo/s2"
)

// PolygonDegeneracy is a degenerate edge (point or sibling pair) and
// whether it is a hole.
type PolygonDegeneracy struct {
	EdgeID EdgeID
	IsHole bool
}

type degenComponent struct {
	root         VertexID
	rootSign     int
	degeneracies []PolygonDegeneracy
}

type degeneracyFinder struct {
	g                  *Graph
	in                 *VertexInMap
	out                *VertexOutMap
	isVertexUsed       []bool
	isEdgeDegeneracy   []bool
	isVertexUnbalanced []bool
}

// FindPolygonDegeneracies is s2builderutil::FindPolygonDegeneracies.
func FindPolygonDegeneracies(g *Graph) ([]PolygonDegeneracy, error) {
	if g.opts.DegenerateEdges == DegenerateDiscard && g.opts.SiblingPairs == SiblingDiscard {
		return nil, nil
	}
	f := &degeneracyFinder{g: g, in: NewVertexInMap(g), out: NewVertexOutMap(g)}
	return f.run()
}

// IsFullyDegenerate reports whether every edge is degenerate or has a
// sibling.
func IsFullyDegenerate(g *Graph) bool {
	for _, e := range g.edges {
		if e.V0 == e.V1 {
			continue
		}
		r := e.reverse()
		i := sort.Search(len(g.edges), func(k int) bool { return !edgeLess(g.edges[k], r) })
		if i == len(g.edges) || g.edges[i] != r {
			return false
		}
	}
	return true
}

func (f *degeneracyFinder) run() ([]PolygonDegeneracy, error) {
	n := f.computeDegeneracies()
	if n == 0 {
		return nil, nil
	}
	if n == f.g.NumEdges() {
		isHole, err := f.g.IsFullPolygon()
		res := make([]PolygonDegeneracy, n)
		for e := range res {
			res[e] = PolygonDegeneracy{EdgeID(e), isHole}
		}
		return res, err
	}
	var comps []degenComponent
	knownVertex := VertexID(-1)
	knownSign := 0
	numUnknown := 0
	f.isVertexUsed = make([]bool, f.g.NumVertices())
	for e := 0; e < f.g.NumEdges(); e++ {
		if f.isEdgeDegeneracy[e] {
			root := f.g.edges[e].V0
			if f.isVertexUsed[root] {
				continue
			}
			c := f.buildComponent(root)
			if c.rootSign == 0 {
				numUnknown++
			} else {
				knownVertex = root
				knownSign = c.rootSign
			}
			comps = append(comps, c)
		}
	}
	if numUnknown > 0 {
		if knownSign == 0 {
			knownVertex = f.findUnbalancedVertex()
			knownSign = f.containsVertexSign(knownVertex)
		}
		for i := range comps {
			c := &comps[i]
			if c.rootSign != 0 {
				continue
			}
			inside := knownSign > 0
			crosser := s2.NewEdgeCrosser(f.g.vertices[knownVertex], f.g.vertices[c.root])
			for e, ed := range f.g.edges {
				if f.isEdgeDegeneracy[e] {
					continue
				}
				if crosser.EdgeOrVertexCrossing(f.g.vertices[ed.V0], f.g.vertices[ed.V1]) {
					inside = !inside
				}
			}
			if inside {
				c.rootSign = 1
			} else {
				c.rootSign = -1
			}
		}
	}
	var res []PolygonDegeneracy
	for _, c := range comps {
		invert := c.rootSign < 0
		for _, d := range c.degeneracies {
			res = append(res, PolygonDegeneracy{d.EdgeID, d.IsHole != invert})
		}
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].EdgeID != res[j].EdgeID {
			return res[i].EdgeID < res[j].EdgeID
		}
		return !res[i].IsHole && res[j].IsHole
	})
	return res, nil
}

func (f *degeneracyFinder) computeDegeneracies() int {
	n := f.g.NumEdges()
	f.isEdgeDegeneracy = make([]bool, n)
	f.isVertexUnbalanced = make([]bool, f.g.NumVertices())
	count := 0
	inIDs := f.in.InEdgeIDs()
	for in, out := 0, 0; out < n; out++ {
		oe := f.g.edges[out]
		if oe.V0 == oe.V1 {
			f.isEdgeDegeneracy[out] = true
			count++
			continue
		}
		for in < n && edgeLess(f.g.edges[inIDs[in]].reverse(), oe) {
			in++
		}
		if in < n && f.g.edges[inIDs[in]].reverse() == oe {
			f.isEdgeDegeneracy[out] = true
			count++
		} else {
			f.isVertexUnbalanced[oe.V0] = true
		}
	}
	return count
}

func (f *degeneracyFinder) buildComponent(root VertexID) degenComponent {
	res := degenComponent{root: root}
	type fv struct {
		v    VertexID
		same bool
	}
	frontier := []fv{{root, true}}
	f.isVertexUsed[root] = true
	for len(frontier) > 0 {
		v0, v0Same := frontier[len(frontier)-1].v, frontier[len(frontier)-1].same
		frontier = frontier[:len(frontier)-1]
		if res.rootSign == 0 && f.isVertexUnbalanced[v0] {
			s := f.containsVertexSign(v0)
			if v0Same {
				res.rootSign = s
			} else {
				res.rootSign = -s
			}
		}
		lo, hi := f.out.EdgeIDs(v0)
		for e := lo; e < hi; e++ {
			v1 := f.g.edges[e].V1
			same := v0Same != f.crossingParity(v0, v1, false)
			if f.isEdgeDegeneracy[e] {
				res.degeneracies = append(res.degeneracies, PolygonDegeneracy{e, same})
			}
			if f.isVertexUsed[v1] {
				continue
			}
			same = same != f.crossingParity(v1, v0, true)
			frontier = append(frontier, fv{v1, same})
			f.isVertexUsed[v1] = true
		}
	}
	return res
}

func (f *degeneracyFinder) crossingParity(v0, v1 VertexID, includeSame bool) bool {
	crossings := 0
	p0, p1 := f.g.vertices[v0], f.g.vertices[v1]
	ref := s2.Ortho(p0)
	lo, hi := f.out.EdgeIDs(v0)
	for e := lo; e < hi; e++ {
		ed := f.g.edges[e]
		if ed.V1 == v1 {
			if includeSame {
				crossings++
			}
		} else if s2.OrderedCCW(ref, f.g.vertices[ed.V1], p1, p0) {
			crossings++
		}
	}
	for _, e := range f.in.EdgeIDs(v0) {
		ed := f.g.edges[e]
		if ed.V0 == v1 {
			if includeSame {
				crossings++
			}
		} else if s2.OrderedCCW(ref, f.g.vertices[ed.V0], p1, p0) {
			crossings++
		}
	}
	return crossings&1 != 0
}

func (f *degeneracyFinder) findUnbalancedVertex() VertexID {
	for v, u := range f.isVertexUnbalanced {
		if u {
			return VertexID(v)
		}
	}
	return -1
}

func (f *degeneracyFinder) containsVertexSign(v0 VertexID) int {
	q := s2.NewContainsVertexQuery(f.g.vertices[v0])
	lo, hi := f.out.EdgeIDs(v0)
	for e := lo; e < hi; e++ {
		q.AddEdge(f.g.vertices[f.g.edges[e].V1], 1)
	}
	for _, e := range f.in.EdgeIDs(v0) {
		q.AddEdge(f.g.vertices[f.g.edges[e].V0], -1)
	}
	return q.ContainsVertex()
}

// LaxDegenerateBoundaries selects how LaxPolygonLayer treats degeneracies.
type LaxDegenerateBoundaries uint8

const (
	LaxDiscard LaxDegenerateBoundaries = iota
	LaxDiscardHoles
	LaxDiscardShells
	LaxKeep
)

// ErrUndirectedUnsupported is returned by LaxPolygonLayer for undirected edges.
var ErrUndirectedUnsupported = errors.New("s2builder: undirected edges not supported yet")

// LaxPolygonLayer assembles edges into a LaxPolygon.
type LaxPolygonLayer struct {
	EdgeType             EdgeType
	DegenerateBoundaries LaxDegenerateBoundaries
	Polygon              *LaxPolygon
}

// NewLaxPolygonLayer returns the C++ defaults (directed, keep degeneracies).
func NewLaxPolygonLayer() *LaxPolygonLayer {
	return &LaxPolygonLayer{EdgeType: EdgeDirected, DegenerateBoundaries: LaxKeep}
}

func (l *LaxPolygonLayer) GraphOptions() GraphOptions {
	if l.DegenerateBoundaries == LaxDiscard {
		return NewGraphOptions(l.EdgeType, DegenerateDiscard, DuplicateKeep, SiblingDiscard)
	}
	return NewGraphOptions(l.EdgeType, DegenerateDiscardExcess, DuplicateKeep, SiblingDiscardExcess)
}

func (l *LaxPolygonLayer) Build(g *Graph) error {
	if g.opts.EdgeType != EdgeDirected {
		return ErrUndirectedUnsupported
	}
	var loops [][]s2.Point
	var firstErr error
	maybeFull := func() {
		full, err := g.IsFullPolygon()
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if full {
			loops = append(loops, []s2.Point{})
		}
	}
	switch l.DegenerateBoundaries {
	case LaxDiscard:
		if g.NumEdges() == 0 {
			maybeFull()
		}
	case LaxKeep:
		if IsFullyDegenerate(g) {
			maybeFull()
		}
	default:
		discardHoles := l.DegenerateBoundaries == LaxDiscardHoles
		degs, err := FindPolygonDegeneracies(g)
		if err != nil {
			return err
		}
		if len(degs) == g.NumEdges() {
			if len(degs) == 0 {
				maybeFull()
			} else if degs[0].IsHole {
				loops = append(loops, []s2.Point{})
			}
		}
		var discard []EdgeID
		for _, d := range degs {
			if d.IsHole == discardHoles {
				discard = append(discard, d.EdgeID)
			}
		}
		if len(discard) > 0 {
			sort.Slice(discard, func(i, j int) bool { return discard[i] < discard[j] })
			var ne []Edge
			var ni []InputEdgeIDSetID
			k := 0
			for e := 0; e < g.NumEdges(); e++ {
				if k < len(discard) && EdgeID(e) == discard[k] {
					k++
					continue
				}
				ne = append(ne, g.edges[e])
				ni = append(ni, g.inputEdgeIDSetIDs[e])
			}
			g = newGraph(g.opts, g.vertices, ne, ni, g.inputEdgeIDSetLex, g.labelSetIDs, g.labelSetLex, g.isFullPolygonPred)
		}
	}
	edgeLoops, err := g.GetDirectedLoops(LoopCircuit)
	if err != nil {
		return err
	}
	for _, el := range edgeLoops {
		vs := make([]s2.Point, len(el))
		for i, e := range el {
			vs[i] = g.vertices[g.edges[e].V0]
		}
		loops = append(loops, vs)
	}
	l.Polygon = NewLaxPolygon(loops)
	return firstErr
}
