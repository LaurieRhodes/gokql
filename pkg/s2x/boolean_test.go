package s2x

import (
	"fmt"

	"github.com/golang/geo/r3"
	"github.com/golang/geo/s1"
	"sort"
	"strings"
	"testing"

	"github.com/golang/geo/s2"
)

const kVertexTestPolygonStr = "0:0, 0:1, 0:2, 0:3, 0:4, 0:5, 5:5, 5:4, 5:3, 5:2, 5:1, 5:0"

func splitTrim(s string, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// makeLaxPolygon is s2textformat::MakeLaxPolygon.
func makeLaxPolygon(s string) *LaxPolygon {
	s = strings.TrimSpace(s)
	if s == "empty" {
		return NewLaxPolygon(nil)
	}
	var loops [][]s2.Point
	for _, ls := range strings.Split(s, ";") {
		ls = strings.TrimSpace(ls)
		if ls == "full" {
			loops = append(loops, []s2.Point{})
			continue
		}
		loops = append(loops, parsePoints(ls))
	}
	return NewLaxPolygon(loops)
}

// makeIndex is s2textformat::MakeIndex: "points # polylines # polygons".
func makeIndex(s string) *s2.ShapeIndex {
	parts := strings.Split(s, "#")
	if len(parts) != 3 {
		panic("index string needs two #: " + s)
	}
	idx := s2.NewShapeIndex()
	var pts []s2.Point
	for _, p := range splitTrim(parts[0], "|") {
		pts = append(pts, parsePoint(p))
	}
	if len(pts) > 0 {
		pv := s2.PointVector(pts)
		idx.Add(&pv)
	}
	for _, l := range splitTrim(parts[1], "|") {
		idx.Add(s2.LaxPolylineFromPoints(parsePoints(l)))
	}
	for _, p := range splitTrim(parts[2], "|") {
		idx.Add(makeLaxPolygon(p))
	}
	return idx
}

func edgeLessPts(a, b s2.Edge) bool {
	if c := a.V0.Cmp(b.V0.Vector); c != 0 {
		return c < 0
	}
	return a.V1.Cmp(b.V1.Vector) < 0
}

// indexMatchingLayer is s2builderutil::IndexMatchingLayer.
type indexMatchingLayer struct {
	opts      GraphOptions
	index     *s2.ShapeIndex
	dimension int
}

func (l *indexMatchingLayer) GraphOptions() GraphOptions { return l.opts }

func edgesStr(es []s2.Edge) string {
	var b []string
	for _, e := range es {
		b = append(b, pointStr(e.V0)+"-"+pointStr(e.V1))
	}
	return strings.Join(b, ", ")
}

func (l *indexMatchingLayer) Build(g *Graph) error {
	var actual, expected []s2.Edge
	for _, e := range g.edges {
		actual = append(actual, s2.Edge{V0: g.vertices[e.V0], V1: g.vertices[e.V1]})
	}
	for s := int32(0); s < numShapeIDs(l.index); s++ {
		sh := l.index.Shape(s)
		if sh == nil || (l.dimension >= 0 && sh.Dimension() != l.dimension) {
			continue
		}
		for e := sh.NumEdges() - 1; e >= 0; e-- {
			expected = append(expected, sh.Edge(e))
		}
	}
	sort.Slice(actual, func(i, j int) bool { return edgeLessPts(actual[i], actual[j]) })
	sort.Slice(expected, func(i, j int) bool { return edgeLessPts(expected[i], expected[j]) })
	var missing, extra []s2.Edge
	ai, ei := 0, 0
	for ai < len(actual) || ei < len(expected) {
		if ei == len(expected) || (ai < len(actual) && edgeLessPts(actual[ai], expected[ei])) {
			extra = append(extra, actual[ai])
			ai++
		} else if ai == len(actual) || edgeLessPts(expected[ei], actual[ai]) {
			missing = append(missing, expected[ei])
			ei++
		} else {
			ai++
			ei++
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("dimension %d: missing edges: %s; extra edges: %s", l.dimension, edgesStr(missing), edgesStr(extra))
	}
	return nil
}

func expectResultIndex(t *testing.T, op OpType, opts BooleanOptions, a, b, expected *s2.ShapeIndex, label string) {
	t.Helper()
	var layers []Layer
	for dim := 0; dim < 3; dim++ {
		de, sp := DegenerateKeep, SiblingKeep
		if dim == 2 {
			de, sp = DegenerateDiscardExcess, SiblingDiscardExcess
		}
		layers = append(layers, &indexMatchingLayer{NewGraphOptions(EdgeDirected, de, DuplicateKeep, sp), expected, dim})
	}
	if err := NewBooleanOperation(op, opts, layers...).Build(a, b); err != nil {
		t.Errorf("%s op %d (polygon %d polyline %d loopsBoundaries %v): %v", label, op, opts.PolygonModel,
			opts.PolylineModel, opts.PolylineLoopsHaveBoundaries, err)
	}
	if got, want := IsEmpty(op, a, b, opts), numShapeIDs(expected) == 0; got != want {
		t.Errorf("%s op %d (polygon %d polyline %d loopsBoundaries %v): IsEmpty %v want %v", label, op, opts.PolygonModel,
			opts.PolylineModel, opts.PolylineLoopsHaveBoundaries, got, want)
	}
}

func expectResult(t *testing.T, op OpType, opts BooleanOptions, a, b, expected string) {
	t.Helper()
	expectResultIndex(t, op, opts, makeIndex(a), makeIndex(b), makeIndex(expected),
		fmt.Sprintf("a=%q b=%q expected=%q", a, b, expected))
}

func roundToE(exp int) BooleanOptions {
	o := DefaultBooleanOptions()
	o.SnapFunction = NewIntLatLngSnapFunction(exp)
	return o
}

// Degeneracy coverage (DegeneracyCoverageTest).

func degenIndex(chars string) *s2.ShapeIndex {
	a, b, c := s2.PointFromCoords(1, 0, 0), s2.PointFromCoords(0, 0, 1), s2.PointFromCoords(0, 1, 0)
	neg := func(p s2.Point) s2.Point { return s2.Point{Vector: p.Mul(-1)} }
	idx := s2.NewShapeIndex()
	lp := func(loops ...[]s2.Point) { idx.Add(NewLaxPolygon(loops)) }
	for i := 0; i < len(chars); i++ {
		switch ch := chars[i]; ch {
		case '.':
		case 'p':
			pv := s2.PointVector{a}
			idx.Add(&pv)
		case 'P':
			idx.Add(s2.LaxPolylineFromPoints([]s2.Point{a, a}))
		case 'B':
			idx.Add(s2.LaxPolylineFromPoints([]s2.Point{b, b}))
		case 'u':
			idx.Add(s2.LaxPolylineFromPoints([]s2.Point{a, b}))
		case 'd':
			idx.Add(s2.LaxPolylineFromPoints([]s2.Point{b, a}))
		case 's':
			lp([]s2.Point{a})
		case 'S':
			lp([]s2.Point{a, b})
		case 'U':
			lp([]s2.Point{a, b, neg(c)})
		case 'D':
			lp([]s2.Point{b, a, c})
		case '~':
			i++
			if chars[i] == 'U' {
				lp([]s2.Point{neg(c), b, a})
			} else {
				lp([]s2.Point{c, a, b})
			}
		case 'Q':
			lp([]s2.Point{a, c, b, neg(c)})
		case 'H':
			lp([]s2.Point{a, b}, []s2.Point{})
		case 'h':
			lp([]s2.Point{a}, []s2.Point{})
		case '*':
			lp([]s2.Point{})
		default:
			panic("unknown degeneracy symbol " + string(ch))
		}
	}
	return idx
}

func runDegeneracy(t *testing.T, op OpType, pm PolygonModel, rules []string) {
	t.Helper()
	const inputChars = ".pPudsSUDHh*"
	symmetric := op != OpDifference
	models := []PolylineModel{PolylineOpen, PolylineSemiOpen, PolylineClosed}
	loopOpts := []bool{true, false}
	const lineChars = "Pud"
	opts := DefaultBooleanOptions()
	opts.PolygonModel = pm
	for i := 0; i < len(inputChars); i++ {
		ch0 := inputChars[i]
		row := strings.Fields(rules[i])
		row = row[2:]
		limit := len(inputChars)
		if symmetric {
			limit = i + 1
		}
		for j := 0; j < limit; j++ {
			ch1 := inputChars[j]
			nModels := 1
			if strings.IndexByte(lineChars, ch0) >= 0 || strings.IndexByte(lineChars, ch1) >= 0 {
				nModels = 3
			}
			for k := 0; k < nModels; k++ {
				opts.PolylineModel = models[k]
				nLoop := 1
				if ch0 == 'P' || ch1 == 'P' {
					nLoop = 2
				}
				for m := 0; m < nLoop; m++ {
					opts.PolylineLoopsHaveBoundaries = loopOpts[m]
					code := row[j]
					choices := strings.FieldsFunc(code, func(r rune) bool { return r == '<' || r == '>' })
					result := choices[0]
					if len(choices) > 1 {
						in0 := Contains(degenIndex(string(ch0)), degenIndex("p"), opts)
						in1 := Contains(degenIndex(string(ch1)), degenIndex("p"), opts)
						if (strings.Contains(code, "<") && !in0) || (strings.Contains(code, ">") && !in1) {
							result = choices[1]
						}
					}
					if c3 := strings.Split(result, "|"); len(c3) > 1 {
						result = c3[k]
					}
					label := fmt.Sprintf("inputs %c,%c expected %s", ch0, ch1, result)
					expectResultIndex(t, op, opts, degenIndex(string(ch0)), degenIndex(string(ch1)), degenIndex(result), label)
					if symmetric && j != i {
						label = fmt.Sprintf("inputs %c,%c expected %s", ch1, ch0, result)
						expectResultIndex(t, op, opts, degenIndex(string(ch1)), degenIndex(string(ch0)), degenIndex(result), label)
					}
				}
			}
		}
	}
}
func TestBool_DegeneratePolylines(t *testing.T) {
	options := DefaultBooleanOptions()
	a := "# 0:0, 0:0 #"
	b := "# #"
	options.PolylineModel = PolylineOpen
	expectResult(t, OpUnion, options, a, b, a)
	options.PolylineModel = PolylineSemiOpen
	expectResult(t, OpUnion, options, a, b, a)
	options.PolylineModel = PolylineClosed
	expectResult(t, OpUnion, options, a, b, a)
}

func TestBool_DegeneratePolygons(t *testing.T) {
	options := DefaultBooleanOptions()
	a := "# # 0:0, 0:5, 5:5, 5:0; 1:1; 2:2, 3:3; 6:6; 7:7, 8:8"
	b := "# #"
	options.PolygonModel = PolygonOpen
	expectResult(t, OpUnion, options, a, b, a)
	options.PolygonModel = PolygonSemiOpen
	expectResult(t, OpUnion, options, a, b, a)
	options.PolygonModel = PolygonClosed
	expectResult(t, OpUnion, options, a, b, a)
}

func TestBool_PointPoint(t *testing.T) {
	options := DefaultBooleanOptions()
	a := "0:0 | 1:0 # #"
	b := "0:0 | 2:0 # #"
	expectResult(t, OpUnion, options, a, b,
		"0:0 | 0:0 | 1:0 | 2:0 # #")
	expectResult(t, OpIntersection, options, a, b,
		"0:0 | 0:0 # #")
	expectResult(t, OpDifference, options, a, b,
		"1:0 # #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"1:0 | 2:0 # #")
}

func TestBool_PointOpenPolyline(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolylineModel = PolylineOpen
	a := "0:0 | 1:0 | 2:0 | 3:0 | 4:0 | 5:0 # #"
	b := "# 0:0, 1:0, 2:0 | 3:0, 3:0 | 4:0, 5:0, 4:0 #"
	expectResult(t, OpUnion, options, a, b,
		"0:0 | 2:0 | 3:0 | 4:0 "+
			"# 0:0, 1:0, 2:0 | 3:0, 3:0 | 4:0, 5:0, 4:0 #")
	expectResult(t, OpIntersection, options, a, b,
		"1:0 | 5:0 # #")
	expectResult(t, OpDifference, options, a, b,
		"0:0 | 2:0 | 3:0 | 4:0 # #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"0:0 | 2:0 | 3:0 | 4:0"+
			"# 0:0, 1:0, 2:0 | 3:0, 3:0 | 4:0, 5:0, 4:0 #")
}

func TestBool_PointOpenPolylineLoopBoundariesFalse(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolylineModel = PolylineOpen
	options.PolylineLoopsHaveBoundaries = false
	a := "0:0 | 1:0 | 2:0 | 3:0 | 4:0 | 5:0 # #"
	b := "# 0:0, 1:0, 2:0 | 3:0, 3:0 | 4:0, 5:0, 4:0 #"
	expectResult(t, OpUnion, options, a, b,
		"0:0 | 2:0 | 3:0 "+
			"# 0:0, 1:0, 2:0 | 3:0, 3:0 | 4:0, 5:0, 4:0 #")
	expectResult(t, OpIntersection, options, a, b,
		"1:0 | 4:0 | 5:0 # #")
	expectResult(t, OpDifference, options, a, b,
		"0:0 | 2:0 | 3:0 # #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"0:0 | 2:0 | 3:0 "+
			"# 0:0, 1:0, 2:0 | 3:0, 3:0 | 4:0, 5:0, 4:0 #")
}

func TestBool_PointSemiOpenPolyline(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolylineModel = PolylineSemiOpen
	for _, bool_value := range []bool{false, true} {
		options.PolylineLoopsHaveBoundaries = bool_value
		a := "0:0 | 1:0 | 2:0 | 3:0 | 4:0 | 5:0 # #"
		b := "# 0:0, 1:0, 2:0 | 3:0, 3:0 | 4:0, 5:0, 4:0 #"
		expectResult(t, OpUnion, options, a, b,
			"2:0 | 3:0 # 0:0, 1:0, 2:0 | 3:0, 3:0 | 4:0, 5:0, 4:0 #")
		expectResult(t, OpIntersection, options, a, b,
			"0:0 | 1:0 | 4:0 | 5:0 # #")
		expectResult(t, OpDifference, options, a, b,
			"2:0 | 3:0 # #")
		expectResult(t, OpSymmetricDifference, options, a, b,
			"2:0 | 3:0 # 0:0, 1:0, 2:0 | 3:0, 3:0 | 4:0, 5:0, 4:0 #")
	}
}

func TestBool_PointClosedPolyline(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolylineModel = PolylineClosed
	for _, bool_value := range []bool{false, true} {
		options.PolylineLoopsHaveBoundaries = bool_value
		a := "0:0 | 1:0 | 2:0 | 3:0 | 4:0 | 5:0 # #"
		b := "# 0:0, 1:0, 2:0 | 3:0, 3:0 | 4:0, 5:0, 4:0 #"
		expectResult(t, OpUnion, options, a, b,
			"# 0:0, 1:0, 2:0 | 3:0, 3:0 | 4:0, 5:0, 4:0 #")
		expectResult(t, OpIntersection, options, a, b,
			"0:0 | 1:0 | 2:0 | 3:0 | 4:0 | 5:0 # #")
		expectResult(t, OpDifference, options, a, b,
			"# #")
		expectResult(t, OpSymmetricDifference, options, a, b,
			"# 0:0, 1:0, 2:0 | 3:0, 3:0 | 4:0, 5:0, 4:0 #")
	}
}

func TestBool_PointPolygonInterior(t *testing.T) {
	options := DefaultBooleanOptions() // PolygonModel is irrelevant.
	a := "1:1 | 4:4 # #"
	b := "# # 0:0, 0:3, 3:0"
	expectResult(t, OpUnion, options, a, b,
		"4:4 # # 0:0, 0:3, 3:0")
	expectResult(t, OpIntersection, options, a, b,
		"1:1 # #")
	expectResult(t, OpDifference, options, a, b,
		"4:4 # #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"4:4 # # 0:0, 0:3, 3:0")
}

func TestBool_PointOpenPolygonVertex(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonOpen
	a := "0:1 | 1:0 # #"
	b := "# # 0:0, 0:1, 1:0"
	expectResult(t, OpUnion, options, a, b,
		"0:1 | 1:0 # # 0:0, 0:1, 1:0")
	expectResult(t, OpIntersection, options, a, b,
		"# #")
	expectResult(t, OpDifference, options, a, b,
		"0:1 | 1:0 # #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"0:1 | 1:0 # # 0:0, 0:1, 1:0")
}

func TestBool_PointSemiOpenPolygonVertex(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonSemiOpen
	// polygon := s2textformat::MakePolygonOrDie("0:0, 0:1, 1:0")
	// ASSERT_TRUE(polygon->Contains(s2textformat::MakePointOrDie("0:1")))
	// ASSERT_FALSE(polygon->Contains(s2textformat::MakePointOrDie("1:0")))
	a := "0:1 | 1:0 # #"
	b := "# # 0:0, 0:1, 1:0"
	expectResult(t, OpUnion, options, a, b,
		"1:0 # # 0:0, 0:1, 1:0")
	expectResult(t, OpIntersection, options, a, b,
		"0:1 # #")
	expectResult(t, OpDifference, options, a, b,
		"1:0 # #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"1:0 # # 0:0, 0:1, 1:0")
}

func TestBool_PointClosedPolygonVertex(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonClosed
	a := "0:1 | 1:0 # #"
	b := "# # 0:0, 0:1, 1:0"
	expectResult(t, OpUnion, options, a, b,
		"# # 0:0, 0:1, 1:0")
	expectResult(t, OpIntersection, options, a, b,
		"0:1 | 1:0 # #")
	expectResult(t, OpDifference, options, a, b,
		"# #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# # 0:0, 0:1, 1:0")
}

func TestBool_PolylineVertexOpenPolylineVertex(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolylineModel = PolylineOpen
	a := "# 0:0, 0:1, 0:2 | 0:3, 0:4, 0:3 #"
	b := "# 0:0, 1:0 | -1:1, 0:1, 1:1 | -1:2, 0:2 " +
		"| 1:3, 0:3, 1:3 | 0:4, 1:4, 0:4 #"
	expectResult(t, OpUnion, options, a, b,
		"# 0:0, 0:1, 0:2 | 0:0, 1:0 | -1:1, 0:1, 1:1 | -1:2, 0:2 "+
			"| 0:3, 0:4, 0:3 | 1:3, 0:3, 1:3 | 0:4, 1:4, 0:4 #")
	expectResult(t, OpIntersection, options, a, b,
		"# 0:1, 0:1 | 0:1, 0:1 #")
	expectResult(t, OpDifference, options, a, b,
		"# 0:0, 0:1, 0:2 | 0:3, 0:4, 0:3 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 0:0, 0:1, 0:2 | 0:0, 1:0 | -1:1, 0:1, 1:1 | -1:2, 0:2 "+
			"| 0:3, 0:4, 0:3 | 1:3, 0:3, 1:3 | 0:4, 1:4, 0:4 #")
}

func TestBool_PolylineVertexOpenPolylineVertexLoopBoundariesFalse(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolylineModel = PolylineOpen
	options.PolylineLoopsHaveBoundaries = false
	a := "# 0:0, 0:1, 0:2 | 0:3, 0:4, 0:3 #"
	b := "# 0:0, 1:0 | -1:1, 0:1, 1:1 | -1:2, 0:2 " +
		"| 1:3, 0:3, 1:3 | 0:4, 1:4, 0:4 #"
	expectResult(t, OpUnion, options, a, b,
		"# 0:0, 0:1, 0:2 | 0:0, 1:0 | -1:1, 0:1, 1:1 | -1:2, 0:2 "+
			"| 0:3, 0:4, 0:3 | 1:3, 0:3, 1:3 | 0:4, 1:4, 0:4 #")
	expectResult(t, OpIntersection, options, a, b,
		"# 0:1, 0:1 | 0:1, 0:1 "+
			"| 0:3, 0:3 | 0:3, 0:3 | 0:4, 0:4 | 0:4, 0:4 #")
	expectResult(t, OpDifference, options, a, b,
		"# 0:0, 0:1, 0:2 | 0:3, 0:4, 0:3 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 0:0, 0:1, 0:2 | 0:0, 1:0 | -1:1, 0:1, 1:1 | -1:2, 0:2 "+
			"| 0:3, 0:4, 0:3 | 1:3, 0:3, 1:3 | 0:4, 1:4, 0:4 #")
}

func TestBool_PolylineVertexSemiOpenPolylineVertex(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolylineModel = PolylineSemiOpen
	for _, bool_value := range []bool{false, true} {
		options.PolylineLoopsHaveBoundaries = bool_value
		a := "# 0:0, 0:1, 0:2 | 0:3, 0:4, 0:3 #"
		b := "# 0:0, 1:0 | -1:1, 0:1, 1:1 | -1:2, 0:2 " +
			"| 1:3, 0:3, 1:3 | 0:4, 1:4, 0:4 #"
		expectResult(t, OpUnion, options, a, b,
			"# 0:0, 0:1, 0:2 | 0:0, 1:0 | -1:1, 0:1, 1:1 | -1:2, 0:2 "+
				"| 0:3, 0:4, 0:3 | 1:3, 0:3, 1:3 | 0:4, 1:4, 0:4 #")
		expectResult(t, OpIntersection, options, a, b,
			"# 0:0, 0:0 | 0:0, 0:0 | 0:1, 0:1 | 0:1, 0:1 "+
				"| 0:3, 0:3 | 0:3, 0:3 | 0:4, 0:4 | 0:4, 0:4 #")
		expectResult(t, OpDifference, options, a, b,
			"# 0:0, 0:1, 0:2 | 0:3, 0:4, 0:3 #")
		expectResult(t, OpSymmetricDifference, options, a, b,
			"# 0:0, 0:1, 0:2 | 0:0, 1:0 | -1:1, 0:1, 1:1 | -1:2, 0:2 "+
				"| 0:3, 0:4, 0:3 | 1:3, 0:3, 1:3 | 0:4, 1:4, 0:4 #")
	}
}

func TestBool_PolylineVertexClosedPolylineVertex(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolylineModel = PolylineClosed
	a := "# 0:0, 0:1, 0:2 | 0:3, 0:4, 0:3 #"
	b := "# 0:0, 1:0 | -1:1, 0:1, 1:1 | -1:2, 0:2 " +
		"| 1:3, 0:3, 1:3 | 0:4, 1:4, 0:4 #"
	expectResult(t, OpUnion, options, a, b,
		"# 0:0, 0:1, 0:2 | 0:0, 1:0 | -1:1, 0:1, 1:1 | -1:2, 0:2 "+
			"| 0:3, 0:4, 0:3 | 1:3, 0:3, 1:3 | 0:4, 1:4, 0:4 #")
	expectResult(t, OpIntersection, options, a, b,
		"# 0:0, 0:0 | 0:0, 0:0 | 0:1, 0:1 | 0:1, 0:1 "+
			"| 0:2, 0:2 | 0:2, 0:2 "+
			"| 0:3, 0:3 | 0:3, 0:3 | 0:3, 0:3 "+
			"| 0:4, 0:4 | 0:4, 0:4 | 0:4, 0:4 #")
	expectResult(t, OpDifference, options, a, b,
		"# 0:0, 0:1, 0:2 | 0:3, 0:4, 0:3 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 0:0, 0:1, 0:2 | 0:0, 1:0 | -1:1, 0:1, 1:1 | -1:2, 0:2 "+
			"| 0:3, 0:4, 0:3 | 1:3, 0:3, 1:3 | 0:4, 1:4, 0:4 #")
}

func TestBool_PolylineVertexClosedPolylineVertexLoopBoundariesFalse(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolylineModel = PolylineClosed
	options.PolylineLoopsHaveBoundaries = false
	a := "# 0:0, 0:1, 0:2 | 0:3, 0:4, 0:3 #"
	b := "# 0:0, 1:0 | -1:1, 0:1, 1:1 | -1:2, 0:2 " +
		"| 1:3, 0:3, 1:3 | 0:4, 1:4, 0:4 #"
	expectResult(t, OpUnion, options, a, b,
		"# 0:0, 0:1, 0:2 | 0:0, 1:0 | -1:1, 0:1, 1:1 | -1:2, 0:2 "+
			"| 0:3, 0:4, 0:3 | 1:3, 0:3, 1:3 | 0:4, 1:4, 0:4 #")
	expectResult(t, OpIntersection, options, a, b,
		"# 0:0, 0:0 | 0:0, 0:0 | 0:1, 0:1 | 0:1, 0:1 "+
			"| 0:2, 0:2 | 0:2, 0:2 "+
			"| 0:3, 0:3 | 0:3, 0:3 | 0:4, 0:4 | 0:4, 0:4 #")
	expectResult(t, OpDifference, options, a, b,
		"# 0:0, 0:1, 0:2 | 0:3, 0:4, 0:3 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 0:0, 0:1, 0:2 | 0:0, 1:0 | -1:1, 0:1, 1:1 | -1:2, 0:2 "+
			"| 0:3, 0:4, 0:3 | 1:3, 0:3, 1:3 | 0:4, 1:4, 0:4 #")
}

func TestBool_TestSemiOpenPolygonVerticesContained(t *testing.T) {
	// polygon := s2textformat::MakePolygonOrDie(kVertexTestPolygonStr)
	// EXPECT_TRUE(polygon->Contains(s2textformat::MakePointOrDie("0:1")))
	// EXPECT_TRUE(polygon->Contains(s2textformat::MakePointOrDie("0:2")))
	// EXPECT_TRUE(polygon->Contains(s2textformat::MakePointOrDie("0:3")))
	// EXPECT_TRUE(polygon->Contains(s2textformat::MakePointOrDie("0:4")))
	// EXPECT_FALSE(polygon->Contains(s2textformat::MakePointOrDie("5:1")))
	// EXPECT_FALSE(polygon->Contains(s2textformat::MakePointOrDie("5:2")))
	// EXPECT_FALSE(polygon->Contains(s2textformat::MakePointOrDie("5:3")))
	// EXPECT_FALSE(polygon->Contains(s2textformat::MakePointOrDie("5:4")))
}

func TestBool_PolylineVertexOpenPolygonVertex(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonOpen
	a := "# 1:1, 0:1 | 0:2, 1:2 | -1:3, 0:3 | 0:4, -1:4 " +
		"| 6:1, 5:1 | 5:2, 6:2 | 4:3, 5:3 | 5:4, 4:4 #"
	b := "# # " + kVertexTestPolygonStr
	kDifferenceResult :=
		"# 0:1, 0:1 | 0:2, 0:2 | -1:3, 0:3 | 0:4, -1:4" +
			"| 6:1, 5:1 | 5:2, 6:2 | 5:3, 5:3 | 5:4, 5:4 #"
	expectResult(t, OpUnion, options, a, b,
		kDifferenceResult+kVertexTestPolygonStr)
	expectResult(t, OpIntersection, options, a, b,
		"# 1:1, 0:1 | 0:2, 1:2 | 4:3, 5:3 | 5:4, 4:4 #")
	expectResult(t, OpDifference, options, a, b, kDifferenceResult)
	expectResult(t, OpSymmetricDifference, options, a, b,
		kDifferenceResult+kVertexTestPolygonStr)
}

func TestBool_PolylineVertexOpenPolygonClosedPolylineVertex(t *testing.T) {
	kTestGeometrySuffix :=
		"-2:0, 0:1 | -2:1, 0:2 | -2:2, 0:3 | -2:3, 0:4 | " +
			"7:0, 5:1 | 7:1, 5:2 | 7:2, 5:3 | 7:3, 5:4 # " +
			kVertexTestPolygonStr
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonOpen
	a := "# 1:1, 0:1 | 0:2, 1:2 | -1:3, 0:3 | 0:4, -1:4 " +
		"| 6:1, 5:1 | 5:2, 6:2 | 4:3, 5:3 | 5:4, 4:4 #"
	b := "# " + kTestGeometrySuffix
	kDifferencePrefix :=
		"# -1:3, 0:3 | 0:4, -1:4 | 6:1, 5:1 | 5:2, 6:2"
	expectResult(t, OpUnion, options, a, b,
		kDifferencePrefix+
			" | 0:1, 0:1 | 0:2, 0:2 | 5:3, 5:3 | 5:4, 5:4 | "+
			kTestGeometrySuffix)
	expectResult(t, OpIntersection, options, a, b,
		"# 1:1, 0:1 | 0:2, 1:2 | 0:3, 0:3 | 0:4, 0:4"+
			"| 5:1, 5:1 | 5:2, 5:2 | 4:3, 5:3 | 5:4, 4:4"+
			"| 0:1, 0:1 | 0:2, 0:2 | 0:3, 0:3 | 0:4, 0:4"+
			"| 5:1, 5:1 | 5:2, 5:2 | 5:3, 5:3 | 5:4, 5:4 #")
	expectResult(t, OpDifference, options, a, b, kDifferencePrefix+" #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		kDifferencePrefix+" | "+kTestGeometrySuffix)
}

func TestBool_PolylineVertexSemiOpenPolygonVertex(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonSemiOpen
	a := "# 1:1, 0:1 | 0:2, 1:2 | -1:3, 0:3 | 0:4, -1:4 " +
		"| 6:1, 5:1 | 5:2, 6:2 | 4:3, 5:3 | 5:4, 4:4 #"
	b := "# # " + kVertexTestPolygonStr
	kDifferenceResult :=
		"# -1:3, 0:3 | 0:4, -1:4 | 6:1, 5:1 | 5:2, 6:2 | 5:3, 5:3 | 5:4, 5:4 #"
	expectResult(t, OpUnion, options, a, b,
		kDifferenceResult+kVertexTestPolygonStr)
	expectResult(t, OpIntersection, options, a, b,
		"# 1:1, 0:1 | 0:2, 1:2 | 0:3, 0:3 | 0:4, 0:4 "+
			"| 4:3, 5:3 | 5:4, 4:4 #")
	expectResult(t, OpDifference, options, a, b, kDifferenceResult)
	expectResult(t, OpSymmetricDifference, options, a, b,
		kDifferenceResult+kVertexTestPolygonStr)
}

func TestBool_PolylineVertexClosedPolygonVertex(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonClosed
	a := "# 1:1, 0:1 | 0:2, 1:2 | -1:3, 0:3 | 0:4, -1:4 " +
		"| 6:1, 5:1 | 5:2, 6:2 | 4:3, 5:3 | 5:4, 4:4 #"
	b := "# # " + kVertexTestPolygonStr
	kDifferenceResult :=
		"# -1:3, 0:3 | 0:4, -1:4 | 6:1, 5:1 | 5:2, 6:2 #"
	expectResult(t, OpUnion, options, a, b,
		kDifferenceResult+kVertexTestPolygonStr)
	expectResult(t, OpIntersection, options, a, b,
		"# 1:1, 0:1 | 0:2, 1:2 | 0:3, 0:3 | 0:4, 0:4"+
			"| 5:1, 5:1 | 5:2, 5:2 | 4:3, 5:3 | 5:4, 4:4 #")
	expectResult(t, OpDifference, options, a, b, kDifferenceResult)
	expectResult(t, OpSymmetricDifference, options, a, b,
		kDifferenceResult+kVertexTestPolygonStr)
}

func TestBool_PolylineEdgePolylineEdgeCrossing(t *testing.T) {
	options := roundToE(1)
	a := "# 0:0, 2:2 #"
	b := "# 2:0, 0:2 #"
	expectResult(t, OpUnion, options, a, b,
		"# 0:0, 1:1, 2:2 | 2:0, 1:1, 0:2 #")
	expectResult(t, OpIntersection, options, a, b,
		"# 1:1, 1:1 | 1:1, 1:1 #")
	expectResult(t, OpDifference, options, a, b,
		"# 0:0, 1:1, 2:2 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 0:0, 1:1, 2:2 | 2:0, 1:1, 0:2 #")
}

func TestBool_PolylineEdgePolylineEdgeOverlap(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonOpen
	a := "# 0:0, 1:0, 2:0, 2:5 | 3:0, 3:0 | 6:0, 5:0, 4:0 #"
	b := "# 0:0, 1:0, 2:0 | 3:0, 3:0 | 4:0, 5:0 #"
	expectResult(t, OpUnion, options, a, b,
		"# 0:0, 1:0, 2:0, 2:5 | 0:0, 1:0, 2:0 | 3:0, 3:0 | 3:0, 3:0 "+
			"| 6:0, 5:0, 4:0 | 4:0, 5:0 #")
	expectResult(t, OpIntersection, options, a, b,
		"# 0:0, 1:0, 2:0 | 0:0, 1:0, 2:0 | 3:0, 3:0 | 3:0, 3:0 "+
			"| 5:0, 4:0 | 4:0, 5:0 #")
	expectResult(t, OpDifference, options, a, b,
		"# 2:0, 2:5 | 6:0, 5:0 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 2:0, 2:5 | 6:0, 5:0 #")
}

func TestBool_PolylineLoopMultipleOpenPolylineEdge(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolylineModel = PolylineOpen
	a := "# 0:0, 0:1, 1:0, 0:0 | 2:2, 2:3, 3:2, 2:2 #"
	b := "# 0:0, 0:0 | 0:0, 0:1 | 2:2, 2:2 | 2:2, 3:2 #"
	expectResult(t, OpUnion, options, a, b,
		"# 0:0, 0:1, 1:0, 0:0 | 0:0, 0:0 | 0:0, 0:1 "+
			"| 2:2, 2:3, 3:2, 2:2 | 2:2, 2:2 | 2:2, 3:2 #")
	expectResult(t, OpIntersection, options, a, b,
		"# 0:0, 0:1 | 0:0, 0:1 | 2:2, 3:2 | 3:2, 2:2 #")
	expectResult(t, OpDifference, options, a, b,
		"# 0:1, 1:0, 0:0 | 2:2, 2:3, 3:2 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 0:1, 1:0, 0:0 | 0:0, 0:0 | 2:2, 2:3, 3:2 | 2:2, 2:2 #")
}

func TestBool_PolylineLoopMultipleSemiOpenPolylineEdge(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolylineModel = PolylineSemiOpen
	a := "# 0:0, 0:1, 1:0, 0:0 | 2:2, 2:3, 3:2, 2:2 #"
	b := "# 0:0, 0:0 | 0:0, 0:1 | 2:2, 2:2 | 2:2, 3:2 #"
	expectResult(t, OpUnion, options, a, b,
		"# 0:0, 0:1, 1:0, 0:0 | 0:0, 0:0 | 0:0, 0:1 "+
			"| 2:2, 2:3, 3:2, 2:2 | 2:2, 2:2 | 2:2, 3:2 #")
	expectResult(t, OpIntersection, options, a, b,
		"# 0:0, 0:0 | 0:0, 0:1 | 0:0, 0:1 "+
			"| 2:2, 2:2 | 2:2, 2:2 | 2:2, 3:2 | 3:2, 2:2 #")
	expectResult(t, OpDifference, options, a, b,
		"# 0:1, 1:0, 0:0 | 2:2, 2:3, 3:2 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 0:1, 1:0, 0:0 | 2:2, 2:3, 3:2 #")
}

func TestBool_PolylineLoopMultipleClosedPolylineEdge(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolylineModel = PolylineClosed
	a := "# 0:0, 0:1, 1:0, 0:0 | 2:2, 2:3, 3:2, 2:2 #"
	b := "# 0:0, 0:0 | 0:0, 0:1 | 2:2, 2:2 | 2:2, 3:2 #"
	expectResult(t, OpUnion, options, a, b,
		"# 0:0, 0:1, 1:0, 0:0 | 0:0, 0:0 | 0:0, 0:1 "+
			"| 2:2, 2:3, 3:2, 2:2 | 2:2, 2:2 | 2:2, 3:2 #")
	expectResult(t, OpIntersection, options, a, b,
		"# 0:0, 0:0 | 0:0, 0:0 | 0:0, 0:1 | 0:0, 0:1 "+
			"| 2:2, 2:2 | 2:2, 2:2 | 2:2, 3:2 | 3:2, 2:2 #")
	expectResult(t, OpDifference, options, a, b,
		"# 0:1, 1:0, 0:0 | 2:2, 2:3, 3:2 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 0:1, 1:0, 0:0 | 2:2, 2:3, 3:2 #")
}

func TestBool_PolylineLoopMultiplePolylineEdgeLoopBoundariesFalse(t *testing.T) {
	for _, polyline_model := range []PolylineModel{PolylineOpen, PolylineSemiOpen, PolylineClosed} {
		options := DefaultBooleanOptions()
		options.PolylineModel = polyline_model
		options.PolylineLoopsHaveBoundaries = false
		a := "# 0:0, 0:1, 1:0, 0:0 | 2:2, 2:3, 3:2, 2:2 #"
		b := "# 0:0, 0:0 | 0:0, 0:1 | 2:2, 2:2 | 2:2, 3:2 #"
		expectResult(t, OpUnion, options, a, b,
			"# 0:0, 0:1, 1:0, 0:0 | 0:0, 0:0 | 0:0, 0:1 "+
				"| 2:2, 2:3, 3:2, 2:2 | 2:2, 2:2 | 2:2, 3:2 #")
		expectResult(t, OpIntersection, options, a, b,
			"# 0:0, 0:0 | 0:0, 0:1 | 0:0, 0:1 "+
				"| 2:2, 2:2 | 2:2, 3:2 | 3:2, 2:2 #")
		expectResult(t, OpDifference, options, a, b,
			"# 0:1, 1:0, 0:0 | 2:2, 2:3, 3:2 #")
		expectResult(t, OpSymmetricDifference, options, a, b,
			"# 0:1, 1:0, 0:0 | 2:2, 2:3, 3:2 #")
	}
}

func TestBool_PolylineEdgeOpenPolygonEdgeOverlap(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonOpen
	a := "# 1:1, 1:3, 3:3 | 3:3, 1:3 # "
	b := "# # 1:1, 1:3, 3:3, 3:1"
	expectResult(t, OpUnion, options, a, b,
		"# 1:1, 1:3, 3:3 | 3:3, 1:3 # 1:1, 1:3, 3:3, 3:1")
	expectResult(t, OpIntersection, options, a, b,
		"# #")
	expectResult(t, OpDifference, options, a, b,
		"# 1:1, 1:3, 3:3 | 3:3, 1:3 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 1:1, 1:3, 3:3 | 3:3, 1:3 # 1:1, 1:3, 3:3, 3:1")
}

func TestBool_PolylineEdgeSemiOpenPolygonEdgeOverlap(t *testing.T) {
	// polygon := s2textformat::MakePolygonOrDie("1:1, 1:3, 3:3, 3:1")
	// ASSERT_FALSE(polygon->Contains(s2textformat::MakePointOrDie("1:1")))
	// ASSERT_TRUE(polygon->Contains(s2textformat::MakePointOrDie("1:3")))
	// ASSERT_FALSE(polygon->Contains(s2textformat::MakePointOrDie("3:3")))
	// ASSERT_FALSE(polygon->Contains(s2textformat::MakePointOrDie("3:1")))
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonSemiOpen
	a := "# 1:1, 1:3, 3:3 | 3:3, 1:3 # "
	b := "# # 1:1, 1:3, 3:3, 3:1"
	expectResult(t, OpUnion, options, a, b,
		"# 1:1, 1:1 | 3:3, 3:3 | 3:3, 1:3 # 1:1, 1:3, 3:3, 3:1")
	expectResult(t, OpIntersection, options, a, b,
		"# 1:3, 1:3 | 1:1, 1:3, 3:3 #")
	expectResult(t, OpDifference, options, a, b,
		"# 1:1, 1:1 | 3:3, 3:3 | 3:3, 1:3 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 1:1, 1:1 | 3:3, 3:3 | 3:3, 1:3 # 1:1, 1:3, 3:3, 3:1")
}

func TestBool_PolylineEdgeClosedPolygonEdgeOverlap(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonClosed
	a := "# 1:1, 1:3, 3:3 | 3:3, 1:3 # "
	b := "# # 1:1, 1:3, 3:3, 3:1"
	expectResult(t, OpUnion, options, a, b,
		"# # 1:1, 1:3, 3:3, 3:1")
	expectResult(t, OpIntersection, options, a, b,
		"# 1:1, 1:3, 3:3 | 3:3, 1:3 #")
	expectResult(t, OpDifference, options, a, b,
		"# #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# # 1:1, 1:3, 3:3, 3:1")
}

func TestBool_PolygonVertexMatching(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolylineModel = PolylineClosed
	options.PolygonModel = PolygonClosed
	a := "# 0:0, 1:1 # "
	b := "# # 0:0, 1:1"
	expectResult(t, OpUnion, options, a, b, "# # 0:0, 1:1")
}

func TestBool_PolylineEdgePolygonInterior(t *testing.T) {
	options := DefaultBooleanOptions() // PolygonModel is irrelevant.
	a := "# 1:1, 2:2 | 3:3, 3:3 | 6:6, 7:7 | 8:8, 8:8 # "
	b := "# # 0:0, 0:5, 5:5, 5:0"
	expectResult(t, OpUnion, options, a, b,
		"# 6:6, 7:7 | 8:8, 8:8 # 0:0, 0:5, 5:5, 5:0")
	expectResult(t, OpIntersection, options, a, b,
		"# 1:1, 2:2 | 3:3, 3:3 #")
	expectResult(t, OpDifference, options, a, b,
		"# 6:6, 7:7 | 8:8, 8:8 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 6:6, 7:7 | 8:8, 8:8 # 0:0, 0:5, 5:5, 5:0")
}

func TestBool_PolylineEdgeIsolatedStartVertexPlusInteriorCrossing(t *testing.T) {
	options := roundToE(1)
	a := "# 0:0, 0:10, 0:4 # "
	b := "# # 0:0, -5:5, 5:5"
	expectResult(t, OpDifference, options, a, b,
		"# 0:0, 0:0 | 0:5, 0:10, 0:5 #")
}

func TestBool_PolygonEdgeIsolatedStartVertexPlusInteriorCrossing(t *testing.T) {
	options := roundToE(1)
	options.PolygonModel = PolygonClosed
	a := "# # 0:0, 5:5, -5:5"
	b := "# # 1:4, 0:0, 0:8"
	expectResult(t, OpIntersection, options, a, b,
		"# # 0:0; 0:5, 0:8, 0.8:5")
}

func TestBool_PolygonVertexOpenPolygonVertex(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonOpen
	a := "# # 0:0, 0:5, 1:5, 0:0, 2:5, 3:5"
	b := "# # 0:0, 5:3, 5:2"
	expectResult(t, OpUnion, options, a, b,
		"# # 0:0, 0:5, 1:5, 0:0, 2:5, 3:5, 0:0, 5:3, 5:2")
	expectResult(t, OpIntersection, options, a, b,
		"# #")
	expectResult(t, OpDifference, options, a, b,
		"# # 0:0, 0:5, 1:5, 0:0, 2:5, 3:5")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# # 0:0, 0:5, 1:5, 0:0, 2:5, 3:5, 0:0, 5:3, 5:2")
}

func TestBool_PolygonVertexSemiOpenPolygonVertex(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonSemiOpen
	a := "# # 0:0, 0:5, 1:5, 0:0, 2:5, 3:5"
	b := "# # 0:0, 5:3, 5:2"
	expectResult(t, OpUnion, options, a, b,
		"# # 0:0, 0:5, 1:5, 0:0, 2:5, 3:5, 0:0, 5:3, 5:2")
	expectResult(t, OpIntersection, options, a, b,
		"# #")
	expectResult(t, OpDifference, options, a, b,
		"# # 0:0, 0:5, 1:5, 0:0, 2:5, 3:5")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# # 0:0, 0:5, 1:5, 0:0, 2:5, 3:5, 0:0, 5:3, 5:2")
}

func TestBool_PolygonVertexClosedPolygonVertex(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonClosed
	a := "# # 0:0, 0:5, 1:5, 0:0, 2:5, 3:5"
	b := "# # 0:0, 5:3, 5:2"
	expectResult(t, OpUnion, options, a, b,
		"# # 0:0, 0:5, 1:5, 0:0, 2:5, 3:5, 0:0, 5:3, 5:2")
	expectResult(t, OpIntersection, options, a, b,
		"# # 0:0")
	expectResult(t, OpDifference, options, a, b,
		"# # 0:0, 0:5, 1:5, 0:0, 2:5, 3:5")
	expectResult(t, OpDifference, options, b, a,
		"# # 0:0, 5:3, 5:2")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# # 0:0, 0:5, 1:5, 0:0, 2:5, 3:5, 0:0, 5:3, 5:2")
}

func TestBool_PolygonEdgePolygonEdgeCrossing(t *testing.T) {
	options := roundToE(2)
	a := "# # 0:0, 0:2, 2:2, 2:0"
	b := "# # 1:1, 1:3, 3:3, 3:1"
	expectResult(t, OpUnion, options, a, b,
		"# # 0:0, 0:2, 1:2, 1:3, 3:3, 3:1, 2:1, 2:0")
	expectResult(t, OpIntersection, options, a, b,
		"# # 1:1, 1:2, 2:2, 2:1")
	expectResult(t, OpDifference, options, a, b,
		"# # 0:0, 0:2, 1:2, 1:1, 2:1, 2:0")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# # 0:0, 0:2, 1:2, 1:1, 2:1, 2:0; "+
			"1:2, 1:3, 3:3, 3:1, 2:1, 2:2")
}

func TestBool_PolygonEdgeOpenPolygonEdgeOverlap(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonOpen
	a := "# # 0:0, 0:4, 2:4, 2:0"
	b := "# # 0:0, 1:1, 2:0; 0:4, 1:5, 2:4"
	expectResult(t, OpUnion, options, a, b,
		"# # 0:0, 0:4, 2:4, 2:0; 0:4, 1:5, 2:4")
	expectResult(t, OpIntersection, options, a, b,
		"# # 0:0, 1:1, 2:0")
	expectResult(t, OpDifference, options, a, b,
		"# # 0:0, 0:4, 2:4, 2:0, 1:1")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# # 0:0, 0:4, 2:4, 2:0, 1:1; 0:4, 1:5, 2:4")
}

func TestBool_PolygonEdgeSemiOpenPolygonEdgeOverlap(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonSemiOpen
	a := "# # 0:0, 0:4, 2:4, 2:0"
	b := "# # 0:0, 1:1, 2:0; 0:4, 1:5, 2:4"
	expectResult(t, OpUnion, options, a, b,
		"# # 0:0, 0:4, 1:5, 2:4, 2:0")
	expectResult(t, OpIntersection, options, a, b,
		"# # 0:0, 1:1, 2:0")
	expectResult(t, OpDifference, options, a, b,
		"# # 0:0, 0:4, 2:4, 2:0, 1:1")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# # 0:0, 0:4, 2:4, 2:0, 1:1; 0:4, 1:5, 2:4")
}

func TestBool_PolygonEdgeClosedPolygonEdgeOverlap(t *testing.T) {
	options := DefaultBooleanOptions()
	options.PolygonModel = PolygonClosed
	a := "# # 0:0, 0:4, 2:4, 2:0"
	b := "# # 0:0, 1:1, 2:0; 0:4, 1:5, 2:4"
	expectResult(t, OpUnion, options, a, b,
		"# # 0:0, 0:4, 1:5, 2:4, 2:0")
	expectResult(t, OpIntersection, options, a, b,
		"# # 0:0, 1:1, 2:0; 0:4, 2:4")
	expectResult(t, OpDifference, options, a, b,
		"# # 0:0, 0:4, 2:4, 2:0, 1:1")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# # 0:0, 0:4, 2:4, 2:0, 1:1; 0:4, 1:5, 2:4")
}

func TestBool_PolygonPolygonInterior(t *testing.T) {
	options := DefaultBooleanOptions() // PolygonModel is irrelevant.
	a := "# # 0:0, 0:4, 4:4, 4:0"
	b := "# # 1:1, 1:2, 2:2, 2:1; 5:5, 5:6, 6:6, 6:5"
	expectResult(t, OpUnion, options, a, b,
		"# # 0:0, 0:4, 4:4, 4:0; 5:5, 5:6, 6:6, 6:5")
	expectResult(t, OpIntersection, options, a, b,
		"# # 1:1, 1:2, 2:2, 2:1")
	expectResult(t, OpDifference, options, a, b,
		"# # 0:0, 0:4, 4:4, 4:0; 2:1, 2:2, 1:2, 1:1")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# # 0:0, 0:4, 4:4, 4:0; 2:1, 2:2, 1:2, 1:1; "+
			"5:5, 5:6, 6:6, 6:5")
}

func TestBool_PolygonEdgesDegenerateAfterSnapping(t *testing.T) {
	options := roundToE(0)
	a := "# # 0:-1, 0:1, 0.1:1, 0.1:-1"
	b := "# # -1:0.1, 1:0.1, 1:0, -1:0"
	expectResult(t, OpUnion, options, a, b,
		"# # 0:-1, 0:0, 0:1, 0:0 | "+
			"-1:0, 0:0, 1:0, 0:0")
	expectResult(t, OpIntersection, options, a, b,
		"# # 0:0")
	expectResult(t, OpDifference, options, a, b,
		"# # 0:-1, 0:0, 0:1, 0:0")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# # 0:-1, 0:0, 0:1, 0:0 | "+
			"-1:0, 0:0, 1:0, 0:0")
}
func TestDegen_OpenIntersection(t *testing.T) {
	rules := []string{
		". |  .",
		"p |  .    pp",
		"P |  .    p<.   PP",
		"u |  .    p<.   PP<.  uu",
		"d |  .    p<.   PP<.  ud    dd",
		"s |  .     .     .     .     .     s",
		"S |  .     .     .     .     .     .     S",
		"U |  .     .     .     .     .     .     .     U",
		"D |  .     .     .     .     .     .     .     .     D",
		"H |  .     .     .     .     .     .     .     U     D     H",
		"h |  .     .     .     u     d     .     S     U     D     H     h",
		"* |  .     p     P     u     d     s     S     U     D     H     h     *",
	}
	runDegeneracy(t, OpIntersection, PolygonOpen, rules)
}

func TestDegen_SemiOpenIntersection(t *testing.T) {
	rules := []string{
		". |  .",
		"p |  .    pp",
		"P |  .    p<.   PP",
		"u |  .    p<.   PP<.  uu",
		"d |  .    p<.   PP<.  ud    dd",
		"s |  .     .     .     .     .     s",
		"S |  .    p<.   P<.    .     .    s<.    S",
		"U |  .    p<.   P<.    u    P<>.  s<.    .     U",
		"D |  .    p<.   P<.   P<>.   d    s<.    .     .     D",
		"H |  .    p<.   P<.    u     d    s<.    .     U     D     H",
		"h |  .     p     P     u     d     .     S     U     D     H     h",
		"* |  .     p     P     u     d     s     S     U     D     H     h     *",
	}
	runDegeneracy(t, OpIntersection, PolygonSemiOpen, rules)
}

func TestDegen_ClosedIntersection(t *testing.T) {
	rules := []string{
		". |  .",
		"p |  .    pp",
		"P |  .    p<.   PP",
		"u |  .    p<.   PP<.  uu",
		"d |  .    p<.   PP<.  ud    dd",
		"s |  .     p     P    P>.   P>.    s",
		"S |  .     p     P     u     d     s     S",
		"U |  .     p     P     u     d     s     S     U",
		"D |  .     p     P     u     d     s     S     S     D",
		"H |  .     p     P     u     d     s     S     U     D     H",
		"h |  .     p     P     u     d     s     S     U     D     H     h",
		"* |  .     p     P     u     d     s     S     U     D     H     h     *",
	}
	runDegeneracy(t, OpIntersection, PolygonClosed, rules)
}

func TestDegen_OpenUnion(t *testing.T) {
	rules := []string{
		". |  .",
		"p |  p    pp",
		"P |  P   P<Pp   PP",
		"u |  u   u<up   Pu    uu",
		"d |  d   d<dp   Pd    ud    dd",
		"s |  s    ps    Ps    us    ds     s",
		"S |  S    pS    PS    uS    dS     S     S",
		"U |  U    pU    PU    uU    dU     U     U     U",
		"D |  D    pD    PD    uD    dD     D     D    UD     D",
		"H |  H    pH    PH    uH    dH     H     H     H     H     H",
		"h |  h    ph    Ph   Ph>h  Ph>h    h     h     h     h     h     h",
		"* |  *     *     *     *     *     *     *     *     *     *     *     *",
	}
	runDegeneracy(t, OpUnion, PolygonOpen, rules)
}

func TestDegen_SemiOpenUnion(t *testing.T) {
	rules := []string{
		". |  .",
		"p |  p    pp",
		"P |  P   P<Pp   PP",
		"u |  u   u<up   Pu    uu",
		"d |  d   d<dp   Pd    ud    dd",
		"s |  s    ps    Ps    us    ds     s",
		"S |  S   S<pS  S<PS   uS    dS     S     S",
		"U |  U   U<pU  U<PU U|U|BU  dU     U     U     U",
		"D |  D   D<pD  D<PD   uD  D|BD|PBD D     D     Q     D",
		"H |  H   H<pH  H<PH    H     H     H     *     *     *     H",
		"h |  h     h     h     h     h     *    *>h   *>h   *>h   *>h    h",
		"* |  *     *     *     *     *     *     *     *     *     *     *     *",
	}
	runDegeneracy(t, OpUnion, PolygonSemiOpen, rules)
}

func TestDegen_ClosedUnion(t *testing.T) {
	rules := []string{
		". |  .",
		"p |  p    pp",
		"P |  P   P<Pp   PP",
		"u |  u   u<up   Pu    uu",
		"d |  d   d<dp   Pd    ud    dd",
		"s |  s     s     s    us    ds     s",
		"S |  S     S     S     S     S     S     S",
		"U |  U     U     U     U     U     U     U     U",
		"D |  D     D     D     D     D     D     D     Q     D",
		"H |  H     H     H     H     H     H     *     *     *     H",
		"h |  h     h     h     h     h     *     *     *     *     *     h",
		"* |  *     *     *     *     *     *     *     *     *     *     *     *",
	}
	runDegeneracy(t, OpUnion, PolygonClosed, rules)
}

func TestDegen_OpenDifference(t *testing.T) {
	rules := []string{
		". |  .     .     .     .     .     .     .     .     .     .     .     .",
		"p |  p     .    .>p   .>p   .>p    p     p     p     p     p     p     .",
		"P |  P     P     .    .>P   .>P    P     P     P     P     P     P     .",
		"u |  u     u     u     .   .|P|.   u     u     u     u     u    P<.    .",
		"d |  d     d     d   .|B|.   .     d     d     d     d     d    P<.    .",
		"s |  s     s     s     s     s     .     s     s     s     s     s     .",
		"S |  S     S     S     S     S     S     .     S     S     S     .     .",
		"U |  U     U     U     U     U     U     U     .     U     .     .     .",
		"D |  D     D     D     D     D     D     D     D     .     .     .     .",
		"H |  H     H     H     H     H     H     H    ~U    ~D     .     .     .",
		"h |  h     h     h     h     h     h     H    ~U    ~D     S     .     .",
		"* |  *     *     *     *     *     h     H    ~U    ~D     S     s     .",
	}
	runDegeneracy(t, OpDifference, PolygonOpen, rules)
}

func TestDegen_SemiOpenDifference(t *testing.T) {
	rules := []string{
		". |  .     .     .     .     .     .     .     .     .     .     .     .",
		"p |  p     .    .>p   .>p   .>p    p     p    .>p   .>p    .     .     .",
		"P |  P     P     .    .>P   .>P    P     P    .>P   .>P    .     .     .",
		"u |  u     u     u     .   .|P|.   u     u   .|.|B   u     .     .     .",
		"d |  d     d     d   .|B|.   .     d     d     d   .|B|PB  .     .     .",
		"s |  s     s     s     s     s     .    .>s   .>s   .>s   .>s    s     .",
		"S |  S     S     S     S     S     S     .     .     .     S    s<.    .",
		"U |  U     U     U     U     U     U     U     .     U     .    s<.    .",
		"D |  D     D     D     D     D     D     D     D     .     .    s<.    .",
		"H |  H     H     H     H     H     H     H    ~U    ~D     .    s<.    .",
		"h |  h     h     h     h     h     h     H    ~U    ~D     S     .     .",
		"* |  *     *     *     *     *     h     H    ~U    ~D     S     s     .",
	}
	runDegeneracy(t, OpDifference, PolygonSemiOpen, rules)
}

func TestDegen_ClosedDifference(t *testing.T) {
	rules := []string{
		". |  .     .     .     .     .     .     .     .     .     .     .     .",
		"p |  p     .    .>p   .>p   .>p    .     .     .     .     .     .     .",
		"P |  P     P     .    .>P   .>P    .     .     .     .     .     .     .",
		"u |  u     u     u     .   .|P|.   u     .     .     .     .     .     .",
		"d |  d     d     d   .|B|.   .     d     .     .     .     .     .     .",
		"s |  s     s     s     s     s     .     .     .     .     .     s     .",
		"S |  S     S     S     S     S     S     .     .     .     S     .     .",
		"U |  U     U     U     U     U     U     U     .     U     .     .     .",
		"D |  D     D     D     D     D     D     D     D     .     .     .     .",
		"H |  H     H     H     H     H     H     H    ~U    ~D     .     .     .",
		"h |  h     h     h     h     h     h     H    ~U    ~D     S     .     .",
		"* |  *     *     *     *     *     h     H    ~U    ~D     S     s     .",
	}
	runDegeneracy(t, OpDifference, PolygonClosed, rules)
}

func TestDegen_OpenSymmetricDifference(t *testing.T) {
	rules := []string{
		". |  .",
		"p |  p     .",
		"P |  P   P<Pp    .",
		"u |  u   u<up  u<uP    .",
		"d |  d   d<dp  d<dP .|PB|.   .",
		"s |  s    sp    sP    su    sd     .",
		"S |  S    Sp    SP    Su    Sd     S     .",
		"U |  U    Up    UP    Uu    Ud     U     U     .",
		"D |  D    Dp    DP    Du    Dd     D     D    UD     .",
		"H |  H    Hp    HP    Hu    Hd     H     H    ~U    ~D     .",
		"h |  h    hp    hP   hP>h  hP>h    h     H    ~U    ~D     S     .",
		"* |  *     *     *     *     *     h     H    ~U    ~D     S     s     .",
	}
	runDegeneracy(t, OpSymmetricDifference, PolygonOpen, rules)
}

func TestDegen_SemiOpenSymmetricDifference(t *testing.T) {
	rules := []string{
		". |  .",
		"p |  p     .",
		"P |  P   P<Pp    .",
		"u |  u   u<up  u<uP    .",
		"d |  d   d<dp  d<dP .|PB|.   .",
		"s |  s    sp    sP    su    sd     .",
		"S |  S    Sp    SP    Su    Sd     S     .",
		"U |  U   U<Up  U<UP U|U|UB  Ud     U     U     .",
		"D |  D   D<Dp  D<DP   Du  D|BD|PBD D     D    UD     .",
		"H |  H     H     H     H     H     H     H    ~U    ~D     .",
		"h |  h     h     h     h     h     h     H    ~U    ~D     S     .",
		"* |  *     *     *     *     *     h     H    ~U    ~D     S     s     .",
	}
	runDegeneracy(t, OpSymmetricDifference, PolygonSemiOpen, rules)
}

func TestDegen_ClosedSymmetricDifference(t *testing.T) {
	rules := []string{
		". |  .",
		"p |  p     .",
		"P |  P   P<Pp    .",
		"u |  u   u<up  u<uP    .",
		"d |  d   d<dp  d<dP .|PB|.   .",
		"s |  s     s     s    su    sd     .",
		"S |  S     S     S     S     S     S     .",
		"U |  U     U     U     U     U     U     U     .",
		"D |  D     D     D     D     D     D     D    UD     .",
		"H |  H     H     H     H     H     H     H    ~U    ~D     .",
		"h |  h     h     h     h     h     h     H    ~U    ~D     S     .",
		"* |  *     *     *     *     *     h     H    ~U    ~D     S     s     .",
	}
	runDegeneracy(t, OpSymmetricDifference, PolygonClosed, rules)
}

func TestBool_ThreeOverlappingBars(t *testing.T) {
	options := roundToE(2)
	a := "# # 0:0, 0:2, 3:2, 3:0; 0:3, 0:5, 3:5, 3:3"
	b := "# # 1:1, 1:4, 2:4, 2:1"
	expectResult(t, OpUnion, options, a, b,
		"# # 0:0, 0:2, 1:2, 1:3, 0:3, 0:5, 3:5, 3:3, 2:3, 2:2, 3:2, 3:0")
	expectResult(t, OpIntersection, options, a, b,
		"# # 1:1, 1:2, 2:2, 2:1; 1:3, 1:4, 2:4, 2:3")
	expectResult(t, OpDifference, options, a, b,
		"# # 0:0, 0:2, 1:2, 1:1, 2:1, 2:2, 3:2, 3:0; "+
			"0:3, 0:5, 3:5, 3:3, 2:3, 2:4, 1:4, 1:3")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# # 0:0, 0:2, 1:2, 1:1, 2:1, 2:2, 3:2, 3:0; "+
			"0:3, 0:5, 3:5, 3:3, 2:3, 2:4, 1:4, 1:3; "+
			"1:2, 1:3, 2:3, 2:2")
}

func TestBool_FourOverlappingBars(t *testing.T) {
	options := roundToE(2)
	a := "# # 1:88, 1:93, 2:93, 2:88; -1:88, -1:93, 0:93, 0:88"
	b := "# # -2:89, -2:90, 3:90, 3:89; -2:91, -2:92, 3:92, 3:91"
	expectResult(t, OpUnion, options, a, b,
		"# # -1:88, -1:89, -2:89, -2:90, -1:90, -1:91, -2:91, -2:92, -1:92, "+
			"-1:93, 0:93, 0:92, 1:92, 1:93, 2:93, 2:92, 3:92, 3:91, 2:91, "+
			"2:90, 3:90, 3:89, 2:89, 2:88, 1:88, 1:89, 0:89, 0:88; "+
			"0:90, 1:90, 1:91, 0:91")
	expectResult(t, OpIntersection, options, a, b,
		"# # 1:89, 1:90, 2:90, 2:89; 1:91, 1:92, 2:92, 2:91; "+
			"-1:89, -1:90, 0:90, 0:89; -1:91, -1:92, 0:92, 0:91")
	expectResult(t, OpDifference, options, a, b,
		"# # 1:88, 1:89, 2:89, 2:88; 1:90, 1:91, 2:91, 2:90; "+
			"1:92, 1:93, 2:93, 2:92; -1:88, -1:89, 0:89, 0:88; "+
			"-1:90, -1:91, 0:91, 0:90; -1:92, -1:93, 0:93, 0:92")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# # 1:88, 1:89, 2:89, 2:88; -1:88, -1:89, 0:89, 0:88; "+
			"1:90, 1:91, 2:91, 2:90; -1:90, -1:91, 0:91, 0:90; "+
			"1:92, 1:93, 2:93, 2:92; -1:92, -1:93, 0:93, 0:92; "+
			"-2:89, -2:90, -1:90, -1:89; -2:91, -2:92, -1:92, -1:91; "+
			"0:89, 0:90, 1:90, 1:89; 0:91, 0:92, 1:92, 1:91; "+
			"2:89, 2:90, 3:90, 3:89; 2:91, 2:92, 3:92, 3:91")
}

func TestBool_OverlappingDoughnuts(t *testing.T) {
	options := roundToE(1)
	a := "# # -1:-93, -1:-89, 3:-89, 3:-93; " +
		"0:-92, 2:-92, 2:-90, 0:-90"
	b := "# # -3:-91, -3:-87, 1:-87, 1:-91; " +
		"-2:-90, 0:-90, 0:-88, -2:-88"
	expectResult(t, OpUnion, options, a, b,
		"# # -1:-93, -1:-91, -3:-91, -3:-87, 1:-87, 1:-89, 3:-89, 3:-93; "+
			"0:-92, 2:-92, 2:-90, 1:-90, 1:-91, 0:-91; "+
			"-2:-90, -1:-90, -1:-89, 0:-89, 0:-88, -2:-88")
	expectResult(t, OpIntersection, options, a, b,
		"# # -1:-91, -1:-90, 0:-90, 0:-91; "+
			"0:-90, 0:-89, 1:-89, 1:-90")
	expectResult(t, OpDifference, options, a, b,
		"# # -1:-93, -1:-91, 0:-91, 0:-92, 2:-92, "+
			"2:-90, 1:-90, 1:-89, 3:-89, 3:-93; "+
			"-1:-90, -1:-89, 0:-89, 0:-90")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# # -1:-93, -1:-91, 0:-91, 0:-92, 2:-92, "+
			"2:-90, 1:-90, 1:-89, 3:-89, 3:-93; "+
			"-3:-91, -3:-87, 1:-87, 1:-89, 0:-89, 0:-88,-2:-88,-2:-90,-1:-90,-1:-91; "+
			"-1:-90, -1:-89, 0:-89, 0:-90; "+
			"1:-91, 0:-91, 0:-90, 1:-90")
}

func TestBool_PolylineEnteringRectangle(t *testing.T) {
	options := roundToE(1)
	a := "# 0:0, 2:2 #"
	b := "# # 1:1, 1:3, 3:3, 3:1"
	expectResult(t, OpUnion, options, a, b,
		"# 0:0, 1:1 # 1:1, 1:3, 3:3, 3:1")
	expectResult(t, OpIntersection, options, a, b,
		"# 1:1, 2:2 #")
	expectResult(t, OpDifference, options, a, b,
		"# 0:0, 1:1 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 0:0, 1:1 # 1:1, 1:3, 3:3, 3:1")
}

func TestBool_PolylineCrossingRectangleTwice(t *testing.T) {
	options := roundToE(1)
	a := "# 0:-5, 0:5, 5:0, -5:0 #"
	b := "# # 1:1, 1:-1, -1:-1, -1:1"
	expectResult(t, OpUnion, options, a, b,
		"# 0:-5, 0:-1 | 0:1, 0:5, 5:0, 1:0 | -1:0, -5:0 "+
			"# 1:1, 1:0, 1:-1, 0:-1, -1:-1, -1:0, -1:1, 0:1")
	expectResult(t, OpIntersection, options, a, b,
		"# 0:-1, 0:1 | 1:0, -1:0 #")
	expectResult(t, OpDifference, options, a, b,
		"# 0:-5, 0:-1 | 0:1, 0:5, 5:0, 1:0 | -1:0, -5:0 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 0:-5, 0:-1 | 0:1, 0:5, 5:0, 1:0 | -1:0, -5:0 "+
			"# 1:1, 1:0, 1:-1, 0:-1, -1:-1, -1:0, -1:1, 0:1")
}

func TestBool_SelfIntersectingPolylines(t *testing.T) {
	options := roundToE(1)
	a := "# 0:2, 4:2, 2:0, 2:5 #"
	b := "# 0:4, 5:4, 3:6, 3:3 #"
	expectResult(t, OpUnion, options, a, b,
		"# 0:2, 4:2, 2:0, 2:4, 2:5 | 0:4, 2:4, 5:4, 3:6, 3:3 #")
	expectResult(t, OpIntersection, options, a, b,
		"# 2:4, 2:4 | 2:4, 2:4 #")
	expectResult(t, OpDifference, options, a, b,
		"# 0:2, 4:2, 2:0, 2:4, 2:5 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 0:2, 4:2, 2:0, 2:4, 2:5 | 0:4, 2:4, 5:4, 3:6, 3:3 #")
	options.SplitAllCrossingPolylineEdges = true
	expectResult(t, OpUnion, options, a, b,
		"# 0:2, 2:2, 4:2, 2:0, 2:2, 2:4, 2:5 "+
			"| 0:4, 2:4, 3:4, 5:4, 3:6, 3:4, 3:3 #")
	expectResult(t, OpIntersection, options, a, b,
		"# 2:4, 2:4 | 2:4, 2:4 #")
	expectResult(t, OpDifference, options, a, b,
		"# 0:2, 2:2, 4:2, 2:0, 2:2, 2:4, 2:5 #")
	expectResult(t, OpSymmetricDifference, options, a, b,
		"# 0:2, 2:2, 4:2, 2:0, 2:2, 2:4, 2:5 "+
			"| 0:4, 2:4, 3:4, 5:4, 3:6, 3:4, 3:3 #")
}

func computeTestUnion(t *testing.T, aLoops, bLoops [][]s2.Point, snap s1.Angle) {
	t.Helper()
	a, b := s2.NewShapeIndex(), s2.NewShapeIndex()
	a.Add(NewLaxPolygon(aLoops))
	b.Add(NewLaxPolygon(bLoops))
	l := NewLaxPolygonLayer()
	o := DefaultBooleanOptions()
	o.SnapFunction = IdentitySnapFunction{snap}
	if err := NewBooleanOperation(OpUnion, o, l).Build(a, b); err != nil {
		t.Fatal(err)
	}
	if l.Polygon.NumEdges() == 0 && !l.Polygon.IsFull() {
		t.Errorf("union is empty")
	}
}

func pt(x, y, z float64) s2.Point { return s2.Point{Vector: r3.Vector{X: x, Y: y, Z: z}} }

func TestBool_GetCrossedVertexIndexBug1(t *testing.T) {
	a_loops := [][]s2.Point{
		{
			pt(-0.38306437985388492, -0.74921955334206214, 0.54030708099846292),
			pt(-0.3830643798552798, -0.74921955334134249, 0.5403070809984718),
			pt(-0.38306437985529124, -0.74921955334136414, 0.54030708099843361),
			pt(-0.38306437985389635, -0.74921955334208379, 0.54030708099842473),
		},
	}
	b_loops := [][]s2.Point{
		{
			pt(-0.38306437985390962, -0.74921955334210588, 0.54030708099838465),
			pt(-0.38306437985527797, -0.74921955334134205, 0.54030708099847369),
			pt(-0.38306437985527941, -0.74921955334134405, 0.54030708099847014),
			pt(-0.38306437985391095, -0.74921955334210777, 0.54030708099838098),
		},
	}

	computeTestUnion(t, a_loops, b_loops, s1.Angle(intersectionMergeRadius))
}

func TestBool_GetCrossedVertexIndexBug2(t *testing.T) {
	a_loops := [][]s2.Point{
		{
			pt(-0.3837392878495085, -0.7477800800281974, 0.5418201831546835),
			pt(-0.38373928785696076, -0.7477800800212292, 0.54182018315902258),
			pt(-0.38373928785701278, -0.74778008002124685, 0.5418201831589613),
			pt(-0.38373928785703426, -0.7477800800212544, 0.54182018315893576),
			pt(-0.38373947205489456, -0.74778014227795497, 0.5418199667802881),
			pt(-0.38373947204434411, -0.74778014228781997, 0.54181996677414512),
			pt(-0.38373947205872994, -0.74778014228185352, 0.54181996677219124),
			pt(-0.38373947218468357, -0.74778014288930306, 0.54181996584462788),
			pt(-0.3837396702525171, -0.74778021044361542, 0.54181973233114322),
			pt(-0.38373967023137123, -0.74778021046333043, 0.54181973231891067),
			pt(-0.38373947216030285, -0.74778014290791484, 0.54181996583620895),
			pt(-0.38373947217087578, -0.74778014289805739, 0.54181996584232528),
			pt(-0.38373947215649007, -0.74778014290402395, 0.54181996584427927),
			pt(-0.3837394720305386, -0.74778014229658485, 0.5418199667718262),
			pt(-0.38373928783585998, -0.74778008004095942, 0.54182018314673686),
			pt(-0.38373928784641037, -0.7477800800310942, 0.54182018315287972),
			pt(-0.38373928783578648, -0.74778008004093421, 0.54182018314682368),
			pt(-0.383739287835765, -0.74778008004092666, 0.54182018314684921),
		},
	}
	b_loops := [][]s2.Point{
		{
			pt(-0.38373923813692823, -0.7477800632164362, 0.54182024156551456),
			pt(-0.3837392878569364, -0.74778008002122087, 0.54182018315905123),
			pt(-0.38373928784640354, -0.74778008003106944, 0.54182018315291858),
			pt(-0.38373928784638789, -0.74778008003108642, 0.54182018315290648),
			pt(-0.38373928784638023, -0.74778008003109453, 0.54182018315290048),
			pt(-0.38373928783692102, -0.74778008004124585, 0.54182018314559),
			pt(-0.38373928783691913, -0.74778008004124541, 0.54182018314559188),
			pt(-0.38373928784636568, -0.74778008003110774, 0.54182018315289271),
			pt(-0.38373928784637329, -0.74778008003109953, 0.54182018315289848),
			pt(-0.38373928783583561, -0.74778008004095109, 0.5418201831467655),
			pt(-0.38373923811582744, -0.74778006323616641, 0.54182024155322883),
			pt(-0.38373857650312843, -0.74777983961840766, 0.54182101875399913),
			pt(-0.38373857652422921, -0.74777983959867744, 0.54182101876628486),
		},
	}

	computeTestUnion(t, a_loops, b_loops, s1.Angle(intersectionMergeRadius))
}

func TestBool_GetCrossedVertexIndexBug3(t *testing.T) {
	a_loops := [][]s2.Point{
		{
			pt(1, 0, 2.4678234835261742e-72),
			pt(0.99984769515639127, 0.017452406437283512, 1.8530922845942552e-27),
			pt(0.99740259703611311, 0.069881849826437858, 0.017452406437283512),
		},
	}
	b_loops := [][]s2.Point{
		{
			pt(0.99999999999999989, 2.4674476220564615e-72, 2.4678234835261742e-72),
			pt(0.99999999999999989, 2.8837981406657438e-169, 2.4678234835261742e-72),
			pt(1, 2.8837981406657432e-169, 2.4678234835261742e-72),
		},
	}

	computeTestUnion(t, a_loops, b_loops, 0)
}

func TestBool_GetCrossedVertexIndexBug4(t *testing.T) {
	a_loops := [][]s2.Point{{
		pt(0.62233331065911901, -0.0014161759526823048, 0.78275107466533156),
		pt(0.6223328557578689, -0.0014164217071954736, 0.78275143589379825),
		parsePoint("51.51317:-0.1306"),
	}}
	b_loops := [][]s2.Point{{
		pt(0.62233331033809591, -0.001416176126110953, 0.78275107492024998),
		pt(0.62233331033809591, -0.0014161761261109063, 0.78275107492025009),
		parsePoint("51.52:-0.12"),
		parsePoint("51.52:-0.14"),
	}}

	computeTestUnion(t, a_loops, b_loops, 0)
}

func TestBool_GetCrossedVertexIndexBug5(t *testing.T) {
	a_loops := [][]s2.Point{
		{
			pt(0.99984769515639127, 0, 0.017452406437283512),
			pt(0.99923861495548261, 0.017441774902830158, 0.034899496702500969),
			pt(0.99847743863945992, 0.052327985223313139, 0.017452406437283512),
			pt(0.99802119662406841, 0.034851668155187324, 0.052335956242943835),
		},
	}
	b_loops := [][]s2.Point{
		{
			pt(0.99802119662406841, 0.034851668155187324, 0.052335956242943835),
			pt(0.99619692339885657, 0.052208468483931986, 0.069756473744125302),
			pt(0.99802098681615425, 0.034839714972148959, 0.052347914334467859),
			pt(0.99741208276778681, 0.017411821260589495, 0.069756473744125302),
			pt(0.99741219210106513, 0.017411340538768819, 0.069755030419252628),
			pt(0.99741211642315963, 0.017409893252357169, 0.069756473744125302),
			pt(0.99984769515639116, 4.9500424645560228e-16, 0.017452406437284993),
			pt(0.99984769515639127, 3.7368529835165677e-16, 0.017452406437284632),
			pt(0.99984769515639116, 3.3065924905014365e-16, 0.017452406437284504),
			pt(0.99984769515639127, 9.9060035932242025e-16, 0.017452406437284504),
			pt(0.99969541350954794, 0.017449748351250485, 0.017452406437283512),
		},
		{
			pt(0.99984769515639116, 3.3065924905014365e-16, 0.017452406437284504),
			pt(0.99984769515639116, 3.3006856770496304e-16, 0.017452406437284504),
			pt(0.99984769515639127, 0, 0.017452406437284504),
			pt(0.99984769515639127, 0, 0.017452406437283512),
		},
	}

	computeTestUnion(t, a_loops, b_loops, 0)
}

func TestBool_GetCrossedVertexIndexBug6(t *testing.T) {
	a_loops := [][]s2.Point{
		{
			pt(0.99870488823558456, 0.026138065586168355, 0.043650289137205818),
			pt(0.99876259434149239, 0.030513215246694664, 0.0392711578586665),
			pt(0.99984769515639127, 0.017452406437283512, 0),
			pt(0.998782023517925, 0.034862286684437908, 0.034915476003791211),
			pt(0.99878202512991221, 0.034878236872062651, 0.034899496702500969),
			pt(0.9975640502598242, 0.069756473744125302, 0),
			pt(0.99877979583714305, 0.034883478425067296, 0.034958008531414335),
			pt(0.99619692339885657, 0.052208468483931986, 0.069756473744125302),
			pt(0.99847581234813876, 0.017465633646566288, 0.052354596713645812),
			pt(0.9975640502598242, 0, 0.069756473744125302),
			pt(0.99847674250410212, 0.017444393356200013, 0.052343937746706169),
			pt(0.99847743863945992, 0.017428488520812163, 0.052335956242943835),
			pt(0.99984769515639127, 0, 0.017452406437283512),
		},
		{
			pt(0.99619692339885657, 0.052208468483931986, 0.069756473744125302),
			pt(0.99802119661969568, 0.034851668280404598, 0.052335956242943835),
			pt(0.9987605225894034, 0.030527121154938986, 0.039313018084772409),
			pt(0.99870321796526884, 0.026161932439896601, 0.043674199670139441),
		},
		{
			pt(0.99619692339885657, 0.052208468483931986, 0.069756473744125302),
			pt(0.99619692339885657, 0.06966087492121549, 0.052335956242943835),
			pt(0.99513403437078507, 0.069586550480032719, 0.069756473744125302),
		},
	}
	b_loops := [][]s2.Point{
		{
			pt(0.99802200429988497, 0.034828499898458924, 0.052335977377554299),
			pt(0.99862953475457383, 0, 0.052335956242943835),
			pt(0.99923793061512223, 0.017455729388178846, 0.034912111530741322),
			pt(0.99923859085845868, 0.017443155365764275, 0.034899496702500969),
			pt(0.99923793076147094, 0.017455737780810811, 0.034912103145779166),
			pt(0.9992865072388355, 0.020934110218524152, 0.0314362764933699),
			pt(1, 0, 0),
			pt(0.99929987808789411, 0.022418034384064717, 0.029953053064335624),
			pt(0.99931406232431441, 0.02616995393092059, 0.026201876881811362),
			pt(0.99984769515639127, 0.017452406437283512, 0),
			pt(0.99930573320200933, 0.029072747464899757, 0.023298646837028814),
			pt(0.99862953475457383, 0.052335956242943835, 1.700986599320836e-73),
			pt(0.99838518277004218, 0.038347188759395717, 0.041910857059723181),
			pt(0.99619692339885668, 0.052208468483931979, 0.069756473744125289),
		},
		{
			pt(0.99802119662406841, 0.052304074592470849, 0.034899496702500969),
			pt(0.99847743834686298, 0.052327990806397578, 0.017452406437283512),
			pt(0.99619645281505653, 0.052208443821680058, 0.069763212314351342),
			pt(0.99619692339885657, 0.052208468483932, 0.069756473744125316),
			pt(0.99619692339885657, 0.052208468483931986, 0.069756473744125302),
			pt(0.99619692339885679, 0.052208468483931993, 0.069756473744125316),
			pt(0.99619692339885679, 0.052208468483931986, 0.069756473744125302),
			pt(0.99619692339885668, 0.052208468483931979, 0.069756473744125289),
		},
	}

	computeTestUnion(t, a_loops, b_loops, 0)
}

func expectPolygon(t *testing.T, op OpType, aStr, bStr, expected string) {
	t.Helper()
	a := makeIndex("# # " + aStr)
	b := makeIndex("# # " + bStr)
	l := &LaxPolygonLayer{EdgeType: EdgeDirected, DegenerateBoundaries: LaxDiscard}
	o := DefaultBooleanOptions()
	o.SnapFunction = IdentitySnapFunction{1.1 * s1.Degree}
	if err := NewBooleanOperation(op, o, l).Build(a, b); err != nil {
		t.Fatal(err)
	}
	got := ""
	if l.Polygon.IsFull() {
		got = "full"
	} else if l.Polygon.NumEdges() > 0 {
		got = "nonempty"
	}
	if got != expected {
		t.Errorf("op %d %q %q: got %q want %q", op, aStr, bStr, got, expected)
	}
}

func TestBool_FullAndEmptyResults(t *testing.T) {
	kEmpty := ""
	kFull := "full"
	kShell1 := "10:0, 10:10, 20:10"
	kHole1 := "10:0, 20:10, 10:10"
	kShell1Minus := "11:2, 11:9, 18:9"
	kShell1Plus := "9:-2, 9:11, 22:11"
	kShell2 := "10:20, 10:30, 20:30"
	kHole2 := "10:20, 20:30, 10:30"
	kNorthHemi := "0:0, 0:120, 0:-120"
	kSouthHemi := "0:0, 0:-120, 0:120"
	kSouthHemiPlus := "0.5:0, 0.5:-120, 0.5:120"
	k6FaceShell1 := "0:-45, 45:0, 45:90, 0:135, -45:180, -45:-90"
	k6FaceHole1 := "0:-45, -45:-90, -45:180, 0:135, 45:90, 45:0"
	k6FaceShell1Minus :=
		"-1:-45, 44:0, 44:90, -1:135, -46:180, -46:-90"
	k6FaceShell1Plus := "1:-45, 46:0, 46:90, 1:135, -44:180, -44:-90"
	kAlmostEmpty1 := "2:0, 2:10, 3:0"
	kAlmostFull1 := "2:0, 3:0, 2:10"
	kAlmostEmpty2 := "4:0, 4:10, 5:0"
	kAlmostFull2 := "4:0, 5:0, 4:10"
	k6FaceAlmostEmpty1 := k6FaceShell1Minus + "; " + k6FaceHole1
	expectPolygon(t, OpUnion, kEmpty, kEmpty, kEmpty)
	expectPolygon(t, OpUnion, kAlmostEmpty1, kAlmostEmpty2, kEmpty)
	expectPolygon(t, OpUnion, k6FaceAlmostEmpty1, k6FaceAlmostEmpty1, kEmpty)
	expectPolygon(t, OpUnion, kEmpty, kFull, kFull)
	expectPolygon(t, OpUnion, kEmpty, kFull, kFull)
	expectPolygon(t, OpUnion, kFull, kFull, kFull)
	expectPolygon(t, OpUnion, kFull, kShell1, kFull)
	expectPolygon(t, OpUnion, kHole1, kHole2, kFull)
	expectPolygon(t, OpUnion, kHole1, kShell1, kFull)
	expectPolygon(t, OpUnion, kHole1, kShell1Minus, kFull)
	expectPolygon(t, OpUnion, k6FaceHole1, k6FaceShell1Minus, kFull)
	expectPolygon(t, OpIntersection, kEmpty, kEmpty, kEmpty)
	expectPolygon(t, OpIntersection, kEmpty, kFull, kEmpty)
	expectPolygon(t, OpIntersection, kFull, kEmpty, kEmpty)
	expectPolygon(t, OpIntersection, kEmpty, kHole1, kEmpty)
	expectPolygon(t, OpIntersection, kShell1, kShell2, kEmpty)
	expectPolygon(t, OpIntersection, kShell1, kHole1, kEmpty)
	expectPolygon(t, OpIntersection, k6FaceShell1, k6FaceHole1, kEmpty)
	expectPolygon(t, OpIntersection, kShell1Plus, kHole1, kEmpty)
	expectPolygon(t, OpIntersection, k6FaceShell1Plus, k6FaceHole1, kEmpty)
	expectPolygon(t, OpIntersection, kFull, kFull, kFull)
	expectPolygon(t, OpIntersection, kAlmostFull1, kAlmostFull2, kFull)
	expectPolygon(t, OpDifference, kEmpty, kEmpty, kEmpty)
	expectPolygon(t, OpDifference, kEmpty, kFull, kEmpty)
	expectPolygon(t, OpDifference, kFull, kFull, kEmpty)
	expectPolygon(t, OpDifference, kEmpty, kShell1, kEmpty)
	expectPolygon(t, OpDifference, kShell1, kFull, kEmpty)
	expectPolygon(t, OpDifference, kShell1, kShell1, kEmpty)
	expectPolygon(t, OpDifference, kShell1, kHole2, kEmpty)
	expectPolygon(t, OpDifference, k6FaceShell1, k6FaceShell1Plus, kEmpty)
	expectPolygon(t, OpDifference, kShell1Plus, kShell1, kEmpty)
	expectPolygon(t, OpDifference, k6FaceShell1Plus, k6FaceShell1, kEmpty)
	expectPolygon(t, OpDifference, kFull, kEmpty, kFull)
	expectPolygon(t, OpDifference, kAlmostFull1, kAlmostEmpty2, kFull)
	expectPolygon(t, OpSymmetricDifference, kEmpty, kEmpty, kEmpty)
	expectPolygon(t, OpSymmetricDifference, kFull, kFull, kEmpty)
	expectPolygon(t, OpSymmetricDifference, kShell1, kShell1, kEmpty)
	expectPolygon(t, OpSymmetricDifference, kNorthHemi, kNorthHemi, kEmpty)
	expectPolygon(t, OpSymmetricDifference, k6FaceShell1, k6FaceShell1, kEmpty)
	expectPolygon(t, OpSymmetricDifference, kShell1Plus, kShell1, kEmpty)
	expectPolygon(t, OpSymmetricDifference, k6FaceShell1Plus, k6FaceShell1, kEmpty)
	expectPolygon(t, OpSymmetricDifference, k6FaceShell1Minus, k6FaceShell1, kEmpty)
	expectPolygon(t, OpSymmetricDifference, kFull, kEmpty, kFull)
	expectPolygon(t, OpSymmetricDifference, kEmpty, kFull, kFull)
	expectPolygon(t, OpSymmetricDifference, kShell1, kHole1, kFull)
	expectPolygon(t, OpSymmetricDifference, kAlmostEmpty1, kAlmostFull1, kFull)
	expectPolygon(t, OpSymmetricDifference, kShell1Plus, kHole1, kFull)
	expectPolygon(t, OpSymmetricDifference, kAlmostFull1, kAlmostEmpty2, kFull)
	expectPolygon(t, OpSymmetricDifference, kNorthHemi, kSouthHemi, kFull)
	expectPolygon(t, OpSymmetricDifference, kNorthHemi, kSouthHemiPlus, kFull)
}

func TestBool_Equals(t *testing.T) {
	eq := func(a, b string) bool { return Equals(makeIndex(a), makeIndex(b), DefaultBooleanOptions()) }
	if !eq("# #", "# #") || !eq("# # full", "# # full") {
		t.Error("equal sets")
	}
	for _, c := range [][2]string{{"# #", "# # full"}, {"0:0 # #", "# #"}, {"0:0 # #", "# # full"},
		{"# 0:0, 1:1 #", "# #"}, {"# 0:0, 1:1 #", "# # full"}, {"# # 0:0, 0:1, 1:0 ", "# #"}, {"# # 0:0, 0:1, 1:0 ", "# # full"}} {
		if eq(c[0], c[1]) {
			t.Errorf("%q == %q", c[0], c[1])
		}
	}
}

func TestBool_ContainsIntersectsEmptyAndFull(t *testing.T) {
	o := DefaultBooleanOptions()
	empty, full := makeIndex("# #"), makeIndex("# # full")
	if !Contains(empty, empty, o) || Contains(empty, full, o) || !Contains(full, empty, o) || !Contains(full, full, o) {
		t.Error("contains")
	}
	if Intersects(empty, empty, o) || Intersects(empty, full, o) || Intersects(full, empty, o) || !Intersects(full, full, o) {
		t.Error("intersects")
	}
}
