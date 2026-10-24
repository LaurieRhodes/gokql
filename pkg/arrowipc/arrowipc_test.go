package arrowipc

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

// render is a table as text: the schema, then one line per row.
func render(t *types.Table) string {
	var b strings.Builder
	for i, c := range t.Schema.Columns {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(c.Name + ":" + c.Type.String())
	}
	b.WriteString("\n")
	for _, r := range t.Rows {
		for i, c := range t.Schema.Columns {
			if i > 0 {
				b.WriteString(" | ")
			}
			if r[i] == nil {
				b.WriteString("null")
			} else {
				b.WriteString(types.FormatValue(r[i], c.Type))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// TestReadPyarrowFixtures reads files pyarrow wrote (gen_fixtures.py):
// the file format plain and with LZ4 and Zstandard compression, a stream
// with a delta dictionary, a sliced table and an empty file.
func TestReadPyarrowFixtures(t *testing.T) {
	for _, f := range []string{"all_plain.arrow", "all_lz4.feather", "all_zstd.feather", "delta.arrows", "sliced.arrows", "empty.arrow"} {
		t.Run(f, func(t *testing.T) {
			tbl, err := Read(filepath.Join("testdata", f))
			if err != nil {
				t.Fatal(err)
			}
			got := render(tbl)
			golden := filepath.Join("testdata", strings.TrimSuffix(f, filepath.Ext(f))+".golden")
			if strings.HasPrefix(f, "all_") {
				golden = filepath.Join("testdata", "all.golden")
			}
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("got\n%s\nwant\n%s", got, want)
			}
		})
	}
}

// TestWriteRead writes every KQL type, nulls and more than one batch,
// and reads it back.
func TestWriteRead(t *testing.T) {
	cols := []types.Column{
		{Name: "a", Type: types.TypeLong}, {Name: "b", Type: types.TypeInt},
		{Name: "c", Type: types.TypeReal}, {Name: "d", Type: types.TypeBool},
		{Name: "e", Type: types.TypeString}, {Name: "f", Type: types.TypeDatetime},
		{Name: "g", Type: types.TypeTimespan}, {Name: "h", Type: types.TypeDynamic},
		{Name: "i", Type: types.TypeGUID}, {Name: "j", Type: types.TypeDecimal},
	}
	in := types.NewTable("t", types.Schema{Columns: cols})
	dec, _ := types.ParseDecimal("1.25")
	for k := 0; k < batchRows+10; k++ {
		in.Rows = append(in.Rows, types.Row{int64(k), int32(-k), float64(k) / 4, k%2 == 0, "s" + strings.Repeat("x", k%3),
			int64(1704164645123456700) + int64(k), int64(936000000001), `{"k":[1,2]}`, "12345678-1234-1234-1234-123456789012", dec})
	}
	in.Rows = append(in.Rows, types.Row{nil, nil, nil, nil, "", nil, nil, nil, nil, nil})
	var buf bytes.Buffer
	if err := Write(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := Decode(buf.Bytes(), "t")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := render(out), render(in); got != want {
		t.Errorf("round trip differs (%d vs %d bytes of text)", len(got), len(want))
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("ARROW1\x00\x00"), []byte("not arrow at all"), {0xFF, 0xFF, 0xFF, 0xFF, 0x40, 0, 0, 0, 1, 2}} {
		if _, err := Decode(b, "x"); err == nil {
			t.Errorf("Decode(%q) succeeded", b)
		}
	}
}
