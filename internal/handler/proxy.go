package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"go-llm-proxy/internal/api"
	"go-llm-proxy/internal/auth"
	"go-llm-proxy/internal/config"
	"go-llm-proxy/internal/httputil"
	"go-llm-proxy/internal/lb"
	"go-llm-proxy/internal/pipeline"
	"go-llm-proxy/internal/usage"
)

type ProxyHandler struct {
	config   *config.ConfigStore
	client   *http.Client
	usage    *usage.UsageLogger // nil if logging disabled
	pipeline *pipeline.Pipeline
}

func NewProxyHandler(cs *config.ConfigStore, usage *usage.UsageLogger, pipeline *pipeline.Pipeline) *ProxyHandler {
	return &ProxyHandler{
		config:   cs,
		usage:    usage,
		pipeline: pipeline,
		client:   httputil.NewHTTPClient(),
	}
}

// proxyRequestContext bundles the per-request metadata that flows through the
// proxy handler methods, avoiding 10+ parameter sprawl.
type proxyRequestContext struct {
	model       *config.ModelConfig
	modelName   string
	aliasFrom   string
	endpoint    string
	requestBody []byte
	keyName     string
	keyHash     string
	startTime   time.Time
}

func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	cleanPath := path.Clean(r.URL.Path)
	requireAnthropic := false
	if strings.HasPrefix(cleanPath, "/anthropic/") {
		cleanPath = strings.TrimPrefix(cleanPath, "/anthropic")
		requireAnthropic = true
	}

	if !AllowedPaths.MatchString(cleanPath) {
		httputil.WriteError(w, http.StatusNotFound, "unsupported endpoint")
		return
	}

	bodyLimit := p.config.Get().RequestBodyLimit()
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		logBodyLimit(r, err, bodyLimit)
		httputil.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	contentType := r.Header.Get("Content-Type")
	isMultipart := strings.HasPrefix(contentType, "multipart/form-data")

	var modelName string
	if isMultipart {
		modelName = ExtractModelFromMultipart(body, contentType)
	} else {
		modelName = ExtractModelFromJSON(body)
	}
	if modelName == "" {
		httputil.WriteError(w, http.StatusBadRequest, "missing model field in request")
		return
	}

	cfg := p.config.Get()
	requestedModel := modelName
	modelName = config.ResolveModelName(cfg, requestedModel)
	aliasFrom := ""
	if modelName != requestedModel {
		aliasFrom = requestedModel
		slog.Info("resolved model alias", "model", modelName, "alias_from", aliasFrom)
		if isMultipart {
			body = RewriteModelInMultipart(body, contentType, modelName)
		} else {
			body = RewriteModelName(body, modelName)
		}
	}

	key := auth.KeyFromContext(r.Context())
	if !auth.KeyAllowsModel(key, modelName) {
		httputil.WriteError(w, http.StatusForbidden, "not authorized for requested model")
		return
	}

	model := config.FindModel(cfg, modelName)
	if model == nil {
		RecordUnknownRequest(r, requestedModel, strings.TrimPrefix(cleanPath, "/v1/"))
		httputil.WriteError(w, http.StatusNotFound, "unknown model")
		return
	}

	if requireAnthropic && model.Type != config.BackendAnthropic {
		httputil.WriteError(w, http.StatusBadRequest, "model is not an anthropic backend")
		return
	}

	// Pick a backend for this request (sticky across a conversation's turns).
	// Everything below sees the chosen backend through the model view, so
	// search loops, probes, and pipeline sub-requests inherit it.
	r = withAffinity(r, body)
	model, releaseBackend := lb.ResolveModel(cfg, model, body)
	defer releaseBackend()

	if model.Model != modelName {
		if isMultipart {
			body = RewriteModelInMultipart(body, contentType, model.Model)
		} else {
			body = RewriteModelName(body, model.Model)
		}
	}

	// AWS Bedrock backends use the Converse API (translated from Chat
	// Completions, signed with SigV4 or a Bedrock API key). Dispatched here
	// because the wire protocol diverges entirely from the standard OpenAI-
	// compatible passthrough below. Implementation in proxy_bedrock.go.
	if model.Type == config.BackendBedrock {
		if cleanPath != "/v1/chat/completions" {
			httputil.WriteError(w, http.StatusBadRequest,
				"bedrock models only support /v1/chat/completions on this endpoint; use /v1/messages for the Anthropic protocol")
			return
		}
		if isMultipart {
			httputil.WriteError(w, http.StatusBadRequest, "multipart requests are not supported for bedrock backends")
			return
		}
		keyName := ""
		keyHash := ""
		if key != nil {
			keyName = key.Name
			keyHash = usage.HashKey(key.Key)
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(model.Timeout)*time.Second)
		defer cancel()
		p.handleBedrockChat(ctx, w, body, modelName, aliasFrom, model, keyName, keyHash, r.Header.Get("X-Request-ID"), time.Now())
		return
	}

	// Pipeline + search: parse body once for both if needed.
	isChatCompletions := cleanPath == "/v1/chat/completions" && !isMultipart
	var parsedChatReq map[string]any

	if p.pipeline != nil && isChatCompletions && p.pipeline.BodyNeedsProcessing(body) {
		if err := json.Unmarshal(body, &parsedChatReq); err != nil {
			slog.Warn("pipeline: failed to parse request body for processing", "error", err)
		} else {
			processed, pErr := p.pipeline.ProcessRequest(r.Context(), parsedChatReq, model)
			if pErr != nil {
				slog.Warn("pipeline: processing failed, sending original request", "error", pErr)
			} else {
				parsedChatReq = processed
				if newBody, mErr := json.Marshal(processed); mErr != nil {
					slog.Error("pipeline: failed to re-marshal processed request", "error", mErr)
				} else {
					body = newBody
				}
			}
		}
	}

	// Apply model's default sampling parameters to Chat Completions requests.
	if isChatCompletions && model.Defaults != nil {
		if parsedChatReq == nil {
			if err := json.Unmarshal(body, &parsedChatReq); err != nil {
				slog.Warn("failed to parse chat request for defaults", "error", err)
			}
		}
		if parsedChatReq != nil {
			model.ApplySamplingDefaults(parsedChatReq)
			if newBody, err := json.Marshal(parsedChatReq); err == nil {
				body = newBody
			}
		}
	}

	// Anthropic backends expose Messages rather than Chat Completions. The
	// dedicated bridge translates both request and response shapes while
	// preserving the Chat pipeline, model defaults, sticky backend selection,
	// failover, tools, and streaming semantics.
	if model.Type == config.BackendAnthropic && isChatCompletions {
		keyName, keyHash := "", ""
		if key != nil {
			keyName = key.Name
			keyHash = usage.HashKey(key.Key)
		}
		slog.Info("proxying chat completions request (anthropic translation)",
			"model", modelName, "key", keyName)
		p.handleAnthropicChat(w, r, body, modelName, aliasFrom, model, cfg, parsedChatReq,
			keyName, keyHash, time.Now())
		return
	}

	// Build the upstream URL.
	relPath := cleanPath
	if model.Type != config.BackendAnthropic {
		relPath = strings.TrimPrefix(cleanPath, "/v1")
	}
	upstreamURL := strings.TrimRight(model.Backend, "/") + relPath

	// model.Timeout bounds the wait for response headers and non-streaming
	// bodies; it is disarmed below once the response turns out to be a 2xx
	// event stream (see armUntilHeaders).
	ctx, cancel, disarmTimeout := armUntilHeaders(r.Context(), model)
	defer cancel()

	upReq, err := http.NewRequestWithContext(ctx, r.Method, upstreamURL, bytes.NewReader(body))
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to create upstream request")
		return
	}

	copyHeaders(upReq.Header, r.Header, model.Type)

	if model.APIKey != "" {
		if model.Type == config.BackendAnthropic {
			upReq.Header.Set("X-Api-Key", model.APIKey)
		} else {
			upReq.Header.Set("Authorization", "Bearer "+model.APIKey)
		}
	}

	keyName := ""
	keyHash := ""
	if key != nil {
		keyName = key.Name
		keyHash = usage.HashKey(key.Key)
	}
	slog.Info("proxying request", "model", modelName, "path", cleanPath, "key", keyName)

	startTime := time.Now()
	rc := proxyRequestContext{
		model: model, modelName: modelName, aliasFrom: aliasFrom, endpoint: cleanPath,
		requestBody: body, keyName: keyName, keyHash: keyHash, startTime: startTime,
	}

	resp, err := p.client.Do(upReq)

	// One-shot failover: a transport error or 5xx from the chosen backend —
	// before anything was written to the client — retries once on another
	// model replica. A cold prefix cache beats a 502.
	if err != nil && ctx.Err() == nil || err == nil && resp.StatusCode >= 500 {
		if alt, altRelease := lb.ResolveAlternate(cfg, model, model.Backend); alt != nil {
			defer altRelease()
			// The failed attempt is recorded here; with no alternate the
			// outcome is recorded once, below / by logUsage.
			lb.RecordAttempt(model.Backend, statusOf(resp), err, false)
			slog.Warn("failing over to alternate backend",
				"model", modelName, "failed", model.Backend, "alternate", alt.Backend, "error", err)
			if resp != nil {
				resp.Body.Close()
			}
			model = alt
			rc.model = alt
			altReq, altErr := http.NewRequestWithContext(ctx, r.Method, strings.TrimRight(model.Backend, "/")+relPath, bytes.NewReader(body))
			if altErr == nil {
				copyHeaders(altReq.Header, r.Header, model.Type)
				if model.APIKey != "" {
					if model.Type == config.BackendAnthropic {
						altReq.Header.Set("X-Api-Key", model.APIKey)
					} else {
						altReq.Header.Set("Authorization", "Bearer "+model.APIKey)
					}
				}
				resp, err = p.client.Do(altReq)
			}
		}
	}

	if err != nil {
		lb.RecordAttempt(model.Backend, 0, err, r.Context().Err() != nil)
		if ctx.Err() != nil {
			httputil.WriteError(w, http.StatusGatewayTimeout, "upstream request timed out")
			return
		}
		slog.Error("upstream request failed", "error", err, "model", modelName)
		httputil.WriteError(w, http.StatusBadGateway, "upstream request failed")
		return
	}
	defer resp.Body.Close()

	isStreaming := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	disarmIfStream(resp, disarmTimeout)

	copyResponseHeaders(w, resp)
	httputil.SetSecurityHeaders(w)

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, api.MaxResponseBodySize))
		slog.Error("upstream returned error", "model", modelName, "status", resp.StatusCode,
			"body", string(errBody))
		if model.Type == config.BackendAnthropic {
			httputil.WriteError(w, resp.StatusCode, fmt.Sprintf("backend returned HTTP %d", resp.StatusCode))
		} else {
			// Same cleaned message and OpenAI error code the Responses
			// endpoint returns, so clients see why a request was rejected.
			writeUpstreamError(w, classifyUpstreamError(resp.StatusCode, errBody, resp.Header))
		}

		logUsage(p.usage, usageLogInput{
			startTime: startTime, statusCode: resp.StatusCode,
			keyName: keyName, keyHash: keyHash,
			model: modelName, aliasFrom: aliasFrom, endpoint: cleanPath,
			backend:      model.Backend,
			requestBytes: int64(len(body)), responseBytes: int64(len(errBody)),
		})
		return
	}

	// For Chat Completions (without search), filter <think> tags from content.
	if isChatCompletions && isStreaming {
		usageData := streamChatWithThinkFilter(w, resp)
		logUsageFromChatResponse(p.usage, usageData, rc, 0)
		return
	}
	if isChatCompletions && !isStreaming {
		p.handleNonStreamingChatWithFilter(w, resp, rc)
		return
	}

	w.WriteHeader(resp.StatusCode)
	p.streamRawResponse(w, resp, rc, isStreaming)
}

// maxUsageCaptureBytes bounds how much of the upstream response body we
// keep in memory for token-usage extraction. Non-streaming responses carry
// usage near the top of the JSON; streaming responses carry the include_usage
// chunk near the end. Capturing the full body (up to MaxResponseBodySize =
// 100 MB) into a buffer per request doesn't scale under concurrency — a
// handful of parallel large streams can OOM the process.
//
// For non-streaming, 1 MB is well beyond any realistic LLM response (even
// 100K tokens of JSON is ~400 KB).
//
// For streaming, we capture both the prefix (first chunk often has model
// metadata) and the tail (final chunk has usage), which is handled below
// via a head+tail ring strategy.
const (
	maxUsageCaptureBytes   = 1 * 1024 * 1024
	streamTailCaptureBytes = 64 * 1024
)

// streamRawResponse streams the upstream response to the client without parsing.
func (p *ProxyHandler) streamRawResponse(w http.ResponseWriter, resp *http.Response,
	rc proxyRequestContext, isStreaming bool) {

	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 4096)
	var totalBytes int64
	capture := newCaptureBuffer(isStreaming)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			totalBytes += int64(n)
			if totalBytes > api.MaxResponseBodySize {
				slog.Error("upstream response exceeded size limit", "model", rc.modelName, "bytes", totalBytes)
				capture.discard()
				break
			}
			if p.usage != nil {
				capture.write(buf[:n])
			}
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				break
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			break
		}
	}

	if p.usage != nil {
		var tokens usage.TokenUsage
		if body := capture.bytes(); body != nil {
			tokens = usage.ExtractTokenUsage(body, rc.model.Type, isStreaming)
		}
		rec := usage.UsageRecord{
			Timestamp:     rc.startTime,
			KeyHash:       rc.keyHash,
			KeyName:       rc.keyName,
			Model:         rc.modelName,
			AliasFrom:     rc.aliasFrom,
			Endpoint:      rc.endpoint,
			StatusCode:    resp.StatusCode,
			RequestBytes:  int64(len(rc.requestBody)),
			ResponseBytes: totalBytes,
			InputTokens:   tokens.InputTokens,
			OutputTokens:  tokens.OutputTokens,
			TotalTokens:   tokens.TotalTokens,
			DurationMS:    time.Since(rc.startTime).Milliseconds(),
		}
		p.usage.Log(rec)
	}
}

// handleNonStreamingChatWithFilter handles non-streaming Chat Completions responses,
// filtering <think>...</think> tags from the content.
func (p *ProxyHandler) handleNonStreamingChatWithFilter(w http.ResponseWriter, resp *http.Response, rc proxyRequestContext) {
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, api.MaxResponseBodySize))
	if err != nil {
		httputil.WriteError(w, http.StatusBadGateway, "failed to read upstream response")
		return
	}

	var chatResp api.ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		// Can't parse - pass through unchanged.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		return
	}

	// Filter think tags from content.
	filterNonStreamingContent(&chatResp)

	finalBody, err := json.Marshal(chatResp)
	if err != nil {
		finalBody = respBody
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(finalBody)

	logUsageFromChatResponse(p.usage, chatResp.Usage, rc, int64(len(finalBody)))
}

// logUsageFromChatResponse logs usage from a Chat Completions response
// (streaming or non-streaming). Thin wrapper around logUsageChat that
// extracts fields from the per-request proxyRequestContext — saves 6 lines
// of boilerplate at each of the 4 call sites in this file.
func logUsageFromChatResponse(ul *usage.UsageLogger, usageData *api.ChunkUsage,
	rc proxyRequestContext, responseBytes int64) {
	logUsageChat(ul, usageLogInput{
		startTime:     rc.startTime,
		statusCode:    http.StatusOK,
		keyName:       rc.keyName,
		keyHash:       rc.keyHash,
		model:         rc.modelName,
		aliasFrom:     rc.aliasFrom,
		backend:       rc.model.Backend,
		endpoint:      rc.endpoint,
		requestBytes:  int64(len(rc.requestBody)),
		responseBytes: responseBytes,
	}, usageData)
}
