package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// translateInputImage preserves images, while treating PDF data URLs as files.
// No file bytes are decoded, fetched, rendered or inspected by the proxy.
func translateInputImage(p map[string]json.RawMessage) map[string]any {
	var url, filename string
	json.Unmarshal(p["image_url"], &url)
	json.Unmarshal(p["filename"], &filename)
	if strings.HasPrefix(url, "data:application/pdf;") {
		file := map[string]any{"file_data": url}
		if filename != "" {
			file["filename"] = filename
		}
		return map[string]any{"type": "file", "file": file}
	}
	image := map[string]any{"url": url}
	var detail string
	json.Unmarshal(p["detail"], &detail)
	if detail != "" {
		image["detail"] = detail
	}
	return map[string]any{"type": "image_url", "image_url": image}
}

func translateInputFile(p map[string]json.RawMessage) map[string]any {
	file := map[string]any{}
	for _, key := range []string{"file_data", "file_id", "filename"} {
		var value string
		if json.Unmarshal(p[key], &value) == nil && value != "" {
			file[key] = value
		}
	}
	return map[string]any{"type": "file", "file": file}
}

// Native Responses/Anthropic endpoints may support remote file URLs. Chat
// Completions has no equivalent document URL part. Reject that conversion
// explicitly rather than downloading the file or silently dropping it.
func validateChatFileURLs(raw json.RawMessage) error {
	if !bytes.Contains(raw, []byte(`"input_file"`)) && !bytes.Contains(raw, []byte(`"document"`)) {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	var visit func(any) error
	visit = func(value any) error {
		switch v := value.(type) {
		case []any:
			for _, item := range v {
				if err := visit(item); err != nil {
					return err
				}
			}
		case map[string]any:
			if v["type"] == "input_file" && v["file_url"] != nil && v["file_url"] != "" {
				return fmt.Errorf("file_url requires a native Responses backend; when translating to Chat Completions, use inline file_data, an upstream file_id, or application-extracted text")
			}
			if v["type"] == "document" {
				if source, ok := v["source"].(map[string]any); ok && source["type"] == "url" {
					return fmt.Errorf("document URLs require a native Anthropic backend; when translating to Chat Completions, use inline document data or application-extracted text")
				}
			}
			for _, item := range v {
				if err := visit(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(value)
}
