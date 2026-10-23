package engine

// func_geo_h3.go — H3 cell functions, backed by pkg/h3 (a Go port of
// Uber's H3 v4.5.0).

import (
	"encoding/json"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/h3"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	for name, t := range map[string]types.KQLType{
		"geo_point_to_h3cell":         types.TypeString,
		"geo_h3cell_to_central_point": types.TypeDynamic,
		"geo_h3cell_to_polygon":       types.TypeDynamic,
		"geo_h3cell_level":            types.TypeInt,
		"geo_h3cell_parent":           types.TypeString,
		"geo_h3cell_children":         types.TypeDynamic,
		"geo_h3cell_neighbors":        types.TypeDynamic,
		"geo_h3cell_rings":            types.TypeDynamic,
		"geo_polygon_to_h3cells":      types.TypeDynamic,
	} {
		scalarReturnTypes[name] = t
	}
}

const (
	h3MaxChildDepth = 5   // geo_h3cell_children: at most 7^5 children per cell
	h3MaxRingK      = 142 // geo_h3cell_rings: largest accepted distance
)

// h3Arg reads an H3 cell token. ADX parses it leniently as hex: case,
// surrounding blanks, a leading '+' or "0x" are all accepted.
func h3Arg(v types.Value) (h3.Cell, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "+")
	if len(s) > 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		s = s[2:]
	}
	if s == "" || len(s) > 16 {
		return 0, false
	}
	return h3.ParseCell(s)
}

func h3CellsJSON(cs []h3.Cell) string {
	ss := make([]string, len(cs))
	for i, c := range cs {
		ss[i] = c.String()
	}
	return jsonStringArray(ss)
}

func evalGeoH3(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "geo_point_to_h3cell":
		// (lon, lat [, resolution = 6]); invalid input gives "".
		vals, ok, err := geoArgs(eng, fc, schema, row, 2, 3)
		if !ok {
			if err != nil {
				return nil, true, err
			}
			return "", true, nil
		}
		lng, ok1 := geoCoord(vals[0])
		lat, ok2 := geoCoord(vals[1])
		if !ok1 || !ok2 || lng < -180 || lng > 180 || lat < -90 || lat > 90 {
			return "", true, nil
		}
		res := int64(6)
		if len(vals) == 3 {
			var okr bool
			if res, okr = geoInt(vals[2]); !okr || res < 0 || res > h3.MaxRes {
				return "", true, nil
			}
		}
		c, err := h3.LatLngToCell(h3.LatLng{Lat: h3.DegsToRads(lat), Lng: h3.DegsToRads(lng)}, int(res))
		if err != nil {
			return "", true, nil
		}
		return c.String(), true, nil

	case "geo_h3cell_to_central_point", "geo_h3cell_to_polygon", "geo_h3cell_level", "geo_h3cell_neighbors":
		vals, ok, err := geoArgs(eng, fc, schema, row, 1, 1)
		if !ok {
			return nil, true, err
		}
		c, ok := h3Arg(vals[0])
		if !ok {
			return nil, true, nil
		}
		switch fc.Name {
		case "geo_h3cell_level":
			return int32(c.Resolution()), true, nil
		case "geo_h3cell_to_central_point":
			g := c.LatLng()
			return `{"type":"Point","coordinates":[` + geoNum(h3Deg(g.Lng)) + "," + geoNum(h3Deg(g.Lat)) + "]}", true, nil
		case "geo_h3cell_to_polygon":
			b := c.Boundary()
			ring := make([][]float64, 0, len(b)+1)
			for _, p := range b {
				ring = append(ring, []float64{h3Deg(p.Lng), h3Deg(p.Lat)})
			}
			ring = append(ring, ring[0])
			return `{"type":"Polygon","coordinates":[` + rawCoordsJSON(ring) + "]}", true, nil
		}
		// Neighbors: the ring at distance 1, in H3's disk order.
		ring, err := c.GridRing(1)
		if err != nil {
			return nil, true, nil
		}
		return h3CellsJSON(ring), true, nil

	case "geo_h3cell_parent", "geo_h3cell_children":
		vals, ok, err := geoArgs(eng, fc, schema, row, 1, 2)
		if !ok {
			if err != nil || fc.Name == "geo_h3cell_children" {
				return nil, true, err
			}
			return "", true, nil
		}
		null := types.Value(nil)
		if fc.Name == "geo_h3cell_parent" {
			null = ""
		}
		c, ok := h3Arg(vals[0])
		if !ok {
			return null, true, nil
		}
		res := c.Resolution()
		target := int64(res - 1)
		if fc.Name == "geo_h3cell_children" {
			target = int64(res + 1)
		}
		if len(vals) == 2 {
			var okr bool
			if target, okr = geoInt(vals[1]); !okr {
				return null, true, nil
			}
		}
		if fc.Name == "geo_h3cell_parent" {
			if target < 0 || target >= int64(res) {
				return "", true, nil
			}
			p, _ := c.Parent(int(target))
			return p.String(), true, nil
		}
		if target <= int64(res) || target > h3.MaxRes || target-int64(res) > h3MaxChildDepth {
			return nil, true, nil
		}
		ch, _ := c.Children(int(target))
		return h3CellsJSON(ch), true, nil

	case "geo_h3cell_rings":
		vals, ok, err := geoArgs(eng, fc, schema, row, 2, 2)
		if !ok {
			return nil, true, err
		}
		c, ok := h3Arg(vals[0])
		if !ok {
			return nil, true, nil
		}
		k, ok := geoInt(vals[1])
		if !ok || k < 0 || k > h3MaxRingK {
			return nil, true, nil
		}
		cells, dist, err := c.GridDiskDistances(int(k))
		if err != nil {
			return nil, true, nil
		}
		rings := make([][]h3.Cell, k+1)
		for i, cc := range cells {
			rings[dist[i]] = append(rings[dist[i]], cc)
		}
		var b strings.Builder
		b.WriteByte('[')
		for i, r := range rings {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(h3CellsJSON(r))
		}
		b.WriteByte(']')
		return b.String(), true, nil

	case "geo_polygon_to_h3cells":
		// (polygon [, resolution = 6 [, radius_m = 0]]); no cells gives null.
		vals, ok, err := geoArgs(eng, fc, schema, row, 1, 3)
		if !ok {
			return nil, true, err
		}
		res, radius := int64(6), 0.0
		if len(vals) >= 2 {
			var okr bool
			if res, okr = geoInt(vals[1]); !okr || res < 0 || res > h3.MaxRes {
				return nil, true, nil
			}
		}
		if len(vals) == 3 {
			var okr bool
			if radius, okr = geoCoord(vals[2]); !okr || radius < 0 {
				return nil, true, nil
			}
		}
		cells, ok := h3PolygonCells(vals[0], int(res), radius)
		if !ok || len(cells) == 0 {
			return nil, true, nil
		}
		return h3CellsJSON(cells), true, nil
	}
	return evalGeoOps(eng, fc, schema, row)
}

// h3MaxPolygonCells bounds geo_polygon_to_h3cells' output.
const h3MaxPolygonCells = 1 << 20

// h3GeoPolygons reads the loops of a GeoJSON Polygon or MultiPolygon
// as H3 polygons (radians, closing vertex dropped).
func h3GeoPolygons(s string) ([]h3.GeoPolygon, bool) {
	var obj struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if json.Unmarshal([]byte(s), &obj) != nil {
		return nil, false
	}
	var polys [][][][]float64
	switch obj.Type {
	case "Polygon":
		var p [][][]float64
		if json.Unmarshal(obj.Coordinates, &p) != nil {
			return nil, false
		}
		polys = [][][][]float64{p}
	case "MultiPolygon":
		if json.Unmarshal(obj.Coordinates, &polys) != nil {
			return nil, false
		}
	default:
		return nil, false
	}
	var out []h3.GeoPolygon
	for _, p := range polys {
		var gp h3.GeoPolygon
		for i, ring := range p {
			if len(ring) < 4 {
				return nil, false
			}
			loop := make([]h3.LatLng, 0, len(ring)-1)
			for _, c := range ring[:len(ring)-1] {
				if len(c) < 2 {
					return nil, false
				}
				loop = append(loop, h3.LatLng{Lat: h3.DegsToRads(c[1]), Lng: h3.DegsToRads(c[0])})
			}
			if i == 0 {
				gp.Outer = loop
			} else {
				gp.Holes = append(gp.Holes, loop)
			}
		}
		out = append(out, gp)
	}
	return out, len(out) > 0
}

// h3PolygonCells returns the cells whose centres lie in the polygon(s)
// (H3 polygonToCells, edges straight in lat/lng) or, with radius > 0,
// within radius metres of them.
func h3PolygonCells(v types.Value, res int, radius float64) ([]h3.Cell, bool) {
	g := parseGeoShape(v)
	if g == nil || !g.isPolygonal() {
		return nil, false
	}
	s, _ := v.(string)
	polys, ok := h3GeoPolygons(s)
	if !ok {
		return nil, false
	}
	seen := map[h3.Cell]bool{}
	var out []h3.Cell
	for i := range polys {
		cs, ok := h3.PolygonToCells(&polys[i], res)
		if !ok {
			return nil, false
		}
		// ADX lists the hash table back to front.
		for j := len(cs) - 1; j >= 0; j-- {
			if !seen[cs[j]] {
				seen[cs[j]] = true
				out = append(out, cs[j])
			}
		}
		if len(out) > h3MaxPolygonCells {
			return nil, false
		}
	}
	if radius <= 0 {
		return out, true
	}
	idx := s2.NewShapeIndex()
	for _, p := range g.polys {
		idx.Add(p)
	}
	limit := s1.ChordAngleFromAngle(s1.Angle(radius / geoSphereRadius)).Successor()
	q := s2.NewClosestEdgeQuery(idx, s2.NewClosestEdgeQueryOptions().IncludeInteriors(true))
	near := func(c h3.Cell) bool {
		ll := c.LatLng()
		p := s2.PointFromLatLng(s2.LatLng{Lat: s1.Angle(ll.Lat), Lng: s1.Angle(ll.Lng)})
		return q.IsDistanceLess(s2.NewMinDistanceToPointTarget(p), limit)
	}
	visited := map[h3.Cell]bool{}
	frontier := append([]h3.Cell(nil), out...)
	for _, c := range out {
		visited[c] = true
	}
	for i := range polys {
		for _, ll := range polys[i].Outer {
			c, err := h3.LatLngToCell(ll, res)
			if err == nil && !visited[c] {
				visited[c] = true
				frontier = append(frontier, c)
				if near(c) {
					seen[c] = true
					out = append(out, c)
				}
			}
		}
	}
	for len(frontier) > 0 {
		var next []h3.Cell
		for _, c := range frontier {
			ring, err := c.GridRing(1)
			if err != nil {
				continue
			}
			for _, n := range ring {
				if visited[n] {
					continue
				}
				visited[n] = true
				if near(n) {
					if !seen[n] {
						seen[n] = true
						out = append(out, n)
						if len(out) > h3MaxPolygonCells {
							return nil, false
						}
					}
					next = append(next, n)
				}
			}
		}
		frontier = next
	}
	return out, true
}

func h3Deg(rad float64) float64 { return h3.RadsToDegs(rad) }
