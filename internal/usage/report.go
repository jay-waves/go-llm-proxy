package usage

import (
	"database/sql"
	"fmt"
	"os"
	"text/tabwriter"

	_ "modernc.org/sqlite"
)

// RunUsageReport opens the usage database and prints a daily summary per user.
func RunUsageReport(dbPath string, days int) {
	db, err := sql.Open("sqlite", usageDSN(dbPath, true))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening usage db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	fromDay, untilDay := usagePeriod(days)

	rows, err := db.Query(`
		SELECT
			day,
			key_name,
			key_hash,
			SUM(requests)                       AS requests,
			SUM(successful) AS successful,
			SUM(errors) AS errors,
			SUM(input_tokens)              AS input_tokens,
			SUM(output_tokens)             AS output_tokens,
			SUM(total_tokens)              AS total_tokens,
			SUM(request_bytes)             AS request_bytes,
			SUM(response_bytes)            AS response_bytes,
			CAST(1.0 * SUM(duration_ms) / SUM(requests) AS INTEGER) AS avg_duration_ms
		FROM usage_daily
		WHERE day >= ? AND day < ?
		GROUP BY day, key_hash
		ORDER BY day DESC, total_tokens DESC
	`, fromDay, untilDay)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query error: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "DATE\tUSER\tKEY\tREQUESTS\tOK\tERROR\tINPUT TOK\tOUTPUT TOK\tTOTAL TOK\tREQ BYTES\tRESP BYTES\tAVG MS\n")
	fmt.Fprintf(w, "----\t----\t---\t--------\t--\t-----\t---------\t----------\t---------\t---------\t----------\t------\n")

	count := 0
	for rows.Next() {
		var (
			day           string
			keyName       string
			keyHash       string
			requests      int
			successful    int
			errors        int
			inputTokens   int64
			outputTokens  int64
			totalTokens   int64
			requestBytes  int64
			responseBytes int64
			avgDuration   int64
		)
		if err := rows.Scan(&day, &keyName, &keyHash, &requests, &successful, &errors,
			&inputTokens, &outputTokens, &totalTokens,
			&requestBytes, &responseBytes, &avgDuration); err != nil {
			fmt.Fprintf(os.Stderr, "scan error: %v\n", err)
			continue
		}

		displayName := keyName
		if displayName == "" {
			displayName = "(unnamed)"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%d\n",
			day,
			displayName,
			keyHash[:min(8, len(keyHash))],
			requests,
			successful,
			errors,
			formatNumber(inputTokens),
			formatNumber(outputTokens),
			formatNumber(totalTokens),
			formatBytes(requestBytes),
			formatBytes(responseBytes),
			avgDuration,
		)
		count++
	}
	w.Flush()

	if count == 0 {
		fmt.Println("(no usage data found)")
	}

	// Also print a totals-by-user summary.
	printUserSummary(db, days)
}

// printUserSummary prints an aggregate summary across all days per user.
func printUserSummary(db *sql.DB, days int) {
	fromDay, untilDay := usagePeriod(days)
	rows, err := db.Query(`
		SELECT
			key_name,
			key_hash,
			SUM(requests)                       AS requests,
			SUM(input_tokens)              AS input_tokens,
			SUM(output_tokens)             AS output_tokens,
			SUM(total_tokens)              AS total_tokens,
			MIN(day)                 AS first_seen,
			MAX(last_seen)                 AS last_seen,
			COUNT(DISTINCT day) AS active_days
		FROM usage_daily
		WHERE day >= ? AND day < ?
		GROUP BY key_hash
		ORDER BY total_tokens DESC
	`, fromDay, untilDay)
	if err != nil {
		return
	}
	defer rows.Close()

	fmt.Println()
	fmt.Println("=== User Summary ===")
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "USER\tKEY\tREQUESTS\tINPUT TOK\tOUTPUT TOK\tTOTAL TOK\tACTIVE DAYS\tFIRST SEEN\tLAST SEEN\n")
	fmt.Fprintf(w, "----\t---\t--------\t---------\t----------\t---------\t-----------\t----------\t---------\n")

	for rows.Next() {
		var (
			keyName      string
			keyHash      string
			requests     int
			inputTokens  int64
			outputTokens int64
			totalTokens  int64
			firstSeen    string
			lastSeen     string
			activeDays   int
		)
		if err := rows.Scan(&keyName, &keyHash, &requests,
			&inputTokens, &outputTokens, &totalTokens,
			&firstSeen, &lastSeen, &activeDays); err != nil {
			continue
		}

		displayName := keyName
		if displayName == "" {
			displayName = "(unnamed)"
		}

		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%d\t%s\t%s\n",
			displayName,
			keyHash[:min(8, len(keyHash))],
			requests,
			formatNumber(inputTokens),
			formatNumber(outputTokens),
			formatNumber(totalTokens),
			activeDays,
			firstSeen[:10],
			lastSeen[:10],
		)
	}
	w.Flush()
}

// RunModelReport prints a per-model breakdown.
func RunModelReport(dbPath string, days int) {
	db, err := sql.Open("sqlite", usageDSN(dbPath, true))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening usage db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	fromDay, untilDay := usagePeriod(days)

	rows, err := db.Query(`
		SELECT
			model,
			SUM(requests)                       AS requests,
			COUNT(DISTINCT key_hash)       AS unique_users,
			SUM(input_tokens)              AS input_tokens,
			SUM(output_tokens)             AS output_tokens,
			SUM(total_tokens)              AS total_tokens,
			CAST(1.0 * SUM(duration_ms) / SUM(requests) AS INTEGER) AS avg_duration_ms
		FROM usage_daily
		WHERE day >= ? AND day < ?
		GROUP BY model
		ORDER BY total_tokens DESC
	`, fromDay, untilDay)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query error: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	fmt.Println("=== Model Summary ===")
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "MODEL\tREQUESTS\tUSERS\tINPUT TOK\tOUTPUT TOK\tTOTAL TOK\tAVG MS\n")
	fmt.Fprintf(w, "-----\t--------\t-----\t---------\t----------\t---------\t------\n")

	for rows.Next() {
		var (
			model        string
			requests     int
			uniqueUsers  int
			inputTokens  int64
			outputTokens int64
			totalTokens  int64
			avgDuration  int64
		)
		if err := rows.Scan(&model, &requests, &uniqueUsers,
			&inputTokens, &outputTokens, &totalTokens, &avgDuration); err != nil {
			continue
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\t%s\t%d\n",
			model,
			requests,
			uniqueUsers,
			formatNumber(inputTokens),
			formatNumber(outputTokens),
			formatNumber(totalTokens),
			avgDuration,
		)
	}
	w.Flush()
}

func formatNumber(n int64) string {
	if n == 0 {
		return "0"
	}
	s := fmt.Sprintf("%d", n)
	// Insert commas.
	out := make([]byte, 0, len(s)+(len(s)-1)/3)
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return string(out)
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
