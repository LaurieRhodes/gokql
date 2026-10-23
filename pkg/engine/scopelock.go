package engine

// scopelock.go — a per-scope reader/writer lock that makes .compact
// safe while other processes use the scope.
//
// .compact writes the merged extent, then renames the old ones to
// .superseded. Between those two steps every row is in the scope
// twice: a reader listing the files then double-counted it, and a
// writer extending a dictionary during a _Dictionaries compaction
// computed its next codes from a doubled table. So every top-level
// statement holds a SHARED flock on <scope>/_scope.lock for its
// duration, and .compact holds it EXCLUSIVELY: compaction waits for
// statements in flight, and statements wait for the compaction (a few
// seconds even for a large scope). flock releases when a process
// exits, so a crashed process cannot wedge a scope. Binaries from
// before this file do not take the lock and are not protected.

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/parser"
)

const scopeLockName = "_scope.lock"

// scopeLockWarnAfter is how long a statement waits before saying why.
const scopeLockWarnAfter = 2 * time.Second

// lockScope takes the scope lock for one top-level statement: exclusive
// for compaction, shared otherwise. It returns the release function
// (a no-op when the scope cannot be locked, e.g. a read-only scope
// directory without a lock file, or an in-memory catalog).
func (e *Engine) lockScope(stmt parser.Statement) func() {
	if e.Catalog == nil || !e.Catalog.IsDiscovery() {
		return func() {}
	}
	path := filepath.Join(e.Catalog.DatabasePath(), scopeLockName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		if f, err = os.Open(path); err != nil {
			return func() {}
		}
	}
	how, what := syscall.LOCK_SH, "shared"
	if needsExclusiveScope(stmt) {
		how, what = syscall.LOCK_EX, "exclusive"
	}
	if syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB) != nil {
		done := make(chan struct{})
		go func() {
			select {
			case <-done:
			case <-time.After(scopeLockWarnAfter):
				if how == syscall.LOCK_EX {
					fmt.Fprintln(os.Stderr, "gokql: waiting for statements in progress to finish before compacting")
				} else {
					fmt.Fprintln(os.Stderr, "gokql: waiting for a compaction of this scope to finish")
				}
			}
		}()
		err := syscall.Flock(int(f.Fd()), how)
		close(done)
		if err != nil {
			f.Close()
			if e.Verbose {
				fmt.Fprintf(os.Stderr, "[scope] %s lock unavailable: %v\n", what, err)
			}
			return func() {}
		}
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}

// needsExclusiveScope reports whether a statement compacts or
// otherwise supersedes extents (.set-or-replace, .delete records).
func needsExclusiveScope(stmt parser.Statement) bool {
	switch s := stmt.(type) {
	case *parser.CompactTableCmd, *parser.CompactDatabaseCmd, *parser.MergeExtentsCmd, *parser.DeleteRecordsCmd:
		return true
	case *parser.SetOrAppendCmd:
		return s.Mode == "set-or-replace"
	case *parser.ExecuteScriptCmd:
		for _, c := range s.Commands {
			if st, err := parser.Parse(c); err == nil && needsExclusiveScope(st) {
				return true
			}
		}
	case *parser.CompoundStatement:
		if needsExclusiveScope(s.Final) {
			return true
		}
		for _, b := range s.Batch {
			if needsExclusiveScope(b) {
				return true
			}
		}
	}
	return false
}
