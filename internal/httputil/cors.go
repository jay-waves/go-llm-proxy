package httputil

import (
	"net/http"
	"strings"
)

const corsHeaders = "Authorization, Content-Type, X-API-Key, Anthropic-Version, Anthropic-Beta, OpenAI-Organization, OpenAI-Project"

// CORSMiddleware handles browser preflights before API authentication. Actual
// requests always reach the original handler, including its authentication.
// The callback reads the current config so origin changes apply on hot reload.
func CORSMiddleware(origins func() []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") && !strings.HasPrefix(r.URL.Path, "/anthropic/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		preflight := r.Method == http.MethodOptions && origin != "" && r.Header.Get("Access-Control-Request-Method") != ""
		if preflight {
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
		}
		allowed := false
		if origin != "" {
			for _, configured := range origins() {
				if origin == strings.TrimSuffix(configured, "/") {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			if preflight {
				WriteError(w, http.StatusForbidden, "CORS origin is not allowed")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		if preflight {
			method := r.Header.Get("Access-Control-Request-Method")
			if method != http.MethodGet && method != http.MethodPost {
				WriteError(w, http.StatusForbidden, "CORS method is not allowed")
				return
			}
			for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
				header = strings.TrimSpace(header)
				if header == "" {
					continue
				}
				found := false
				for _, permitted := range strings.Split(corsHeaders, ", ") {
					if strings.EqualFold(header, permitted) {
						found = true
						break
					}
				}
				if !found {
					WriteError(w, http.StatusForbidden, "CORS header is not allowed")
					return
				}
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
			w.Header().Set("Access-Control-Allow-Headers", corsHeaders)
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
