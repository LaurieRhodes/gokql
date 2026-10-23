package s2x

// Tests converted from s2buffer_operation_test.cc, plus distance checks
// standing in for its containment tests.

import (
	"math"
	"testing"

	"github.com/golang/geo/r3"
	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

func doBuffer(t *testing.T, input func(*BufferOperation), opts BufferOptions) *LaxPolygon {
	t.Helper()
	layer := NewLaxPolygonLayer()
	op := NewBufferOperation(layer, opts)
	input(op)
	if err := op.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	return layer.Polygon
}

func bufferOpts(r s1.Angle, frac float64) BufferOptions {
	o := NewBufferOptions(r)
	o.SetErrorFraction(frac)
	return o
}

var bufferRadii = []s1.Angle{-200 * s1.Degree, -1 * s1.Degree, 0, 1 * s1.Degree, 200 * s1.Degree}

func testBufferEmpty(t *testing.T, input func(*BufferOperation)) {
	t.Helper()
	for _, r := range bufferRadii {
		if p := doBuffer(t, input, bufferOpts(r, 0.1)); !p.IsEmpty() {
			t.Errorf("radius %v: not empty", r)
		}
	}
}

func TestBuffer_Empty(t *testing.T) {
	testBufferEmpty(t, func(*BufferOperation) {})
	testBufferEmpty(t, func(op *BufferOperation) { op.AddPolyline([]s2.Point{{Vector: r3.Vector{X: 1}}}) })
	testBufferEmpty(t, func(op *BufferOperation) { op.AddLoop(nil) })
	testBufferEmpty(t, func(op *BufferOperation) { op.AddShape(&s2.PointVector{}) })
	testBufferEmpty(t, func(op *BufferOperation) { op.AddShape(s2.LaxPolylineFromPoints(nil)) })
	testBufferEmpty(t, func(op *BufferOperation) { op.AddShape(NewLaxPolygon(nil)) })
}

func TestBuffer_PoorlyNormalizedPoint(t *testing.T) {
	doBuffer(t, func(op *BufferOperation) {
		op.AddPoint(s2.Point{Vector: r3.Vector{X: 1 - 2*dblEpsilon}})
	}, bufferOpts(s1.Degree, 0.01))
}

func TestBuffer_FullPolygonShape(t *testing.T) {
	for _, r := range bufferRadii {
		p := doBuffer(t, func(op *BufferOperation) { op.AddShape(makeLaxPolygon("full")) }, bufferOpts(r, 0.1))
		if !p.IsFull() {
			t.Errorf("radius %v: not full", r)
		}
	}
}

func TestBuffer_PointsAndPolylinesAreRemoved(t *testing.T) {
	p := doBuffer(t, func(op *BufferOperation) {
		op.AddPoint(parsePoint("0:0"))
		op.AddShape(s2.LaxPolylineFromPoints(parsePoints("2:2, 2:3")))
	}, bufferOpts(-s1.Degree, 0.1))
	if !p.IsEmpty() {
		t.Error("not empty")
	}
}

func TestBuffer_BufferedPointsAreSymmetric(t *testing.T) {
	p := doBuffer(t, func(op *BufferOperation) {
		op.AddPoint(s2.Point{Vector: r3.Vector{X: 1}})
	}, bufferOpts(5*s1.Degree, 0.001234567))
	vs := p.LoopVertices(0)
	n := len(vs)
	edge := vs[0].Distance(vs[n-1])
	for i := 1; i < n; i++ {
		if d := math.Abs(float64(edge - vs[i-1].Distance(vs[i]))); d > 1e-14 {
			t.Errorf("edge %d differs by %g", i, d)
		}
	}
}

func TestBuffer_SetCircleSegments(t *testing.T) {
	o := NewBufferOptions(1e-12)
	for n := 3; n <= 20; n++ {
		o.SetCircleSegments(float64(n))
		if got := o.CircleSegments(); math.Abs(got-float64(n)) > 1e-5*float64(n) {
			t.Errorf("CircleSegments() = %v, want %d", got, n)
		}
		p := doBuffer(t, func(op *BufferOperation) { op.AddPoint(s2.Point{Vector: r3.Vector{X: 1}}) }, o)
		if p.NumEdges() != n {
			t.Errorf("%d segments: %d vertices", n, p.NumEdges())
		}
	}
}

func TestBuffer_SetSnapFunction(t *testing.T) {
	o := NewBufferOptions(0)
	o.Snap = NewIntLatLngSnapFunction(0)
	p := doBuffer(t, func(op *BufferOperation) { op.AddPoint(parsePoint("0.1:-0.4")) }, o)
	if p.NumEdges() != 1 || p.LoopVertices(0)[0] != parsePoint("0:0") {
		t.Errorf("got %v", p.loops)
	}
}

func TestBuffer_NegativeBufferRadiusMultipleLayers(t *testing.T) {
	op := NewBufferOperation(NewLaxPolygonLayer(), NewBufferOptions(-1))
	op.AddLoop(parsePoints("0:0, 0:1, 1:0"))
	op.AddShape(makeLaxPolygon("2:2, 2:3, 3:2"))
	if err := op.Build(); err != ErrNegativeBufferLayers {
		t.Errorf("err = %v", err)
	}
}

// bufferMaxError is S2BufferOperation::Options::max_error() for identity
// snapping with a zero radius.
func bufferMaxError(o BufferOptions) s1.Angle {
	return max(minRequestedError, s1.Angle(o.ErrorFraction)*o.Radius.Abs()) + maxAbsoluteInterpolationError + intersectionError
}

func distanceToEdges(p s2.Point, pts []s2.Point, closed bool) s1.Angle {
	if len(pts) == 1 {
		return p.Distance(pts[0])
	}
	d := s1.InfAngle()
	n := len(pts) - 1
	if closed {
		n++
	}
	for i := 0; i < n; i++ {
		d = min(d, s2.DistanceFromSegment(p, pts[i], pts[(i+1)%len(pts)]))
	}
	return d
}

// TestBuffer_Distances checks that every output vertex of a positive
// buffer lies within max_error of the buffer radius from the input.
func TestBuffer_Distances(t *testing.T) {
	inputs := []struct {
		name   string
		pts    []s2.Point
		closed bool
		add    func(op *BufferOperation, pts []s2.Point)
	}{
		{"point", parsePoints("10:20"), false, func(op *BufferOperation, p []s2.Point) { op.AddPoint(p[0]) }},
		{"zigzag", parsePoints("0:0, 0:7, 5:3, 5:10"), false, func(op *BufferOperation, p []s2.Point) { op.AddPolyline(p) }},
		{"square", parsePoints("0:0, 0:10, 10:10, 10:0"), true, func(op *BufferOperation, p []s2.Point) { op.AddLoop(p) }},
	}
	for _, in := range inputs {
		for _, r := range []s1.Angle{1e-5, 0.01 * s1.Degree, 1 * s1.Degree, 10 * s1.Degree} {
			for _, f := range []float64{1e-4, 0.01, 0.1, 0.5} {
				o := bufferOpts(r, f)
				p := doBuffer(t, func(op *BufferOperation) { in.add(op, in.pts) }, o)
				if p.IsEmpty() {
					t.Errorf("%s r=%v f=%v: empty", in.name, r, f)
					continue
				}
				maxErr := bufferMaxError(o)
				for _, lp := range p.loops {
					for _, v := range lp {
						d := distanceToEdges(v, in.pts, in.closed)
						if in.closed && d < r-maxErr && containsBruteForce(NewLaxPolygon([][]s2.Point{in.pts}), v) {
							continue // interior vertex of a concave corner join
						}
						if d > r+maxErr || d < r-maxErr {
							t.Errorf("%s r=%v f=%v: vertex at %v (max error %v)", in.name, r, f, d, maxErr)
						}
					}
				}
				for _, v := range in.pts {
					if !containsBruteForce(p, v) {
						t.Errorf("%s r=%v f=%v: input vertex not contained", in.name, r, f)
					}
				}
			}
		}
	}
}
