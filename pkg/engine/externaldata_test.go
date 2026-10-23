package engine

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestExternalData(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	csvPath := write("a.csv", []byte("name,n\nalpha,1\nbeta,x\ngamma,3,extra\n"))
	tsvPath := write("b.tsv", []byte("p\t7\nq\t8\n"))
	jsonPath := write("c.json", []byte(`{"name":"j","n":5,"d":{"k":1}}`+"\n"+`{"name":"k"}`+"\n"))
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write([]byte("z,9\n"))
	w.Close()
	gzPath := write("d.csv.gz", gz.Bytes())

	eng := diskEngineEmpty(t)
	expectWrRows(t, eng, `externaldata(name:string, n:long) ['`+csvPath+`'] with (ignoreFirstRecord=true)`,
		"alpha | 1", "beta | ", "gamma | 3")
	expectWrRows(t, eng, `let X = externaldata(name:string, n:long)
    [h'`+csvPath+`']; X | count`, "4")
	expectWrRows(t, eng, `external_data(s:string, n:long) ['`+tsvPath+`', '`+gzPath+`'] | summarize sum(n)`, "24")
	expectWrRows(t, eng, `externaldata(name:string, n:long, d:dynamic) [@'`+jsonPath+`'] with (format='multijson')`,
		`j | 5 | {"k":1}`, "k |  | ")
	expectErrContains(t, eng, `externaldata(a:string) ['`+filepath.Join(dir, "missing.csv")+`']`, "no such file")
}
