package engine

// script.go — .execute database script <| commands: runs control
// commands in order and reports each one's outcome, as ADX does.
// Without ContinueOnErrors the commands after a failure are skipped;
// with ThrowOnErrors a failure is returned as an error.
//
// Atomic (a gokql extension): when every command is an append
// (.set-or-append, .append), all of them are evaluated and checked
// first and only then written, so a failing command leaves nothing
// written. ADX scripts are not transactional; a claim and its edges
// written by one script otherwise survive half-written.

import (
	"fmt"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func scriptResultTable() *types.Table {
	return types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "OperationId", Type: types.TypeLong},
		{Name: "CommandType", Type: types.TypeString},
		{Name: "CommandText", Type: types.TypeString},
		{Name: "Result", Type: types.TypeString},
		{Name: "Reason", Type: types.TypeString},
	}})
}

func commandType(text string) string {
	f := strings.Fields(text)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func (e *Engine) applyExecuteScript(cmd *parser.ExecuteScriptCmd) (*types.Table, error) {
	out := scriptResultTable()
	if cmd.Atomic {
		return e.executeScriptAtomic(cmd, out)
	}
	failed := false
	for i, text := range cmd.Commands {
		id := int64(i + 1)
		if failed && !cmd.ContinueOnErrors {
			out.AddRow(types.Row{id, commandType(text), text, "Skipped", ""})
			continue
		}
		stmt, err := parser.Parse(text)
		if err == nil {
			if _, nested := stmt.(*parser.ExecuteScriptCmd); nested {
				err = fmt.Errorf("a script cannot run another script")
			} else {
				_, err = e.execute(stmt)
			}
		}
		if err != nil {
			failed = true
			out.AddRow(types.Row{id, commandType(text), text, "Failed", err.Error()})
			if cmd.ThrowOnErrors {
				return nil, fmt.Errorf(".execute database script: command %d failed: %w", id, err)
			}
			continue
		}
		out.AddRow(types.Row{id, commandType(text), text, "Completed", ""})
	}
	return out, nil
}

func (e *Engine) executeScriptAtomic(cmd *parser.ExecuteScriptCmd, out *types.Table) (*types.Table, error) {
	var cmds []*parser.SetOrAppendCmd
	for i, text := range cmd.Commands {
		stmt, err := parser.Parse(text)
		if err != nil {
			return nil, fmt.Errorf(".execute database script: command %d: %w", i+1, err)
		}
		sa, ok := stmt.(*parser.SetOrAppendCmd)
		if !ok || sa.Mode == "set-or-replace" {
			return nil, fmt.Errorf(".execute database script with (Atomic=true): command %d is not .set-or-append or .append", i+1)
		}
		cmds = append(cmds, sa)
	}
	// Two commands creating or extending the same table would each
	// plan against the schema before the other's change.
	seen := map[string]bool{}
	var pend []*pendingAppend
	for i, sa := range cmds {
		p, err := e.prepareAppend(sa)
		if err != nil {
			return nil, fmt.Errorf(".execute database script with (Atomic=true): command %d failed, nothing written: %w", i+1, err)
		}
		if (p.create || len(p.extend) > 0) && seen[p.table] || seen[p.table] && hasSchemaChange(pend, p.table) {
			return nil, fmt.Errorf(".execute database script with (Atomic=true): command %d changes the schema of %s, "+
				"which an earlier command also writes; split the script", i+1, p.table)
		}
		seen[p.table] = true
		pend = append(pend, p)
	}
	for i, sa := range cmds {
		if err := e.checkDirectWrite(sa.TableName, sa.Mode); err != nil {
			return nil, fmt.Errorf(".execute database script with (Atomic=true): command %d: %w", i+1, err)
		}
	}
	// Update policies of every command are evaluated before any write.
	if _, err := e.commitWithPolicies(pend); err != nil {
		return nil, fmt.Errorf(".execute database script with (Atomic=true): %w", err)
	}
	for i := range pend {
		out.AddRow(types.Row{int64(i + 1), commandType(cmd.Commands[i]), cmd.Commands[i], "Completed", ""})
	}
	return out, nil
}

func hasSchemaChange(pend []*pendingAppend, table string) bool {
	for _, p := range pend {
		if p.table == table && (p.create || len(p.extend) > 0) {
			return true
		}
	}
	return false
}
