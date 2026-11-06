package usage

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDailyAggregationPreservesDashboard(t *testing.T) {
	ul, err := NewUsageLogger(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ul.Close()
	today := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	for _, r := range []UsageRecord{
		{Timestamp: today, KeyHash: "A", KeyName: "old name", Model: "m", Backend: "one", StatusCode: 200, InputTokens: 10, OutputTokens: 5, TotalTokens: 15, DurationMS: 100},
		{Timestamp: today.Add(time.Minute), KeyHash: "A", KeyName: "old name", Model: "m", Backend: "one", StatusCode: 500, InputTokens: 5, OutputTokens: 5, TotalTokens: 10, DurationMS: 300},
		{Timestamp: today.Add(-time.Minute), KeyHash: "A", KeyName: "old name", Model: "m", Backend: "one", StatusCode: 302, InputTokens: 20, TotalTokens: 20, DurationMS: 200},
		{Timestamp: today, KeyHash: "B", KeyName: "second", Model: "m", Backend: "one", StatusCode: 200, TotalTokens: 40, DurationMS: 1000},
		{Timestamp: today.Add(time.Hour), KeyHash: "A", KeyName: "new name", Model: "n", Backend: "two", StatusCode: 200, TotalTokens: 50, DurationMS: 2000},
		{Timestamp: today.AddDate(0, 0, -1), KeyHash: "A", KeyName: "old name", Model: "m", Backend: "one", StatusCode: 200, TotalTokens: 20, DurationMS: 500},
	} {
		ul.Log(r)
	}
	// Querying is also a FIFO flush barrier; no timer/sleep is needed.
	d, err := ul.QueryDashboardData(2)
	if err != nil {
		t.Fatal(err)
	}
	if d.Totals.Requests != 6 || d.Totals.TotalTokens != 155 || d.Totals.Users != 2 || math.Abs(d.Totals.ErrorRate-100.0/6) > 0.00001 {
		t.Fatalf("totals: %#v", d.Totals)
	}
	if len(d.Daily) != 2 || d.Daily[0].Requests != 1 || d.Daily[1].Requests != 5 {
		t.Fatalf("daily: %#v", d.Daily)
	}
	if len(d.Users) != 2 || d.Users[0].Name != "new name" || d.Users[0].Requests != 5 || d.Users[0].ActiveDays != 2 {
		t.Fatalf("users: %#v", d.Users)
	}
	if len(d.Models) != 2 || d.Models[0].Model != "m" || d.Models[0].Requests != 5 || d.Models[0].Users != 2 || d.Models[0].AvgLatency != 420 {
		t.Fatalf("weighted model statistics: %#v", d.Models)
	}
	if len(d.Backends) != 2 || d.Backends[0].AvgLatency != 420 || len(d.DailyModels) != 3 {
		t.Fatalf("backend/model statistics: %#v %#v", d.Backends, d.DailyModels)
	}
	activity, err := ul.QueryKeyActivity(2)
	if err != nil {
		t.Fatal(err)
	}
	if activity["A"].Requests != 5 || activity["A"].TotalTokens != 115 {
		t.Fatalf("activity: %#v", activity)
	}
	var buckets, successful, errors int
	if err := ul.readDB.QueryRow(`SELECT COUNT(*),SUM(successful),SUM(errors) FROM usage_daily`).Scan(&buckets, &successful, &errors); err != nil {
		t.Fatal(err)
	}
	if buckets != 4 || successful != 4 || errors != 1 {
		t.Fatalf("buckets=%d success=%d errors=%d", buckets, successful, errors)
	}
}

func TestDailyUTCWindowAndEmptyDashboard(t *testing.T) {
	ul, err := NewUsageLogger(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ul.Close()
	empty, err := ul.QueryDashboardData(1)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Daily == nil || empty.Users == nil || empty.Models == nil || empty.Backends == nil || empty.DailyModels == nil {
		t.Fatal("empty arrays must not become null")
	}
	day := time.Now().UTC().Truncate(24 * time.Hour)
	for _, stamp := range []time.Time{day.Add(-time.Second), day.Add(30 * time.Minute).In(time.FixedZone("UTC+8", 8*3600)), day.Add(24 * time.Hour)} {
		ul.Log(UsageRecord{Timestamp: stamp, KeyHash: "key", Model: "m", StatusCode: 200})
	}
	d, err := ul.QueryDashboardData(1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Totals.Requests != 1 || len(d.Daily) != 1 || d.Daily[0].Date != day.Format("2006-01-02") {
		t.Fatalf("one UTC day: %#v", d)
	}
}

func TestDailyFailedBatchCanRetryWithoutDoubleCounting(t *testing.T) {
	ul, err := NewUsageLogger(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ul.Close()
	if _, err := ul.db.Exec(`CREATE TRIGGER reject_usage BEFORE INSERT ON usage_daily BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	ul.Log(UsageRecord{Timestamp: time.Now(), KeyHash: "key", Model: "m", StatusCode: 200})
	if err := ul.Flush(); err == nil {
		t.Fatal("expected failed flush")
	}
	if _, err := ul.db.Exec(`DROP TRIGGER reject_usage`); err != nil {
		t.Fatal(err)
	}
	ul.Log(UsageRecord{Timestamp: time.Now(), KeyHash: "key", Model: "m", StatusCode: 200})
	d, err := ul.QueryDashboardData(1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Totals.Requests != 2 {
		t.Fatalf("retry lost or duplicated counts: %d", d.Totals.Requests)
	}
}

func TestDailyCLIReports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	ul, err := NewUsageLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ul.Close()
	for i := 0; i < 3; i++ {
		ul.Log(UsageRecord{Timestamp: time.Now(), KeyHash: "short", KeyName: "local", Model: "daily-model", StatusCode: 200, InputTokens: 5, OutputTokens: 3, TotalTokens: 8, DurationMS: 100})
	}
	if err := ul.Flush(); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original; reader.Close(); writer.Close() }()
	RunUsageReport(path, 1)
	RunModelReport(path, 1)
	writer.Close()
	os.Stdout = original
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, "User Summary") || !strings.Contains(text, "Model Summary") || !strings.Contains(text, "daily-model") {
		t.Fatalf("reports missing: %s", text)
	}
	modelLine := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "daily-model ") {
			modelLine = line
		}
	}
	fields := strings.Fields(modelLine)
	if len(fields) < 7 || fields[1] != "3" || fields[2] != "1" || fields[3] != "15" || fields[4] != "9" || fields[5] != "24" || fields[6] != "100" {
		t.Fatalf("incorrect model report: %q", modelLine)
	}
}
