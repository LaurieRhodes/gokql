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
	ShowAll   bool // .show graph_models with (showAll = true): every version
}

// GraphModelPolicyCmd: .show graph_model M|* policy P,
// .alter graph_model M policy P ```{json}``` and
// .delete graph_model M policy P.
type GraphModelPolicyCmd struct {
	Action string // "show", "alter" or "delete"
	Model  string // "*" = every model (show only)
	Policy string
	Value  string // the JSON policy object (alter)
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
	// Report: "" (the listing), "statistics", "data statistics" or
	// "failures" (failures is plural-form only).
	Report string
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
func (*GraphModelPolicyCmd) statementNode()        {}

// ShowOperationsCmd: .show operations [Id | (Id, ...)]. Operations
// recorded here are graph snapshot builds.
type ShowOperationsCmd struct{ Ids []string }

func (*ShowOperationsCmd) statementNode() {}

// parseShowWithProps parses a trailing with (k = v, ...) clause,
// returning the text before it and the lowercased properties.
func parseShowWithProps(rest, cmd string) (string, map[string]string, error) {
	props := map[string]string{}
	idx := findKeyword(" "+rest, " with ")
	if idx < 0 {
		return rest, props, nil
	}
	clause := strings.TrimSpace(rest[idx+len(" with")-1:])
	rest = strings.TrimSpace(rest[:max(idx-1, 0)])
	if !strings.HasPrefix(clause, "(") || !strings.HasSuffix(clause, ")") {
		return "", nil, fmt.Errorf("%s: expected with (property = value)", cmd)
	}
	for _, p := range splitTopLevelCommas(clause[1 : len(clause)-1]) {
		eq := strings.IndexByte(p, '=')
		if eq < 0 {
			return "", nil, fmt.Errorf("%s: expected property = value, got %q", cmd, strings.TrimSpace(p))
		}
		props[strings.ToLower(strings.TrimSpace(p[:eq]))] = strings.Trim(strings.TrimSpace(p[eq+1:]), `"'`)
	}
	return rest, props, nil
}

// parseGraphModelCommand recognises the graph model commands; ok=false
// when input is none of them.
func parseGraphModelCommand(input string) (Statement, bool, error) {
	lower := keywordFold(input)
	word := func(prefix string) (string, bool) {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(input[len(prefix):]), true
		}
		return "", false
	}

	if lower == ".show operations" || strings.HasPrefix(lower, ".show operations ") {
		rest := strings.TrimSpace(input[len(".show operations"):])
		rest = strings.TrimSuffix(strings.TrimPrefix(rest, "("), ")")
		cmd := &ShowOperationsCmd{}
		for _, id := range splitTopLevelCommas(rest) {
			if id = strings.Trim(strings.TrimSpace(id), `"'`); id != "" {
				cmd.Ids = append(cmd.Ids, strings.ToLower(id))
			}
		}
		return cmd, true, nil
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
	if lower == ".show graph_models" || strings.HasPrefix(lower, ".show graph_models ") {
		rest, props, err := parseShowWithProps(strings.TrimSpace(input[len(".show graph_models"):]), ".show graph_models")
		if err != nil {
			return nil, true, err
		}
		cmd := &ShowGraphModelsCmd{}
		switch strings.ToLower(rest) {
		case "":
		case "details":
			cmd.Details = true
		default:
			return nil, true, fmt.Errorf(".show graph_models: unexpected %q", rest)
		}
		for k, v := range props {
			if k != "showall" {
				return nil, true, fmt.Errorf(".show graph_models: unknown property %q", k)
			}
			cmd.ShowAll = strings.EqualFold(v, "true")
		}
		return cmd, true, nil
	}
	for _, action := range []string{"show", "alter", "delete"} {
		rest, ok := word("." + action + " graph_model ")
		if !ok {
			continue
		}
		pi := findKeyword(" "+rest, " policy ")
		if pi < 0 {
			if action == "show" {
				break
			}
			return nil, true, fmt.Errorf(".%s graph_model: expected Model policy PolicyName", action)
		}
		model := unquoteIdent(strings.TrimSpace(rest[:max(pi-1, 0)]))
		after := strings.TrimSpace(rest[pi+len(" policy")-1:])
		end := strings.IndexAny(after, " \t\r\n`{")
		if end < 0 {
			end = len(after)
		}
		cmd := &GraphModelPolicyCmd{Action: action, Model: model, Policy: strings.ToLower(after[:end])}
		value := strings.TrimSpace(after[end:])
		value = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "```"), "```"))
		switch {
		case model == "" || cmd.Policy == "":
			return nil, true, fmt.Errorf(".%s graph_model: expected Model policy PolicyName", action)
		case action == "alter" && value == "":
			return nil, true, fmt.Errorf(".alter graph_model %s policy %s: missing policy object", model, cmd.Policy)
		case action != "alter" && value != "":
			return nil, true, fmt.Errorf(".%s graph_model %s policy %s: unexpected %q", action, model, cmd.Policy, value)
		case model == "*" && action != "show":
			return nil, true, fmt.Errorf(".%s graph_model: a model name is required", action)
		}
		cmd.Value = value
		return cmd, true, nil
	}
	if rest, ok := word(".show graph_model "); ok {
		cmd := &ShowGraphModelsCmd{}
		rest, props, err := parseShowWithProps(rest, ".show graph_model")
		if err != nil {
			return nil, true, err
		}
		for k, v := range props {
			if k != "id" {
				return nil, true, fmt.Errorf(".show graph_model: unknown property %q", k)
			}
			cmd.VersionId = v
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
	if rest, ok := word(".make "); ok && strings.Contains(keywordFold(rest), "graph_snapshot") {
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
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return nil, true, fmt.Errorf(".show graph_snapshots: expected a model name or *")
		}
		cmd := &ShowGraphSnapshotsCmd{Model: unquoteIdent(fields[0])}
		switch strings.ToLower(strings.Join(fields[1:], " ")) {
		case "":
		case "statistics":
			cmd.Report = "statistics"
		case "data statistics":
			cmd.Report = "data statistics"
		case "failures":
			cmd.Report = "failures"
		default:
			return nil, true, fmt.Errorf(".show graph_snapshots: unexpected %q", strings.Join(fields[1:], " "))
		}
		return cmd, true, nil
	}
	if lower == ".show graph_snapshots" {
		return &ShowGraphSnapshotsCmd{Model: "*"}, true, nil
	}
	if rest, ok := word(".show graph_snapshot "); ok {
		fields := strings.Fields(rest)
		cmd := &ShowGraphSnapshotsCmd{}
		tail := ""
		if len(fields) > 1 {
			tail = strings.ToLower(strings.Join(fields[1:], " "))
		}
		switch {
		case len(fields) == 0:
			return nil, true, fmt.Errorf(".show graph_snapshot: expected Model.Snapshot")
		case tail == "":
		case tail == "details":
			cmd.Details = true
		case tail == "statistics" || tail == "data statistics":
			cmd.Report = tail
		default:
			return nil, true, fmt.Errorf(".show graph_snapshot: expected Model.Snapshot [details | statistics | data statistics]")
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
