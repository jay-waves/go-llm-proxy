package httputil

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS(t *testing.T) {
	origins := []string{"http://pdf.ts.localhost", "http://epub.ts.localhost/"}
	h := CORSMiddleware(func() []string { return origins }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	for _, tc := range []struct {
		name, origin, path, method, requestedMethod, headers, auth string
		status                                                     int
		cors                                                       bool
	}{
		{"pdf preflight", origins[0], "/v1/chat/completions", "OPTIONS", "POST", "authorization,content-type", "", 204, true},
		{"epub preflight", "http://epub.ts.localhost", "/v1/responses", "OPTIONS", "POST", "Authorization,Content-Type", "", 204, true},
		{"auth required", origins[0], "/v1/models", "GET", "", "", "", 401, true},
		{"authenticated request", origins[0], "/v1/models", "GET", "", "", "Bearer test-key", 200, true},
		{"foreign origin", "http://other.ts.localhost", "/v1/models", "OPTIONS", "GET", "", "", 403, false},
		{"origin suffix", origins[0] + ".evil.com", "/v1/models", "OPTIONS", "GET", "", "", 403, false},
		{"wrong scheme", "https://pdf.ts.localhost", "/v1/models", "OPTIONS", "GET", "", "", 403, false},
		{"wrong port", "http://pdf.ts.localhost:8080", "/v1/models", "OPTIONS", "GET", "", "", 403, false},
		{"no origin", "", "/v1/models", "GET", "", "", "Bearer test-key", 200, false},
		{"admin excluded", origins[0], "/admin/usage/data", "OPTIONS", "GET", "", "", 401, false},
		{"unsupported method", origins[0], "/v1/models", "OPTIONS", "DELETE", "", "", 403, true},
		{"unsupported header", origins[0], "/v1/models", "OPTIONS", "GET", "x-not-allowed", "", 403, true},
		{"document API", origins[0], "/layout-parsing", "OPTIONS", "POST", "content-type", "", 401, false},
		{"document restructure", origins[0], "/restructure-pages", "OPTIONS", "POST", "content-type", "", 401, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Access-Control-Request-Method", tc.requestedMethod)
			r.Header.Set("Access-Control-Request-Headers", tc.headers)
			r.Header.Set("Authorization", tc.auth)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d", w.Code, tc.status)
			}
			want := ""
			if tc.cors {
				want = tc.origin
			}
			if got := w.Header().Get("Access-Control-Allow-Origin"); got != want {
				t.Errorf("origin %q, want %q", got, want)
			}
			if w.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Error("cross-origin cookies must not be enabled")
			}
		})
	}
	// A reload revokes the old origin without recreating the HTTP handler.
	origins = []string{"http://epub.ts.localhost"}
	r := httptest.NewRequest("OPTIONS", "/v1/models", nil)
	r.Header.Set("Origin", "http://pdf.ts.localhost")
	r.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("updated allowlist not applied")
	}
}
