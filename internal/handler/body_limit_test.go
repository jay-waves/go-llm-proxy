package handler

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"go-llm-proxy/internal/auth"
	"go-llm-proxy/internal/config"
)

// The configured max_request_body_mb applies on every endpoint: a body over
// it is rejected with 413 before reaching the backend, one under it is
// proxied.
func TestRequestBodyLimitIsConfigurable(t *testing.T) {
	var reached atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		reached.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer backend.Close()
	cfg := &config.Config{
		MaxRequestBodyMB: 1,
		Models:           []config.ModelConfig{{Name: "m", Model: "m", Backend: backend.URL + "/v1", Timeout: 10}},
		Keys:             []config.KeyConfig{{Key: "k", Name: "k"}},
	}
	cs := config.NewTestConfigStore(cfg)
	body := func(size int) io.Reader {
		pad := strings.Repeat("x", size)
		return bytes.NewReader([]byte(`{"model":"m","messages":[{"role":"user","content":"` + pad + `"}]}`))
	}
	post := func(h http.Handler, path string, r io.Reader) int {
		req := httptest.NewRequest(http.MethodPost, path, r)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer k")
		w := httptest.NewRecorder()
		auth.AuthMiddleware(cs, h).ServeHTTP(w, req)
		return w.Code
	}
	for _, tt := range []struct {
		name, path string
		h          http.Handler
	}{
		{"chat", "/v1/chat/completions", NewProxyHandler(cs, nil, nil)},
		{"messages", "/v1/messages", NewMessagesHandler(cs, nil, nil)},
		{"responses", "/v1/responses", NewResponsesHandler(cs, nil, nil)},
		{"count_tokens", "/v1/messages/count_tokens", NewCountTokensHandler(cs, nil)},
	} {
		before := reached.Load()
		if code := post(tt.h, tt.path, body(2<<20)); code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: 2 MB body with a 1 MB limit: status %d", tt.name, code)
		}
		if reached.Load() != before {
			t.Errorf("%s: an oversized body reached the backend", tt.name)
		}
	}
	if code := post(NewProxyHandler(cs, nil, nil), "/v1/chat/completions", body(512<<10)); code != http.StatusOK {
		t.Errorf("512 KB body with a 1 MB limit: status %d", code)
	}
}
