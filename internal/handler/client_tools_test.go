package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-llm-proxy/internal/config"
	"go-llm-proxy/internal/pipeline"
)

// A client-owned function named web_search is an ordinary tool. Its calls
// must return to the client without execution or an extra upstream request.
func TestClientExecutedSearchToolIsForwarded(t *testing.T) {
	for _, endpoint := range []string{"chat", "responses", "messages"} {
		for _, stream := range []bool{false, true} {
			name := endpoint
			if stream {
				name += " stream"
			}
			t.Run(name, func(t *testing.T) {
				calls := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					body, _ := io.ReadAll(r.Body)
					if !strings.Contains(string(body), `"web_search"`) {
						t.Error("client tool definition was dropped")
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						io.WriteString(w, "data: {\"id\":\"chat-1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call-client\",\"type\":\"function\",\"function\":{\"name\":\"web_search\",\"arguments\":\"{\\\"query\\\":\\\"test\\\"}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat-1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						io.WriteString(w, `{"id":"chat-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-client","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"test\"}"}}]},"finish_reason":"tool_calls"}]}`)
					}
				}))
				defer upstream.Close()
				cs := config.NewTestConfigStore(&config.Config{Models: []config.ModelConfig{{Name: "test", Model: "upstream", Backend: upstream.URL + "/v1", Type: config.BackendOpenAI, SupportsVision: true, ResponsesMode: "translate"}}})
				pl := pipeline.NewPipeline(cs, http.DefaultClient)
				streamJSON := "false"
				if stream {
					streamJSON = "true"
				}
				var h http.Handler
				var path, body string
				switch endpoint {
				case "chat":
					h = NewProxyHandler(cs, nil, pl)
					path = "/v1/chat/completions"
					body = `{"model":"test","stream":` + streamJSON + `,"messages":[{"role":"user","content":"search"}],"tools":[{"type":"function","function":{"name":"web_search","parameters":{"type":"object"}}}]}`
				case "responses":
					h = NewResponsesHandler(cs, nil, pl)
					path = "/v1/responses"
					body = `{"model":"test","stream":` + streamJSON + `,"input":"search","tools":[{"type":"function","name":"web_search","parameters":{"type":"object"}}]}`
				case "messages":
					h = NewMessagesHandler(cs, nil, pl)
					path = "/v1/messages"
					body = `{"model":"test","stream":` + streamJSON + `,"max_tokens":100,"messages":[{"role":"user","content":"search"}],"tools":[{"name":"web_search","input_schema":{"type":"object"}}]}`
				}
				r := httptest.NewRequest("POST", path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 200 || !strings.Contains(w.Body.String(), "web_search") {
					t.Fatalf("tool not returned: %d %s", w.Code, w.Body.String())
				}
				if calls != 1 {
					t.Fatalf("proxy executed a tool continuation: %d upstream requests", calls)
				}
			})
		}
	}
}

func TestCapabilitiesDoNotAdvertiseRemovedServices(t *testing.T) {
	h := NewCapabilitiesHandler(config.NewTestConfigStore(&config.Config{Models: []config.ModelConfig{{Name: "model", CodeLink: true}}}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/capabilities", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	for _, removed := range []string{"web_search", "documents", "qdrant", "/mcp"} {
		if strings.Contains(w.Body.String(), removed) {
			t.Fatalf("capabilities still advertise %s", removed)
		}
	}
	if !strings.Contains(w.Body.String(), "model") {
		t.Fatal("model discovery was removed")
	}
}
