package engine

// callout.go — the callout policy: which outside endpoints a query may
// reach, in ADX's form. Rules come from "callouts" in the primary
// scope's .okql-federation.json, e.g.
//
//	"callouts": [{"CalloutType": "webapi",
//	              "CalloutUriRegex": "^https://prices\\.azure\\.com/",
//	              "CanCall": true}]
//
// plus ADX's predefined rule that external_data (externaldata) may read
// anything. A callout is allowed when a rule of its type matches the
// URI and none of the matching rules denies it; with no matching rule
// it is refused. Types used here: webapi (http_request,
// http_request_post), azure_openai (ai_embeddings, ai_chat_completion*
// endpoints) and external_data (externaldata over http). The policy is
// the operator's file: no command may change it, so a query (or the
// model writing it) cannot widen what it may reach.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

type calloutRule struct {
	CalloutType     string `json:"CalloutType"`
	CalloutUriRegex string `json:"CalloutUriRegex"`
	CanCall         bool   `json:"CanCall"`
}

var predefinedCallouts = []calloutRule{{CalloutType: "external_data", CalloutUriRegex: ".*", CanCall: true}}

func (e *Engine) calloutRules() []calloutRule {
	if e.hostCallouts != nil {
		return *e.hostCallouts
	}
	return e.federation().Callouts
}

// checkCallout returns an error unless the policy lets a callout of
// kind reach uri.
func (e *Engine) checkCallout(kind, uri string) error {
	allowed := false
	for _, r := range append(append([]calloutRule{}, predefinedCallouts...), e.calloutRules()...) {
		if !strings.EqualFold(r.CalloutType, kind) {
			continue
		}
		re, err := regexp.Compile(r.CalloutUriRegex)
		if err != nil {
			return fmt.Errorf("callout policy: invalid CalloutUriRegex %q: %w", r.CalloutUriRegex, err)
		}
		if !re.MatchString(uri) {
			continue
		}
		if !r.CanCall {
			return fmt.Errorf("callout to %s denied by the callout policy (%s)", uri, kind)
		}
		allowed = true
	}
	if !allowed {
		return fmt.Errorf("callout to %s is not allowed: the callout policy has no %s rule for it. "+
			"Add {\"CalloutType\": %q, \"CalloutUriRegex\": \"<regex>\", \"CanCall\": true} to \"callouts\" in %s",
			uri, kind, kind, federationConfigFileName)
	}
	return nil
}

// applyShowCalloutPolicy implements .show cluster policy callout.
func (e *Engine) applyShowCalloutPolicy() (*types.Table, error) {
	out := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "PolicyName", Type: types.TypeString},
		{Name: "EntityName", Type: types.TypeString},
		{Name: "Policy", Type: types.TypeString},
		{Name: "ChildEntities", Type: types.TypeString},
		{Name: "EntityType", Type: types.TypeString},
	}})
	add := func(rules []calloutRule, kind string) {
		if rules == nil {
			rules = []calloutRule{}
		}
		b, _ := json.Marshal(rules)
		out.AddRow(types.Row{"CalloutPolicy", "", string(b), "", kind})
	}
	add(e.calloutRules(), "Cluster")
	add(predefinedCallouts, "Cluster immutable policy")
	return out, nil
}
