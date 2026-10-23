package s2x

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

// parsePoints reads s2textformat "lat:lng, lat:lng" (degrees).
func parsePoints(s string) []s2.Point {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []s2.Point
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		ll := strings.Split(f, ":")
		lat, err1 := strconv.ParseFloat(strings.TrimSpace(ll[0]), 64)
		lng, err2 := strconv.ParseFloat(strings.TrimSpace(ll[1]), 64)
		if err1 != nil || err2 != nil {
			panic("bad point " + f)
		}
		out = append(out, s2.PointFromLatLng(s2.LatLngFromDegrees(lat, lng)))
	}
	return out
}

func parsePoint(s string) s2.Point { return parsePoints(s)[0] }

// makeLoop is s2textformat::MakeLoop ("empty" and "full" supported).
func makeLoop(s string) *s2.Loop {
	s = strings.TrimSpace(s)
	switch s {
	case "empty":
		return s2.EmptyLoop()
	case "full":
		return s2.FullLoop()
	}
	return s2.LoopFromPoints(parsePoints(s))
}

// makePolygon is s2textformat::MakePolygon: loops separated by ';'.
func makePolygon(s string) *s2.Polygon {
	s = strings.TrimSpace(s)
	if s == "" || s == "empty" {
		return s2.PolygonFromLoops(nil)
	}
	if s == "full" {
		return s2.FullPolygon()
	}
	var loops []*s2.Loop
	for _, ls := range strings.Split(s, ";") {
		if strings.TrimSpace(ls) == "" {
			continue
		}
		l := makeLoop(ls)
		l.Normalize()
		loops = append(loops, l)
	}
	return s2.PolygonFromLoops(loops)
}

func makePolyline(s string) s2.Polyline { return s2.Polyline(parsePoints(s)) }

func pointStr(p s2.Point) string {
	ll := s2.LatLngFromPoint(p)
	return strconv.FormatFloat(ll.Lat.Degrees(), 'g', 15, 64) + ":" + strconv.FormatFloat(ll.Lng.Degrees(), 'g', 15, 64)
}

func pointsStr(ps []s2.Point) string {
	var b []string
	for _, p := range ps {
		b = append(b, pointStr(p))
	}
	return strings.Join(b, ", ")
}

func polygonStr(p *s2.Polygon) string {
	if p.IsFull() {
		return "full"
	}
	var b []string
	for i := 0; i < p.NumLoops(); i++ {
		b = append(b, pointsStr(p.Loop(i).Vertices()))
	}
	return strings.Join(b, ";\n")
}

func loopsApproxEqual(a, b []s2.Point, tol s1.Angle) bool {
	if len(a) != len(b) {
		return false
	}
	n := len(a)
	if n == 0 {
		return true
	}
	for off := 0; off < n; off++ {
		if a[0].Angle(b[off].Vector) > tol {
			continue
		}
		ok := true
		for i := 0; i < n; i++ {
			if a[i].Angle(b[(i+off)%n].Vector) > tol {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func polygonsApproxEqual(a, b *s2.Polygon, tol s1.Angle) bool {
	if a.NumLoops() != b.NumLoops() {
		return false
	}
	used := make([]bool, b.NumLoops())
	for i := 0; i < a.NumLoops(); i++ {
		found := false
		for j := 0; j < b.NumLoops(); j++ {
			if !used[j] && a.Loop(i).IsHole() == b.Loop(j).IsHole() &&
				loopsApproxEqual(a.Loop(i).Vertices(), b.Loop(j).Vertices(), tol) {
				used[j] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func expectPolygonsApproxEqual(t *testing.T, expected, actual *s2.Polygon, tol s1.Angle) {
	t.Helper()
	if !polygonsApproxEqual(expected, actual, tol) {
		t.Errorf("\nexpected: %s\nactual:   %s\ntolerance %v", polygonStr(expected), polygonStr(actual), tol.Degrees())
	}
}

func expectPolygonsEqual(t *testing.T, expected, actual *s2.Polygon) {
	t.Helper()
	expectPolygonsApproxEqual(t, expected, actual, 0)
}

var _ = fmt.Sprint
