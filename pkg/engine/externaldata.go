package engine

// externaldata.go — externaldata (schema) [uris] [with (...)]: rows read
// from local files, file:// or http(s):// URLs, as ADX reads blobs.
// Formats: csv (default), tsv, psv, scsv, txt (one column per line),
// json / multijson (objects matched to columns by name). Fields map to
// the declared columns by position (csv family); a field that does not
// parse as its column's type is null, missing fields are null and
// extra ones are ignored. ignoreFirstRecord=true skips a header line.
// A .gz URI is decompressed.

import (
	"bufio"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

const externalDataMaxBytes = 1 << 30

func (e *Engine) openExternal(uri string) (io.ReadCloser, error) {
	lower := strings.ToLower(uri)
	var rc io.ReadCloser
	switch {
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		if err := e.checkCallout("external_data", uri); err != nil {
			return nil, err
		}
		client := &http.Client{Timeout: 5 * time.Minute}
		resp, err := client.Get(uri)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 != 2 {
			resp.Body.Close()
			return nil, fmt.Errorf("%s: HTTP %s", uri, resp.Status)
		}
		rc = resp.Body
	default:
		f, err := os.Open(strings.TrimPrefix(uri, "file://"))
		if err != nil {
			return nil, err
		}
		rc = f
	}
	path := lower
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(rc)
		if err != nil {
			rc.Close()
			return nil, fmt.Errorf("%s: %w", uri, err)
		}
		return struct {
			io.Reader
			io.Closer
		}{gz, rc}, nil
	}
	return rc, nil
}

func externalFormat(tf *parser.TableFunc, uri string) string {
	if f := strings.ToLower(tf.Props["format"]); f != "" {
		return f
	}
	path := strings.ToLower(uri)
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimSuffix(path, ".gz")
	for _, ext := range []string{"tsv", "psv", "scsv", "json", "multijson", "txt"} {
		if strings.HasSuffix(path, "."+ext) {
			return ext
		}
	}
	return "csv"
}

func externalValue(field string, t types.KQLType) types.Value {
	if t == types.TypeString {
		return field
	}
	if strings.TrimSpace(field) == "" {
		return nil
	}
	v, err := types.ParseValue(strings.TrimSpace(field), t)
	if err != nil {
		return nil
	}
	return v
}

func (e *Engine) readExternalData(tf *parser.TableFunc) (*types.Table, error) {
	out := types.NewTable("", *tf.Schema)
	cols := tf.Schema.Columns
	skipFirst := strings.EqualFold(tf.Props["ignorefirstrecord"], "true")
	for _, uri := range tf.URIs {
		rc, err := e.openExternal(uri)
		if err != nil {
			return nil, err
		}
		r := io.LimitReader(rc, externalDataMaxBytes)
		format := externalFormat(tf, uri)
		err = func() error {
			defer rc.Close()
			switch format {
			case "csv", "tsv", "psv", "scsv":
				cr := csv.NewReader(r)
				cr.FieldsPerRecord = -1
				cr.LazyQuotes = true
				cr.Comma = map[string]rune{"csv": ',', "tsv": '\t', "psv": '|', "scsv": ';'}[format]
				first := true
				for {
					rec, err := cr.Read()
					if err == io.EOF {
						return nil
					}
					if err != nil {
						return fmt.Errorf("%s: %w", uri, err)
					}
					if first && skipFirst {
						first = false
						continue
					}
					first = false
					row := make(types.Row, len(cols))
					for i := range cols {
						if i < len(rec) {
							row[i] = externalValue(rec[i], cols[i].Type)
						}
					}
					out.AddRow(row)
				}
			case "txt":
				sc := bufio.NewScanner(r)
				sc.Buffer(make([]byte, 1<<20), 64<<20)
				first := true
				for sc.Scan() {
					if first && skipFirst {
						first = false
						continue
					}
					first = false
					row := make(types.Row, len(cols))
					row[0] = externalValue(sc.Text(), cols[0].Type)
					out.AddRow(row)
				}
				return sc.Err()
			case "json", "multijson":
				dec := json.NewDecoder(r)
				dec.UseNumber()
				for {
					var obj map[string]interface{}
					if err := dec.Decode(&obj); err == io.EOF {
						return nil
					} else if err != nil {
						return fmt.Errorf("%s: %w", uri, err)
					}
					row := make(types.Row, len(cols))
					for i, c := range cols {
						v, ok := obj[c.Name]
						if !ok || v == nil {
							continue
						}
						if c.Type == types.TypeDynamic {
							b, _ := json.Marshal(v)
							row[i] = string(b)
							continue
						}
						if s, isStr := v.(string); isStr {
							row[i] = externalValue(s, c.Type)
						} else {
							row[i] = externalValue(fmt.Sprint(v), c.Type)
						}
					}
					out.AddRow(row)
				}
			}
			return fmt.Errorf("unsupported format %q (csv, tsv, psv, scsv, txt, json, multijson)", format)
		}()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
