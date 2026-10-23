package engine

// plugin_http.go — evaluate http_request(Uri [, RequestHeaders
// [, Options]]) and http_request_post(Uri [, RequestHeaders [, Options
// [, Content]]]): one row of ResponseHeaders (a property bag) and
// ResponseBody (parsed as JSON when the response says
// application/json, else the text), as ADX returns them. The callout
// policy (callout.go, type webapi) must allow the URI. As in ADX the
// request carries x-ms-client-request-id and x-ms-readonly: true;
// Options is accepted and ignored.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/LaurieRhodes/gokql/pkg/types"
)

func init() {
	evaluatePlugins["http_request"] = func(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
		return e.httpRequest("http_request", http.MethodGet, args)
	}
	evaluatePlugins["http_request_post"] = func(e *Engine, in *types.Table, args []string, _ *types.Schema) (*types.Table, error) {
		return e.httpRequest("http_request_post", http.MethodPost, args)
	}
}

const httpBodyLimit = 64 << 20

// pluginBag evaluates a dynamic property-bag argument.
func pluginBag(e *Engine, name, which, text string) (map[string]interface{}, error) {
	if isTildeArg(text) {
		return nil, nil
	}
	v, _, err := pluginScalar(e, text)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", name, which, err)
	}
	switch x := v.(type) {
	case nil:
		return nil, nil
	case map[string]interface{}:
		return x, nil
	case string:
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(x), &m); err != nil {
			return nil, fmt.Errorf("%s: %s must be a property bag", name, which)
		}
		return m, nil
	}
	return nil, fmt.Errorf("%s: %s must be a property bag", name, which)
}

func (e *Engine) httpRequest(name, method string, args []string) (*types.Table, error) {
	max := 3
	if method == http.MethodPost {
		max = 4
	}
	if len(args) < 1 || len(args) > max {
		return nil, fmt.Errorf("%s: expected (Uri [, RequestHeaders [, Options%s]])", name, map[bool]string{true: " [, Content]", false: ""}[max == 4])
	}
	uv, _, err := pluginScalar(e, args[0])
	if err != nil {
		return nil, fmt.Errorf("%s: Uri: %w", name, err)
	}
	uri, ok := uv.(string)
	if !ok || uri == "" {
		return nil, fmt.Errorf("%s: Uri must be a non-empty string", name)
	}
	lower := strings.ToLower(uri)
	if !strings.HasPrefix(lower, "https://") && !strings.HasPrefix(lower, "http://") {
		return nil, fmt.Errorf("%s: Uri must be http(s)", name)
	}
	if err := e.checkCallout("webapi", uri); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var headers map[string]interface{}
	if len(args) > 1 {
		if headers, err = pluginBag(e, name, "RequestHeaders", args[1]); err != nil {
			return nil, err
		}
	}
	if len(args) > 2 {
		if _, err := pluginBag(e, name, "Options", args[2]); err != nil {
			return nil, err
		}
	}
	if strings.HasPrefix(lower, "http://") {
		for k := range headers {
			if strings.EqualFold(k, "authorization") {
				return nil, fmt.Errorf("%s: authentication requires https", name)
			}
		}
	}
	var body io.Reader
	if method == http.MethodPost {
		content := ""
		if len(args) > 3 && !isTildeArg(args[3]) {
			cv, _, err := pluginScalar(e, args[3])
			if err != nil {
				return nil, fmt.Errorf("%s: Content: %w", name, err)
			}
			if cv != nil {
				content = fmt.Sprint(cv)
			}
		}
		body = bytes.NewReader([]byte(content))
	}
	ctx := e.Ctx
	req, err := newHTTPRequest(ctx, method, uri, body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	req.Header.Set("x-ms-client-request-id", "gokql;"+newGUIDString())
	req.Header.Set("x-ms-readonly", "true")
	for k, v := range headers {
		req.Header.Set(k, fmt.Sprint(v))
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, httpBodyLimit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: reading the response: %w", name, err)
	}
	if len(raw) > httpBodyLimit {
		return nil, fmt.Errorf("%s: response larger than %d bytes", name, httpBodyLimit)
	}
	hdr := map[string]interface{}{}
	keys := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		hdr[k] = strings.Join(resp.Header[k], ", ")
	}
	hb, _ := json.Marshal(hdr)
	var respBody string
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if (mt == "application/json" || strings.HasSuffix(mt, "+json")) && json.Valid(raw) {
		respBody = string(raw)
	} else {
		b, _ := json.Marshal(string(raw))
		respBody = string(b)
	}
	out := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "ResponseHeaders", Type: types.TypeDynamic},
		{Name: "ResponseBody", Type: types.TypeDynamic},
	}})
	out.AddRow(types.Row{string(hb), respBody})
	return out, nil
}

func newHTTPRequest(ctx context.Context, method, uri string, body io.Reader) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return http.NewRequestWithContext(ctx, method, uri, body)
}
