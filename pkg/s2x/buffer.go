package s2x

// S2BufferOperation (s2buffer_operation.cc): expands or shrinks points,
// polylines and polygons by a radius, via S2WindingOperation.

import (
	"errors"
	"math"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

// EndCapStyle and PolylineSide are S2BufferOperation options.
type EndCapStyle uint8

const (
	EndCapRound EndCapStyle = iota
	EndCapFlat
)

type PolylineSide uint8

const (
	PolylineBoth PolylineSide = iota
	PolylineLeft
	PolylineRight
)

const (
	minRequestedError             = s1.Angle(2 * dblErr)
	getPointOnLineError           = s1.Angle((4+2/sqrt3)*dblErr) + s1.Angle(robustCrossProdError)
	getPointOnRayPerpendicularErr = s1.Angle(3 * dblErr)
	maxAbsoluteInterpolationError = getPointOnLineError + getPointOnRayPerpendicularErr
	BufferMinErrorFraction        = 1e-6
	BufferDefaultErrorFraction    = 0.02
	bufferRightAngle              = s1.ChordAngle(2)
)

// BufferOptions is S2BufferOperation::Options.
type BufferOptions struct {
	Radius        s1.Angle
	ErrorFraction float64
	EndCap        EndCapStyle
	Side          PolylineSide
	Snap          SnapFunction
}

// NewBufferOptions returns the C++ defaults for a radius.
func NewBufferOptions(r s1.Angle) BufferOptions {
	return BufferOptions{Radius: r, ErrorFraction: BufferDefaultErrorFraction, Snap: IdentitySnapFunction{}}
}

// SetErrorFraction clamps as the C++ setter does.
func (o *BufferOptions) SetErrorFraction(f float64) {
	o.ErrorFraction = math.Max(BufferMinErrorFraction, math.Min(1, f))
}

// BufferMaxCircleSegments corresponds to BufferMinErrorFraction.
const BufferMaxCircleSegments = 1570.7968503979573

// CircleSegments is the number of segments used for a full circle.
func (o *BufferOptions) CircleSegments() float64 { return math.Pi / math.Acos(1-o.ErrorFraction) }

// SetCircleSegments sets the error fraction from a segment count.
func (o *BufferOptions) SetCircleSegments(n float64) {
	n = math.Max(2, math.Min(BufferMaxCircleSegments, n))
	o.SetErrorFraction(1 - math.Cos(math.Pi/n) + 1e-15)
}

// BufferOperation is S2BufferOperation.
type BufferOperation struct {
	opts        BufferOptions
	bufferSign  int
	absRadius   s1.ChordAngle
	vertexStep  s1.ChordAngle
	pointStep   s1.ChordAngle
	edgeStep    s1.ChordAngle
	refPoint    s2.Point
	refWinding  int
	inputStart  s2.Point
	offsetStart s2.Point
	haveInput   bool
	haveOffset  bool
	sweepA      s2.Point
	sweepB      s2.Point
	path        []s2.Point
	numPolygons int
	op          *WindingOperation
}

func sgnf(x float64) int {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	}
	return 0
}

// NewBufferOperation is S2BufferOperation::Init.
func NewBufferOperation(result Layer, opts BufferOptions) *BufferOperation {
	b := &BufferOperation{opts: opts, refPoint: s2.OriginPoint()}
	b.bufferSign = sgnf(opts.Radius.Radians())
	absRadius := opts.Radius.Abs()
	requested := max(minRequestedError, s1.Angle(opts.ErrorFraction)*absRadius)
	maxErr := maxAbsoluteInterpolationError + requested
	if absRadius <= maxErr {
		b.absRadius = 0
		b.bufferSign = 0
	} else if absRadius+maxErr >= math.Pi {
		b.absRadius = s1.StraightChordAngle
	} else {
		b.absRadius = s1.ChordAngleFromAngle(absRadius)
		vstep := maxEdgeSpan(absRadius, requested)
		b.vertexStep = s1.ChordAngleFromAngle(vstep)
		b.pointStep = s1.ChordAngleFromAngle(s1.Angle(2*math.Pi/math.Ceil(2*math.Pi/vstep.Radians()) + 1e-15))
		edgeRadius := s1.Angle(math.Pi/2) - absRadius
		if edgeRadius > maxErr {
			b.edgeStep = s1.ChordAngleFromAngle(maxEdgeSpan(edgeRadius, requested))
		}
	}
	b.op = NewWindingOperation(result, opts.Snap, b.bufferSign == 0 && opts.Radius >= 0)
	return b
}

func maxEdgeSpan(radius, requested s1.Angle) s1.Angle {
	step := s1.Angle(2*math.Pi/3 + 1e-15)
	minRadius := radius - requested
	if radius.Radians() < math.Pi/2 {
		step = min(step, s1.Angle(2*math.Acos(math.Tan(minRadius.Radians())/math.Tan(radius.Radians()))))
	} else if minRadius.Radians() > math.Pi/2 {
		step = min(step, s1.Angle(2*math.Acos(math.Tan(radius.Radians())/math.Tan(minRadius.Radians()))))
	}
	return step
}

func (b *BufferOperation) setInputVertex(a s2.Point) {
	if b.haveInput {
		b.updateRefWinding(b.sweepA, b.sweepB, a)
	} else {
		b.inputStart = a
		b.haveInput = true
	}
	b.sweepA = a
}

func (b *BufferOperation) addOffsetVertex(p s2.Point) {
	b.path = append(b.path, p)
	if b.haveOffset {
		b.updateRefWinding(b.sweepA, b.sweepB, p)
	} else {
		b.offsetStart = p
		b.haveOffset = true
	}
	b.sweepB = p
}

func (b *BufferOperation) closeBufferRegion() {
	if b.haveOffset && b.haveInput {
		b.updateRefWinding(b.sweepA, b.sweepB, b.inputStart)
		b.updateRefWinding(b.inputStart, b.sweepB, b.offsetStart)
	}
}

func (b *BufferOperation) outputPath() {
	b.op.AddLoop(b.path)
	b.path = nil
	b.haveInput = false
	b.haveOffset = false
}

func (b *BufferOperation) updateRefWinding(a, bb, c s2.Point) {
	sg := sign(a, bb, c)
	if sg == 0 {
		return
	}
	inside := s2.AngleContainsVertex(a, bb, c) == (sg > 0)
	cr := s2.NewEdgeCrosser(bb, b.refPoint)
	inside = inside != cr.EdgeOrVertexCrossing(a, bb)
	inside = inside != cr.EdgeOrVertexCrossing(bb, c)
	inside = inside != cr.EdgeOrVertexCrossing(c, a)
	if inside {
		b.refWinding += sg
	}
}

func (b *BufferOperation) addFullPolygon() { b.refWinding++ }

func neg(p s2.Point) s2.Point { return s2.Point{Vector: p.Mul(-1)} }

// AddPoint buffers a point.
func (b *BufferOperation) AddPoint(p s2.Point) {
	if b.bufferSign < 0 {
		return
	}
	if b.absRadius >= s1.StraightChordAngle {
		b.addFullPolygon()
		return
	}
	if b.bufferSign == 0 {
		b.path = append(b.path, p)
	} else {
		b.setInputVertex(p)
		start := s2.Ortho(p)
		angle := s1.ChordAngle(0)
		for q := 0; q < 4; q++ {
			rot := unit(p.Cross(start.Vector))
			for ; angle < bufferRightAngle; angle = angle.Add(b.pointStep) {
				dir := pointOnRayChord(start, rot, angle)
				b.addOffsetVertex(pointOnRayChord(p, dir, b.absRadius))
			}
			angle = angle.Sub(bufferRightAngle)
			start = rot
		}
		b.closeBufferRegion()
	}
	b.outputPath()
}

func (b *BufferOperation) edgeAxis(a, c s2.Point) s2.Point {
	return s2.Point{Vector: unit(robustCrossProd(c, a)).Mul(float64(b.bufferSign))}
}

func (b *BufferOperation) addVertexArc(v, start, end s2.Point) {
	rot := s2.Point{Vector: unit(v.Cross(start.Vector)).Mul(float64(b.bufferSign))}
	angle, span := s1.ChordAngle(0), s2.ChordAngleBetweenPoints(start, end)
	for {
		dir := pointOnRayChord(start, rot, angle)
		b.addOffsetVertex(pointOnRayChord(v, dir, b.absRadius))
		angle = angle.Add(b.vertexStep)
		if angle >= span {
			break
		}
	}
}

func (b *BufferOperation) closeVertexArc(v, end s2.Point) {
	b.addOffsetVertex(pointOnRayChord(v, end, b.absRadius))
}

func (b *BufferOperation) addEdgeArc(a, c s2.Point) {
	axis := b.edgeAxis(a, c)
	if b.edgeStep == 0 {
		b.addOffsetVertex(axis)
	} else {
		rot := s2.Point{Vector: unit(a.Cross(axis.Vector)).Mul(float64(b.bufferSign))}
		angle, span := s1.ChordAngle(0), s2.ChordAngleBetweenPoints(a, c)
		for {
			p := pointOnRayChord(a, rot, angle)
			b.addOffsetVertex(pointOnRayChord(p, axis, b.absRadius))
			angle = angle.Add(b.edgeStep)
			if angle >= span {
				break
			}
		}
	}
	b.setInputVertex(c)
}

func (b *BufferOperation) closeEdgeArc(a, c s2.Point) {
	if b.edgeStep != 0 {
		b.addOffsetVertex(pointOnRayChord(c, b.edgeAxis(a, c), b.absRadius))
	}
}

func (b *BufferOperation) bufferEdgeAndVertex(a, v, c s2.Point) {
	b.addEdgeArc(a, v)
	if b.bufferSign*sign(a, v, c) >= 0 {
		start := b.edgeAxis(a, v)
		end := b.edgeAxis(v, c)
		b.addVertexArc(v, start, end)
		if b.edgeStep == 0 {
			b.closeVertexArc(v, end)
		}
	} else {
		b.closeEdgeArc(a, v)
		b.addOffsetVertex(v)
	}
}

func (b *BufferOperation) addStartCap(a, c s2.Point) {
	axis := b.edgeAxis(a, c)
	if b.opts.EndCap == EndCapFlat {
		if b.opts.Side == PolylineBoth {
			b.addOffsetVertex(pointOnRayChord(a, neg(axis), b.absRadius))
		}
		return
	}
	if b.opts.Side == PolylineBoth {
		b.addVertexArc(a, neg(axis), axis)
	} else {
		b.addVertexArc(a, unit(axis.Cross(a.Vector)), axis)
	}
}

func (b *BufferOperation) addEndCap(a, c s2.Point) {
	axis := b.edgeAxis(a, c)
	if b.opts.EndCap == EndCapFlat {
		b.closeEdgeArc(a, c)
		return
	}
	if b.opts.Side == PolylineBoth {
		b.addVertexArc(c, axis, neg(axis))
	} else {
		end := unit(c.Cross(axis.Vector))
		b.addVertexArc(c, axis, end)
		b.closeVertexArc(c, end)
	}
}

func (b *BufferOperation) bufferLoop(loop []s2.Point) {
	n := len(loop)
	if n == 0 {
		return
	}
	if n == 1 {
		b.AddPoint(loop[0])
		return
	}
	if b.absRadius >= s1.StraightChordAngle {
		if b.bufferSign > 0 {
			b.addFullPolygon()
		}
		return
	}
	if b.bufferSign == 0 {
		b.path = append(b.path[:0], loop...)
	} else {
		b.setInputVertex(loop[0])
		for i := 0; i < n; i++ {
			b.bufferEdgeAndVertex(loop[i], loop[(i+1)%n], loop[(i+2)%n])
		}
		b.closeBufferRegion()
	}
	b.outputPath()
}

// AddPolyline buffers a polyline.
func (b *BufferOperation) AddPolyline(pl []s2.Point) {
	if b.opts.Side == PolylineLeft {
		r := make([]s2.Point, len(pl))
		for i, p := range pl {
			r[len(pl)-1-i] = p
		}
		pl = r
	}
	if b.bufferSign < 0 {
		return
	}
	n := len(pl)
	if n <= 1 {
		return
	}
	if n == 2 && pl[0] == pl[1] {
		b.AddPoint(pl[0])
		return
	}
	if b.absRadius >= s1.StraightChordAngle {
		b.addFullPolygon()
		return
	}
	if b.bufferSign == 0 {
		b.path = append(b.path[:0], pl[:n-1]...)
		for i := n - 1; i >= 1; i-- {
			b.path = append(b.path, pl[i])
		}
	} else {
		b.setInputVertex(pl[0])
		b.addStartCap(pl[0], pl[1])
		for i := 0; i < n-2; i++ {
			b.bufferEdgeAndVertex(pl[i], pl[i+1], pl[i+2])
		}
		b.addEdgeArc(pl[n-2], pl[n-1])
		b.addEndCap(pl[n-2], pl[n-1])
		if b.opts.Side == PolylineBoth {
			for i := n - 3; i >= 0; i-- {
				b.bufferEdgeAndVertex(pl[i+2], pl[i+1], pl[i])
			}
			b.addEdgeArc(pl[1], pl[0])
			b.closeBufferRegion()
		} else {
			for i := n - 1; i >= 0; i-- {
				b.path = append(b.path, pl[i])
			}
		}
	}
	b.outputPath()
}

// AddLoop buffers a loop (the interior is on its left).
func (b *BufferOperation) AddLoop(loop []s2.Point) {
	if len(loop) == 0 {
		return
	}
	b.bufferLoop(loop)
	if containsBruteForce(NewLaxPolygon([][]s2.Point{loop}), b.refPoint) {
		b.refWinding++
	}
	b.numPolygons++
}

// AddShape buffers every chain of a shape.
func (b *BufferOperation) AddShape(sh s2.Shape) {
	dim := sh.Dimension()
	for c := 0; c < sh.NumChains(); c++ {
		ch := sh.Chain(c)
		if ch.Length == 0 {
			continue
		}
		if dim == 0 {
			b.AddPoint(sh.Edge(c).V0)
			continue
		}
		vs := chainVertices(sh, c)
		if dim == 1 {
			b.AddPolyline(vs)
		} else {
			b.bufferLoop(vs)
		}
	}
	if containsBruteForce(sh, b.refPoint) {
		b.refWinding++
	}
	if dim == 2 {
		b.numPolygons++
	}
}

// chainVertices is S2::GetChainVertices.
func chainVertices(sh s2.Shape, c int) []s2.Point {
	ch := sh.Chain(c)
	n := ch.Length
	if sh.Dimension() == 1 {
		n++
	}
	vs := make([]s2.Point, 0, n)
	e0 := ch.Start
	for i := 0; i < n; i++ {
		if i < ch.Length {
			vs = append(vs, sh.Edge(e0+i).V0)
		} else {
			vs = append(vs, sh.Edge(e0+ch.Length-1).V1)
		}
	}
	if sh.Dimension() == 2 && n == 0 {
		return nil
	}
	return vs
}

// ErrNegativeBufferLayers is reported for a negative radius with more than
// one polygon layer.
var ErrNegativeBufferLayers = errors.New("negative buffer radius requires at most one polygon layer")

// Build computes the buffered region.
func (b *BufferOperation) Build() error {
	if b.bufferSign < 0 && b.numPolygons > 1 {
		return ErrNegativeBufferLayers
	}
	return b.op.Build(b.refPoint, b.refWinding, WindingPositive)
}
