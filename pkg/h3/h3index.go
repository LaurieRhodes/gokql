package h3

import (
	"fmt"
	"math"
	"math/bits"
	"strconv"
)

// Port of h3Index.c (H3 v4.5.0).

// Cell is an H3 cell index.
type Cell uint64

const (
	modeOffset     = 59
	bcOffset       = 45
	resOffset      = 52
	reservedOffset = 56
	perDigitOffset = 3
	highBitMask    = uint64(1) << 63
	modeMask       = uint64(15) << modeOffset
	bcMask         = uint64(127) << bcOffset
	resMask        = uint64(15) << resOffset
	reservedMask   = uint64(7) << reservedOffset
	digitMask      = uint64(7)
	h3Init         = uint64(35184372088831)
)

func (h Cell) Resolution() int { return int((uint64(h) & resMask) >> resOffset) }
func (h Cell) BaseCell() int   { return int((uint64(h) & bcMask) >> bcOffset) }
func (h Cell) mode() int       { return int((uint64(h) & modeMask) >> modeOffset) }

func (h Cell) digit(r int) int {
	return int((uint64(h) >> ((MaxRes - r) * perDigitOffset)) & digitMask)
}

func (h *Cell) setMode(v int) { *h = Cell(uint64(*h)&^modeMask | uint64(v)<<modeOffset) }
func (h *Cell) setBaseCell(v int) {
	*h = Cell(uint64(*h)&^bcMask | uint64(v)<<bcOffset)
}
func (h *Cell) setResolution(v int) {
	*h = Cell(uint64(*h)&^resMask | uint64(v)<<resOffset)
}
func (h *Cell) setDigit(r, d int) {
	shift := (MaxRes - r) * perDigitOffset
	*h = Cell(uint64(*h)&^(digitMask<<shift) | uint64(d)<<shift)
}

// String is the cell's lowercase hex form, as H3 and ADX print it.
func (h Cell) String() string { return strconv.FormatUint(uint64(h), 16) }

// ParseCell reads a hex cell index; ok is false for a malformed or
// invalid one.
func ParseCell(s string) (Cell, bool) {
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0, false
	}
	h := Cell(v)
	return h, h.IsValid()
}

var isBaseCellPentagonArr = func() [128]bool {
	var a [128]bool
	for bc := 0; bc < numBaseCells; bc++ {
		a[bc] = baseCellData[bc].isPentagon != 0
	}
	return a
}()

// IsValid reports whether h is a valid cell index.
func (h Cell) IsValid() bool {
	u := uint64(h)
	if u>>56 != 0b00001000 {
		return false
	}
	res := h.Resolution()
	bc := h.BaseCell()
	if bc >= numBaseCells {
		return false
	}
	// no digit 1..res is 7
	const mhi = uint64(0b100100100100100100100100100100100100100100100)
	const mlo = mhi >> 2
	shift := 3 * (15 - res)
	x := u >> shift << shift
	if x&mhi&(^x-mlo) != 0 {
		return false
	}
	// all digits after res are 7
	if res < 15 {
		s := 19 + 3*res
		y := ^u << s >> s
		if y != 0 {
			return false
		}
	}
	// no deleted subsequence in a pentagon
	if isBaseCellPentagonArr[bc] {
		y := u << 19 >> 19
		if y != 0 && (63-bits.LeadingZeros64(y))%3 == 0 {
			return false
		}
	}
	return true
}

func setH3Index(res, baseCell, initDigit int) Cell {
	h := Cell(h3Init)
	h.setMode(cellMode)
	h.setResolution(res)
	h.setBaseCell(baseCell)
	for r := 1; r <= res; r++ {
		h.setDigit(r, initDigit)
	}
	return h
}

// Parent is h's ancestor at parentRes (ok false when parentRes is
// out of range or finer than h).
func (h Cell) Parent(parentRes int) (Cell, bool) {
	childRes := h.Resolution()
	if parentRes < 0 || parentRes > MaxRes || parentRes > childRes {
		return 0, false
	}
	p := h
	p.setResolution(parentRes)
	for i := parentRes + 1; i <= childRes; i++ {
		p.setDigit(i, int(digitMask))
	}
	return p, true
}

// IsPentagon reports whether h is one of the 12 pentagons of its
// resolution.
func (h Cell) IsPentagon() bool {
	return isBaseCellPentagon(h.BaseCell()) && h.leadingNonZeroDigit() == centerDigit
}

func (h Cell) leadingNonZeroDigit() int {
	for r := 1; r <= h.Resolution(); r++ {
		if d := h.digit(r); d != 0 {
			return d
		}
	}
	return centerDigit
}

// Children are h's descendants at childRes, in digit order (as H3's
// child iterator yields them); a pentagon skips its deleted
// sub-sequence. ok is false when childRes is coarser than h.
func (h Cell) Children(childRes int) ([]Cell, bool) {
	res := h.Resolution()
	if childRes < res || childRes > MaxRes {
		return nil, false
	}
	var out []Cell
	var walk func(c Cell, pent bool)
	walk = func(c Cell, pent bool) {
		r := c.Resolution()
		if r == childRes {
			out = append(out, c)
			return
		}
		for d := 0; d < 7; d++ {
			if pent && d == pentagonSkippedDigit {
				continue
			}
			ch := c
			ch.setResolution(r + 1)
			ch.setDigit(r+1, d)
			walk(ch, pent && d == centerDigit)
		}
	}
	walk(h, h.IsPentagon())
	return out, true
}

func (h Cell) rotatePent60ccw() Cell {
	found := false
	for r, res := 1, h.Resolution(); r <= res; r++ {
		h.setDigit(r, rotate60ccwDigit(h.digit(r)))
		if !found && h.digit(r) != 0 {
			found = true
			if h.leadingNonZeroDigit() == kAxesDigit {
				h = h.rotate60ccw()
			}
		}
	}
	return h
}

func (h Cell) rotatePent60cw() Cell {
	found := false
	for r, res := 1, h.Resolution(); r <= res; r++ {
		h.setDigit(r, rotate60cwDigit(h.digit(r)))
		if !found && h.digit(r) != 0 {
			found = true
			if h.leadingNonZeroDigit() == kAxesDigit {
				h = h.rotate60cw()
			}
		}
	}
	return h
}

func (h Cell) rotate60ccw() Cell {
	for r, res := 1, h.Resolution(); r <= res; r++ {
		h.setDigit(r, rotate60ccwDigit(h.digit(r)))
	}
	return h
}

func (h Cell) rotate60cw() Cell {
	for r, res := 1, h.Resolution(); r <= res; r++ {
		h.setDigit(r, rotate60cwDigit(h.digit(r)))
	}
	return h
}

func faceIjkToH3(fijk faceIJK, res int) Cell {
	h := Cell(h3Init)
	h.setMode(cellMode)
	h.setResolution(res)
	if res == 0 {
		if fijk.coord.i > maxFaceCoord || fijk.coord.j > maxFaceCoord || fijk.coord.k > maxFaceCoord {
			return 0
		}
		h.setBaseCell(faceIjkToBaseCell(fijk))
		return h
	}
	fijkBC := fijk
	ijk := &fijkBC.coord
	for r := res - 1; r >= 0; r-- {
		lastIJK := *ijk
		var lastCenter coordIJK
		if isResolutionClassIII(r + 1) {
			ijk.upAp7()
			lastCenter = *ijk
			lastCenter.downAp7()
		} else {
			ijk.upAp7r()
			lastCenter = *ijk
			lastCenter.downAp7r()
		}
		diff := ijkSub(lastIJK, lastCenter)
		diff.normalize()
		h.setDigit(r+1, unitIjkToDigit(diff))
	}
	if fijkBC.coord.i > maxFaceCoord || fijkBC.coord.j > maxFaceCoord || fijkBC.coord.k > maxFaceCoord {
		return 0
	}
	baseCell := faceIjkToBaseCell(fijkBC)
	h.setBaseCell(baseCell)
	numRots := faceIjkToBaseCellCCWrot60(fijkBC)
	if isBaseCellPentagon(baseCell) {
		if h.leadingNonZeroDigit() == kAxesDigit {
			if baseCellIsCwOffset(baseCell, fijkBC.face) {
				h = h.rotate60cw()
			} else {
				h = h.rotate60ccw()
			}
		}
		for i := 0; i < numRots; i++ {
			h = h.rotatePent60ccw()
		}
	} else {
		for i := 0; i < numRots; i++ {
			h = h.rotate60ccw()
		}
	}
	return h
}

// LatLngToCell is the cell at res containing the point (radians).
func LatLngToCell(g LatLng, res int) (Cell, error) {
	if res < 0 || res > MaxRes {
		return 0, fmt.Errorf("h3: resolution %d out of range", res)
	}
	if math.IsInf(g.Lat, 0) || math.IsNaN(g.Lat) || math.IsInf(g.Lng, 0) || math.IsNaN(g.Lng) {
		return 0, fmt.Errorf("h3: non-finite coordinate")
	}
	h := faceIjkToH3(vec3ToFaceIjk(latLngToVec3(g), res), res)
	if h == 0 {
		return 0, fmt.Errorf("h3: no cell")
	}
	return h, nil
}

func h3ToFaceIjkWithInitializedFijk(h Cell, fijk *faceIJK) bool {
	ijk := &fijk.coord
	res := h.Resolution()
	possibleOverage := true
	if !isBaseCellPentagon(h.BaseCell()) && (res == 0 || (ijk.i == 0 && ijk.j == 0 && ijk.k == 0)) {
		possibleOverage = false
	}
	for r := 1; r <= res; r++ {
		if isResolutionClassIII(r) {
			ijk.downAp7()
		} else {
			ijk.downAp7r()
		}
		ijk.neighbor(h.digit(r))
	}
	return possibleOverage
}

func h3ToFaceIjk(h Cell) faceIJK {
	baseCell := h.BaseCell()
	if isBaseCellPentagon(baseCell) && h.leadingNonZeroDigit() == 5 {
		h = h.rotate60cw()
	}
	fijk := baseCellData[baseCell].homeFijk
	if !h3ToFaceIjkWithInitializedFijk(h, &fijk) {
		return fijk
	}
	origIJK := fijk.coord
	res := h.Resolution()
	if isResolutionClassIII(res) {
		fijk.coord.downAp7r()
		res++
	}
	pentLeading4 := isBaseCellPentagon(baseCell) && h.leadingNonZeroDigit() == 4
	if adjustOverageClassII(&fijk, res, pentLeading4, false) != noOverage {
		if isBaseCellPentagon(baseCell) {
			for adjustOverageClassII(&fijk, res, false, false) != noOverage {
			}
		}
		if res != h.Resolution() {
			fijk.coord.upAp7r()
		}
	} else if res != h.Resolution() {
		fijk.coord = origIJK
	}
	return fijk
}

// LatLng is h's center (radians).
func (h Cell) LatLng() LatLng {
	return vec3ToLatLng(faceIjkToVec3(h3ToFaceIjk(h), h.Resolution()))
}

// Boundary is h's boundary vertices (radians), counter-clockwise.
func (h Cell) Boundary() []LatLng {
	fijk := h3ToFaceIjk(h)
	if h.IsPentagon() {
		return faceIjkPentToCellBoundary(fijk, h.Resolution(), 0, numPentVerts)
	}
	return faceIjkToCellBoundary(fijk, h.Resolution(), 0, numHexVerts)
}
