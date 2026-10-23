package main

// script.go — splitting piped input and .read files into statements.
//
// A statement may span lines. A line continues the statement before it
// when that statement has an open bracket or ``` block, ends with "|",
// "<|", "," or ";" (let statements and batches), or when the line
// itself starts with "|", "[", "(", ",", ")", "]" or "}". A blank line,
// or any other line, ends it. A statement that starts with #crp lines
// (GQL, openCypher) runs to the next blank line.
// Whole-line // comments are dropped.

import "strings"

func scriptStatements(text string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		t := strings.TrimSpace(line)
		// #crp request properties (a GQL or openCypher query) hold the
		// statement open until a blank line
		crp := strings.HasPrefix(strings.TrimSpace(cur.String()), "#crp")
		open := cur.Len() > 0 && (crp && t != "" || statementOpen(cur.String()))
		if strings.HasPrefix(t, "//") && !open {
			continue
		}
		if t == "" {
			if !open {
				flush()
			} else {
				cur.WriteString("\n")
			}
			continue
		}
		if cur.Len() > 0 && (open || strings.IndexByte("|[(,)]}", t[0]) >= 0) {
			cur.WriteString("\n")
			cur.WriteString(line)
			continue
		}
		flush()
		cur.WriteString(line)
	}
	flush()
	return out
}

// statementOpen reports whether s cannot end here: an unclosed bracket,
// string or ``` block, or a trailing |, <|, , or ;.
func statementOpen(s string) bool {
	depth := 0
	inFence := false
	for i := 0; i < len(s); i++ {
		if strings.HasPrefix(s[i:], "```") {
			inFence = !inFence
			i += 2
			continue
		}
		if inFence {
			continue
		}
		switch c := s[i]; c {
		case '"', '\'':
			j := i + 1
			for j < len(s) && s[j] != c {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(s) {
				return true
			}
			i = j
		case '/':
			if i+1 < len(s) && s[i+1] == '/' {
				for i < len(s) && s[i] != '\n' {
					i++
				}
			}
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		}
	}
	if depth > 0 || inFence {
		return true
	}
	t := strings.TrimSpace(s)
	return strings.HasSuffix(t, "|") || strings.HasSuffix(t, ",") || strings.HasSuffix(t, ";")
}
