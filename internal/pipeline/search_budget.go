package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
)

// MaxSearchesPerRequest caps the web searches the proxy runs for one client
// request, across every round of its search loop. A request's max_uses can
// lower it but not raise it: Claude Code sends 8, and a chat model given
// that many tends to spend them on near-duplicate rewordings of one query
// (every search is a paid provider call).
const MaxSearchesPerRequest = 5

// searchLimitText is the tool result for a search past the request's limit.
const searchLimitText = "Web search was not run: the search limit for this request was reached. Answer from what you already have."

type searchBudget struct {
	mu   sync.Mutex
	left int
}

func (b *searchBudget) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.left <= 0 {
		return false
	}
	b.left--
	return true
}

type searchBudgetKey struct{}

// WithSearchBudget returns ctx carrying the search budget of one client
// request: maxUses (a web_search tool's max_uses; 0 if none), capped at
// MaxSearchesPerRequest. Handlers call it once per request so every round
// of the search loop draws on the same budget.
func WithSearchBudget(ctx context.Context, maxUses int) context.Context {
	if maxUses <= 0 || maxUses > MaxSearchesPerRequest {
		maxUses = MaxSearchesPerRequest
	}
	return context.WithValue(ctx, searchBudgetKey{}, &searchBudget{left: maxUses})
}

// searchBudgetFrom is ctx's request budget, or a fresh one (one round's
// worth) when the handler didn't attach one.
func searchBudgetFrom(ctx context.Context) *searchBudget {
	if b, ok := ctx.Value(searchBudgetKey{}).(*searchBudget); ok {
		return b
	}
	return &searchBudget{left: MaxSearchesPerRequest}
}

// AnthropicWebSearchMaxUses is the max_uses of an Anthropic request's
// web_search_* server tool, or 0 when it has none.
func AnthropicWebSearchMaxUses(tools []json.RawMessage) int {
	for _, raw := range tools {
		var t struct {
			Type    string `json:"type"`
			MaxUses int    `json:"max_uses"`
		}
		if json.Unmarshal(raw, &t) == nil && strings.HasPrefix(t.Type, "web_search_") {
			return t.MaxUses
		}
	}
	return 0
}
