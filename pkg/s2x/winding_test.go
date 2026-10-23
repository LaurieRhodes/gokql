package s2x

// Tests converted from s2winding_operation_test.cc.

import (
	"strings"
	"testing"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

func laxPolygonOrEmpty(s string) *LaxPolygon {
	if strings.TrimSpace(s) == "" {
		return NewLaxPolygon(nil)
	}
	return makeLaxPolygon(s)
}

func expectWindingResult(t *testing.T, snap SnapFunction, includeDegeneracies bool, loops []string,
	refStr string, refWinding int, rule WindingRule, expectedStr string) {
	t.Helper()
	expected := s2.NewShapeIndex()
	expected.Add(laxPolygonOrEmpty(expectedStr))
	layer := NewLaxPolygonLayer()
	op := NewWindingOperation(layer, snap, includeDegeneracies)
	for _, l := range loops {
		op.AddLoop(parsePoints(l))
	}
	if err := op.Build(parsePoint(refStr), refWinding, rule); err != nil {
		t.Fatalf("%v: build: %v", loops, err)
	}
	actual := s2.NewShapeIndex()
	actual.Add(layer.Polygon)
	diff := NewLaxPolygonLayer()
	if err := NewBooleanOperation(OpSymmetricDifference, DefaultBooleanOptions(), diff).Build(actual, expected); err != nil {
		t.Fatal(err)
	}
	if !diff.Polygon.IsEmpty() {
		t.Errorf("%v ref %s winding %d rule %d: result differs from %q", loops, refStr, refWinding, rule, expectedStr)
	}
}

func expectDegenerateWindingResult(t *testing.T, snap SnapFunction, loops []string, refStr string,
	refWinding int, rule WindingRule, expectedFalse, expectedTrue string) {
	t.Helper()
	expectWindingResult(t, snap, false, loops, refStr, refWinding, rule, expectedFalse)
	expectWindingResult(t, snap, true, loops, refStr, refWinding, rule, expectedTrue)
}

var zeroSnap = IdentitySnapFunction{}

func TestWinding_Empty(t *testing.T) {
	expectWindingResult(t, zeroSnap, false, []string{""}, "5:5", 0, WindingPositive, "")
	expectWindingResult(t, zeroSnap, false, []string{""}, "5:5", 1, WindingPositive, "full")
}

func TestWinding_PointLoop(t *testing.T) {
	expectDegenerateWindingResult(t, zeroSnap, []string{"2:2"}, "5:5", 0, WindingPositive, "", "2:2")
}

func TestWinding_SiblingPairLoop(t *testing.T) {
	expectDegenerateWindingResult(t, zeroSnap, []string{"2:2, 3:3"}, "5:5", 0, WindingPositive, "", "2:2, 3:3")
}

func TestWinding_Rectangle(t *testing.T) {
	r := []string{"0:0, 0:10, 10:10, 10:0"}
	expectWindingResult(t, zeroSnap, false, r, "5:5", 1, WindingPositive, "0:0, 0:10, 10:10, 10:0")
	expectWindingResult(t, zeroSnap, false, r, "5:5", 1, WindingNegative, "")
	expectWindingResult(t, zeroSnap, false, r, "5:5", 1, WindingNonZero, "0:0, 0:10, 10:10, 10:0")
	expectWindingResult(t, zeroSnap, false, r, "5:5", 1, WindingOdd, "0:0, 0:10, 10:10, 10:0")
}

func TestWinding_BowTie(t *testing.T) {
	s := IdentitySnapFunction{Radius: s1.Degree}
	b := []string{"5:-5, -5:5, 5:5, -5:-5"}
	expectWindingResult(t, s, false, b, "10:0", 0, WindingPositive, "0:0, -5:5, 5:5")
	expectWindingResult(t, s, false, b, "10:0", 0, WindingNegative, "-5:-5, 0:0, 5:-5")
	expectWindingResult(t, s, false, b, "10:0", 0, WindingNonZero, "0:0, -5:5, 5:5; -5:-5, 0:0, 5:-5")
	expectWindingResult(t, s, false, b, "10:0", 0, WindingOdd, "0:0, -5:5, 5:5; -5:-5, 0:0, 5:-5")
}

func TestWinding_CollapsingShell(t *testing.T) {
	s := IdentitySnapFunction{Radius: 5 * s1.Degree}
	expectDegenerateWindingResult(t, s, []string{"0:0, 0:3, 3:3"}, "10:0", 0, WindingPositive, "", "0:0")
	expectDegenerateWindingResult(t, s, []string{"0:0, 0:3, 3:3"}, "1:1", 1, WindingPositive, "", "0:0")
	expectWindingResult(t, s, false, []string{"0:0, 3:3, 0:3"}, "10:0", 1, WindingPositive, "full")
	expectWindingResult(t, s, false, []string{"0:0, 3:3, 0:3"}, "1:1", 0, WindingPositive, "full")
}

func TestWinding_TouchingTriangles(t *testing.T) {
	l := []string{"0:0, 0:8, 8:8", "0:0, 8:8, 8:0"}
	expectWindingResult(t, zeroSnap, false, l, "1:1", 1, WindingPositive, "0:0, 0:8, 8:8, 8:0")
	expectDegenerateWindingResult(t, zeroSnap, l, "2:2", 1, WindingOdd,
		"0:0, 0:8, 8:8, 8:0", "0:0, 0:8, 8:8; 0:0, 8:8, 8:0")
}

func TestWinding_TouchingTrianglesAfterSnapping(t *testing.T) {
	s := NewIntLatLngSnapFunction(0)
	l := []string{"0.1:0.2, 0:7.8, 7.6:8.2", "0.3:0.2, 8.1:7.8, 7.6:0.4"}
	expectWindingResult(t, s, false, l, "6:2", 1, WindingPositive, "0:0, 0:8, 8:8, 8:0")
	expectDegenerateWindingResult(t, s, l, "2:6", 1, WindingOdd,
		"0:0, 0:8, 8:8, 8:0", "0:0, 0:8, 8:8; 0:0, 8:8, 8:0")
}

func TestWinding_UnionOfSquares(t *testing.T) {
	s := NewIntLatLngSnapFunction(1)
	l := []string{"0:0, 0:4, 4:4, 4:0", "1:1, 1:5, 5:5, 5:1", "2:2, 2:6, 6:6, 6:2",
		"3:3, 3:7, 7:7, 7:3", "4:4, 4:8, 8:8, 8:4"}
	expectWindingResult(t, s, false, l, "0.5:0.5", 1, WindingPositive,
		"7:4, 7:3, 6:3, 6:2, 5:2, 5:1, 4:1, 4:0, 0:0, 0:4, "+
			"1:4, 1:5, 2:5, 2:6, 3:6, 3:7, 4:7, 4:8, 8:8, 8:4")
	expectWindingResult(t, s, false, l, "0.5:0.5", 0, WindingPositive,
		"6:4, 6:3, 5:3, 5:2, 4:2, 4:1, 1:1, 1:4, 2:4, 2:5, "+
			"3:5, 3:6, 4:6, 4:7, 7:7, 7:4")
	expectWindingResult(t, s, false, l, "0.5:0.5", -1, WindingPositive,
		"5:4, 5:3, 4:3, 4:2, 2:2, 2:4, 3:4, 3:5, 4:5, 4:6, 6:6, 6:4")
	expectWindingResult(t, s, false, l, "0.5:0.5", -2, WindingPositive,
		"3:3, 3:4, 4:4, 4:3; 4:4, 4:5, 5:5, 5:4")
	expectWindingResult(t, s, false, l, "0.5:0.5", 1, WindingOdd,
		"4:1, 4:0, 0:0, 0:4, 1:4, 1:1; "+
			"4:3, 4:2, 2:2, 2:4, 3:4, 3:3; "+
			"1:4, 1:5, 2:5, 2:4; "+
			"5:4, 5:3, 4:3, 4:4; "+
			"5:2, 5:1, 4:1, 4:2; "+
			"2:5, 2:6, 3:6, 3:5; "+
			"6:3, 6:2, 5:2, 5:3; "+
			"3:6, 3:7, 4:7, 4:6; "+
			"3:4, 3:5, 4:5, 4:4; "+
			"7:4, 7:3, 6:3, 6:4; "+
			"4:7, 4:8, 8:8, 8:4, 7:4, 7:7; "+
			"4:5, 4:6, 6:6, 6:4, 5:4, 5:5")
}

func TestWinding_SymmetricDifferenceDegeneracies(t *testing.T) {
	expectDegenerateWindingResult(t, NewIntLatLngSnapFunction(1),
		[]string{"0:0, 0:3, 3:3, 3:0", "1:1", "2:2", "4:4", "0:0, 0:3, 3:3, 3:0", "1:1", "4:4", "5:5"},
		"10:10", 0, WindingOdd, "", "2:2; 5:5")
}
