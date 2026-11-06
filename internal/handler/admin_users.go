package handler

import (
	"log/slog"
	"net/http"

	"go-llm-proxy/internal/config"
	"go-llm-proxy/internal/httputil"
	"go-llm-proxy/internal/usage"
)

// keyModelNames includes both chat and audio models available to manually
// managed API keys.
func keyModelNames(cfg *config.Config) []string {
	out := make([]string, 0, len(cfg.Models)+2)
	for _, m := range cfg.Models {
		out = append(out, m.Name)
	}
	if cfg.Audio.Whisper != nil {
		out = append(out, cfg.Audio.Whisper.Name)
	}
	if cfg.Audio.TTS != nil {
		out = append(out, cfg.Audio.TTS.Name)
	}
	return out
}

// UsersPage renders the /admin/users HTML page.
func (h *AdminHandler) UsersPage(w http.ResponseWriter, r *http.Request) {
	body := webAsset("templates/users.html")
	script := "users.js"
	h.renderShell(w, "users", "Admin · API Keys", body, script)
}

// UsersData serves the JSON payload the /admin/users page fetches on load.
func (h *AdminHandler) UsersData(w http.ResponseWriter, r *http.Request) {
	cfg := h.cs.Get()

	var activity map[string]usage.KeyActivity
	if h.ul != nil {
		activity, _ = h.ul.QueryKeyActivity(30) // best-effort; nil map on error
	}

	users := make([]map[string]any, 0, len(cfg.Keys))
	for _, k := range cfg.Keys {
		models := k.Models
		if models == nil {
			models = []string{}
		}
		hash := config.KeyHash(k.Key)
		entry := map[string]any{
			"name":     k.Name,
			"key_hash": hash,
			"masked":   config.MaskKey(k.Key),
			"models":   models,
		}

		if a, ok := activity[hash]; ok {
			entry["last_seen"] = a.LastSeen
			entry["requests_30d"] = a.Requests
			entry["tokens_30d"] = a.TotalTokens
		}
		users = append(users, entry)
	}

	allModels := keyModelNames(cfg)

	writeJSON(w, http.StatusOK, map[string]any{
		"users":      users,
		"all_models": allModels,
	})
}

// UsersMutate handles POST /admin/users/mutate.
func (h *AdminHandler) UsersMutate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action  string   `json:"action"`
		KeyHash string   `json:"key_hash"`
		Name    string   `json:"name"`
		Models  []string `json:"models"`
	}
	if err := decodeJSONBody(r, &req, 32*1024); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	switch req.Action {
	case "add":
		key, err := h.cs.AddKey(req.Name)
		if err != nil {
			writeMutateError(w, err)
			return
		}
		slog.Info("admin: api key added", "name", req.Name, "hash", config.KeyHash(key)[:8])
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"key":      key, // full key, shown once
			"key_hash": config.KeyHash(key),
			"masked":   config.MaskKey(key),
			"name":     req.Name,
		})
	case "update_models":
		if req.KeyHash == "" {
			httputil.WriteError(w, http.StatusBadRequest, "key_hash is required")
			return
		}
		if err := h.cs.UpdateKeyModels(req.KeyHash, req.Models); err != nil {
			writeMutateError(w, err)
			return
		}
		slog.Info("admin: api key models updated", "hash", req.KeyHash[:min(8, len(req.KeyHash))], "models", req.Models)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "rename":
		if req.KeyHash == "" {
			httputil.WriteError(w, http.StatusBadRequest, "key_hash is required")
			return
		}
		if err := h.cs.RenameKey(req.KeyHash, req.Name); err != nil {
			writeMutateError(w, err)
			return
		}
		slog.Info("admin: api key renamed", "hash", req.KeyHash[:min(8, len(req.KeyHash))], "new_name", req.Name)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "reveal":
		// Recovery path: return the full key for an existing entry. Keys are
		// stored in plaintext in the config, so this grants no privilege the
		// admin doesn't already hold — but each reveal is audit-logged.
		if req.KeyHash == "" {
			httputil.WriteError(w, http.StatusBadRequest, "key_hash is required")
			return
		}
		fullKey := config.LookupKeyByHash(h.cs.Get(), req.KeyHash)
		if fullKey == "" {
			httputil.WriteError(w, http.StatusNotFound, "key not found")
			return
		}
		slog.Info("admin: api key revealed", "hash", req.KeyHash[:min(8, len(req.KeyHash))])
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": fullKey})

	case "rotate":
		// Reissue path: replace the secret, keep name + model allowlist.
		// The old key stops working immediately.
		if req.KeyHash == "" {
			httputil.WriteError(w, http.StatusBadRequest, "key_hash is required")
			return
		}
		newKey, err := h.cs.RotateKey(req.KeyHash)
		if err != nil {
			writeMutateError(w, err)
			return
		}
		slog.Info("admin: api key rotated",
			"old_hash", req.KeyHash[:min(8, len(req.KeyHash))],
			"new_hash", config.KeyHash(newKey)[:8])
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"key":      newKey,
			"key_hash": config.KeyHash(newKey),
			"masked":   config.MaskKey(newKey),
		})

	case "delete":
		if req.KeyHash == "" {
			httputil.WriteError(w, http.StatusBadRequest, "key_hash is required")
			return
		}
		if err := h.cs.DeleteKey(req.KeyHash); err != nil {
			writeMutateError(w, err)
			return
		}
		slog.Info("admin: api key deleted", "hash", req.KeyHash[:min(8, len(req.KeyHash))])
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		httputil.WriteError(w, http.StatusBadRequest, "unknown action")
	}
}

// writeMutateError translates common mutator errors into appropriate HTTP
// statuses.
func writeMutateError(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case contains(msg, "not found"):
		httputil.WriteError(w, http.StatusNotFound, msg)
	case contains(msg, "already exists"), contains(msg, "last remaining"), contains(msg, "referenced by"):
		httputil.WriteError(w, http.StatusConflict, msg)
	case contains(msg, "invalid"), contains(msg, "required"), contains(msg, "unknown"), contains(msg, "too long"):
		httputil.WriteError(w, http.StatusBadRequest, msg)
	default:
		httputil.WriteError(w, http.StatusInternalServerError, msg)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	if len(sub) > len(s) {
		return -1
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
