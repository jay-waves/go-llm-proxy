package pipeline

import (
	"context"

	"encoding/json"

	"net/http"
	"net/http/httptest"
	"strings"

	"testing"

	"go-llm-proxy/internal/config"
)

// --- BodyNeedsProcessing tests ---

func TestBodyNeedsProcessing_ImageURL(t *testing.T) {
	p := &Pipeline{}
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}]}]}`)
	if !p.BodyNeedsProcessing(body) {
		t.Fatal("expected true for image_url")
	}
}

func TestBodyNeedsProcessing_PDF(t *testing.T) {
	p := &Pipeline{}
	body := []byte(`{"messages":[{"role":"user","content":"application/pdf"}]}`)
	if p.BodyNeedsProcessing(body) {
		t.Fatal("expected false for application/pdf")
	}
}

func TestBodyNeedsProcessing_PDFMagicBytes(t *testing.T) {
	p := &Pipeline{}
	body := []byte(`{"data":"JVBERi0xLjQ="}`)
	if p.BodyNeedsProcessing(body) {
		t.Fatal("expected false for PDF magic bytes")
	}
}

func TestBodyNeedsProcessing_TextOnly(t *testing.T) {
	p := &Pipeline{}
	body := []byte(`{"messages":[{"role":"user","content":"hello world"}]}`)
	if p.BodyNeedsProcessing(body) {
		t.Fatal("expected false for text-only request")
	}
}

func TestBodyNeedsProcessing_Document(t *testing.T) {
	p := &Pipeline{}
	body := []byte(`{"messages":[{"content":[{"type":"document"}]}]}`)
	if p.BodyNeedsProcessing(body) {
		t.Fatal("expected false for document type")
	}
}

// --- ShouldProcess tests ---

// An Anthropic-shaped backend is not necessarily real Claude (e.g. a
// third-party Anthropic-compatible API): auto-rewrite must key off the
// Vision capable flag, not the protocol. A non-vision-capable Anthropic
// model still needs the vision/document fallback.
func TestShouldProcess_AnthropicBackend_NotVisionCapable(t *testing.T) {
	p := &Pipeline{}
	m := &config.ModelConfig{Type: config.BackendAnthropic}
	if !p.ShouldProcess(m) {
		t.Fatal("expected true for a non-vision-capable anthropic backend (auto-rewrite keys off SupportsVision, not protocol)")
	}
}

// Native files do not activate any processing stage.
func TestShouldProcess_AnthropicBackend_VisionCapable(t *testing.T) {
	p := &Pipeline{config: config.NewTestConfigStore(&config.Config{})}
	m := &config.ModelConfig{Type: config.BackendAnthropic, SupportsVision: true}
	if p.ShouldProcess(m) {
		t.Fatal("no processing expected for a vision-capable model without search")
	}
}

func TestShouldProcess_AnthropicForcePipeline(t *testing.T) {
	p := &Pipeline{}
	m := &config.ModelConfig{Type: config.BackendAnthropic, ForcePipeline: true}
	if !p.ShouldProcess(m) {
		t.Fatal("expected true for anthropic with force_pipeline")
	}
}

func TestShouldProcess_OpenAIBackend(t *testing.T) {
	p := &Pipeline{}
	m := &config.ModelConfig{Type: config.BackendOpenAI}
	if !p.ShouldProcess(m) {
		t.Fatal("expected true for openai backend")
	}
}

func TestShouldProcess_DefaultBackend(t *testing.T) {
	p := &Pipeline{}
	m := &config.ModelConfig{}
	if !p.ShouldProcess(m) {
		t.Fatal("expected true for default backend type")
	}
}

// --- resolveVisionProcessor tests ---

func TestResolveVisionProcessor_GlobalDefault(t *testing.T) {
	cfg := &config.Config{
		Processors: config.ProcessorsConfig{Vision: "qwen-3.5"},
		Models:     []config.ModelConfig{{Name: "test", Backend: "http://localhost/v1"}},
	}
	p := &Pipeline{config: config.NewTestConfigStore(cfg)}
	model := &config.ModelConfig{Name: "test"}
	if got := p.resolveVisionProcessor(model); got != "qwen-3.5" {
		t.Fatalf("expected qwen-3.5, got %q", got)
	}
}

func TestResolveVisionProcessor_PerModelOverride(t *testing.T) {
	cfg := &config.Config{
		Processors: config.ProcessorsConfig{Vision: "qwen-3.5"},
	}
	p := &Pipeline{config: config.NewTestConfigStore(cfg)}
	model := &config.ModelConfig{
		Name:       "test",
		Processors: &config.ProcessorsConfig{Vision: "custom-vision"},
	}
	if got := p.resolveVisionProcessor(model); got != "custom-vision" {
		t.Fatalf("expected custom-vision, got %q", got)
	}
}

func TestResolveVisionProcessor_PerModelNone(t *testing.T) {
	cfg := &config.Config{
		Processors: config.ProcessorsConfig{Vision: "qwen-3.5"},
	}
	p := &Pipeline{config: config.NewTestConfigStore(cfg)}
	model := &config.ModelConfig{
		Name:       "test",
		Processors: &config.ProcessorsConfig{Vision: "none"},
	}
	if got := p.resolveVisionProcessor(model); got != "" {
		t.Fatalf("expected empty (disabled), got %q", got)
	}
}

func TestResolveVisionProcessor_NoConfig(t *testing.T) {
	cfg := &config.Config{}
	p := &Pipeline{config: config.NewTestConfigStore(cfg)}
	model := &config.ModelConfig{Name: "test"}
	if got := p.resolveVisionProcessor(model); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

// --- resolveOCRProcessor tests ---

// --- resolveWebSearchKey tests ---

// --- Vision processor tests ---

func TestProcessImages_NoImages(t *testing.T) {
	p := &Pipeline{}
	chatReq := map[string]any{
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "hello",
			},
		},
	}
	result, err := p.processImages(context.Background(), chatReq, []*config.ModelConfig{&config.ModelConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	msgs := result["messages"].([]any)
	m := msgs[0].(map[string]any)
	if m["content"] != "hello" {
		t.Fatal("content should be unchanged")
	}
}

func TestDescribeImage_FallsBackToReasoningContent(t *testing.T) {
	// Regression: reasoning-model vision backends (e.g., Qwen3-VL variants)
	// sometimes return content="" with the actual description in
	// reasoning_content, especially when finish_reason=length truncates
	// mid-thinking. The pipeline must surface reasoning_content instead
	// of treating the response as empty.
	ResetImageCache()
	defer ResetImageCache()

	visionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{
					"finish_reason": "length",
					"message": map[string]any{
						"content":           "",
						"reasoning_content": "A blue diagram showing a centerline mark and calibration axes",
					},
				},
			},
		})
	}))
	defer visionServer.Close()

	visionModel := &config.ModelConfig{Name: "vision", Backend: visionServer.URL, Model: "v"}
	p := &Pipeline{client: http.DefaultClient}
	chatReq := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": "data:image/png;base64,abc"},
					},
				},
			},
		},
	}
	result, err := p.processImages(context.Background(), chatReq, []*config.ModelConfig{visionModel})
	if err != nil {
		t.Fatal(err)
	}
	text := result["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "centerline mark") {
		t.Fatalf("expected reasoning_content to be used as description, got: %s", text)
	}
	if !strings.HasPrefix(text, "<image_description>") {
		t.Fatalf("expected XML-tag framing even for reasoning_content fallback, got: %s", text)
	}
}

func TestProcessImages_ReplacesImageWithDescription(t *testing.T) {
	ResetImageCache()
	// Set up a mock vision model backend.
	visionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{
					"message": map[string]any{
						"content": "A screenshot showing a terminal with Go code",
					},
				},
			},
		})
	}))
	defer visionServer.Close()

	visionModel := &config.ModelConfig{
		Name:    "vision",
		Backend: visionServer.URL,
		Model:   "vision",
	}

	p := &Pipeline{client: http.DefaultClient}
	chatReq := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "What is this?"},
					map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": "data:image/png;base64,abc123"},
					},
				},
			},
		},
	}

	result, err := p.processImages(context.Background(), chatReq, []*config.ModelConfig{visionModel})
	if err != nil {
		t.Fatal(err)
	}

	msgs := result["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(content))
	}

	// First part should be unchanged text.
	first := content[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "What is this?" {
		t.Fatalf("first part unexpected: %v", first)
	}

	// Second part should be the description replacing the image.
	second := content[1].(map[string]any)
	if second["type"] != "text" {
		t.Fatalf("expected text type, got %v", second["type"])
	}
	text := second["text"].(string)
	if text != "<image_description>A screenshot showing a terminal with Go code</image_description>" {
		t.Fatalf("unexpected description: %s", text)
	}
}

func TestProcessImages_CacheHit(t *testing.T) {
	ResetImageCache()

	callCount := 0
	visionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{
					"message": map[string]any{
						"content": "A German Shepherd dog sitting on grass",
					},
				},
			},
		})
	}))
	defer visionServer.Close()

	visionModel := &config.ModelConfig{
		Name:    "vision",
		Backend: visionServer.URL,
		Model:   "vision",
	}

	p := &Pipeline{client: http.DefaultClient}
	imageURL := "data:image/png;base64,uniqueTestImage123"
	makeChatReq := func() map[string]any {
		return map[string]any{
			"messages": []any{
				map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{
							"type":      "image_url",
							"image_url": map[string]any{"url": imageURL},
						},
					},
				},
			},
		}
	}

	// First call — should hit vision model.
	result, err := p.processImages(context.Background(), makeChatReq(), []*config.ModelConfig{visionModel})
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 vision call, got %d", callCount)
	}
	msgs := result["messages"].([]any)
	text := msgs[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if text != "<image_description>A German Shepherd dog sitting on grass</image_description>" {
		t.Fatalf("unexpected description: %s", text)
	}

	// Second call — should use cache, NOT call vision model again.
	result, err = p.processImages(context.Background(), makeChatReq(), []*config.ModelConfig{visionModel})
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 1 {
		t.Fatalf("expected still 1 vision call (cached), got %d", callCount)
	}
	msgs = result["messages"].([]any)
	text = msgs[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if text != "<image_description>A German Shepherd dog sitting on grass</image_description>" {
		t.Fatalf("unexpected cached description: %s", text)
	}
}

func TestProcessImages_VisionModelFailure(t *testing.T) {
	ResetImageCache()
	// Vision model returns 500.
	visionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer visionServer.Close()

	visionModel := &config.ModelConfig{
		Name:    "vision",
		Backend: visionServer.URL,
		Model:   "vision",
	}

	p := &Pipeline{client: http.DefaultClient}
	chatReq := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": "data:image/png;base64,abc"},
					},
				},
			},
		},
	}

	result, err := p.processImages(context.Background(), chatReq, []*config.ModelConfig{visionModel})
	if err != nil {
		t.Fatal(err)
	}

	// Should have a fallback text instead of the image.
	msgs := result["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if text != "[Image could not be processed]" {
		t.Fatalf("expected fallback text, got: %s", text)
	}
}

// --- Tool-role image cascade (OCR → vision) tests ---

// mockImageServer returns a handler that counts hits and produces either a
// text response, empty content, or an HTTP 500.
func mockImageServer(t *testing.T, behavior string, text string, hits *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		switch behavior {
		case "err":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"simulated"}`))
		case "empty":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{
					map[string]any{"message": map[string]any{"content": ""}},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{
					map[string]any{"message": map[string]any{"content": text}},
				},
			})
		}
	}))
}

func toolRoleReq() map[string]any {
	return map[string]any{
		"messages": []any{
			map[string]any{
				"role": "tool",
				"content": []any{
					map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": "data:image/png;base64,toolpage"},
					},
				},
			},
		},
	}
}

func TestImageCascade_ToolRole_VisionFailure_TTLShortCircuits(t *testing.T) {
	ResetImageCache()
	defer ResetImageCache()

	visionHits := 0
	visionSrv := mockImageServer(t, "err", "", &visionHits)
	defer visionSrv.Close()

	visionModel := &config.ModelConfig{Name: "vision-model", Backend: visionSrv.URL, Model: "vision"}

	p := &Pipeline{client: http.DefaultClient}
	// First call: both stages should be attempted.
	result, err := p.processImages(context.Background(), toolRoleReq(), []*config.ModelConfig{visionModel})
	if err != nil {
		t.Fatal(err)
	}
	if visionHits != 1 {
		t.Fatalf("expected cascade tried both (1+1), got vision=%d", visionHits)
	}
	content := result["messages"].([]any)[0].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if text != "[Image could not be processed]" {
		t.Fatalf("expected failure placeholder, got: %s", text)
	}

	// Second call within TTL: neither upstream should be re-invoked.
	_, err = p.processImages(context.Background(), toolRoleReq(), []*config.ModelConfig{visionModel})
	if err != nil {
		t.Fatal(err)
	}
	if visionHits != 1 {
		t.Fatalf("expected TTL short-circuit (still 1), got vision=%d", visionHits)
	}
}

func TestImageCascade_ToolRole_OnlyVision_NoDuplicateCall(t *testing.T) {
	ResetImageCache()
	defer ResetImageCache()

	// Only vision is configured; OCR resolves to the same model.
	hits := 0
	srv := mockImageServer(t, "err", "", &hits)
	defer srv.Close()
	visionModel := &config.ModelConfig{Name: "vision-model", Backend: srv.URL, Model: "vision"}

	p := &Pipeline{client: http.DefaultClient}
	_, err := p.processImages(context.Background(), toolRoleReq(), []*config.ModelConfig{visionModel})
	if err != nil {
		t.Fatal(err)
	}
	// Cascade should not fire a fallback to the same model — exactly one call.
	if hits != 1 {
		t.Fatalf("expected exactly 1 call (no duplicate), got %d", hits)
	}
}

func TestRequestContainsImageURLs(t *testing.T) {
	withImages := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "http://example.com/img.png"}},
				},
			},
		},
	}
	if !RequestContainsImageURLs(withImages) {
		t.Fatal("expected true")
	}

	withoutImages := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	if RequestContainsImageURLs(withoutImages) {
		t.Fatal("expected false")
	}
}

// --- Error formatting tests ---

func TestImageNotSupportedError(t *testing.T) {
	msg := imageNotSupportedError("MiniMax-M2.5", "400: model does not support image inputs")
	if msg == "" {
		t.Fatal("expected non-empty error message")
	}
	// Should mention the model name and have config guidance.
	for _, substr := range []string{"MiniMax-M2.5", "vision:", "Original error:"} {
		if !strings.Contains(msg, substr) {
			t.Fatalf("error message missing %q: %s", substr, msg)
		}
	}
}

// --- Web search tests ---

// A non-vision-capable Anthropic-shaped model (e.g. a third-party
// Anthropic-compatible API, not real Claude) must still get the vision
// fallback — protocol alone doesn't mean the backend can see images.
func TestPipeline_ProcessRequest_AnthropicNotVisionCapable_RewritesImages(t *testing.T) {
	visionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "A description"}}},
		})
	}))
	defer visionServer.Close()

	cfg := &config.Config{
		Processors: config.ProcessorsConfig{Vision: "vision-model"},
		Models: []config.ModelConfig{
			{Name: "test", Backend: "http://localhost/v1", Type: config.BackendAnthropic},
			{Name: "vision-model", Backend: visionServer.URL},
		},
	}
	p := NewPipeline(config.NewTestConfigStore(cfg), http.DefaultClient)
	model := &config.ModelConfig{Name: "test", Type: config.BackendAnthropic}

	chatReq := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,abc"}},
				},
			},
		},
	}

	result, err := p.ProcessRequest(context.Background(), chatReq, model)
	if err != nil {
		t.Fatal(err)
	}
	msgs := result["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	part := content[0].(map[string]any)
	if part["type"] != "text" {
		t.Fatalf("expected image_url to be rewritten to text for a non-vision-capable anthropic backend, got %q", part["type"])
	}
}

// A vision-capable Anthropic backend (real Claude) should get the raw image
// passed through untouched — it handles images natively.
func TestPipeline_ProcessRequest_AnthropicVisionCapable_PreservesImages(t *testing.T) {
	cfg := &config.Config{
		Processors: config.ProcessorsConfig{Vision: "vision-model"},
		Models: []config.ModelConfig{
			{Name: "test", Backend: "http://localhost/v1", Type: config.BackendAnthropic, SupportsVision: true},
			{Name: "vision-model", Backend: "http://localhost/v1"},
		},
	}
	p := NewPipeline(config.NewTestConfigStore(cfg), http.DefaultClient)
	model := &config.ModelConfig{Name: "test", Type: config.BackendAnthropic, SupportsVision: true}

	chatReq := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,abc"}},
				},
			},
		},
	}

	result, err := p.ProcessRequest(context.Background(), chatReq, model)
	if err != nil {
		t.Fatal(err)
	}
	msgs := result["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	part := content[0].(map[string]any)
	if part["type"] != "image_url" {
		t.Fatal("expected image_url to be preserved for a vision-capable anthropic backend")
	}
}

func TestPipeline_ProcessRequest_ForcePipeline(t *testing.T) {
	ResetImageCache()
	// Set up a mock vision model backend.
	visionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{
					"message": map[string]any{"content": "A code screenshot"},
				},
			},
		})
	}))
	defer visionServer.Close()

	cfg := &config.Config{
		Processors: config.ProcessorsConfig{Vision: "vision-model"},
		Models: []config.ModelConfig{
			{Name: "test", Backend: "http://localhost/v1", Type: config.BackendAnthropic, ForcePipeline: true},
			{Name: "vision-model", Backend: visionServer.URL, Model: "vision-model"},
		},
	}
	p := NewPipeline(config.NewTestConfigStore(cfg), http.DefaultClient)
	model := &cfg.Models[0]

	chatReq := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,abc"}},
				},
			},
		},
	}

	result, err := p.ProcessRequest(context.Background(), chatReq, model)
	if err != nil {
		t.Fatal(err)
	}
	// Should process despite Anthropic type because force_pipeline is true.
	msgs := result["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	part := content[0].(map[string]any)
	if part["type"] != "text" {
		t.Fatal("expected image to be replaced with text description")
	}
}

func TestPipeline_ProcessRequest_SupportsVisionSkipsProcessing(t *testing.T) {
	cfg := &config.Config{
		Processors: config.ProcessorsConfig{Vision: "vision-model"},
		Models: []config.ModelConfig{
			{Name: "test", Backend: "http://localhost/v1", SupportsVision: true},
			{Name: "vision-model", Backend: "http://localhost/v1"},
		},
	}
	p := NewPipeline(config.NewTestConfigStore(cfg), http.DefaultClient)
	model := &cfg.Models[0]

	chatReq := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,abc"}},
				},
			},
		},
	}

	result, err := p.ProcessRequest(context.Background(), chatReq, model)
	if err != nil {
		t.Fatal(err)
	}
	// Should NOT process — model supports vision natively.
	msgs := result["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	part := content[0].(map[string]any)
	if part["type"] != "image_url" {
		t.Fatal("expected image_url preserved for vision-capable model")
	}
}

func TestPipeline_ProcessRequest_CleansUpInternalFields(t *testing.T) {
	cfg := &config.Config{}
	p := NewPipeline(config.NewTestConfigStore(cfg), http.DefaultClient)
	model := &config.ModelConfig{Name: "test", Backend: "http://localhost/v1"}

	chatReq := map[string]any{
		"messages":               []any{map[string]any{"role": "user", "content": "hi"}},
		InternalKeyStrippedTools: []string{"web_search_20250305"},
	}

	result, err := p.ProcessRequest(context.Background(), chatReq, model)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := result[InternalKeyStrippedTools]; exists {
		t.Fatal("internal field should be cleaned up")
	}
}

// --- PDF vision fallback tests ---

// --- Cross-API PDF normalization tests (M4) ---

// --- PDF cascade tests (OCR → vision) ---

// scannedPDFStub is a PDF without extractable text, used to force Stage 2+.
const scannedPDFStub = "%PDF-1.0\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n3 0 obj<</Type/Page/MediaBox[0 0 612 792]/Parent 2 0 R>>endobj\nxref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000058 00000 n \n0000000115 00000 n \ntrailer<</Size 4/Root 1 0 R>>\nstartxref\n190\n%%EOF"

// mockProcessorServer returns a handler that counts hits and responds with
// the given behavior: "ok" = 200 with text, "empty" = 200 with empty content,
// "err" = 500 error.
func mockProcessorServer(t *testing.T, behavior string, text string, hits *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		switch behavior {
		case "err":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"simulated"}`))
		case "empty":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "r1", "model": "x",
				"choices": []map[string]any{{
					"index": 0, "finish_reason": "stop",
					"message": map[string]any{"role": "assistant", "content": ""},
				}},
			})
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "r1", "model": "x",
				"choices": []map[string]any{{
					"index": 0, "finish_reason": "stop",
					"message": map[string]any{"role": "assistant", "content": text},
				}},
			})
		}
	}))
}

func runCascade(t *testing.T, cfg *config.Config, pdfBase64 string) (map[string]any, error) {
	t.Helper()
	cs := config.NewTestConfigStore(cfg)
	p := NewPipeline(cs, http.DefaultClient)
	model := &cfg.Models[0]
	req := map[string]any{
		"model": "target",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "pdf_data", "data": pdfBase64, "filename": "test.pdf"},
				},
			},
		},
	}
	return p.ProcessRequest(context.Background(), req, model)
}
