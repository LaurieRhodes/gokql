package engine

import "testing"

// Expected values are the Kusto reference examples.
func TestGeoFunctionsDocExamples(t *testing.T) {
	cases := []struct{ q, want string }{
		// points, lines, polygons
		{`print geo_point_in_circle(-122.143564, 47.535677, -122.100896, 47.527351, 3500)`, "true"},
		{`print geo_point_in_polygon(-73.9495, 40.7957, dynamic({"type":"Polygon","coordinates":[[[-73.92597198486328,40.87821814104651],[-73.94691467285156,40.85069618625578],[-73.94691467285156,40.841865966890786],[-74.01008605957031,40.7519385984599],[-74.01866912841797,40.704586878965245],[-74.01214599609375,40.699901911003046],[-73.99772644042969,40.70875101828792],[-73.97747039794922,40.71083299030839],[-73.97026062011719,40.7290474687069],[-73.97506713867186,40.734510542567644],[-73.97086334228516,40.74533952447939],[-73.96652221679688,40.75060450059279],[-73.9570951461792,40.75880846946801],[-73.9393281936646,40.7815034996788],[-73.92597198486328,40.87821814104651]]]}))`, "true"},
		{`print geo_line_densify(dynamic({"type":"LineString","coordinates":[[50,50],[51,51]]}))`,
			`{"type":"LineString","coordinates":[[50,50],[50.125,50.125],[50.25,50.25],[50.375,50.375],[50.5,50.5],[50.625,50.625],[50.75,50.75],[50.875,50.875],[51,51]]}`},
		{`print geo_polygon_densify(dynamic({"type":"Polygon","coordinates":[[[10,10],[11,10],[11,11],[10,11],[10,10]]]}))`,
			`{"type":"Polygon","coordinates":[[[10,10],[10.25,10],[10.5,10],[10.75,10],[11,10],[11,11],[10.75,11],[10.5,11],[10.25,11],[10,11],[10,10]]]}`},
		{`print isnull(geo_line_densify(dynamic({"type":"LineString","coordinates":[[50,50],[51,51]]}), 0))`, "true"},
		{`print geo_intersects_2lines(dynamic({"type":"LineString","coordinates":[[0,0],[2,2]]}), dynamic({"type":"LineString","coordinates":[[0,2],[2,0]]}))`, "true"},
		{`print geo_intersects_2lines(dynamic({"type":"LineString","coordinates":[[0,0],[1,0]]}), dynamic({"type":"LineString","coordinates":[[0,1],[1,1]]}))`, "false"},
		{`print geo_intersects_line_with_polygon(dynamic({"type":"LineString","coordinates":[[-1,0.5],[0.5,0.5]]}), dynamic({"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0]]]}))`, "true"},
		{`print geo_intersects_2polygons(dynamic({"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0]]]}), dynamic({"type":"Polygon","coordinates":[[[5,5],[6,5],[6,6],[5,6],[5,5]]]}))`, "false"},
		{`print geo_from_wkt("MULTIPOLYGON (((0 0, 2 0, 2 2, 0 0)),((10 10, 12 10, 12 12, 10 10)))")`,
			`{"type":"MultiPolygon","coordinates":[[[[0,0],[2,0],[2,2],[0,0]]],[[[10,10],[12,10],[12,12],[10,10]]]]}`},
		{`print geo_from_wkt("GEOMETRYCOLLECTION (POINT (1 1), LINESTRING (2 2, 3 3))")`,
			`{"type":"GeometryCollection","geometries":[{"type":"Point","coordinates":[1,1]},{"type":"LineString","coordinates":[[2,2],[3,3]]}]}`},
		{`print isnull(geo_from_wkt("LINESTRING"))`, "true"},
		// geohash
		{`print geo_point_to_geohash(-80.195829, 25.802215, 8)`, "dhwfz15h"},
		{`print geo_point_to_geohash(-122.303404, 47.570482)`, "c23n8"},
		{`print geo_point_to_geohash(200, 1, 8)`, ""},
		{`print geo_geohash_to_central_point("sunny")`, `{"type":"Point","coordinates":[42.47314453125,23.70849609375]}`},
		{`print geo_geohash_to_polygon("dr5ru")`, `{"type":"Polygon","coordinates":[[[-74.00390625,40.7373046875],[-73.9599609375,40.7373046875],[-73.9599609375,40.78125],[-74.00390625,40.78125],[-74.00390625,40.7373046875]]]}`},
		{`print geo_geohash_neighbors("sunny")`, `["sunnt","sunpj","sunnx","sunpn","sunnv","sunpp","sunnz","sunnw"]`},
		{`print isnull(geo_geohash_neighbors("a"))`, "true"},
		// S2
		{`print geo_point_to_s2cell(-80.195829, 25.802215, 8)`, "88d9b"},
		{`print geo_point_to_s2cell(10.6842, 53, 8)`, "47ae3"},
		{`print geo_point_to_s2cell(1, 1, 35)`, ""},
		{`print geo_s2cell_neighbors("89c259")`, `["89c25d","89c2f9","89c251","89c257","89c25f","89c25b","89c2f7","89c2f5"]`},
		{`print isnull(geo_s2cell_to_polygon("a"))`, "true"},
		{`print array_length(geo_polygon_to_s2cells(dynamic({"type":"Polygon","coordinates":[[[0,0],[0,50],[100,50],[0,0]]]}), 5))`, "286"},
		{`print isnull(geo_polygon_to_s2cells(dynamic({"type":"Polygon","coordinates":[[[0,0],[0,50],[100,50],[0,0]]]}), 30))`, "true"},
		{`print geo_line_to_s2cells(dynamic({"type":"LineString","coordinates":[[-0.087152,51.509596],[-0.088340,51.506110]]}), 14)`, `["48760351","48760357"]`},
	}
	for _, c := range cases {
		tbl := queryResult(t, c.q)
		expectCell(t, tbl, 0, 0, c.want)
	}
}

func TestGeoS2CellRoundTrip(t *testing.T) {
	// A point's cell contains the cell's central point at every level.
	for _, lvl := range []string{"0", "5", "11", "20", "30"} {
		tbl := queryResult(t, `let c = geo_point_to_s2cell(-73.98, 40.75, `+lvl+`);
print geo_point_to_s2cell(todouble(geo_s2cell_to_central_point(c).coordinates[0]), todouble(geo_s2cell_to_central_point(c).coordinates[1]), `+lvl+`) == c`)
		expectCell(t, tbl, 0, 0, "true")
	}
}

const geoLookupPolygons = `let polygons = datatable(polygon_name:string, polygon:dynamic)
[
    "New York", dynamic({"type":"Polygon", "coordinates":[[[-73.97375470114766,40.74300078124614],[-73.98653921014294,40.75486501361894],[-73.99910622331991,40.74112695466084],[-73.97375470114766,40.74300078124614]]]}),
    "Paris", dynamic({"type":"Polygon","coordinates":[[[2.57564669886321,48.769567764921334],[2.420098611499384,49.05163394896812],[2.1016783119165723,48.80113794475062],[2.57564669886321,48.769567764921334]]]}),
];
let locations = datatable(location_name:string, longitude:real, latitude:real)
[
    "Empire State Building", -73.98567337898565, 40.74842629977377,
    "National Museum of Mathematics", -73.98778501496217, 40.743565232771545,
    "Eiffel Tower", 2.294489426068907, 48.858263476169185,
    "London", -0.13245599272019604, 51.49879464208368,
    "Statue of Liberty",  -74.04462223203123, 40.689195627512674,
];
`

func TestGeoPolygonLookup(t *testing.T) {
	for q, want := range map[string]string{
		`locations | evaluate geo_polygon_lookup(polygons, polygon, longitude, latitude) | count`:                                                                                 "3",
		`locations | evaluate geo_polygon_lookup(polygons, polygon, longitude, latitude, return_unmatched = true) | count`:                                                        "5",
		`locations | evaluate geo_polygon_lookup(polygons, polygon, longitude, latitude, radius = 7000) | where location_name == "Statue of Liberty" | project polygon_name`:      "New York",
		`locations | evaluate geo_polygon_lookup(polygons, polygon, longitude, latitude) | project c = array_length(bag_keys(pack_all()))`:                                        "4",
		`locations | evaluate geo_polygon_lookup(polygons, polygon, longitude, latitude, return_lookup_key = true) | project c = array_length(bag_keys(pack_all()))`:              "5",
		`locations | evaluate geo_polygon_lookup(polygons, polygon, longitude, latitude) | summarize count() by polygon_name | where polygon_name == "New York" | project count_`: "2",
	} {
		tbl := queryResult(t, geoLookupPolygons+q)
		expectCell(t, tbl, 0, 0, want)
	}
}

func TestGeoLineLookup(t *testing.T) {
	pre := `let roads = datatable(road_name:string, road:dynamic)
[
    "5th Avenue NY", dynamic({"type":"LineString","coordinates":[[-73.97291864770574,40.76428551254824],[-73.99708638113894,40.73145135821781]]}),
    "Palace Ave", dynamic({"type":"LineString","coordinates":[[-0.18756982045002246,51.50245944666557],[-0.18908519740253382,51.50544952706903]]}),
];
let locations = datatable(location_name:string, longitude:real, latitude:real)
[
    "Empire State Building", -73.98567337898565, 40.74842629977377,
    "Grand Central Terminal", -73.97713140725149, 40.752730320824895,
    "Statue of Liberty",  -74.04462223203123, 40.689195627512674,
    "Kensington Palace", -0.1885272501232862,  51.504906159672316
];
`
	for q, want := range map[string]string{
		`locations | evaluate geo_line_lookup(roads, road, longitude, latitude, 100) | count`:                          "2",
		`locations | evaluate geo_line_lookup(roads, road, longitude, latitude, 350) | count`:                          "3",
		`locations | evaluate geo_line_lookup(roads, road, longitude, latitude, 100, return_unmatched = true) | count`: "4",
	} {
		tbl := queryResult(t, pre+q)
		expectCell(t, tbl, 0, 0, want)
	}
}
