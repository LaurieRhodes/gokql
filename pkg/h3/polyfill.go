package h3

// Port of polygonToCells from H3 v4.1 (algos.c, bbox.c, polygon.c):
// cells whose centres fall inside a lat/lng polygon, with edges taken as
// straight lines in lat/lng space.

import "math"

// GeoPolygon is an outer loop with optional holes (radians, unclosed).
type GeoPolygon struct {
	Outer []LatLng
	Holes [][]LatLng
}

type bbox struct{ north, south, east, west float64 }

const earthRadiusKm = 6371.007180918475

const polygonToCellsBuffer = 12

func (b bbox) transmeridian() bool { return b.east < b.west }

func (b bbox) contains(p LatLng) bool {
	if p.Lat < b.south || p.Lat > b.north {
		return false
	}
	if b.transmeridian() {
		return p.Lng >= b.west || p.Lng <= b.east
	}
	return p.Lng >= b.west && p.Lng <= b.east
}

func bboxFromLoop(loop []LatLng) bbox {
	if len(loop) == 0 {
		return bbox{}
	}
	b := bbox{south: math.MaxFloat64, west: math.MaxFloat64, north: -math.MaxFloat64, east: -math.MaxFloat64}
	minPosLng, maxNegLng := math.MaxFloat64, -math.MaxFloat64
	trans := false
	for i, c := range loop {
		next := loop[(i+1)%len(loop)]
		b.south = math.Min(b.south, c.Lat)
		b.west = math.Min(b.west, c.Lng)
		b.north = math.Max(b.north, c.Lat)
		b.east = math.Max(b.east, c.Lng)
		if c.Lng > 0 && c.Lng < minPosLng {
			minPosLng = c.Lng
		}
		if c.Lng < 0 && c.Lng > maxNegLng {
			maxNegLng = c.Lng
		}
		if math.Abs(c.Lng-next.Lng) > math.Pi {
			trans = true
		}
	}
	if trans {
		b.east, b.west = maxNegLng, minPosLng
	}
	return b
}

func normalizeLng(lng float64, trans bool) float64 {
	if trans && lng < 0 {
		return lng + 2*math.Pi
	}
	return lng
}

func pointInsideLoop(loop []LatLng, b bbox, p LatLng) bool {
	if !b.contains(p) {
		return false
	}
	trans := b.transmeridian()
	contains := false
	lat := p.Lat
	lng := normalizeLng(p.Lng, trans)
	for i := range loop {
		a, c := loop[i], loop[(i+1)%len(loop)]
		if a.Lat > c.Lat {
			a, c = c, a
		}
		if lat == a.Lat || lat == c.Lat {
			lat += 2.220446049250313e-16
		}
		if lat < a.Lat || lat > c.Lat {
			continue
		}
		aLng := normalizeLng(a.Lng, trans)
		cLng := normalizeLng(c.Lng, trans)
		if aLng == lng || cLng == lng {
			lng -= 2.220446049250313e-16
		}
		ratio := (lat - a.Lat) / (c.Lat - a.Lat)
		testLng := normalizeLng(aLng+(cLng-aLng)*ratio, trans)
		if testLng > lng {
			contains = !contains
		}
	}
	return contains
}

func pointInsidePolygon(p *GeoPolygon, boxes []bbox, ll LatLng) bool {
	if !pointInsideLoop(p.Outer, boxes[0], ll) {
		return false
	}
	for i, h := range p.Holes {
		if pointInsideLoop(h, boxes[i+1], ll) {
			return false
		}
	}
	return true
}

// GreatCircleDistanceRads is the haversine distance between a and b.
func GreatCircleDistanceRads(a, b LatLng) float64 {
	sinLat := math.Sin((b.Lat - a.Lat) / 2)
	sinLng := math.Sin((b.Lng - a.Lng) / 2)
	A := sinLat*sinLat + math.Cos(a.Lat)*math.Cos(b.Lat)*sinLng*sinLng
	return 2 * math.Atan2(math.Sqrt(A), math.Sqrt(1-A))
}

func pentagonRadiusKm(res int) float64 {
	p := setH3Index(res, 4, 0) // first pentagon of the resolution
	return GreatCircleDistanceRads(p.LatLng(), p.Boundary()[0]) * earthRadiusKm
}

func bboxHexEstimate(b bbox, res int) (int, bool) {
	pr := pentagonRadiusKm(res)
	area := 0.8 * (2.59807621135 * pr * pr)
	p1 := LatLng{b.north, b.east}
	p2 := LatLng{b.south, b.west}
	d := GreatCircleDistanceRads(p1, p2) * earthRadiusKm
	lngDiff := math.Abs(p1.Lng - p2.Lng)
	latDiff := math.Abs(p1.Lat - p2.Lat)
	if lngDiff == 0 || latDiff == 0 {
		return 0, false
	}
	ratio := math.Max(lngDiff, latDiff) / math.Min(lngDiff, latDiff)
	a := d * d / math.Min(3, ratio)
	est := math.Ceil(a / area)
	if math.IsInf(est, 0) || math.IsNaN(est) {
		return 0, false
	}
	if est == 0 {
		est = 1
	}
	return int(est), true
}

func lineHexEstimate(o, d LatLng, res int) (int, bool) {
	pr := pentagonRadiusKm(res)
	dist := GreatCircleDistanceRads(o, d) * earthRadiusKm
	c := math.Ceil(dist / (2 * pr))
	if math.IsInf(c, 0) || math.IsNaN(c) {
		return 0, false
	}
	if c == 0 {
		c = 1
	}
	return int(c), true
}

// MaxPolygonToCellsSize is the hash-table size polygonToCells uses.
func MaxPolygonToCellsSize(p *GeoPolygon, res int) (int, bool) {
	n, ok := bboxHexEstimate(bboxFromLoop(p.Outer), res)
	if !ok {
		return 0, false
	}
	total := len(p.Outer)
	for _, h := range p.Holes {
		total += len(h)
	}
	if n < total {
		n = total
	}
	return n + polygonToCellsBuffer, true
}

func getEdgeHexagons(loop []LatLng, numHex, res int, search, found []Cell, nSearch *int) bool {
	for i, o := range loop {
		d := loop[(i+1)%len(loop)]
		est, ok := lineHexEstimate(o, d, res)
		if !ok {
			return false
		}
		for j := 0; j < est; j++ {
			var p LatLng
			p.Lat = o.Lat*float64(est-j)/float64(est) + d.Lat*float64(j)/float64(est)
			p.Lng = o.Lng*float64(est-j)/float64(est) + d.Lng*float64(j)/float64(est)
			h, err := LatLngToCell(p, res)
			if err != nil {
				return false
			}
			loc := int(uint64(h) % uint64(numHex))
			loops := 0
			for found[loc] != 0 {
				if loops > numHex {
					return false
				}
				if found[loc] == h {
					break
				}
				loc = (loc + 1) % numHex
				loops++
			}
			if found[loc] == h {
				continue
			}
			found[loc] = h
			search[*nSearch] = h
			*nSearch++
		}
	}
	return true
}

// PolygonToCells returns the cells at res whose centres lie inside p, in
// the order of H3 v4.1's output hash table. ok is false when the
// estimate fails or the table overflows.
func PolygonToCells(p *GeoPolygon, res int) ([]Cell, bool) {
	if res < 0 || res > MaxRes || len(p.Outer) == 0 {
		return nil, false
	}
	boxes := []bbox{bboxFromLoop(p.Outer)}
	for _, h := range p.Holes {
		boxes = append(boxes, bboxFromLoop(h))
	}
	numHex, ok := MaxPolygonToCellsSize(p, res)
	if !ok {
		return nil, false
	}
	return polygonToCellsN(p, res, boxes, numHex)
}

func polygonToCellsN(p *GeoPolygon, res int, boxes []bbox, numHex int) ([]Cell, bool) {
	search := make([]Cell, numHex)
	found := make([]Cell, numHex)
	out := make([]Cell, numHex)
	nSearch := 0
	if !getEdgeHexagons(p.Outer, numHex, res, search, found, &nSearch) {
		return nil, false
	}
	for _, h := range p.Holes {
		if !getEdgeHexagons(h, numHex, res, search, found, &nSearch) {
			return nil, false
		}
	}
	for i := range found {
		found[i] = 0
	}
	nFound := 0
	for nSearch > 0 {
		for i := 0; i < nSearch; i++ {
			ring, _ := search[i].GridDisk(1)
			for _, hex := range ring {
				if hex == 0 {
					continue
				}
				loc := int(uint64(hex) % uint64(numHex))
				loops := 0
				for out[loc] != 0 {
					if loops > numHex {
						return nil, false
					}
					if out[loc] == hex {
						break
					}
					loc = (loc + 1) % numHex
					loops++
				}
				if out[loc] == hex {
					continue
				}
				if !pointInsidePolygon(p, boxes, hex.LatLng()) {
					continue
				}
				out[loc] = hex
				if nFound >= numHex {
					return nil, false
				}
				found[nFound] = hex
				nFound++
			}
		}
		search, found = found, search
		for j := 0; j < nSearch; j++ {
			found[j] = 0
		}
		nSearch, nFound = nFound, 0
	}
	res2 := make([]Cell, 0, numHex)
	for _, c := range out {
		if c != 0 {
			res2 = append(res2, c)
		}
	}
	return res2, true
}
