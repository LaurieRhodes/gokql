package parser

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
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

	// Trailing partitioned-by Col ( operators ).
	if idx := findKeyword(" "+rest, " partitioned-by "); idx >= 0 {
		part := strings.TrimSpace(rest[idx+len(" partitioned-by")-1:])
		rest = strings.TrimSpace(rest[:max(idx-1, 0)])
		open := strings.IndexByte(part, '(')
		if open < 0 {
			return nil, fmt.Errorf("make-graph: expected 'partitioned-by Column ( graph operator )'")
		}
		op.PartitionBy = unquoteIdent(strings.TrimSpace(part[:open]))
		closeIdx := matchingClose(part, open, '(', ')')
		if op.PartitionBy == "" || closeIdx < 0 || strings.TrimSpace(part[closeIdx+1:]) != "" {
			return nil, fmt.Errorf("make-graph: expected 'partitioned-by Column ( graph operator )'")
		}
		ops, err := parseSubPipeline("make-graph partitioned-by", part[open+1:closeIdx])
		if err != nil {
			return nil, err
		}
		op.PartitionOps = ops
	}

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
		lower := keywordFold(rest)
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
	return parseGraphPatternOp(s, "graph-match")
}

// parseGraphShortestPaths parses (the prefix already stripped):
//
//	[output=any|all] Pattern [where <expr>] project <items>
func parseGraphShortestPaths(s string) (*GraphMatchOp, error) {
	return parseGraphPatternOp(s, "graph-shortest-paths")
}

func parseGraphPatternOp(s, opName string) (*GraphMatchOp, error) {
	s = strings.TrimSpace(s)
	op := &GraphMatchOp{Cycles: "unique_edges"}
	if opName == "graph-shortest-paths" {
		op.Shortest = "any"
		op.Cycles = "" // the search default: no node revisited
	}

	// Leading operator properties: cycles= (graph-match) and output=
	// (graph-shortest-paths).
	for {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(s[:eq]))
		if key != "cycles" && key != "output" {
			break
		}
		r := strings.TrimSpace(s[eq+1:])
		e := strings.IndexAny(r, " \t\n(")
		if e < 0 {
			e = len(r)
		}
		v := strings.ToLower(r[:e])
		switch {
		case key == "cycles":
			if v != "none" && v != "all" && v != "unique_edges" {
				return nil, fmt.Errorf("graph-match: cycles must be none, all or unique_edges, got %q", r[:e])
			}
			op.Cycles = v
		case key == "output" && opName == "graph-shortest-paths":
			if v != "any" && v != "all" {
				return nil, fmt.Errorf("graph-shortest-paths: output must be any or all, got %q", r[:e])
			}
			op.Shortest = v
		default:
			return nil, fmt.Errorf("%s: unknown property %q", opName, key)
		}
		s = strings.TrimSpace(r[e:])
	}

	// The project clause is required and terminates the operator.
	projIdx := findKeyword(s, " project ")
	if projIdx < 0 {
		return nil, fmt.Errorf("%s: project clause is required", opName)
	}
	projStr := strings.TrimSpace(s[projIdx+len(" project "):])
	head := strings.TrimSpace(s[:projIdx])

	if whereIdx := findKeyword(head, " where "); whereIdx >= 0 {
		whereStr := strings.TrimSpace(head[whereIdx+len(" where "):])
		expr, err := ParseExpr(whereStr)
		if err != nil {
			return nil, fmt.Errorf("%s where: %w", opName, err)
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
		return nil, fmt.Errorf("%s project: %w", opName, err)
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
	minText, maxText := strings.TrimSpace(rangeStr[:dotIdx]), strings.TrimSpace(rangeStr[dotIdx+2:])
	minVal, err1 := strconv.Atoi(minText)
	maxVal, err2 := strconv.Atoi(maxText)
	if err1 != nil || err2 != nil {
		// Scalar-expression bounds (let names, function parameters):
		// checked when the pattern is planned.
		if minText == "" || maxText == "" {
			return fmt.Errorf("graph-match: expected '*min..max' hop range, got %q", inner)
		}
		if err1 != nil {
			e, err := ParseExpr(minText)
			if err != nil {
				return fmt.Errorf("graph-match: hop range %q: %w", inner, err)
			}
			edge.MinExpr = e
		} else {
			edge.MinExpr = &Literal{Value: int64(minVal), Type: types.TypeLong}
		}
		if err2 != nil {
			e, err := ParseExpr(maxText)
			if err != nil {
				return fmt.Errorf("graph-match: hop range %q: %w", inner, err)
			}
			edge.MaxExpr = e
		} else {
			edge.MaxExpr = &Literal{Value: int64(maxVal), Type: types.TypeLong}
		}
		edge.MinHops, edge.MaxHops = 1, 1
		return nil
	}
	return CheckHopRange(minVal, maxVal, inner, edge)
}

// CheckHopRange validates and sets a hop range.
func CheckHopRange(minVal, maxVal int, text string, edge *GraphMatchEdge) error {
	if minVal < 0 || maxVal < minVal || maxVal < 1 {
		return fmt.Errorf("graph-match: invalid hop range %q (expected *min..max with 0 <= min <= max, max >= 1)", text)
	}
	if maxVal > MaxGraphHops {
		return fmt.Errorf("graph-match: hop range max %d exceeds limit %d", maxVal, MaxGraphHops)
	}
	edge.MinHops = minVal
	edge.MaxHops = maxVal
	return nil
}

// parseGraphToTable parses the operands of graph-to-table (prefix
// stripped). A bare graph-to-table means edges (a lenient extension:
// ADX requires the output to be named).
func parseGraphToTable(s string) (*GraphToTableOp, error) {
	op := &GraphToTableOp{}
	if strings.TrimSpace(s) == "" {
		op.Edges = true
		return op, nil
	}
	parts := splitTopLevelCommas(s)
	if len(parts) > 2 {
		return nil, fmt.Errorf("graph-to-table: expected at most two outputs (nodes, edges)")
	}
	for _, part := range parts {
		// Normalise "opt = value" to "opt=value" before tokenising.
		fields := strings.Fields(strings.ReplaceAll(strings.ReplaceAll(part, " =", "="), "= ", "="))
		if len(fields) == 0 {
			return nil, fmt.Errorf("graph-to-table: expected 'nodes' or 'edges'")
		}
		kind := strings.ToLower(fields[0])
		var alias *string
		switch kind {
		case "nodes":
			if op.Nodes {
				return nil, fmt.Errorf("graph-to-table: nodes given twice")
			}
			op.Nodes, alias = true, &op.NodesAs
		case "edges":
			if op.Edges {
				return nil, fmt.Errorf("graph-to-table: edges given twice")
			}
			op.Edges, alias = true, &op.EdgesAs
		default:
			return nil, fmt.Errorf("graph-to-table: expected 'nodes' or 'edges', got %q", fields[0])
		}
		for i := 1; i < len(fields); i++ {
			f := fields[i]
			lf := strings.ToLower(f)
			if lf == "as" {
				if i+1 >= len(fields) {
					return nil, fmt.Errorf("graph-to-table: expected a name after 'as'")
				}
				*alias = unquoteIdent(fields[i+1])
				i++
				continue
			}
			eq := strings.IndexByte(f, '=')
			if eq < 0 {
				return nil, fmt.Errorf("graph-to-table: unexpected %q", f)
			}
			name := unquoteIdent(f[eq+1:])
			if name == "" {
				return nil, fmt.Errorf("graph-to-table: %s needs a column name", f[:eq])
			}
			switch opt := strings.ToLower(f[:eq]); {
			case opt == "with_node_id" && kind == "nodes":
				op.NodeIdColumn = name
			case opt == "with_source_id" && kind == "edges":
				op.SourceIdColumn = name
			case opt == "with_target_id" && kind == "edges":
				op.TargetIdColumn = name
			default:
				return nil, fmt.Errorf("graph-to-table %s: unknown option %q", kind, f[:eq])
			}
		}
	}
	if op.Nodes && op.Edges && (op.NodesAs == "" || op.EdgesAs == "") {
		return nil, fmt.Errorf("graph-to-table: multiple graph outputs require aliases (nodes as N, edges as E)")
	}
	return op, nil
}

// parseGraphMarkComponents parses [kind=weak|strong] [with_component_id=Name]
// (the prefix already stripped).
func parseGraphMarkComponents(s string) (*GraphMarkComponentsOp, error) {
	op := &GraphMarkComponentsOp{Kind: "weak", ComponentId: "ComponentId"}
	fields := strings.Fields(strings.ReplaceAll(strings.ReplaceAll(s, " =", "="), "= ", "="))
	for _, f := range fields {
		eq := strings.IndexByte(f, '=')
		if eq < 0 {
			return nil, fmt.Errorf("graph-mark-components: unexpected %q", f)
		}
		key, val := strings.ToLower(f[:eq]), unquoteIdent(f[eq+1:])
		switch key {
		case "kind":
			v := strings.ToLower(val)
			if v != "weak" && v != "strong" {
				return nil, fmt.Errorf("graph-mark-components: kind must be weak or strong, got %q", val)
			}
			op.Kind = v
		case "with_component_id":
			if val == "" {
				return nil, fmt.Errorf("graph-mark-components: with_component_id needs a name")
			}
			op.ComponentId = val
		default:
			return nil, fmt.Errorf("graph-mark-components: unknown property %q", f[:eq])
		}
	}
	return op, nil
}
