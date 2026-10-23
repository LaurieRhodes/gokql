package h3

import "math"

// Port of faceijk.c and baseCells.c (H3 v4.5.0).

type faceIJK struct {
	face  int
	coord coordIJK
}

type faceOrientIJK struct {
	face      int
	translate coordIJK // res 0 translation relative to primary face
	ccwRot60  int      // 60 degree ccw rotations relative to primary face
}

type baseCellRotation struct {
	baseCell int
	ccwRot60 int
}

type baseCellInfo struct {
	homeFijk     faceIJK
	isPentagon   int
	cwOffsetPent [2]int
}

const (
	faceIJ          = 1
	faceKI          = 2
	faceJK          = 3
	invalidBaseCell = 127
	maxFaceCoord    = 2

	noOverage = 0
	faceEdge  = 1
	newFace   = 2

	sqrt7  = 2.6457513110645905905016157536392604257102
	rsqrt7 = 0.37796447300922722721451653623418006081576
)

func isResolutionClassIII(r int) bool { return r%2 != 0 }

func posAngleRads(rads float64) float64 {
	tmp := rads
	if rads < 0.0 {
		tmp = rads + m2Pi
	}
	if rads >= m2Pi {
		tmp -= m2Pi
	}
	return tmp
}

// --- 3D vectors (vec3d.h) ---

func latLngToVec3(g LatLng) vec3d {
	r := math.Cos(g.Lat)
	return vec3d{math.Cos(g.Lng) * r, math.Sin(g.Lng) * r, math.Sin(g.Lat)}
}

func vec3ToLatLng(v vec3d) LatLng { return LatLng{math.Asin(v.z), math.Atan2(v.y, v.x)} }

func vec3LinComb(a float64, v1 vec3d, b float64, v2 vec3d) vec3d {
	return vec3d{a*v1.x + b*v2.x, a*v1.y + b*v2.y, a*v1.z + b*v2.z}
}

func vec3Cross(v1, v2 vec3d) vec3d {
	return vec3d{v1.y*v2.z - v1.z*v2.y, v1.z*v2.x - v1.x*v2.z, v1.x*v2.y - v1.y*v2.x}
}

func vec3Dot(v1, v2 vec3d) float64 { return v1.x*v2.x + v1.y*v2.y + v1.z*v2.z }

func vec3Normalize(v *vec3d) {
	n := math.Sqrt(vec3Dot(*v, *v))
	s := 0.0
	if n > 0.0 {
		s = 1.0 / n
	}
	v.x *= s
	v.y *= s
	v.z *= s
}

func vec3DistSq(v1, v2 vec3d) float64 {
	d := vec3LinComb(1.0, v1, -1.0, v2)
	return vec3Dot(d, d)
}

// --- faceijk.c ---

func vec3ToFaceIjk(p vec3d, res int) faceIJK {
	face, v := vec3ToHex2d(p, res)
	return faceIJK{face, hex2dToCoordIJK(v)}
}

func vec3TangentBasis(p vec3d) (north, east vec3d) {
	northPole := vec3d{0, 0, 1}
	north = vec3LinComb(1.0, northPole, -vec3Dot(northPole, p), p)
	vec3Normalize(&north)
	east = vec3Cross(north, p)
	return
}

func vec3AzimuthRads(p1, p2 vec3d) float64 {
	northDir, eastDir := vec3TangentBasis(p1)
	p2Proj := vec3LinComb(1.0, p2, -vec3Dot(p2, p1), p1)
	vec3Normalize(&p2Proj)
	return math.Atan2(vec3Dot(p2Proj, eastDir), vec3Dot(p2Proj, northDir))
}

func vec3ToHex2d(p vec3d, res int) (int, vec2d) {
	face, sqd := vec3ToClosestFace(p)
	r := math.Acos(1 - sqd*0.5)
	if r < epsilon {
		return face, vec2d{}
	}
	theta := posAngleRads(faceAxesAzRadsCII[face][0] - posAngleRads(vec3AzimuthRads(faceCenterPoint[face], p)))
	if isResolutionClassIII(res) {
		theta = posAngleRads(theta - ap7RotRads)
	}
	r = math.Tan(r)
	r *= invRes0UGnomonic
	for i := 0; i < res; i++ {
		r *= sqrt7
	}
	return face, vec2d{r * math.Cos(theta), r * math.Sin(theta)}
}

func hex2dToVec3(v vec2d, face, res int, substrate bool) vec3d {
	r := v2dMag(v)
	if r < epsilon {
		return faceCenterPoint[face]
	}
	theta := math.Atan2(v.y, v.x)
	for i := 0; i < res; i++ {
		r *= rsqrt7
	}
	if substrate {
		r *= oneThird
		if isResolutionClassIII(res) {
			r *= rsqrt7
		}
	}
	r *= res0UGnomonic
	r = math.Atan(r)
	if !substrate && isResolutionClassIII(res) {
		theta = posAngleRads(theta + ap7RotRads)
	}
	theta = posAngleRads(faceAxesAzRadsCII[face][0] - theta)
	northDir, eastDir := vec3TangentBasis(faceCenterPoint[face])
	dir := vec3LinComb(math.Cos(theta), northDir, math.Sin(theta), eastDir)
	out := vec3LinComb(math.Cos(r), faceCenterPoint[face], math.Sin(r), dir)
	vec3Normalize(&out)
	return out
}

func faceIjkToVec3(h faceIJK, res int) vec3d {
	return hex2dToVec3(ijkToHex2d(h.coord), h.face, res, false)
}

// faceIjkPentToCellBoundary appends a pentagon's boundary vertices.
func faceIjkPentToCellBoundary(h faceIJK, res, start, length int) []LatLng {
	adjRes := res
	centerIJK := h
	fijkVerts := faceIjkPentToVerts(&centerIJK, &adjRes)
	additional := 0
	if length == numPentVerts {
		additional = 1
	}
	var out []LatLng
	var lastFijk faceIJK
	for vert := start; vert < start+length+additional; vert++ {
		v := vert % numPentVerts
		fijk := fijkVerts[v]
		adjustPentVertOverage(&fijk, adjRes)
		if isResolutionClassIII(res) && vert > start {
			tmpFijk := fijk
			orig2d0 := ijkToHex2d(lastFijk.coord)
			currentToLastDir := adjacentFaceDir[tmpFijk.face][lastFijk.face]
			fo := faceNeighbors[tmpFijk.face][currentToLastDir]
			tmpFijk.face = fo.face
			ijk := &tmpFijk.coord
			for i := 0; i < fo.ccwRot60; i++ {
				ijk.rotate60ccw()
			}
			trans := fo.translate
			trans.scale(unitScaleByCIIres[adjRes] * 3)
			*ijk = ijkAdd(*ijk, trans)
			ijk.normalize()
			orig2d1 := ijkToHex2d(*ijk)
			maxDim := float64(maxDimByCIIres[adjRes])
			v0 := vec2d{3.0 * maxDim, 0.0}
			v1 := vec2d{-1.5 * maxDim, 3.0 * sqrt3_2 * maxDim}
			v2 := vec2d{-1.5 * maxDim, -3.0 * sqrt3_2 * maxDim}
			var e0, e1 vec2d
			switch adjacentFaceDir[tmpFijk.face][fijk.face] {
			case faceIJ:
				e0, e1 = v0, v1
			case faceJK:
				e0, e1 = v1, v2
			default:
				e0, e1 = v2, v0
			}
			inter := v2dIntersect(orig2d0, orig2d1, e0, e1)
			out = append(out, vec3ToLatLng(hex2dToVec3(inter, tmpFijk.face, adjRes, true)))
		}
		if vert < start+numPentVerts {
			vec := ijkToHex2d(fijk.coord)
			out = append(out, vec3ToLatLng(hex2dToVec3(vec, fijk.face, adjRes, true)))
		}
		lastFijk = fijk
	}
	return out
}

func faceIjkPentToVerts(fijk *faceIJK, res *int) [numPentVerts]faceIJK {
	vertsCII := [numPentVerts]coordIJK{{2, 1, 0}, {1, 2, 0}, {0, 2, 1}, {0, 1, 2}, {1, 0, 2}}
	vertsCIII := [numPentVerts]coordIJK{{5, 4, 0}, {1, 5, 0}, {0, 5, 4}, {0, 1, 5}, {4, 0, 5}}
	verts := vertsCII
	if isResolutionClassIII(*res) {
		verts = vertsCIII
	}
	fijk.coord.downAp3()
	fijk.coord.downAp3r()
	if isResolutionClassIII(*res) {
		fijk.coord.downAp7r()
		*res++
	}
	var out [numPentVerts]faceIJK
	for v := 0; v < numPentVerts; v++ {
		out[v].face = fijk.face
		out[v].coord = ijkAdd(fijk.coord, verts[v])
		out[v].coord.normalize()
	}
	return out
}

// faceIjkToCellBoundary is a hexagon's boundary vertices.
func faceIjkToCellBoundary(h faceIJK, res, start, length int) []LatLng {
	adjRes := res
	centerIJK := h
	fijkVerts := faceIjkToVerts(&centerIJK, &adjRes)
	additional := 0
	if length == numHexVerts {
		additional = 1
	}
	var out []LatLng
	lastFace := -1
	lastOverage := noOverage
	for vert := start; vert < start+length+additional; vert++ {
		v := vert % numHexVerts
		fijk := fijkVerts[v]
		overage := adjustOverageClassII(&fijk, adjRes, false, true)
		if isResolutionClassIII(res) && vert > start && fijk.face != lastFace && lastOverage != faceEdge {
			lastV := (v + 5) % numHexVerts
			orig2d0 := ijkToHex2d(fijkVerts[lastV].coord)
			orig2d1 := ijkToHex2d(fijkVerts[v].coord)
			maxDim := float64(maxDimByCIIres[adjRes])
			v0 := vec2d{3.0 * maxDim, 0.0}
			v1 := vec2d{-1.5 * maxDim, 3.0 * sqrt3_2 * maxDim}
			v2 := vec2d{-1.5 * maxDim, -3.0 * sqrt3_2 * maxDim}
			face2 := lastFace
			if lastFace == centerIJK.face {
				face2 = fijk.face
			}
			var e0, e1 vec2d
			switch adjacentFaceDir[centerIJK.face][face2] {
			case faceIJ:
				e0, e1 = v0, v1
			case faceJK:
				e0, e1 = v1, v2
			default:
				e0, e1 = v2, v0
			}
			inter := v2dIntersect(orig2d0, orig2d1, e0, e1)
			if !v2dAlmostEquals(orig2d0, inter) && !v2dAlmostEquals(orig2d1, inter) {
				out = append(out, vec3ToLatLng(hex2dToVec3(inter, centerIJK.face, adjRes, true)))
			}
		}
		if vert < start+numHexVerts {
			vec := ijkToHex2d(fijk.coord)
			out = append(out, vec3ToLatLng(hex2dToVec3(vec, fijk.face, adjRes, true)))
		}
		lastFace = fijk.face
		lastOverage = overage
	}
	return out
}

func faceIjkToVerts(fijk *faceIJK, res *int) [numHexVerts]faceIJK {
	vertsCII := [numHexVerts]coordIJK{{2, 1, 0}, {1, 2, 0}, {0, 2, 1}, {0, 1, 2}, {1, 0, 2}, {2, 0, 1}}
	vertsCIII := [numHexVerts]coordIJK{{5, 4, 0}, {1, 5, 0}, {0, 5, 4}, {0, 1, 5}, {4, 0, 5}, {5, 0, 1}}
	verts := vertsCII
	if isResolutionClassIII(*res) {
		verts = vertsCIII
	}
	fijk.coord.downAp3()
	fijk.coord.downAp3r()
	if isResolutionClassIII(*res) {
		fijk.coord.downAp7r()
		*res++
	}
	var out [numHexVerts]faceIJK
	for v := 0; v < numHexVerts; v++ {
		out[v].face = fijk.face
		out[v].coord = ijkAdd(fijk.coord, verts[v])
		out[v].coord.normalize()
	}
	return out
}

// adjustOverageClassII moves a Class II coordinate on a face to the
// adjacent face when it lies off the face.
func adjustOverageClassII(fijk *faceIJK, res int, pentLeading4, substrate bool) int {
	overage := noOverage
	ijk := &fijk.coord
	maxDim := maxDimByCIIres[res]
	if substrate {
		maxDim *= 3
	}
	if substrate && ijk.i+ijk.j+ijk.k == maxDim {
		overage = faceEdge
	} else if ijk.i+ijk.j+ijk.k > maxDim {
		overage = newFace
		var fo faceOrientIJK
		if ijk.k > 0 {
			if ijk.j > 0 {
				fo = faceNeighbors[fijk.face][faceJK]
			} else {
				fo = faceNeighbors[fijk.face][faceKI]
				if pentLeading4 {
					origin := coordIJK{maxDim, 0, 0}
					tmp := ijkSub(*ijk, origin)
					tmp.rotate60cw()
					*ijk = ijkAdd(tmp, origin)
				}
			}
		} else {
			fo = faceNeighbors[fijk.face][faceIJ]
		}
		fijk.face = fo.face
		for i := 0; i < fo.ccwRot60; i++ {
			ijk.rotate60ccw()
		}
		trans := fo.translate
		unitScale := unitScaleByCIIres[res]
		if substrate {
			unitScale *= 3
		}
		trans.scale(unitScale)
		*ijk = ijkAdd(*ijk, trans)
		ijk.normalize()
		if substrate && ijk.i+ijk.j+ijk.k == maxDim {
			overage = faceEdge
		}
	}
	return overage
}

func adjustPentVertOverage(fijk *faceIJK, res int) int {
	for {
		if o := adjustOverageClassII(fijk, res, false, true); o != newFace {
			return o
		}
	}
}

func vec3ToClosestFace(v vec3d) (int, float64) {
	face, sqd := 0, 5.0
	for f := 0; f < numIcosaFaces; f++ {
		if d := vec3DistSq(faceCenterPoint[f], v); d < sqd {
			face, sqd = f, d
		}
	}
	return face, sqd
}

// --- baseCells.c ---

func isBaseCellPentagon(bc int) bool {
	return bc >= 0 && bc < numBaseCells && baseCellData[bc].isPentagon != 0
}

func isBaseCellPolarPentagon(bc int) bool { return bc == 4 || bc == 117 }

func faceIjkToBaseCell(h faceIJK) int {
	return faceIjkBaseCells[h.face][h.coord.i][h.coord.j][h.coord.k].baseCell
}

func faceIjkToBaseCellCCWrot60(h faceIJK) int {
	return faceIjkBaseCells[h.face][h.coord.i][h.coord.j][h.coord.k].ccwRot60
}

func baseCellToCCWrot60(bc, face int) int {
	if face < 0 || face >= numIcosaFaces {
		return -1
	}
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				if faceIjkBaseCells[face][i][j][k].baseCell == bc {
					return faceIjkBaseCells[face][i][j][k].ccwRot60
				}
			}
		}
	}
	return -1
}

func baseCellIsCwOffset(bc, testFace int) bool {
	return baseCellData[bc].cwOffsetPent[0] == testFace || baseCellData[bc].cwOffsetPent[1] == testFace
}

func getBaseCellNeighbor(bc, dir int) int { return baseCellNeighbors[bc][dir] }
