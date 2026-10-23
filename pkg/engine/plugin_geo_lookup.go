package engine

// plugin_geo_lookup.go — evaluate geo_polygon_lookup / geo_line_lookup:
//
//	T | evaluate geo_polygon_lookup(LookupTable, LookupPolygonKey,
//	    SourceLongitude, SourceLatitude [, radius] [, return_unmatched]
//	    [, lookup_area_radius] [, return_lookup_key])
//	T | evaluate geo_line_lookup(LookupTable, LookupLineKey,
//	    SourceLongitude, SourceLatitude, radius [, return_unmatched]
//	    [, lookup_area_radius] [, return_lookup_key])
//
// A join of T to LookupTable on location: a source point matches a
// polygon that contains it (geo_point_in_polygon) or, with a radius,
// lies within radius metres of it (geo_distance_point_to_polygon); it
// matches a line within radius metres (geo_distance_point_to_line).
// Output: the source columns, then the lookup columns, without the key
// column unless return_lookup_key is true (a lookup column whose name
// the source already has gets a "1" suffix, as with lookup).
// return_unmatched=true keeps unmatched source rows with nulls. Every
// matching lookup row is returned, in lookup-table order.
// lookup_area_radius only tunes ADX's internal matching and is
// accepted and ignored. Optional arguments may be positional or named.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["geo_polygon_lookup"] = func(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
		return e.applyGeoLookup("geo_polygon_lookup", false, in, args)
	}
	evaluatePlugins["geo_line_lookup"] = func(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
		return e.applyGeoLookup("geo_line_lookup", true, in, args)
	}
}

type geoLookupEntry struct {
	row   int
	shape *geoShape
	bound s2.Cap
}

func (e *Engine) applyGeoLookup(name string, lines bool, input *types.Table, args []string) (*types.Table, error) {
	optNames := []string{"radius", "return_unmatched", "lookup_area_radius", "return_lookup_key"}
	if len(args) < 4 {
		return nil, fmt.Errorf("%s(LookupTable, LookupKey, SourceLongitude, SourceLatitude, ...) needs at least 4 arguments", name)
	}
	opts := map[string]string{}
	pos := 0
	for _, a := range args[4:] {
		a = strings.TrimSpace(a)
		if k, v, ok := strings.Cut(a, "="); ok && !strings.ContainsAny(k, "\"'(") {
			k = strings.ToLower(strings.TrimSpace(k))
			known := false
			for _, n := range optNames {
				known = known || n == k
			}
			if !known {
				return nil, fmt.Errorf("%s: unknown argument %q", name, k)
			}
			opts[k] = strings.TrimSpace(v)
			continue
		}
		if pos >= len(optNames) {
			return nil, fmt.Errorf("%s: too many arguments", name)
		}
		if a != "" {
			opts[optNames[pos]] = a
		}
		pos++
	}
	boolOpt := func(k string) (bool, error) {
		v, ok := opts[k]
		if !ok {
			return false, nil
		}
		switch strings.ToLower(v) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return false, fmt.Errorf("%s: %s must be true or false", name, k)
	}
	returnUnmatched, err := boolOpt("return_unmatched")
	if err != nil {
		return nil, err
	}
	returnKey, err := boolOpt("return_lookup_key")
	if err != nil {
		return nil, err
	}
	radius, hasRadius := 0.0, false
	if v, ok := opts["radius"]; ok {
		r, err := e.geoLookupNumber(v)
		if err != nil || r < 0 {
			return nil, fmt.Errorf("%s: radius must be a non-negative number", name)
		}
		radius, hasRadius = r, true
	}
	if lines && !hasRadius {
		return nil, fmt.Errorf("%s: radius is required", name)
	}

	stmt, err := parser.Parse(strings.TrimSpace(args[0]))
	if err != nil {
		return nil, fmt.Errorf("%s: lookup table: %w", name, err)
	}
	lookup, err := e.Execute(stmt)
	if err != nil {
		return nil, fmt.Errorf("%s: lookup table: %w", name, err)
	}
	col := func(t *types.Table, which, n string) (int, error) {
		i := t.Schema.ColumnIndex(strings.TrimSpace(n))
		if i < 0 {
			return 0, fmt.Errorf("%s: %s column %q not found", name, which, strings.TrimSpace(n))
		}
		return i, nil
	}
	keyCol, err := col(lookup, "lookup", args[1])
	if err != nil {
		return nil, err
	}
	lngCol, err := col(input, "source", args[2])
	if err != nil {
		return nil, err
	}
	latCol, err := col(input, "source", args[3])
	if err != nil {
		return nil, err
	}

	outSchema := types.Schema{Columns: append([]types.Column(nil), input.Schema.Columns...)}
	var lkCols []int
	for i, c := range lookup.Schema.Columns {
		if i == keyCol && !returnKey {
			continue
		}
		nc := c
		for outSchema.ColumnIndex(nc.Name) >= 0 {
			nc.Name += "1"
		}
		outSchema.Columns = append(outSchema.Columns, nc)
		lkCols = append(lkCols, i)
	}

	// Parse each lookup shape once; a bounding cap grown by the radius
	// rejects most non-matches cheaply.
	grow := s1.Angle(radius/geoSphereRadius) * 1.0001
	var entries []geoLookupEntry
	for ri, r := range lookup.Rows {
		g := parseGeoShape(r[keyCol])
		if g == nil || (lines && !g.isLinear()) || (!lines && !g.isPolygonal()) {
			continue
		}
		b := s2.EmptyRect()
		for _, p := range g.polys {
			b = b.Union(p.RectBound())
		}
		for i := range g.lines {
			b = b.Union(g.lines[i].RectBound())
		}
		c := b.CapBound()
		if radius > 0 {
			c = c.Expanded(grow)
		}
		entries = append(entries, geoLookupEntry{row: ri, shape: g, bound: c})
	}
	limit := s1.Angle(radius / geoSphereRadius)

	out := types.NewTable(input.Name, outSchema)
	for _, sr := range input.Rows {
		matched := false
		lng, ok1 := geoCoord(sr[lngCol])
		lat, ok2 := geoCoord(sr[latCol])
		if p, ok := geoPoint(lng, lat); ok1 && ok2 && ok {
			for _, en := range entries {
				if !en.bound.ContainsPoint(p) {
					continue
				}
				var hit bool
				switch {
				case lines:
					d, _ := linesDistance(en.shape.lines, p)
					hit = d <= limit
				case hasRadius:
					d, _ := polygonDistance(en.shape, p)
					hit = d <= limit
				default:
					hit = en.shape.containsPoint(p)
				}
				if !hit {
					continue
				}
				matched = true
				row := make(types.Row, 0, len(outSchema.Columns))
				row = append(row, sr...)
				for _, c := range lkCols {
					row = append(row, lookup.Rows[en.row][c])
				}
				out.AddRow(row)
			}
		}
		if !matched && returnUnmatched {
			row := make(types.Row, len(outSchema.Columns))
			copy(row, sr)
			out.AddRow(row)
		}
	}
	return out, nil
}

// geoLookupNumber reads a numeric plugin argument: a literal, or a
// constant expression such as a let-bound scalar.
func (e *Engine) geoLookupNumber(s string) (float64, error) {
	if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return f, nil
	}
	stmt, err := parser.Parse("print __v = " + s)
	if err != nil {
		return 0, err
	}
	t, err := e.Execute(stmt)
	if err != nil {
		return 0, err
	}
	if len(t.Rows) != 1 || len(t.Rows[0]) != 1 {
		return 0, fmt.Errorf("not a scalar")
	}
	f, ok := geoCoord(t.Rows[0][0])
	if !ok {
		return 0, fmt.Errorf("not a number")
	}
	return f, nil
}
