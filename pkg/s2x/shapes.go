package s2x

import (
	"math"
	"sort"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

// edgeVectorShape is a dimension-1 shape where every edge is its own chain
// (S2EdgeVectorShape). It embeds s2.PointVector only to inherit the
// unexported methods of the s2.Shape interface; every exported method is
// overridden.
type edgeVectorShape struct {
	s2.PointVector
	edges []s2.Edge
}

func newEdgeVectorShape(edges []s2.Edge) *edgeVectorShape { return &edgeVectorShape{edges: edges} }

func (s *edgeVectorShape) NumEdges() int                     { return len(s.edges) }
func (s *edgeVectorShape) Edge(i int) s2.Edge                { return s.edges[i] }
func (s *edgeVectorShape) ReferencePoint() s2.ReferencePoint { return s2.OriginReferencePoint(false) }
func (s *edgeVectorShape) NumChains() int                    { return len(s.edges) }
func (s *edgeVectorShape) Chain(i int) s2.Chain              { return s2.Chain{Start: i, Length: 1} }
func (s *edgeVectorShape) ChainEdge(i, j int) s2.Edge        { return s.edges[i] }
func (s *edgeVectorShape) ChainPosition(e int) s2.ChainPosition {
	return s2.ChainPosition{ChainID: e, Offset: 0}
}
func (s *edgeVectorShape) Dimension() int { return 1 }
func (s *edgeVectorShape) IsEmpty() bool  { return len(s.edges) == 0 }
func (s *edgeVectorShape) IsFull() bool   { return false }

func intervalFromPoint(p float64) s1.Interval {
	if p == -math.Pi {
		p = math.Pi
	}
	return s1.Interval{Lo: p, Hi: p}
}

// shapeArea is S2::GetArea(shape).
func shapeArea(sh s2.Shape) float64 {
	if sh.Dimension() != 2 {
		return 0
	}
	area := 0.0
	for c := 0; c < sh.NumChains(); c++ {
		ch := sh.Chain(c)
		vs := make([]s2.Point, ch.Length)
		for i := 0; i < ch.Length; i++ {
			vs[i] = sh.ChainEdge(c, i).V0
		}
		area += loopSignedArea(vs)
	}
	if area < 0 {
		area += 4 * math.Pi
	}
	return area
}

// indexArea is S2::GetArea(index).
func indexArea(idx *s2.ShapeIndex) float64 {
	area := 0.0
	for s := int32(0); s < numShapeIDs(idx); s++ {
		if sh := idx.Shape(s); sh != nil {
			area += shapeArea(sh)
		}
	}
	return area
}

// LaxPolygon is S2LaxPolygonShape: loops may be degenerate (a single
// point or sibling pairs) and an empty loop is the full loop. (golang/geo's
// LaxPolygon reports wrong chain ids for multi-loop polygons.)
type LaxPolygon struct {
	s2.PointVector
	loops [][]s2.Point
	cum   []int // cumulative vertex counts, len(loops)+1
	ref   *s2.ReferencePoint
}

// NewLaxPolygon builds a lax polygon from loops of vertices.
func NewLaxPolygon(loops [][]s2.Point) *LaxPolygon {
	p := &LaxPolygon{loops: loops, cum: make([]int, len(loops)+1)}
	for i, l := range loops {
		p.cum[i+1] = p.cum[i] + len(l)
	}
	return p
}

func (p *LaxPolygon) NumLoops() int                 { return len(p.loops) }
func (p *LaxPolygon) LoopVertices(i int) []s2.Point { return p.loops[i] }
func (p *LaxPolygon) NumEdges() int                 { return p.cum[len(p.loops)] }
func (p *LaxPolygon) loopOf(e int) int {
	i := sort.Search(len(p.loops), func(k int) bool { return p.cum[k+1] > e })
	return i
}
func (p *LaxPolygon) Edge(e int) s2.Edge {
	i := p.loopOf(e)
	return p.ChainEdge(i, e-p.cum[i])
}
func (p *LaxPolygon) ReferencePoint() s2.ReferencePoint {
	if p.ref == nil {
		r := referencePointForShape(p)
		p.ref = &r
	}
	return *p.ref
}
func (p *LaxPolygon) NumChains() int       { return len(p.loops) }
func (p *LaxPolygon) Chain(i int) s2.Chain { return s2.Chain{Start: p.cum[i], Length: len(p.loops[i])} }
func (p *LaxPolygon) ChainEdge(i, j int) s2.Edge {
	l := p.loops[i]
	k := j + 1
	if k == len(l) {
		k = 0
	}
	return s2.Edge{V0: l[j], V1: l[k]}
}
func (p *LaxPolygon) ChainPosition(e int) s2.ChainPosition {
	i := p.loopOf(e)
	return s2.ChainPosition{ChainID: i, Offset: e - p.cum[i]}
}
func (p *LaxPolygon) Dimension() int { return 2 }
func (p *LaxPolygon) IsEmpty() bool  { return p.NumEdges() == 0 && len(p.loops) == 0 }
func (p *LaxPolygon) IsFull() bool   { return p.NumEdges() == 0 && len(p.loops) > 0 }

// referencePointForShape is s2shapeutil::GetReferencePoint.
func referencePointForShape(shape s2.Shape) s2.ReferencePoint {
	if shape.NumEdges() == 0 {
		return s2.OriginReferencePoint(shape.NumChains() > 0)
	}
	if ref, ok := referencePointAtVertex(shape, shape.Edge(0).V0); ok {
		return ref
	}
	n := shape.NumEdges()
	edges := make([]s2.Edge, n)
	rev := make([]s2.Edge, n)
	for i := 0; i < n; i++ {
		e := shape.Edge(i)
		edges[i] = e
		rev[i] = s2.Edge{V0: e.V1, V1: e.V0}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].Cmp(edges[j]) < 0 })
	sort.Slice(rev, func(i, j int) bool { return rev[i].Cmp(rev[j]) < 0 })
	for i := 0; i < n; i++ {
		if edges[i].Cmp(rev[i]) < 0 {
			if ref, ok := referencePointAtVertex(shape, edges[i].V0); ok {
				return ref
			}
		}
		if rev[i].Cmp(edges[i]) < 0 {
			if ref, ok := referencePointAtVertex(shape, rev[i].V0); ok {
				return ref
			}
		}
	}
	for i := 0; i < shape.NumChains(); i++ {
		if shape.Chain(i).Length == 0 {
			return s2.OriginReferencePoint(true)
		}
	}
	return s2.OriginReferencePoint(false)
}

func referencePointAtVertex(shape s2.Shape, v s2.Point) (s2.ReferencePoint, bool) {
	q := s2.NewContainsVertexQuery(v)
	for e := 0; e < shape.NumEdges(); e++ {
		ed := shape.Edge(e)
		if ed.V0 == v {
			q.AddEdge(ed.V1, 1)
		}
		if ed.V1 == v {
			q.AddEdge(ed.V0, -1)
		}
	}
	s := q.ContainsVertex()
	if s == 0 {
		return s2.ReferencePoint{}, false
	}
	return s2.ReferencePoint{Point: v, Contained: s > 0}, true
}
