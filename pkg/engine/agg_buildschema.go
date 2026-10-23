package engine

// agg_buildschema.go — buildschema(DynamicExpr): the minimal schema
// admitting every value. A scalar is its type name (long, double,
// string, bool); an object maps each key seen to its schema; an array
// is {"`indexer`": item schema}. A place that held several kinds is an
// array: its object and array shapes merged into one object first,
// then the scalar types in order of first appearance (the emulator).
// Keys keep their order of first appearance.
// Nulls add nothing.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

type schemaNode struct {
	kinds   []string // "t:<type>", "obj", "arr" in order of first appearance
	objKeys []string
	obj     map[string]*schemaNode
	arr     *schemaNode
}

func (n *schemaNode) addKind(k string) {
	for _, x := range n.kinds {
		if x == k {
			return
		}
	}
	n.kinds = append(n.kinds, k)
}

// addJSON merges the next JSON value from dec into n.
func (n *schemaNode) addJSON(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			n.addKind("obj")
			if n.obj == nil {
				n.obj = map[string]*schemaNode{}
			}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return err
				}
				k, _ := kt.(string)
				c := n.obj[k]
				if c == nil {
					c = &schemaNode{}
					n.obj[k] = c
					n.objKeys = append(n.objKeys, k)
				}
				if err := c.addJSON(dec); err != nil {
					return err
				}
			}
			_, err = dec.Token() // }
			return err
		case '[':
			n.addKind("arr")
			if n.arr == nil {
				n.arr = &schemaNode{}
			}
			for dec.More() {
				if err := n.arr.addJSON(dec); err != nil {
					return err
				}
			}
			_, err = dec.Token() // ]
			return err
		}
	case json.Number:
		if strings.ContainsAny(t.String(), ".eE") {
			n.addKind("t:double")
		} else {
			n.addKind("t:long")
		}
	case string:
		n.addKind("t:string")
	case bool:
		n.addKind("t:bool")
	}
	return nil // null
}

func (n *schemaNode) render(b *bytes.Buffer) {
	// As the emulator renders a place: its object and array shapes
	// merged into one object ("`indexer`" holding the array items),
	// first, then the scalar types in order of first appearance.
	var complexKinds, prims []string
	for _, k := range n.kinds {
		if k == "obj" || k == "arr" {
			complexKinds = append(complexKinds, k)
		} else {
			prims = append(prims, k)
		}
	}
	entries := len(prims)
	if len(complexKinds) > 0 {
		entries++
	}
	if entries == 0 {
		b.WriteString("null")
		return
	}
	if entries > 1 {
		b.WriteByte('[')
	}
	if len(complexKinds) > 0 {
		b.WriteByte('{')
		first := true
		for _, k := range complexKinds {
			if k == "obj" {
				for _, key := range n.objKeys {
					if !first {
						b.WriteByte(',')
					}
					first = false
					kb, _ := json.Marshal(key)
					b.Write(kb)
					b.WriteByte(':')
					n.obj[key].render(b)
				}
				continue
			}
			if !first {
				b.WriteByte(',')
			}
			first = false
			b.WriteString("\"`indexer`\":")
			n.arr.render(b)
		}
		b.WriteByte('}')
	}
	for i, k := range prims {
		if i > 0 || len(complexKinds) > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k[2:])
		b.Write(kb)
	}
	if entries > 1 {
		b.WriteByte(']')
	}
}

func computeBuildSchema(eng *Engine, agg parser.Aggregation, rows []types.Row, schema *types.Schema) (types.Value, error) {
	if len(agg.Args) != 1 {
		return nil, fmt.Errorf("buildschema: expected 1 argument")
	}
	root := &schemaNode{}
	for _, r := range rows {
		v, err := evalExpr(eng, agg.Args[0], schema, r)
		if err != nil {
			return nil, err
		}
		if v == nil {
			continue
		}
		text, ok := v.(string)
		if !ok {
			b, _ := json.Marshal(v)
			text = string(b)
		}
		dec := json.NewDecoder(strings.NewReader(text))
		dec.UseNumber()
		if err := root.addJSON(dec); err != nil {
			// not JSON: a plain string value
			root.addKind("t:string")
		}
	}
	if len(root.kinds) == 0 {
		return nil, nil
	}
	var b bytes.Buffer
	root.render(&b)
	return b.String(), nil
}
