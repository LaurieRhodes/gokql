package parser

import (
	"fmt"
	"strings"
)

// graph_model_parse.go — persistent graph model and snapshot commands.

// CreateOrAlterGraphModelCmd: .create-or-alter graph_model Name Payload
// (the JSON payload may be fenced with ```).
type CreateOrAlterGraphModelCmd struct {
	Name    string
	Payload string
}

// DropGraphModelCmd: .drop graph_model Name — the model, all its
// versions and all its snapshots.
type DropGraphModelCmd struct{ Name string }

// ShowGraphModelsCmd: .show graph_model Name [details] [with (id=...)]
// and .show graph_models [details]. Name "" lists every model.
// VersionId "" is the latest version, "*" every version.
type ShowGraphModelsCmd struct {
	Name      string
	Details   bool
	VersionId string
}

// MakeGraphSnapshotCmd: .make [async] graph_snapshot Name from Model.
type MakeGraphSnapshotCmd struct {
	Name, Model string
	Async       bool
}

// ShowGraphSnapshotsCmd: .show graph_snapshots Model|* and
// .show graph_snapshot Model.Name [details].
type ShowGraphSnapshotsCmd struct {
	Model    string // "*" = every model
	Snapshot string // set for the single-snapshot form
	Details  bool
}

// DropGraphSnapshotCmd: .drop graph_snapshot Model.Name, or
// .drop graph_snapshots Model (Snapshot "").
type DropGraphSnapshotCmd struct {
	Model, Snapshot string
}

func (*CreateOrAlterGraphModelCmd) statementNode() {}
func (*DropGraphModelCmd) statementNode()          {}
func (*ShowGraphModelsCmd) statementNode()         {}
func (*MakeGraphSnapshotCmd) statementNode()       {}
func (*ShowGraphSnapshotsCmd) statementNode()      {}
func (*DropGraphSnapshotCmd) statementNode()       {}

// parseGraphModelCommand recognises the graph model commands; ok=false
// when input is none of them.
func parseGraphModelCommand(input string) (Statement, bool, error) {
	lower := strings.ToLower(input)
	word := func(prefix string) (string, bool) {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(input[len(prefix):]), true
		}
		return "", false
	}

	if rest, ok := word(".create-or-alter graph_model "); ok {
		end := strings.IndexAny(rest, " \t\r\n`{")
		if end <= 0 {
			return nil, true, fmt.Errorf(".create-or-alter graph_model: expected a model name and a JSON definition")
		}
		name := unquoteIdent(rest[:end])
		payload := strings.TrimSpace(rest[end:])
		payload = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(payload, "```"), "```"))
		if payload == "" {
			return nil, true, fmt.Errorf(".create-or-alter graph_model %s: missing JSON definition", name)
		}
		return &CreateOrAlterGraphModelCmd{Name: name, Payload: payload}, true, nil
	}
	if rest, ok := word(".drop graph_model "); ok {
		if rest == "" || strings.ContainsAny(rest, " \t") {
			return nil, true, fmt.Errorf(".drop graph_model: expected a model name")
		}
		return &DropGraphModelCmd{Name: unquoteIdent(rest)}, true, nil
	}
	if lower == ".show graph_models" || lower == ".show graph_models details" {
		return &ShowGraphModelsCmd{Details: strings.HasSuffix(lower, " details")}, true, nil
	}
	if rest, ok := word(".show graph_model "); ok {
		cmd := &ShowGraphModelsCmd{}
		if idx := findKeyword(" "+rest, " with "); idx >= 0 {
			props := strings.TrimSpace(rest[idx+len(" with")-1:])
			rest = strings.TrimSpace(rest[:max(idx-1, 0)])
			if !strings.HasPrefix(props, "(") || !strings.HasSuffix(props, ")") {
				return nil, true, fmt.Errorf(".show graph_model: expected with (id = \"...\")")
			}
			for _, p := range splitTopLevelCommas(props[1 : len(props)-1]) {
				eq := strings.IndexByte(p, '=')
				if eq < 0 || !strings.EqualFold(strings.TrimSpace(p[:eq]), "id") {
					return nil, true, fmt.Errorf(".show graph_model: unknown property %q", strings.TrimSpace(p))
				}
				cmd.VersionId = strings.Trim(strings.TrimSpace(p[eq+1:]), `"'`)
			}
		}
		fields := strings.Fields(rest)
		switch {
		case len(fields) == 1:
		case len(fields) == 2 && strings.EqualFold(fields[1], "details"):
			cmd.Details = true
		default:
			return nil, true, fmt.Errorf(".show graph_model: expected a model name [details]")
		}
		cmd.Name = unquoteIdent(fields[0])
		return cmd, true, nil
	}
	if rest, ok := word(".make "); ok && strings.Contains(strings.ToLower(rest), "graph_snapshot") {
		fields := strings.Fields(rest)
		cmd := &MakeGraphSnapshotCmd{}
		if len(fields) > 0 && strings.EqualFold(fields[0], "async") {
			cmd.Async = true
			fields = fields[1:]
		}
		if len(fields) != 4 || !strings.EqualFold(fields[0], "graph_snapshot") || !strings.EqualFold(fields[2], "from") {
			return nil, true, fmt.Errorf(".make graph_snapshot: expected .make [async] graph_snapshot Name from Model")
		}
		cmd.Name, cmd.Model = unquoteIdent(fields[1]), unquoteIdent(fields[3])
		return cmd, true, nil
	}
	if rest, ok := word(".show graph_snapshots "); ok {
		if rest == "" || strings.ContainsAny(rest, " \t") {
			return nil, true, fmt.Errorf(".show graph_snapshots: expected a model name or *")
		}
		return &ShowGraphSnapshotsCmd{Model: unquoteIdent(rest)}, true, nil
	}
	if lower == ".show graph_snapshots" {
		return &ShowGraphSnapshotsCmd{Model: "*"}, true, nil
	}
	if rest, ok := word(".show graph_snapshot "); ok {
		fields := strings.Fields(rest)
		cmd := &ShowGraphSnapshotsCmd{}
		switch {
		case len(fields) == 1:
		case len(fields) == 2 && strings.EqualFold(fields[1], "details"):
			cmd.Details = true
		default:
			return nil, true, fmt.Errorf(".show graph_snapshot: expected Model.Snapshot [details]")
		}
		m, s, err := splitModelSnapshot(fields[0])
		if err != nil {
			return nil, true, fmt.Errorf(".show graph_snapshot: %w", err)
		}
		cmd.Model, cmd.Snapshot = m, s
		return cmd, true, nil
	}
	if rest, ok := word(".drop graph_snapshots "); ok {
		if rest == "" || strings.ContainsAny(rest, " \t") {
			return nil, true, fmt.Errorf(".drop graph_snapshots: expected a model name")
		}
		return &DropGraphSnapshotCmd{Model: unquoteIdent(rest)}, true, nil
	}
	if rest, ok := word(".drop graph_snapshot "); ok {
		m, s, err := splitModelSnapshot(rest)
		if err != nil {
			return nil, true, fmt.Errorf(".drop graph_snapshot: %w", err)
		}
		return &DropGraphSnapshotCmd{Model: m, Snapshot: s}, true, nil
	}
	return nil, false, nil
}

func splitModelSnapshot(s string) (string, string, error) {
	s = strings.TrimSpace(s)
	dot := strings.LastIndexByte(s, '.')
	if dot <= 0 || dot == len(s)-1 || strings.ContainsAny(s, " \t") {
		return "", "", fmt.Errorf("expected Model.Snapshot, got %q", s)
	}
	return unquoteIdent(s[:dot]), unquoteIdent(s[dot+1:]), nil
}
