package engine

// func_geo_ops.go — geospatial set operations and simplification, built on
// pkg/s2x (ports of S2BooleanOperation and S2Builder).

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/s2x"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	for _, name := range []string{
		"geo_union_polygons_array", "geo_union_lines_array",
		"geo_intersection_2polygons", "geo_intersection_2lines", "geo_intersection_line_with_polygon",
		"geo_polygon_simplify", "geo_line_simplify", "geo_simplify_polygons_array",
		"geo_point_buffer", "geo_line_buffer", "geo_polygon_buffer",
	} {
		scalarReturnTypes[name] = types.TypeDynamic
	}
}

// geoDefaultSimplifyTolerance is the documented default tolerance (metres).
const geoDefaultSimplifyTolerance = 10.0

// geoOpResult collects the output of a boolean operation.
type geoOpResult struct{ s2x.GeometryLayers }

func newGeoOpResult() *geoOpResult { return &geoOpResult{} }

func (r *geoOpResult) layers() []s2x.Layer { return r.Layers() }

// geoJSON renders the result as ADX does: a single geometry when only one
// kind is present, a GeometryCollection otherwise (empty when nothing is).
func (r *geoOpResult) geoJSON() string {
	var parts []string
	if js := geoPointsJSON(r.Points); js != "" {
		parts = append(parts, js)
	}
	if js := geoLinesJSON(r.Polylines); js != "" {
		parts = append(parts, js)
	}
	if p := r.Polygon; p != nil && p.NumLoops() > 0 {
		parts = append(parts, geoPolygonJSON(p))
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return `{"type":"GeometryCollection","geometries":[` + strings.Join(parts, ",") + "]}"
}

func geoPointsJSON(ps []s2.Point) string {
	switch len(ps) {
	case 0:
		return ""
	case 1:
		return geoPointJSON(ps[0])
	}
	return `{"type":"MultiPoint","coordinates":` + geoCoordsJSON(ps) + "}"
}

func geoLinesJSON(ls []s2.Polyline) string {
	switch len(ls) {
	case 0:
		return ""
	case 1:
		return `{"type":"LineString","coordinates":` + geoCoordsJSON(ls[0]) + "}"
	}
	var b strings.Builder
	b.WriteString(`{"type":"MultiLineString","coordinates":[`)
	for i, l := range ls {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(geoCoordsJSON(l))
	}
	b.WriteString("]}")
	return b.String()
}

// geoLoopRing is a loop's GeoJSON ring: oriented vertices (holes run
// clockwise) with the first vertex repeated.
func geoLoopRing(l *s2.Loop) []s2.Point {
	n := l.NumVertices()
	ring := make([]s2.Point, 0, n+1)
	for i := 0; i < n; i++ {
		ring = append(ring, l.OrientedVertex(i))
	}
	return append(ring, ring[0])
}

// geoPolygonJSON renders an S2Polygon: each shell with the holes directly
// inside it; several shells make a MultiPolygon.
func geoPolygonRings(p *s2.Polygon) [][]string {
	var polys [][]string
	for i := 0; i < p.NumLoops(); i++ {
		l := p.Loop(i)
		ring := geoCoordsJSON(geoLoopRing(l))
		if !l.IsHole() || len(polys) == 0 {
			polys = append(polys, []string{ring})
		} else {
			polys[len(polys)-1] = append(polys[len(polys)-1], ring)
		}
	}
	return polys
}

func geoPolygonJSON(p *s2.Polygon) string {
	polys := geoPolygonRings(p)
	if len(polys) == 1 {
		return `{"type":"Polygon","coordinates":[` + strings.Join(polys[0], ",") + "]}"
	}
	var b strings.Builder
	b.WriteString(`{"type":"MultiPolygon","coordinates":[`)
	for i, rings := range polys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("[" + strings.Join(rings, ",") + "]")
	}
	b.WriteString("]}")
	return b.String()
}

func geoIndexOf(polys []*s2.Polygon, lines []s2.Polyline) *s2.ShapeIndex {
	idx := s2.NewShapeIndex()
	for _, l := range lines {
		pl := l
		idx.Add(&pl)
	}
	for _, p := range polys {
		idx.Add(p)
	}
	return idx
}

// geoBoolean runs op on two indexes; polygon boundaries are closed (two
// polygons touching at a vertex intersect in that point).
func geoBoolean(op s2x.OpType, a, b *s2.ShapeIndex) (*geoOpResult, bool) {
	r := newGeoOpResult()
	o := s2x.DefaultBooleanOptions()
	o.PolygonModel = s2x.PolygonClosed
	if err := s2x.NewBooleanOperation(op, o, r.layers()...).Build(a, b); err != nil {
		return nil, false
	}
	return r, true
}

// geoArrayShapes parses a dynamic array of shapes.
func geoArrayShapes(v types.Value) ([]*geoShape, bool) {
	if _, ok := v.(string); !ok {
		return nil, false
	}
	arr, ok := parseJSONArray(v)
	if !ok {
		return nil, false
	}
	out := make([]*geoShape, 0, len(arr))
	for _, e := range arr {
		js, err := json.Marshal(e)
		if err != nil {
			return nil, false
		}
		g := parseGeoShape(string(js))
		if g == nil {
			return nil, false
		}
		out = append(out, g)
	}
	return out, true
}

func evalGeoOps(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "geo_union_polygons_array", "geo_union_lines_array":
		vals, ok, err := geoArgs(eng, fc, schema, row, 1, 1)
		if !ok {
			return nil, true, err
		}
		shapes, ok := geoArrayShapes(vals[0])
		if !ok || len(shapes) == 0 {
			return nil, true, nil
		}
		if fc.Name == "geo_union_polygons_array" {
			var polys []*s2.Polygon
			for _, g := range shapes {
				if !g.isPolygonal() {
					return nil, true, nil
				}
				polys = append(polys, g.polys...)
			}
			p, ok := geoDestructiveUnion(polys)
			if !ok {
				return nil, true, nil
			}
			return geoPolygonJSON(p), true, nil
		}
		var lines []s2.Polyline
		for _, g := range shapes {
			if !g.isLinear() {
				return nil, true, nil
			}
			lines = append(lines, g.lines...)
		}
		r, ok := geoBoolean(s2x.OpUnion, geoIndexOf(nil, lines), s2.NewShapeIndex())
		if !ok {
			return nil, true, nil
		}
		return r.geoJSON(), true, nil

	case "geo_intersection_2polygons", "geo_intersection_2lines", "geo_intersection_line_with_polygon":
		vals, ok, err := geoArgs(eng, fc, schema, row, 2, 2)
		if !ok {
			return nil, true, err
		}
		a, b := parseGeoShape(vals[0]), parseGeoShape(vals[1])
		if a == nil || b == nil {
			return nil, true, nil
		}
		switch fc.Name {
		case "geo_intersection_2polygons":
			if !a.isPolygonal() || !b.isPolygonal() {
				return nil, true, nil
			}
		case "geo_intersection_2lines":
			if !a.isLinear() || !b.isLinear() {
				return nil, true, nil
			}
		default:
			if !a.isLinear() || !b.isPolygonal() {
				return nil, true, nil
			}
		}
		r, ok := geoBoolean(s2x.OpIntersection, geoIndexOf(a.polys, a.lines), geoIndexOf(b.polys, b.lines))
		if !ok {
			return nil, true, nil
		}
		return r.geoJSON(), true, nil

	case "geo_simplify_polygons_array":
		vals, ok, err := geoArgs(eng, fc, schema, row, 1, 2)
		if !ok {
			return nil, true, err
		}
		tol := geoDefaultSimplifyTolerance
		if len(vals) == 2 {
			var okt bool
			if tol, okt = geoCoord(vals[1]); !okt || tol < 0 {
				return nil, true, nil
			}
		}
		shapes, ok := geoArrayShapes(vals[0])
		if !ok || len(shapes) == 0 {
			return nil, true, nil
		}
		for _, g := range shapes {
			if !g.isPolygonal() {
				return nil, true, nil
			}
		}
		out, ok := geoSimplifyPolygons(shapes, tol)
		if !ok {
			return nil, true, nil
		}
		return out, true, nil

	case "geo_polygon_simplify", "geo_line_simplify":
		vals, ok, err := geoArgs(eng, fc, schema, row, 1, 2)
		if !ok {
			return nil, true, err
		}
		tol := geoDefaultSimplifyTolerance
		if len(vals) == 2 {
			var okt bool
			if tol, okt = geoCoord(vals[1]); !okt || tol < 0 {
				return nil, true, nil
			}
		}
		g := parseGeoShape(vals[0])
		if g == nil {
			return nil, true, nil
		}
		if fc.Name == "geo_polygon_simplify" && !g.isPolygonal() || fc.Name == "geo_line_simplify" && !g.isLinear() {
			return nil, true, nil
		}
		out, ok := geoSimplify(g, tol)
		if !ok {
			return nil, true, nil
		}
		return out, true, nil

	case "geo_point_buffer":
		vals, ok, err := geoArgs(eng, fc, schema, row, 3, 4)
		if !ok {
			return nil, true, err
		}
		p, ok := geoArgPoint(vals, 0)
		if !ok {
			return nil, true, nil
		}
		out, ok := geoBuffer(vals[2:], func(op *s2x.BufferOperation) { op.AddPoint(p) })
		if !ok {
			return nil, true, nil
		}
		return out, true, nil

	case "geo_line_buffer", "geo_polygon_buffer":
		vals, ok, err := geoArgs(eng, fc, schema, row, 2, 3)
		if !ok {
			return nil, true, err
		}
		g := parseGeoShape(vals[0])
		if g == nil || fc.Name == "geo_line_buffer" && !g.isLinear() || fc.Name == "geo_polygon_buffer" && !g.isPolygonal() {
			return nil, true, nil
		}
		out, ok := geoBuffer(vals[1:], func(op *s2x.BufferOperation) {
			for _, pl := range g.lines {
				op.AddPolyline(pl)
			}
			for _, p := range g.polys {
				var loops [][]s2.Point
				for i := 0; i < p.NumLoops(); i++ {
					l := p.Loop(i)
					vs := make([]s2.Point, l.NumVertices())
					for j := range vs {
						vs[j] = l.OrientedVertex(j)
					}
					loops = append(loops, vs)
				}
				op.AddShape(s2x.NewLaxPolygon(loops))
			}
		})
		if !ok {
			return nil, true, nil
		}
		return out, true, nil
	}
	return nil, false, nil
}

// geoDefaultBufferTolerance is the tolerance (metres) the emulator uses
// when none is given; the docs say 0.1, but the emulator's output for an
// omitted tolerance matches 10 exactly.
const geoDefaultBufferTolerance = 10.0

// geoMaxBufferErrorFraction caps tolerance/radius. Any cap between about
// 0.14 and 0.99 gives the emulator's output (the vertex step is already at
// its 120-degree maximum there).
const geoMaxBufferErrorFraction = 0.5

// geoBuffer runs S2BufferOperation with radius vals[0] (metres) and
// optional tolerance vals[1] (metres, as the error fraction tol/radius).
// Per the emulator, the result is null for radius <= 0, a radius that
// rounds to 10,000 km or more, a tolerance fraction of at most 1e-6, or
// an empty result.
func geoBuffer(vals []types.Value, add func(*s2x.BufferOperation)) (string, bool) {
	r, ok := geoCoord(vals[0])
	if !ok || !(r > 0) || r >= 1e7-0.5 {
		return "", false
	}
	tol := geoDefaultBufferTolerance
	if len(vals) > 1 {
		if tol, ok = geoCoord(vals[1]); !ok || !(tol > 0) {
			return "", false
		}
	}
	frac := tol / r
	if !(frac > s2x.BufferMinErrorFraction) {
		return "", false
	}
	// A tolerance at or above the radius still gives a buffer (a triangle
	// for a point), not the degenerate C++ result for error fraction 1.
	frac = min(frac, geoMaxBufferErrorFraction)
	// The emulator's vertex spacing matches a single-precision fraction.
	frac = float64(float32(frac))
	opts := s2x.NewBufferOptions(s1.Angle(r / geoSphereRadius))
	opts.SetErrorFraction(frac)
	l := s2x.NewPolygonLayer()
	op := s2x.NewBufferOperation(l, opts)
	add(op)
	if op.Build() != nil || l.Polygon == nil || l.Polygon.NumLoops() == 0 {
		return "", false
	}
	return geoPolygonJSON(l.Polygon), true
}

// index returns the result as a shape index (for chaining operations).
func (r *geoOpResult) index() *s2.ShapeIndex {
	idx := s2.NewShapeIndex()
	if len(r.Points) > 0 {
		pv := s2.PointVector(r.Points)
		idx.Add(&pv)
	}
	for _, l := range r.Polylines {
		pl := l
		idx.Add(&pl)
	}
	if p := r.Polygon; p != nil && p.NumLoops() > 0 {
		idx.Add(p)
	}
	return idx
}

// geoSimplify snaps the shape with an identity snap function of the given
// radius (metres) and simplifies edge chains (S2Builder).
func geoSimplify(g *geoShape, tolMeters float64) (string, bool) {
	opts := s2x.NewBuilderOptions(s2x.IdentitySnapFunction{Radius: s1.Angle(tolMeters / geoSphereRadius)})
	opts.SetSimplifyEdgeChains(true)
	b := s2x.NewBuilder(opts)
	if g.isPolygonal() {
		l := s2x.NewPolygonLayer()
		b.StartLayer(l)
		for _, p := range g.polys {
			b.AddPolygon(p)
		}
		if b.Build() != nil || l.Polygon == nil || l.Polygon.NumLoops() == 0 {
			return "", false
		}
		return geoPolygonJSON(l.Polygon), true
	}
	l := s2x.NewPolylineVectorLayer()
	b.StartLayer(l)
	for _, pl := range g.lines {
		b.AddPolyline(pl)
	}
	if b.Build() != nil || len(l.Polylines) == 0 {
		return "", false
	}
	return geoLinesJSON(l.Polylines), true
}

// geoDestructiveUnion is the older S2Polygon::DestructiveApproxUnion
// that ADX's results match: a multimap from vertex count to polygon (equal
// keys in insertion order); the two first entries are unioned with snap
// radius S2::kIntersectionMergeRadius and the result is re-inserted under
// the sum of their keys, not its own vertex count. (Current S2 uses a
// priority queue on the true count, which starts output rings elsewhere.)
func geoDestructiveUnion(polys []*s2.Polygon) (*s2.Polygon, bool) {
	if len(polys) == 0 {
		return s2.PolygonFromLoops(nil), true
	}
	type item struct {
		p *s2.Polygon
		n int
	}
	var queue []item
	insert := func(it item) {
		i := sort.Search(len(queue), func(i int) bool { return queue[i].n > it.n })
		queue = append(queue, item{})
		copy(queue[i+1:], queue[i:])
		queue[i] = it
	}
	for _, p := range polys {
		insert(item{p, p.NumEdges()})
	}
	o := s2x.DefaultBooleanOptions()
	o.SnapFunction = s2x.IdentitySnapFunction{Radius: s2x.IntersectionMergeRadius}
	for len(queue) > 1 {
		a, b := queue[0], queue[1]
		queue = queue[2:]
		ia, ib := s2.NewShapeIndex(), s2.NewShapeIndex()
		ia.Add(a.p)
		ib.Add(b.p)
		l := s2x.NewPolygonLayer()
		if err := s2x.NewBooleanOperation(s2x.OpUnion, o, l).Build(ia, ib); err != nil {
			return nil, false
		}
		insert(item{l.Polygon, a.n + b.n})
	}
	return queue[0].p, true
}

// geoSimplifyPolygons simplifies several polygons together (one S2Builder
// layer each, so they snap to shared vertices); every result must be
// non-empty. One polygon gives a Polygon, several a MultiPolygon.
func geoSimplifyPolygons(shapes []*geoShape, tolMeters float64) (string, bool) {
	opts := s2x.NewBuilderOptions(s2x.IdentitySnapFunction{Radius: s1.Angle(tolMeters / geoSphereRadius)})
	opts.SetSimplifyEdgeChains(true)
	b := s2x.NewBuilder(opts)
	layers := make([]*s2x.PolygonLayer, len(shapes))
	for i, g := range shapes {
		layers[i] = s2x.NewPolygonLayer()
		b.StartLayer(layers[i])
		for _, p := range g.polys {
			b.AddPolygon(p)
		}
	}
	if b.Build() != nil {
		return "", false
	}
	if len(layers) == 1 {
		if layers[0].Polygon == nil || layers[0].Polygon.NumLoops() == 0 {
			return "", false
		}
		return geoPolygonJSON(layers[0].Polygon), true
	}
	var b2 strings.Builder
	b2.WriteString(`{"type":"MultiPolygon","coordinates":[`)
	n := 0
	for _, l := range layers {
		p := l.Polygon
		if p == nil || p.NumLoops() == 0 {
			return "", false
		}
		for _, rings := range geoPolygonRings(p) {
			if n > 0 {
				b2.WriteByte(',')
			}
			b2.WriteString("[" + strings.Join(rings, ",") + "]")
			n++
		}
	}
	b2.WriteString("]}")
	return b2.String(), true
}
