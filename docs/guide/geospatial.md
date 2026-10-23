# Geospatial

<!-- gokql:setup
.set-or-append Sites <| datatable(Site:string, Lon:real, Lat:real)
  ["Girsu", 46.1, 31.43,
   "Lagash", 46.41, 31.41,
   "Uruk", 45.64, 31.32,
   "Ur", 46.1, 30.96,
   "Nippur", 45.23, 32.13]

.set-or-append Regions <| datatable(Region:string, Shape:dynamic)
  ["South", dynamic({"type": "Polygon", "coordinates": [[[45.8, 30.7], [46.8, 30.7], [46.8, 31.6], [45.8, 31.6], [45.8, 30.7]]]}),
   "North", dynamic({"type": "Polygon", "coordinates": [[[44.8, 31.8], [45.8, 31.8], [45.8, 32.6], [44.8, 32.6], [44.8, 31.8]]]})]
-->

gokql implements ADX's geospatial functions on the sphere, with the
same S2 geometry ADX uses: distances, containment, areas, buffers,
unions and intersections; geohash, S2 and H3 cells; and the
`geo_polygon_lookup` and `geo_line_lookup` plugins. Coordinates are
longitude first, in degrees; shapes are GeoJSON as `dynamic`;
distances and areas are in metres and square metres.

## Distances and containment

```kql
Sites
| extend KmToGirsu = round(geo_distance_2points(Lon, Lat, 46.1, 31.43) / 1000, 1)
| where Site != "Girsu"
| order by KmToGirsu asc
```

```output
Site    Lon    Lat    KmToGirsu
────    ────   ────   ─────────
Lagash  46.41  31.41  29.5
Uruk    45.64  31.32  45.4
Ur      46.1   30.96  52.3
Nippur  45.23  32.13  113.2
(4 rows)
```

```kql
Sites
| extend InSouth = geo_point_in_polygon(Lon, Lat, toscalar(Regions | where Region == "South" | project Shape))
| project Site, InSouth
```

```output
Site    InSouth
────    ───────
Girsu   true
Lagash  true
Uruk    false
Ur      true
Nippur  false
(5 rows)
```

## Assigning points to regions

`geo_polygon_lookup` joins points to the polygons that contain them,
the efficient way to label many points:

```kql
Sites
| evaluate geo_polygon_lookup(Regions, Shape, Lon, Lat)
| project Site, Region
| order by Site asc
```

```output
Site    Region
────    ──────
Girsu   South
Lagash  South
Nippur  North
Ur      South
(4 rows)
```

Uruk lies in neither region and is left out; pass
`return_unmatched = true` as the plugin's next argument to keep such
rows with an empty region.

## Shapes

```kql
Regions
| project Region,
    AreaKm2 = round(geo_polygon_area(Shape) / 1e6),
    PerimeterKm = round(geo_polygon_perimeter(Shape) / 1000),
    Centroid = geo_polygon_centroid(Shape)
```

```output
Region  AreaKm2  PerimeterKm  Centroid
──────  ───────  ───────────  ────────
South   9523     390          {"type":"Point","coordinates":[46.299999999999784,31.150253634420086]}
North   8370     366          {"type":"Point","coordinates":[45.300000000000018,32.200397485906684]}
(2 rows)
```

Buffers grow points, lines and polygons by a distance
(`geo_point_buffer`, `geo_line_buffer`, `geo_polygon_buffer`);
`geo_union_polygons_array` and `geo_intersection_2polygons` combine
shapes; `geo_polygon_simplify`, `geo_line_simplify` and the densify
functions reduce or add vertices.

## Grid cells

Cells bucket points for aggregation and joins. All three systems are
available:

```kql
Sites
| project Site,
    Geohash = geo_point_to_geohash(Lon, Lat, 4),
    S2 = geo_point_to_s2cell(Lon, Lat, 8),
    H3 = geo_point_to_h3cell(Lon, Lat, 5)
| order by Site asc
```

```output
Site    Geohash  S2     H3
────    ───────  ────   ────
Girsu   tj8f     3fe0b  852c860ffffffff
Lagash  tj94     3fe75  852c862bfffffff
Nippur  tj8n     3fe21  852c84cbfffffff
Ur      tj8b     3fde1  852c864ffffffff
Uruk    tj86     3fe0f  852c86c7fffffff
(5 rows)
```

Each system has functions for a cell's polygon, centre and neighbours,
and for covering a polygon with cells (`geo_polygon_to_s2cells`,
`geo_polygon_to_h3cells`), which makes point-in-region joins over
large tables cheap: join on the cell, then confirm with
`geo_point_in_polygon`.

## Functions

| Group | Functions |
|---|---|
| Measures | `geo_distance_2points`, `geo_distance_point_to_line`, `geo_distance_point_to_polygon`, `geo_line_length`, `geo_polygon_area`, `geo_polygon_perimeter`, `geo_angle`, `geo_azimuth` |
| Predicates | `geo_point_in_circle`, `geo_point_in_polygon`, `geo_intersects_2lines`, `geo_intersects_2polygons`, `geo_intersects_line_with_polygon` |
| Constructions | `geo_point_buffer`, `geo_line_buffer`, `geo_polygon_buffer`, `geo_intersection_2lines`, `geo_intersection_2polygons`, `geo_intersection_line_with_polygon`, `geo_union_lines_array`, `geo_union_polygons_array`, `geo_line_centroid`, `geo_polygon_centroid`, `geo_closest_point_on_line`, `geo_closest_point_on_polygon`, `geo_line_interpolate_point`, `geo_line_locate_point` |
| Simplify and densify | `geo_line_simplify`, `geo_polygon_simplify`, `geo_simplify_polygons_array`, `geo_line_densify`, `geo_polygon_densify` |
| Geohash | `geo_point_to_geohash`, `geo_geohash_to_central_point`, `geo_geohash_to_polygon`, `geo_geohash_neighbors` |
| S2 | `geo_point_to_s2cell`, `geo_s2cell_to_central_point`, `geo_s2cell_to_polygon`, `geo_s2cell_neighbors`, `geo_polygon_to_s2cells`, `geo_line_to_s2cells` |
| H3 | `geo_point_to_h3cell`, `geo_h3cell_to_central_point`, `geo_h3cell_to_polygon`, `geo_h3cell_parent`, `geo_h3cell_children`, `geo_h3cell_level`, `geo_h3cell_neighbors`, `geo_h3cell_rings`, `geo_polygon_to_h3cells` |
| Conversion | `geo_from_wkt` |
| Plugins | `geo_polygon_lookup`, `geo_line_lookup` |

`geo_info_from_ip_address` is not supported (it needs a GeoIP
database). Results agree with ADX to floating-point precision; where
ADX leaves an order unspecified (the order of neighbour cells, for
example), gokql's may differ.

## See also

- [Functions reference](../reference/functions.md#geospatial-functions)
