package s2x

// Output layers (s2builderutil_s2polygon_layer, _s2polyline_layer,
// _s2polyline_vector_layer, _s2point_vector_layer). Edge labels are not
// supported by these layers.

import (
	"errors"

	"github.com/golang/geo/s2"
)

// ErrEdgesDoNotFormPolyline is BUILDER_EDGES_DO_NOT_FORM_POLYLINE.
var ErrEdgesDoNotFormPolyline = errors.New("s2builder: input edges cannot be assembled into polyline")

// ErrNonDegenerateEdges is reported by PointVectorLayer.
var ErrNonDegenerateEdges = errors.New("s2builder: found non-degenerate edges")

// PolygonLayer assembles edges into an S2Polygon.
type PolygonLayer struct {
	EdgeType EdgeType
	Validate bool
	Polygon  *s2.Polygon
}

// NewPolygonLayer returns a directed polygon layer.
func NewPolygonLayer() *PolygonLayer { return &PolygonLayer{EdgeType: EdgeDirected} }

func (l *PolygonLayer) GraphOptions() GraphOptions {
	return NewGraphOptions(l.EdgeType, DegenerateDiscard, DuplicateKeep, SiblingDiscard)
}

func loopsFromEdgeLoops(g *Graph, edgeLoops [][]EdgeID, loops []*s2.Loop) []*s2.Loop {
	for _, el := range edgeLoops {
		vs := make([]s2.Point, len(el))
		for i, e := range el {
			vs[i] = g.vertices[g.edges[e].V0]
		}
		loops = append(loops, s2.LoopFromPoints(vs))
	}
	return loops
}

func (l *PolygonLayer) Build(g *Graph) error {
	if g.NumEdges() == 0 {
		full, err := g.IsFullPolygon()
		if full {
			l.Polygon = s2.FullPolygon()
		} else {
			l.Polygon = s2.PolygonFromLoops(nil)
		}
		return err
	}
	if g.opts.EdgeType == EdgeDirected {
		edgeLoops, err := g.GetDirectedLoops(LoopSimple)
		if err != nil {
			return err
		}
		l.Polygon = s2.PolygonFromOrientedLoops(loopsFromEdgeLoops(g, edgeLoops, nil))
	} else {
		comps, err := g.GetUndirectedComponents(LoopSimple)
		if err != nil {
			return err
		}
		var loops []*s2.Loop
		for _, c := range comps {
			loops = loopsFromEdgeLoops(g, c[0], loops)
		}
		for _, lp := range loops {
			lp.Normalize()
		}
		l.Polygon = s2.PolygonFromLoops(loops)
	}
	if l.Validate {
		return l.Polygon.Validate()
	}
	return nil
}

// PolylineLayer assembles edges into a single polyline.
type PolylineLayer struct {
	EdgeType EdgeType
	Polyline s2.Polyline
}

func (l *PolylineLayer) GraphOptions() GraphOptions {
	return NewGraphOptions(l.EdgeType, DegenerateDiscard, DuplicateKeep, SiblingKeep)
}

func (l *PolylineLayer) Build(g *Graph) error {
	l.Polyline = nil
	if g.NumEdges() == 0 {
		return nil
	}
	pls := g.GetPolylines(PolylineWalk)
	if len(pls) != 1 {
		return ErrEdgesDoNotFormPolyline
	}
	l.Polyline = edgePolylineVertices(g, pls[0])
	return nil
}

func edgePolylineVertices(g *Graph, pl []EdgeID) s2.Polyline {
	vs := make(s2.Polyline, 0, len(pl)+1)
	vs = append(vs, g.vertices[g.edges[pl[0]].V0])
	for _, e := range pl {
		vs = append(vs, g.vertices[g.edges[e].V1])
	}
	return vs
}

// PolylineVectorLayer assembles edges into polylines.
type PolylineVectorLayer struct {
	EdgeType       EdgeType
	PolylineType   PolylineType
	DuplicateEdges DuplicateEdges
	SiblingPairs   SiblingPairs
	Polylines      []s2.Polyline
}

// NewPolylineVectorLayer returns the C++ default options (directed, PATH,
// keep duplicates and siblings).
func NewPolylineVectorLayer() *PolylineVectorLayer {
	return &PolylineVectorLayer{EdgeType: EdgeDirected, PolylineType: PolylinePath,
		DuplicateEdges: DuplicateKeep, SiblingPairs: SiblingKeep}
}

func (l *PolylineVectorLayer) GraphOptions() GraphOptions {
	return NewGraphOptions(l.EdgeType, DegenerateDiscard, l.DuplicateEdges, l.SiblingPairs)
}

func (l *PolylineVectorLayer) Build(g *Graph) error {
	for _, pl := range g.GetPolylines(l.PolylineType) {
		l.Polylines = append(l.Polylines, edgePolylineVertices(g, pl))
	}
	return nil
}

// PointVectorLayer collects degenerate edges as points.
type PointVectorLayer struct {
	DuplicateEdges DuplicateEdges
	Points         []s2.Point
}

// NewPointVectorLayer merges duplicate points (the C++ default).
func NewPointVectorLayer() *PointVectorLayer {
	return &PointVectorLayer{DuplicateEdges: DuplicateMerge}
}

func (l *PointVectorLayer) GraphOptions() GraphOptions {
	return NewGraphOptions(EdgeDirected, DegenerateKeep, l.DuplicateEdges, SiblingKeep)
}

func (l *PointVectorLayer) Build(g *Graph) error {
	var err error
	for _, e := range g.edges {
		if e.V0 != e.V1 {
			err = ErrNonDegenerateEdges
			continue
		}
		l.Points = append(l.Points, g.vertices[e.V0])
	}
	return err
}
