package h3

// Port of the grid traversal parts of algos.c (H3 v4.5.0).

import "errors"

var (
	errPentagon    = errors.New("h3: pentagon distortion")
	errCellInvalid = errors.New("h3: invalid cell")
	errFailed      = errors.New("h3: failed")
	errDomain      = errors.New("h3: argument out of domain")
)

var directions = [6]int{jAxesDigit, jkAxesDigit, kAxesDigit, ikAxesDigit, iAxesDigit, ijAxesDigit}

const nextRingDirection = iAxesDigit

const (
	c0  = centerDigit
	dK  = kAxesDigit
	dJ  = jAxesDigit
	dJK = jkAxesDigit
	dI  = iAxesDigit
	dIK = ikAxesDigit
	dIJ = ijAxesDigit
)

var newDigitII = [7][7]int{
	{c0, dK, dJ, dJK, dI, dIK, dIJ},
	{dK, dI, dJK, dIJ, dIK, dJ, c0},
	{dJ, dJK, dK, dI, dIJ, c0, dIK},
	{dJK, dIJ, dI, dIK, c0, dK, dJ},
	{dI, dIK, dIJ, c0, dJ, dJK, dK},
	{dIK, dJ, c0, dK, dJK, dIJ, dI},
	{dIJ, c0, dIK, dJ, dK, dI, dJK},
}

var newAdjustmentII = [7][7]int{
	{c0, c0, c0, c0, c0, c0, c0},
	{c0, dK, c0, dK, c0, dIK, c0},
	{c0, c0, dJ, dJK, c0, c0, dJ},
	{c0, dK, dJK, dJK, c0, c0, c0},
	{c0, c0, c0, c0, dI, dI, dIJ},
	{c0, dIK, c0, c0, dI, dIK, c0},
	{c0, c0, dJ, c0, dIJ, c0, dIJ},
}

var newDigitIII = [7][7]int{
	{c0, dK, dJ, dJK, dI, dIK, dIJ},
	{dK, dJ, dJK, dI, dIK, dIJ, c0},
	{dJ, dJK, dI, dIK, dIJ, c0, dK},
	{dJK, dI, dIK, dIJ, c0, dK, dJ},
	{dI, dIK, dIJ, c0, dK, dJ, dJK},
	{dIK, dIJ, c0, dK, dJ, dJK, dI},
	{dIJ, c0, dK, dJ, dJK, dI, dIK},
}

var newAdjustmentIII = [7][7]int{
	{c0, c0, c0, c0, c0, c0, c0},
	{c0, dK, c0, dJK, c0, dK, c0},
	{c0, c0, dJ, dJ, c0, c0, dIJ},
	{c0, dJK, dJ, dJK, c0, c0, c0},
	{c0, c0, c0, c0, dI, dIK, dI},
	{c0, dK, c0, c0, dIK, dIK, c0},
	{c0, c0, dIJ, c0, dI, c0, dIJ},
}

// neighborRotations returns the neighbor of origin in direction dir,
// updating the running rotation count (h3NeighborRotations).
func neighborRotations(origin Cell, dir int, rotations *int) (Cell, error) {
	current := origin
	if dir < centerDigit || dir >= invalidDigit {
		return 0, errFailed
	}
	*rotations = *rotations % 6
	for i := 0; i < *rotations; i++ {
		dir = rotate60ccwDigit(dir)
	}
	newRotations := 0
	oldBaseCell := current.BaseCell()
	if oldBaseCell < 0 || oldBaseCell >= numBaseCells {
		return 0, errCellInvalid
	}
	oldLeadingDigit := current.leadingNonZeroDigit()
	r := current.Resolution() - 1
	for {
		if r == -1 {
			current.setBaseCell(baseCellNeighbors[oldBaseCell][dir])
			newRotations = baseCellNeighbor60CCWRots[oldBaseCell][dir]
			if current.BaseCell() == invalidBaseCell {
				current.setBaseCell(baseCellNeighbors[oldBaseCell][ikAxesDigit])
				newRotations = baseCellNeighbor60CCWRots[oldBaseCell][ikAxesDigit]
				current = current.rotate60ccw()
				*rotations = *rotations + 1
			}
			break
		}
		oldDigit := current.digit(r + 1)
		var nextDir int
		if oldDigit == invalidDigit {
			return 0, errCellInvalid
		} else if isResolutionClassIII(r + 1) {
			current.setDigit(r+1, newDigitII[oldDigit][dir])
			nextDir = newAdjustmentII[oldDigit][dir]
		} else {
			current.setDigit(r+1, newDigitIII[oldDigit][dir])
			nextDir = newAdjustmentIII[oldDigit][dir]
		}
		if nextDir != centerDigit {
			dir = nextDir
			r--
		} else {
			break
		}
	}
	newBaseCell := current.BaseCell()
	if isBaseCellPentagon(newBaseCell) {
		alreadyAdjustedKSubsequence := false
		if current.leadingNonZeroDigit() == kAxesDigit {
			if oldBaseCell != newBaseCell {
				if baseCellIsCwOffset(newBaseCell, baseCellData[oldBaseCell].homeFijk.face) {
					current = current.rotate60cw()
				} else {
					current = current.rotate60ccw()
				}
				alreadyAdjustedKSubsequence = true
			} else {
				switch oldLeadingDigit {
				case centerDigit:
					return 0, errPentagon
				case jkAxesDigit:
					current = current.rotate60ccw()
					*rotations = *rotations + 1
				case ikAxesDigit:
					current = current.rotate60cw()
					*rotations = *rotations + 5
				default:
					return 0, errFailed
				}
			}
		}
		for i := 0; i < newRotations; i++ {
			current = current.rotatePent60ccw()
		}
		if oldBaseCell != newBaseCell {
			if isBaseCellPolarPentagon(newBaseCell) {
				if oldBaseCell != 118 && oldBaseCell != 8 && current.leadingNonZeroDigit() != jkAxesDigit {
					*rotations = *rotations + 1
				}
			} else if current.leadingNonZeroDigit() == ikAxesDigit && !alreadyAdjustedKSubsequence {
				*rotations = *rotations + 1
			}
		}
	} else {
		for i := 0; i < newRotations; i++ {
			current = current.rotate60ccw()
		}
	}
	*rotations = (*rotations + newRotations) % 6
	return current, nil
}

func maxGridDiskSize(k int) int { return 3*k*(k+1) + 1 }

// gridDiskDistancesUnsafe is the fast spiral walk; it fails near pentagons.
func gridDiskDistancesUnsafe(origin Cell, k int) ([]Cell, []int, error) {
	if k < 0 {
		return nil, nil, errDomain
	}
	out := make([]Cell, 0, maxGridDiskSize(k))
	dist := make([]int, 0, maxGridDiskSize(k))
	out = append(out, origin)
	dist = append(dist, 0)
	if origin.IsPentagon() {
		return out, dist, errPentagon
	}
	ring, direction, i, rotations := 1, 0, 0, 0
	var err error
	for ring <= k {
		if direction == 0 && i == 0 {
			if origin, err = neighborRotations(origin, nextRingDirection, &rotations); err != nil {
				return out, dist, err
			}
			if origin.IsPentagon() {
				return out, dist, errPentagon
			}
		}
		if origin, err = neighborRotations(origin, directions[direction], &rotations); err != nil {
			return out, dist, err
		}
		out = append(out, origin)
		dist = append(dist, ring)
		i++
		if i == ring {
			i = 0
			direction++
			if direction == 6 {
				direction = 0
				ring++
			}
		}
		if origin.IsPentagon() {
			return out, dist, errPentagon
		}
	}
	return out, dist, nil
}

// GridDiskDistances returns all cells within k steps of origin with their
// distances. The order follows H3's gridDiskDistances: the spiral walk when
// it succeeds, otherwise the hash-slot order of the safe fallback (zero
// slots removed).
func (h Cell) GridDiskDistances(k int) ([]Cell, []int, error) {
	out, dist, err := gridDiskDistancesUnsafe(h, k)
	if err == nil {
		return out, dist, nil
	}
	if err != errPentagon && err != errFailed {
		return nil, nil, err
	}
	maxIdx := maxGridDiskSize(k)
	slots := make([]Cell, maxIdx)
	sdist := make([]int, maxIdx)
	if err := gridDiskInternal(h, k, slots, sdist, 0); err != nil {
		return nil, nil, err
	}
	out, dist = out[:0], dist[:0]
	for i, c := range slots {
		if c != 0 {
			out = append(out, c)
			dist = append(dist, sdist[i])
		}
	}
	return out, dist, nil
}

func gridDiskInternal(origin Cell, k int, out []Cell, dist []int, curK int) error {
	maxIdx := uint64(len(out))
	off := uint64(origin) % maxIdx
	for out[off] != 0 && out[off] != origin {
		off = (off + 1) % maxIdx
	}
	if out[off] == origin && dist[off] <= curK {
		return nil
	}
	out[off] = origin
	dist[off] = curK
	if curK >= k {
		return nil
	}
	for i := 0; i < 6; i++ {
		rotations := 0
		next, err := neighborRotations(origin, directions[i], &rotations)
		if err == errPentagon {
			continue
		}
		if err != nil {
			return err
		}
		if err := gridDiskInternal(next, k, out, dist, curK+1); err != nil {
			return err
		}
	}
	return nil
}

// GridDisk returns all cells within k steps of h.
func (h Cell) GridDisk(k int) ([]Cell, error) {
	c, _, err := h.GridDiskDistances(k)
	return c, err
}

// gridRingUnsafe returns the hollow ring at distance k; fails near pentagons.
func gridRingUnsafe(origin Cell, k int) ([]Cell, error) {
	if k < 0 {
		return nil, errDomain
	}
	if k == 0 {
		return []Cell{origin}, nil
	}
	if origin.IsPentagon() {
		return nil, errPentagon
	}
	rotations := 0
	var err error
	for ring := 0; ring < k; ring++ {
		if origin, err = neighborRotations(origin, nextRingDirection, &rotations); err != nil {
			return nil, err
		}
		if origin.IsPentagon() {
			return nil, errPentagon
		}
	}
	last := origin
	out := make([]Cell, 0, 6*k)
	out = append(out, origin)
	for direction := 0; direction < 6; direction++ {
		for pos := 0; pos < k; pos++ {
			if origin, err = neighborRotations(origin, directions[direction], &rotations); err != nil {
				return nil, err
			}
			if pos != k-1 || direction != 5 {
				out = append(out, origin)
				if origin.IsPentagon() {
					return nil, errPentagon
				}
			}
		}
	}
	if last != origin {
		return nil, errPentagon
	}
	return out, nil
}

// GridRing returns the cells at exactly distance k (gridRing, v4.5: the
// unsafe walk, falling back to filtering the disk).
func (h Cell) GridRing(k int) ([]Cell, error) {
	if out, err := gridRingUnsafe(h, k); err == nil {
		return out, nil
	} else if err != errPentagon {
		return nil, err
	}
	maxIdx := maxGridDiskSize(k)
	cells := make([]Cell, maxIdx)
	dist := make([]int, maxIdx)
	if err := gridDiskInternal(h, k, cells, dist, 0); err != nil {
		return nil, err
	}
	var out []Cell
	for i, c := range cells {
		if c != 0 && dist[i] == k {
			out = append(out, c)
		}
	}
	return out, nil
}
