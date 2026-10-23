package parser

import (
	"fmt"
	"strings"
)

// ingestOptions lists the with(...) properties each ingest-from-query
// command accepts. Anything else is an error rather than silently
// ignored.
var ingestOptions = map[string]map[string]bool{
	"set":            {},
	"append":         {"extend_schema": true},
	"set-or-append":  {"extend_schema": true},
	"set-or-replace": {"extend_schema": true, "recreate_schema": true},
}

// parseIngestTarget splits "T [with (name=value, ...)]", the text before
// <| of .set / .append / .set-or-append / .set-or-replace. Property
// names are lower-cased; values have surrounding quotes removed.
func parseIngestTarget(cmd, s string) (string, map[string]string, error) {
	s = strings.TrimSpace(s)
	name, opts := s, map[string]string(nil)
	lower := strings.ToLower(s)
	if i := strings.Index(lower, " with"); i >= 0 {
		after := strings.TrimSpace(s[i+len(" with"):])
		if !strings.HasPrefix(after, "(") || !strings.HasSuffix(after, ")") {
			return "", nil, fmt.Errorf(".%s: expected with (name=value, ...) before <|", cmd)
		}
		name = strings.TrimSpace(s[:i])
		var err error
		if opts, err = parsePropertyList(after[1 : len(after)-1]); err != nil {
			return "", nil, fmt.Errorf(".%s: %w", cmd, err)
		}
		for k := range opts {
			if !ingestOptions[cmd][k] {
				var ok []string
				for o := range ingestOptions[cmd] {
					ok = append(ok, o)
				}
				return "", nil, fmt.Errorf(".%s: unsupported property %q (supported: %s)", cmd, k, strings.Join(ok, ", "))
			}
		}
	}
	if strings.HasPrefix(strings.ToLower(name), "database(") || strings.HasPrefix(strings.ToLower(name), "cluster(") {
		return "", nil, fmt.Errorf(".%s: %s is in another database; writes go to the current database only", cmd, name)
	}
	if name == "" || strings.ContainsAny(name, " \t()") && !strings.HasPrefix(name, "[") {
		return "", nil, fmt.Errorf(".%s: invalid table name %q", cmd, name)
	}
	return name, opts, nil
}

// parsePropertyList parses "a=1, b='x'" into lower-cased names and
// unquoted values.
func parsePropertyList(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range splitAndTrim(s, ',') {
		if kv == "" {
			continue
		}
		eq := strings.Index(kv, "=")
		if eq < 0 {
			return nil, fmt.Errorf("malformed property %q, expected name=value", kv)
		}
		v := strings.TrimSpace(kv[eq+1:])
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[strings.ToLower(strings.TrimSpace(kv[:eq]))] = v
	}
	return out, nil
}

// DeleteRecordsCmd: .delete table T records [with (whatif=true)] <| T |
// where P [| where Q ...] — removes the rows of T matching every
// predicate. With whatif, only counts them.
type DeleteRecordsCmd struct {
	TableName string
	Where     Expr
	WhatIf    bool
}

func (*DeleteRecordsCmd) statementNode() {}

func parseDeleteRecords(rest string) (Statement, error) {
	idx := strings.Index(rest, "<|")
	if idx < 0 {
		return nil, fmt.Errorf(".delete table: expected .delete table T records <| T | where ...")
	}
	head := strings.Fields(rest[:idx])
	if len(head) < 2 || !strings.EqualFold(head[1], "records") {
		return nil, fmt.Errorf(".delete table: expected .delete table T records <| T | where ...")
	}
	cmd := &DeleteRecordsCmd{TableName: head[0]}
	if len(head) > 2 {
		w := strings.TrimSpace(strings.Join(head[2:], " "))
		lw := strings.ToLower(w)
		if !strings.HasPrefix(lw, "with") {
			return nil, fmt.Errorf(".delete table: unexpected %q", w)
		}
		w = strings.TrimSpace(w[len("with"):])
		if !strings.HasPrefix(w, "(") || !strings.HasSuffix(w, ")") {
			return nil, fmt.Errorf(".delete table: expected with (whatif=true)")
		}
		opts, err := parsePropertyList(w[1 : len(w)-1])
		if err != nil {
			return nil, fmt.Errorf(".delete table: %w", err)
		}
		for k, v := range opts {
			if k != "whatif" {
				return nil, fmt.Errorf(".delete table: unsupported property %q (supported: whatif)", k)
			}
			cmd.WhatIf = strings.EqualFold(v, "true")
		}
	}
	st, err := parseQuery(strings.TrimSpace(rest[idx+2:]))
	if err != nil {
		return nil, fmt.Errorf(".delete table %s records: %w", cmd.TableName, err)
	}
	q, ok := st.(*Query)
	if !ok || q.Source != cmd.TableName || q.SourceFunc != nil || q.SourceDB != nil || q.SourceFuncCall != nil || len(q.Operators) == 0 {
		return nil, fmt.Errorf(".delete table %s records: the query must be %s | where <predicate>", cmd.TableName, cmd.TableName)
	}
	for _, op := range q.Operators {
		w, ok := op.(*WhereOp)
		if !ok {
			return nil, fmt.Errorf(".delete table %s records: only where operators may follow %s", cmd.TableName, cmd.TableName)
		}
		if cmd.Where == nil {
			cmd.Where = w.Predicate
		} else {
			cmd.Where = &BinaryExpr{Left: cmd.Where, Op: OpAnd, Right: w.Predicate}
		}
	}
	return cmd, nil
}

// ExecuteScriptCmd: .execute database script [with (...)] <| commands.
// Each command starts on a new line with a dot. ContinueOnErrors
// (default false) runs the rest after a failure; ThrowOnErrors makes a
// failure an error; Atomic (a gokql extension) evaluates every append
// of an all-append script before writing any of them.
type ExecuteScriptCmd struct {
	Commands         []string
	ContinueOnErrors bool
	ThrowOnErrors    bool
	Atomic           bool
}

func (*ExecuteScriptCmd) statementNode() {}

func parseExecuteScript(rest string) (Statement, error) {
	idx := strings.Index(rest, "<|")
	if idx < 0 {
		return nil, fmt.Errorf(".execute database script requires <| followed by the commands")
	}
	cmd := &ExecuteScriptCmd{}
	head := strings.TrimSpace(rest[:idx])
	if head != "" {
		if !strings.HasPrefix(strings.ToLower(head), "with") {
			return nil, fmt.Errorf(".execute database script: unexpected %q", head)
		}
		w := strings.TrimSpace(head[len("with"):])
		if !strings.HasPrefix(w, "(") || !strings.HasSuffix(w, ")") {
			return nil, fmt.Errorf(".execute database script: expected with (name=value, ...)")
		}
		opts, err := parsePropertyList(w[1 : len(w)-1])
		if err != nil {
			return nil, fmt.Errorf(".execute database script: %w", err)
		}
		for k, v := range opts {
			b := strings.EqualFold(v, "true")
			switch k {
			case "continueonerrors":
				cmd.ContinueOnErrors = b
			case "throwonerrors":
				cmd.ThrowOnErrors = b
			case "atomic":
				cmd.Atomic = b
			default:
				return nil, fmt.Errorf(".execute database script: unsupported property %q (supported: ContinueOnErrors, ThrowOnErrors, Atomic)", k)
			}
		}
	}
	var cur []string
	flush := func() {
		if t := strings.TrimSpace(strings.Join(cur, "\n")); t != "" {
			cmd.Commands = append(cmd.Commands, t)
		}
		cur = nil
	}
	for _, line := range strings.Split(rest[idx+2:], "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "//") {
			continue
		}
		if strings.HasPrefix(t, ".") {
			flush()
		}
		cur = append(cur, line)
	}
	flush()
	if len(cmd.Commands) == 0 {
		return nil, fmt.Errorf(".execute database script: no commands after <|")
	}
	for _, c := range cmd.Commands {
		if !strings.HasPrefix(c, ".") {
			return nil, fmt.Errorf(".execute database script: each command must start with a dot: %q", firstLine(c))
		}
	}
	return cmd, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ShowCalloutPolicyCmd: .show cluster policy callout — the callout
// policy (gokql.yaml "callouts" plus the predefined rules).
// Altering it from a query is refused: the policy is the operator's
// file, not something a query may widen.
type ShowCalloutPolicyCmd struct{}

func (*ShowCalloutPolicyCmd) statementNode() {}

// ShowDatabasesCmd: .show databases — the databases of this cluster
// (the local scope and its federation aliases).
type ShowDatabasesCmd struct{}

func (*ShowDatabasesCmd) statementNode() {}
