package s2x

// Ports of the s2predicates.cc functions S2Builder needs that golang/geo
// does not export. C++ retries an uncertain double-precision test in long
// double before falling back to exact arithmetic; Go has no long double,
// so these go straight from the double test to the exact one. Results are
// identical, only slower in the rare uncertain cases.

import (
	"math"
	"math/big"

	"github.com/golang/geo/r3"
	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

const (
	dblErr = 0x1p-53 // rounding_epsilon<double>()
	tErr   = dblErr
	sqrt3  = 1.73205080756887729352744634150587236694280525381038062805580
	// k45Degrees is S1ChordAngle::FromLength2(2 - sqrt(2)).
	k45Length2 = 2 - math.Sqrt2
)

func pv(p s2.Point) r3.PreciseVector { return r3.PreciseVectorFromVector(p.Vector) }

func bigF(f float64) *big.Float { return new(big.Float).SetPrec(r3.MaxPrec).SetFloat64(f) }
func bAdd(a, b *big.Float) *big.Float {
	return new(big.Float).SetPrec(r3.MaxPrec).Add(a, b)
}
func bSub(a, b *big.Float) *big.Float {
	return new(big.Float).SetPrec(r3.MaxPrec).Sub(a, b)
}
func bMul(a, b *big.Float) *big.Float {
	return new(big.Float).SetPrec(r3.MaxPrec).Mul(a, b)
}

func sgnInt(x int) int {
	if x > 0 {
		return 1
	}
	if x < 0 {
		return -1
	}
	return 0
}

func clampSign(x int) int { return max(-1, min(1, x)) }

// sign is s2pred::Sign: the robust orientation of (a, b, c), never zero for
// distinct points.
func sign(a, b, c s2.Point) int { return int(s2.RobustSign(a, b, c)) }

// orderedCCW is s2pred::OrderedCCW.
func orderedCCW(a, b, c, o s2.Point) bool { return s2.OrderedCCW(a, b, c, o) }

// unperturbedSign is the orientation without symbolic perturbation (zero for
// exactly collinear points).
func unperturbedSign(a, b, c s2.Point) int {
	det := a.Cross(b.Vector).Dot(c.Vector)
	const maxDeterminantError = 1.8274 * 0x1p-52
	if det > maxDeterminantError {
		return 1
	}
	if det < -maxDeterminantError {
		return -1
	}
	if a == b || b == c || c == a {
		return 0
	}
	return pv(a).Dot(pv(b).Cross(pv(c))).Sign()
}

func less(a, b s2.Point) bool { return a.Cmp(b.Vector) < 0 }

func getCosDistance(x, y r3.Vector) (float64, float64) {
	c := x.Dot(y)
	return c, 9.5*dblErr*math.Abs(c) + 1.5*dblErr
}

func getSin2Distance(x, y r3.Vector) (float64, float64) {
	n := x.Sub(y).Cross(x.Add(y))
	d2 := 0.25 * n.Norm2()
	return d2, (21+4*sqrt3)*dblErr*d2 + 32*sqrt3*dblErr*dblErr*math.Sqrt(d2) +
		768*dblErr*dblErr*dblErr*dblErr
}

func triageCompareCosDistance(x, y r3.Vector, r2 float64) int {
	cosXY, cosXYErr := getCosDistance(x, y)
	cosR := 1 - 0.5*r2
	cosRErr := 2 * tErr * cosR
	diff := cosXY - cosR
	e := cosXYErr + cosRErr
	if diff > e {
		return -1
	}
	if diff < -e {
		return 1
	}
	return 0
}

func triageCompareSin2Distance(x, y r3.Vector, r2 float64) int {
	sin2XY, sin2XYErr := getSin2Distance(x, y)
	sin2R := r2 * (1 - 0.25*r2)
	sin2RErr := 3 * tErr * sin2R
	diff := sin2XY - sin2R
	e := sin2XYErr + sin2RErr
	if diff > e {
		return 1
	}
	if diff < -e {
		return -1
	}
	return 0
}

func triageCompareDistance(x, y r3.Vector, r2 float64) int {
	s := triageCompareCosDistance(x, y, r2)
	if s == 0 && r2 < k45Length2 {
		s = triageCompareSin2Distance(x, y, r2)
	}
	return s
}

func exactCompareDistance(x, y r3.PreciseVector, r2 *big.Float) int {
	cosXY := x.Dot(y)
	cosR := bSub(bigF(1), bMul(bigF(0.5), r2))
	xySign, rSign := cosXY.Sign(), cosR.Sign()
	if xySign != rSign {
		if xySign > rSign {
			return -1
		}
		return 1
	}
	cmp := bSub(bMul(bMul(bMul(cosR, cosR), x.Norm2()), y.Norm2()), bMul(cosXY, cosXY))
	return xySign * cmp.Sign()
}

// compareDistance is s2pred::CompareDistance (golang/geo exports it as
// s2.CompareDistance).
func compareDistance(x, y s2.Point, r s1.ChordAngle) int {
	return s2.CompareDistance(x, y, r)
}

// compareDistances is s2pred::CompareDistances.
func compareDistances(x, a, b s2.Point) int { return s2.CompareDistances(x, a, b) }

func getClosestVertex(x, a0, a1 r3.Vector) (r3.Vector, float64) {
	a0x2 := a0.Sub(x).Norm2()
	a1x2 := a1.Sub(x).Norm2()
	if a0x2 < a1x2 || (a0x2 == a1x2 && a0.Cmp(a1) < 0) {
		return a0, a0x2
	}
	return a1, a1x2
}

func triageCompareLineSin2Distance(x, a0, a1 r3.Vector, r2 float64, n r3.Vector, n1, n2 float64) int {
	if r2 >= 2.0 {
		return -1
	}
	n2sin2R := n2 * r2 * (1 - 0.25*r2)
	n2sin2RErr := 6 * tErr * n2sin2R
	cv, ax2 := getClosestVertex(x, a0, a1)
	xDn := x.Sub(cv).Dot(n)
	xDn2 := xDn * xDn
	c1 := ((3.5+2*sqrt3)*n1 + 32*sqrt3*dblErr) * tErr * math.Sqrt(ax2)
	xDn2Err := 4*tErr*xDn2 + (2*math.Abs(xDn)+c1)*c1
	n2sin2RErr += 8 * dblErr * n2sin2R
	diff := xDn2 - n2sin2R
	e := xDn2Err + n2sin2RErr
	if diff > e {
		return 1
	}
	if diff < -e {
		return -1
	}
	return 0
}

func triageCompareLineCos2Distance(x, a0, a1 r3.Vector, r2 float64, n r3.Vector, n1, n2 float64) int {
	if r2 >= 2.0 {
		return -1
	}
	cosR := 1 - 0.5*r2
	n2cos2R := n2 * cosR * cosR
	n2cos2RErr := 7 * tErr * n2cos2R
	m2 := x.Cross(n).Norm2()
	m1 := math.Sqrt(m2)
	m1Err := ((1+8/sqrt3)*n1 + 32*sqrt3*dblErr) * tErr
	m2Err := 3*tErr*m2 + (2*m1+m1Err)*m1Err
	n2cos2RErr += 8 * dblErr * n2cos2R
	diff := m2 - n2cos2R
	e := m2Err + n2cos2RErr
	if diff > e {
		return -1
	}
	if diff < -e {
		return 1
	}
	return 0
}

func triageCompareLineDistance(x, a0, a1 r3.Vector, r2 float64, n r3.Vector, n1, n2 float64) int {
	if r2 < k45Length2 {
		return triageCompareLineSin2Distance(x, a0, a1, r2, n, n1, n2)
	}
	return triageCompareLineCos2Distance(x, a0, a1, r2, n, n1, n2)
}

func triageCompareEdgeDistance(x, a0, a1 r3.Vector, r2 float64) int {
	n := a0.Sub(a1).Cross(a0.Add(a1))
	m := n.Cross(x)
	a0Dir := a0.Sub(x)
	a1Dir := a1.Sub(x)
	a0Sign := a0Dir.Dot(m)
	a1Sign := a1Dir.Dot(m)
	n2 := n.Norm2()
	n1 := math.Sqrt(n2)
	n1Err := ((3.5+8/sqrt3)*n1 + 32*sqrt3*dblErr) * tErr
	a0SignErr := n1Err * a0Dir.Norm()
	a1SignErr := n1Err * a1Dir.Norm()
	if a0Sign < a0SignErr && a1Sign > -a1SignErr {
		if a0Sign > -a0SignErr || a1Sign < a1SignErr {
			vertexSign := min(triageCompareDistance(x, a0, r2), triageCompareDistance(x, a1, r2))
			lineSign := triageCompareLineDistance(x, a0, a1, r2, n, n1, n2)
			if vertexSign == lineSign {
				return lineSign
			}
			return 0
		}
		return triageCompareLineDistance(x, a0, a1, r2, n, n1, n2)
	}
	return min(triageCompareDistance(x, a0, r2), triageCompareDistance(x, a1, r2))
}

func exactCompareLineDistance(x, a0, a1 r3.PreciseVector, r2 *big.Float) int {
	if r2.Cmp(bigF(2)) >= 0 {
		return -1
	}
	n := a0.Cross(a1)
	sinD := x.Dot(n)
	sin2R := bMul(r2, bSub(bigF(1), bMul(bigF(0.25), r2)))
	cmp := bSub(bMul(sinD, sinD), bMul(bMul(sin2R, x.Norm2()), n.Norm2()))
	return cmp.Sign()
}

func exactCompareEdgeDistance(x, a0, a1 s2.Point, r s1.ChordAngle) int {
	nx := s2.Point{Vector: x.Mul(-1)}
	if a0 != nx && a1 != nx &&
		compareEdgeDirections(a0, a1, a0, x) > 0 &&
		compareEdgeDirections(a0, a1, x, a1) > 0 {
		return exactCompareLineDistance(pv(x), pv(a0), pv(a1), bigF(float64(r)))
	}
	return min(compareDistance(x, a0, r), compareDistance(x, a1, r))
}

// compareEdgeDistance is s2pred::CompareEdgeDistance: the sign of
// distance(x, edge a0a1) - r.
func compareEdgeDistance(x, a0, a1 s2.Point, r s1.ChordAngle) int {
	s := triageCompareEdgeDistance(x.Vector, a0.Vector, a1.Vector, float64(r))
	if s != 0 {
		return s
	}
	if a0 == a1 {
		return compareDistance(x, a0, r)
	}
	return exactCompareEdgeDistance(x, a0, a1, r)
}

// compareEdgePairDistance is s2pred::CompareEdgePairDistance.
func compareEdgePairDistance(a0, a1, b0, b1 s2.Point, r s1.ChordAngle) int {
	if s2.CrossingSign(a0, a1, b0, b1) != s2.DoNotCross {
		if r > 0 {
			return -1
		}
		if r < 0 {
			return 1
		}
		return 0
	}
	return min(min(compareEdgeDistance(a0, b0, b1, r), compareEdgeDistance(a1, b0, b1, r)),
		min(compareEdgeDistance(b0, a0, a1, r), compareEdgeDistance(b1, a0, a1, r)))
}

func triageCompareEdgeDirections(a0, a1, b0, b1 r3.Vector) int {
	na := a0.Sub(a1).Cross(a0.Add(a1))
	nb := b0.Sub(b1).Cross(b0.Add(b1))
	naLen, nbLen := na.Norm(), nb.Norm()
	cosAB := na.Dot(nb)
	cosABErr := ((5+4*sqrt3)*naLen*nbLen + 32*sqrt3*dblErr*(naLen+nbLen)) * tErr
	if cosAB > cosABErr {
		return 1
	}
	if cosAB < -cosABErr {
		return -1
	}
	return 0
}

// compareEdgeDirections is s2pred::CompareEdgeDirections: the sign of the
// angle between the directions of edges a and b.
func compareEdgeDirections(a0, a1, b0, b1 s2.Point) int {
	s := triageCompareEdgeDirections(a0.Vector, a1.Vector, b0.Vector, b1.Vector)
	if s != 0 {
		return s
	}
	if a0 == a1 || b0 == b1 {
		return 0
	}
	return pv(a0).Cross(pv(a1)).Dot(pv(b0).Cross(pv(b1))).Sign()
}

func getCircumcenter(a, b, c r3.Vector) (r3.Vector, float64) {
	abDiff, abSum := a.Sub(b), a.Add(b)
	bcDiff, bcSum := b.Sub(c), b.Add(c)
	nab := abDiff.Cross(abSum)
	nabLen := nab.Norm()
	abLen := abDiff.Norm()
	nbc := bcDiff.Cross(bcSum)
	nbcLen := nbc.Norm()
	bcLen := bcDiff.Norm()
	mab := nab.Cross(abSum)
	mbc := nbc.Cross(bcSum)
	e := ((16+24*sqrt3)*tErr+8*dblErr*(abLen+bcLen))*nabLen*nbcLen +
		128*sqrt3*dblErr*tErr*(nabLen+nbcLen) +
		3*4096*dblErr*dblErr*tErr*tErr
	return mab.Cross(mbc), e
}

func triageEdgeCircumcenterSign(x0, x1, a, b, c r3.Vector, abcSign int) int {
	z, zErr := getCircumcenter(a, b, c)
	nx := x0.Sub(x1).Cross(x0.Add(x1))
	result := float64(abcSign) * nx.Dot(z)
	zLen := z.Norm()
	nxLen := nx.Norm()
	nxErr := ((1+2*sqrt3)*nxLen + 32*sqrt3*dblErr) * tErr
	resultErr := (3*tErr*nxLen+nxErr)*zLen + zErr*nxLen
	if result > resultErr {
		return 1
	}
	if result < -resultErr {
		return -1
	}
	return 0
}

func linearlyDependent(x, y r3.PreciseVector) bool { return x.Cross(y).IsZero() }

func exactEdgeCircumcenterSign(x0, x1, a, b, c r3.PreciseVector, abcSign int) int {
	if linearlyDependent(x0, x1) {
		return 0
	}
	nx := x0.Cross(x1)
	dab := nx.Dot(a.Cross(b))
	dbc := nx.Dot(b.Cross(c))
	dca := nx.Dot(c.Cross(a))
	abc2 := bMul(a.Norm2(), bMul(dbc, dbc))
	bca2 := bMul(b.Norm2(), bMul(dca, dca))
	cab2 := bMul(c.Norm2(), bMul(dab, dab))
	lhs3Sgn, rhs3Sgn := dab.Sign(), -dbc.Sign()
	lhs2Sgn := clampSign(lhs3Sgn - rhs3Sgn)
	if lhs2Sgn == 0 && lhs3Sgn != 0 {
		lhs2Sgn = bSub(cab2, abc2).Sign() * lhs3Sgn
	}
	rhs2Sgn := -dca.Sign()
	result := clampSign(lhs2Sgn - rhs2Sgn)
	if result == 0 && lhs2Sgn != 0 {
		lhs4Sgn := dab.Sign() * dbc.Sign()
		rhs4 := bSub(bSub(bca2, cab2), abc2)
		result = clampSign(lhs4Sgn - rhs4.Sign())
		if result == 0 && lhs4Sgn != 0 {
			result = bSub(bMul(bMul(bigF(4), abc2), cab2), bMul(rhs4, rhs4)).Sign() * lhs4Sgn
		}
		result *= lhs2Sgn
	}
	return abcSign * result
}

func symbolicEdgeCircumcenterSign(x0, x1, a, b, c s2.Point) int {
	if a == b || b == c || c == a {
		return 0
	}
	if less(b, a) {
		a, b = b, a
	}
	if less(c, b) {
		b, c = c, b
	}
	if less(b, a) {
		a, b = b, a
	}
	if s := unperturbedSign(x0, x1, a); s != 0 {
		return s
	}
	if s := unperturbedSign(x0, x1, b); s != 0 {
		return s
	}
	return unperturbedSign(x0, x1, c)
}

// edgeCircumcenterSign is s2pred::EdgeCircumcenterSign.
func edgeCircumcenterSign(x0, x1, a, b, c s2.Point) int {
	abcSign := sign(a, b, c)
	s := triageEdgeCircumcenterSign(x0.Vector, x1.Vector, a.Vector, b.Vector, c.Vector, abcSign)
	if s != 0 {
		return s
	}
	if x0 == x1 || a == b || b == c || c == a {
		return 0
	}
	s = exactEdgeCircumcenterSign(pv(x0), pv(x1), pv(a), pv(b), pv(c), abcSign)
	if s != 0 {
		return s
	}
	return symbolicEdgeCircumcenterSign(x0, x1, a, b, c)
}

// excluded is s2pred::Excluded.
type excluded int

const (
	excludedFirst excluded = iota
	excludedSecond
	excludedNeither
	excludedUncertain
)

const rightLength2 = 2.0 // S1ChordAngle::Right().length2()

func triageVoronoiSiteExclusion(a, b, x0, x1 r3.Vector, r2 float64) excluded {
	n := x0.Sub(x1).Cross(x0.Add(x1))
	n2 := n.Norm2()
	n1 := math.Sqrt(n2)
	dnErr := ((3.5+2*sqrt3)*n1 + 32*sqrt3*dblErr) * tErr
	cosR := 1 - 0.5*r2
	sin2R := r2 * (1 - 0.25*r2)
	n2sin2R := n2 * sin2R
	cva, ax2 := getClosestVertex(a, x0, x1)
	aDn := a.Sub(cva).Dot(n)
	aDn2 := aDn * aDn
	aDnErr := dnErr * math.Sqrt(ax2)
	ra2 := n2sin2R - aDn2
	ra2Err := (8*dblErr+4*tErr)*aDn2 + (2*math.Abs(aDn)+aDnErr)*aDnErr + 6*tErr*n2sin2R
	minRa2 := ra2 - ra2Err
	if minRa2 < 0 {
		return excludedUncertain
	}
	ra := math.Sqrt(ra2)
	raErr := 1.5*tErr*ra + 0.5*ra2Err/math.Sqrt(minRa2)
	cvb, bx2 := getClosestVertex(b, x0, x1)
	bDn := b.Sub(cvb).Dot(n)
	bDn2 := bDn * bDn
	bDnErr := dnErr * math.Sqrt(bx2)
	rb2 := n2sin2R - bDn2
	rb2Err := (8*dblErr+4*tErr)*bDn2 + (2*math.Abs(bDn)+bDnErr)*bDnErr + 6*tErr*n2sin2R
	minRb2 := rb2 - rb2Err
	if minRb2 < 0 {
		return excludedUncertain
	}
	rb := math.Sqrt(rb2)
	rbErr := 1.5*tErr*rb + 0.5*rb2Err/math.Sqrt(minRb2)
	lhs3 := cosR * (rb - ra)
	absLhs3 := math.Abs(lhs3)
	lhs3Err := cosR*(raErr+rbErr) + 3*tErr*absLhs3
	aXb := a.Sub(b).Cross(a.Add(b))
	aXb1 := aXb.Norm()
	sinD := 0.5 * aXb.Dot(n)
	sinDErr := (4*dblErr+(2.5+2*sqrt3)*tErr)*aXb1*n1 + 16*sqrt3*dblErr*tErr*(aXb1+n1)
	result := absLhs3 - sinD
	resultErr := lhs3Err + sinDErr
	if result < -resultErr {
		return excludedNeither
	}
	if sinD < -sinDErr {
		ca := triageCompareCosDistance(a, x0, rightLength2)
		cb := triageCompareCosDistance(b, x1, rightLength2)
		if ca < 0 && cb < 0 {
			return excludedNeither
		}
		if ca <= 0 && cb <= 0 {
			return excludedUncertain
		}
		if ca > 0 {
			return excludedFirst
		}
		return excludedSecond
	}
	if sinD <= sinDErr {
		return excludedUncertain
	}
	cosD := a.Dot(b)*n2 - aDn*bDn
	cosDErr := ((8*dblErr+5*tErr)*math.Abs(aDn)+aDnErr)*math.Abs(bDn) +
		(math.Abs(aDn)+aDnErr)*bDnErr + (8*dblErr+8*tErr)*n2
	if cosD <= -cosDErr {
		return excludedNeither
	}
	if cosD < cosDErr {
		return excludedUncertain
	}
	if result <= resultErr {
		return excludedUncertain
	}
	if lhs3 > 0 {
		return excludedFirst
	}
	return excludedSecond
}

func exactVoronoiSiteExclusion(a, b, x0, x1 r3.PreciseVector, r2 *big.Float, aLessB bool) excluded {
	n := x0.Cross(x1)
	rhs2 := a.Cross(b).Dot(n)
	rhs2Sgn := rhs2.Sign()
	if rhs2Sgn < 0 {
		r90 := bigF(rightLength2)
		ca := exactCompareDistance(a, x0, r90)
		cb := exactCompareDistance(b, x1, r90)
		if ca < 0 && cb < 0 {
			return excludedNeither
		}
		if ca > 0 {
			return excludedFirst
		}
		return excludedSecond
	}
	n2 := n.Norm2()
	aDn := a.Dot(n)
	bDn := b.Dot(n)
	cosD := bSub(bMul(a.Dot(b), n2), bMul(aDn, bDn))
	if cosD.Sign() < 0 {
		return excludedNeither
	}
	a2 := a.Norm2()
	b2 := b.Norm2()
	n2sin2R := bMul(bMul(r2, bSub(bigF(1), bMul(bigF(0.25), r2))), n2)
	sa2 := bMul(b2, bSub(bMul(n2sin2R, a2), bMul(aDn, aDn)))
	sb2 := bMul(a2, bSub(bMul(n2sin2R, b2), bMul(bDn, bDn)))
	lhs2Sgn := bSub(sb2, sa2).Sign()
	if lhs2Sgn == 0 {
		return excludedNeither
	}
	cosR := bSub(bigF(1), bMul(bigF(0.5), r2))
	cos2R := bMul(cosR, cosR)
	lhs3 := bSub(bMul(cos2R, bAdd(sa2, sb2)), bMul(rhs2, rhs2))
	if lhs3.Sign() < 0 {
		return excludedNeither
	}
	lhs4 := bMul(lhs3, lhs3)
	rhs4 := bMul(bMul(bMul(bMul(bigF(4), cos2R), cos2R), sa2), sb2)
	result := bSub(lhs4, rhs4).Sign()
	if result < 0 {
		return excludedNeither
	}
	if result == 0 {
		// (a > b) in C++ compares the exact vectors lexicographically.
		if (lhs2Sgn > 0) == !aLessB {
			return excludedNeither
		}
	}
	if lhs2Sgn > 0 {
		return excludedFirst
	}
	return excludedSecond
}

// getVoronoiSiteExclusion is s2pred::GetVoronoiSiteExclusion.
func getVoronoiSiteExclusion(a, b, x0, x1 s2.Point, r s1.ChordAngle) excluded {
	if compareDistances(x1, a, b) < 0 {
		return excludedSecond
	}
	res := triageVoronoiSiteExclusion(a.Vector, b.Vector, x0.Vector, x1.Vector, float64(r))
	if res != excludedUncertain {
		return res
	}
	return exactVoronoiSiteExclusion(pv(a), pv(b), pv(x0), pv(x1), bigF(float64(r)), less(a, b) || a == b)
}
