package engine

// file_arrow.go: arrow("path") reads an Apache Arrow IPC file (Feather
// v2) or stream as a table (pkg/arrowipc), and vortex("path") reads one
// Vortex file.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/arrowipc"
	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func readArrowFile(path string) (*types.Table, error) { return arrowipc.Read(path) }

// readVortexFile reads one Vortex file through a temporary
// single-file database, so it is decoded exactly as a stored extent.
// Strings in a database extent are stored against the database's shared
// dictionaries, so the _Dictionaries extents beside the file are used too.
func (e *Engine) readVortexFile(path string) (t *types.Table, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "gokql-vortex-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	// An extent's table name is its file name up to the last "_"; shared
	// dictionaries are keyed by it, so keep the name when it has that shape.
	base := filepath.Base(abs)
	table := strings.TrimSuffix(base, filepath.Ext(base))
	if k := strings.LastIndexByte(table, '_'); k > 0 && filepath.Ext(base) == ".vtx" {
		table = table[:k]
	} else {
		table, base = "V", "V_18e000000000000000000000.vtx"
	}
	if err := os.Symlink(abs, filepath.Join(dir, base)); err != nil {
		return nil, err
	}
	dicts, _ := filepath.Glob(filepath.Join(filepath.Dir(abs), "_Dictionaries_*.vtx"))
	for _, d := range dicts {
		if d == abs {
			continue
		}
		if err := os.Symlink(d, filepath.Join(dir, filepath.Base(d))); err != nil {
			return nil, err
		}
	}
	defer func() {
		if r := recover(); r != nil {
			t, err = nil, fmt.Errorf("cannot decode %s: its string dictionaries are not beside it (%v)", filepath.Base(path), r)
		}
	}()
	cat, err := catalog.Open(dir)
	if err != nil {
		return nil, err
	}
	if cat.GetTable(table) == nil {
		return nil, fmt.Errorf("not a readable Vortex file")
	}
	t, err = e.forNested(cat).executeQuery(&parser.Query{Source: table})
	if err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	if ext := filepath.Ext(name); ext != "" {
		name = name[:len(name)-len(ext)]
	}
	t.Name = name
	return t, nil
}
