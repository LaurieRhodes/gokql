package h3

import "math"

const (
	m2Pi       = 6.28318530717958647692528676655900576839433
	mPi180     = 0.0174532925199432957692369076848861271111
	m180Pi     = 57.29577951308232087679815481410517033240547
	epsilon    = 0.0000000000000001
	sqrt3_2    = 0.8660254037844386467637231707529361834714
	sin60      = sqrt3_2
	rsin60     = 1.1547005383792515290182975610039149112953
	oneThird   = 0.333333333333333333333333333333333333333
	oneSeventh = 0.14285714285714285714285714285714285
	ap7RotRads = 0.333473172251832115336090755351601070065900389
	sinAp7Rot  = 0.3273268353539885718950318
	cosAp7Rot  = 0.9449111825230680680167902
	// EarthRadiusKm is the authalic radius H3 uses.
	EarthRadiusKm     = 6371.007180918475
	res0UGnomonic     = 0.38196601125010500003
	invRes0UGnomonic  = 2.61803398874989588842
	MaxRes            = 15
	numIcosaFaces     = 20
	numBaseCells      = 122
	numHexVerts       = 6
	numPentVerts      = 5
	numPentagons      = 12
	cellMode          = 1
	maxCellBndryVerts = 10
)

// vec2d is a 2D floating-point vector.
type vec2d struct{ x, y float64 }

func v2dMag(v vec2d) float64 { return math.Sqrt(v.x*v.x + v.y*v.y) }

// v2dIntersect is the intersection of lines p0-p1 and p2-p3.
func v2dIntersect(p0, p1, p2, p3 vec2d) vec2d {
	s1 := vec2d{p1.x - p0.x, p1.y - p0.y}
	s2 := vec2d{p3.x - p2.x, p3.y - p2.y}
	t := (s2.x*(p0.y-p2.y) - s2.y*(p0.x-p2.x)) / (-s2.x*s1.y + s1.x*s2.y)
	return vec2d{p0.x + t*s1.x, p0.y + t*s1.y}
}

// flt32Epsilon is C's FLT_EPSILON.
const flt32Epsilon = 1.1920928955078125e-07

func v2dAlmostEquals(a, b vec2d) bool {
	return math.Abs(a.x-b.x) < flt32Epsilon && math.Abs(a.y-b.y) < flt32Epsilon
}

// LatLng is a point in radians.
type LatLng struct{ Lat, Lng float64 }

// vec3d is a 3D floating-point vector.
type vec3d struct{ x, y, z float64 }

// RadsToDegs and DegsToRads convert as H3 does (multiplying by the
// constant ratio), which keeps results bit-identical to the C library
// more often than dividing by π.
func RadsToDegs(r float64) float64 { return r * m180Pi }
func DegsToRads(d float64) float64 { return d * mPi180 }
