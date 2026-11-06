package handler

import (
	"bufio"

	"encoding/json"
	"fmt"

	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"go-llm-proxy/internal/api"
	"go-llm-proxy/internal/config"
	"go-llm-proxy/internal/httputil"
)

// msgToolCallState tracks a tool call during Anthropic Messages streaming.
type msgToolCallState struct {
	callID     string
	name       string
	blockIndex int
	args       strings.Builder
}

// messagesPingInterval is how long a translated stream may be silent before a
// ping event is sent. A variable so tests can shorten it.
var messagesPingInterval = 15 * time.Second

// maxSSELineSize bounds one upstream SSE line. Tool-call argument chunks and
// reasoning deltas can be large; 16 MiB is far above anything legitimate.
const maxSSELineSize = 16 * 1024 * 1024

func (h *MessagesHandler) handleStreaming(w http.ResponseWriter, resp *http.Response, req messagesRequest, model *config.ModelConfig, chatReq map[string]any, requestBytes int64, keyName, keyHash string, startTime time.Time, headersAlreadySent bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		httputil.WriteAnthropicError(w, http.StatusInternalServerError, "api_error", "streaming not supported")
		return
	}

	if !headersAlreadySent {
		httputil.SetSecurityHeaders(w)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
	}

	upstreamModel := req.Model
	msgID := api.RandomID("msg_")
	blockIndex := 0
	sawToolCalls := false
	ctx := resp.Request.Context()
	textBlockOpen := false
	reasoningBlockOpen := false
	var backendStopReason any
	var toolCalls []*msgToolCallState
	var usageData *api.ChunkUsage
	var finishReason string
	var responseBytes int64

	// All writes to w go through writeMu: a ping goroutine writes while the
	// translation loop is blocked reading the upstream.
	var writeMu sync.Mutex
	lastWrite := time.Now()
	writeRaw := func(format string, args ...any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		fmt.Fprintf(w, format, args...)
		flusher.Flush()
		lastWrite = time.Now()
	}
	emit := func(eventType string, data map[string]any) {
		data["type"] = eventType
		jsonData, _ := json.Marshal(data)
		writeRaw("event: %s\ndata: %s\n\n", eventType, jsonData)
	}

	emitMessageStart := func() {
		emit("message_start", map[string]any{
			"message": map[string]any{
				"id":            msgID,
				"type":          "message",
				"role":          "assistant",
				"content":       []any{},
				"model":         upstreamModel,
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage":         map[string]any{"input_tokens": 0, "output_tokens": 1},
			},
		})
		slog.Debug("emitted message_start", "model", upstreamModel)
	}

	// The upstream accepted the request (headers are in), so commit to the
	// stream now: message_start and a ping go out immediately, and pings
	// continue whenever the upstream is quiet (long prefill, slow reasoning)
	// so the client and any intermediary see a live connection.
	emitMessageStart()
	emit("ping", map[string]any{})
	pingDone := make(chan struct{})
	var pingWG sync.WaitGroup
	pingWG.Add(1)
	go func() {
		defer pingWG.Done()
		interval := messagesPingInterval
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-ticker.C:
				writeMu.Lock()
				idle := time.Since(lastWrite) >= interval
				writeMu.Unlock()
				if idle {
					emit("ping", map[string]any{})
				}
			}
		}
	}()
	stopPings := func() {
		select {
		case <-pingDone:
		default:
			close(pingDone)
		}
		pingWG.Wait()
	}
	defer stopPings()

	openTextBlock := func() {
		emit("content_block_start", map[string]any{
			"index":         blockIndex,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		slog.Debug("opened text block", "index", blockIndex)
		textBlockOpen = true
	}

	closeTextBlock := func() {
		if !textBlockOpen {
			return
		}
		emit("content_block_stop", map[string]any{"index": blockIndex})
		slog.Debug("closed text block", "index", blockIndex)
		blockIndex++
		textBlockOpen = false
	}

	openReasoningBlock := func() {
		emit("content_block_start", map[string]any{
			"index":         blockIndex,
			"content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""},
		})
		slog.Debug("opened thinking block", "index", blockIndex)
		reasoningBlockOpen = true
	}

	closeReasoningBlock := func() {
		if !reasoningBlockOpen {
			return
		}
		// Emit placeholder signature before closing.
		emit("content_block_delta", map[string]any{
			"index": blockIndex,
			"delta": map[string]any{"type": "signature_delta", "signature": "proxy-generated"},
		})
		emit("content_block_stop", map[string]any{"index": blockIndex})
		slog.Debug("closed thinking block", "index", blockIndex)
		blockIndex++
		reasoningBlockOpen = false
	}

	// Determine if search buffering is needed.

	// bufferedEvent stores tool call events that may need to be replayed or discarded.

	// Note: block closing after buffer replay uses emit() directly instead of
	// bufferOrEmit to avoid re-buffering stop events. See the replay sections below.

	// Think-tag filter: strips <think>...</think> from content and routes to reasoning.
	var thinkFilter thinkTagFilter

	// Read and translate the upstream SSE stream.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), maxSSELineSize)
	sawDone, sawChunk := false, false

	for scanner.Scan() {
		line := scanner.Text()
		responseBytes += int64(len(line)) + 1

		if responseBytes > api.MaxResponseBodySize {
			slog.Error("upstream streaming response exceeded size limit", "model", req.Model)
			break
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]
		if data == "[DONE]" {
			sawDone = true
			break
		}

		var chunk api.ChatChunk
		if json.Unmarshal([]byte(data), &chunk) != nil {
			slog.Debug("skipped unparseable upstream SSE chunk", "data", data)
			continue
		}

		sawChunk = true
		if chunk.Usage != nil {
			usageData = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		delta := choice.Delta

		// emitReasoningChunk emits a reasoning text segment as a thinking block delta.
		emitReasoningChunk := func(text string) {
			closeTextBlock() // reasoning after text gets its own block index
			if !reasoningBlockOpen {
				openReasoningBlock()
			}
			emit("content_block_delta", map[string]any{
				"index": blockIndex,
				"delta": map[string]any{"type": "thinking_delta", "thinking": text},
			})
		}

		// emitContentChunk emits a content text segment as a text block delta.
		emitContentChunk := func(text string) {
			if reasoningBlockOpen {
				closeReasoningBlock()
			}
			if !textBlockOpen {
				openTextBlock()
			}
			emit("content_block_delta", map[string]any{
				"index": blockIndex,
				"delta": map[string]any{"type": "text_delta", "text": text},
			})
		}

		// Reasoning tokens -> thinking block.
		// Supports both "reasoning" and "reasoning_content" JSON fields.
		if r := delta.EffectiveReasoning(); r != nil && *r != "" {
			emitReasoningChunk(*r)
		}

		// Content delta -> text block, with <think> tag filtering.
		if delta.Content != nil && *delta.Content != "" {
			for _, seg := range thinkFilter.Process(*delta.Content) {
				if seg.IsReasoning {
					emitReasoningChunk(seg.Text)
				} else {
					emitContentChunk(seg.Text)
				}
			}
		}

		// Tool call deltas.
		for _, tc := range delta.ToolCalls {
			if startsNewToolCall(toolCalls, tc) {
				// New tool call — close open blocks first.
				closeReasoningBlock()
				closeTextBlock()

				if len(toolCalls) == 0 {
				}
				sawToolCalls = true

				name := ""
				if tc.Function != nil {
					name = tc.Function.Name
				}
				callID := tc.ID
				if callID == "" {
					callID = api.RandomID("call_")
				}
				tcs := &msgToolCallState{
					callID:     callID,
					name:       name,
					blockIndex: blockIndex,
				}
				// Grow slice to accommodate index.
				for len(toolCalls) <= tc.Index {
					toolCalls = append(toolCalls, nil)
				}
				toolCalls[tc.Index] = tcs

				emit("content_block_start", map[string]any{
					"index": blockIndex,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    callID,
						"name":  name,
						"input": map[string]any{},
					},
				})
				slog.Debug("opened tool_use block", "index", blockIndex, "name", name)
				blockIndex++
			}

			if tc.Function != nil && tc.Function.Arguments != "" {
				if tc.Index < len(toolCalls) && toolCalls[tc.Index] != nil {
					tcs := toolCalls[tc.Index]
					tcs.args.WriteString(tc.Function.Arguments)
					emit("content_block_delta", map[string]any{
						"index": tcs.blockIndex,
						"delta": map[string]any{
							"type":         "input_json_delta",
							"partial_json": tc.Function.Arguments,
						},
					})
				}
			}
		}

		// Finish reason.
		if choice.FinishReason != nil {
			finishReason = *choice.FinishReason
		}
		if choice.StopReason != nil {
			backendStopReason = choice.StopReason
		}
	}
	scanErr := scanner.Err()

	// Flush any pending think-tag buffer before finalizing.
	for _, seg := range thinkFilter.Flush() {
		if seg.IsReasoning {
			closeTextBlock()
			if !reasoningBlockOpen {
				openReasoningBlock()
			}
			emit("content_block_delta", map[string]any{
				"index": blockIndex,
				"delta": map[string]any{"type": "thinking_delta", "thinking": seg.Text},
			})
		} else {
			if reasoningBlockOpen {
				closeReasoningBlock()
			}
			if !textBlockOpen {
				openTextBlock()
			}
			emit("content_block_delta", map[string]any{
				"index": blockIndex,
				"delta": map[string]any{"type": "text_delta", "text": seg.Text},
			})
		}
	}

	// Check for streaming search loop: if search is enabled, finish_reason is tool_calls,
	// and all tool calls are web_search, execute the search and re-stream from backend.
	{
		// Not a search case: replay any buffered events and close directly.
		closeReasoningBlock()
		closeTextBlock()
		for _, tc := range toolCalls {
			if tc != nil {
				emit("content_block_stop", map[string]any{"index": tc.blockIndex})
			}
		}
		toolCalls = nil // Already closed — prevent terminal double-close.
	}

	// A stream is complete only if the upstream said so: a finish_reason, with
	// no read error ([DONE] itself is optional once a finish_reason arrived). A cut connection, a scanner failure or an EOF with no
	// finish_reason is reported as a stream error (retryable), never as a
	// normal end_turn — the client would otherwise take a half-written answer
	// or half-written tool call as final.
	truncated := scanErr != nil || finishReason == ""
	if truncated && ctx.Err() == nil {
		stopPings()
		closeReasoningBlock()
		closeTextBlock()
		slog.Warn("upstream stream ended without completion",
			"model", req.Model, "scan_error", scanErr, "saw_done", sawDone, "saw_chunk", sawChunk,
			"finish_reason", finishReason, "response_bytes", responseBytes)
		writeRaw("event: error\ndata: %s\n\n", mustJSON(map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "overloaded_error",
				"message": "upstream stream ended unexpectedly before completion",
			},
		}))
	} else if ctx.Err() == nil {
		if finishReason == "" {
			finishReason = "stop"
		}

		// Close any open text block (idempotent — checks textBlockOpen).
		closeTextBlock()

		// Close tool call blocks from the re-stream path only.
		// Blocks from the original stream were already closed by closeAllBlocks/replay.
		for _, tc := range toolCalls {
			if tc != nil {
				emit("content_block_stop", map[string]any{"index": tc.blockIndex})
			}
		}

		stopReason, stopSequence := resolveStopReason(finishReason, backendStopReason, req.StopSequences, sawToolCalls)

		usage := anthropicUsage(usageData)
		if usage == nil {
			usage = anthropicUsage(&api.ChunkUsage{})
		}
		emit("message_delta", map[string]any{
			"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": stopSequence},
			"usage": usage,
		})

		emit("message_stop", map[string]any{})

		slog.Debug("stream complete", "stop_reason", stopReason, "blocks", blockIndex)
	}

	logUsageChat(h.usage, usageLogInput{
		startTime: startTime, statusCode: resp.StatusCode,
		keyName: keyName, keyHash: keyHash,
		model: req.Model, aliasFrom: req.aliasFrom, endpoint: "/v1/messages",
		backend:      model.Backend,
		requestBytes: requestBytes, responseBytes: responseBytes,
	}, usageData)
}

// startsNewToolCall reports whether a tool-call delta opens a new tool_use
// block. A delta starts one when it carries an id the call at that index does
// not already have (some servers repeat the id on every delta), or when it is
// the first delta seen for that index and names a function even though the id
// is missing.
func startsNewToolCall(calls []*msgToolCallState, tc api.ChunkToolCall) bool {
	var existing *msgToolCallState
	if tc.Index >= 0 && tc.Index < len(calls) {
		existing = calls[tc.Index]
	}
	if existing != nil {
		return false
	}
	if tc.ID != "" {
		return true
	}
	return tc.Function != nil && tc.Function.Name != ""
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
