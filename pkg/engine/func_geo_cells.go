package engine

// func_geo_cells.go — geohash and S2 cell grid functions.

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	for name, t := range map[string]types.KQLType{
		"geo_point_to_geohash":         types.TypeString,
		"geo_geohash_to_central_point": types.TypeDynamic,
		"geo_geohash_to_polygon":       types.TypeDynamic,
		"geo_geohash_neighbors":        types.TypeDynamic,
		"geo_point_to_s2cell":          types.TypeString,
		"geo_s2cell_to_central_point":  types.TypeDynamic,
		"geo_s2cell_to_polygon":        types.TypeDynamic,
		"geo_s2cell_neighbors":         types.TypeDynamic,
		"geo_polygon_to_s2cells":       types.TypeDynamic,
		"geo_line_to_s2cells":          types.TypeDynamic,
	} {
		scalarReturnTypes[name] = t
	}
}

// maxS2Covering is the documented token limit for geo_*_to_s2cells.
const maxS2Covering = 65536

func evalGeoCells(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "geo_point_to_geohash", "geo_point_to_s2cell":
		// (lon, lat [, accuracy|level]); invalid input gives "".
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
		if fc.Name == "geo_point_to_geohash" {
			n := int64(5)
			if len(vals) == 3 {
				var okn bool
				if n, okn = geoInt(vals[2]); !okn || n < 1 || n > 18 {
					return "", true, nil
				}
			}
			return geohashEncode(lng, lat, int(n)), true, nil
		}
		level := int64(11)
		if len(vals) == 3 {
			var okl bool
			if level, okl = geoInt(vals[2]); !okl || level < 0 || level > 30 {
				return "", true, nil
			}
		}
		return s2.CellIDFromLatLng(s2.LatLngFromDegrees(lat, lng)).Parent(int(level)).ToToken(), true, nil

	case "geo_geohash_to_central_point", "geo_geohash_to_polygon", "geo_geohash_neighbors":
		vals, ok, err := geoArgs(eng, fc, schema, row, 1, 1)
		if !ok {
			return nil, true, err
		}
		s, _ := vals[0].(string)
		box, ok := geohashDecode(s)
		if !ok {
			return nil, true, nil
		}
		switch fc.Name {
		case "geo_geohash_to_central_point":
			return `{"type":"Point","coordinates":[` + geoNum((box[0]+box[2])/2) + "," + geoNum((box[1]+box[3])/2) + "]}", true, nil
		case "geo_geohash_to_polygon":
			ring := [][]float64{{box[0], box[1]}, {box[2], box[1]}, {box[2], box[3]}, {box[0], box[3]}, {box[0], box[1]}}
			return `{"type":"Polygon","coordinates":[` + rawCoordsJSON(ring) + "]}", true, nil
		}
		return jsonStringArray(geohashNeighbors(strings.ToLower(s), box)), true, nil

	case "geo_s2cell_to_central_point", "geo_s2cell_to_polygon", "geo_s2cell_neighbors":
		vals, ok, err := geoArgs(eng, fc, schema, row, 1, 1)
		if !ok {
			return nil, true, err
		}
		id, ok := s2Token(vals[0])
		if !ok {
			return nil, true, nil
		}
		switch fc.Name {
		case "geo_s2cell_to_central_point":
			return geoPointJSON(id.Point()), true, nil
		case "geo_s2cell_to_polygon":
			c := s2.CellFromCellID(id)
			ring := []s2.Point{c.Vertex(0), c.Vertex(1), c.Vertex(2), c.Vertex(3), c.Vertex(0)}
			return `{"type":"Polygon","coordinates":[` + geoCoordsJSON(ring) + "]}", true, nil
		}
		var out []string
		for _, n := range s2CellNeighbors(id) {
			out = append(out, n.ToToken())
		}
		return jsonStringArray(out), true, nil

	case "geo_polygon_to_s2cells", "geo_line_to_s2cells":
		// (shape [, level = 11 [, radius_m = 0]])
		vals, ok, err := geoArgs(eng, fc, schema, row, 1, 3)
		if !ok {
			return nil, true, err
		}
		level, radius := int64(11), 0.0
		if len(vals) >= 2 {
			var okl bool
			if level, okl = geoInt(vals[1]); !okl || level < 0 || level > 30 {
				return nil, true, nil
			}
		}
		if len(vals) == 3 {
			var okr bool
			if radius, okr = geoCoord(vals[2]); !okr || radius < 0 {
				return nil, true, nil
			}
		}
		g := parseGeoShape(vals[0])
		if g == nil {
			return nil, true, nil
		}
		var regions []s2.Region
		var idx *s2.ShapeIndex
		if radius > 0 {
			idx = s2.NewShapeIndex()
		}
		if fc.Name == "geo_polygon_to_s2cells" {
			if !g.isPolygonal() {
				return nil, true, nil
			}
			for _, p := range g.polys {
				regions = append(regions, p)
				if idx != nil {
					idx.Add(p)
				}
			}
		} else {
			if !g.isLinear() {
				return nil, true, nil
			}
			for i := range g.lines {
				l := &g.lines[i]
				regions = append(regions, l)
				if idx != nil {
					idx.Add(l)
				}
			}
		}
		cells, ok := s2FixedLevelCovering(regions, int(level), idx, radius)
		if !ok {
			return nil, true, nil
		}
		out := make([]string, len(cells))
		for i, c := range cells {
			out[i] = c.ToToken()
		}
		return jsonStringArray(out), true, nil
	}
	return evalGeoH3(eng, fc, schema, row)
}

func geoInt(v types.Value) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int32:
		return int64(x), true
	case int:
		return int64(x), true
	case float64:
		if x == math.Trunc(x) && !math.IsInf(x, 0) {
			return int64(x), true
		}
	}
	return 0, false
}

func geoNum(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func jsonStringArray(ss []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, s := range ss {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Quote(s))
	}
	b.WriteByte(']')
	return b.String()
}

// --- geohash ---

const geohashAlphabet = "0123456789bcdefghjkmnpqrstuvwxyz"

func geohashEncode(lng, lat float64, n int) string {
	minLng, maxLng, minLat, maxLat := -180.0, 180.0, -90.0, 90.0
	out := make([]byte, n)
	even := true
	for i := 0; i < n; i++ {
		ch := 0
		for bit := 4; bit >= 0; bit-- {
			if even {
				mid := (minLng + maxLng) / 2
				if lng >= mid {
					ch |= 1 << bit
					minLng = mid
				} else {
					maxLng = mid
				}
			} else {
				mid := (minLat + maxLat) / 2
				if lat >= mid {
					ch |= 1 << bit
					minLat = mid
				} else {
					maxLat = mid
				}
			}
			even = !even
		}
		out[i] = geohashAlphabet[ch]
	}
	return string(out)
}

// geohashDecode returns the cell's box as {minLng, minLat, maxLng, maxLat}.
func geohashDecode(s string) ([4]float64, bool) {
	if len(s) < 1 || len(s) > 18 {
		return [4]float64{}, false
	}
	minLng, maxLng, minLat, maxLat := -180.0, 180.0, -90.0, 90.0
	even := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		v := strings.IndexByte(geohashAlphabet, c)
		if v < 0 {
			return [4]float64{}, false
		}
		for bit := 4; bit >= 0; bit-- {
			on := v&(1<<bit) != 0
			if even {
				mid := (minLng + maxLng) / 2
				if on {
					minLng = mid
				} else {
					maxLng = mid
				}
			} else {
				mid := (minLat + maxLat) / 2
				if on {
					minLat = mid
				} else {
					maxLat = mid
				}
			}
			even = !even
		}
	}
	return [4]float64{minLng, minLat, maxLng, maxLat}, true
}

// geohashNeighbors returns the cells around box in ADX's order: SW, NW,
// SE, N, W, NE, E, S. Longitude wraps at the antimeridian; there are no
// neighbors past a pole.
func geohashNeighbors(h string, box [4]float64) []string {
	w, ht := box[2]-box[0], box[3]-box[1]
	cx, cy := (box[0]+box[2])/2, (box[1]+box[3])/2
	var out []string
	for _, d := range [][2]float64{{-1, -1}, {-1, 1}, {1, -1}, {0, 1}, {-1, 0}, {1, 1}, {1, 0}, {0, -1}} {
		y := cy + d[1]*ht
		if y < -90 || y > 90 {
			continue
		}
		x := cx + d[0]*w
		if x > 180 {
			x -= 360
		} else if x < -180 {
			x += 360
		}
		n := geohashEncode(x, y, len(h))
		if n != h {
			out = append(out, n)
		}
	}
	return out
}

// --- S2 ---

func s2Token(v types.Value) (s2.CellID, bool) {
	s, _ := v.(string)
	if s == "" || len(s) > 16 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte("0123456789abcdefABCDEF", s[i]) < 0 {
			return 0, false
		}
	}
	id := s2.CellIDFromToken(strings.ToLower(s))
	return id, id.IsValid()
}

// s2CellNeighbors returns the cell's eight neighbors (fewer at a cube
// corner) in ADX's order. AllNeighbors lists them by (i, j) offset as
// (-1,-1) (+1,-1) (0,-1) (0,+1) (-1,0) (+1,0) (-1,+1) (+1,+1); ADX
// starts at (+1,+1) and swaps the (-1,0)/(+1,0) pair.
func s2CellNeighbors(id s2.CellID) []s2.CellID {
	all := id.AllNeighbors(id.Level())
	if len(all) == 8 {
		all = []s2.CellID{all[7], all[0], all[1], all[2], all[3], all[5], all[4], all[6]}
	}
	seen := map[s2.CellID]bool{id: true}
	var out []s2.CellID
	for _, n := range all {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// s2FixedLevelCovering returns, sorted, every level-`level` cell that
// intersects one of the regions or, with radius > 0, lies within radius
// metres of the shapes in idx. Each region is connected, so its cells
// are found by flooding out from the cell of its first vertex; ok is
// false once more than maxS2Covering cells are found.
func s2FixedLevelCovering(regions []s2.Region, level int, idx *s2.ShapeIndex, radius float64) ([]s2.CellID, bool) {
	var near func(c s2.Cell) bool
	if idx != nil && radius > 0 {
		limit := s1.ChordAngleFromAngle(s1.Angle(radius / geoSphereRadius)).Successor()
		q := s2.NewClosestEdgeQuery(idx, s2.NewClosestEdgeQueryOptions().IncludeInteriors(true))
		near = func(c s2.Cell) bool { return q.IsDistanceLess(s2.NewMinDistanceToCellTarget(c), limit) }
	}
	set := map[s2.CellID]bool{}
	for _, r := range regions {
		var seed s2.Point
		switch x := r.(type) {
		case *s2.Polygon:
			if x.NumLoops() == 0 {
				continue
			}
			seed = x.Loop(0).Vertex(0)
		case *s2.Polyline:
			if len(*x) == 0 {
				continue
			}
			seed = (*x)[0]
		default:
			continue
		}
		hit := func(id s2.CellID) bool {
			c := s2.CellFromCellID(id)
			if near != nil {
				return near(c)
			}
			return r.IntersectsCell(c)
		}
		start := s2.CellIDFromLatLng(s2.LatLngFromPoint(seed)).Parent(level)
		visited := map[s2.CellID]bool{start: true}
		frontier := []s2.CellID{start}
		set[start] = true
		for len(frontier) > 0 {
			var next []s2.CellID
			for _, c := range frontier {
				for _, n := range c.AllNeighbors(level) {
					if visited[n] {
						continue
					}
					visited[n] = true
					if hit(n) {
						set[n] = true
						next = append(next, n)
						if len(set) > maxS2Covering {
							return nil, false
						}
					}
				}
			}
			frontier = next
		}
	}
	out := make([]s2.CellID, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, true
}
