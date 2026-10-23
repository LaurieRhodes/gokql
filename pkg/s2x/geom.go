package s2x

// Geometry helpers ported from the current C++ S2 (s2edge_crossings.cc,
// s2edge_distances.cc, s1chord_angle.cc) where golang/geo has an older
// variant or none.

import (
	"math"
	"math/big"

	"github.com/golang/geo/r3"
	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

const dblEpsilon = 0x1p-52

// robustCrossProdError is S2::kRobustCrossProdError.
const robustCrossProdError = 6 * dblErr

// intersectionError is S2::kIntersectionError; intersectionMergeRadius is
// S2::kIntersectionMergeRadius.
const (
	intersectionError       = s1.Angle(8 * dblErr)
	intersectionMergeRadius = 2 * intersectionError
)

func chordFromAngle(a s1.Angle) s1.ChordAngle { return s1.ChordAngleFromAngle(a) }

func chordBetween(x, y s2.Point) s1.ChordAngle { return s2.ChordAngleBetweenPoints(x, y) }

// plusError is S1ChordAngle::PlusError.
func plusError(c s1.ChordAngle, e float64) s1.ChordAngle { return c.Expanded(e) }

// s1AngleConstructorMaxError is S1ChordAngle::GetS1AngleConstructorMaxError.
func s1AngleConstructorMaxError(c s1.ChordAngle) float64 { return 1.5 * dblEpsilon * float64(c) }

// s2PointConstructorMaxError is S1ChordAngle::GetS2PointConstructorMaxError.
func s2PointConstructorMaxError(c s1.ChordAngle) float64 {
	return 4.5*dblEpsilon*float64(c) + 16*dblEpsilon*dblEpsilon
}

func updateMinInteriorDistanceMaxError(dist s1.ChordAngle) float64 {
	if dist >= s1.RightChordAngle {
		return 0
	}
	b := math.Min(1.0, 0.5*float64(dist))
	a := math.Sqrt(b * (2 - b))
	return ((2.5+2*sqrt3+8.5*a)*a + (2+2*sqrt3/3+6.5*(1-b))*b + (23+16/sqrt3)*dblEpsilon) * dblEpsilon
}

// updateMinDistanceMaxError is S2::GetUpdateMinDistanceMaxError.
func updateMinDistanceMaxError(dist s1.ChordAngle) float64 {
	return math.Max(updateMinInteriorDistanceMaxError(dist), s2PointConstructorMaxError(dist))
}

func getStableCrossProd(a, b r3.Vector) (r3.Vector, bool) {
	kMinNorm := (32 * sqrt3 * dblErr) / (robustCrossProdError/tErr - (1 + 2*sqrt3))
	r := a.Sub(b).Cross(a.Add(b))
	return r, r.Norm2() >= kMinNorm*kMinNorm
}

func symbolicCrossProdSorted(a, b r3.Vector) r3.Vector {
	if b.X != 0 || b.Y != 0 {
		return r3.Vector{X: -b.Y, Y: b.X, Z: 0}
	}
	if b.Z != 0 {
		return r3.Vector{X: b.Z, Y: 0, Z: 0}
	}
	if a.X != 0 || a.Y != 0 {
		return r3.Vector{X: a.Y, Y: -a.X, Z: 0}
	}
	return r3.Vector{X: 1, Y: 0, Z: 0}
}

func isNormalizable(p r3.Vector) bool {
	return math.Max(math.Abs(p.X), math.Max(math.Abs(p.Y), math.Abs(p.Z))) >= math.Ldexp(1, -242)
}

func ensureNormalizable(p r3.Vector) r3.Vector {
	if !isNormalizable(p) {
		pMax := math.Max(math.Abs(p.X), math.Max(math.Abs(p.Y), math.Abs(p.Z)))
		return p.Mul(math.Ldexp(2, -1-math.Ilogb(pMax)))
	}
	return p
}

func normalizableFromExact(xf r3.PreciseVector) r3.Vector {
	x := xf.Vector()
	if isNormalizable(x) {
		return x
	}
	// Scale by the largest exponent so the result is representable.
	exp := math.MinInt32
	for _, f := range []*big.Float{xf.X, xf.Y, xf.Z} {
		if f.Sign() != 0 {
			exp = max(exp, f.MantExp(nil))
		}
	}
	if exp == math.MinInt32 {
		return r3.Vector{}
	}
	sc := func(f *big.Float) float64 {
		g := new(big.Float).SetPrec(r3.MaxPrec).SetMantExp(f, -exp)
		v, _ := g.Float64()
		return v
	}
	return r3.Vector{X: sc(xf.X), Y: sc(xf.Y), Z: sc(xf.Z)}
}

func symbolicCrossProd(a, b r3.Vector) r3.Vector {
	if a.Cmp(b) < 0 {
		return ensureNormalizable(symbolicCrossProdSorted(a, b))
	}
	return ensureNormalizable(symbolicCrossProdSorted(b, a)).Mul(-1)
}

func exactCrossProd(a, b r3.Vector) r3.Vector {
	xf := r3.PreciseVectorFromVector(a).Cross(r3.PreciseVectorFromVector(b))
	if !xf.IsZero() {
		return normalizableFromExact(xf)
	}
	if a.Cmp(b) < 0 {
		return ensureNormalizable(symbolicCrossProd(a, b))
	}
	return ensureNormalizable(symbolicCrossProd(b, a)).Mul(-1)
}

// robustCrossProd is S2::RobustCrossProd (not normalized).
func robustCrossProd(a, b s2.Point) r3.Vector {
	if r, ok := getStableCrossProd(a.Vector, b.Vector); ok {
		return r
	}
	if a == b {
		return s2.Ortho(a).Vector
	}
	return exactCrossProd(a.Vector, b.Vector)
}

func unit(v r3.Vector) s2.Point { return s2.Point{Vector: v.Normalize()} }

// project is S2::Project with a precomputed a×b.
func project(x, a, b s2.Point, aCrossB r3.Vector) s2.Point {
	if x == a || x == b {
		return x
	}
	n := unit(aCrossB)
	p := unit(robustCrossProd(n, x).Cross(n.Vector))
	if sign(p, n, a) > 0 && sign(p, n, b) < 0 {
		return p
	}
	if x.Sub(a.Vector).Norm2() <= x.Sub(b.Vector).Norm2() {
		return a
	}
	return b
}

func projectAB(x, a, b s2.Point) s2.Point { return project(x, a, b, robustCrossProd(a, b)) }

// pointOnRayChord is S2::GetPointOnRay with an S1ChordAngle distance.
func pointOnRayChord(origin, dir s2.Point, r s1.ChordAngle) s2.Point {
	return unit(origin.Mul(r.Cos()).Add(dir.Mul(r.Sin())))
}

// pointOnRay is S2::GetPointOnRay with an S1Angle distance.
func pointOnRay(origin, dir s2.Point, r s1.Angle) s2.Point {
	s, c := math.Sincos(r.Radians())
	return unit(origin.Mul(c).Add(dir.Mul(s)))
}

// pointOnLine is S2::GetPointOnLine.
func pointOnLine(a, b s2.Point, r s1.Angle) s2.Point {
	dir := unit(robustCrossProd(a, b).Cross(a.Vector))
	return pointOnRay(a, dir, r)
}

func angleBetween(x, y r3.Vector) s1.Angle { return x.Angle(y) }

// isEdgeBNearEdgeA is S2::IsEdgeBNearEdgeA.
func isEdgeBNearEdgeA(a0, a1, b0, b1 s2.Point, tolerance s1.Angle) bool {
	aOrtho := robustCrossProd(a0, a1).Normalize()
	aNearestB0 := project(b0, a0, a1, aOrtho)
	aNearestB1 := project(b1, a0, a1, aOrtho)
	if sign(s2.Point{Vector: aOrtho}, aNearestB0, aNearestB1) < 0 {
		aOrtho = aOrtho.Mul(-1)
	}
	b0Dist := angleBetween(b0.Vector, aNearestB0.Vector)
	b1Dist := angleBetween(b1.Vector, aNearestB1.Vector)
	if b0Dist > tolerance || b1Dist > tolerance {
		return false
	}
	bOrtho := robustCrossProd(b0, b1).Normalize()
	planar := angleBetween(aOrtho, bOrtho)
	if planar <= tolerance {
		return true
	}
	if planar >= math.Pi/2 {
		return (angleBetween(b0.Vector, a0.Vector) < angleBetween(b0.Vector, a1.Vector)) ==
			(angleBetween(b1.Vector, a0.Vector) < angleBetween(b1.Vector, a1.Vector))
	}
	furthest := unit(bOrtho.Cross(robustCrossProd(s2.Point{Vector: aOrtho}, s2.Point{Vector: bOrtho})))
	furthestInv := s2.Point{Vector: furthest.Mul(-1)}
	bo := s2.Point{Vector: bOrtho}
	return !((sign(bo, b0, furthest) > 0 && sign(furthest, b1, bo) > 0) ||
		(sign(bo, b0, furthestInv) > 0 && sign(furthestInv, b1, bo) > 0))
}

// cellIDOf is S2CellId(point).
func cellIDOf(p s2.Point) s2.CellID { return s2.CellFromPoint(p).ID() }

// IntersectionMergeRadius is S2::kIntersectionMergeRadius.
const IntersectionMergeRadius = intersectionMergeRadius
