package engine

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func calloutScope(t *testing.T, rules []calloutRule) *Engine {
	t.Helper()
	dir := t.TempDir()
	b, _ := json.Marshal(map[string]interface{}{"callouts": rules})
	if err := os.WriteFile(filepath.Join(dir, federationConfigFileName), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return discoverEngine(t, dir)
}

func TestHTTPRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"Items":[{"n":1,"s":"a"},{"n":2,"s":"b"}],"ro":"`+r.Header.Get("x-ms-readonly")+`","h":"`+r.Header.Get("X-Test")+`"}`)
		case "/echo":
			b, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, r.Method+" "+r.Header.Get("Content-Type")+" "+string(b))
		}
	}))
	defer srv.Close()
	eng := calloutScope(t, []calloutRule{{CalloutType: "webapi", CalloutUriRegex: "^" + srv.URL + "/", CanCall: true},
		{CalloutType: "webapi", CalloutUriRegex: "/denied", CanCall: false}})
	expectWrRows(t, eng, `evaluate http_request('`+srv.URL+`/json', dynamic({'X-Test': 'yes'}))
		| project ResponseBody.Items, ro = tostring(ResponseBody.ro), h = tostring(ResponseBody.h)
		| mv-expand ResponseBody_Items | evaluate bag_unpack(ResponseBody_Items) | project n, s, ro, h`,
		"1 | a | true | yes", "2 | b | true | yes")
	expectWrRows(t, eng, `let u = '`+srv.URL+`/echo'; evaluate http_request_post(u, dynamic(null), dynamic(null), '{"q":1}') | project tostring(ResponseBody)`,
		`POST application/json; charset=utf-8 {"q":1}`)
	expectWrRows(t, eng, `evaluate http_request('`+srv.URL+`/json') | project h = isnotnull(ResponseHeaders['Content-Type'])`, "true")
	expectErrContains(t, eng, `evaluate http_request('`+srv.URL+`/denied')`, "denied")
	expectErrContains(t, eng, `evaluate http_request('https://example.com/')`, "not allowed")
	expectErrContains(t, eng, `.alter cluster policy callout @'[]'`, "cannot be changed")
	expectWrRows(t, eng, `.show cluster policy callout | project EntityType`, "Cluster", "Cluster immutable policy")
	// no policy: webapi refused, externaldata over http allowed (ADX's predefined rule)
	bare := discoverEngine(t, t.TempDir())
	expectErrContains(t, bare, `evaluate http_request('`+srv.URL+`/json')`, "not allowed")
	expectWrRows(t, bare, `externaldata(x:string)['`+srv.URL+`/echo'] with (format='txt')`, "GET  ")
}
