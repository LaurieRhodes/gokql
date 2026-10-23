package s2x

// Port of S2PolylineSimplifier (s2polyline_simplifier.cc).

import (
	"math"

	"github.com/golang/geo/r3"
	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

type rangeToAvoid struct {
	interval s1.Interval
	onLeft   bool
}

type polylineSimplifier struct {
	src           s2.Point
	xDir, yDir    r3.Vector
	window        s1.Interval
	rangesToAvoid []rangeToAvoid
}

func (s *polylineSimplifier) init(src s2.Point) {
	s.src = src
	s.window = s1.FullInterval()
	s.rangesToAvoid = s.rangesToAvoid[:0]
	tmp := src.Abs()
	v := [3]float64{src.X, src.Y, src.Z}
	t := [3]float64{tmp.X, tmp.Y, tmp.Z}
	var i int
	if t[0] < t[1] {
		if t[0] < t[2] {
			i = 0
		} else {
			i = 2
		}
	} else if t[1] < t[2] {
		i = 1
	} else {
		i = 2
	}
	j := i + 1
	if i == 2 {
		j = 0
	}
	k := i - 1
	if i == 0 {
		k = 2
	}
	var y, x [3]float64
	y[i] = 0
	y[j] = v[k]
	y[k] = -v[j]
	x[i] = v[j]*v[j] + v[k]*v[k]
	x[j] = -v[j] * v[i]
	x[k] = -v[k] * v[i]
	s.yDir = r3.Vector{X: y[0], Y: y[1], Z: y[2]}
	s.xDir = r3.Vector{X: x[0], Y: x[1], Z: x[2]}
}

func (s *polylineSimplifier) extend(dst s2.Point) bool {
	if chordBetween(s.src, dst) > s1.RightChordAngle {
		return false
	}
	dir := s.direction(dst)
	if !s.window.Contains(dir) {
		return false
	}
	for _, r := range s.rangesToAvoid {
		if r.interval.Contains(dir) {
			return false
		}
	}
	return true
}

func (s *polylineSimplifier) targetDisc(p s2.Point, r s1.ChordAngle) bool {
	semi := s.semiwidth(p, r, -1)
	if semi >= math.Pi {
		return true
	}
	if semi < 0 {
		s.window = s1.EmptyInterval()
		return false
	}
	center := s.direction(p)
	target := intervalFromPoint(center).Expanded(semi)
	s.window = s.window.Intersection(target)
	for _, r := range s.rangesToAvoid {
		s.avoidRange(r.interval, r.onLeft)
	}
	s.rangesToAvoid = s.rangesToAvoid[:0]
	return !s.window.IsEmpty()
}

func (s *polylineSimplifier) avoidDisc(p s2.Point, r s1.ChordAngle, discOnLeft bool) bool {
	semi := s.semiwidth(p, r, 1)
	if semi >= math.Pi {
		s.window = s1.EmptyInterval()
		return false
	}
	center := s.direction(p)
	dleft, dright := semi, math.Pi/2
	if discOnLeft {
		dleft, dright = math.Pi/2, semi
	}
	avoid := s1.IntervalFromEndpoints(math.Remainder(center-dright, 2*math.Pi), math.Remainder(center+dleft, 2*math.Pi))
	if s.window.IsFull() {
		s.rangesToAvoid = append(s.rangesToAvoid, rangeToAvoid{avoid, discOnLeft})
		return true
	}
	s.avoidRange(avoid, discOnLeft)
	return !s.window.IsEmpty()
}

func (s *polylineSimplifier) avoidRange(avoid s1.Interval, discOnLeft bool) {
	if s.window.ContainsInterval(avoid) {
		if discOnLeft {
			s.window = s1.IntervalFromEndpoints(s.window.Lo, avoid.Lo)
		} else {
			s.window = s1.IntervalFromEndpoints(avoid.Hi, s.window.Hi)
		}
	} else {
		s.window = s.window.Intersection(avoid.Complement())
	}
}

func (s *polylineSimplifier) direction(p s2.Point) float64 {
	return math.Atan2(p.Dot(s.yDir), p.Dot(s.xDir))
}

func (s *polylineSimplifier) semiwidth(p s2.Point, r s1.ChordAngle, round float64) float64 {
	const e = 0.5 * dblEpsilon
	r2 := float64(r)
	a2 := float64(chordBetween(s.src, p))
	a2 -= 64 * e * e * round
	if a2 <= r2 {
		return math.Pi
	}
	sin2R := r2 * (1 - 0.25*r2)
	sin2A := a2 * (1 - 0.25*a2)
	semi := math.Asin(math.Sqrt(sin2R / sin2A))
	errv := (2*10+4)*e + 17*e*semi
	return semi + round*errv
}
