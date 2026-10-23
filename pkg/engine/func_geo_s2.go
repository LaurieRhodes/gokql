package engine

// func_geo_s2.go — geo_* functions over points, lines and polygons on
// the sphere (see geo_shape.go for the model).

import (
	"fmt"
	"math"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	for name, t := range map[string]types.KQLType{
		"geo_point_in_circle":           types.TypeBool,
		"geo_point_in_polygon":          types.TypeBool,
		"geo_distance_point_to_line":    types.TypeReal,
		"geo_distance_point_to_polygon": types.TypeReal,
		"geo_polygon_area":              types.TypeReal,
		"geo_polygon_perimeter":         types.TypeReal,
		"geo_polygon_centroid":          types.TypeDynamic,
		"geo_line_length":               types.TypeReal,
		"geo_line_centroid":             types.TypeDynamic,
		"geo_line_interpolate_point":    types.TypeDynamic,
		"geo_line_locate_point":         types.TypeReal,
		"geo_closest_point_on_line":     types.TypeDynamic,
		"geo_closest_point_on_polygon":  types.TypeDynamic,
		"geo_angle":                     types.TypeReal,
		"geo_azimuth":                   types.TypeReal,
	} {
		scalarReturnTypes[name] = t
	}
}

// geoArgs evaluates fc's arguments; ok is false when any is null.
func geoArgs(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row, min, max int) ([]types.Value, bool, error) {
	if len(fc.Args) < min || len(fc.Args) > max {
		if min == max {
			return nil, false, fmt.Errorf("%s requires %d arguments", fc.Name, min)
		}
		return nil, false, fmt.Errorf("%s requires %d to %d arguments", fc.Name, min, max)
	}
	vals := make([]types.Value, len(fc.Args))
	for i, a := range fc.Args {
		v, err := evalExpr(eng, a, schema, row)
		if err != nil {
			return nil, false, err
		}
		if v == nil {
			return nil, false, nil
		}
		vals[i] = v
	}
	return vals, true, nil
}

// geoArgPoint reads (lon, lat) from vals[i], vals[i+1].
func geoArgPoint(vals []types.Value, i int) (s2.Point, bool) {
	lng, ok1 := geoCoord(vals[i])
	lat, ok2 := geoCoord(vals[i+1])
	if !ok1 || !ok2 {
		return s2.Point{}, false
	}
	return geoPoint(lng, lat)
}

func evalGeoS2(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "geo_point_in_circle":
		// (lon, lat, center_lon, center_lat, radius_m)
		vals, ok, err := geoArgs(eng, fc, schema, row, 5, 5)
		if !ok {
			return nil, true, err
		}
		p, ok1 := geoArgPoint(vals, 0)
		c, ok2 := geoArgPoint(vals, 2)
		r, ok3 := geoCoord(vals[4])
		if !ok1 || !ok2 || !ok3 || r < 0 {
			return nil, true, nil
		}
		return geoMeters(p.Distance(c)) <= r, true, nil

	case "geo_point_in_polygon":
		vals, ok, err := geoArgs(eng, fc, schema, row, 3, 3)
		if !ok {
			return nil, true, err
		}
		p, okp := geoArgPoint(vals, 0)
		g := parseGeoShape(vals[2])
		if !okp || g == nil || !g.isPolygonal() {
			return nil, true, nil
		}
		return g.containsPoint(p), true, nil

	case "geo_distance_point_to_line", "geo_closest_point_on_line":
		vals, ok, err := geoArgs(eng, fc, schema, row, 3, 3)
		if !ok {
			return nil, true, err
		}
		p, okp := geoArgPoint(vals, 0)
		g := parseGeoShape(vals[2])
		if !okp || g == nil || !g.isLinear() {
			return nil, true, nil
		}
		d, at := linesDistance(g.lines, p)
		if fc.Name == "geo_closest_point_on_line" {
			return geoPointJSON(at), true, nil
		}
		return geoMeters(d), true, nil

	case "geo_distance_point_to_polygon", "geo_closest_point_on_polygon":
		vals, ok, err := geoArgs(eng, fc, schema, row, 3, 3)
		if !ok {
			return nil, true, err
		}
		p, okp := geoArgPoint(vals, 0)
		g := parseGeoShape(vals[2])
		if !okp || g == nil || !g.isPolygonal() {
			return nil, true, nil
		}
		d, at := polygonDistance(g, p)
		if fc.Name == "geo_closest_point_on_polygon" {
			return geoPointJSON(at), true, nil
		}
		return geoMeters(d), true, nil

	case "geo_polygon_area", "geo_polygon_perimeter", "geo_polygon_centroid":
		vals, ok, err := geoArgs(eng, fc, schema, row, 1, 1)
		if !ok {
			return nil, true, err
		}
		g := parseGeoShape(vals[0])
		if g == nil || !g.isPolygonal() {
			return nil, true, nil
		}
		polys := g.polys
		if g.overlapping() {
			// Overlapping members measure as their union.
			u, ok := geoDestructiveUnion(g.polys)
			if !ok {
				return nil, true, nil
			}
			polys = []*s2.Polygon{u}
		}
		switch fc.Name {
		case "geo_polygon_area":
			a := 0.0
			for _, poly := range polys {
				a += poly.Area()
			}
			return a * geoSphereRadius * geoSphereRadius, true, nil
		case "geo_polygon_perimeter":
			var t s1.Angle
			for _, poly := range polys {
				for _, l := range poly.Loops() {
					n := l.NumVertices()
					for i := 0; i < n; i++ {
						t += l.Vertex(i).Distance(l.Vertex((i + 1) % n))
					}
				}
			}
			return geoMeters(t), true, nil
		default:
			var c s2.Point
			for _, poly := range polys {
				c = s2.Point{Vector: c.Add(poly.Centroid().Vector)}
			}
			if c.Norm() == 0 {
				return nil, true, nil
			}
			return geoPointJSON(s2.Point{Vector: c.Normalize()}), true, nil
		}

	case "geo_line_length", "geo_line_centroid":
		vals, ok, err := geoArgs(eng, fc, schema, row, 1, 1)
		if !ok {
			return nil, true, err
		}
		g := parseGeoShape(vals[0])
		if g == nil || !g.isLinear() {
			return nil, true, nil
		}
		if fc.Name == "geo_line_length" {
			var t s1.Angle
			for _, l := range g.lines {
				t += polylineLength(l)
			}
			return geoMeters(t), true, nil
		}
		var c s2.Point
		for _, l := range g.lines {
			c = s2.Point{Vector: c.Add(l.Centroid().Vector)}
		}
		if c.Norm() == 0 {
			return nil, true, nil
		}
		return geoPointJSON(s2.Point{Vector: c.Normalize()}), true, nil

	case "geo_line_interpolate_point":
		// (line, fraction in [0, 1]) along a single line
		vals, ok, err := geoArgs(eng, fc, schema, row, 2, 2)
		if !ok {
			return nil, true, err
		}
		g := parseGeoShape(vals[0])
		f, okf := geoCoord(vals[1])
		if g == nil || !g.isLinear() || len(g.lines) != 1 || !okf || f < 0 || f > 1 {
			return nil, true, nil
		}
		p, _ := g.lines[0].Interpolate(f)
		return geoPointJSON(p), true, nil

	case "geo_line_locate_point":
		// (line, lon, lat): the fraction along the line of the point
		// closest to (lon, lat)
		vals, ok, err := geoArgs(eng, fc, schema, row, 3, 3)
		if !ok {
			return nil, true, err
		}
		g := parseGeoShape(vals[0])
		p, okp := geoArgPoint(vals, 1)
		if g == nil || !g.isLinear() || len(g.lines) != 1 || !okp {
			return nil, true, nil
		}
		q, next := g.lines[0].Project(p)
		return g.lines[0].Uninterpolate(q, next), true, nil

	case "geo_angle":
		// (p1, p2, p3 as lon/lat pairs): the angle at p2, in radians
		// [0, 2π), measured clockwise from p1 to p3
		vals, ok, err := geoArgs(eng, fc, schema, row, 6, 6)
		if !ok {
			return nil, true, err
		}
		p1, ok1 := geoArgPoint(vals, 0)
		p2, ok2 := geoArgPoint(vals, 2)
		p3, ok3 := geoArgPoint(vals, 4)
		if !ok1 || !ok2 || !ok3 || p1 == p2 || p3 == p2 {
			return nil, true, nil
		}
		a := s2.TurnAngle(p1, p2, p3)
		return math.Pi - float64(a), true, nil

	case "geo_azimuth":
		// (lon1, lat1, lon2, lat2): the initial bearing from point 1 to
		// point 2, radians clockwise from north in [0, 2π)
		vals, ok, err := geoArgs(eng, fc, schema, row, 4, 4)
		if !ok {
			return nil, true, err
		}
		p1, ok1 := geoArgPoint(vals, 0)
		p2, ok2 := geoArgPoint(vals, 2)
		if !ok1 || !ok2 || p1 == p2 {
			return nil, true, nil
		}
		a := s2.LatLngFromPoint(p1)
		b := s2.LatLngFromPoint(p2)
		dl := float64(b.Lng - a.Lng)
		y := math.Sin(dl) * math.Cos(float64(b.Lat))
		x := math.Cos(float64(a.Lat))*math.Sin(float64(b.Lat)) - math.Sin(float64(a.Lat))*math.Cos(float64(b.Lat))*math.Cos(dl)
		az := math.Atan2(y, x)
		if az < 0 {
			az += 2 * math.Pi
		}
		return az, true, nil
	}
	return evalGeoS2b(eng, fc, schema, row)
}
