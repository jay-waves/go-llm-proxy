package handler

import (
	"bytes"
	"encoding/json"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"

	"go-llm-proxy/internal/config"
	"go-llm-proxy/internal/httputil"
)

// modelInfo is the public metadata exposed to the config page.
// It intentionally omits backend URLs, API keys, and other sensitive fields.
type modelInfo struct {
	ID             string `json:"id"`
	Local          bool   `json:"local"`
	Protocol       string `json:"protocol"`        // "openai" or "anthropic"
	Type           string `json:"type"`            // backend type: "openai", "anthropic", "bedrock"
	ContextWindow  int    `json:"context_window"`  // max tokens (0 = unknown)
	SupportsVision bool   `json:"supports_vision"` // model handles images natively
	SupportsAudio  bool   `json:"supports_audio"`  // model handles audio (transcription or audio input)
}

var privateRanges = []struct{ start, end net.IP }{
	{net.ParseIP("10.0.0.0").To4(), net.ParseIP("10.255.255.255").To4()},
	{net.ParseIP("172.16.0.0").To4(), net.ParseIP("172.31.255.255").To4()},
	{net.ParseIP("192.168.0.0").To4(), net.ParseIP("192.168.255.255").To4()},
	{net.ParseIP("127.0.0.0").To4(), net.ParseIP("127.255.255.255").To4()},
	{net.ParseIP("0.0.0.0").To4(), net.ParseIP("0.0.0.0").To4()},
}

func isPrivateIP(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}

	// Check IPv6 loopback and private ranges.
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}

	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	for _, r := range privateRanges {
		if ipInRange(ip4, r.start, r.end) {
			return true
		}
	}
	return false
}

func ipInRange(ip, lo, hi net.IP) bool {
	return bytes.Compare(ip, lo) >= 0 && bytes.Compare(ip, hi) <= 0
}

func modelInfoFromConfig(cfg *config.Config) []modelInfo {
	out := make([]modelInfo, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		u, _ := url.Parse(m.Backend)
		// A connect:// backend is hardware reached through an Eidonix
		// connector: our own infrastructure, like a private address.
		local := httputil.IsConnectURL(m.Backend)
		if !local && u != nil {
			host := u.Hostname()
			if host == "localhost" {
				local = true
			} else {
				local = isPrivateIP(u.Host)
			}
		}
		proto := "openai"
		if m.Type == config.BackendAnthropic {
			proto = "anthropic"
		}
		out = append(out, modelInfo{
			ID:             m.Name,
			Local:          local,
			Protocol:       proto,
			Type:           m.Type,
			ContextWindow:  m.ContextWindow,
			SupportsVision: m.SupportsVision,
			SupportsAudio:  m.SupportsAudio,
		})
	}
	return out
}

// ConfigPageHandler serves the config generator UI at GET /.
type ConfigPageHandler struct {
	config *config.ConfigStore
	health *config.HealthStore
	tmpl   *template.Template
}

func NewConfigPageHandler(cs *config.ConfigStore, health *config.HealthStore) *ConfigPageHandler {
	return &ConfigPageHandler{
		config: cs,
		health: health,
		tmpl:   template.Must(template.New("page").Parse(configPageHTML)),
	}
}

func (h *ConfigPageHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := h.config.Get()
	models := modelInfoFromConfig(cfg)
	health := h.health.GetStatus()

	// Create model status map for quick lookup.
	modelStatus := make(map[string]map[string]any, len(health))
	for name, s := range health {
		modelStatus[name] = map[string]any{
			"online":     s.Online,
			"last_check": s.LastCheck.Unix(),
			"error":      s.Error,
		}
	}

	data, err := json.Marshal(models)
	if err != nil {
		slog.Error("failed to marshal model info", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}

	healthData, err := json.Marshal(modelStatus)
	if err != nil {
		slog.Error("failed to marshal health status", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Pass processors config as separate JS variables.
	type tmplData struct {
		Models    template.JS
		Health    template.JS
		HasVision bool
	}
	td := tmplData{
		Models:    template.JS(data),
		Health:    template.JS(healthData),
		HasVision: len(cfg.Processors.EffectiveVisionModels()) > 0,
	}

	var buf bytes.Buffer
	if err := h.tmpl.Execute(&buf, td); err != nil {
		slog.Error("failed to render config page", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}

	httputil.SetSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", cspWeb)
	if _, err := buf.WriteTo(w); err != nil {
		slog.Error("failed to write config page response", "error", err)
	}
}

var configPageHTML = webAsset("templates/setup.html")
