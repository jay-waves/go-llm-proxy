package handler

import (
	"go-llm-proxy/internal/httputil"
	"log/slog"
	"net/http"
	"strconv"
)

// UsagePage shares the admin shell and session, even when logging is disabled.
func (h *AdminHandler) UsagePage(w http.ResponseWriter, r *http.Request) {
	if h.ul == nil {
		h.renderShell(w, "usage", "Overview", webAsset("templates/usage-disabled.html"), "")
		return
	}
	h.renderShell(w, "usage", "Overview", webAsset("templates/usage.html"), "usage.js")
}

func (h *AdminHandler) UsageData(w http.ResponseWriter, r *http.Request) {
	if h.ul == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "usage logging is disabled")
		return
	}
	days := 30
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v > 0 && v <= 365 {
		days = v
	}
	data, err := h.ul.QueryDashboardData(days)
	if err != nil {
		slog.Error("dashboard query failed", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, data)
}
