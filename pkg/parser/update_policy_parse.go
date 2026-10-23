package parser

import (
	"fmt"
	"strings"
)

// UpdatePolicyCmd: .show table T policy update, .alter table T policy
// update <json array>, .delete table T policy update.
type UpdatePolicyCmd struct {
	Action string // "show", "alter", "delete"
	Table  string
	Value  string // alter: the JSON array
}

func (*UpdatePolicyCmd) statementNode() {}

// parseUpdatePolicyCommand recognises the table update policy commands.
// The policy is a JSON array, given bare, in ```fences``` or as a
// string literal (@'...', '...', "...").
func parseUpdatePolicyCommand(input string) (Statement, bool, error) {
	lower := keywordFold(input)
	for _, action := range []string{"show", "alter", "delete"} {
		prefix := "." + action + " table "
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		rest := input[len(prefix):]
		i := findKeyword(" "+rest, " policy update")
		if i < 0 {
			return nil, false, nil
		}
		table := unquoteIdent(strings.TrimSpace(rest[:max(i-1, 0)]))
		value := strings.TrimSpace(rest[i-1+len(" policy update"):])
		if table == "" || strings.ContainsAny(table, " \t") {
			return nil, true, fmt.Errorf(".%s table: expected a table name before policy update", action)
		}
		cmd := &UpdatePolicyCmd{Action: action, Table: table}
		if action != "alter" {
			if value != "" {
				return nil, true, fmt.Errorf(".%s table %s policy update: unexpected %q", action, table, value)
			}
			return cmd, true, nil
		}
		value = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "```"), "```"))
		if value != "" && (value[0] == '@' || value[0] == '\'' || value[0] == '"') {
			x, err := ParseExpr(value)
			var s string
			isStr := false
			if lit, ok := x.(*Literal); ok && err == nil {
				s, isStr = lit.Value.(string)
			}
			if !isStr {
				return nil, true, fmt.Errorf(".alter table %s policy update: expected the policy as a JSON array or string literal", table)
			}
			value = s
		}
		if value == "" {
			return nil, true, fmt.Errorf(".alter table %s policy update: missing the policy (a JSON array)", table)
		}
		cmd.Value = value
		return cmd, true, nil
	}
	return nil, false, nil
}
