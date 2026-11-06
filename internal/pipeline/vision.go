package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go-llm-proxy/internal/api"
	"go-llm-proxy/internal/config"
)

// imageCache stores image URL hash → description so that images are only
// processed once. Subsequent requests containing the same image reuse the
// cached description, making follow-up turns fast. Bounded to prevent
// unbounded memory growth in long-running processes.
var imageCache = newBoundedCache()

// ResetImageCache clears the image description cache. Exported for testing.
func ResetImageCache() {
	imageCache.Reset()
}

// maxImagesPerRequest caps the number of images the vision processor will handle
// in a single request. Beyond this, remaining images get a placeholder to prevent
// a single request from triggering unbounded outbound HTTP calls.
const maxImagesPerRequest = 10

// maxConcurrentVision limits how many concurrent vision model calls are made.
const maxConcurrentVision = 5

// imageFailureTTL is how long a failed image extraction is cached. Short
// enough to allow retry after transient upstream issues, long enough to
// prevent re-running the full cascade on every turn for the same image.
const imageFailureTTL = 5 * time.Minute

// Image prompts for ordinary images and text-heavy tool screenshots.
const (
	visionPromptDescribe = "Describe this image accurately and objectively. Include all visible subjects, objects, text, and relevant details. Be specific about what you observe."
	visionPromptText     = "Extract all text from this page. Reproduce the text content verbatim, preserving structure (headings, paragraphs, lists, tables). Focus on text content, not visual layout."
)

// processImages detects image content in the translated Chat Completions request,
// sends each image to the vision model for description, and replaces the image_url
// parts with text descriptions. Images are processed concurrently for speed, and
func (p *Pipeline) processImages(ctx context.Context, chatReq map[string]any,
	visionModels []*config.ModelConfig) (map[string]any, error) {

	var visionModel *config.ModelConfig
	if len(visionModels) > 0 {
		visionModel = visionModels[0]
	}
	// Normalize messages to []any — translation layers may produce []map[string]any.
	var messages []any
	switch m := chatReq["messages"].(type) {
	case []any:
		messages = m
	case []map[string]any:
		messages = make([]any, len(m))
		for i, msg := range m {
			messages[i] = msg
		}
		chatReq["messages"] = messages
	default:
		return chatReq, nil
	}

	// --- First pass: collect all images that need processing. ---
	//
	// Each image may produce up to two jobs: a vision (describe) job and an
	// OCR (text extraction) job. Cache keys use suffixes ":v" and ":o" to
	// store results independently.
	//
	// For tool-role images (screenshots, view_image output): text-oriented description.
	// For user-role images: ordinary visual description.
	type imageJob struct {
		url       string
		cacheKey  string // hash + ":v" or ":o"
		failKey   string // hash + ":fail" — TTL'd sentinel used to short-circuit retries
		prompt    string
		maxTokens int
		model     *config.ModelConfig
	}
	var jobs []imageJob
	seenKeys := map[string]bool{}

	imageCount := 0
	for _, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		content := normalizeContentParts(msgMap)
		if content == nil {
			continue
		}

		role, _ := msgMap["role"].(string)
		isToolRole := role == "tool"

		for _, part := range content {
			partMap, ok := part.(map[string]any)
			if !ok || partMap["type"] != "image_url" {
				continue
			}

			imageCount++
			if imageCount > maxImagesPerRequest {
				continue
			}

			url := extractImageURL(partMap)
			if url == "" {
				continue
			}

			// SSRF pre-flight: cheaply reject URLs whose hostname itself is
			// an obvious block-target (literal private IP, metadata name).
			// The authoritative enforcement is the safeHTTPClient dialer
			// used later in describeImage — it re-validates every resolved
			// IP at connect time, which closes the DNS-rebinding window.
			if !imageURLPreflight(url) {
				slog.Warn("blocked image URL targeting internal network", "url_prefix", url[:min(len(url), 60)])
				continue
			}

			hash := hashImageURL(url)

			if isToolRole {
				// Tool screenshots use the same vision cascade with a text-oriented prompt.
				ocrKey := hash + ":o"
				failKey := hash + ":fail"
				if _, ok := imageCache.Load(ocrKey); ok {
					continue
				}
				if _, ok := imageCache.Load(failKey); ok {
					// Recent failure still cached — skip until TTL expires.
					continue
				}
				if seenKeys[ocrKey] {
					continue
				}
				seenKeys[ocrKey] = true

				jobs = append(jobs, imageJob{
					url: url, cacheKey: ocrKey, failKey: failKey,
					prompt: visionPromptText, maxTokens: 2000, model: visionModel,
				})
			} else {
				// User-role images: vision description only.
				// OCR is skipped for user-attached photos — dedicated OCR models
				// hallucinate on natural images. Text in photos is captured
				// adequately by the vision model's description.
				vKey := hash + ":v"
				if _, ok := imageCache.Load(vKey); !ok && !seenKeys[vKey] {
					seenKeys[vKey] = true
					jobs = append(jobs, imageJob{
						url: url, cacheKey: vKey,
						prompt: visionPromptDescribe, maxTokens: 1000, model: visionModel,
					})
				}
			}
		}
	}

	// --- Process all uncached images concurrently. ---
	type jobResult struct {
		desc string
		err  error
	}
	results := make([]jobResult, len(jobs))

	if len(jobs) > 0 {
		var wg sync.WaitGroup
		sem := make(chan struct{}, maxConcurrentVision)

		for i, job := range jobs {
			wg.Add(1)
			go func(idx int, j imageJob) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				desc, err := p.describeImageAny(ctx, visionModels, j.url, j.prompt, j.maxTokens)
				// Cascade: if the primary attempt failed or came back empty,
				// and a fallback is configured, retry with the fallback.

				results[idx] = jobResult{desc: desc, err: err}
			}(i, job)
		}
		wg.Wait()

		// Cache successful results permanently; failures short-TTL.
		for i, r := range results {
			if r.err != nil || strings.TrimSpace(r.desc) == "" {
				if r.err != nil {
					slog.Warn("failed to process image",
						"model", jobs[i].model.Name, "cache_key", jobs[i].cacheKey, "error", r.err)
				}
				// Cache the failure briefly to prevent a cascade re-run on
				// every subsequent turn while the underlying upstream is
				// misbehaving. Only applies when a failKey was set (tool-role).
				if jobs[i].failKey != "" {
					imageCache.StoreWithTTL(jobs[i].failKey, "1", imageFailureTTL)
				}
			} else {
				imageCache.Store(jobs[i].cacheKey, r.desc)
			}
		}
	}

	// Build a lookup from cache key → result for jobs that just completed.
	jobDescriptions := map[string]string{}
	jobErrors := map[string]bool{}
	for i, r := range results {
		if r.err != nil {
			jobErrors[jobs[i].cacheKey] = true
		} else {
			jobDescriptions[jobs[i].cacheKey] = r.desc
		}
	}

	// --- Second pass: replace images with combined descriptions. ---
	imageCount = 0
	anyModified := false
	for i, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		content := normalizeContentParts(msgMap)
		if content == nil {
			continue
		}

		role, _ := msgMap["role"].(string)
		isToolRole := role == "tool"

		msgModified := false
		newContent := make([]any, 0, len(content))
		for _, part := range content {
			partMap, ok := part.(map[string]any)
			if !ok {
				newContent = append(newContent, part)
				continue
			}
			if partMap["type"] != "image_url" {
				newContent = append(newContent, part)
				continue
			}

			imageCount++
			if imageCount > maxImagesPerRequest {
				newContent = append(newContent, map[string]any{
					"type": "text",
					"text": "[Image omitted: too many images in request]",
				})
				msgModified = true
				continue
			}

			imageURL := extractImageURL(partMap)
			if imageURL == "" {
				newContent = append(newContent, map[string]any{
					"type": "text",
					"text": "[Image: unsupported format]",
				})
				msgModified = true
				continue
			}

			hash := hashImageURL(imageURL)
			replacement := buildImageReplacement(hash, isToolRole, imageCache, jobDescriptions)

			newContent = append(newContent, map[string]any{
				"type": "text",
				"text": replacement,
			})
			msgModified = true
		}

		if msgModified {
			msgMap["content"] = newContent
			messages[i] = msgMap
			anyModified = true
		}
	}

	if anyModified {
		chatReq["messages"] = messages
	}
	return chatReq, nil
}

// normalizeContentParts converts a message's content field to []any, handling
// both []any (from messages_translate) and []map[string]any (from responses_translate).
// Returns nil if content is not an array type. If the content was []map[string]any,
// it is also updated in the message map for downstream consistency.
func normalizeContentParts(msgMap map[string]any) []any {
	switch c := msgMap["content"].(type) {
	case []any:
		return c
	case []map[string]any:
		parts := make([]any, len(c))
		for i, p := range c {
			parts[i] = p
		}
		msgMap["content"] = parts
		return parts
	default:
		return nil
	}
}

// buildImageReplacement constructs the replacement text for a single image.
//
// For tool-role images (PDF pages, screenshots): OCR text only.
// For user-role images: vision description only.
//
// Output uses XML-like tags so target models clearly distinguish pipeline-
// injected content from user-authored text. Failures use plain bracketed
// strings — they're empty/informational, wrapping them in tags adds no value.
func buildImageReplacement(hash string, isToolRole bool, cache *boundedCache, jobDescs map[string]string) string {
	// Helper to look up a result from cache or fresh job results.
	lookup := func(cacheKey string) (string, bool) {
		if cached, ok := cache.Load(cacheKey); ok {
			return cached, true
		}
		if desc, ok := jobDescs[cacheKey]; ok {
			return desc, true
		}
		return "", false
	}

	if isToolRole {
		// Tool-role: OCR only.
		if ocrText, ok := lookup(hash + ":o"); ok {
			return fmt.Sprintf("<page_text>%s</page_text>", ocrText)
		}
		return "[Image could not be processed]"
	}

	// User-role: vision description only.
	if visionDesc, ok := lookup(hash + ":v"); ok {
		return fmt.Sprintf("<image_description>%s</image_description>", visionDesc)
	}
	return "[Image could not be processed]"
}

// hashImageURL returns a hex-encoded SHA-256 hash of the image URL (or data URL).
// This is used as the cache key for image descriptions.
func hashImageURL(imageURL string) string {
	h := sha256.Sum256([]byte(imageURL))
	return fmt.Sprintf("%x", h)
}

// extractImageURL gets the URL string from an image_url content part.
func extractImageURL(part map[string]any) string {
	iu, ok := part["image_url"].(map[string]any)
	if !ok {
		return ""
	}
	u, _ := iu["url"].(string)
	return u
}

// imageURLPreflight returns false for URLs that are obviously unsafe just
// from the raw string — wrong scheme, literal private IP, metadata hostname.
// Deliberately does NOT resolve DNS: the authoritative IP check happens at
// dial time inside safeHTTPClient, which closes the rebinding window.
func imageURLPreflight(imageURL string) bool {
	if strings.HasPrefix(imageURL, "data:") {
		return true
	}
	parsed, err := url.Parse(imageURL)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return preflightURLSafe(parsed)
}

// isPrivateIP returns true if the IP is in a private, loopback, link-local,
// unspecified, or cloud-metadata range that should not be reachable from
// user-supplied URLs. Normalizes IPv4-mapped IPv6 (::ffff:a.b.c.d) to its
// IPv4 form so attackers can't bypass the filter by using the v6-mapped
// encoding of a private v4 address.
func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	// Unspecified (0.0.0.0 / ::) is routed to the local host on most
	// platforms — treat as loopback.
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return true
	}
	// IsPrivate() covers RFC 1918 and ULA (fc00::/7). Explicit ranges below
	// cover AWS/GCP/Azure metadata endpoints (169.254.169.254 is inside
	// 169.254.0.0/16, which IsLinkLocalUnicast already catches) plus
	// carrier-grade NAT.
	for _, cidr := range extraPrivateCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

var extraPrivateCIDRs = func() []*net.IPNet {
	ranges := []string{
		"100.64.0.0/10", // carrier-grade NAT (RFC 6598)
		"::/128",        // unspecified (defense-in-depth; IsUnspecified covers this too)
	}
	out := make([]*net.IPNet, 0, len(ranges))
	for _, cidr := range ranges {
		if _, n, err := net.ParseCIDR(cidr); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// describeImageAny walks a model cascade in priority order, returning the
// first successful, non-empty description. Later entries only run when an
// earlier one errors or returns nothing.
func (p *Pipeline) describeImageAny(ctx context.Context, models []*config.ModelConfig,
	imageURL, prompt string, maxTokens int) (string, error) {
	var lastErr error
	for i, m := range models {
		if m == nil {
			continue
		}
		desc, err := p.describeImage(ctx, m, imageURL, prompt, maxTokens)
		if err == nil && strings.TrimSpace(desc) != "" {
			if i > 0 {
				slog.Info("vision cascade fallback succeeded", "model", m.Name, "position", i+1)
			}
			return desc, nil
		}
		if err == nil {
			err = fmt.Errorf("model %s returned empty description", m.Name)
		}
		lastErr = err
		if i < len(models)-1 {
			slog.Warn("vision cascade stage failed, trying next", "model", m.Name, "error", err)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no vision model available")
	}
	return "", lastErr
}

// describeImage sends an image to a vision-capable model and returns a text description.
// The prompt and maxTokens control the style of description (general vs OCR).
func (p *Pipeline) describeImage(ctx context.Context, visionModel *config.ModelConfig,
	imageURL, prompt string, maxTokens int) (string, error) {

	// Use a dedicated timeout instead of the caller's context. The caller's
	// context is tied to the client connection, which may be closed (e.g. Claude
	// Code retry) before the vision model finishes. A 60s timeout gives large
	// images enough time while still bounding the call.
	visionCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = ctx // original context intentionally unused

	start := time.Now()

	// Fetch http(s) images in-process via the SSRF-safe client and forward
	// the bytes inline as a data: URL. This eliminates DNS rebinding (the
	// upstream never resolves the remote hostname) and removes our
	// dependence on the vision model's own SSRF protection, if any.
	forwardedURL, err := fetchImageAsDataURL(visionCtx, imageURL)
	if err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}

	reqBody := map[string]any{
		"model": visionModel.Model,
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": prompt,
					},
					map[string]any{
						"type": "image_url",
						"image_url": map[string]any{
							"url": forwardedURL,
						},
					},
				},
			},
		},
		"max_completion_tokens": maxTokens,
		// Disable reasoning/thinking for vision utility calls — we want all
		// tokens spent on the description, not internal chain-of-thought.
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal vision request: %w", err)
	}

	url := visionModel.Backend + api.ChatCompletionsPath
	req, err := http.NewRequestWithContext(visionCtx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build vision request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if visionModel.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+visionModel.APIKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("vision model request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB limit
	if err != nil {
		return "", fmt.Errorf("read vision response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		slog.Error("vision model error", "status", resp.StatusCode, "body", string(respBody))
		return "", fmt.Errorf("vision model returned HTTP %d", resp.StatusCode)
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("parse vision response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("vision model returned empty response")
	}

	// Prefer the final content. Fall back to reasoning_content when the
	// backend is a reasoning model (e.g., Qwen3-VL variants) that put the
	// actual description into its thinking channel — commonly happens when
	// finish_reason=length truncates the response mid-reasoning before the
	// model emits a final answer. The reasoning text is still useful
	// extraction output from the model's perspective; surfacing it prevents
	// the cascade from failing unnecessarily.
	msg := chatResp.Choices[0].Message
	desc := msg.Content
	source := "content"
	if strings.TrimSpace(desc) == "" && strings.TrimSpace(msg.ReasoningContent) != "" {
		desc = msg.ReasoningContent
		source = "reasoning_content"
		slog.Debug("vision model emitted only reasoning_content; using it as description",
			"vision_model", visionModel.Name,
			"finish_reason", chatResp.Choices[0].FinishReason)
	}
	if strings.TrimSpace(desc) == "" {
		return "", fmt.Errorf("vision model returned empty response")
	}

	slog.Debug("image described by vision model",
		"vision_model", visionModel.Name,
		"duration", time.Since(start),
		"description_len", len(desc),
		"source", source)

	return desc, nil
}

// RequestContainsImageURLs checks if a translated Chat Completions request
// contains any image_url content parts. Handles both []any and []map[string]any
// message slice types (depending on which handler built the request).
func RequestContainsImageURLs(chatReq map[string]any) bool {
	// Try []any first (used by pipeline and responses handler).
	if msgs, ok := chatReq["messages"].([]any); ok {
		for _, msg := range msgs {
			if hasImageURLParts(msg) {
				return true
			}
		}
	}
	// Try []map[string]any (used by messages_translate).
	if msgs, ok := chatReq["messages"].([]map[string]any); ok {
		for _, msg := range msgs {
			if hasImageURLParts(msg) {
				return true
			}
		}
	}
	return false
}

// hasImageURLParts checks if a single message (as any) contains image_url content parts.
func hasImageURLParts(msg any) bool {
	m, ok := msg.(map[string]any)
	if !ok {
		return false
	}
	parts := normalizeContentParts(m)
	if parts == nil {
		return false
	}
	for _, part := range parts {
		p, ok := part.(map[string]any)
		if ok && p["type"] == "image_url" {
			return true
		}
	}
	return false
}
