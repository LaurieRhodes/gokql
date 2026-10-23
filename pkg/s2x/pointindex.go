package s2x

// Closest-site queries for S2Builder, standing in for S2PointIndex plus
// S2ClosestPointQuery (golang/geo has neither). Queries are "conservative"
// in the C++ sense: they return every site whose computed distance is
// below r plus the maximum error of the distance computation, which is the
// candidate set S2Builder expects (it applies exact predicates itself).

import (
	"slices"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

// conservativeLimit is the max_distance used by
// set_conservative_max_distance(r): results have distance < limit.
func conservativeLimit(r s1.ChordAngle) s1.ChordAngle {
	return plusError(r, updateMinDistanceMaxError(r)).Successor()
}

// siteGrid supports incremental insertion and point-within-radius queries
// (S2Builder::ChooseInitialSites) by bucketing sites into cells of a level
// whose width is at least the query radius.
type siteGrid struct {
	level   int
	buckets map[s2.CellID][]int32
	sites   *[]s2.Point
	coverer *s2.RegionCoverer
	radius  s1.ChordAngle
	limit   s1.ChordAngle
}

func newSiteGrid(sites *[]s2.Point, radius s1.ChordAngle) *siteGrid {
	ang := radius.Angle()
	level := s2.MinWidthMetric.MaxLevel(float64(ang))
	if ang <= 0 {
		level = s2.MaxLevel
	}
	return &siteGrid{
		level:   level,
		buckets: map[s2.CellID][]int32{},
		sites:   sites,
		coverer: &s2.RegionCoverer{MinLevel: level, MaxLevel: level, MaxCells: 1 << 20},
		radius:  radius,
		limit:   conservativeLimit(radius),
	}
}

func (g *siteGrid) add(p s2.Point, id int32) {
	c := cellIDOf(p).Parent(g.level)
	g.buckets[c] = append(g.buckets[c], id)
}

// near returns the ids of sites within the conservative radius of p.
func (g *siteGrid) near(p s2.Point, out []int32) []int32 {
	out = out[:0]
	capR := g.limit.Angle() + 1e-15
	cp := s2.CapFromCenterAngle(p, capR)
	for _, c := range g.coverer.Covering(cp) {
		for _, id := range g.buckets[c] {
			if s2.ChordAngleBetweenPoints(p, (*g.sites)[id]) < g.limit {
				out = append(out, id)
			}
		}
	}
	return out
}

// siteTree is a static index of sites sorted by leaf cell id, queried by
// descending the cell hierarchy (S2Builder::CollectSiteEdges).
type siteTree struct {
	keys  []s2.CellID
	ids   []int32
	sites []s2.Point
}

func newSiteTree(sites []s2.Point) *siteTree {
	n := len(sites)
	ord := make([]int32, n)
	keys := make([]s2.CellID, n)
	for i := range ord {
		ord[i] = int32(i)
		keys[i] = cellIDOf(sites[i])
	}
	slices.SortFunc(ord, func(a, b int32) int {
		if keys[a] < keys[b] {
			return -1
		}
		if keys[a] > keys[b] {
			return 1
		}
		return int(a - b)
	})
	t := &siteTree{keys: make([]s2.CellID, n), ids: ord, sites: sites}
	for i, id := range ord {
		t.keys[i] = keys[id]
	}
	return t
}

func (t *siteTree) rangeOf(c s2.CellID) (int, int) {
	lo, _ := slices.BinarySearch(t.keys, c.RangeMin())
	hi, _ := slices.BinarySearch(t.keys, c.RangeMax()+1)
	return lo, hi
}

// nearEdge returns the ids of sites whose distance to edge ab is below the
// conservative limit for r.
func (t *siteTree) nearEdge(a, b s2.Point, r s1.ChordAngle, out []int32) []int32 {
	out = out[:0]
	limit := conservativeLimit(r)
	var visit func(c s2.CellID)
	visit = func(c s2.CellID) {
		lo, hi := t.rangeOf(c)
		if lo >= hi {
			return
		}
		if hi-lo <= 12 || c.IsLeaf() {
			for i := lo; i < hi; i++ {
				id := t.ids[i]
				d, _ := s2.UpdateMinDistance(t.sites[id], a, b, s1.InfChordAngle())
				if d < limit {
					out = append(out, id)
				}
			}
			return
		}
		cell := s2.CellFromCellID(c)
		if cell.DistanceToEdge(a, b) >= plusError(limit, 1e-13) {
			return
		}
		for _, ch := range c.Children() {
			visit(ch)
		}
	}
	for f := 0; f < 6; f++ {
		visit(s2.CellIDFromFace(f))
	}
	return out
}
