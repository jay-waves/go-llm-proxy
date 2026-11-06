package handler

import (
	"log/slog"
	"net/http"

	"go-llm-proxy/internal/config"
	"go-llm-proxy/internal/httputil"
)

// ProcessorsPage preserves old bookmarks without maintaining a second editor.
func (h *AdminHandler) ProcessorsPage(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/chat?tab=helpers", http.StatusSeeOther)
}

// ProcessorsData serves GET /admin/processors/data.
func (h *AdminHandler) ProcessorsData(w http.ResponseWriter, r *http.Request) {
	cfg := h.cs.Get()
	all := make([]string, 0, len(cfg.Models))
	visionCapable := make([]string, 0)
	audioCapable := make([]string, 0)
	for _, m := range cfg.Models {
		all = append(all, m.Name)
		if m.SupportsVision {
			visionCapable = append(visionCapable, m.Name)
		}
		if m.SupportsAudio {
			audioCapable = append(audioCapable, m.Name)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"processors": map[string]any{
			"vision": cfg.Processors.Vision,
			"audio":  cfg.Processors.Audio,
		},
		"all_models":     all,
		"vision_capable": visionCapable,
		"audio_capable":  audioCapable,
	})
}

// ProcessorsMutate handles POST /admin/processors/mutate.
// Body: vision, audio and the optional ordered vision_models list.
func (h *AdminHandler) ProcessorsMutate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Vision       string    `json:"vision"`
		Audio        string    `json:"audio"`
		VisionModels *[]string `json:"vision_models"`
	}
	if err := decodeJSONBody(r, &req, 16*1024); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	cur := h.cs.Get().Processors
	p := config.ProcessorsConfig{
		Vision:       req.Vision,
		Audio:        req.Audio,
		VisionModels: cur.VisionModels,
	}

	if req.VisionModels != nil {
		p.VisionModels = *req.VisionModels
	}

	if err := h.cs.UpdateProcessors(p); err != nil {
		writeMutateError(w, err)
		return
	}
	slog.Info("admin: processors updated",
		"vision", p.Vision, "audio", p.Audio,
		"vision_cascade", len(p.VisionModels))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
