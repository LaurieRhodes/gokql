package parser

import (
	"fmt"
	"strconv"
	"strings"
)

// graph_parse.go — make-graph and graph-match operator syntax.

// parseMakeGraph parses (the "make-graph " prefix already stripped):
//
//	Source --> Target [with_node_id=Name] [with T1 on Id1 [, T2 on Id2 ...]]
//
// with_node_id may also come after the with clause.
func parseMakeGraph(s string) (*MakeGraphOp, error) {
	s = strings.TrimSpace(s)

	arrowIdx := strings.Index(s, "-->")
	if arrowIdx < 0 {
		return nil, fmt.Errorf("make-graph: expected 'Source --> Target'")
	}
	op := &MakeGraphOp{SourceColumn: unquoteIdent(strings.TrimSpace(s[:arrowIdx]))}
	if op.SourceColumn == "" {
		return nil, fmt.Errorf("make-graph: missing source column before -->")
	}
	rest := strings.TrimSpace(s[arrowIdx+len("-->"):])

	// Target column: the first whitespace-delimited token.
	end := strings.IndexAny(rest, " \t\n")
	if end < 0 {
		end = len(rest)
	}
	op.TargetColumn = unquoteIdent(rest[:end])
	if op.TargetColumn == "" {
		return nil, fmt.Errorf("make-graph: missing target column after -->")
	}
	rest = strings.TrimSpace(rest[end:])

	for rest != "" {
		lower := strings.ToLower(rest)
		switch {
		case strings.HasPrefix(lower, "with_node_id"):
			r := strings.TrimSpace(rest[len("with_node_id"):])
			if !strings.HasPrefix(r, "=") {
				return nil, fmt.Errorf("make-graph: expected with_node_id=Name")
			}
			r = strings.TrimSpace(r[1:])
			e := strings.IndexAny(r, " \t\n")
			if e < 0 {
				e = len(r)
			}
			op.WithNodeId = unquoteIdent(r[:e])
			if op.WithNodeId == "" {
				return nil, fmt.Errorf("make-graph: expected with_node_id=Name")
			}
			rest = strings.TrimSpace(r[e:])
		case strings.HasPrefix(lower, "with ") || strings.HasPrefix(lower, "with\t"):
			if op.NodeTables != nil {
				return nil, fmt.Errorf("make-graph: duplicate with clause")
			}
			body := strings.TrimSpace(rest[len("with "):])
			// The with clause runs to a trailing with_node_id, if any.
			tail := ""
			if idx := findKeyword(body, " with_node_id"); idx >= 0 {
				tail = body[idx:]
				body = body[:idx]
			}
			for _, part := range splitTopLevelCommas(body) {
				part = strings.TrimSpace(part)
				onIdx := findKeyword(part, " on ")
				if onIdx < 0 {
					return nil, fmt.Errorf("make-graph: expected 'with NodeTable on NodeIdColumn'")
				}
				nt := GraphNodeTable{
					Table:    strings.TrimSpace(part[:onIdx]),
					IdColumn: unquoteIdent(strings.TrimSpace(part[onIdx+len(" on "):])),
				}
				if nt.Table == "" || nt.IdColumn == "" {
					return nil, fmt.Errorf("make-graph: expected 'with NodeTable on NodeIdColumn'")
				}
				op.NodeTables = append(op.NodeTables, nt)
			}
			rest = strings.TrimSpace(tail)
		default:
			return nil, fmt.Errorf("make-graph: unexpected %q (expected with_node_id= or with)", rest)
		}
	}
	return op, nil
}

// splitTopLevelCommas splits s on commas outside brackets and quotes.
func splitTopLevelCommas(s string) []string {
	var parts []string
	depth := 0
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote && !precededByOddBackslashes(s, i) {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

// parseGraphMatch parses (the "graph-match " prefix already stripped):
//
//	[cycles=X] Pattern [, Pattern ...] [where <expr>] project <items>
func parseGraphMatch(s string) (*GraphMatchOp, error) {
	s = strings.TrimSpace(s)
	op := &GraphMatchOp{Cycles: "unique_edges"}

	// Leading operator properties.
	for {
		lower := strings.ToLower(s)
		if !strings.HasPrefix(lower, "cycles") {
			break
		}
		r := strings.TrimSpace(s[len("cycles"):])
		if !strings.HasPrefix(r, "=") {
			break
		}
		r = strings.TrimSpace(r[1:])
		e := strings.IndexAny(r, " \t\n(")
		if e < 0 {
			e = len(r)
		}
		v := strings.ToLower(r[:e])
		switch v {
		case "none", "all", "unique_edges":
			op.Cycles = v
		default:
			return nil, fmt.Errorf("graph-match: cycles must be none, all or unique_edges, got %q", r[:e])
		}
		s = strings.TrimSpace(r[e:])
	}

	// The project clause is required and terminates the operator.
	projIdx := findKeyword(s, " project ")
	if projIdx < 0 {
		return nil, fmt.Errorf("graph-match: project clause is required")
	}
	projStr := strings.TrimSpace(s[projIdx+len(" project "):])
	head := strings.TrimSpace(s[:projIdx])

	if whereIdx := findKeyword(head, " where "); whereIdx >= 0 {
		whereStr := strings.TrimSpace(head[whereIdx+len(" where "):])
		expr, err := ParseExpr(whereStr)
		if err != nil {
			return nil, fmt.Errorf("graph-match where: %w", err)
		}
		op.Where = expr
		head = strings.TrimSpace(head[:whereIdx])
	}

	for _, part := range splitTopLevelCommas(head) {
		seq, err := parseGraphPattern(part)
		if err != nil {
			return nil, err
		}
		op.Sequences = append(op.Sequences, seq)
	}

	items, err := parseProjectItems(projStr)
	if err != nil {
		return nil, fmt.Errorf("graph-match project: %w", err)
	}
	op.Project = &ProjectOp{Items: items}
	return op, nil
}

// parseGraphPattern parses one path sequence: (a)-[e]->(b)<-[f*1..3]-(c).
// Nodes are parenthesized variable names ("()" = anonymous). Edges:
//
//	-[e]->  <-[e]-  -[e]-       named, forward / backward / either
//	-->     <--     --          anonymous
//	-[e*min..max]->             variable length (also -[*min..max]->)
func parseGraphPattern(s string) (GraphMatchSequence, error) {
	var seq GraphMatchSequence
	s = strings.TrimSpace(s)
	if s == "" {
		return seq, fmt.Errorf("graph-match: missing pattern")
	}

	i := 0
	expectNode := true
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
			i++
		}
		if i >= len(s) {
			break
		}

		if expectNode {
			if s[i] != '(' {
				return seq, fmt.Errorf("graph-match: expected '(' in pattern at %q", s[i:])
			}
			closeIdx := strings.IndexByte(s[i:], ')')
			if closeIdx < 0 {
				return seq, fmt.Errorf("graph-match: unmatched '(' in pattern")
			}
			name := strings.TrimSpace(s[i+1 : i+closeIdx])
			if strings.ContainsAny(name, ": \t") {
				return seq, fmt.Errorf("graph-match: unsupported node element (%s)", name)
			}
			seq.Nodes = append(seq.Nodes, name)
			i += closeIdx + 1
			expectNode = false
			continue
		}

		edge := GraphMatchEdge{MinHops: 1, MaxHops: 1, Direction: EdgeForward}
		rest := s[i:]
		var inner string
		switch {
		case strings.HasPrefix(rest, "<-["):
			closeRel := strings.IndexByte(rest, ']')
			if closeRel < 0 {
				return seq, fmt.Errorf("graph-match: unmatched '[' in pattern")
			}
			inner = rest[3:closeRel]
			after := rest[closeRel+1:]
			if !strings.HasPrefix(after, "-") || strings.HasPrefix(after, "->") {
				return seq, fmt.Errorf("graph-match: expected '-' closing a backward edge at %q", after)
			}
			edge.Direction = EdgeBackward
			i += closeRel + 2
		case strings.HasPrefix(rest, "<--"):
			edge.Direction = EdgeBackward
			i += 3
		case strings.HasPrefix(rest, "-["):
			closeRel := strings.IndexByte(rest, ']')
			if closeRel < 0 {
				return seq, fmt.Errorf("graph-match: unmatched '[' in pattern")
			}
			inner = rest[2:closeRel]
			after := rest[closeRel+1:]
			switch {
			case strings.HasPrefix(after, "->"):
				i += closeRel + 3
			case strings.HasPrefix(after, "-"):
				edge.Direction = EdgeAny
				i += closeRel + 2
			default:
				return seq, fmt.Errorf("graph-match: expected '->' or '-' after edge at %q", after)
			}
		case strings.HasPrefix(rest, "-->"):
			i += 3
		case strings.HasPrefix(rest, "--"):
			edge.Direction = EdgeAny
			i += 2
		default:
			return seq, fmt.Errorf("graph-match: expected an edge ('-[', '<-[', '-->', '<--' or '--') at %q", rest)
		}

		if err := parseGraphEdgeInner(strings.TrimSpace(inner), &edge); err != nil {
			return seq, err
		}
		seq.Edges = append(seq.Edges, edge)
		expectNode = true
	}

	if expectNode {
		return seq, fmt.Errorf("graph-match: pattern must end with a node")
	}
	return seq, nil
}

// parseGraphEdgeInner parses the bracketed part of an edge element:
// [name][*min..max [distinct]].
func parseGraphEdgeInner(inner string, edge *GraphMatchEdge) error {
	starIdx := strings.IndexByte(inner, '*')
	if starIdx < 0 {
		if strings.ContainsAny(inner, ": \t") {
			return fmt.Errorf("graph-match: unsupported edge element [%s]", inner)
		}
		edge.Name = inner
		return nil
	}
	edge.Name = strings.TrimSpace(inner[:starIdx])
	edge.VarLength = true
	rangeStr := strings.TrimSpace(inner[starIdx+1:])

	// Optional trailing " distinct": BFS reachable-node semantics.
	rangeFields := strings.Fields(rangeStr)
	if len(rangeFields) == 2 && strings.EqualFold(rangeFields[1], "distinct") {
		edge.Distinct = true
		rangeStr = rangeFields[0]
	} else if len(rangeFields) > 1 {
		return fmt.Errorf("graph-match: unexpected trailing text in hop range %q (only 'distinct' is recognized)", inner)
	}

	dotIdx := strings.Index(rangeStr, "..")
	if dotIdx < 0 {
		return fmt.Errorf("graph-match: expected '*min..max' hop range, got %q", inner)
	}
	minVal, err1 := strconv.Atoi(strings.TrimSpace(rangeStr[:dotIdx]))
	maxVal, err2 := strconv.Atoi(strings.TrimSpace(rangeStr[dotIdx+2:]))
	if err1 != nil || err2 != nil || minVal < 0 || maxVal < minVal || maxVal < 1 {
		return fmt.Errorf("graph-match: invalid hop range %q (expected *min..max with 0 <= min <= max, max >= 1)", inner)
	}
	if maxVal > MaxGraphHops {
		return fmt.Errorf("graph-match: hop range max %d exceeds limit %d", maxVal, MaxGraphHops)
	}
	edge.MinHops = minVal
	edge.MaxHops = maxVal
	return nil
}
