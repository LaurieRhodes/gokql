package catalog

// freshness.go — keeping a long-lived Catalog in step with other
// writers to the same directory.
//
// A Catalog is built from the directory once, at Open. Another process
// (or another Engine in this one) committing extents afterwards is
// invisible until the catalog is rebuilt. Refresh rebuilds it when the
// directory changed, so the catalog is what a fresh Open would produce,
// and costs two stats when nothing changed. Footers of files already
// read are reused: extent files are immutable once committed.
//
// Change detection uses directory stamps (inode, size, mtime): every
// commit (write-to-temp + rename), supersession and removal changes the
// directory's mtime. A stamp observed within racyWindow of its mtime is
// treated as unsettled and re-checked next time, so a second change
// inside one timestamp tick is not missed (git's "racily clean" rule).

import (
	"os"
	"syscall"
	"time"
)

const racyWindow = 2 * time.Second

// fileStamp identifies one version of a file or directory.
type fileStamp struct {
	ok    bool
	ino   uint64
	size  int64
	mtime int64 // UnixNano
}

func statStamp(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	st := fileStamp{ok: true, size: fi.Size(), mtime: fi.ModTime().UnixNano()}
	if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
		st.ino = uint64(sys.Ino)
	}
	return st
}

// settled reports whether a stamp observed at `at` can be trusted to
// change if the directory changes.
func (s fileStamp) settled(at time.Time) bool {
	return s.ok && at.UnixNano()-s.mtime >= int64(racyWindow)
}

// Refresh rebuilds the catalog if the database directory changed since
// it was last read. In-memory catalogs are unaffected.
func (c *Catalog) Refresh() error {
	if !c.discovery {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rediscoverLocked(false)
}
