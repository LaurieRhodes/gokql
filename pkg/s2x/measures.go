package s2x

// Loop measures from s2loop_measures.{h,cc}: signed area and curvature of
// a vertex loop, with the full loop (no vertices) having a tiny negative
// area.

import (
	"math"

	"github.com/golang/geo/s2"
)

type kahanSum struct{ sum, err float64 }

func (k *kahanSum) add(v float64) {
	t1 := v - k.err
	t2 := k.sum + t1
	k.err = (t2 - k.sum) - t1
	k.sum = t2
}

func signedTriArea(a, b, c s2.Point) float64 { return float64(sign(a, b, c)) * s2.PointArea(a, b, c) }

func surfaceIntegralKahan(loop []s2.Point, f func(a, b, c s2.Point) float64) float64 {
	const maxLength = math.Pi - 1e-5
	var sum kahanSum
	n := len(loop)
	if n < 3 {
		return 0
	}
	origin := loop[0]
	for i := 1; i+1 < n; i++ {
		if loop[i+1].Angle(origin.Vector) > maxLength {
			old := origin
			if origin == loop[0] {
				origin = unit(robustCrossProd(loop[0], loop[i]))
			} else if loop[i].Angle(loop[0].Vector) < maxLength {
				origin = loop[0]
			} else {
				origin = s2.Point{Vector: loop[0].Cross(old.Vector)}
				sum.add(f(loop[0], old, origin))
			}
			sum.add(f(old, loop[i], origin))
		}
		sum.add(f(origin, loop[i], loop[i+1]))
	}
	if origin != loop[0] {
		sum.add(f(origin, loop[n-1], loop[0]))
	}
	return sum.sum
}

func curvatureMaxError(loop []s2.Point) float64 { return 11.25 * dblEpsilon * float64(len(loop)) }

func pruneDegeneracies(loop []s2.Point) []s2.Point {
	var vs []s2.Point
	for _, v := range loop {
		if len(vs) > 0 {
			if v == vs[len(vs)-1] {
				continue
			}
			if len(vs) >= 2 && v == vs[len(vs)-2] {
				vs = vs[:len(vs)-1]
				continue
			}
		}
		vs = append(vs, v)
	}
	if len(vs) >= 2 && vs[0] == vs[len(vs)-1] {
		vs = vs[:len(vs)-1]
	}
	if len(vs) < 3 {
		return nil
	}
	k := 0
	for vs[k+1] == vs[len(vs)-(k+1)] || vs[k] == vs[len(vs)-(k+2)] {
		k++
	}
	return vs[k : len(vs)-k]
}

type loopOrder struct{ first, dir int }

func at(loop []s2.Point, i int) s2.Point {
	n := len(loop)
	i %= n
	if i < 0 {
		i += n
	}
	return loop[i]
}

func isOrderLess(o1, o2 loopOrder, loop []s2.Point) bool {
	if o1 == o2 {
		return false
	}
	i1, i2 := o1.first, o2.first
	for n := len(loop) - 1; n > 0; n-- {
		i1 += o1.dir
		i2 += o2.dir
		c := at(loop, i1).Cmp(at(loop, i2).Vector)
		if c < 0 {
			return true
		}
		if c > 0 {
			return false
		}
	}
	return false
}

func canonicalLoopOrder(loop []s2.Point) loopOrder {
	n := len(loop)
	if n == 0 {
		return loopOrder{0, 1}
	}
	minIdx := []int{0}
	for i := 1; i < n; i++ {
		c := loop[i].Cmp(loop[minIdx[0]].Vector)
		if c <= 0 {
			if c < 0 {
				minIdx = minIdx[:0]
			}
			minIdx = append(minIdx, i)
		}
	}
	minOrder := loopOrder{minIdx[0], 1}
	for _, m := range minIdx {
		o1 := loopOrder{m, 1}
		o2 := loopOrder{m + n, -1}
		if isOrderLess(o1, minOrder, loop) {
			minOrder = o1
		}
		if isOrderLess(o2, minOrder, loop) {
			minOrder = o2
		}
	}
	return minOrder
}

// loopCurvature is S2::GetCurvature.
func loopCurvature(loop []s2.Point) float64 {
	if len(loop) == 0 {
		return -2 * math.Pi
	}
	loop = pruneDegeneracies(loop)
	if len(loop) == 0 {
		return 2 * math.Pi
	}
	o := canonicalLoopOrder(loop)
	i, dir, n := o.first, o.dir, len(loop)
	sum := float64(s2.TurnAngle(at(loop, i+n-dir), at(loop, i), at(loop, i+dir)))
	comp := 0.0
	for n--; n > 0; n-- {
		i += dir
		angle := float64(s2.TurnAngle(at(loop, i-dir), at(loop, i), at(loop, i+dir)))
		old := sum
		angle += comp
		sum += angle
		comp = (old - sum) + angle
	}
	const maxCurvature = 2*math.Pi - 4*dblEpsilon
	sum += comp
	return math.Max(-maxCurvature, math.Min(maxCurvature, float64(dir)*sum))
}

// loopSignedArea is S2::GetSignedArea.
func loopSignedArea(loop []s2.Point) float64 {
	area := surfaceIntegralKahan(loop, signedTriArea)
	maxErr := curvatureMaxError(loop)
	area = math.Remainder(area, 4*math.Pi)
	if area == -2*math.Pi {
		area = 2 * math.Pi
	}
	if math.Abs(area) <= maxErr {
		c := loopCurvature(loop)
		if c == 2*math.Pi {
			return 0
		}
		if area <= 0 && c > 0 {
			return math.SmallestNonzeroFloat64 * 0x1p52 // DBL_MIN
		}
		if area >= 0 && c < 0 {
			return -math.SmallestNonzeroFloat64 * 0x1p52
		}
	}
	return area
}
