package engine

import (
	"fmt"
	"sort"
	"testing"
)

func distinctElems(d *distinctSet) []string {
	c := *d
	c.str = map[string]struct{}{}
	for k := range d.str {
		c.str[k] = struct{}{}
	}
	c.codes = map[int32]struct{}{}
	for k := range d.codes {
		c.codes[k] = struct{}{}
	}
	c.bits = append([]uint64(nil), d.bits...)
	c.toStr()
	var out []string
	for k := range c.str {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestDistinctSetCoded(t *testing.T) {
	dict := make([]string, 1000)
	for i := range dict {
		dict[i] = fmt.Sprintf("v%04d", i)
	}
	other := append([]string(nil), dict...) // same contents, other backing

	var a distinctSet
	for _, c := range []int{3, 3, 7} {
		a.addCode(dict, c)
	}
	if a.count() != 2 || a.bits != nil {
		t.Fatalf("sparse: count %d bits %v", a.count(), a.bits != nil)
	}
	for c := 0; c < 50; c++ { // past the bitset threshold
		a.addCode(dict, c*2)
	}
	if a.bits == nil || a.count() != 52 { // 50 evens 0..98 plus 3, 7
		t.Fatalf("dense: count %d bits %v", a.count(), a.bits != nil)
	}

	var b distinctSet
	b.addCode(dict, 999)
	b.addCode(dict, 3)
	b.union(&a)
	if b.count() != 53 || b.str != nil { // plus 999
		t.Fatalf("coded union: count %d str %v", b.count(), b.str != nil)
	}

	// A different dictionary falls back to strings, losing nothing.
	var c distinctSet
	c.addCode(other, 999)
	c.addCode(other, 500)
	c.union(&b)
	if c.count() != 54 || c.dict != nil { // plus 500
		t.Fatalf("mixed union: count %d", c.count())
	}
	if got := distinctElems(&c); len(got) != 54 || got[0] != "v0000" || got[53] != "v0999" {
		t.Fatalf("elements %v", got)
	}

	// Codes then plain strings (an extent without the dictionary).
	var d distinctSet
	d.addCode(dict, 1)
	d.toStr()
	d.str["v0001"] = struct{}{}
	d.str["zz"] = struct{}{}
	if d.count() != 2 {
		t.Fatalf("toStr: count %d", d.count())
	}
}
