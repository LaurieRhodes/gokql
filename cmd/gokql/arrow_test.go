package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/engine"
)

// TestArrowRoundTrip writes every KQL type with -mode arrow and reads it
// back with arrow(): values, nulls and types must survive.
func TestArrowRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cat, err := catalog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	st := &replState{eng: engine.New(cat), mode: "arrow", output: &buf}
	q := `datatable(a:long,b:int,c:real,d:bool,e:string,f:datetime,g:timespan,h:dynamic,i:guid,j:decimal)[` +
		`1,2,3.5,true,"x",datetime(2024-01-02 03:04:05.1234567),time(1.02:00:00.0000001),dynamic({"k":[1,2]}),guid(12345678-1234-1234-1234-123456789012),decimal(1.25),` +
		`long(null),int(null),real(null),bool(null),"",datetime(null),timespan(null),dynamic(null),guid(null),decimal(null)]`
	if err := st.runCommand(q); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "out.arrow")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func(q string) string {
		var out bytes.Buffer
		rs := &replState{eng: engine.New(cat), mode: "csv", output: &out}
		if err := rs.runCommand(q); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out.String())
	}
	want := read(q + ` | extend t=strcat(gettype(a),gettype(f),gettype(g),gettype(i),gettype(j))`)
	got := read(`arrow("` + path + `") | extend t=strcat(gettype(a),gettype(f),gettype(g),gettype(i),gettype(j))`)
	if got != want {
		t.Errorf("round trip differs\n got: %s\nwant: %s", got, want)
	}
	if s := read(`arrow("` + path + `") | getschema | project ColumnType`); s != "ColumnType\nlong\nint\nreal\nbool\nstring\ndatetime\ntimespan\ndynamic\nguid\ndecimal" {
		t.Errorf("schema: %s", s)
	}
}

// TestVortexFileSource reads one extent of a database with vortex(),
// including its dictionary-encoded strings.
func TestVortexFileSource(t *testing.T) {
	dir := t.TempDir()
	cat, err := catalog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	st := &replState{eng: engine.New(cat), mode: "csv", output: &out}
	if err := st.runCommand(`.set-or-append T <| range i from 1 to 100 step 1 | extend s=strcat("v", i % 7)`); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "T_*.vtx"))
	var big string
	var size int64
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil && fi.Size() > size {
			big, size = f, fi.Size()
		}
	}
	out.Reset()
	if err := st.runCommand(`vortex("` + big + `") | summarize count(), dcount(s), max(i)`); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "count_,dcount_s,max_i\n100,7,100" {
		t.Errorf("vortex(): %q", got)
	}
}
