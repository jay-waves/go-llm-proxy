package handler

import (
	"context"
	"net/http"
	"time"

	"go-llm-proxy/internal/config"
	"go-llm-proxy/internal/httputil"
)

// usesConnect reports whether the config involves an Eidonix connect hub: a
// connect: block, or any backend written as connect://.
func usesConnect(cfg *config.Config) bool {
	if cfg.Connect != nil {
		return true
	}
	any := func(backends []config.BackendConfig) bool {
		for _, b := range backends {
			if httputil.IsConnectURL(b.URL) {
				return true
			}
		}
		return false
	}
	for i := range cfg.Models {
		if any(cfg.EffectiveBackends(&cfg.Models[i])) {
			return true
		}
	}
	if a := cfg.Audio.Whisper; a != nil && any(a.Backends) {
		return true
	}
	if a := cfg.Audio.TTS; a != nil && any(a.Backends) {
		return true
	}

	return false
}

// ConnectData serves the read-only "Connected services" panel: the hub's
// catalog (site, online, service, models, healthy). It answers
// {"configured": false} when no connect hub is in use, so the panel stays
// hidden for deployments that don't use one.
func (h *AdminHandler) ConnectData(w http.ResponseWriter, r *http.Request) {
	cfg := h.cs.Get()
	if !usesConnect(cfg) {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	hubURL, application := cfg.ConnectSettings()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	cat, err := config.FetchHubCatalog(ctx, httputil.NewHTTPClient(), hubURL, application, "")
	out := map[string]any{"configured": true, "url": hubURL}
	if err != nil {
		out["error"] = err.Error()
	} else {
		sites := make([]map[string]any, 0, len(cat.Sites))
		for _, s := range cat.Sites {
			services := make([]map[string]any, 0, len(s.Services))
			for _, svc := range s.Services {
				models := svc.Models
				if models == nil {
					models = []string{}
				}
				services = append(services, map[string]any{
					"name": svc.Name, "kind": svc.Kind, "healthy": svc.Healthy, "models": models,
				})
			}
			sites = append(sites, map[string]any{"name": s.Name, "online": s.Online, "services": services})
		}
		out["sites"] = sites
	}
	writeJSON(w, http.StatusOK, out)
}
