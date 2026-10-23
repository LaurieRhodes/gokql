package parser

// macro_expand.go — macro-expand over entity groups of databases, as a
// text rewrite before parsing: the subquery is instantiated once per
// entity, with Entity.T replaced by the entity's qualified table
// (database('d').T, cluster('c').database('d').T) and
// Entity.$current_database / Entity.$current_cluster_endpoint by the
// entity's names, and the instances are combined with union (kind and
// isfuzzy carried over). Entity groups come inline
// (entity_group [ ... ]) or from let G = entity_group [ ... ].

import (
	"fmt"
	"regexp"
	"strings"
)

var letEntityGroup = regexp.MustCompile(`(?is)^\s*let\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*entity_group\s*\[`)

var aliasStatement = regexp.MustCompile(`(?is)^\s*alias\s+database\s+(\[\s*(?:'[^']*'|"[^"]*")\s*\]|[A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.+?)\s*$`)

// expandAliases removes alias database statements and rewrites
// database("Alias") in the later statements to the aliased reference
// (cluster("c").database("d") or database("d")).
func expandAliases(stmts []string) ([]string, error) {
	type alias struct{ name, target string }
	var aliases []alias
	var out []string
	for _, s := range stmts {
		if m := aliasStatement.FindStringSubmatch(s); m != nil {
			name := m[1]
			if strings.HasPrefix(name, "[") {
				name = strings.TrimSpace(name[1 : len(name)-1])
				name = name[1 : len(name)-1]
			}
			target := strings.TrimSpace(m[2])
			if _, d, rest, ok := SplitQualifiedName(target + ".x"); !ok || d == "" || rest != "x" {
				return nil, fmt.Errorf("alias database %s: expected cluster('c').database('d') or database('d'), got %s", name, target)
			}
			aliases = append(aliases, alias{name, target})
			continue
		}
		for _, a := range aliases {
			for _, q := range []string{"'", `"`} {
				for _, f := range []string{"database(", "database ("} {
					s = strings.ReplaceAll(s, f+q+a.name+q+")", a.target)
				}
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// expandMacros rewrites the macro-expand statements of a query.
func expandMacros(stmts []string) ([]string, error) {
	hasMacro := false
	for _, s := range stmts {
		if strings.Contains(strings.ToLower(s), "macro-expand") {
			hasMacro = true
			break
		}
	}
	if !hasMacro {
		return stmts, nil
	}
	groups := map[string][]string{}
	var out []string
	for _, s := range stmts {
		if m := letEntityGroup.FindStringSubmatchIndex(s); m != nil {
			name := s[m[2]:m[3]]
			open := m[1] - 1
			end := findMatchingBracket(s, open)
			if end < 0 {
				return nil, fmt.Errorf("entity_group: unmatched '['")
			}
			refs, err := entityRefs(s[open+1 : end])
			if err != nil {
				return nil, err
			}
			groups[name] = refs
			continue
		}
		x, err := expandMacroStatement(s, groups)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}

func entityRefs(s string) ([]string, error) {
	var refs []string
	seen := map[string]bool{}
	for _, r := range splitRespectingParens(s, ',') {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		c, d, rest, ok := SplitQualifiedName(r + ".x")
		if !ok || rest != "x" || d == "" {
			return nil, fmt.Errorf("entity_group: %q is not a database reference (database('d') or cluster('c').database('d'))", r)
		}
		key := strings.ToLower(c + "\x00" + d)
		if seen[key] {
			return nil, fmt.Errorf("entity_group: %s is listed twice", r)
		}
		seen[key] = true
		refs = append(refs, r)
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("entity_group: no entities")
	}
	return refs, nil
}

var macroHead = regexp.MustCompile(`(?is)macro-expand\b`)

// expandMacroStatement rewrites each macro-expand source of s.
func expandMacroStatement(s string, groups map[string][]string) (string, error) {
	for {
		loc := macroHead.FindStringIndex(s)
		if loc == nil || inString(s, loc[0]) {
			return s, nil
		}
		rest := s[loc[1]:]
		kind, fuzzy := "outer", "false"
		for {
			t := strings.TrimLeft(rest, " \t\r\n")
			lt := strings.ToLower(t)
			switch {
			case strings.HasPrefix(lt, "kind"):
				v, r := propValue(t[len("kind"):])
				kind, rest = strings.ToLower(v), r
				continue
			case strings.HasPrefix(lt, "isfuzzy"):
				v, r := propValue(t[len("isfuzzy"):])
				fuzzy, rest = strings.ToLower(v), r
				continue
			}
			rest = t
			break
		}
		var refs []string
		var ident string
		lr := strings.ToLower(rest)
		if strings.HasPrefix(lr, "entity_group") {
			b := strings.Index(rest, "[")
			end := findMatchingBracket(rest, b)
			if b < 0 || end < 0 {
				return "", fmt.Errorf("macro-expand: expected entity_group [ ... ]")
			}
			var err error
			if refs, err = entityRefs(rest[b+1 : end]); err != nil {
				return "", err
			}
			rest = strings.TrimSpace(rest[end+1:])
			if !strings.HasPrefix(strings.ToLower(rest), "as ") {
				return "", fmt.Errorf("macro-expand: expected 'as Identifier' after the entity group")
			}
			f := strings.Fields(rest[3:])
			if len(f) == 0 {
				return "", fmt.Errorf("macro-expand: expected an identifier after 'as'")
			}
			ident = strings.TrimSuffix(f[0], "(")
			rest = strings.TrimSpace(rest[3:])[len(ident):]
		} else {
			// X in G | G as X
			f := strings.Fields(rest)
			if len(f) < 3 {
				return "", fmt.Errorf("macro-expand: expected entity_group [...] as X, X in Group or Group as X")
			}
			var group string
			switch strings.ToLower(f[1]) {
			case "in":
				ident, group = f[0], strings.TrimSuffix(f[2], "(")
			case "as":
				group, ident = f[0], strings.TrimSuffix(f[2], "(")
			default:
				return "", fmt.Errorf("macro-expand: expected entity_group [...] as X, X in Group or Group as X")
			}
			g, ok := groups[group]
			if !ok {
				return "", fmt.Errorf("macro-expand: unknown entity group %q (stored entity groups are not supported; use let %s = entity_group [...])", group, group)
			}
			refs = g
			i := strings.Index(rest, group) + len(group)
			if strings.ToLower(f[1]) == "as" {
				i = strings.Index(rest[i:], ident) + i + len(ident)
			}
			rest = rest[i:]
		}
		rest = strings.TrimLeft(rest, " \t\r\n")
		if !strings.HasPrefix(rest, "(") {
			return "", fmt.Errorf("macro-expand: expected ( subquery )")
		}
		end := findMatchingParen(rest, 0)
		if end < 0 {
			return "", fmt.Errorf("macro-expand: unmatched '('")
		}
		sub := rest[1:end]
		var legs []string
		for _, r := range refs {
			legs = append(legs, "("+instantiate(sub, ident, r)+")")
		}
		union := fmt.Sprintf("union kind=%s isfuzzy=%s %s", kind, fuzzy, strings.Join(legs, ", "))
		s = s[:loc[0]] + union + rest[end+1:]
	}
}

// propValue reads "= value" from s.
func propValue(s string) (string, string) {
	s = strings.TrimLeft(s, " \t")
	s = strings.TrimLeft(strings.TrimPrefix(s, "="), " \t")
	i := strings.IndexAny(s, " \t\r\n")
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i:]
}

// instantiate replaces ident.Name in sub (outside string literals)
// with ref.Name, and ident.$current_database /
// ident.$current_cluster_endpoint with the entity's names.
func instantiate(sub, ident, ref string) string {
	cluster, db, _, _ := SplitQualifiedName(ref + ".x")
	var b strings.Builder
	n := len(ident)
	for i := 0; i < len(sub); {
		c := sub[i]
		if c == '\'' || c == '"' {
			j := skipString(sub, i)
			b.WriteString(sub[i:j])
			i = j
			continue
		}
		if strings.HasPrefix(sub[i:], ident+".") && (i == 0 || !isIdentChar(sub[i-1])) {
			after := sub[i+n+1:]
			switch {
			case strings.HasPrefix(after, "$current_database"):
				b.WriteString(fmt.Sprintf("%q", db))
				i += n + 1 + len("$current_database")
				continue
			case strings.HasPrefix(after, "$current_cluster_endpoint"):
				if cluster != "" {
					b.WriteString(fmt.Sprintf("%q", cluster))
				} else {
					b.WriteString("current_cluster_endpoint()")
				}
				i += n + 1 + len("$current_cluster_endpoint")
				continue
			}
			b.WriteString(ref + ".")
			i += n + 1
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// skipString returns the index just past the string literal at i.
func skipString(s string, i int) int {
	q := s[i]
	verbatim := i > 0 && s[i-1] == '@'
	for j := i + 1; j < len(s); j++ {
		if s[j] == 0x5c && !verbatim {
			j++
			continue
		}
		if s[j] == q {
			return j + 1
		}
	}
	return len(s)
}

// inString reports whether position p of s lies inside a string literal.
func inString(s string, p int) bool {
	for i := 0; i < p; {
		if s[i] == 0x27 || s[i] == '"' {
			j := skipString(s, i)
			if p < j {
				return true
			}
			i = j
			continue
		}
		i++
	}
	return false
}
