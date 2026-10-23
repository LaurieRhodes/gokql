package engine

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LaurieRhodes/gokql/pkg/embed"
)

func TestAIPlugins(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req map[string]json.RawMessage
		_ = json.Unmarshal(b, &req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/embeddings"):
			var in []string
			_ = json.Unmarshal(req["input"], &in)
			var data []string
			for i, s := range in {
				data = append(data, `{"index":`+itoa(i)+`,"embedding":[`+itoa(len(s))+`,1]}`)
			}
			io.WriteString(w, `{"data":[`+strings.Join(data, ",")+`]}`)
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			if strings.Contains(string(req["messages"]), "fail") {
				w.WriteHeader(500)
				io.WriteString(w, `{"error":"boom"}`)
				return
			}
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"model=`+strings.Trim(string(req["model"]), `"`)+
				` t=`+string(req["temperature"])+` n=`+itoa(strings.Count(string(req["messages"]), "role"))+`"}}]}`)
		}
	}))
	defer srv.Close()
	eng := calloutScope(t, []calloutRule{{CalloutType: "azure_openai", CalloutUriRegex: "^" + srv.URL, CanCall: true}})
	emb := srv.URL + "/openai/deployments/emb3/embeddings?api-version=1;managed_identity=system"
	chat := srv.URL + "/openai/deployments/gpt/chat/completions?api-version=1;impersonate"
	expectWrRows(t, eng, `datatable(T:string)['ab', 'abcd'] | evaluate ai_embeddings(T, '`+emb+`', dynamic({'RecordsPerRequest': 2}))`,
		`ab | [2,1]`, `abcd | [4,1]`)
	expectWrRows(t, eng, `evaluate ai_embeddings('xyz', '`+emb+`') | project gettype(_embeddings), _embeddings[0]`, "array | 3")
	expectWrRows(t, eng, `datatable(P:string)['hi', 'fail'] | evaluate ai_chat_completion_prompt(P, '`+chat+`', dynamic({'ModelParameters': {'temperature': 0.5}}), true) | project P, P_completion, e = P_completion_error != ''`,
		"hi | model=gpt t=0.5 n=1 | false", "fail |  | true")
	expectWrRows(t, eng, `datatable(P:dynamic)[dynamic([{'role':'system','content':'s'},{'role':'user','content':'u'}])] | evaluate ai_chat_completion(P, '`+chat+`') | project P_completion`,
		"model=gpt t= n=2")
	expectWrRows(t, eng, `datatable(P:string)['hi', 'fail'] | evaluate ai_chat_completion_prompt(P, '`+chat+`', dynamic({'ReturnSuccessfulOnly': true})) | project P`, "hi")
	expectErrContains(t, eng, `evaluate ai_embeddings('x', 'https://example.com/v1/embeddings')`, "not allowed")
	// the engine's own embedder
	eng.SetEmbedder(embed.NewMock(4))
	expectWrRows(t, eng, `evaluate ai_embeddings('x', '') | project array_length(_embeddings)`, "4")
}
