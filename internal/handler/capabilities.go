package handler

import (
	"encoding/json"
	"go-llm-proxy/internal/auth"
	"go-llm-proxy/internal/config"
	"go-llm-proxy/internal/httputil"
	"net/http"
)

// CapabilitiesHandler serves GET /v1/capabilities: which of the proxy's
// services a key can use besides chat, and where, so a client can turn them
// on without keys of its own. Paths are from the proxy's root; a service
// that isn't configured is null.
//
//	"defaults": {"main": "GLM-5.3-Flash", …}, "models": ["GLM-5.3-Flash", …]}
//
// models are those marked code_link that the key may use, in config
// order; null when no model is marked (the client then decides).
// native_classifier_models are the models (the key may use) with
// native_classifier set, so a client can send Claude Code's auto-mode
// classifier requests to them natively.
type CapabilitiesHandler struct {
	config *config.ConfigStore
}

func NewCapabilitiesHandler(cs *config.ConfigStore) *CapabilitiesHandler {
	return &CapabilitiesHandler{config: cs}
}

func (h *CapabilitiesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	httputil.SetSecurityHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	cfg := h.config.Get()
	caps := map[string]any{"service": "go-llm-proxy", "defaults": map[string]any{}}
	if d := cfg.ClientDefaults; d != nil {
		caps["defaults"] = d
	}

	caps["models"] = nil
	native := []string{}
	key := auth.KeyFromContext(r.Context())
	marked := false
	offered := []string{}
	for _, m := range cfg.Models {
		if m.NativeClassifier && auth.KeyAllowsModel(key, m.Name) {
			native = append(native, m.Name)
		}
		if !m.CodeLink {
			continue
		}
		marked = true
		if auth.KeyAllowsModel(key, m.Name) {
			offered = append(offered, m.Name)
		}
	}
	if marked {
		caps["models"] = offered
	}
	caps["native_classifier_models"] = native
	json.NewEncoder(w).Encode(caps)
}
