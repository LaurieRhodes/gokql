package engine

// func_geo.go — geospatial scalar functions.

import (
	"fmt"
	"math"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// geoSphereRadius is the sphere ADX measures on by default: the
// documented geo_distance_2points(-122.407628, 47.578557, -118.275287,
// 34.019056) == 1546754.35197381 is the great-circle distance on a
// sphere of exactly this radius.
const geoSphereRadius = 6371010.0

func init() { scalarReturnTypes["geo_distance_2points"] = types.TypeReal }

func evalGeoFunc(eng *Engine, fc *parser.FuncCall, schema *types.Schema, row types.Row) (types.Value, bool, error) {
	switch fc.Name {
	case "geo_distance_2points":
		if len(fc.Args) != 4 && len(fc.Args) != 5 {
			return nil, true, fmt.Errorf("geo_distance_2points requires 4 or 5 arguments (lon1, lat1, lon2, lat2 [, use_spheroid])")
		}
		var c [4]float64
		for i := 0; i < 4; i++ {
			v, err := evalExpr(eng, fc.Args[i], schema, row)
			if err != nil {
				return nil, true, err
			}
			f, ok := geoCoord(v)
			if !ok {
				return nil, true, nil
			}
			c[i] = f
		}
		spheroid := false
		if len(fc.Args) == 5 {
			v, err := evalExpr(eng, fc.Args[4], schema, row)
			if err != nil {
				return nil, true, err
			}
			spheroid, _ = v.(bool)
		}
		lon1, lat1, lon2, lat2 := c[0], c[1], c[2], c[3]
		if math.Abs(lon1) > 180 || math.Abs(lon2) > 180 || math.Abs(lat1) > 90 || math.Abs(lat2) > 90 {
			return nil, true, nil
		}
		if spheroid {
			if d, ok := vincentyDistance(lon1, lat1, lon2, lat2); ok {
				return d, true, nil
			}
			return nil, true, nil
		}
		return haversineDistance(lon1, lat1, lon2, lat2, geoSphereRadius), true, nil
	}
	return evalGeoS2(eng, fc, schema, row)
}

func geoCoord(v types.Value) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	case int:
		return float64(x), true
	case types.Decimal:
		return x.Float64(), true
	}
	return 0, false
}

func haversineDistance(lon1, lat1, lon2, lat2, r float64) float64 {
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp := p2 - p1
	dl := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}

// vincentyDistance is the WGS-84 ellipsoidal distance (Vincenty's
// inverse formula, sub-millimetre). ok=false when the iteration does
// not converge (nearly antipodal points).
func vincentyDistance(lon1, lat1, lon2, lat2 float64) (float64, bool) {
	const a, f = 6378137.0, 1 / 298.257223563
	b := a * (1 - f)
	rad := math.Pi / 180
	L := (lon2 - lon1) * rad
	U1 := math.Atan((1 - f) * math.Tan(lat1*rad))
	U2 := math.Atan((1 - f) * math.Tan(lat2*rad))
	sinU1, cosU1 := math.Sincos(U1)
	sinU2, cosU2 := math.Sincos(U2)
	lambda := L
	var sinSigma, cosSigma, sigma, cos2Alpha, cos2SigmaM float64
	for iter := 0; ; iter++ {
		if iter == 200 {
			return 0, false
		}
		sinL, cosL := math.Sincos(lambda)
		sinSigma = math.Hypot(cosU2*sinL, cosU1*sinU2-sinU1*cosU2*cosL)
		if sinSigma == 0 {
			return 0, true // coincident points
		}
		cosSigma = sinU1*sinU2 + cosU1*cosU2*cosL
		sigma = math.Atan2(sinSigma, cosSigma)
		sinAlpha := cosU1 * cosU2 * sinL / sinSigma
		cos2Alpha = 1 - sinAlpha*sinAlpha
		cos2SigmaM = 0
		if cos2Alpha != 0 {
			cos2SigmaM = cosSigma - 2*sinU1*sinU2/cos2Alpha
		}
		C := f / 16 * cos2Alpha * (4 + f*(4-3*cos2Alpha))
		prev := lambda
		lambda = L + (1-C)*f*sinAlpha*(sigma+C*sinSigma*(cos2SigmaM+C*cosSigma*(-1+2*cos2SigmaM*cos2SigmaM)))
		if math.Abs(lambda-prev) < 1e-12 {
			break
		}
	}
	u2 := cos2Alpha * (a*a - b*b) / (b * b)
	A := 1 + u2/16384*(4096+u2*(-768+u2*(320-175*u2)))
	B := u2 / 1024 * (256 + u2*(-128+u2*(74-47*u2)))
	dSigma := B * sinSigma * (cos2SigmaM + B/4*(cosSigma*(-1+2*cos2SigmaM*cos2SigmaM)-
		B/6*cos2SigmaM*(-3+4*sinSigma*sinSigma)*(-3+4*cos2SigmaM*cos2SigmaM)))
	return b * A * (sigma - dSigma), true
}
