package usage

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	usageQueueSize     = 1024
	usageBatchSize     = 256
	usageFlushInterval = 10 * time.Second
)

// UsageLogger persists daily counters. A single writer batches updates; reads
// enqueue a flush barrier so the dashboard includes all previously logged work.
type UsageLogger struct {
	db, readDB *sql.DB
	records    chan usageCommand
	queueMu    sync.RWMutex
	closed     bool
	worker     sync.WaitGroup
	closeOnce  sync.Once
	closeErr   error
}

type usageCommand struct {
	record UsageRecord
	flush  chan error
}

type dailyKey struct{ day, keyHash, model, backend string }

type dailyCounters struct {
	name, lastSeen                                                                  string
	requests, successful, errors                                                    int64
	requestBytes, responseBytes, inputTokens, outputTokens, totalTokens, durationMS int64
}

const dailySchema = `CREATE TABLE IF NOT EXISTS usage_daily (
 day TEXT NOT NULL,
 key_hash TEXT NOT NULL,
 model TEXT NOT NULL,
 backend TEXT NOT NULL DEFAULT '',
 key_name TEXT NOT NULL DEFAULT '',
 last_seen TEXT NOT NULL,
 requests INTEGER NOT NULL,
 successful INTEGER NOT NULL,
 errors INTEGER NOT NULL,
 request_bytes INTEGER NOT NULL,
 response_bytes INTEGER NOT NULL,
 input_tokens INTEGER NOT NULL,
 output_tokens INTEGER NOT NULL,
 total_tokens INTEGER NOT NULL,
 duration_ms INTEGER NOT NULL,
 PRIMARY KEY (day, key_hash, model, backend)
) WITHOUT ROWID;`

const dailyUpsert = `INSERT INTO usage_daily
 (day, key_hash, model, backend, key_name, last_seen, requests, successful, errors,
  request_bytes, response_bytes, input_tokens, output_tokens, total_tokens, duration_ms)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
 ON CONFLICT(day, key_hash, model, backend) DO UPDATE SET
 key_name = CASE WHEN excluded.last_seen >= last_seen THEN excluded.key_name ELSE key_name END,
 last_seen = MAX(last_seen, excluded.last_seen),
 requests = requests + excluded.requests,
 successful = successful + excluded.successful,
 errors = errors + excluded.errors,
 request_bytes = request_bytes + excluded.request_bytes,
 response_bytes = response_bytes + excluded.response_bytes,
 input_tokens = input_tokens + excluded.input_tokens,
 output_tokens = output_tokens + excluded.output_tokens,
 total_tokens = total_tokens + excluded.total_tokens,
 duration_ms = duration_ms + excluded.duration_ms`

func usageDSN(path string, readOnly bool) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	if readOnly {
		return path + separator + "mode=ro&_pragma=busy_timeout(5000)"
	}
	return path + separator + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
}

func NewUsageLogger(dbPath string) (*UsageLogger, error) {
	db, err := sql.Open("sqlite", usageDSN(dbPath, false))
	if err != nil {
		return nil, fmt.Errorf("opening usage db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(dailySchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating daily usage table: %w", err)
	}
	readDB, err := sql.Open("sqlite", usageDSN(dbPath, true))
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("opening usage read db: %w", err)
	}
	readDB.SetMaxOpenConns(1)
	ul := &UsageLogger{db: db, readDB: readDB, records: make(chan usageCommand, usageQueueSize)}
	ul.worker.Add(1)
	go ul.runWriter()
	return ul, nil
}

func (ul *UsageLogger) Log(rec UsageRecord) {
	ul.queueMu.RLock()
	defer ul.queueMu.RUnlock()
	if ul.closed {
		return
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now()
	}
	ul.records <- usageCommand{record: rec}
}

func (ul *UsageLogger) Flush() error {
	ul.queueMu.RLock()
	defer ul.queueMu.RUnlock()
	if ul.closed {
		return sql.ErrConnDone
	}
	done := make(chan error, 1)
	ul.records <- usageCommand{flush: done}
	return <-done
}

func (ul *UsageLogger) runWriter() {
	defer ul.worker.Done()
	ticker := time.NewTicker(usageFlushInterval)
	defer ticker.Stop()
	pending := make(map[dailyKey]*dailyCounters)
	count := 0
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		if err := ul.writeDailyBatch(pending); err != nil {
			slog.Error("failed to persist daily usage", "error", err)
			return err
		}
		clear(pending)
		count = 0
		return nil
	}
	for {
		select {
		case command, ok := <-ul.records:
			if !ok {
				ul.closeErr = flush()
				return
			}
			if command.flush != nil {
				command.flush <- flush()
				continue
			}
			rec := command.record
			key := dailyKey{rec.Timestamp.UTC().Format("2006-01-02"), rec.KeyHash, rec.Model, rec.Backend}
			value := pending[key]
			if value == nil {
				value = &dailyCounters{}
				pending[key] = value
			}
			stamp := rec.Timestamp.UTC().Format(time.RFC3339)
			if stamp >= value.lastSeen {
				value.name = rec.KeyName
				value.lastSeen = stamp
			}
			value.requests++
			if rec.StatusCode >= 200 && rec.StatusCode < 300 {
				value.successful++
			}
			if rec.StatusCode >= 400 {
				value.errors++
			}
			value.requestBytes += rec.RequestBytes
			value.responseBytes += rec.ResponseBytes
			value.inputTokens += int64(rec.InputTokens)
			value.outputTokens += int64(rec.OutputTokens)
			value.totalTokens += int64(rec.TotalTokens)
			value.durationMS += rec.DurationMS
			count++
			if count >= usageBatchSize {
				_ = flush()
			}
		case <-ticker.C:
			_ = flush()
		}
	}
}

func (ul *UsageLogger) writeDailyBatch(pending map[dailyKey]*dailyCounters) error {
	tx, err := ul.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(dailyUpsert)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for key, c := range pending {
		if _, err := stmt.Exec(key.day, key.keyHash, key.model, key.backend, c.name, c.lastSeen, c.requests, c.successful, c.errors,
			c.requestBytes, c.responseBytes, c.inputTokens, c.outputTokens, c.totalTokens, c.durationMS); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (ul *UsageLogger) Close() error {
	ul.closeOnce.Do(func() {
		ul.queueMu.Lock()
		ul.closed = true
		close(ul.records)
		ul.queueMu.Unlock()
		ul.worker.Wait()
		readErr := ul.readDB.Close()
		writeErr := ul.db.Close()
		if ul.closeErr == nil {
			if writeErr != nil {
				ul.closeErr = writeErr
			} else {
				ul.closeErr = readErr
			}
		}
	})
	return ul.closeErr
}

// A period includes today and the preceding days-1 UTC calendar days.
func usagePeriod(days int) (string, string) {
	if days <= 0 {
		days = 30
	}
	today := time.Now().UTC()
	return today.AddDate(0, 0, -days+1).Format("2006-01-02"), today.AddDate(0, 0, 1).Format("2006-01-02")
}
