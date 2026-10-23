package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInferStorageSchema(t *testing.T) {
	dir := t.TempDir()
	write := func(n, s string) {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("part-1.csv", "id,name,score,when,ok\n1,a,1.5,2024-01-01,true\n2,b,2,2024-02-01 10:00,false\n")
	write("js-1.json", `{"source":"DataExplorer","created_at":"2022-04-10 15:47:57","author_id":739144091473215488,"time_millisec":1547083647000,"tags":[1]}`+"\n"+
		`{"source":"x","created_at":"2022-04-11 15:47:57","author_id":1,"time_millisec":1547083647001,"odd key":{"a":1}}`)
	d := strings.ReplaceAll(dir, `\`, `\\`)
	expectWrRows(t, discoverEngine(t, t.TempDir()), `evaluate infer_storage_schema(dynamic({'StorageContainers': ['`+d+`;secret'], 'DataFormat': 'csv', 'FileExtension': '.csv', 'InferenceOptions': {'UseFirstRowAsHeader': true}}))`,
		"id:long, name:string, score:real, when:datetime, ok:bool")
	expectWrRows(t, discoverEngine(t, t.TempDir()), `evaluate infer_storage_schema(dynamic({'StorageContainers': ['`+d+`'], 'DataFormat': 'multijson', 'FileNamePrefix': 'js-'}))`,
		"source:string, created_at:datetime, author_id:long, time_millisec:long, tags:dynamic, ['odd key']:dynamic")
	expectWrRows(t, discoverEngine(t, t.TempDir()), `evaluate infer_storage_schema_with_suggestions(dynamic({'StorageContainers': ['`+d+`'], 'DataFormat': 'json', 'FileNamePrefix': 'js-'}))
		| mv-expand c = parse_json(SuggestedTableSchema).Columns
		| project n = tostring(c.RecommendedColumn.Name), t = tostring(c.RecommendedColumn.CslType.type), m = tostring(c.ApplicableTransformationMapping)`,
		"source | string | None", "created_at | datetime | None", "author_id | string | None",
		"time_millisec | datetime | DateTimeFromUnixMilliseconds", "tags | dynamic | None", "odd key | dynamic | None")
}
