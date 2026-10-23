package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if f, err := Load(dir); err != nil || f.Path != "" {
		t.Fatalf("empty dir: %+v %v", f, err)
	}
	// the template parses (all commented out)
	if _, err := Parse([]byte(Template)); err != nil {
		t.Fatalf("template: %v", err)
	}
	// uncommented template parses with every field
	var lines []string
	for _, l := range strings.Split(Template, "\n") {
		if strings.HasPrefix(l, "# ") && strings.Contains(l, ":") && !strings.Contains(l, "gokql.yaml") && !strings.Contains(l, "Reference") &&
			!strings.HasPrefix(l, "# This") && !strings.HasPrefix(l, "# What") && !strings.HasPrefix(l, "# Refuse") &&
			!strings.HasPrefix(l, "# Other") && !strings.HasPrefix(l, "# The ") && !strings.HasPrefix(l, "# the ") &&
			!strings.HasPrefix(l, "# may") && !strings.HasPrefix(l, "# gokql") && !strings.HasPrefix(l, "# https") {
			lines = append(lines, l[2:])
		}
	}
	f, err := Parse([]byte(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatalf("uncommented template: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if f.Cluster != "lab" || !f.Server.ReadOnly || len(f.Server.Databases) != 1 || !f.Server.Databases[0].ReadOnly ||
		len(f.Callouts) != 1 || f.Callouts[0].CalloutUriRegex != `^https://prices\.azure\.com/` || f.Server.TLS.Key == "" {
		t.Fatalf("uncommented template: %+v", f)
	}
	// unknown fields are errors
	if _, err := Parse([]byte("clustr: x\n")); err == nil {
		t.Fatal("typo accepted")
	}
	// relative paths are refused
	os.WriteFile(filepath.Join(dir, FileName), []byte("databases:\n  - name: a\n    path: rel\n"), 0o644)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative path: %v", err)
	}
}

func TestLegacy(t *testing.T) {
	dir := t.TempDir()
	var warnings []string
	Warn = func(s string) { warnings = append(warnings, s) }
	os.WriteFile(filepath.Join(dir, legacyFederation), []byte(`{"cluster":"odm","aliases":[{"alias":"m","path":"/x/m"}],"callouts":[{"CalloutType":"webapi","CalloutUriRegex":".*","CanCall":true}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, legacyServer), []byte(`{"addr":":9","dbName":"n","databases":[{"name":"d","path":"/x/d"}]}`), 0o644)
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Legacy || f.Cluster != "odm" || f.Databases[0].Name != "m" || f.Server.Addr != ":9" || f.Server.Name != "n" || len(f.Callouts) != 1 {
		t.Fatalf("%+v", f)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "-write-config") {
		t.Fatalf("warnings %q", warnings)
	}
	b, err := f.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	g, err := Parse(b)
	if err != nil || g.Cluster != "odm" || g.Server.Databases[0].Path != "/x/d" {
		t.Fatalf("round trip: %v %+v\n%s", err, g, b)
	}
}

// docs/reference/configuration.md shows the template verbatim.
func TestDocsShowTemplate(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "configuration.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "```yaml\n"+Template+"```") {
		t.Fatal("docs/reference/configuration.md does not contain config.Template: update its Template section")
	}
}
