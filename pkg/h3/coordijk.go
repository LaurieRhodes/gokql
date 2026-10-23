// Package h3 is a pure-Go port of the core of Uber's H3 hexagonal
// geospatial indexing library (https://github.com/uber/h3, v4.5.0,
// Apache License 2.0; see LICENSE in this directory): converting
// points to cells, cells to centres and boundaries, the hierarchy
// (parent, children) and grid disks. It is a port, not a binding, so
// gokql builds without cgo.
package h3

import "math"

// coordIJK is a hex coordinate in the i, j, k axes system.
type coordIJK struct{ i, j, k int }

// coordIJ is a hex coordinate in the i, j axes system.
type coordIJ struct{ i, j int }

// Direction digits (H3's Direction enum).
const (
	centerDigit  = 0
	kAxesDigit   = 1
	jAxesDigit   = 2
	jkAxesDigit  = 3
	iAxesDigit   = 4
	ikAxesDigit  = 5
	ijAxesDigit  = 6
	invalidDigit = 7
	numDigits    = invalidDigit
	// pentagonSkippedDigit is the child digit skipped for pentagons.
	pentagonSkippedDigit = kAxesDigit
)

var unitVecs = [7]coordIJK{
	{0, 0, 0}, {0, 0, 1}, {0, 1, 0}, {0, 1, 1}, {1, 0, 0}, {1, 0, 1}, {1, 1, 0},
}

func ijkAdd(a, b coordIJK) coordIJK { return coordIJK{a.i + b.i, a.j + b.j, a.k + b.k} }

func ijkSub(a, b coordIJK) coordIJK { return coordIJK{a.i - b.i, a.j - b.j, a.k - b.k} }

func (c *coordIJK) scale(f int) { c.i *= f; c.j *= f; c.k *= f }

// normalize puts c in normal form: non-negative, minimum zero.
func (c *coordIJK) normalize() {
	if c.i < 0 {
		c.j -= c.i
		c.k -= c.i
		c.i = 0
	}
	if c.j < 0 {
		c.i -= c.j
		c.k -= c.j
		c.j = 0
	}
	if c.k < 0 {
		c.i -= c.k
		c.j -= c.k
		c.k = 0
	}
	m := c.i
	if c.j < m {
		m = c.j
	}
	if c.k < m {
		m = c.k
	}
	if m > 0 {
		c.i -= m
		c.j -= m
		c.k -= m
	}
}

// ijkToHex2d is the center point of a hex in 2D cartesian coordinates.
func ijkToHex2d(h coordIJK) vec2d {
	i := h.i - h.k
	j := h.j - h.k
	return vec2d{float64(i) - 0.5*float64(j), float64(j) * sqrt3_2}
}

// hex2dToCoordIJK is the hex containing a 2D cartesian point.
func hex2dToCoordIJK(v vec2d) coordIJK {
	var h coordIJK
	a1 := math.Abs(v.x)
	a2 := math.Abs(v.y)
	x2 := a2 * rsin60
	x1 := a1 + x2/2.0
	m1 := int(x1)
	m2 := int(x2)
	r1 := x1 - float64(m1)
	r2 := x2 - float64(m2)
	if r1 < 0.5 {
		if r1 < 1.0/3.0 {
			if r2 < (1.0+r1)/2.0 {
				h.i, h.j = m1, m2
			} else {
				h.i, h.j = m1, m2+1
			}
		} else {
			if r2 < (1.0 - r1) {
				h.j = m2
			} else {
				h.j = m2 + 1
			}
			if (1.0-r1) <= r2 && r2 < (2.0*r1) {
				h.i = m1 + 1
			} else {
				h.i = m1
			}
		}
	} else {
		if r1 < 2.0/3.0 {
			if r2 < (1.0 - r1) {
				h.j = m2
			} else {
				h.j = m2 + 1
			}
			if (2.0*r1-1.0) < r2 && r2 < (1.0-r1) {
				h.i = m1
			} else {
				h.i = m1 + 1
			}
		} else {
			if r2 < (r1 / 2.0) {
				h.i, h.j = m1+1, m2
			} else {
				h.i, h.j = m1+1, m2+1
			}
		}
	}
	// fold across the axes if necessary
	if v.x < 0.0 {
		if h.j%2 == 0 {
			axisi := h.j / 2
			diff := h.i - axisi
			h.i = int(float64(h.i) - 2.0*float64(diff))
		} else {
			axisi := (h.j + 1) / 2
			diff := h.i - axisi
			h.i = int(float64(h.i) - (2.0*float64(diff) + 1))
		}
	}
	if v.y < 0.0 {
		h.i = h.i - (2*h.j+1)/2
		h.j = -1 * h.j
	}
	h.normalize()
	return h
}

// unitIjkToDigit is the direction digit of a unit vector, or invalidDigit.
func unitIjkToDigit(ijk coordIJK) int {
	c := ijk
	c.normalize()
	for d := centerDigit; d < numDigits; d++ {
		if c == unitVecs[d] {
			return d
		}
	}
	return invalidDigit
}

// lround rounds half away from zero, as C's lround.
func lround(x float64) int { return int(math.Round(x)) }

// upAp7 moves c to its parent's coordinates (counter-clockwise
// aperture 7 grid).
func (c *coordIJK) upAp7() {
	i := c.i - c.k
	j := c.j - c.k
	c.i = lround(float64(3*i-j) * oneSeventh)
	c.j = lround(float64(i+2*j) * oneSeventh)
	c.k = 0
	c.normalize()
}

// upAp7r is upAp7 for the clockwise aperture 7 grid.
func (c *coordIJK) upAp7r() {
	i := c.i - c.k
	j := c.j - c.k
	c.i = lround(float64(2*i+j) * oneSeventh)
	c.j = lround(float64(3*j-i) * oneSeventh)
	c.k = 0
	c.normalize()
}

func (c *coordIJK) down(iv, jv, kv coordIJK) {
	iv.scale(c.i)
	jv.scale(c.j)
	kv.scale(c.k)
	*c = ijkAdd(ijkAdd(iv, jv), kv)
	c.normalize()
}

// downAp7 moves c to its center child's coordinates (counter-clockwise).
func (c *coordIJK) downAp7() { c.down(coordIJK{3, 0, 1}, coordIJK{1, 3, 0}, coordIJK{0, 1, 3}) }

// downAp7r moves c to its center child's coordinates (clockwise).
func (c *coordIJK) downAp7r() { c.down(coordIJK{3, 1, 0}, coordIJK{0, 3, 1}, coordIJK{1, 0, 3}) }

// downAp3 finds the center of c in the next finer aperture 3 grid (ccw).
func (c *coordIJK) downAp3() { c.down(coordIJK{2, 0, 1}, coordIJK{1, 2, 0}, coordIJK{0, 1, 2}) }

// downAp3r finds the center of c in the next finer aperture 3 grid (cw).
func (c *coordIJK) downAp3r() { c.down(coordIJK{2, 1, 0}, coordIJK{0, 2, 1}, coordIJK{1, 0, 2}) }

// neighbor moves c one step in the digit's direction.
func (c *coordIJK) neighbor(digit int) {
	if digit > centerDigit && digit < numDigits {
		*c = ijkAdd(*c, unitVecs[digit])
		c.normalize()
	}
}

// rotate60ccw rotates c 60 degrees counter-clockwise.
func (c *coordIJK) rotate60ccw() { c.down(coordIJK{1, 1, 0}, coordIJK{0, 1, 1}, coordIJK{1, 0, 1}) }

// rotate60cw rotates c 60 degrees clockwise.
func (c *coordIJK) rotate60cw() { c.down(coordIJK{1, 0, 1}, coordIJK{1, 1, 0}, coordIJK{0, 1, 1}) }

// rotate60ccwDigit rotates a direction digit 60 degrees ccw.
func rotate60ccwDigit(d int) int {
	switch d {
	case kAxesDigit:
		return ikAxesDigit
	case ikAxesDigit:
		return iAxesDigit
	case iAxesDigit:
		return ijAxesDigit
	case ijAxesDigit:
		return jAxesDigit
	case jAxesDigit:
		return jkAxesDigit
	case jkAxesDigit:
		return kAxesDigit
	}
	return d
}

// rotate60cwDigit rotates a direction digit 60 degrees cw.
func rotate60cwDigit(d int) int {
	switch d {
	case kAxesDigit:
		return jkAxesDigit
	case jkAxesDigit:
		return jAxesDigit
	case jAxesDigit:
		return ijAxesDigit
	case ijAxesDigit:
		return iAxesDigit
	case iAxesDigit:
		return ikAxesDigit
	case ikAxesDigit:
		return kAxesDigit
	}
	return d
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ijkDistance is the grid distance between two coordinates.
func ijkDistance(a, b coordIJK) int {
	d := ijkSub(a, b)
	d.normalize()
	return max(absInt(d.i), absInt(d.j), absInt(d.k))
}

func ijkToIj(c coordIJK) coordIJ { return coordIJ{c.i - c.k, c.j - c.k} }

func ijToIjk(ij coordIJ) coordIJK {
	c := coordIJK{ij.i, ij.j, 0}
	c.normalize()
	return c
}
