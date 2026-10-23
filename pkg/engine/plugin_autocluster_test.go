package engine

import (
	"strings"
	"testing"
)

// Expected segments from the Kusto emulator (engine 2025.07.06).
func TestAutocluster(t *testing.T) {
	src := `datatable(A:string, B:string, C:long)["x","p",1,"x","p",1,"x","q",1,"x","p",2,"y","p",2,"y","q",3,"x","p",1,"z","r",3,"x","q",1,"y","p",2]`
	cases := []struct {
		sw   string
		want []string // Count|A|B|C per segment, in order
	}{
		{"0.05", []string{"3|x|p|1", "2|x|q|1", "2|y|p|2", "1|x|p|2", "1|y|q|3", "1|z|r|3"}},
		{"0.5", []string{"3|x|p|1", "2|y|p|2", "1|y|q|3", "1|z|r|3"}},
		{"0.65", []string{"5|x||1", "4|x|p|", "3||p|2", "3||q|", "1|z|r|3"}},
		{"0.8", []string{"5|x||1", "6||p|", "3||q|", "2|||3"}},
		{"0.95", []string{"6|x||", "6||p|", "3||q|", "2|||3"}},
	}
	for _, c := range cases {
		tbl := queryResult(t, src+` | evaluate autocluster(`+c.sw+`)`)
		expectColNames(t, tbl, "SegmentId", "Count", "Percent", "A", "B", "C")
		var got []string
		for i := range tbl.Rows {
			got = append(got, strings.Join([]string{cell(t, tbl, i, 1), cell(t, tbl, i, 3), cell(t, tbl, i, 4), cell(t, tbl, i, 5)}, "|"))
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("SizeWeight %s: got %v, want %v", c.sw, got, c.want)
		}
	}
	tbl := queryResult(t, src+` | evaluate autocluster(0.95, '~', '~', '*', long(-1))`)
	expectCell(t, tbl, 0, 4, "*")
	expectCell(t, tbl, 0, 5, "-1")
}
