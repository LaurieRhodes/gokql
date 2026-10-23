package parser

// pattern.go — the pattern statement:
//
//	declare pattern country = (name:string)[state:string]
//	{
//	  ("USA").["New York"] = { print Capital = "Albany" };
//	  ("Canada").["Alberta"] = { print Capital = "Edmonton" };
//	};
//	country("Canada").Alberta
//
// A declaration maps argument values (and an optional path value) to
// a tabular body. Each invocation name(args)[.path | .['path'] |
// .["*"]] in the statements after it is replaced, before they are
// parsed, by its body in parentheses — or, for the "*" path, by the
// union of every body with those arguments.

import (
	"fmt"
	"strings"
)

type patternEntry struct {
	args []string // argument values, as strings
	path string   // "" when the pattern has no path parameter
	body string
}

type patternDecl struct {
	name    string
	hasPath bool
	entries []patternEntry
}

// isPatternDecl reports whether a statement is a declare pattern.
func isPatternDecl(st string) bool {
	return strings.HasPrefix(keywordFold(strings.TrimSpace(st)), "declare pattern ")
}

// parsePatternDecl parses one declare pattern statement.
func parsePatternDecl(st string) (*patternDecl, error) {
	s := strings.TrimSpace(strings.TrimSpace(st)[len("declare pattern "):])
	eq := strings.IndexByte(s, '=')
	if eq < 0 {
		return nil, fmt.Errorf("declare pattern %s: the pattern has no definition here", strings.TrimSpace(s))
	}
	d := &patternDecl{name: strings.TrimSpace(s[:eq])}
	if !isValidIdentifier(d.name) {
		return nil, fmt.Errorf("declare pattern: invalid name %q", d.name)
	}
	s = strings.TrimSpace(s[eq+1:])
	// (params)
	if !strings.HasPrefix(s, "(") {
		return nil, fmt.Errorf("declare pattern %s: expected (parameters)", d.name)
	}
	c := matchingClose(s, 0, '(', ')')
	if c < 0 {
		return nil, fmt.Errorf("declare pattern %s: unmatched '('", d.name)
	}
	s = strings.TrimSpace(s[c+1:])
	// [path:type]
	if strings.HasPrefix(s, "[") {
		c := matchingClose(s, 0, '[', ']')
		if c < 0 {
			return nil, fmt.Errorf("declare pattern %s: unmatched '['", d.name)
		}
		d.hasPath = true
		s = strings.TrimSpace(s[c+1:])
	}
	if !strings.HasPrefix(s, "{") || matchingClose(s, 0, '{', '}') != len(s)-1 {
		return nil, fmt.Errorf("declare pattern %s: expected { definitions }", d.name)
	}
	for _, def := range splitStatements(s[1 : len(s)-1]) {
		def = strings.TrimSpace(def)
		if def == "" {
			continue
		}
		e, err := parsePatternEntry(def, d.hasPath)
		if err != nil {
			return nil, fmt.Errorf("declare pattern %s: %w", d.name, err)
		}
		d.entries = append(d.entries, e)
	}
	return d, nil
}

// parsePatternEntry parses ("a", "b").["p"] = { body }.
func parsePatternEntry(def string, hasPath bool) (patternEntry, error) {
	var e patternEntry
	if !strings.HasPrefix(def, "(") {
		return e, fmt.Errorf("expected (values) in %q", def)
	}
	c := matchingClose(def, 0, '(', ')')
	if c < 0 {
		return e, fmt.Errorf("unmatched '(' in %q", def)
	}
	args, err := patternValues(def[1:c])
	if err != nil {
		return e, err
	}
	e.args = args
	rest := strings.TrimSpace(def[c+1:])
	if hasPath {
		p, n, err := patternPath(rest)
		if err != nil {
			return e, err
		}
		e.path, rest = p, strings.TrimSpace(rest[n:])
	}
	if !strings.HasPrefix(rest, "=") {
		return e, fmt.Errorf("expected '=' in %q", def)
	}
	rest = strings.TrimSpace(rest[1:])
	if !strings.HasPrefix(rest, "{") || matchingClose(rest, 0, '{', '}') != len(rest)-1 {
		return e, fmt.Errorf("expected { body } in %q", def)
	}
	e.body = strings.TrimSpace(rest[1 : len(rest)-1])
	return e, nil
}

// patternValues reads comma-separated literal values as strings.
func patternValues(s string) ([]string, error) {
	var out []string
	for _, a := range splitRespectingParens(s, ',') {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		x, err := ParseExpr(a)
		if err != nil {
			return nil, err
		}
		lit, ok := x.(*Literal)
		if !ok {
			return nil, fmt.Errorf("pattern values must be literals, got %q", a)
		}
		out = append(out, fmt.Sprint(lit.Value))
	}
	return out, nil
}

// patternPath reads .name, .['name'] or .["name"] at the start of s,
// returning the name and the bytes consumed.
func patternPath(s string) (string, int, error) {
	if !strings.HasPrefix(s, ".") {
		return "", 0, fmt.Errorf("expected .path in %q", s)
	}
	t := s[1:]
	if strings.HasPrefix(t, "[") {
		c := matchingClose(t, 0, '[', ']')
		if c < 0 {
			return "", 0, fmt.Errorf("unmatched '[' in %q", s)
		}
		v, err := patternValues(t[1:c])
		if err != nil || len(v) != 1 {
			return "", 0, fmt.Errorf("bad path in %q", s)
		}
		return v[0], 1 + c + 1, nil
	}
	n := 0
	for n < len(t) && isIdentChar(t[n]) {
		n++
	}
	if n == 0 {
		return "", 0, fmt.Errorf("expected .path in %q", s)
	}
	return t[:n], 1 + n, nil
}

// expandPatterns replaces invocations of the declared patterns in s.
func expandPatterns(s string, decls map[string]*patternDecl) (string, error) {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			b.WriteByte(ch)
			if ch == '\\' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			} else if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			b.WriteByte(ch)
			continue
		}
		if isIdentStart(ch) && (i == 0 || !isIdentChar(s[i-1])) {
			j := i
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			d := decls[s[i:j]]
			k := j
			for k < len(s) && s[k] == ' ' {
				k++
			}
			if d != nil && k < len(s) && s[k] == '(' {
				c := matchingClose(s, k, '(', ')')
				if c < 0 {
					return "", fmt.Errorf("pattern %s: unmatched '('", d.name)
				}
				args, err := patternValues(s[k+1 : c])
				if err != nil {
					return "", fmt.Errorf("pattern %s: %w", d.name, err)
				}
				end, path := c+1, ""
				if d.hasPath {
					p, n, err := patternPath(s[end:])
					if err != nil {
						return "", fmt.Errorf("pattern %s: %w", d.name, err)
					}
					path, end = p, end+n
				}
				repl, err := d.resolve(args, path)
				if err != nil {
					return "", err
				}
				b.WriteString(repl)
				i = end - 1
				continue
			}
			b.WriteString(s[i:j])
			i = j - 1
			continue
		}
		b.WriteByte(ch)
	}
	return b.String(), nil
}

// resolve is the replacement text for one invocation.
func (d *patternDecl) resolve(args []string, path string) (string, error) {
	var bodies []string
	for _, e := range d.entries {
		if len(e.args) != len(args) {
			continue
		}
		same := true
		for i := range args {
			if e.args[i] != args[i] {
				same = false
				break
			}
		}
		if same && (!d.hasPath || path == "*" || e.path == path) {
			bodies = append(bodies, "("+e.body+")")
		}
	}
	switch {
	case len(bodies) == 0:
		return "", fmt.Errorf("pattern %s(%s)%s: no matching definition", d.name, strings.Join(args, ", "), pathSuffix(path))
	case len(bodies) == 1 && path != "*":
		return bodies[0], nil
	}
	return "(union " + strings.Join(bodies, ", ") + ")", nil
}

func pathSuffix(p string) string {
	if p == "" {
		return ""
	}
	return ".[" + p + "]"
}
