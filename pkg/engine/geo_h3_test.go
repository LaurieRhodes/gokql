package engine

import "testing"

// Expected values are the Kusto emulator's answers.
func TestGeoH3Functions(t *testing.T) {
	poly := `dynamic({"type":"Polygon","coordinates":[[[-3.659,40.553],[-3.913,40.409],[-3.729,40.273],[-3.524,40.440],[-3.659,40.553]]]})`
	cases := []struct{ q, want string }{
		{`print geo_point_to_h3cell(-74.04450446039874, 40.689250859314974)`, "862a1072fffffff"},
		{`print geo_point_to_h3cell(180, 0, 3)`, "837eb5fffffffff"},
		{`print geo_point_to_h3cell(1, 1, 15)`, "8f7541ad5a726dc"},
		{`print geo_point_to_h3cell(1, 90, 1)`, "81033ffffffffff"},
		{`print geo_point_to_h3cell(1, 1, 16)`, ""},
		{`print geo_point_to_h3cell(181, 1, 1)`, ""},
		{`print geo_h3cell_level(" 0x862A1072FFFFFFF")`, "6"},
		{`print isnull(geo_h3cell_level("862a1072ffffff"))`, "true"},
		{`print geo_h3cell_parent("862a1072fffffff")`, "852a1073fffffff"},
		{`print geo_h3cell_parent("862a1072fffffff", 0)`, "802bfffffffffff"},
		{`print geo_h3cell_parent("862a1072fffffff", 6)`, ""},
		{`print geo_h3cell_parent("8009fffffffffff")`, ""},
		{`print geo_h3cell_children("862a1072fffffff")`, `["872a10728ffffff","872a10729ffffff","872a1072affffff","872a1072bffffff","872a1072cffffff","872a1072dffffff","872a1072effffff"]`},
		{`print array_length(geo_h3cell_children("8009fffffffffff", 5))`, "14006"},
		{`print isnull(geo_h3cell_children("862a1072fffffff", 12))`, "true"},
		{`print isnull(geo_h3cell_children("862a1072fffffff", 6))`, "true"},
		{`print geo_h3cell_rings("861f8894fffffff", 1)`, `[["861f8894fffffff"],["861f88947ffffff","861f8895fffffff","861f88867ffffff","861f8d497ffffff","861f8d4b7ffffff","861f8896fffffff"]]`},
		{`print geo_h3cell_rings("8009fffffffffff", 1)`, `[["8009fffffffffff"],["8007fffffffffff","8019fffffffffff","8001fffffffffff","801ffffffffffff","8011fffffffffff"]]`},
		{`print array_length(geo_h3cell_rings("862a1072fffffff", 142))`, "143"},
		{`print isnull(geo_h3cell_rings("862a1072fffffff", 143))`, "true"},
		{`print array_sort_asc(geo_h3cell_neighbors("862a1072fffffff"))`, `["862a100d7ffffff","862a100dfffffff","862a10707ffffff","862a1070fffffff","862a10727ffffff","862a10777ffffff"]`},
		{`print isnull(geo_h3cell_to_central_point("1"))`, "true"},
		{`print array_sort_asc(geo_polygon_to_h3cells(` + poly + `))`, `["86390ca07ffffff","86390ca27ffffff","86390ca2fffffff","86390ca37ffffff","86390cb07ffffff","86390cb0fffffff","86390cb17ffffff","86390cb1fffffff","86390cb57ffffff","86390cb87ffffff","86390cb8fffffff","86390cba7ffffff","86390cbafffffff"]`},
		{`print geo_polygon_to_h3cells(` + poly + `, 4)`, `["84390cbffffffff"]`},
		{`print array_length(geo_polygon_to_h3cells(` + poly + `, 9))`, "4635"},
		{`print array_length(geo_polygon_to_h3cells(` + poly + `, 6, 1000))`, "17"},
		{`print isnull(geo_polygon_to_h3cells(dynamic({"type":"Polygon","coordinates":[[[0,0],[0.001,0],[0.001,0.001],[0,0]]]})))`, "true"},
		{`print array_length(geo_polygon_to_h3cells(dynamic({"type":"Polygon","coordinates":[[[170,-10],[-170,-10],[-170,10],[170,10],[170,-10]]]}), 2))`, "73"},
		{`print array_length(geo_polygon_to_h3cells(dynamic({"type":"Polygon","coordinates":[[[-10,-10],[10,-10],[10,10],[-10,10],[-10,-10]],[[-5,-5],[-5,5],[5,5],[5,-5],[-5,-5]]]}), 3))`, "365"},
	}
	for _, c := range cases {
		tbl := queryResult(t, c.q)
		expectCell(t, tbl, 0, 0, c.want)
	}
}
