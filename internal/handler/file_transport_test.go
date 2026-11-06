package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-llm-proxy/internal/config"
	"go-llm-proxy/internal/pipeline"
)

func TestNativeFileInputsRemainOpaque(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, body, marker string
		responses                    bool
	}{
		{"chat inline", "/v1/chat/completions", `{"model":"files","messages":[{"role":"user","content":[{"type":"file","file":{"filename":"book.pdf","file_data":"data:application/pdf;base64,opaque-bytes"}}]}]}`, "opaque-bytes", false},
		{"responses inline", "/v1/responses", `{"model":"files","input":[{"role":"user","content":[{"type":"input_file","filename":"book.pdf","file_data":"data:application/pdf;base64,opaque-bytes"}]}]}`, "opaque-bytes", true},
		{"responses URL", "/v1/responses", `{"model":"files","input":[{"role":"user","content":[{"type":"input_file","file_url":"https://example.invalid/book.pdf"}]}]}`, "https://example.invalid/book.pdf", true},
		{"responses file ID", "/v1/responses", `{"model":"files","input":[{"role":"user","content":[{"type":"input_file","file_id":"file-upstream-123"}]}]}`, "file-upstream-123", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				data, _ := json.Marshal(body)
				received = string(data)
				w.Header().Set("Content-Type", "application/json")
				if tc.responses {
					w.Write([]byte(`{"id":"resp-test","object":"response","status":"completed","output":[]}`))
				} else {
					w.Write([]byte(`{"id":"chat-test","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
				}
			}))
			defer upstream.Close()
			cs := config.NewTestConfigStore(&config.Config{Models: []config.ModelConfig{{Name: "files", Model: "upstream-model", Backend: upstream.URL + "/v1", Type: config.BackendOpenAI, SupportsVision: true, ResponsesMode: "native"}}})
			pl := pipeline.NewPipeline(cs, http.DefaultClient)
			var h http.Handler = NewProxyHandler(cs, nil, pl)
			if tc.responses {
				h = NewResponsesHandler(cs, nil, pl)
			}
			r := httptest.NewRequest("POST", tc.endpoint, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(received, tc.marker) {
				t.Fatalf("file was changed or dropped: %s", received)
			}
			if strings.Contains(received, "pdf_data") || strings.Contains(received, "could not be read") {
				t.Fatal("internal file processing leaked upstream")
			}
		})
	}
}

func TestFileProtocolTranslation(t *testing.T) {
	for _, tc := range []struct{ key, value string }{{"file_data", "data:application/pdf;base64,opaque"}, {"file_id", "file-upstream-123"}} {
		p := map[string]json.RawMessage{"type": json.RawMessage(`"input_file"`), tc.key: json.RawMessage(`"` + tc.value + `"`)}
		part := translateInputFile(p)
		if part["type"] != "file" || part["file"].(map[string]any)[tc.key] != tc.value {
			t.Fatalf("file not preserved: %#v", part)
		}
	}
	part := translateDocumentParts(map[string]json.RawMessage{"type": json.RawMessage(`"document"`), "title": json.RawMessage(`"book.pdf"`), "source": json.RawMessage(`{"type":"base64","media_type":"application/pdf","data":"opaque"}`)})
	file := part[0].(map[string]any)["file"].(map[string]any)
	if file["file_data"] != "data:application/pdf;base64,opaque" || file["filename"] != "book.pdf" {
		t.Fatalf("Anthropic inline document not preserved: %#v", file)
	}
}

func TestTranslatedFileURLsReturnExplicitError(t *testing.T) {
	for _, raw := range []string{
		`[{"type":"input_file","file_url":"https://example.invalid/file.pdf"}]`,
		`{"role":"user","content":[{"type":"document","source":{"type":"url","url":"https://example.invalid/file.pdf"}}]}`,
	} {
		if err := validateChatFileURLs(json.RawMessage(raw)); err == nil {
			t.Fatalf("unsupported URL conversion accepted: %s", raw)
		}
	}
}
