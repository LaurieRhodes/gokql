package engine

// plugin_ai.go — evaluate ai_embeddings(Text, ConnectionString
// [, Options [, IncludeErrorMessages]]), ai_chat_completion(Prompt,
// ConnectionString [, ...]) (Prompt: a dynamic array of {role,
// content} messages) and ai_chat_completion_prompt(Prompt, ...)
// (Prompt: a string).
//
// The connection string is ADX's "<ModelDeploymentUri>;<auth>": the URI
// is called as an OpenAI-compatible endpoint (Azure OpenAI, OpenAI,
// llama-server, Ollama's /v1), the ";..." authentication part is
// ignored, and an API key comes only from the environment
// (GOKQL_AI_API_KEY, or GOKQL_EMBED_API_KEY for embeddings), sent as
// api-key to *.azure.com and as a bearer token elsewhere. The callout
// policy must allow the URI (type azure_openai, callout.go). For
// ai_embeddings an empty connection string, or "default", uses the
// engine's own embedder (GOKQL_EMBED, docs/guide/embeddings-and-ai.md), as
// embed_text() does.
//
// Output, as Kusto.Language types it: the input columns plus
// <Text>_embeddings (dynamic) or <Prompt>_completion (string), and
// <...>_error (string) with IncludeErrorMessages; a constant Text or
// Prompt gives one record with _embeddings / _completion. A failed
// call leaves the value null; ReturnSuccessfulOnly drops those rows
// (unless errors are included). Options RecordsPerRequest (batching
// for embeddings) and ModelParameters (merged into the request body)
// are honoured; the others are accepted.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/embed"
	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["ai_embeddings"] = func(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
		return e.aiPlugin("ai_embeddings", in, args)
	}
	evaluatePlugins["ai_chat_completion"] = func(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
		return e.aiPlugin("ai_chat_completion", in, args)
	}
	evaluatePlugins["ai_chat_completion_prompt"] = func(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
		return e.aiPlugin("ai_chat_completion_prompt", in, args)
	}
}

var deploymentName = regexp.MustCompile(`(?i)/deployments/([^/?]+)`)

type aiOptions struct {
	recordsPerRequest    int
	returnSuccessfulOnly bool
	modelParameters      map[string]interface{}
	timeout              time.Duration
}

func (e *Engine) aiPlugin(name string, in *types.Table, args []string) (*types.Table, error) {
	if len(args) < 2 || len(args) > 4 {
		return nil, fmt.Errorf("%s: expected (%s, ConnectionString [, Options [, IncludeErrorMessages]])", name,
			map[bool]string{true: "Text", false: "Prompt"}[name == "ai_embeddings"])
	}
	textExpr, err := parser.ParseExpr(strings.TrimSpace(args[0]))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	csv, _, err := pluginScalar(e, args[1])
	if err != nil {
		return nil, fmt.Errorf("%s: ConnectionString: %w", name, err)
	}
	cs, _ := csv.(string)
	uri := strings.TrimSpace(cs)
	if i := strings.Index(uri, ";"); i >= 0 {
		uri = strings.TrimSpace(uri[:i])
	}
	opts := aiOptions{recordsPerRequest: 1, timeout: 2 * time.Minute}
	if len(args) > 2 {
		m, err := pluginBag(e, name, "Options", args[2])
		if err != nil {
			return nil, err
		}
		for k, v := range m {
			switch strings.ToLower(k) {
			case "recordsperrequest":
				if n := int(types.ToInt64(v)); n > 0 {
					opts.recordsPerRequest = n
				}
			case "returnsuccessfulonly":
				b, _ := v.(bool)
				opts.returnSuccessfulOnly = b
			case "modelparameters":
				switch x := v.(type) {
				case map[string]interface{}:
					opts.modelParameters = x
				case string:
					_ = json.Unmarshal([]byte(x), &opts.modelParameters)
				}
			}
		}
	}
	withErrors := false
	if len(args) > 3 && !isTildeArg(args[3]) {
		v, _, err := pluginScalar(e, args[3])
		if err != nil {
			return nil, fmt.Errorf("%s: IncludeErrorMessages: %w", name, err)
		}
		withErrors, _ = v.(bool)
	}
	if withErrors {
		opts.returnSuccessfulOnly = false
	}
	embedding := name == "ai_embeddings"
	useDefault := embedding && (uri == "" || strings.EqualFold(uri, "default"))
	if !useDefault {
		l := strings.ToLower(uri)
		if !strings.HasPrefix(l, "https://") && !strings.HasPrefix(l, "http://") {
			return nil, fmt.Errorf("%s: the connection string must start with the model deployment URI (https://...)", name)
		}
		if err := e.checkCallout("azure_openai", uri); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}

	// rows and their inputs
	col, isCol := textExpr.(*parser.ColumnRef)
	src := in
	if !isCol {
		src = &types.Table{Rows: []types.Row{{}}} // a constant: one record
	}
	inputs := make([]types.Value, len(src.Rows))
	for i, r := range src.Rows {
		v, err := evalExpr(e, textExpr, &in.Schema, r)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		inputs[i] = v
	}
	results := make([]types.Value, len(inputs))
	errs := make([]string, len(inputs))
	ctx := e.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	key := os.Getenv("GOKQL_AI_API_KEY")
	switch {
	case embedding:
		var emb embed.Embedder
		if useDefault {
			emb = e.currentEmbedder()
		} else {
			model := ""
			if m := deploymentName.FindStringSubmatch(uri); m != nil {
				model = m[1]
			}
			if s, ok := opts.modelParameters["model"].(string); ok {
				model = s
			}
			o := embed.NewOpenAI(uri, model)
			if key != "" {
				o.APIKey = key
			}
			o.MaxBatch = opts.recordsPerRequest
			emb = o
		}
		var idx []int
		var texts []string
		for i, v := range inputs {
			if v == nil {
				errs[i] = "null text"
				continue
			}
			idx = append(idx, i)
			texts = append(texts, kqlToString(v, types.TypeString))
		}
		for start := 0; start < len(texts); start += opts.recordsPerRequest {
			end := start + opts.recordsPerRequest
			if end > len(texts) {
				end = len(texts)
			}
			vecs, err := emb.EmbedBatch(ctx, texts[start:end])
			for k := start; k < end; k++ {
				i := idx[k]
				if err != nil {
					errs[i] = err.Error()
					continue
				}
				b, _ := json.Marshal(vecs[k-start])
				results[i] = string(b)
			}
		}
	default:
		for i, v := range inputs {
			if v == nil {
				errs[i] = "null prompt"
				continue
			}
			var messages json.RawMessage
			if name == "ai_chat_completion_prompt" {
				messages, _ = json.Marshal([]map[string]string{{"role": "user", "content": kqlToString(v, types.TypeString)}})
			} else {
				s := fmt.Sprint(v)
				if !json.Valid([]byte(s)) {
					errs[i] = "the prompt must be a dynamic array of {role, content} messages"
					continue
				}
				messages = json.RawMessage(s)
			}
			text, err := chatCompletion(ctx, uri, key, messages, opts)
			if err != nil {
				errs[i] = err.Error()
				continue
			}
			results[i] = text
		}
	}

	prefix := ""
	if isCol {
		prefix = col.Name
	}
	suffix, typ := "_completion", types.TypeString
	if embedding {
		suffix, typ = "_embeddings", types.TypeDynamic
	}
	schema := types.Schema{}
	if isCol {
		schema.Columns = append(schema.Columns, in.Schema.Columns...)
	}
	schema.Columns = append(schema.Columns, types.Column{Name: prefix + suffix, Type: typ})
	if withErrors {
		schema.Columns = append(schema.Columns, types.Column{Name: prefix + suffix + "_error", Type: types.TypeString})
	}
	out := types.NewTable("", schema)
	for i, r := range src.Rows {
		if results[i] == nil && opts.returnSuccessfulOnly {
			continue
		}
		row := types.Row{}
		if isCol {
			row = append(row, r...)
		}
		row = append(row, results[i])
		if withErrors {
			row = append(row, errs[i])
		}
		out.AddRow(row)
	}
	return out, nil
}

// chatCompletion posts messages to an OpenAI-compatible chat endpoint
// and returns the first choice's content.
func chatCompletion(ctx context.Context, uri, key string, messages json.RawMessage, opts aiOptions) (string, error) {
	body := map[string]interface{}{}
	if m := deploymentName.FindStringSubmatch(uri); m != nil {
		body["model"] = m[1]
	}
	for k, v := range opts.modelParameters {
		body[k] = v
	}
	body["messages"] = messages
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uri, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	embed.SetAPIKey(req, key)
	resp, err := (&http.Client{Timeout: opts.timeout}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, httpBodyLimit))
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		s := strings.TrimSpace(string(raw))
		if len(s) > 300 {
			s = s[:300]
		}
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, s)
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || len(r.Choices) == 0 {
		return "", fmt.Errorf("unexpected response: no choices")
	}
	return r.Choices[0].Message.Content, nil
}
