package usage

import (
	"bufio"
	"bytes"
	"crypto/sha256"

	"encoding/hex"
	"encoding/json"
	"fmt"

	"strings"

	"time"

	"go-llm-proxy/internal/config"

	_ "modernc.org/sqlite"
)

// UsageRecord holds the metrics for a single proxied request.
type UsageRecord struct {
	Timestamp     time.Time
	KeyHash       string // first 16 hex chars of SHA-256(key)
	KeyName       string
	Model         string
	AliasFrom     string // requested alias, empty when the canonical name was used
	Backend       string // backend URL that served the request (empty pre-pools)
	Endpoint      string
	StatusCode    int
	RequestBytes  int64
	ResponseBytes int64
	InputTokens   int
	OutputTokens  int
	TotalTokens   int
	DurationMS    int64
}

type DashboardData struct {
	Totals      DashboardTotals `json:"totals"`
	Daily       []DailyRow      `json:"daily"`
	DailyModels []DailyModelRow `json:"daily_models"`
	Users       []UserRow       `json:"users"`
	Models      []ModelRow      `json:"models"`
	Backends    []BackendRow    `json:"backends"`
}

type DashboardTotals struct {
	Requests    int     `json:"requests"`
	TotalTokens int64   `json:"total_tokens"`
	Users       int     `json:"users"`
	ErrorRate   float64 `json:"error_rate"`
}

type DailyRow struct {
	Date        string `json:"date"`
	Requests    int    `json:"requests"`
	TotalTokens int64  `json:"total_tokens"`
	Errors      int    `json:"errors"`
}

type DailyModelRow struct {
	Date        string `json:"date"`
	Model       string `json:"model"`
	Requests    int    `json:"requests"`
	TotalTokens int64  `json:"total_tokens"`
}

type UserRow struct {
	Name        string `json:"name"`
	KeyHash     string `json:"key_hash"`
	Requests    int    `json:"requests"`
	TotalTokens int64  `json:"total_tokens"`
	ActiveDays  int    `json:"active_days"`
	LastSeen    string `json:"last_seen"`
}

type ModelRow struct {
	Model       string  `json:"model"`
	Requests    int     `json:"requests"`
	Users       int     `json:"users"`
	TotalTokens int64   `json:"total_tokens"`
	AvgLatency  float64 `json:"avg_latency_ms"`
	Errors      int     `json:"errors"`
}

// BackendRow aggregates usage per (model, backend URL) — the load-balancer's
// distribution view. Rows exist only for records written after the backend
// column was introduced.
type BackendRow struct {
	Model       string  `json:"model"`
	Backend     string  `json:"backend"`
	Requests    int     `json:"requests"`
	TotalTokens int64   `json:"total_tokens"`
	AvgLatency  float64 `json:"avg_latency_ms"`
	Errors      int     `json:"errors"`
}

func (ul *UsageLogger) QueryDashboardData(days int) (*DashboardData, error) {
	if err := ul.Flush(); err != nil {
		return nil, err
	}
	fromDay, untilDay := usagePeriod(days)

	data := DashboardData{Daily: []DailyRow{}, DailyModels: []DailyModelRow{}, Users: []UserRow{}, Models: []ModelRow{}, Backends: []BackendRow{}}

	err := ul.readDB.QueryRow(`
		SELECT
			COALESCE(SUM(requests), 0),
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COUNT(DISTINCT key_hash),
			CAST(COALESCE(100.0 * SUM(errors) / NULLIF(COALESCE(SUM(requests), 0), 0), 0) AS REAL)
		FROM usage_daily
		WHERE day >= ? AND day < ?
	`, fromDay, untilDay).Scan(&data.Totals.Requests, &data.Totals.TotalTokens, &data.Totals.Users, &data.Totals.ErrorRate)
	if err != nil {
		return nil, fmt.Errorf("totals query: %w", err)
	}

	rows, err := ul.readDB.Query(`
		SELECT
			day,
			COALESCE(SUM(requests), 0)        AS requests,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			SUM(errors) AS errors
		FROM usage_daily
		WHERE day >= ? AND day < ?
		GROUP BY day
		ORDER BY day
	`, fromDay, untilDay)
	if err != nil {
		return nil, fmt.Errorf("daily query: %w", err)
	}
	for rows.Next() {
		var r DailyRow
		if err := rows.Scan(&r.Date, &r.Requests, &r.TotalTokens, &r.Errors); err != nil {
			rows.Close()
			return nil, err
		}
		data.Daily = append(data.Daily, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	userRows, err := ul.readDB.Query(`
		SELECT
			key_name,
			key_hash,
			COALESCE(SUM(requests), 0)        AS requests,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COUNT(DISTINCT day) AS active_days,
			MAX(last_seen)  AS last_seen
		FROM usage_daily
		WHERE day >= ? AND day < ?
		GROUP BY key_hash
		ORDER BY total_tokens DESC
	`, fromDay, untilDay)
	if err != nil {
		return nil, fmt.Errorf("users query: %w", err)
	}
	for userRows.Next() {
		var r UserRow
		var lastSeen string
		if err := userRows.Scan(&r.Name, &r.KeyHash, &r.Requests, &r.TotalTokens, &r.ActiveDays, &lastSeen); err != nil {
			userRows.Close()
			return nil, err
		}
		if r.Name == "" {
			r.Name = "(unnamed)"
		}
		if len(r.KeyHash) > 8 {
			r.KeyHash = r.KeyHash[:8]
		}
		if len(lastSeen) >= 10 {
			r.LastSeen = lastSeen[:10]
		} else {
			r.LastSeen = lastSeen
		}
		data.Users = append(data.Users, r)
	}
	if err := userRows.Err(); err != nil {
		userRows.Close()
		return nil, err
	}
	userRows.Close()

	modelRows, err := ul.readDB.Query(`
		SELECT
			model,
			COALESCE(SUM(requests), 0)        AS requests,
			COUNT(DISTINCT key_hash) AS unique_users,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			CAST(1.0 * SUM(duration_ms) / SUM(requests) AS REAL) AS avg_duration_ms,
			SUM(errors) AS errors
		FROM usage_daily
		WHERE day >= ? AND day < ?
		GROUP BY model
		ORDER BY total_tokens DESC
	`, fromDay, untilDay)
	if err != nil {
		return nil, fmt.Errorf("models query: %w", err)
	}
	for modelRows.Next() {
		var r ModelRow
		if err := modelRows.Scan(&r.Model, &r.Requests, &r.Users, &r.TotalTokens, &r.AvgLatency, &r.Errors); err != nil {
			modelRows.Close()
			return nil, err
		}
		data.Models = append(data.Models, r)
	}
	if err := modelRows.Err(); err != nil {
		modelRows.Close()
		return nil, err
	}
	modelRows.Close()

	backendRows, err := ul.readDB.Query(`
		SELECT
			model,
			backend,
			COALESCE(SUM(requests), 0)        AS requests,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			CAST(1.0 * SUM(duration_ms) / SUM(requests) AS REAL) AS avg_duration_ms,
			SUM(errors) AS errors
		FROM usage_daily
		WHERE day >= ? AND day < ? AND backend != ''
		GROUP BY model, backend
		ORDER BY model, total_tokens DESC
	`, fromDay, untilDay)
	if err != nil {
		return nil, fmt.Errorf("backends query: %w", err)
	}
	for backendRows.Next() {
		var r BackendRow
		if err := backendRows.Scan(&r.Model, &r.Backend, &r.Requests, &r.TotalTokens, &r.AvgLatency, &r.Errors); err != nil {
			backendRows.Close()
			return nil, err
		}
		data.Backends = append(data.Backends, r)
	}
	if err := backendRows.Err(); err != nil {
		backendRows.Close()
		return nil, err
	}
	backendRows.Close()

	dailyModelRows, err := ul.readDB.Query(`
		SELECT
			day,
			model,
			COALESCE(SUM(requests), 0)        AS requests,
			COALESCE(SUM(total_tokens), 0)
		FROM usage_daily
		WHERE day >= ? AND day < ?
		GROUP BY day, model
		ORDER BY day, model
	`, fromDay, untilDay)
	if err != nil {
		return nil, fmt.Errorf("daily models query: %w", err)
	}
	for dailyModelRows.Next() {
		var r DailyModelRow
		if err := dailyModelRows.Scan(&r.Date, &r.Model, &r.Requests, &r.TotalTokens); err != nil {
			dailyModelRows.Close()
			return nil, err
		}
		data.DailyModels = append(data.DailyModels, r)
	}
	if err := dailyModelRows.Err(); err != nil {
		dailyModelRows.Close()
		return nil, err
	}
	dailyModelRows.Close()

	return &data, nil
}

// KeyActivity summarizes recent traffic for one API key.
type KeyActivity struct {
	Requests    int    `json:"requests"`
	TotalTokens int64  `json:"total_tokens"`
	LastSeen    string `json:"last_seen"` // YYYY-MM-DD
}

// QueryKeyActivity returns per-key activity for the trailing window, keyed by
// key hash. The admin Users page joins this against the configured keys so
// stale keys are distinguishable from active ones.
func (ul *UsageLogger) QueryKeyActivity(days int) (map[string]KeyActivity, error) {
	if err := ul.Flush(); err != nil {
		return nil, err
	}
	fromDay, untilDay := usagePeriod(days)
	rows, err := ul.readDB.Query(`
		SELECT key_hash, COALESCE(SUM(requests), 0), COALESCE(SUM(total_tokens), 0), MAX(last_seen)
		FROM usage_daily
		WHERE day >= ? AND day < ?
		GROUP BY key_hash
	`, fromDay, untilDay)
	if err != nil {
		return nil, fmt.Errorf("key activity query: %w", err)
	}
	defer rows.Close()
	out := make(map[string]KeyActivity)
	for rows.Next() {
		var hash, lastSeen string
		var a KeyActivity
		if err := rows.Scan(&hash, &a.Requests, &a.TotalTokens, &lastSeen); err != nil {
			rows.Close()
			return nil, err
		}
		if len(lastSeen) >= 10 {
			a.LastSeen = lastSeen[:10]
		} else {
			a.LastSeen = lastSeen
		}
		out[hash] = a
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// HashKey returns the first 16 hex characters of the SHA-256 hash of a key.
// This is enough to uniquely identify keys without leaking the secret.
func HashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:8])
}

// --- Token extraction from upstream responses ---

// TokenUsage holds token counts extracted from an upstream response.
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// ExtractTokenUsage attempts to extract token counts from a response body.
// It handles both OpenAI and Anthropic response formats, as well as SSE
// streaming responses from both providers.
func ExtractTokenUsage(responseBody []byte, backendType string, isStreaming bool) TokenUsage {
	if isStreaming {
		return extractTokensFromSSE(responseBody, backendType)
	}
	return extractTokensFromJSON(responseBody, backendType)
}

// extractTokensFromJSON handles non-streaming responses.
func extractTokensFromJSON(body []byte, backendType string) TokenUsage {
	if backendType == config.BackendAnthropic {
		return extractAnthropicTokens(body)
	}
	return extractOpenAITokens(body)
}

// extractOpenAITokens parses the "usage" object from an OpenAI-format response.
//
//	{"usage": {"prompt_tokens": N, "completion_tokens": N, "total_tokens": N}}
func extractOpenAITokens(body []byte) TokenUsage {
	var resp struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return TokenUsage{}
	}
	return TokenUsage{
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
		TotalTokens:  resp.Usage.TotalTokens,
	}
}

// extractAnthropicTokens parses the "usage" object from an Anthropic-format response.
//
//	{"usage": {"input_tokens": N, "output_tokens": N, "cache_creation_input_tokens": N, "cache_read_input_tokens": N}}
func extractAnthropicTokens(body []byte) TokenUsage {
	var resp struct {
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return TokenUsage{}
	}
	total := resp.Usage.InputTokens + resp.Usage.OutputTokens +
		resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens
	return TokenUsage{
		InputTokens:  resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens,
		OutputTokens: resp.Usage.OutputTokens,
		TotalTokens:  total,
	}
}

// extractTokensFromSSE scans SSE data lines for token usage information.
// OpenAI puts usage in the final chunk; Anthropic sends it in message_delta
// and message_start events.
func extractTokensFromSSE(body []byte, backendType string) TokenUsage {
	if backendType == config.BackendAnthropic {
		return extractAnthropicSSETokens(body)
	}
	return extractOpenAISSETokens(body)
}

// extractOpenAISSETokens scans for the usage object in the final SSE chunk.
// OpenAI sends: data: {"usage":{"prompt_tokens":N,...},"choices":[]}
func extractOpenAISSETokens(body []byte) TokenUsage {
	var best TokenUsage
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]
		if data == "[DONE]" {
			continue
		}
		u := extractOpenAITokens([]byte(data))
		if u.TotalTokens > 0 {
			best = u
		}
	}
	return best
}

// extractAnthropicSSETokens scans Anthropic SSE events for token usage.
// message_start contains input_tokens; message_delta contains output_tokens.
func extractAnthropicSSETokens(body []byte) TokenUsage {
	var usage TokenUsage
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	var currentEvent string
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimSpace(line[7:])
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := []byte(line[6:])

		switch currentEvent {
		case "message_start":
			// {"type":"message_start","message":{"usage":{"input_tokens":N}}}
			var msg struct {
				Message struct {
					Usage struct {
						InputTokens              int `json:"input_tokens"`
						CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
						CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(data, &msg) == nil {
				usage.InputTokens = msg.Message.Usage.InputTokens +
					msg.Message.Usage.CacheCreationInputTokens +
					msg.Message.Usage.CacheReadInputTokens
			}

		case "message_delta":
			// {"type":"message_delta","usage":{"output_tokens":N}}
			var msg struct {
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(data, &msg) == nil {
				usage.OutputTokens = msg.Usage.OutputTokens
			}
		}
	}

	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	return usage
}
