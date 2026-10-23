package engine

// plugin_infer_schema.go — evaluate infer_storage_schema(Options) and
// infer_storage_schema_with_suggestions(Options) over local files and
// directories (paths or file://) and http(s) files (no listing), in the
// formats externaldata reads: csv, tsv, psv, scsv, txt, json and
// multijson (.gz too). StorageContainers entries lose a ";key" suffix;
// FileNamePrefix and FileExtension filter a directory's files; Mode is
// last (default; the newest file), any (the first) or all (merged).
// Up to 10000 records per file are read.
//
// A column's type is the narrowest that every non-empty value takes:
// bool, long, real (long and real merge to real), datetime, guid or
// string, with JSON objects and arrays dynamic and other mixtures
// string (dynamic for JSON). Delimited columns are Column1..N unless
// InferenceOptions.UseFirstRowAsHeader. The suggestions variant reports
// each column ADX's way, recommending string for long columns named as
// ids and datetime (DateTimeFromUnixMilliseconds/Seconds) for long
// columns whose name speaks of time and whose values lie in the epoch
// ranges.

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["infer_storage_schema"] = func(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
		return e.inferStorageSchema("infer_storage_schema", args, false)
	}
	evaluatePlugins["infer_storage_schema_with_suggestions"] = func(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
		return e.inferStorageSchema("infer_storage_schema_with_suggestions", args, true)
	}
}

const inferMaxRecords = 10000

// inferCol accumulates what a column's values have been.
type inferCol struct {
	name               string
	seen               map[string]bool // bool long real datetime guid string dynamic
	minLong, maxLong   int64
	haveLong, anyValue bool
}

func (c *inferCol) note(kind string) {
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	c.seen[kind] = true
	c.anyValue = true
}

func (c *inferCol) noteLong(v int64) {
	c.note("long")
	if !c.haveLong || v < c.minLong {
		c.minLong = v
	}
	if !c.haveLong || v > c.maxLong {
		c.maxLong = v
	}
	c.haveLong = true
}

func (c *inferCol) kind(json bool) string {
	if !c.anyValue {
		return "string"
	}
	if len(c.seen) == 1 {
		for k := range c.seen {
			return k
		}
	}
	if len(c.seen) == 2 && c.seen["long"] && c.seen["real"] {
		return "real"
	}
	if json {
		return "dynamic"
	}
	return "string"
}

// classifyText is a delimited field's (or JSON string's) kind.
func classifyText(c *inferCol, s string, numbers bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if numbers {
		if l := strings.ToLower(s); l == "true" || l == "false" {
			c.note("bool")
			return
		}
		if v, err := types.ParseValue(s, types.TypeLong); err == nil && v != nil {
			c.noteLong(types.ToInt64(v))
			return
		}
		if v, err := types.ParseValue(s, types.TypeReal); err == nil && v != nil {
			c.note("real")
			return
		}
	}
	if len(s) >= 8 && (s[0] >= '0' && s[0] <= '9') {
		if v, err := types.ParseValue(s, types.TypeDatetime); err == nil && v != nil {
			c.note("datetime")
			return
		}
	}
	if len(s) == 36 && s[8] == '-' && s[13] == '-' {
		if v, err := types.ParseValue(s, types.TypeGUID); err == nil && v != nil {
			c.note("guid")
			return
		}
	}
	c.note("string")
}

func (e *Engine) inferStorageSchema(name string, args []string, suggestions bool) (*types.Table, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("%s: expected (Options)", name)
	}
	opts, err := pluginBag(e, name, "Options", args[0])
	if err != nil {
		return nil, err
	}
	get := func(k string) interface{} {
		for kk, v := range opts {
			if strings.EqualFold(kk, k) {
				return v
			}
		}
		return nil
	}
	var containers []string
	switch x := get("StorageContainers").(type) {
	case []interface{}:
		for _, v := range x {
			if s, ok := v.(string); ok {
				containers = append(containers, s)
			}
		}
	case string:
		containers = append(containers, x)
	}
	if len(containers) == 0 {
		return nil, fmt.Errorf("%s: Options.StorageContainers is required", name)
	}
	format, _ := get("DataFormat").(string)
	format = strings.ToLower(format)
	if format == "" {
		return nil, fmt.Errorf("%s: Options.DataFormat is required", name)
	}
	delim := map[string]rune{"csv": ',', "tsv": '\t', "tsve": '\t', "psv": '|', "scsv": ';', "sohsv": '\x01'}
	_, delimited := delim[format]
	isJSON := format == "json" || format == "multijson"
	if !delimited && !isJSON && format != "txt" && format != "raw" {
		return nil, fmt.Errorf("%s: DataFormat %q is not supported here (csv, tsv, psv, scsv, txt, json, multijson)", name, format)
	}
	ext, _ := get("FileExtension").(string)
	prefix, _ := get("FileNamePrefix").(string)
	mode, _ := get("Mode").(string)
	mode = strings.ToLower(mode)
	if mode == "" {
		mode = "last"
	}
	header := false
	if io, ok := get("InferenceOptions").(map[string]interface{}); ok {
		for k, v := range io {
			if strings.EqualFold(k, "UseFirstRowAsHeader") {
				header, _ = v.(bool)
			}
		}
	}

	// the files
	type file struct {
		uri   string
		mtime int64
	}
	var files []file
	for _, c := range containers {
		if i := strings.Index(c, ";"); i >= 0 {
			c = c[:i]
		}
		l := strings.ToLower(c)
		if strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") {
			files = append(files, file{uri: c})
			continue
		}
		p := strings.TrimPrefix(c, "file://")
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if !st.IsDir() {
			files = append(files, file{uri: p, mtime: st.ModTime().UnixNano()})
			continue
		}
		err = filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			base := filepath.Base(path)
			if (prefix == "" || strings.HasPrefix(base, prefix)) && (ext == "" || strings.HasSuffix(base, ext)) {
				files = append(files, file{uri: path, mtime: info.ModTime().UnixNano()})
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s: no files found", name)
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].uri < files[j].uri })
	switch mode {
	case "any":
		files = files[:1]
	case "last":
		best := 0
		for i := range files {
			if files[i].mtime > files[best].mtime {
				best = i
			}
		}
		files = files[best : best+1]
	case "all":
	default:
		return nil, fmt.Errorf("%s: Mode must be any, last or all", name)
	}

	var cols []*inferCol
	byName := map[string]*inferCol{}
	colAt := func(i int) *inferCol {
		for len(cols) <= i {
			c := &inferCol{name: fmt.Sprintf("Column%d", len(cols)+1)}
			cols = append(cols, c)
		}
		return cols[i]
	}
	named := func(n string) *inferCol {
		if c, ok := byName[n]; ok {
			return c
		}
		c := &inferCol{name: n}
		byName[n] = c
		cols = append(cols, c)
		return c
	}
	for _, f := range files {
		rc, err := e.openExternal(f.uri)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		err = func() error {
			defer rc.Close()
			switch {
			case delimited:
				r := csv.NewReader(bufio.NewReader(rc))
				r.Comma = delim[format]
				r.FieldsPerRecord = -1
				r.LazyQuotes = true
				var names []string
				for n := 0; n < inferMaxRecords; {
					rec, err := r.Read()
					if err == io.EOF {
						break
					}
					if err != nil {
						return err
					}
					if header && names == nil {
						names = rec
						continue
					}
					for i, v := range rec {
						var c *inferCol
						if names != nil && i < len(names) {
							c = named(strings.TrimSpace(names[i]))
						} else {
							c = colAt(i)
						}
						classifyText(c, v, true)
					}
					n++
				}
			case isJSON:
				dec := json.NewDecoder(bufio.NewReader(rc))
				dec.UseNumber()
				noteObj := func(raw json.RawMessage) {
					var m map[string]interface{}
					d := json.NewDecoder(strings.NewReader(string(raw)))
					d.UseNumber()
					if d.Decode(&m) != nil {
						return
					}
					for _, k := range objectKeys(raw) {
						c := named(k)
						switch v := m[k].(type) {
						case nil:
						case bool:
							c.note("bool")
						case json.Number:
							if i, err := v.Int64(); err == nil {
								c.noteLong(i)
							} else {
								c.note("real")
							}
						case string:
							classifyText(c, v, false)
						default:
							c.note("dynamic")
						}
					}
				}
				for n := 0; n < inferMaxRecords; n++ {
					var raw json.RawMessage
					if err := dec.Decode(&raw); err == io.EOF {
						break
					} else if err != nil {
						return err
					}
					t := strings.TrimSpace(string(raw))
					switch {
					case strings.HasPrefix(t, "{"):
						noteObj(raw)
					case strings.HasPrefix(t, "["):
						var items []json.RawMessage
						if json.Unmarshal(raw, &items) == nil {
							for _, it := range items {
								if strings.HasPrefix(strings.TrimSpace(string(it)), "{") {
									noteObj(it)
								}
							}
						}
					}
				}
			default: // txt: one string column
				named("Line").note("string")
			}
			return nil
		}()
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", name, f.uri, err)
		}
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("%s: no records found", name)
	}
	if !suggestions {
		parts := make([]string, len(cols))
		for i, c := range cols {
			parts[i] = schemaIdent(c.name) + ":" + c.kind(isJSON)
		}
		out := types.NewTable("", types.Schema{Columns: []types.Column{{Name: "CslSchema", Type: types.TypeString}}})
		out.AddRow(types.Row{strings.Join(parts, ", ")})
		return out, nil
	}
	cslType := func(t string) map[string]interface{} {
		num := t == "long" || t == "int" || t == "real" || t == "decimal"
		return map[string]interface{}{"type": t, "IsNumeric": num, "IsSummable": num || t == "datetime" || t == "timespan"}
	}
	var list []interface{}
	for _, c := range cols {
		t := c.kind(isJSON)
		rec, mapping := t, "None"
		ln := strings.ToLower(c.name)
		if t == "long" && c.haveLong {
			timeName := strings.Contains(ln, "time") || strings.Contains(ln, "date") || strings.HasSuffix(ln, "ts") || strings.Contains(ln, "epoch")
			switch {
			case strings.HasSuffix(ln, "id"):
				rec = "string"
			case timeName && c.minLong >= 1e11 && c.maxLong < 1e14:
				rec, mapping = "datetime", "DateTimeFromUnixMilliseconds"
			case timeName && c.minLong >= 1e9 && c.maxLong < 1e10:
				rec, mapping = "datetime", "DateTimeFromUnixSeconds"
			}
		}
		list = append(list, map[string]interface{}{
			"OriginalColumn":                  map[string]interface{}{"Name": c.name, "CslType": cslType(t)},
			"RecommendedColumn":               map[string]interface{}{"Name": c.name, "CslType": cslType(rec)},
			"ApplicableTransformationMapping": mapping,
		})
	}
	b, _ := json.Marshal(map[string]interface{}{"Columns": list})
	out := types.NewTable("", types.Schema{Columns: []types.Column{{Name: "SuggestedTableSchema", Type: types.TypeString}}})
	out.AddRow(types.Row{string(b)})
	return out, nil
}

// objectKeys lists a JSON object's keys in document order.
func objectKeys(raw json.RawMessage) []string {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var keys []string
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return keys
		}
		k, _ := t.(string)
		keys = append(keys, k)
		var skip json.RawMessage
		if d.Decode(&skip) != nil {
			return keys
		}
	}
	return keys
}

// schemaIdent writes a column name as a schema accepts it.
func schemaIdent(n string) string {
	ok := n != ""
	for i, r := range n {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			ok = false
			break
		}
	}
	if ok {
		return n
	}
	return "['" + strings.ReplaceAll(n, "'", "\\'") + "']"
}
