package engine

// updatepolicy.go — table update policies, as ADX's: when rows are
// appended to a policy's Source table, its Query runs over just those
// rows (Source names the new rows inside the query) and the result is
// appended to the table that holds the policy.
//
// IsTransactional: the policy's query runs, and its result is checked
// against the target, before anything is written; a failure fails the
// whole write, source included. Otherwise the policy runs after the
// source write and a failure is reported in the command's result
// without undoing it. Policies cascade (a target may be another
// policy's source) up to maxPolicyDepth.
//
// BlockDirectWrites (a gokql extension, not ADX): the target accepts
// rows only through its update policy; .set-or-append, .append and
// .ingest into it directly are refused. With a transactional policy
// whose query validates (assert()), every row in the table has passed
// validation. .set-or-replace and .delete records stay allowed, as
// maintenance.
//
// Policies are triggered by .set-or-append and .append (and the
// appends of an .execute database script). .ingest inline / csv into a
// policy source is refused rather than silently skipping the policy.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

const updatePoliciesTableName = "_UpdatePolicies"

const maxPolicyDepth = 8

type updatePolicy struct {
	IsEnabled                    bool
	Source                       string
	Query                        string
	IsTransactional              bool
	PropagateIngestionProperties bool `json:",omitempty"`
	BlockDirectWrites            bool `json:",omitempty"`
}

func updatePoliciesSchema() types.Schema {
	return types.Schema{Columns: []types.Column{
		{Name: "Table", Type: types.TypeString},
		{Name: "Policy", Type: types.TypeString}, // JSON array; "" once deleted
		{Name: "CreatedAt", Type: types.TypeDatetime},
	}}
}

// tablePolicies returns every table's current update policies (the
// latest row per table wins).
func (e *Engine) tablePolicies() (map[string][]updatePolicy, error) {
	if e.Catalog.GetTable(updatePoliciesTableName) == nil {
		return nil, nil
	}
	all, err := e.executeQuery(&parser.Query{Source: updatePoliciesTableName})
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", updatePoliciesTableName, err)
	}
	ti, pi, ci := all.Schema.ColumnIndex("Table"), all.Schema.ColumnIndex("Policy"), all.Schema.ColumnIndex("CreatedAt")
	type latest struct {
		at     int64
		policy string
	}
	byTable := map[string]latest{}
	for _, r := range all.Rows {
		t := fmt.Sprintf("%v", r[ti])
		at, _ := r[ci].(int64)
		if l, ok := byTable[t]; !ok || at >= l.at {
			p, _ := r[pi].(string)
			byTable[t] = latest{at, p}
		}
	}
	out := map[string][]updatePolicy{}
	for t, l := range byTable {
		if l.policy == "" {
			continue
		}
		var ps []updatePolicy
		if err := json.Unmarshal([]byte(l.policy), &ps); err != nil {
			return nil, fmt.Errorf("update policy of %s: %w", t, err)
		}
		out[t] = ps
	}
	return out, nil
}

func (e *Engine) applyUpdatePolicyCmd(cmd *parser.UpdatePolicyCmd) (*types.Table, error) {
	if e.Catalog.GetTable(cmd.Table) == nil {
		return nil, fmt.Errorf("table %q not found", cmd.Table)
	}
	switch cmd.Action {
	case "show":
		all, err := e.tablePolicies()
		if err != nil {
			return nil, err
		}
		out := types.NewTable("", types.Schema{Columns: []types.Column{
			{Name: "PolicyName", Type: types.TypeString},
			{Name: "EntityName", Type: types.TypeString},
			{Name: "Policy", Type: types.TypeString},
		}})
		policy := "null"
		if ps, ok := all[cmd.Table]; ok {
			b, _ := json.Marshal(ps)
			policy = string(b)
		}
		out.AddRow(types.Row{"UpdatePolicy", cmd.Table, policy})
		return out, nil
	case "delete":
		return e.writeUpdatePolicy(cmd.Table, "")
	}
	var ps []updatePolicy
	dec := json.NewDecoder(strings.NewReader(cmd.Value))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ps); err != nil {
		return nil, fmt.Errorf(".alter table %s policy update: the policy must be a JSON array of "+
			"{IsEnabled, Source, Query, IsTransactional, PropagateIngestionProperties, BlockDirectWrites}: %w", cmd.Table, err)
	}
	for i, p := range ps {
		if e.Catalog.GetTable(p.Source) == nil {
			return nil, fmt.Errorf(".alter table %s policy update: policy %d: source table %q not found", cmd.Table, i+1, p.Source)
		}
		if p.Source == cmd.Table {
			return nil, fmt.Errorf(".alter table %s policy update: policy %d: a table cannot be its own source", cmd.Table, i+1)
		}
		if _, err := parser.Parse(p.Query); err != nil {
			return nil, fmt.Errorf(".alter table %s policy update: policy %d: query: %w", cmd.Table, i+1, err)
		}
	}
	b, _ := json.Marshal(ps)
	return e.writeUpdatePolicy(cmd.Table, string(b))
}

func (e *Engine) writeUpdatePolicy(table, policy string) (*types.Table, error) {
	if e.Catalog.GetTable(updatePoliciesTableName) == nil {
		if err := e.Catalog.CreateTable(updatePoliciesTableName, updatePoliciesSchema()); err != nil {
			return nil, err
		}
		if err := e.persistDiscoverySchema(updatePoliciesTableName, updatePoliciesSchema()); err != nil {
			return nil, err
		}
	}
	def := e.Catalog.GetTable(updatePoliciesTableName)
	row := types.Row{table, policy, time.Now().UTC().UnixNano()}
	if _, err := e.flushBatch(updatePoliciesTableName, def, []types.Row{row}); err != nil {
		return nil, err
	}
	return okResult("OK"), nil
}

// checkDirectWrite refuses an append into a table whose update policy
// blocks direct writes, and an ingest into a policy source.
func (e *Engine) checkDirectWrite(table, mode string) error {
	if mode == "set-or-replace" {
		return nil
	}
	all, err := e.tablePolicies()
	if err != nil {
		return err
	}
	for _, p := range all[table] {
		if p.IsEnabled && p.BlockDirectWrites {
			return fmt.Errorf("table %q accepts rows only through its update policy (from %s); write to %s instead",
				table, p.Source, p.Source)
		}
	}
	if mode == "ingest" {
		for target, ps := range all {
			for _, p := range ps {
				if p.IsEnabled && p.Source == table {
					return fmt.Errorf("table %q is the source of %s's update policy; use .set-or-append so the policy runs", table, target)
				}
			}
		}
	}
	return nil
}

// policyRun is one update policy's output for a write.
type policyRun struct {
	target string
	p      *pendingAppend // nil when the query returned no rows
	err    error
}

// runPolicies evaluates, for rows just prepared for table src, every
// enabled policy with that source, and those policies' own policies in
// turn. transactional selects which policies to run.
func (e *Engine) runPolicies(all map[string][]updatePolicy, src *pendingAppend, transactional bool, depth int) []policyRun {
	var out []policyRun
	if depth > maxPolicyDepth {
		return []policyRun{{target: src.table, err: fmt.Errorf("update policies nest deeper than %d (a cycle?)", maxPolicyDepth)}}
	}
	for target, ps := range all {
		for _, pol := range ps {
			if !pol.IsEnabled || pol.Source != src.table || pol.IsTransactional != transactional {
				continue
			}
			res, err := e.evalPolicyQuery(pol, src)
			if err != nil {
				out = append(out, policyRun{target: target, err: fmt.Errorf("update policy of %s: %w", target, err)})
				continue
			}
			if len(res.Rows) == 0 {
				out = append(out, policyRun{target: target})
				continue
			}
			p, err := e.prepareFromResult(&parser.SetOrAppendCmd{TableName: target, Mode: "append"}, res)
			if err != nil {
				out = append(out, policyRun{target: target, err: fmt.Errorf("update policy of %s: %w", target, err)})
				continue
			}
			out = append(out, policyRun{target: target, p: p})
			out = append(out, e.runPolicies(all, p, transactional, depth+1)...)
		}
	}
	return out
}

// evalPolicyQuery runs a policy's query with its Source table name bound
// to the new rows only.
func (e *Engine) evalPolicyQuery(pol updatePolicy, src *pendingAppend) (*types.Table, error) {
	stmt, err := parser.Parse(pol.Query)
	if err != nil {
		return nil, err
	}
	delta := types.NewTable(src.table, src.schema)
	for _, r := range src.rows {
		delta.AddRow(r)
	}
	saved := e.letContext
	e.setLetContext(&LetContext{Tables: map[string]*types.Table{pol.Source: delta}, Parent: saved})
	defer e.setLetContext(saved)
	return e.execute(stmt)
}

// commitWithPolicies writes prepared appends after evaluating their
// transactional update policies (any failure: nothing is written),
// then runs the other policies and reports their outcomes as extra
// result rows.
func (e *Engine) commitWithPolicies(pend []*pendingAppend) (*types.Table, error) {
	all, err := e.tablePolicies()
	if err != nil {
		return nil, err
	}
	var chain []*pendingAppend
	var txRuns []policyRun
	for _, p := range pend {
		chain = append(chain, p)
		for _, r := range e.runPolicies(all, p, true, 0) {
			if r.err != nil {
				return nil, fmt.Errorf("%w (transactional update policy: nothing was written)", r.err)
			}
			txRuns = append(txRuns, r)
			if r.p != nil {
				chain = append(chain, r.p)
			}
		}
	}
	direct := map[*pendingAppend]bool{}
	for _, p := range pend {
		direct[p] = true
	}
	var out *types.Table
	for i, p := range chain {
		t, err := e.commitAppend(p)
		if err != nil {
			return nil, fmt.Errorf("writing %s failed after %d of %d writes: %w", p.table, i, len(chain), err)
		}
		switch {
		case out == nil:
			out = t
		case direct[p]:
			out.Rows = append(out.Rows, t.Rows...)
		default:
			for _, r := range t.Rows {
				out.AddRow(types.Row{"OK: update policy -> " + p.table, r[1], r[2]})
			}
		}
	}
	// Non-transactional policies run after the writes they depend on.
	for _, p := range pend {
		for _, r := range e.runPolicies(all, p, false, 0) {
			switch {
			case r.err != nil:
				out.AddRow(types.Row{"FAILED: " + r.err.Error(), "", int64(0)})
			case r.p != nil:
				t, err := e.commitAppend(r.p)
				if err != nil {
					out.AddRow(types.Row{"FAILED: update policy -> " + r.target + ": " + err.Error(), "", int64(0)})
					continue
				}
				for _, row := range t.Rows {
					out.AddRow(types.Row{"OK: update policy -> " + r.target, row[1], row[2]})
				}
			}
		}
	}
	return out, nil
}
