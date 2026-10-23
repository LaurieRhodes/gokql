package s2x

// GeometryLayers collects the three output dimensions of a boolean
// operation as simple geometries: points (including degenerate edges of
// the higher dimensions), polylines (including degenerate polygon sibling
// pairs) and one S2Polygon.

import (
	"github.com/golang/geo/s2"
)

// GeometryLayers holds the assembled output.
type GeometryLayers struct {
	Points    []s2.Point
	Polylines []s2.Polyline
	Polygon   *s2.Polygon
	// line edges found as polygon degeneracies, assembled with the
	// polylines of dimension 1.
	extraLines [][]s2.Point
}

// Layers returns the dimension 0, 1 and 2 layers for a BooleanOperation.
func (gl *GeometryLayers) Layers() []Layer {
	return []Layer{&geoPointLayer{gl}, &geoLineLayer{gl}, &geoPolygonLayer{gl}}
}

// subgraph keeps the edges for which keep returns true and reprocesses
// them under opts.
func (g *Graph) subgraph(keep func(e EdgeID) bool, opts GraphOptions) (*Graph, error) {
	var ne []Edge
	var ni []InputEdgeIDSetID
	for e := range g.edges {
		if keep(EdgeID(e)) {
			ne = append(ne, g.edges[e])
			ni = append(ni, g.inputEdgeIDSetIDs[e])
		}
	}
	return g.MakeSubgraph(opts, &ne, &ni, g.inputEdgeIDSetLex, g.isFullPolygonPred)
}

type geoPointLayer struct{ gl *GeometryLayers }

func (l *geoPointLayer) GraphOptions() GraphOptions {
	return NewGraphOptions(EdgeDirected, DegenerateKeep, DuplicateMerge, SiblingKeep)
}

func (l *geoPointLayer) Build(g *Graph) error {
	for _, e := range g.edges {
		l.gl.Points = append(l.gl.Points, g.vertices[e.V0])
	}
	return nil
}

type geoLineLayer struct{ gl *GeometryLayers }

func (l *geoLineLayer) GraphOptions() GraphOptions {
	return NewGraphOptions(EdgeDirected, DegenerateDiscardExcess, DuplicateMerge, SiblingKeep)
}

func (l *geoLineLayer) Build(g *Graph) error {
	for _, e := range g.edges {
		if e.V0 == e.V1 {
			l.gl.Points = append(l.gl.Points, g.vertices[e.V0])
		}
	}
	sub, err := g.subgraph(func(e EdgeID) bool { return g.edges[e].V0 != g.edges[e].V1 },
		NewGraphOptions(EdgeDirected, DegenerateDiscard, DuplicateMerge, SiblingKeep))
	if err != nil {
		return err
	}
	for _, pl := range sub.GetPolylines(PolylineWalk) {
		l.gl.Polylines = append(l.gl.Polylines, edgePolylineVertices(sub, pl))
	}
	return nil
}

type geoPolygonLayer struct{ gl *GeometryLayers }

func (l *geoPolygonLayer) GraphOptions() GraphOptions {
	return NewGraphOptions(EdgeDirected, DegenerateDiscardExcess, DuplicateKeep, SiblingDiscardExcess)
}

func (l *geoPolygonLayer) Build(g *Graph) error {
	degs, err := FindPolygonDegeneracies(g)
	if err != nil {
		return err
	}
	isDegen := make([]bool, g.NumEdges())
	isLine := make([]bool, g.NumEdges())
	edgeID := map[Edge]EdgeID{}
	for e, ed := range g.edges {
		edgeID[ed] = EdgeID(e)
	}
	minIDs := g.GetMinInputEdgeIDs()
	anyLine := false
	for _, d := range degs {
		isDegen[d.EdgeID] = true
		if d.IsHole {
			continue
		}
		e := g.edges[d.EdgeID]
		if e.V0 == e.V1 {
			l.gl.Points = append(l.gl.Points, g.vertices[e.V0])
			continue
		}
		// Of each sibling pair keep the edge that came first in the input.
		r, ok := edgeID[e.reverse()]
		if !ok || minIDs[d.EdgeID] < minIDs[r] || (minIDs[d.EdgeID] == minIDs[r] && d.EdgeID < r) {
			isLine[d.EdgeID] = true
			anyLine = true
		}
	}
	if anyLine {
		sub, err := g.subgraph(func(e EdgeID) bool { return isLine[e] },
			NewGraphOptions(EdgeDirected, DegenerateDiscard, DuplicateMerge, SiblingKeep))
		if err != nil {
			return err
		}
		for _, pl := range sub.GetPolylines(PolylineWalk) {
			l.gl.Polylines = append(l.gl.Polylines, edgePolylineVertices(sub, pl))
		}
	}
	if len(degs) == g.NumEdges() {
		if len(degs) == 0 {
			if full, err := g.IsFullPolygon(); err == nil && full {
				l.gl.Polygon = s2.FullPolygon()
			}
		} else if degs[0].IsHole {
			l.gl.Polygon = s2.FullPolygon()
		}
		return nil
	}
	sub, err := g.subgraph(func(e EdgeID) bool { return !isDegen[e] },
		NewGraphOptions(EdgeDirected, DegenerateDiscard, DuplicateKeep, SiblingDiscard))
	if err != nil {
		return err
	}
	edgeLoops, err := sub.GetDirectedLoops(LoopSimple)
	if err != nil {
		return err
	}
	l.gl.Polygon = s2.PolygonFromOrientedLoops(loopsFromEdgeLoops(sub, edgeLoops, nil))
	return nil
}
