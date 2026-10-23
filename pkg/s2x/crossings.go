package s2x

import "github.com/golang/geo/s2"

// visitInteriorCrossings calls f once for every pair of edges in the
// index (all shapes) that cross at a point interior to both
// (s2shapeutil::VisitCrossingEdgePairs with CrossingType::INTERIOR).
func visitInteriorCrossings(idx *s2.ShapeIndex, f func(a, b s2.Edge)) {
	q := s2.NewCrossingEdgeQuery(idx)
	type key struct{ s, e int32 }
	for sid := int32(0); sid < int32(idx.Len()); sid++ {
		shA := idx.Shape(sid)
		if shA == nil {
			continue
		}
		for ea := 0; ea < shA.NumEdges(); ea++ {
			a := shA.Edge(ea)
			for sb := sid; sb < int32(idx.Len()); sb++ {
				shB := idx.Shape(sb)
				if shB == nil {
					continue
				}
				for _, eb := range q.Crossings(a.V0, a.V1, shB, s2.CrossingTypeInterior) {
					if sb == sid && eb <= ea {
						continue
					}
					f(a, shB.Edge(eb))
				}
			}
		}
	}
}

// visitCrossingEdgePairs calls f for every pair (a from index A, b from
// index B) of edges that cross or share a vertex
// (s2shapeutil::VisitCrossingEdgePairs with CrossingType::ALL). isInterior
// reports a crossing interior to both edges. f returns false to stop.
func visitCrossingEdgePairs(ai, bi *s2.ShapeIndex, f func(a, b shapeEdge, isInterior bool) bool) bool {
	q := s2.NewCrossingEdgeQuery(bi)
	na, nb := numShapeIDs(ai), numShapeIDs(bi)
	for sa := int32(0); sa < na; sa++ {
		shA := ai.Shape(sa)
		if shA == nil {
			continue
		}
		for ea := 0; ea < shA.NumEdges(); ea++ {
			e := shA.Edge(ea)
			a := shapeEdge{shapeEdgeID{sa, int32(ea)}, e.V0, e.V1}
			for sb := int32(0); sb < nb; sb++ {
				shB := bi.Shape(sb)
				if shB == nil {
					continue
				}
				for _, eb := range q.Crossings(e.V0, e.V1, shB, s2.CrossingTypeAll) {
					be := shB.Edge(eb)
					b := shapeEdge{shapeEdgeID{sb, int32(eb)}, be.V0, be.V1}
					interior := s2.CrossingSign(a.v0, a.v1, b.v0, b.v1) == s2.Cross
					if !f(a, b, interior) {
						return false
					}
				}
			}
		}
	}
	return true
}

// visitIncidentEdges calls f for every edge of the index with p as an
// endpoint (S2ContainsPointQuery::VisitIncidentEdges).
func visitIncidentEdges(idx *s2.ShapeIndex, p s2.Point, f func(e shapeEdge)) {
	q := s2.NewCrossingEdgeQuery(idx)
	n := numShapeIDs(idx)
	for s := int32(0); s < n; s++ {
		sh := idx.Shape(s)
		if sh == nil {
			continue
		}
		for _, e := range q.Crossings(p, p, sh, s2.CrossingTypeAll) {
			ed := sh.Edge(e)
			if ed.V0 == p || ed.V1 == p {
				f(shapeEdge{shapeEdgeID{s, int32(e)}, ed.V0, ed.V1})
			}
		}
	}
}
