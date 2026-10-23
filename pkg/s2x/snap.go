package s2x

// Snap functions (s2builderutil_snap_functions.cc). golang/geo has
// IdentitySnapper and CellIDSnapper, but its IntLatLngSnapper rounds radians
// rather than degrees, so all three are ported here.

import (
	"math"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

// SnapFunction is S2Builder::SnapFunction (the same method set as
// s2.Snapper).
type SnapFunction = s2.Snapper

// IdentitySnapFunction snaps nothing but merges vertices within the radius.
type IdentitySnapFunction struct{ Radius s1.Angle }

func (f IdentitySnapFunction) SnapRadius() s1.Angle              { return f.Radius }
func (f IdentitySnapFunction) MinVertexSeparation() s1.Angle     { return f.Radius }
func (f IdentitySnapFunction) MinEdgeVertexSeparation() s1.Angle { return 0.5 * f.Radius }
func (f IdentitySnapFunction) SnapPoint(p s2.Point) s2.Point     { return p }

// CellIDSnapFunction snaps to S2CellId centers at a level.
type CellIDSnapFunction struct {
	Level  int
	Radius s1.Angle
}

// CellIDSnapMinRadius is S2CellIdSnapFunction::MinSnapRadiusForLevel.
func CellIDSnapMinRadius(level int) s1.Angle {
	return s1.Angle(0.5*s2.MaxDiagMetric.Value(level) + 4*dblEpsilon)
}

// CellIDSnapLevelForMaxRadius is S2CellIdSnapFunction::LevelForMaxSnapRadius.
func CellIDSnapLevelForMaxRadius(r s1.Angle) int {
	return s2.MaxDiagMetric.MinLevel(2 * (r.Radians() - 4*dblEpsilon))
}

func NewCellIDSnapFunction(level int) CellIDSnapFunction {
	return CellIDSnapFunction{Level: level, Radius: CellIDSnapMinRadius(level)}
}

func (f CellIDSnapFunction) SnapRadius() s1.Angle { return f.Radius }
func (f CellIDSnapFunction) MinVertexSeparation() s1.Angle {
	minEdge := s1.Angle(s2.MinEdgeMetric.Value(f.Level))
	maxDiag := s1.Angle(s2.MaxDiagMetric.Value(f.Level))
	return max(minEdge, max(0.548*f.Radius, f.Radius-0.5*maxDiag))
}
func (f CellIDSnapFunction) MinEdgeVertexSeparation() s1.Angle {
	minDiag := s1.Angle(s2.MinDiagMetric.Value(f.Level))
	if f.Radius == CellIDSnapMinRadius(f.Level) {
		return 0.565 * minDiag
	}
	vs := f.MinVertexSeparation()
	return max(0.397*minDiag, max(0.219*f.Radius, 0.5*(vs/f.Radius)*vs))
}
func (f CellIDSnapFunction) SnapPoint(p s2.Point) s2.Point {
	return cellIDOf(p).Parent(f.Level).Point()
}

// IntLatLngSnapFunction snaps to E-exponent lat/lng coordinates.
type IntLatLngSnapFunction struct {
	Exponent               int
	Radius                 s1.Angle
	fromDegrees, toDegrees float64
}

const radToDeg = 180 / math.Pi

// IntLatLngSnapMinRadius is IntLatLngSnapFunction::MinSnapRadiusForExponent.
func IntLatLngSnapMinRadius(exponent int) s1.Angle {
	power := 1.0
	for i := 0; i < exponent; i++ {
		power *= 10
	}
	return degrees(math.Sqrt2/2/power) + s1.Angle((9*math.Sqrt2+1.5)*dblEpsilon)
}

func degrees(d float64) s1.Angle { return s1.Angle(d * (math.Pi / 180)) }

func NewIntLatLngSnapFunction(exponent int) IntLatLngSnapFunction {
	power := 1.0
	for i := 0; i < exponent; i++ {
		power *= 10
	}
	return IntLatLngSnapFunction{Exponent: exponent, Radius: IntLatLngSnapMinRadius(exponent),
		fromDegrees: power, toDegrees: 1 / power}
}

func (f IntLatLngSnapFunction) SnapRadius() s1.Angle { return f.Radius }
func (f IntLatLngSnapFunction) MinVertexSeparation() s1.Angle {
	return max(0.471*f.Radius, f.Radius-degrees(math.Sqrt2/2*f.toDegrees))
}
func (f IntLatLngSnapFunction) MinEdgeVertexSeparation() s1.Angle {
	vs := f.MinVertexSeparation()
	return max(0.277*degrees(f.toDegrees), max(0.222*f.Radius, 0.5*(vs/f.Radius)*vs))
}
func (f IntLatLngSnapFunction) SnapPoint(p s2.Point) s2.Point {
	ll := s2.LatLngFromPoint(p)
	lat := math.Round(float64(ll.Lat) * radToDeg * f.fromDegrees)
	lng := math.Round(float64(ll.Lng) * radToDeg * f.fromDegrees)
	return s2.PointFromLatLng(s2.LatLng{Lat: degrees(lat * f.toDegrees), Lng: degrees(lng * f.toDegrees)})
}
