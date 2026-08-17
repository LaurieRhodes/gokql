// Package uaparser implements the ua-parser/uap-core specification
// (https://github.com/ua-parser/uap-core/blob/master/docs/specification.md)
// against the real, unmodified regexes.yaml pattern database from that
// project (embedded in this package, see NOTICE.md and
// LICENSE-APACHE-2.0.txt for attribution).
//
// Added 2026-08-18: real ADX's own parse_user_agent() docs state its
// implementation is "built on regex checks of the input string against
// a huge number of predefined patterns" -- this package is exactly
// that, using the actual pattern database rather than a hand-rolled
// approximation. Every one of the 1270 regex patterns in regexes.yaml
// was confirmed to compile successfully under Go's RE2-based regexp
// engine (zero backreferences, lookaheads, or other PCRE-only
// constructs found) before this package was written.
package uaparser

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed regexes.yaml
var regexesYAML []byte

// rawEntry mirrors one YAML list item across all three parser
// categories combined -- not every field applies to every category,
// but decoding into one shared struct is simpler than three separate
// ones and yaml.v3 ignores absent keys per document.
type rawEntry struct {
	Regex             string `yaml:"regex"`
	RegexFlag         string `yaml:"regex_flag"`
	FamilyReplacement string `yaml:"family_replacement"`
	V1Replacement     string `yaml:"v1_replacement"`
	V2Replacement     string `yaml:"v2_replacement"`
	V3Replacement     string `yaml:"v3_replacement"`
	OSReplacement     string `yaml:"os_replacement"`
	OSV1Replacement   string `yaml:"os_v1_replacement"`
	OSV2Replacement   string `yaml:"os_v2_replacement"`
	OSV3Replacement   string `yaml:"os_v3_replacement"`
	OSV4Replacement   string `yaml:"os_v4_replacement"`
	DeviceReplacement string `yaml:"device_replacement"`
	BrandReplacement  string `yaml:"brand_replacement"`
	ModelReplacement  string `yaml:"model_replacement"`
}

type rawDoc struct {
	UserAgentParsers []rawEntry `yaml:"user_agent_parsers"`
	OSParsers        []rawEntry `yaml:"os_parsers"`
	DeviceParsers    []rawEntry `yaml:"device_parsers"`
}

// compiledEntry holds one already-compiled regex plus its (still
// template-form, e.g. "Firefox ($1)") replacement strings.
type compiledEntry struct {
	re                                             *regexp.Regexp
	family, v1, v2, v3, v4, deviceFam, brand, model string
}

var (
	loadOnce sync.Once
	loadErr  error
	uaRules  []compiledEntry
	osRules  []compiledEntry
	devRules []compiledEntry
)

func compilePattern(pattern, flag string) (*regexp.Regexp, error) {
	if flag == "i" {
		pattern = "(?i)" + pattern
	}
	return regexp.Compile(pattern)
}

func load() {
	var doc rawDoc
	if err := yaml.Unmarshal(regexesYAML, &doc); err != nil {
		loadErr = fmt.Errorf("uaparser: failed to parse embedded regexes.yaml: %w", err)
		return
	}
	for _, e := range doc.UserAgentParsers {
		re, err := compilePattern(e.Regex, e.RegexFlag)
		if err != nil {
			loadErr = fmt.Errorf("uaparser: user_agent_parsers regex %q: %w", e.Regex, err)
			return
		}
		uaRules = append(uaRules, compiledEntry{re: re, family: e.FamilyReplacement, v1: e.V1Replacement, v2: e.V2Replacement, v3: e.V3Replacement})
	}
	for _, e := range doc.OSParsers {
		re, err := compilePattern(e.Regex, e.RegexFlag)
		if err != nil {
			loadErr = fmt.Errorf("uaparser: os_parsers regex %q: %w", e.Regex, err)
			return
		}
		osRules = append(osRules, compiledEntry{re: re, family: e.OSReplacement, v1: e.OSV1Replacement, v2: e.OSV2Replacement, v3: e.OSV3Replacement, v4: e.OSV4Replacement})
	}
	for _, e := range doc.DeviceParsers {
		re, err := compilePattern(e.Regex, e.RegexFlag)
		if err != nil {
			loadErr = fmt.Errorf("uaparser: device_parsers regex %q: %w", e.Regex, err)
			return
		}
		devRules = append(devRules, compiledEntry{re: re, deviceFam: e.DeviceReplacement, brand: e.BrandReplacement, model: e.ModelReplacement})
	}
}

// ensureLoaded compiles the embedded pattern database exactly once,
// however many times Parse* is called -- 1270 regexes is too many to
// recompile per row.
func ensureLoaded() error {
	loadOnce.Do(load)
	return loadErr
}

// expand substitutes $1..$9 in a replacement template with the
// corresponding regex capture groups from m (m[0] is the whole match,
// m[N] is group N per Go's regexp convention, matching ua-parser's own
// $1..$9 convention exactly). A referenced group beyond len(m)-1, or
// one that didn't participate in the match, expands to "" -- the
// common, permissive convention used across other ua-parser language
// ports rather than an error.
func expand(template string, m []string) string {
	if template == "" {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(template); i++ {
		if template[i] == '$' && i+1 < len(template) && template[i+1] >= '1' && template[i+1] <= '9' {
			idx := int(template[i+1] - '0')
			if idx < len(m) {
				b.WriteString(m[idx])
			}
			i++
			continue
		}
		b.WriteByte(template[i])
	}
	return b.String()
}

// group returns m[idx] if present, else "".
func group(m []string, idx int) string {
	if idx < len(m) {
		return m[idx]
	}
	return ""
}

// Browser holds a parsed browser/user-agent-family result.
type Browser struct {
	Family, MajorVersion, MinorVersion, Patch string
}

// OS holds a parsed operating-system result.
type OS struct {
	Family, MajorVersion, MinorVersion, Patch, PatchMinor string
}

// Device holds a parsed device result.
type Device struct {
	Family, Brand, Model string
}

// ParseBrowser implements the user_agent_parsers algorithm from
// uap-core's own specification.md: first-match-wins linear scan,
// case-sensitive unless regex_flag:'i' is set on that entry,
// replacement templates (family_replacement etc, using $1..$4) take
// precedence over the raw matched groups, and an unmatched string
// falls back to Family="Other" with the version fields left empty.
func ParseBrowser(ua string) (Browser, error) {
	if err := ensureLoaded(); err != nil {
		return Browser{}, err
	}
	for _, rule := range uaRules {
		m := rule.re.FindStringSubmatch(ua)
		if m == nil {
			continue
		}
		b := Browser{}
		if rule.family != "" {
			b.Family = expand(rule.family, m)
		} else {
			b.Family = group(m, 1)
		}
		if rule.v1 != "" {
			b.MajorVersion = expand(rule.v1, m)
		} else {
			b.MajorVersion = group(m, 2)
		}
		if rule.v2 != "" {
			b.MinorVersion = expand(rule.v2, m)
		} else {
			b.MinorVersion = group(m, 3)
		}
		if rule.v3 != "" {
			b.Patch = expand(rule.v3, m)
		} else {
			b.Patch = group(m, 4)
		}
		return b, nil
	}
	return Browser{Family: "Other"}, nil
}

// ParseOS implements the os_parsers algorithm from uap-core's own
// specification.md -- same shape as ParseBrowser but with a fifth
// PatchMinor field (os_v4_replacement / capture group 5).
func ParseOS(ua string) (OS, error) {
	if err := ensureLoaded(); err != nil {
		return OS{}, err
	}
	for _, rule := range osRules {
		m := rule.re.FindStringSubmatch(ua)
		if m == nil {
			continue
		}
		o := OS{}
		if rule.family != "" {
			o.Family = expand(rule.family, m)
		} else {
			o.Family = group(m, 1)
		}
		if rule.v1 != "" {
			o.MajorVersion = expand(rule.v1, m)
		} else {
			o.MajorVersion = group(m, 2)
		}
		if rule.v2 != "" {
			o.MinorVersion = expand(rule.v2, m)
		} else {
			o.MinorVersion = group(m, 3)
		}
		if rule.v3 != "" {
			o.Patch = expand(rule.v3, m)
		} else {
			o.Patch = group(m, 4)
		}
		if rule.v4 != "" {
			o.PatchMinor = expand(rule.v4, m)
		} else {
			o.PatchMinor = group(m, 5)
		}
		return o, nil
	}
	return OS{Family: "Other"}, nil
}

// ParseDevice implements the device_parsers algorithm from uap-core's
// own specification.md. Per that spec: "In case that no replacement
// for a match is given, the first match defines the family and the
// model" -- family and model each independently default to capture
// group 1 when their own explicit *_replacement key is absent; brand
// has no such default and is only ever set via an explicit
// brand_replacement. Per the spec's own final line, leading/trailing
// whitespace is trimmed from every field of the result.
func ParseDevice(ua string) (Device, error) {
	if err := ensureLoaded(); err != nil {
		return Device{}, err
	}
	for _, rule := range devRules {
		m := rule.re.FindStringSubmatch(ua)
		if m == nil {
			continue
		}
		d := Device{}
		if rule.deviceFam != "" {
			d.Family = expand(rule.deviceFam, m)
		} else {
			d.Family = group(m, 1)
		}
		if rule.brand != "" {
			d.Brand = expand(rule.brand, m)
		}
		if rule.model != "" {
			d.Model = expand(rule.model, m)
		} else {
			d.Model = group(m, 1)
		}
		d.Family = strings.TrimSpace(d.Family)
		d.Brand = strings.TrimSpace(d.Brand)
		d.Model = strings.TrimSpace(d.Model)
		return d, nil
	}
	return Device{Family: "Other"}, nil
}
