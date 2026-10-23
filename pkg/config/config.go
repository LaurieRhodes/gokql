// Package config reads gokql.yaml, the optional configuration file in a
// database directory: the database's place in a local cluster, its
// callout policy, whether it is read-only, and the settings of
// `gokql -serve`. Every field is optional and a missing file is an
// empty configuration.
//
// Databases configured before gokql.yaml existed used two JSON files,
// .okql-federation.json and .okql-server.json. They are still read when
// gokql.yaml is absent, with a warning; `gokql -db DIR -write-config`
// converts them.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// FileName is the configuration file inside a database directory.
const FileName = "gokql.yaml"

const (
	legacyFederation = ".okql-federation.json"
	legacyServer     = ".okql-server.json"
)

// Database is another database: of this cluster (Databases), another
// cluster's primary database (Clusters), or one more database served
// by -serve (Server.Databases).
type Database struct {
	Name     string `yaml:"name"`
	Path     string `yaml:"path"`
	ReadOnly bool   `yaml:"readonly,omitempty"`
}

// Callout is one callout policy rule, with ADX's field names.
type Callout struct {
	CalloutType     string `yaml:"CalloutType" json:"CalloutType"`
	CalloutUriRegex string `yaml:"CalloutUriRegex" json:"CalloutUriRegex"`
	CanCall         bool   `yaml:"CanCall" json:"CanCall"`
}

// TLS names a certificate and key (PEM files) for -serve.
type TLS struct {
	Cert string `yaml:"cert,omitempty"`
	Key  string `yaml:"key,omitempty"`
}

// Server holds the settings of gokql -serve.
type Server struct {
	Addr      string     `yaml:"addr,omitempty"`
	Name      string     `yaml:"name,omitempty"`
	ReadOnly  bool       `yaml:"readonly,omitempty"`
	TLS       TLS        `yaml:"tls,omitempty"`
	Databases []Database `yaml:"databases,omitempty"`
}

// File is gokql.yaml.
type File struct {
	Cluster   string     `yaml:"cluster,omitempty"`
	Principal string     `yaml:"principal,omitempty"`
	ReadOnly  bool       `yaml:"readonly,omitempty"`
	Databases []Database `yaml:"databases,omitempty"`
	Clusters  []Database `yaml:"clusters,omitempty"`
	Callouts  []Callout  `yaml:"callouts,omitempty"`
	Server    Server     `yaml:"server,omitempty"`

	// Path is the file this was read from ("" when there was none).
	Path string `yaml:"-"`
	// Legacy is true when it came from the old JSON files.
	Legacy bool `yaml:"-"`
}

// Warn receives warnings (legacy files in use). It writes to stderr
// once per directory unless replaced.
var Warn = func(msg string) { fmt.Fprintln(os.Stderr, "[gokql] "+msg) }

var warned sync.Map

func warnOnce(dir, msg string) {
	if _, done := warned.LoadOrStore(dir+"\x00"+msg, true); !done {
		Warn(msg)
	}
}

// Load reads the configuration of the database in dir.
func Load(dir string) (*File, error) {
	p := filepath.Join(dir, FileName)
	data, err := os.ReadFile(p)
	if err == nil {
		f, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		f.Path = p
		for _, l := range []string{legacyFederation, legacyServer} {
			if _, err := os.Stat(filepath.Join(dir, l)); err == nil {
				warnOnce(dir, fmt.Sprintf("%s: %s is ignored because %s exists; delete it", dir, l, FileName))
			}
		}
		return f, f.validate(p)
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	f, err := loadLegacy(dir)
	if err != nil {
		return nil, err
	}
	return f, f.validate(f.Path)
}

// Parse reads gokql.yaml text; unknown fields are errors, so a typo
// does not silently do nothing.
func Parse(data []byte) (*File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && err.Error() != "EOF" {
		return nil, err
	}
	return &f, nil
}

func (f *File) validate(src string) error {
	check := func(section string, dbs []Database) error {
		seen := map[string]bool{}
		for _, d := range dbs {
			if d.Name == "" || d.Path == "" {
				return fmt.Errorf("%s: %s: each entry needs name and path (got name=%q path=%q)", src, section, d.Name, d.Path)
			}
			if !filepath.IsAbs(d.Path) {
				return fmt.Errorf("%s: %s: %q: path %q must be absolute", src, section, d.Name, d.Path)
			}
			if seen[strings.ToLower(d.Name)] {
				return fmt.Errorf("%s: %s: %q is listed twice", src, section, d.Name)
			}
			seen[strings.ToLower(d.Name)] = true
		}
		return nil
	}
	if err := check("databases", f.Databases); err != nil {
		return err
	}
	if err := check("clusters", f.Clusters); err != nil {
		return err
	}
	return check("server.databases", f.Server.Databases)
}

func loadLegacy(dir string) (*File, error) {
	f := &File{}
	var used []string
	if data, err := os.ReadFile(filepath.Join(dir, legacyFederation)); err == nil {
		var j struct {
			Aliases   []struct{ Alias, Path string } `json:"aliases"`
			Clusters  []struct{ Alias, Path string } `json:"clusters"`
			Cluster   string                         `json:"cluster"`
			Principal string                         `json:"principal"`
			Callouts  []Callout                      `json:"callouts"`
		}
		if err := json.Unmarshal(data, &j); err != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, legacyFederation), err)
		}
		for _, a := range j.Aliases {
			f.Databases = append(f.Databases, Database{Name: a.Alias, Path: a.Path})
		}
		for _, a := range j.Clusters {
			f.Clusters = append(f.Clusters, Database{Name: a.Alias, Path: a.Path})
		}
		f.Cluster, f.Principal, f.Callouts = j.Cluster, j.Principal, j.Callouts
		used = append(used, legacyFederation)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if data, err := os.ReadFile(filepath.Join(dir, legacyServer)); err == nil {
		var j struct {
			Addr      string                        `json:"addr"`
			DBName    string                        `json:"dbName"`
			TLSCert   string                        `json:"tlsCert"`
			TLSKey    string                        `json:"tlsKey"`
			Databases []struct{ Name, Path string } `json:"databases"`
		}
		if err := json.Unmarshal(data, &j); err != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, legacyServer), err)
		}
		f.Server = Server{Addr: j.Addr, Name: j.DBName, TLS: TLS{Cert: j.TLSCert, Key: j.TLSKey}}
		for _, d := range j.Databases {
			f.Server.Databases = append(f.Server.Databases, Database{Name: d.Name, Path: d.Path})
		}
		used = append(used, legacyServer)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if len(used) > 0 {
		f.Legacy = true
		f.Path = filepath.Join(dir, used[0])
		warnOnce(dir, fmt.Sprintf("%s: reading %s; run gokql -db %s -write-config to convert to %s",
			dir, strings.Join(used, " and "), dir, FileName))
	}
	return f, nil
}

// Marshal renders f as gokql.yaml, with a header comment.
func (f *File) Marshal() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("# gokql.yaml: configuration of this database. Every field is optional.\n")
	b.WriteString("# Reference: https://github.com/LaurieRhodes/gokql/blob/main/docs/reference/configuration.md\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Template is a commented gokql.yaml showing every field.
const Template = `# gokql.yaml: configuration of this database. Every field is optional;
# delete what you do not need. Reference:
# https://github.com/LaurieRhodes/gokql/blob/main/docs/reference/configuration.md

# This cluster's name, as cluster("...") and current_cluster_endpoint() see it.
# cluster: lab

# What current_principal() reports.
# principal: analyst

# Refuse every write command, from the CLI and from -serve alike.
# readonly: false

# Other databases of this cluster, read-only from here: database("stars").T
# databases:
#   - name: stars
#     path: /data/scopes/stars

# Other clusters, by their primary database: cluster("archive").database("...").T
# clusters:
#   - name: archive
#     path: /data/archive/primary

# The callout policy, in ADX's form: which URLs http_request (webapi),
# the ai_* plugins (azure_openai) and externaldata over http (external_data)
# may reach. A matching rule with CanCall: false always wins.
# callouts:
#   - CalloutType: webapi
#     CalloutUriRegex: ^https://prices\.azure\.com/
#     CanCall: true

# gokql -serve settings. Flags given on the command line take precedence.
# server:
#   addr: 127.0.0.1:8080
#   name: notes            # the database name clients send (default: the directory's name)
#   readonly: true         # refuse write commands from clients
#   tls:
#     cert: /etc/gokql/cert.pem
#     key: /etc/gokql/key.pem
#   databases:             # more databases served from this endpoint
#     - name: handbook
#       path: /data/handbook
#       readonly: true
`
