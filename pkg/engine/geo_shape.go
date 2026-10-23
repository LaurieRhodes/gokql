package engine

// geo_shape.go — GeoJSON geometry as S2 shapes, for the geo_* functions.
//
// ADX's geospatial model is S2's: WGS-84 coordinates on a sphere,
// geodesic edges, a loop taken as the smaller region it bounds, and
// semi-open containment (when the Earth is subdivided into polygons,
// every point is in exactly one). The functions build on golang/geo's
// s2 package, which implements exactly that model.
//
// Accepted input: Point, MultiPoint, LineString, MultiLineString,
// Polygon, MultiPolygon, a Feature (its geometry) and a
// GeometryCollection. Coordinates are [longitude, latitude]; anything
// malformed or out of range makes the shape invalid (the functions
// then return null).

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

type geoShape struct {
	points []s2.Point
	lines  []s2.Polyline // each with at least two vertices
	// polys holds each GeoJSON polygon separately: a MultiPolygon's
	// members may overlap (measures of their union need polygon
	// boolean operations, which golang/geo lacks).
	polys []*s2.Polygon
}

func (g *geoShape) isPolygonal() bool {
	return len(g.polys) > 0 && len(g.points) == 0 && len(g.lines) == 0
}
func (g *geoShape) isLinear() bool {
	return len(g.lines) > 0 && len(g.polys) == 0 && len(g.points) == 0
}

// containsPoint: in any member polygon.
func (g *geoShape) containsPoint(p s2.Point) bool {
	for _, poly := range g.polys {
		if poly.ContainsPoint(p) {
			return true
		}
	}
	return false
}

// overlapping reports whether member polygons intersect, in which case
// union measures (area, perimeter, centroid) are not available.
func (g *geoShape) overlapping() bool {
	for i := range g.polys {
		for j := i + 1; j < len(g.polys); j++ {
			if g.polys[i].Intersects(g.polys[j]) {
				return true
			}
		}
	}
	return false
}

// geoShapeCache holds parsed shapes by their JSON text: a polygon
// literal is evaluated once per row. Bounded by a crude reset.
var geoShapeCache = struct {
	sync.Mutex
	m map[string]*geoShape
}{m: map[string]*geoShape{}}

const geoShapeCacheMax = 4096

// parseGeoShape parses a dynamic GeoJSON value; nil when invalid.
func parseGeoShape(v interface{}) *geoShape {
	s, ok := v.(string)
	if !ok || s == "" {
		return nil
	}
	geoShapeCache.Lock()
	if g, hit := geoShapeCache.m[s]; hit {
		geoShapeCache.Unlock()
		return g
	}
	geoShapeCache.Unlock()

	var raw interface{}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if dec.Decode(&raw) != nil {
		return nil
	}
	g := &geoShape{}
	if !g.add(raw, 0) || (len(g.points) == 0 && len(g.lines) == 0 && len(g.polys) == 0) {
		g = nil
	}

	geoShapeCache.Lock()
	if len(geoShapeCache.m) >= geoShapeCacheMax {
		geoShapeCache.m = map[string]*geoShape{}
	}
	geoShapeCache.m[s] = g
	geoShapeCache.Unlock()
	return g
}

func (g *geoShape) add(raw interface{}, depth int) bool {
	obj, ok := raw.(map[string]interface{})
	if !ok || depth > 8 {
		return false
	}
	typ, _ := obj["type"].(string)
	coords := obj["coordinates"]
	switch typ {
	case "Feature":
		return g.add(obj["geometry"], depth+1)
	case "GeometryCollection":
		gs, ok := obj["geometries"].([]interface{})
		if !ok {
			return false
		}
		for _, x := range gs {
			if !g.add(x, depth+1) {
				return false
			}
		}
		return true
	case "Point":
		p, ok := geoJSONPoint(coords)
		if !ok {
			return false
		}
		g.points = append(g.points, p)
		return true
	case "MultiPoint":
		ps, ok := geoJSONPoints(coords, 1)
		if !ok {
			return false
		}
		g.points = append(g.points, ps...)
		return true
	case "LineString":
		ps, ok := geoJSONPoints(coords, 2)
		if !ok || !distinctNeighbours(ps) {
			return false
		}
		g.lines = append(g.lines, s2.Polyline(ps))
		return true
	case "MultiLineString":
		arr, ok := coords.([]interface{})
		if !ok || len(arr) == 0 {
			return false
		}
		for _, l := range arr {
			ps, ok := geoJSONPoints(l, 2)
			if !ok || !distinctNeighbours(ps) {
				return false
			}
			g.lines = append(g.lines, s2.Polyline(ps))
		}
		return true
	case "Polygon":
		return g.addPolygon([]interface{}{coords})
	case "MultiPolygon":
		arr, ok := coords.([]interface{})
		if !ok || len(arr) == 0 {
			return false
		}
		return g.addPolygon(arr)
	}
	return false
}

// addPolygon adds polygons (each a list of rings: shell, then holes).
// Each ring is normalised to the smaller region it bounds, and S2
// nests the loops (holes inside the shell).
func (g *geoShape) addPolygon(polys []interface{}) bool {
	for _, p := range polys {
		rings, ok := p.([]interface{})
		if !ok || len(rings) == 0 {
			return false
		}
		var loops []*s2.Loop
		for _, r := range rings {
			ps, ok := geoJSONPoints(r, 4)
			if !ok || !ps[0].ApproxEqual(ps[len(ps)-1]) {
				return false
			}
			ring := ps[:len(ps)-1] // drop the closing vertex
			if len(ring) < 3 || !distinctNeighbours(ps) {
				return false
			}
			l := s2.LoopFromPoints(ring)
			if l.Validate() != nil {
				return false
			}
			l.Normalize()
			loops = append(loops, l)
		}
		poly := s2.PolygonFromLoops(loops)
		if poly.Validate() != nil {
			return false
		}
		g.polys = append(g.polys, poly)
	}
	return true
}

func distinctNeighbours(ps []s2.Point) bool {
	for i := 1; i < len(ps); i++ {
		if ps[i] == ps[i-1] {
			return false
		}
	}
	return true
}

func geoJSONPoints(v interface{}, min int) ([]s2.Point, bool) {
	arr, ok := v.([]interface{})
	if !ok || len(arr) < min {
		return nil, false
	}
	out := make([]s2.Point, len(arr))
	for i, c := range arr {
		p, ok := geoJSONPoint(c)
		if !ok {
			return nil, false
		}
		out[i] = p
	}
	return out, true
}

func geoJSONPoint(v interface{}) (s2.Point, bool) {
	arr, ok := v.([]interface{})
	if !ok || len(arr) < 2 {
		return s2.Point{}, false
	}
	lng, ok1 := jsonFloat(arr[0])
	lat, ok2 := jsonFloat(arr[1])
	if !ok1 || !ok2 {
		return s2.Point{}, false
	}
	return geoPoint(lng, lat)
}

func jsonFloat(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	}
	return 0, false
}

// geoPoint is the S2 point of (longitude, latitude) in degrees, false
// when out of range.
func geoPoint(lng, lat float64) (s2.Point, bool) {
	if math.IsNaN(lng) || math.IsNaN(lat) || math.Abs(lng) > 180 || math.Abs(lat) > 90 {
		return s2.Point{}, false
	}
	return s2.PointFromLatLng(s2.LatLngFromDegrees(lat, lng)), true
}

// geoMeters converts an angle on the unit sphere to metres.
func geoMeters(a s1.Angle) float64 { return a.Radians() * geoSphereRadius }

// geoPointJSON is the GeoJSON Point of p.
func geoPointJSON(p s2.Point) string {
	ll := s2.LatLngFromPoint(p)
	return `{"type":"Point","coordinates":[` + strconv.FormatFloat(ll.Lng.Degrees(), 'g', 17, 64) + "," +
		strconv.FormatFloat(ll.Lat.Degrees(), 'g', 17, 64) + "]}"
}

// geoRingJSON / geoLineJSON format coordinate arrays.
func geoCoordsJSON(ps []s2.Point) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, p := range ps {
		if i > 0 {
			b.WriteByte(',')
		}
		ll := s2.LatLngFromPoint(p)
		fmt.Fprintf(&b, "[%s,%s]", strconv.FormatFloat(ll.Lng.Degrees(), 'g', 17, 64), strconv.FormatFloat(ll.Lat.Degrees(), 'g', 17, 64))
	}
	b.WriteByte(']')
	return b.String()
}

// polygonDistance is the distance from p to the nearest member
// polygon (0 inside), and the closest point on it.
func polygonDistance(g *geoShape, p s2.Point) (s1.Angle, s2.Point) {
	if g.containsPoint(p) {
		return 0, p
	}
	best := s1.InfAngle()
	var at s2.Point
	for _, poly := range g.polys {
		for _, l := range poly.Loops() {
			n := l.NumVertices()
			for i := 0; i < n; i++ {
				a, b := l.Vertex(i), l.Vertex((i+1)%n)
				if d := s2.DistanceFromSegment(p, a, b); d < best {
					best = d
					at = s2.Project(p, a, b)
				}
			}
		}
	}
	return best, at
}

// linesDistance is the distance from p to the nearest line, and the
// closest point on it.
func linesDistance(lines []s2.Polyline, p s2.Point) (s1.Angle, s2.Point) {
	best := s1.InfAngle()
	var at s2.Point
	for _, l := range lines {
		q, _ := l.Project(p)
		if d := p.Distance(q); d < best {
			best, at = d, q
		}
	}
	return best, at
}

func polylineLength(l s2.Polyline) s1.Angle {
	var t s1.Angle
	for i := 1; i < len(l); i++ {
		t += l[i-1].Distance(l[i])
	}
	return t
}
