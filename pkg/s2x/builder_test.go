package s2x

import (
	"testing"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

func buildPolygon(t *testing.T, opts BuilderOptions, input *s2.Polygon) *s2.Polygon {
	t.Helper()
	b := NewBuilder(opts)
	l := NewPolygonLayer()
	b.StartLayer(l)
	b.AddPolygon(input)
	if err := b.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	return l.Polygon
}

func TestBuilderAddShape(t *testing.T) {
	in := makePolygon("0:0, 0:5, 5:5, 5:0; 1:1, 1:4, 4:4, 4:1")
	b := NewBuilder(DefaultBuilderOptions())
	l := NewPolygonLayer()
	b.StartLayer(l)
	b.AddShape(in)
	if err := b.Build(); err != nil {
		t.Fatal(err)
	}
	expectPolygonsEqual(t, in, l.Polygon)
}

func TestBuilderSimpleVertexMerging(t *testing.T) {
	r := 0.5 * s1.Degree
	out := buildPolygon(t, NewBuilderOptions(IdentitySnapFunction{r}),
		makePolygon("0:0, 0.2:0.2, 0.1:0.2, 0.1:0.9, 0:1, 0.1:1.1, 0.9:1, 1:1, 1:0.9"))
	expectPolygonsApproxEqual(t, makePolygon("0:0, 0:1, 1:0.9"), out, r)
}

func TestBuilderSimpleIntLatLngSnapping(t *testing.T) {
	out := buildPolygon(t, NewBuilderOptions(NewIntLatLngSnapFunction(0)),
		makePolygon("2.01:2.09, 3.24:4.49, 1.78:6.25, 3.51:5.49, 6.11:6.11, 5.22:3.88, 5.55:2.49, 4.49:2.51"))
	expectPolygonsEqual(t, makePolygon("2:2, 3:4, 2:6, 4:5, 6:6, 5:4, 6:2, 4:3"), out)
}

func TestBuilderVerticesMoveLessThanSnapRadius(t *testing.T) {
	in := s2.PolygonFromLoops([]*s2.Loop{s2.RegularLoop(s2.Point{Vector: s2.PointFromCoords(1, 0, 0).Vector}, 20*s1.Degree, 1000)})
	out := buildPolygon(t, NewBuilderOptions(IdentitySnapFunction{1 * s1.Degree}), in)
	if out.NumLoops() != 1 {
		t.Fatalf("loops %d", out.NumLoops())
	}
	n := out.Loop(0).NumVertices()
	if n < 90 || n > 100 {
		t.Errorf("vertices %d, want 90..100", n)
	}
}

func TestBuilderMinEdgeVertexSeparation(t *testing.T) {
	in := makePolygon("0:0, 0:1, 1:.9, 2:.8, 3:.7, 4:.6, 5:.5, 6:.4, 7:.3, 8:.2, 9:.1, 10:0")
	exp := makePolygon("0:0, 0:1, 1:.9, 2:.8, 3:.7, 4:.6, 5:.5, 4.00021862252687:0")
	out := buildPolygon(t, NewBuilderOptions(IdentitySnapFunction{0.5 * s1.Degree}), in)
	expectPolygonsApproxEqual(t, exp, out, s1.Angle(1e-15))
}

func TestBuilderSelfIntersectingPolygon(t *testing.T) {
	in := makePolyline("3:1, 1:3, 1:1, 3:3, 3:1")
	exp := makePolygon("1:1, 1:3, 2:2; 3:3, 3:1, 2:2")
	opts := NewBuilderOptions(NewIntLatLngSnapFunction(1))
	opts.SplitCrossingEdges = true
	b := NewBuilder(opts)
	l := &PolygonLayer{EdgeType: EdgeUndirected}
	b.StartLayer(l)
	b.AddPolyline(in)
	if err := b.Build(); err != nil {
		t.Fatal(err)
	}
	expectPolygonsEqual(t, exp, l.Polygon)
}

func TestBuilderSelfIntersectingPolyline(t *testing.T) {
	in := makePolyline("3:1, 1:3, 1:1, 3:3")
	exp := makePolyline("3:1, 2:2, 1:3, 1:1, 2:2, 3:3")
	opts := NewBuilderOptions(NewIntLatLngSnapFunction(1))
	opts.SplitCrossingEdges = true
	b := NewBuilder(opts)
	l := &PolylineLayer{}
	b.StartLayer(l)
	b.AddPolyline(in)
	if err := b.Build(); err != nil {
		t.Fatal(err)
	}
	if pointsStr(l.Polyline) != pointsStr(exp) {
		t.Errorf("got %s\nwant %s", pointsStr(l.Polyline), pointsStr(exp))
	}
}

func TestBuilderSimplifyOneEdge(t *testing.T) {
	opts := NewBuilderOptions(IdentitySnapFunction{1 * s1.Degree})
	opts.SetSimplifyEdgeChains(true)
	b := NewBuilder(opts)
	l := &PolylineLayer{}
	b.StartLayer(l)
	b.AddPolyline(makePolyline("0:0, 1:0.5, 2:-0.5, 3:0.5, 4:-0.5, 5:0"))
	if err := b.Build(); err != nil {
		t.Fatal(err)
	}
	if got := pointsStr(l.Polyline); got != pointsStr(makePolyline("0:0, 5:0")) {
		t.Errorf("got %s", got)
	}
}

func TestBuilderSimplifyOneLoop(t *testing.T) {
	for _, et := range []EdgeType{EdgeDirected, EdgeUndirected} {
		opts := NewBuilderOptions(IdentitySnapFunction{1 * s1.Degree})
		opts.SetSimplifyEdgeChains(true)
		b := NewBuilder(opts)
		l := &PolygonLayer{EdgeType: et}
		b.StartLayer(l)
		b.AddPolygon(s2.PolygonFromLoops([]*s2.Loop{s2.RegularLoop(s2.PointFromCoords(1, 0, 0), 20*s1.Degree, 1000)}))
		if err := b.Build(); err != nil {
			t.Fatal(err)
		}
		if l.Polygon.NumLoops() != 1 {
			t.Fatalf("loops %d", l.Polygon.NumLoops())
		}
		if n := l.Polygon.Loop(0).NumVertices(); n < 10 || n > 12 {
			t.Errorf("edge type %d: %d vertices, want 10..12", et, n)
		}
	}
}
