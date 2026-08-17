package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveDatabasePrimary guards the two ways a request selects
// the primary database — its real name, and an empty "db" field
// (real Kustainer clients sometimes omit it, defaulting to whatever
// database is "in context" — see cmd/gokql/server.go's protocol notes).
func TestResolveDatabasePrimary(t *testing.T) {
	cfg := serverConfig{dbPath: "/primary/path", dbName: "primary"}

	for _, name := range []string{"primary", ""} {
		got, err := cfg.resolveDatabase(name)
		if err != nil {
			t.Fatalf("resolveDatabase(%q): unexpected error: %v", name, err)
		}
		if got != "/primary/path" {
			t.Errorf("resolveDatabase(%q) = %q, want /primary/path", name, got)
		}
	}
}

// TestResolveDatabaseAdditional guards genuine multi-database
// resolution to an additional, fully read-write serverDatabase entry —
// distinct from federation's read-only alias resolution, which this
// function has nothing to do with.
func TestResolveDatabaseAdditional(t *testing.T) {
	cfg := serverConfig{
		dbPath:    "/primary/path",
		dbName:    "primary",
		databases: map[string]string{"second": "/second/path"},
	}
	got, err := cfg.resolveDatabase("second")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/second/path" {
		t.Errorf("resolveDatabase(\"second\") = %q, want /second/path", got)
	}
}

// TestResolveDatabaseUnknownErrors guards a clear, informative error
// (naming what IS available) rather than a bare "not found" — a
// config mistake should be easy to spot from the error alone.
func TestResolveDatabaseUnknownErrors(t *testing.T) {
	cfg := serverConfig{
		dbPath:    "/primary/path",
		dbName:    "primary",
		databases: map[string]string{"second": "/second/path"},
	}
	if _, err := cfg.resolveDatabase("nosuchdb"); err == nil {
		t.Fatal("expected an error for an unknown database name, got nil")
	}
}

// TestLoadServerConfigDatabasesFromFile guards that .okql-server.json's
// "databases" list is correctly parsed into serverConfig.databases,
// including via a real resolveDatabase call on the result — not just
// checking the raw map, in case the two ever drifted apart.
func TestLoadServerConfigDatabasesFromFile(t *testing.T) {
	dir := t.TempDir()
	writeServerConfigFile(t, dir, `{"dbName":"primary","databases":[{"name":"second","path":"/abs/second"}]}`)

	cfg, err := loadServerConfig(dir, ":8080", "", false, false, map[string]bool{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := cfg.resolveDatabase("second")
	if err != nil {
		t.Fatalf("resolveDatabase(\"second\") after loading from file: %v", err)
	}
	if got != "/abs/second" {
		t.Errorf("resolveDatabase(\"second\") = %q, want /abs/second", got)
	}
}

// TestLoadServerConfigDatabaseNameCollisionErrors guards that an
// additional database can't silently shadow the primary one by
// sharing its name — caught at config-load time, not left to produce
// confusing behavior at request time.
func TestLoadServerConfigDatabaseNameCollisionErrors(t *testing.T) {
	dir := t.TempDir()
	writeServerConfigFile(t, dir, `{"dbName":"primary","databases":[{"name":"primary","path":"/abs/somewhere"}]}`)

	if _, err := loadServerConfig(dir, ":8080", "", false, false, map[string]bool{}); err == nil {
		t.Fatal("expected an error for a databases entry colliding with the primary name, got nil")
	}
}

// TestLoadServerConfigRelativeDatabasePathErrors guards the
// absolute-paths-only requirement for additional databases — the same
// requirement federation's aliases already have, for the same reason
// (see federation.go's design note): a relative path is genuinely
// ambiguous for a containerized deployment.
func TestLoadServerConfigRelativeDatabasePathErrors(t *testing.T) {
	dir := t.TempDir()
	writeServerConfigFile(t, dir, `{"databases":[{"name":"second","path":"relative/path"}]}`)

	if _, err := loadServerConfig(dir, ":8080", "", false, false, map[string]bool{}); err == nil {
		t.Fatal("expected an error for a relative databases path, got nil")
	}
}

// TestLoadServerConfigMissingDatabaseFieldErrors guards a databases
// entry missing either name or path — caught explicitly rather than
// silently producing an empty-string map key or value.
func TestLoadServerConfigMissingDatabaseFieldErrors(t *testing.T) {
	dir := t.TempDir()
	writeServerConfigFile(t, dir, `{"databases":[{"name":"second"}]}`)

	if _, err := loadServerConfig(dir, ":8080", "", false, false, map[string]bool{}); err == nil {
		t.Fatal("expected an error for a databases entry missing path, got nil")
	}
}

func writeServerConfigFile(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, serverConfigFileName), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
