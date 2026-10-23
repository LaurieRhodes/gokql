package h3

// Expected values in testdata/ were produced by the Kusto emulator
// (geo_point_to_h3cell, geo_h3cell_to_central_point, _to_polygon,
// _level, _parent, _children).

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestEmulatorCells(t *testing.T) {
	f, err := os.Open("testdata/points.tsv")
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(f)
	sc.Scan()
	n, bad := 0, 0
	for sc.Scan() {
		p := strings.Split(sc.Text(), "\t")
		lng, _ := strconv.ParseFloat(p[0], 64)
		lat, _ := strconv.ParseFloat(p[1], 64)
		r, _ := strconv.Atoi(p[2])
		c, err := LatLngToCell(LatLng{lat * mPi180, lng * mPi180}, r)
		n++
		if err != nil || c.String() != p[3] {
			bad++
			if bad < 10 {
				t.Errorf("%v %v r%d: got %v %v want %s", lng, lat, r, c, err, p[3])
			}
		}
	}
	t.Logf("%d/%d cells match", n-bad, n)
}

func TestEmulatorGeometry(t *testing.T) {
	f, err := os.Open("testdata/cells.tsv")
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	sc.Scan()
	n, badC, badP, badPar, badCh := 0, 0, 0, 0, 0
	for sc.Scan() {
		p := strings.Split(sc.Text(), "\t")
		c, ok := ParseCell(p[0])
		if !ok {
			t.Fatalf("parse %s", p[0])
		}
		n++
		var pt struct{ Coordinates []float64 }
		json.Unmarshal([]byte(p[1]), &pt)
		ll := c.LatLng()
		if math.Abs(ll.Lng*m180Pi-pt.Coordinates[0]) > 1e-9 || math.Abs(ll.Lat*m180Pi-pt.Coordinates[1]) > 1e-9 {
			badC++
			if badC < 5 {
				t.Errorf("%s center %v %v want %v", p[0], ll.Lng*m180Pi, ll.Lat*m180Pi, pt.Coordinates)
			}
		}
		var poly struct{ Coordinates [][][]float64 }
		json.Unmarshal([]byte(p[2]), &poly)
		b := c.Boundary()
		ring := poly.Coordinates[0]
		match := len(ring) == len(b)+1
		// Vertices agree to ~1e-12 degrees, except the extra vertices
		// where a class III edge crosses an icosahedron edge: those
		// differ from ADX's by up to ~5e-8 degrees (a few millimetres).
		for i := 0; match && i < len(b); i++ {
			if math.Abs(b[i].Lng*m180Pi-ring[i][0]) > 1e-7 || math.Abs(b[i].Lat*m180Pi-ring[i][1]) > 1e-7 {
				match = false
			}
		}
		if !match {
			badP++
			if badP < 5 {
				var got [][]float64
				for _, v := range b {
					got = append(got, []float64{v.Lng * m180Pi, v.Lat * m180Pi})
				}
				t.Errorf("%s boundary %v\n want %v", p[0], got, ring)
			}
		}
		if p[4] != "" {
			par, _ := c.Parent(c.Resolution() - 1)
			if par.String() != p[4] {
				badPar++
			}
		}
		if c.Resolution() < 15 {
			ch, _ := c.Children(c.Resolution() + 1)
			var want []string
			json.Unmarshal([]byte(p[5]), &want)
			var got []string
			for _, x := range ch {
				got = append(got, x.String())
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				badCh++
				if badCh < 3 {
					t.Errorf("%s children %v want %v", p[0], got, want)
				}
			}
		}
	}
	t.Logf("%d cells: center bad %d, boundary bad %d, parent bad %d, children bad %d", n, badC, badP, badPar, badCh)
}

// Ring and disk order as the emulator returns it from geo_h3cell_rings
// (gridDiskDistances grouped by distance), including pentagons.
func TestEmulatorRings(t *testing.T) {
	cases := []struct {
		cell string
		k    int
		want [][]string
	}{
		{"862a1072fffffff", 2, [][]string{{"862a1072fffffff"},
			{"862a10727ffffff", "862a10707ffffff", "862a1070fffffff", "862a10777ffffff", "862a100dfffffff", "862a100d7ffffff"},
			{"862a1008fffffff", "862a1009fffffff", "862a10737ffffff", "862a10717ffffff", "862a1071fffffff", "862a10627ffffff", "862a10757ffffff", "862a10747ffffff", "862a10767ffffff", "862a100cfffffff", "862a100c7ffffff", "862a100f7ffffff"}}},
		{"8009fffffffffff", 1, [][]string{{"8009fffffffffff"}, {"8007fffffffffff", "8019fffffffffff", "8001fffffffffff", "801ffffffffffff", "8011fffffffffff"}}},
		{"8009fffffffffff", 2, [][]string{{"8009fffffffffff"}, {"8019fffffffffff", "8011fffffffffff", "8001fffffffffff", "8007fffffffffff", "801ffffffffffff"},
			{"8039fffffffffff", "800ffffffffffff", "8035fffffffffff", "800bfffffffffff", "802dfffffffffff", "8005fffffffffff", "8003fffffffffff", "8021fffffffffff", "801bfffffffffff", "803ffffffffffff"}}},
		{"85080003fffffff", 2, [][]string{{"85080003fffffff"}, {"8508000ffffffff", "85080017fffffff", "8508000bfffffff", "85080013fffffff", "8508001bfffffff"},
			{"8508008bfffffff", "85080077fffffff", "85080047fffffff", "85080073fffffff", "85080057fffffff", "850800abfffffff", "8508008ffffffff", "850800c7fffffff", "850800cffffffff", "850800bbfffffff"}}},
	}
	for _, tc := range cases {
		c, _ := ParseCell(tc.cell)
		cells, dist, err := c.GridDiskDistances(tc.k)
		if err != nil {
			t.Fatal(err)
		}
		got := make([][]string, tc.k+1)
		for i, x := range cells {
			got[dist[i]] = append(got[dist[i]], x.String())
		}
		if strings.Join(flatten(got), ",") != strings.Join(flatten(tc.want), ",") {
			t.Errorf("%s k=%d:\n got %v\nwant %v", tc.cell, tc.k, got, tc.want)
		}
	}
}

func flatten(x [][]string) []string {
	var out []string
	for _, r := range x {
		out = append(out, r...)
		out = append(out, "|")
	}
	return out
}

// polygonToCells: same cell sets as the emulator's geo_polygon_to_h3cells.
func TestEmulatorPolygonToCells(t *testing.T) {
	pts := [][2]float64{{-3.659, 40.553}, {-3.913, 40.409}, {-3.729, 40.273}, {-3.524, 40.440}}
	var l []LatLng
	for _, p := range pts {
		l = append(l, LatLng{DegsToRads(p[1]), DegsToRads(p[0])})
	}
	gp := &GeoPolygon{Outer: l}
	for res, want := range map[int]int{4: 1, 6: 13, 7: 93, 9: 4635} {
		cs, ok := PolygonToCells(gp, res)
		if !ok || len(cs) != want {
			t.Errorf("res %d: %d cells (ok %v), want %d", res, len(cs), ok, want)
		}
	}
}
