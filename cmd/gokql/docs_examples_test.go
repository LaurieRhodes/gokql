package main

// docs_examples_test.go — every KQL example in the documentation runs.
//
// In README.md and docs/ (except docs/reference, which is generated,
// and docs/dev/history), each ```kql block runs, in file order, against
// one fresh database per file, through the same path as the CLI: a
// block that fails fails the test. A ```kql block followed directly by
// an ```output block must print exactly that output (the CLI's default
// table form). ```kql no-run blocks are not run (network, the user's
// own files); a ```kql error block must fail, and its output block
// holds the error ("Error: ..."). A file may begin with hidden setup:
//
//	<!-- gokql:setup
//	.set-or-append T <| datatable(...)[...]
//	-->
//
// whose statements (separated by blank lines) run first. A setup
// statement headed "@db NAME" or "@cluster NAME" runs in another
// database, which becomes one of this one's databases (or another
// cluster) in its gokql.yaml; "@config {json}" adds fields to that file. To write or
// refresh the output blocks after changing an example:
//
//	go test ./cmd/gokql -run TestDocsExamples -update-docs

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/LaurieRhodes/gokql/pkg/catalog"
	"github.com/LaurieRhodes/gokql/pkg/engine"
)

var updateDocs = flag.Bool("update-docs", false, "rewrite the output blocks of the docs' KQL examples")

type docBlock struct {
	query     string
	noRun     bool
	wantErr   bool
	outStart  int // line index of the output block's first content line; -1 if none
	outEnd    int // line index of its closing fence
	expected  string
	queryLine int
}

var fenceRe = regexp.MustCompile("^(`{3,})\\s*([A-Za-z0-9_-]*)\\s*(.*)$")

// fenceEnd returns the index of the line closing a fence of n backticks.
func fenceEnd(lines []string, from, n int) int {
	j := from
	for j < len(lines) {
		t := strings.TrimSpace(lines[j])
		if strings.HasPrefix(t, strings.Repeat("`", n)) && strings.Trim(t, "`") == "" {
			return j
		}
		j++
	}
	return j
}

func parseDocBlocks(lines []string) []docBlock {
	var out []docBlock
	for i := 0; i < len(lines); i++ {
		m := fenceRe.FindStringSubmatch(strings.TrimRight(lines[i], " "))
		if m == nil {
			continue
		}
		n := len(m[1])
		if m[2] != "kql" {
			i = fenceEnd(lines, i+1, n)
			continue
		}
		start := i + 1
		j := fenceEnd(lines, start, n)
		b := docBlock{query: strings.Join(lines[start:j], "\n"), noRun: strings.Contains(m[3], "no-run"), wantErr: strings.Contains(m[3], "error"), outStart: -1, queryLine: i + 1}
		i = j
		// an output block directly after (blank lines allowed)
		k := j + 1
		for k < len(lines) && strings.TrimSpace(lines[k]) == "" {
			k++
		}
		if k < len(lines) {
			if om := fenceRe.FindStringSubmatch(strings.TrimRight(lines[k], " ")); om != nil && om[2] == "output" {
				e := fenceEnd(lines, k+1, len(om[1]))
				b.outStart, b.outEnd = k+1, e
				b.expected = strings.Join(lines[k+1:e], "\n")
				i = e
			}
		}
		out = append(out, b)
	}
	return out
}

func normaliseOutput(s string) string {
	ls := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range ls {
		ls[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimRight(strings.Join(ls, "\n"), "\n")
}

var setupRe = regexp.MustCompile(`(?s)<!--\s*gokql:setup\s*\n(.*?)-->`)

func docFiles(t *testing.T, root string) []string {
	var files []string
	files = append(files, filepath.Join(root, "README.md"))
	_ = filepath.Walk(filepath.Join(root, "docs"), func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if info.IsDir() && (rel == filepath.Join("docs", "reference") || rel == filepath.Join("docs", "dev", "history")) {
			return filepath.SkipDir
		}
		if !info.IsDir() && strings.HasSuffix(p, ".md") {
			files = append(files, p)
		}
		return nil
	})
	return files
}

func TestDocsExamples(t *testing.T) {
	// examples name files relative to the repository root
	// (./testdata/docs/...), as a reader in a checkout would
	wd, _ := os.Getwd()
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	for _, path := range docFiles(t, root) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, path)
		t.Run(rel, func(t *testing.T) {
			dir := t.TempDir()
			// examples refer to files under ./ as written: run in the scope's parent
			// "@primary NAME" in the setup names the opened database
			primary := "scope"
			if m := regexp.MustCompile(`(?m)^@primary\s+(\S+)`).FindSubmatch(raw); m != nil {
				primary = string(m[1])
			}
			cat, err := catalog.Open(filepath.Join(dir, primary))
			if err != nil {
				t.Fatal(err)
			}
			eng := engine.New(cat)
			var buf bytes.Buffer
			st := &replState{eng: eng, mode: "table", output: &buf}
			run := func(q string) (string, error) {
				buf.Reset()
				err := st.runCommand(q)
				return buf.String(), err
			}
			if m := setupRe.FindSubmatch(raw); m != nil {
				cfg := map[string]interface{}{}
				var aliases, clusters []map[string]string
				for _, q := range strings.Split(string(m[1]), "\n\n") {
					q = strings.TrimSpace(q)
					if q == "" {
						continue
					}
					// @db NAME / @cluster NAME: run the statement in another
					// database, listed as an alias (or another cluster) of
					// this one; @config {json}: more gokql.yaml fields
					if strings.HasPrefix(q, "@primary ") {
						continue
					}
					if strings.HasPrefix(q, "@config ") {
						if err := json.Unmarshal([]byte(q[len("@config "):]), &cfg); err != nil {
							t.Fatalf("setup @config: %v", err)
						}
						continue
					}
					if strings.HasPrefix(q, "@db ") || strings.HasPrefix(q, "@cluster ") {
						head, stmt, _ := strings.Cut(q, "\n")
						kind, name, _ := strings.Cut(head, " ")
						name = strings.TrimSpace(name)
						p := filepath.Join(dir, name)
						known := false
						for _, a := range append(append([]map[string]string{}, aliases...), clusters...) {
							known = known || a["name"] == name
						}
						if !known {
							if kind == "@db" {
								aliases = append(aliases, map[string]string{"name": name, "path": p})
							} else {
								clusters = append(clusters, map[string]string{"name": name, "path": p})
							}
						}
						oc, err := catalog.Open(p)
						if err != nil {
							t.Fatal(err)
						}
						var ob bytes.Buffer
						ost := &replState{eng: engine.New(oc), mode: "table", output: &ob}
						if err := ost.runCommand(stmt); err != nil {
							t.Fatalf("setup %s %s: %v", kind, name, err)
						}
						continue
					}
					if _, err := run(q); err != nil {
						t.Fatalf("setup %q: %v", q, err)
					}
				}
				if len(aliases) > 0 {
					cfg["databases"] = aliases
				}
				if len(clusters) > 0 {
					cfg["clusters"] = clusters
				}
				if len(cfg) > 0 {
					b, _ := yaml.Marshal(cfg)
					if err := os.WriteFile(filepath.Join(dir, primary, "gokql.yaml"), b, 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			lines := strings.Split(string(raw), "\n")
			blocks := parseDocBlocks(lines)
			changed := false
			// rewrite from the end so line numbers stay valid
			type repl struct {
				start, end int
				text       string
			}
			var repls []repl
			for _, b := range blocks {
				if b.noRun || strings.TrimSpace(b.query) == "" {
					continue
				}
				got, err := run(b.query)
				if b.wantErr {
					if err == nil {
						t.Errorf("%s:%d: expected an error\n%s", rel, b.queryLine, b.query)
						continue
					}
					got, err = "Error: "+err.Error(), nil
				}
				if err != nil {
					t.Errorf("%s:%d: %v\n%s", rel, b.queryLine, err, b.query)
					continue
				}
				if b.outStart < 0 {
					continue
				}
				if normaliseOutput(got) != normaliseOutput(b.expected) {
					if *updateDocs {
						repls = append(repls, repl{b.outStart, b.outEnd, normaliseOutput(got)})
						changed = true
						continue
					}
					t.Errorf("%s:%d: output differs\n--- query\n%s\n--- want\n%s\n--- got\n%s", rel, b.queryLine, b.query, b.expected, normaliseOutput(got))
				}
			}
			if changed {
				for i := len(repls) - 1; i >= 0; i-- {
					r := repls[i]
					lines = append(lines[:r.start], append(strings.Split(r.text, "\n"), lines[r.end:]...)...)
				}
				if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// docs/reference/cli.md documents every flag main.go defines.
func TestCLIReferenceDocumentsEveryFlag(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "cli.md"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`flag\.(?:String|Bool|Int|Float64|Duration)\("([a-z0-9-]+)"`)
	n := 0
	for _, m := range re.FindAllSubmatch(src, -1) {
		n++
		f := string(m[1])
		if !regexp.MustCompile("`-" + regexp.QuoteMeta(f) + "[ `]").Match(doc) {
			t.Errorf("docs/reference/cli.md does not document -%s", f)
		}
	}
	if n < 10 {
		t.Fatalf("found only %d flags in main.go", n)
	}
}
