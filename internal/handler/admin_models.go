package handler

import (
	"log/slog"
	"net/http"
	"sort"
	"time"

	"go-llm-proxy/internal/config"
	"go-llm-proxy/internal/httputil"
	"go-llm-proxy/internal/lb"
)

var awsRegions = []string{
	"us-east-1", "us-east-2", "us-west-1", "us-west-2",
	"ca-central-1", "sa-east-1", "eu-west-1", "eu-west-2", "eu-west-3",
	"eu-central-1", "eu-north-1", "eu-south-1", "ap-south-1",
	"ap-northeast-1", "ap-northeast-2", "ap-northeast-3", "ap-southeast-1",
	"ap-southeast-2", "ap-southeast-4", "me-south-1", "me-central-1", "af-south-1",
}

// ModelsPage is the Chat workload editor. /admin/models remains an alias so
// bookmarks from earlier releases continue to work.
func (h *AdminHandler) ModelsPage(w http.ResponseWriter, r *http.Request) {
	body := webAsset("templates/models.html")
	h.renderShell(w, "chat", "Admin · LLM", body, "models.js")
}

func (h *AdminHandler) ModelsData(w http.ResponseWriter, r *http.Request) {
	cfg := h.cs.Get()
	var modelHealth map[string]config.ModelHealth
	var backendHealth map[string]config.BackendHealth
	if h.health != nil {
		modelHealth = h.health.GetStatus()
		backendHealth = h.health.GetBackendStatus()
	}
	models := make([]map[string]any, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		backends := backendData(m.Backends, backendHealth)
		entry := map[string]any{
			"name": m.Name, "type": m.Type, "model": m.Model, "timeout": m.Timeout,
			"context_window": m.ContextWindow, "supports_vision": m.SupportsVision, "supports_audio": m.SupportsAudio, "code_link": m.CodeLink,
			"force_pipeline": m.ForcePipeline, "rewrite_vision": m.RewriteVision, "responses_mode": m.ResponsesMode,
			"messages_mode": m.MessagesMode, "backends": backends,
			"region": m.Region, "aws_access_key": m.AWSAccessKey,
			"has_aws_secret": m.AWSSecretKey != "", "aws_secret_mask": config.MaskSecret(m.AWSSecretKey),
			"has_aws_session": m.AWSSessionToken != "", "guardrail_id": m.GuardrailID,
			"guardrail_version": m.GuardrailVersion, "guardrail_trace": m.GuardrailTrace,
		}
		// Retain these response fields for older admin-page tests/clients. The
		// active editor uses the backends array and never writes model api_key.
		if len(m.Backends) > 0 {
			entry["backend"] = m.Backends[0].URL
			entry["has_api_key"] = m.Backends[0].APIKey != ""
			entry["api_key_masked"] = config.MaskSecret(m.Backends[0].APIKey)
		}
		if m.Defaults != nil {
			entry["defaults"] = samplingDefaultsToMap(m.Defaults)
		}
		if hs, ok := modelHealth[m.Name]; ok {
			entry["health"] = map[string]any{"online": hs.Online, "external": hs.External, "error": hs.Error}
		}
		models = append(models, entry)
	}
	aliasNames := make([]string, 0, len(cfg.Aliases))
	for alias := range cfg.Aliases {
		aliasNames = append(aliasNames, alias)
	}
	sort.Strings(aliasNames)
	aliases := make([]map[string]any, 0, len(aliasNames))
	for _, alias := range aliasNames {
		aliases = append(aliases, map[string]any{"alias": alias, "target": cfg.Aliases[alias]})
	}
	unknown := Snapshot()
	unknownModels := make([]map[string]any, 0, len(unknown))
	for _, entry := range unknown {
		unknownModels = append(unknownModels, map[string]any{
			"id": entry.ID, "count": entry.Count, "requester": entry.LastRequester, "key_hash": entry.LastKeyHash,
			"last_seen": entry.LastSeen.UTC().Format(time.RFC3339), "endpoint": entry.LastEndpoint,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models": models, "regions": awsRegions, "aliases": aliases, "unknown_models": unknownModels,
		"helpers": map[string]any{
			"vision":        cfg.Processors.Vision,
			"legacy_audio":  cfg.Processors.Audio,
			"vision_models": cfg.Processors.EffectiveVisionModels(),
			"auto_mode":     autoModeJSON(cfg.AutoMode),
		},
	})
}

func backendData(backends []config.BackendConfig, health map[string]config.BackendHealth) []map[string]any {
	out := make([]map[string]any, 0, len(backends))
	for _, b := range backends {
		live := lb.Status(b.URL)
		row := map[string]any{
			"url": b.URL, "has_api_key": b.APIKey != "", "api_key_mask": config.MaskSecret(b.APIKey),
			"weight": b.Weight, "max_inflight": b.MaxInflight, "disabled": b.Disabled,
			"inflight": live.Inflight, "breaker": live.Breaker,
		}
		if bh, ok := health[b.URL]; ok {
			row["online"] = bh.Online
			row["error"] = bh.Error
		}
		out = append(out, row)
	}
	return out
}

type backendInputDTO struct {
	URL         string  `json:"url"`
	APIKey      *string `json:"api_key"`
	Weight      int     `json:"weight"`
	MaxInflight int     `json:"max_inflight"`
	Disabled    bool    `json:"disabled"`
}

func resolveBackendRows(existing []config.BackendConfig, rows []backendInputDTO) []config.BackendConfig {
	keyForURL := func(target string) string {
		for _, b := range existing {
			if b.URL == target {
				return b.APIKey
			}
		}
		return ""
	}
	out := make([]config.BackendConfig, 0, len(rows))
	for _, row := range rows {
		b := config.BackendConfig{URL: row.URL, Weight: row.Weight, MaxInflight: row.MaxInflight, Disabled: row.Disabled}
		if row.APIKey != nil {
			b.APIKey = *row.APIKey
		} else {
			b.APIKey = keyForURL(row.URL)
		}
		out = append(out, b)
	}
	return out
}

// autoModeJSON describes the auto_mode block for the admin page.
func autoModeJSON(a *config.AutoModeConfig) map[string]any {
	if a == nil {
		return map[string]any{"model": "", "block_probability": 0}
	}
	return map[string]any{"model": a.Model, "block_probability": a.BlockProbability}
}

func (h *AdminHandler) ModelsMutate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action       string         `json:"action"`
		OriginalName string         `json:"original_name"`
		Name         string         `json:"name"`
		Alias        string         `json:"alias"`
		Target       string         `json:"target"`
		Force        bool           `json:"force"`
		Model        *modelInputDTO `json:"model"`
		// auto_mode action: an empty AutoModel switches interception off.
		AutoModel        string  `json:"auto_model"`
		BlockProbability float64 `json:"block_probability"`
	}
	if err := decodeJSONBody(r, &req, 128*1024); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch req.Action {
	case "add", "update":
		if req.Model == nil {
			httputil.WriteError(w, http.StatusBadRequest, "model is required")
			return
		}
		if req.Action == "update" && req.OriginalName == "" {
			httputil.WriteError(w, http.StatusBadRequest, "original_name is required")
			return
		}
		mc, err := req.Model.toConfig(h.cs.Get(), req.OriginalName)
		if err == nil {
			if req.Action == "add" {
				err = h.cs.AddModel(mc)
			} else {
				err = h.cs.UpdateModel(req.OriginalName, mc)
			}
		}
		if err != nil {
			writeMutateError(w, err)
			return
		}
		slog.Info("admin: chat model saved", "name", mc.Name, "backends", len(mc.Backends))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": mc.Name})
	case "delete":
		if err := h.cs.DeleteModel(req.Name, req.Force); err != nil {
			writeMutateError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "alias_add":
		if err := h.cs.AddAlias(req.Alias, req.Target); err != nil {
			writeMutateError(w, err)
			return
		}
		Remove(req.Alias)
		slog.Info("admin: model alias added", "alias", req.Alias, "target", req.Target)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "alias_update":
		if err := h.cs.UpdateAlias(req.Alias, req.Target); err != nil {
			writeMutateError(w, err)
			return
		}
		Remove(req.Alias)
		slog.Info("admin: model alias updated", "alias", req.Alias, "target", req.Target)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "alias_delete":
		if err := h.cs.DeleteAlias(req.Alias); err != nil {
			writeMutateError(w, err)
			return
		}
		slog.Info("admin: model alias deleted", "alias", req.Alias)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "auto_mode":
		if err := h.cs.UpdateAutoMode(req.AutoModel, req.BlockProbability); err != nil {
			writeMutateError(w, err)
			return
		}
		slog.Info("admin: auto mode updated", "model", req.AutoModel, "block_probability", req.BlockProbability)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		httputil.WriteError(w, http.StatusBadRequest, "unknown action")
	}
}

// BackendsProbe validates a candidate backend without saving it. If the key
// is omitted, it is recovered from the matching configured model/backend.
func (h *AdminHandler) BackendsProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL, Model, Type string
		UpstreamModel    string  `json:"upstream_model"`
		APIKey           *string `json:"api_key"`
	}
	if err := decodeJSONBody(r, &req, 16*1024); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := config.ValidateBackendURL(req.URL); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "url must be a valid http(s) or connect:// URL")
		return
	}
	key := ""
	if req.APIKey != nil {
		key = *req.APIKey
	} else if m := config.FindModel(h.cs.Get(), req.Model); m != nil {
		for _, b := range m.Backends {
			if b.URL == req.URL {
				key = b.APIKey
				break
			}
		}
	}
	detectModel := req.UpstreamModel
	if detectModel == "" {
		detectModel = req.Model
	}
	writeJSON(w, http.StatusOK, config.ProbeBackendForModel(req.URL, key, req.Type, detectModel))
}

type modelInputDTO struct {
	Name             string              `json:"name"`
	Backend          string              `json:"backend"` // legacy admin client
	Type             string              `json:"type"`
	Model            string              `json:"model"`
	ResponsesMode    *string             `json:"responses_mode"` // preserved when omitted for compatibility with older admin clients
	MessagesMode     *string             `json:"messages_mode"`  // preserved when omitted; the admin UI no longer surfaces this
	Timeout          int                 `json:"timeout"`
	ContextWindow    int                 `json:"context_window"`
	SupportsVision   bool                `json:"supports_vision"`
	CodeLink         bool                `json:"code_link"`
	SupportsAudio    *bool               `json:"supports_audio"` // legacy capability; hidden in the new UI
	ForcePipeline    bool                `json:"force_pipeline"`
	RewriteVision    string              `json:"rewrite_vision"`
	Backends         []backendInputDTO   `json:"backends"`
	APIKey           *string             `json:"api_key"` // legacy admin client
	Region           string              `json:"region"`
	AWSAccessKey     string              `json:"aws_access_key"`
	AWSSecretKey     *string             `json:"aws_secret_key"`
	AWSSession       *string             `json:"aws_session_token"`
	GuardrailID      string              `json:"guardrail_id"`
	GuardrailVersion string              `json:"guardrail_version"`
	GuardrailTrace   string              `json:"guardrail_trace"`
	Defaults         *samplingInputDTO   `json:"defaults"`
	Processors       *processorsInputDTO `json:"processors"`
}

type samplingInputDTO struct {
	Temperature      *float64 `json:"temperature"`
	TopP             *float64 `json:"top_p"`
	TopK             *int     `json:"top_k"`
	MaxNewTokens     *int     `json:"max_new_tokens"`
	FrequencyPenalty *float64 `json:"frequency_penalty"`
	PresencePenalty  *float64 `json:"presence_penalty"`
	ReasoningEffort  *string  `json:"reasoning_effort"`
	Stop             []string `json:"stop"`
}

type processorsInputDTO struct {
	Vision string `json:"vision"`
}

func (d *modelInputDTO) toConfig(cfg *config.Config, originalName string) (config.ModelConfig, error) {
	existing := config.FindModel(cfg, originalName)
	rows := d.Backends
	if len(rows) == 0 && d.Backend != "" { // compatibility with the old admin payload
		rows = []backendInputDTO{{URL: d.Backend, APIKey: d.APIKey, Weight: 1}}
	}
	var existingBackends []config.BackendConfig
	if existing != nil {
		existingBackends = existing.Backends
	}
	m := config.ModelConfig{
		Name: d.Name, Backends: resolveBackendRows(existingBackends, rows), Type: d.Type, Model: d.Model,
		Timeout: d.Timeout, ContextWindow: d.ContextWindow, SupportsVision: d.SupportsVision, CodeLink: d.CodeLink,
		ForcePipeline: d.ForcePipeline, RewriteVision: d.RewriteVision,
		Region: d.Region, AWSAccessKey: d.AWSAccessKey, GuardrailID: d.GuardrailID,
		GuardrailVersion: d.GuardrailVersion, GuardrailTrace: d.GuardrailTrace,
	}
	if d.SupportsAudio != nil {
		m.SupportsAudio = *d.SupportsAudio
	} else if existing != nil {
		m.SupportsAudio = existing.SupportsAudio
	}
	if existing != nil {
		// These Bedrock fields are deliberately absent from the compact editor;
		// preserve them unless a caller explicitly supplies replacements.
		if m.GuardrailID == "" {
			m.GuardrailID = existing.GuardrailID
		}
		if m.GuardrailVersion == "" {
			m.GuardrailVersion = existing.GuardrailVersion
		}
		if m.GuardrailTrace == "" {
			m.GuardrailTrace = existing.GuardrailTrace
		}
	}
	// Preserve advanced values omitted by older admin clients. The current UI
	// surfaces responses_mode but messages_mode remains config/API-only.
	if d.ResponsesMode != nil {
		m.ResponsesMode = *d.ResponsesMode
	} else if existing != nil {
		m.ResponsesMode = existing.ResponsesMode
	}
	if d.MessagesMode != nil {
		m.MessagesMode = *d.MessagesMode
	} else if existing != nil {
		m.MessagesMode = existing.MessagesMode
	}
	// developer_role is config-only (no admin UI field): keep it across edits.
	if existing != nil {
		m.NativeClassifier = existing.NativeClassifier
		m.DeveloperRole = existing.DeveloperRole
	}
	m.AWSSecretKey = resolveModelSecret(d.AWSSecretKey, existing, func(x *config.ModelConfig) string { return x.AWSSecretKey })
	m.AWSSessionToken = resolveModelSecret(d.AWSSession, existing, func(x *config.ModelConfig) string { return x.AWSSessionToken })
	if d.Defaults != nil {
		m.Defaults = &config.SamplingDefaults{Temperature: d.Defaults.Temperature, TopP: d.Defaults.TopP, TopK: d.Defaults.TopK, MaxNewTokens: d.Defaults.MaxNewTokens, FrequencyPenalty: d.Defaults.FrequencyPenalty, PresencePenalty: d.Defaults.PresencePenalty, ReasoningEffort: d.Defaults.ReasoningEffort, Stop: d.Defaults.Stop}
		if existing != nil && existing.Defaults != nil {
			m.Defaults.MaxNewTokens = existing.Defaults.MaxNewTokens
			m.Defaults.FrequencyPenalty = existing.Defaults.FrequencyPenalty
			m.Defaults.PresencePenalty = existing.Defaults.PresencePenalty
			m.Defaults.Stop = existing.Defaults.Stop
		}
	} else if existing != nil {
		m.Defaults = existing.Defaults
	}
	if d.Processors != nil { // legacy API compatibility; hidden in the new UI
		m.Processors = &config.ProcessorsConfig{Vision: d.Processors.Vision}
	} else if existing != nil {
		m.Processors = existing.Processors
	}
	return m, nil
}

func resolveModelSecret(in *string, existing *config.ModelConfig, get func(*config.ModelConfig) string) string {
	if in != nil {
		return *in
	}
	if existing != nil {
		return get(existing)
	}
	return ""
}

func samplingDefaultsToMap(d *config.SamplingDefaults) map[string]any {
	out := map[string]any{}
	if d.Temperature != nil {
		out["temperature"] = *d.Temperature
	}
	if d.TopP != nil {
		out["top_p"] = *d.TopP
	}
	if d.TopK != nil {
		out["top_k"] = *d.TopK
	}
	if d.MaxNewTokens != nil {
		out["max_new_tokens"] = *d.MaxNewTokens
	}
	if d.FrequencyPenalty != nil {
		out["frequency_penalty"] = *d.FrequencyPenalty
	}
	if d.PresencePenalty != nil {
		out["presence_penalty"] = *d.PresencePenalty
	}
	if d.ReasoningEffort != nil {
		out["reasoning_effort"] = *d.ReasoningEffort
	}
	if len(d.Stop) > 0 {
		out["stop"] = d.Stop
	}
	return out
}
