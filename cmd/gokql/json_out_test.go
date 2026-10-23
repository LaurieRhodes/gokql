package main

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

func TestJSONRowKeepsColumnOrderAndTypes(t *testing.T) {
	cols := []types.Column{
		{Name: "z", Type: types.TypeLong},
		{Name: "a", Type: types.TypeDynamic},
		{Name: "r", Type: types.TypeReal},
		{Name: "n", Type: types.TypeLong},
		{Name: "s", Type: types.TypeString},
		{Name: "b", Type: types.TypeBool},
		{Name: "bad", Type: types.TypeDynamic},
	}
	row := types.Row{int64(1), `{"k":[1,2]}`, math.NaN(), nil, `x"y`, true, "not json"}
	var b bytes.Buffer
	appendJSONRow(&b, cols, row)
	want := `{"z":1,"a":{"k":[1,2]},"r":"NaN","n":null,"s":"x\"y","b":true,"bad":"not json"}`
	if got := b.String(); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if !json.Valid(b.Bytes()) {
		t.Fatal("invalid JSON")
	}
}

func TestKustoTableEncodesNaNAndDynamic(t *testing.T) {
	tbl := types.NewTable("T", types.Schema{Columns: []types.Column{
		{Name: "r", Type: types.TypeReal},
		{Name: "a", Type: types.TypeDynamic},
	}})
	tbl.AddRow(types.Row{math.Inf(1), `[1,2]`})
	enc, err := json.Marshal(kustoTable("Table_0", tbl))
	if err != nil {
		t.Fatal(err)
	}
	if want := `"Rows":[["Infinity",[1,2]]]`; !bytes.Contains(enc, []byte(want)) {
		t.Fatalf("got %s, want it to contain %s", enc, want)
	}
}
