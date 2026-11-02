package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"go-llm-proxy/internal/config"
)

// jsonText is v as JSON text, for assertions and failure messages.
func jsonText(v any) string { return string(mustJSON(v)) }

// Codex and other Responses-API clients send top-level input_image items (no
// wrapping message) in multi-turn history. They join the user message right
// before them instead of being dropped.
func TestTranslateInputTopLevelImageMergesIntoUser(t *testing.T) {
	input := `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"describe this"}]},
		{"type":"input_image","image_url":"data:image/png;base64,AAAA","detail":"high"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"and this one"}]},
		{"type":"input_image","image_url":"https://example.com/b.jpg"}
	]`
	msgs, err := translateInput(json.RawMessage(input), "")
	if err != nil {
		t.Fatalf("translateInput: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 user messages, got %d: %s", len(msgs), jsonText(msgs))
	}
	for i, m := range msgs {
		if m["role"] != "user" {
			t.Fatalf("msg[%d] role = %v, want user", i, m["role"])
		}
	}
	b0 := jsonText(msgs[0]["content"])
	if !strings.Contains(b0, "describe this") || !strings.Contains(b0, "AAAA") || !strings.Contains(b0, `"detail":"high"`) {
		t.Fatalf("msg[0] content missing text or first image: %s", b0)
	}
	b1 := jsonText(msgs[1]["content"])
	if !strings.Contains(b1, "and this one") || !strings.Contains(b1, "https://example.com/b.jpg") {
		t.Fatalf("msg[1] content missing text or second image: %s", b1)
	}
}

// With no user message before it, a top-level image opens one.
func TestTranslateInputTopLevelImageOpensUserWhenNone(t *testing.T) {
	msgs, err := translateInput(json.RawMessage(`[{"type":"input_image","image_url":"https://example.com/c.png"}]`), "be brief")
	if err != nil {
		t.Fatalf("translateInput: %v", err)
	}
	if len(msgs) != 2 || msgs[0]["role"] != "system" || msgs[1]["role"] != "user" {
		t.Fatalf("expected system + user, got %s", jsonText(msgs))
	}
	if b := jsonText(msgs[1]["content"]); !strings.Contains(b, "https://example.com/c.png") {
		t.Fatalf("image url missing: %s", b)
	}
}

// A PDF data URL sent as an input_image goes to the PDF pipeline, like the
// same part inside a message.
func TestTranslateInputTopLevelPDFRoutesToPipeline(t *testing.T) {
	input := `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"read this"}]},
		{"type":"input_image","image_url":"data:application/pdf;base64,AAAA"}
	]`
	msgs, err := translateInput(json.RawMessage(input), "")
	if err != nil {
		t.Fatalf("translateInput: %v", err)
	}
	if b := jsonText(msgs); !strings.Contains(b, `"type":"pdf_data"`) || !strings.Contains(b, "read this") {
		t.Fatalf("pdf_data part or text missing: %s", b)
	}
}

// Merging into a plain-string user message keeps its text as the first part;
// top-level input_text and input_file are handled too.
func TestTranslateInputTopLevelPartsJoinStringUserMessage(t *testing.T) {
	input := `[
		{"type":"message","role":"user","content":"look"},
		{"type":"input_text","text":"more"},
		{"type":"input_file","filename":"a.pdf","file_data":"JVBERi0xLjQK"}
	]`
	msgs, err := translateInput(json.RawMessage(input), "")
	if err != nil {
		t.Fatalf("translateInput: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %s", jsonText(msgs))
	}
	parts, ok := msgs[0]["content"].([]map[string]any)
	if !ok || len(parts) != 3 {
		t.Fatalf("content = %s", jsonText(msgs[0]["content"]))
	}
	if parts[0]["text"] != "look" || parts[1]["text"] != "more" || parts[2]["type"] != "pdf_data" {
		t.Fatalf("parts = %s", jsonText(parts))
	}
}

// A top-level image never joins a developer message sent with role "user",
// nor a tool result: it opens its own user message.
func TestTranslateInputTopLevelImageDoesNotJoinDeveloperOrTool(t *testing.T) {
	input := `[
		{"type":"message","role":"user","content":"hi"},
		{"type":"message","role":"developer","content":"use tools"},
		{"type":"input_image","image_url":"https://example.com/d.png"},
		{"type":"function_call","call_id":"c1","name":"view_image","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"ok"},
		{"type":"input_image","image_url":"https://example.com/e.png"}
	]`
	msgs, err := translateInputWith(json.RawMessage(input), "", translateOptions{DeveloperRole: config.DeveloperRoleUser})
	if err != nil {
		t.Fatalf("translateInput: %v", err)
	}
	roles := []string{}
	for _, m := range msgs {
		roles = append(roles, m["role"].(string))
	}
	if strings.Join(roles, ",") != "user,user,user,assistant,tool,user" {
		t.Fatalf("roles = %v: %s", roles, jsonText(msgs))
	}
	if dev, _ := msgs[1]["content"].(string); !strings.HasPrefix(dev, developerOpen) {
		t.Fatalf("developer message changed: %s", jsonText(msgs[1]))
	}
	if b := jsonText(msgs[2]["content"]); !strings.Contains(b, "d.png") {
		t.Fatalf("first image: %s", b)
	}
	if b := jsonText(msgs[5]["content"]); !strings.Contains(b, "e.png") {
		t.Fatalf("second image: %s", b)
	}
}
