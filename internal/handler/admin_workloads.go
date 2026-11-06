package handler

import (
	"io"
	"net/http"
	"strings"
	"time"

	"go-llm-proxy/internal/config"
	"go-llm-proxy/internal/httputil"
)

func (h *AdminHandler) AudioPage(w http.ResponseWriter, r *http.Request) {
	body := webAsset("templates/audio.html")
	h.renderShell(w, "audio", "Admin · Audio", body, "audio.js")
}

func (h *AdminHandler) AudioData(w http.ResponseWriter, r *http.Request) {
	cfg := h.cs.Get()
	var health map[string]config.BackendHealth
	if h.health != nil {
		health = h.health.GetBackendStatus()
	}
	encode := func(a *config.AudioModelConfig) any {
		if a == nil {
			return nil
		}
		return map[string]any{"name": a.Name, "model": a.Model, "timeout": a.Timeout, "backends": backendData(a.Backends, health)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"whisper": encode(cfg.Audio.Whisper), "tts": encode(cfg.Audio.TTS)})
}

func (h *AdminHandler) AudioMutate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind     string            `json:"kind"`
		Enabled  bool              `json:"enabled"`
		Name     string            `json:"name"`
		Model    string            `json:"model"`
		Timeout  int               `json:"timeout"`
		Backends []backendInputDTO `json:"backends"`
	}
	if err := decodeJSONBody(r, &req, 128*1024); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Kind != "whisper" && req.Kind != "tts" {
		httputil.WriteError(w, http.StatusBadRequest, "kind must be whisper or tts")
		return
	}
	if !req.Enabled {
		if err := h.cs.UpdateAudioModel(req.Kind, nil); err != nil {
			writeMutateError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	var existing *config.AudioModelConfig
	if req.Kind == "whisper" {
		existing = h.cs.Get().Audio.Whisper
	} else {
		existing = h.cs.Get().Audio.TTS
	}
	var old []config.BackendConfig
	if existing != nil {
		old = existing.Backends
	}
	a := &config.AudioModelConfig{Name: req.Name, Model: req.Model, Timeout: req.Timeout, Backends: resolveBackendRows(old, req.Backends)}
	if err := h.cs.UpdateAudioModel(req.Kind, a); err != nil {
		writeMutateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// WorkloadProbe tests a processor's explicit health path. Unlike the chat
// probe it does not assume /models or any OpenAI response shape.
func (h *AdminHandler) WorkloadProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL, Path, Section string
		APIKey             *string `json:"api_key"`
	}
	if err := decodeJSONBody(r, &req, 16*1024); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := config.ValidateBackendURL(req.URL); err != nil || !strings.HasPrefix(req.Path, "/") {
		httputil.WriteError(w, http.StatusBadRequest, "valid url and absolute health path are required")
		return
	}
	key := ""
	if req.APIKey != nil {
		key = *req.APIKey
	} else {
		key = h.storedWorkloadKey(req.Section, req.URL)
	}
	client := httputil.NewHTTPClient()
	client.Timeout = 10 * time.Second
	if httputil.IsConnectURL(req.URL) {
		// Ask the hub's catalog rather than probing a path through it.
		up, reason := config.CheckConnectBackend(r.Context(), client, req.URL)
		out := map[string]any{"ok": up}
		if up {
			out["detail"] = "service is online and healthy at its site"
		} else {
			out["error"] = reason
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	baseURL := strings.TrimRight(req.URL, "/")
	if (req.Section == "whisper" || req.Section == "tts") && strings.HasSuffix(baseURL, "/v1") {
		baseURL = strings.TrimSuffix(baseURL, "/v1")
	}
	upstream, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, baseURL+req.Path, nil)
	if key != "" {
		upstream.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(upstream)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "status": resp.StatusCode, "detail": strings.TrimSpace(string(body))})
}

func (h *AdminHandler) storedWorkloadKey(section, target string) string {
	cfg := h.cs.Get()
	var backends []config.BackendConfig
	switch section {
	case "whisper":
		if cfg.Audio.Whisper != nil {
			backends = cfg.Audio.Whisper.Backends
		}
	case "tts":
		if cfg.Audio.TTS != nil {
			backends = cfg.Audio.TTS.Backends
		}
	}
	for _, b := range backends {
		if b.URL == target {
			return b.APIKey
		}
	}
	return ""
}
