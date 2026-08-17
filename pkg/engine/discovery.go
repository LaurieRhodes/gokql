package engine

// discovery.go — engine support for catalog-free (discovery-mode)
// databases. See pkg/catalog/discover.go for the model: the
// directory of self-describing .vtx files is the source of truth,
// every write commits by atomic rename of a uniquely named file, and
// concurrent sessions cannot conflict by construction.

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	// The catalog package discovers schemas from extent footers but the
	// dtype↔KQL mapping lives here; inject it.
	catalog.DiscoveredSchemaFn = vortexDTypeToKQL
}

// newExtentID allocates an extent identifier. Catalog mode keeps the
// short sequential hex id. Discovery mode has no allocation authority
// to serialize through, so ids must be unique without coordination:
// nanosecond timestamp plus 32 random bits, all lowercase hex to stay
// inside the <Table>_<hexid>.vtx discovery convention.
func (e *Engine) newExtentID() string {
	if !e.Catalog.IsDiscovery() {
		return fmt.Sprintf("%08x", e.Catalog.Database.Version+1)
	}
	var r [4]byte
	if _, err := rand.Read(r[:]); err != nil {
		binary.LittleEndian.PutUint32(r[:], uint32(os.Getpid()))
	}
	return fmt.Sprintf("%015x%08x", uint64(time.Now().UnixNano()), binary.LittleEndian.Uint32(r[:]))
}

// newExtentPath returns the database-relative path for a new extent of
// the given table. Catalog databases keep extents under extents/;
// discovery scopes use extents/ when the subdirectory already exists
// (a demoted catalog database) and the bare directory otherwise.
func (e *Engine) newExtentPath(tableName, extentID string) string {
	name := fmt.Sprintf("%s_%s.vtx", tableName, extentID)
	if e.Catalog.IsDiscovery() {
		if fi, err := os.Stat(filepath.Join(e.Catalog.DatabasePath(), "extents")); err != nil || !fi.IsDir() {
			return name
		}
	}
	return filepath.ToSlash(filepath.Join("extents", name))
}

// persistDiscoverySchema writes a zero-row schema-bearing extent so a
// created-but-empty table survives process restart in discovery mode
// (there is no catalog.json to remember it). Discovery skips zero-row
// extents when building scan lists, so the file asserts existence and
// schema without ever being scanned.
func (e *Engine) persistDiscoverySchema(tableName string, schema types.Schema) error {
	if !e.Catalog.IsDiscovery() {
		return nil
	}
	id := e.newExtentID()
	relPath := e.newExtentPath(tableName, id)
	empty := types.NewTable(tableName, schema)
	if err := e.SaveExtent(relPath, empty); err != nil {
		return fmt.Errorf("persist schema extent: %w", err)
	}
	return nil
}
