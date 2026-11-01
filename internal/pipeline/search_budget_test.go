package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go-llm-proxy/internal/api"
	"go-llm-proxy/internal/config"
)

// fakeTavily answers every search request in-process. With barrier > 0 a
// search succeeds only once that many are in flight together, so a test
// using it passes only if searches run concurrently.
type fakeTavily struct {
	barrier int
	mu      sync.Mutex
	queries []string
	all     chan struct{}
}

func (f *fakeTavily) RoundTrip(r *http.Request) (*http.Response, error) {
	var body struct {
		Query string `json:"query"`
	}
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, &body)
	f.mu.Lock()
	f.queries = append(f.queries, body.Query)
	n := len(f.queries)
	if f.barrier > 0 && n == f.barrier {
		close(f.all)
	}
	f.mu.Unlock()
	status := http.StatusOK
	if f.barrier > 0 {
		select {
		case <-f.all:
		case <-time.After(3 * time.Second):
			status = http.StatusServiceUnavailable // ran one at a time
		}
	}
	out := fmt.Sprintf(`{"results":[{"title":"Result for %s","url":"https://example.com/%s","content":"snippet"}]}`, body.Query, body.Query)
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(out)), Header: http.Header{}, Request: r}, nil
}

func (f *fakeTavily) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.queries) }

func searchPipeline(f *fakeTavily) *Pipeline {
	cfg := &config.Config{Processors: config.ProcessorsConfig{WebSearchKey: "tvly-test"}}
	return &Pipeline{config: config.NewTestConfigStore(cfg), client: &http.Client{Transport: f}}
}

func searchCalls(queries ...string) []api.ChatChoiceToolCall {
	var out []api.ChatChoiceToolCall
	for i, q := range queries {
		tc := api.ChatChoiceToolCall{ID: fmt.Sprintf("call_%d_%s", i, q), Type: "function"}
		tc.Function.Name = "web_search"
		tc.Function.Arguments = `{"query":"` + q + `"}`
		out = append(out, tc)
	}
	return out
}

func searchResponse(queries ...string) *api.ChatResponse {
	return &api.ChatResponse{Choices: []api.ChatChoice{{
		Message:      api.ChatChoiceMsg{ToolCalls: searchCalls(queries...)},
		FinishReason: "tool_calls",
	}}}
}

func TestSearchBudgetSpansTheWholeLoop(t *testing.T) {
	f := &fakeTavily{}
	p := searchPipeline(f)
	model := &config.ModelConfig{Name: "test"}
	done := "done"
	rounds := 0
	var lastReq map[string]any
	send := func(req map[string]any) (*api.ChatResponse, error) {
		rounds++
		lastReq = req
		if rounds == 1 {
			return searchResponse("d", "e", "f"), nil
		}
		return &api.ChatResponse{Choices: []api.ChatChoice{{Message: api.ChatChoiceMsg{Content: &done}, FinishReason: "stop"}}}, nil
	}
	chatReq := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "q"}}}
	// Claude Code's WebSearch asks for 8; the proxy allows 5.
	ctx := WithSearchBudget(context.Background(), 8)
	if _, err := p.HandleNonStreamingSearchLoop(ctx, chatReq, model, searchResponse("a", "b", "c"), send, 5); err != nil {
		t.Fatal(err)
	}
	if got := f.count(); got != MaxSearchesPerRequest {
		t.Fatalf("searches = %d, want %d", got, MaxSearchesPerRequest)
	}
	msgs := lastReq["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["content"] != searchLimitText {
		t.Errorf("last tool result = %v", last["content"])
	}

	// A lower max_uses still applies.
	f = &fakeTavily{}
	p = searchPipeline(f)
	results := func() []SearchCallResult {
		_, r, _ := p.executeSearchCalls(WithSearchBudget(context.Background(), 2), p.ResolveSearchEntries(model), searchCalls("x", "y", "z"))
		return r
	}()
	if f.count() != 2 || results[2].Error != "max_uses_exceeded" {
		t.Errorf("max_uses 2: searches %d, third %+v", f.count(), results[2])
	}
}

func TestARoundsSearchesRunConcurrently(t *testing.T) {
	f := &fakeTavily{barrier: 3, all: make(chan struct{})}
	p := searchPipeline(f)
	model := &config.ModelConfig{Name: "test"}
	toolResults, results, _ := p.executeSearchCalls(WithSearchBudget(context.Background(), 0), p.ResolveSearchEntries(model), searchCalls("a", "b", "c"))
	if len(results) != 3 {
		t.Fatalf("results = %d", len(results))
	}
	for i, want := range []string{"a", "b", "c"} {
		if results[i].Error != "" {
			t.Fatalf("search %s failed: %s", want, results[i].Error)
		}
		// Results keep the model's call order whatever order searches finish in.
		if results[i].Query != want || len(results[i].Hits) != 1 || results[i].Hits[0].Title != "Result for "+want {
			t.Errorf("result %d = %+v", i, results[i])
		}
		if id := toolResults[i].(map[string]any)["tool_call_id"]; id != searchCalls("a", "b", "c")[i].ID {
			t.Errorf("tool result %d id = %v", i, id)
		}
	}
}

func TestAnthropicWebSearchMaxUses(t *testing.T) {
	tools := []json.RawMessage{
		json.RawMessage(`{"name":"Read","input_schema":{}}`),
		json.RawMessage(`{"type":"web_search_20250305","name":"web_search","max_uses":8}`),
	}
	if got := AnthropicWebSearchMaxUses(tools); got != 8 {
		t.Errorf("max_uses = %d", got)
	}
	if got := AnthropicWebSearchMaxUses(tools[:1]); got != 0 {
		t.Errorf("no search tool: %d", got)
	}
}
