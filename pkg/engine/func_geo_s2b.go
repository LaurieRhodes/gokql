package engine

// func_geo_s2b.go — geo densify, intersects and WKT functions.

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/golang/geo/s2"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	for name, t := range map[string]types.KQLType{
		"geo_line_densify":                 types.TypeDynamic,
		"geo_polygon_densify":              types.TypeDynamic,
		"geo_intersects_2lines":            types.TypeBool,
		"geo_intersects_2polygons":         types.TypeBool,
		"geo_intersects_line_with_polygon": types.TypeBool,
		"geo_from_wkt":                     types.TypeDynamic,
	} {
		scalarReturnTypes[name] = t
	}
}

func evalGeoS2b(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "geo_line_densify", "geo_polygon_densify":
		// (shape [, tolerance_m = 10 [, preserve_crossing]])
		vals, ok, err := geoArgs(eng, fc, schema, row, 1, 3)
		if !ok {
			return nil, true, err
		}
		tol := 10.0
		if len(vals) >= 2 {
			t, okt := geoCoord(vals[1])
			if !okt || t < 0.1 || t > 10000 {
				return nil, true, nil
			}
			tol = t
		}
		polygon := fc.Name == "geo_polygon_densify"
		if g := parseGeoShape(vals[0]); g == nil || (polygon && !g.isPolygonal()) || (!polygon && !g.isLinear()) {
			return nil, true, nil
		}
		out, ok := densifyGeoJSON(vals[0], tol)
		if !ok {
			return nil, true, nil
		}
		return out, true, nil

	case "geo_intersects_2lines", "geo_intersects_2polygons", "geo_intersects_line_with_polygon":
		vals, ok, err := geoArgs(eng, fc, schema, row, 2, 2)
		if !ok {
			return nil, true, err
		}
		a, b := parseGeoShape(vals[0]), parseGeoShape(vals[1])
		if a == nil || b == nil {
			return nil, true, nil
		}
		switch fc.Name {
		case "geo_intersects_2lines":
			if !a.isLinear() || !b.isLinear() {
				return nil, true, nil
			}
			for _, la := range a.lines {
				for _, lb := range b.lines {
					if polylinesIntersect(la, lb) {
						return true, true, nil
					}
				}
			}
			return false, true, nil
		case "geo_intersects_2polygons":
			if !a.isPolygonal() || !b.isPolygonal() {
				return nil, true, nil
			}
			for _, pa := range a.polys {
				for _, pb := range b.polys {
					if pa.Intersects(pb) {
						return true, true, nil
					}
				}
			}
			return false, true, nil
		default:
			if !a.isLinear() || !b.isPolygonal() {
				return nil, true, nil
			}
			for _, l := range a.lines {
				for _, p := range b.polys {
					if polylineIntersectsPolygon(l, p) {
						return true, true, nil
					}
				}
			}
			return false, true, nil
		}

	case "geo_from_wkt":
		vals, ok, err := geoArgs(eng, fc, schema, row, 1, 1)
		if !ok {
			return nil, true, err
		}
		s, _ := vals[0].(string)
		out, ok := wktToGeoJSON(s)
		if !ok {
			return nil, true, nil
		}
		return out, true, nil
	}
	return evalGeoCells(eng, fc, schema, row)
}

func polylinesIntersect(a, b s2.Polyline) bool {
	for i := 1; i < len(a); i++ {
		for j := 1; j < len(b); j++ {
			if s2.CrossingSign(a[i-1], a[i], b[j-1], b[j]) != s2.DoNotCross {
				return true
			}
		}
	}
	return false
}

func polylineIntersectsPolygon(l s2.Polyline, p *s2.Polygon) bool {
	for _, v := range l {
		if p.ContainsPoint(v) {
			return true
		}
	}
	for _, loop := range p.Loops() {
		n := loop.NumVertices()
		for i := 1; i < len(l); i++ {
			for k := 0; k < n; k++ {
				if s2.CrossingSign(l[i-1], l[i], loop.Vertex(k), loop.Vertex((k+1)%n)) != s2.DoNotCross {
					return true
				}
			}
		}
	}
	return false
}

// densifyGeoJSON splits each planar (linear in longitude and latitude)
// edge of a LineString, MultiLineString, Polygon or MultiPolygon into
// 2^k equal planar pieces, the fewest for which every piece's midpoint
// lies within tol metres of the piece's geodesic. Input vertices are
// kept exactly.
func densifyGeoJSON(v types.Value, tol float64) (string, bool) {
	s, _ := v.(string)
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &obj) != nil {
		return "", false
	}
	var typ string
	if json.Unmarshal(obj["type"], &typ) != nil {
		return "", false
	}
	var coords string
	switch typ {
	case "LineString":
		var c [][]float64
		if json.Unmarshal(obj["coordinates"], &c) != nil {
			return "", false
		}
		coords = rawCoordsJSON(densifyRing(c, tol))
	case "MultiLineString", "Polygon":
		var c [][][]float64
		if json.Unmarshal(obj["coordinates"], &c) != nil {
			return "", false
		}
		parts := make([]string, len(c))
		for i, r := range c {
			parts[i] = rawCoordsJSON(densifyRing(r, tol))
		}
		coords = "[" + strings.Join(parts, ",") + "]"
	case "MultiPolygon":
		var c [][][][]float64
		if json.Unmarshal(obj["coordinates"], &c) != nil {
			return "", false
		}
		polys := make([]string, len(c))
		for i, p := range c {
			parts := make([]string, len(p))
			for j, r := range p {
				parts[j] = rawCoordsJSON(densifyRing(r, tol))
			}
			polys[i] = "[" + strings.Join(parts, ",") + "]"
		}
		coords = "[" + strings.Join(polys, ",") + "]"
	default:
		return "", false
	}
	return `{"type":"` + typ + `","coordinates":` + coords + "}", true
}

func densifyRing(c [][]float64, tol float64) [][]float64 {
	if len(c) < 2 {
		return c
	}
	out := [][]float64{c[0]}
	for i := 1; i < len(c); i++ {
		a, b := c[i-1], c[i]
		n := 1
		for n < 1<<20 && planarEdgeDeviation(a, b, n) > tol {
			n *= 2
		}
		for k := 1; k < n; k++ {
			f := float64(k) / float64(n)
			out = append(out, []float64{a[0] + (b[0]-a[0])*f, a[1] + (b[1]-a[1])*f})
		}
		out = append(out, b)
	}
	return out
}

// planarEdgeDeviation is the largest distance, in metres, between the
// planar midpoint of each of n equal pieces of a→b and that piece's
// geodesic.
func planarEdgeDeviation(a, b []float64, n int) float64 {
	worst := 0.0
	at := func(f float64) s2.Point {
		return s2.PointFromLatLng(s2.LatLngFromDegrees(a[1]+(b[1]-a[1])*f, a[0]+(b[0]-a[0])*f))
	}
	for k := 0; k < n; k++ {
		f0, f1 := float64(k)/float64(n), float64(k+1)/float64(n)
		d := geoMeters(s2.DistanceFromSegment(at((f0+f1)/2), at(f0), at(f1)))
		if d > worst {
			worst = d
		}
	}
	return worst
}

func rawCoordsJSON(c [][]float64) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, p := range c {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('[')
		b.WriteString(strconv.FormatFloat(p[0], 'g', -1, 64))
		b.WriteByte(',')
		b.WriteString(strconv.FormatFloat(p[1], 'g', -1, 64))
		b.WriteByte(']')
	}
	b.WriteByte(']')
	return b.String()
}

// --- WKT ---

// wktToGeoJSON converts Well-Known Text (POINT, LINESTRING, POLYGON,
// their MULTI forms and GEOMETRYCOLLECTION) to GeoJSON.
func wktToGeoJSON(s string) (string, bool) {
	w := &wktParser{s: s}
	out, ok := w.geometry()
	w.ws()
	if !ok || w.i != len(w.s) {
		return "", false
	}
	return out, true
}

type wktParser struct {
	s string
	i int
}

func (w *wktParser) ws() {
	for w.i < len(w.s) && (w.s[w.i] == ' ' || w.s[w.i] == '\t' || w.s[w.i] == '\n' || w.s[w.i] == '\r') {
		w.i++
	}
}

func (w *wktParser) word() string {
	w.ws()
	j := w.i
	for w.i < len(w.s) && ((w.s[w.i] >= 'A' && w.s[w.i] <= 'Z') || (w.s[w.i] >= 'a' && w.s[w.i] <= 'z')) {
		w.i++
	}
	return strings.ToUpper(w.s[j:w.i])
}

func (w *wktParser) eat(c byte) bool {
	w.ws()
	if w.i < len(w.s) && w.s[w.i] == c {
		w.i++
		return true
	}
	return false
}

func (w *wktParser) number() (float64, bool) {
	w.ws()
	j := w.i
	for w.i < len(w.s) && strings.IndexByte("+-.0123456789eE", w.s[w.i]) >= 0 {
		w.i++
	}
	f, err := strconv.ParseFloat(w.s[j:w.i], 64)
	return f, err == nil && !math.IsInf(f, 0)
}

// point reads "x y" as [x,y].
func (w *wktParser) point() (string, bool) {
	x, ok1 := w.number()
	y, ok2 := w.number()
	if !ok1 || !ok2 {
		return "", false
	}
	return "[" + strconv.FormatFloat(x, 'g', -1, 64) + "," + strconv.FormatFloat(y, 'g', -1, 64) + "]", true
}

// list reads "(" item, item ... ")".
func (w *wktParser) list(item func() (string, bool)) (string, bool) {
	if !w.eat('(') {
		return "", false
	}
	var parts []string
	for {
		p, ok := item()
		if !ok {
			return "", false
		}
		parts = append(parts, p)
		if w.eat(')') {
			return "[" + strings.Join(parts, ",") + "]", true
		}
		if !w.eat(',') {
			return "", false
		}
	}
}

func (w *wktParser) geometry() (string, bool) {
	kind := w.word()
	points := func() (string, bool) { return w.list(w.point) }
	rings := func() (string, bool) { return w.list(points) }
	var coords string
	var ok bool
	switch kind {
	case "POINT":
		if !w.eat('(') {
			return "", false
		}
		coords, ok = w.point()
		if !ok || !w.eat(')') {
			return "", false
		}
		return `{"type":"Point","coordinates":` + coords + "}", true
	case "LINESTRING":
		coords, ok = points()
		return `{"type":"LineString","coordinates":` + coords + "}", ok
	case "POLYGON":
		coords, ok = rings()
		return `{"type":"Polygon","coordinates":` + coords + "}", ok
	case "MULTIPOINT":
		// MULTIPOINT (1 1, 2 2) and MULTIPOINT ((1 1), (2 2))
		coords, ok = w.list(func() (string, bool) {
			if w.eat('(') {
				p, ok := w.point()
				return p, ok && w.eat(')')
			}
			return w.point()
		})
		return `{"type":"MultiPoint","coordinates":` + coords + "}", ok
	case "MULTILINESTRING":
		coords, ok = rings()
		return `{"type":"MultiLineString","coordinates":` + coords + "}", ok
	case "MULTIPOLYGON":
		coords, ok = w.list(rings)
		return `{"type":"MultiPolygon","coordinates":` + coords + "}", ok
	case "GEOMETRYCOLLECTION":
		var gs []string
		if !w.eat('(') {
			return "", false
		}
		for {
			g, ok := w.geometry()
			if !ok {
				return "", false
			}
			gs = append(gs, g)
			if w.eat(')') {
				return `{"type":"GeometryCollection","geometries":[` + strings.Join(gs, ",") + "]}", true
			}
			if !w.eat(',') {
				return "", false
			}
		}
	}
	return "", false
}
