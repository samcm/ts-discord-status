package main

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"
)

var (
	recapDBPath string
	recapYear   int
	recapTZ     string
)

func init() {
	recapCmd.Flags().StringVar(&recapDBPath, "db", "", "Path to the status database (required)")
	recapCmd.Flags().IntVar(&recapYear, "year", 0, "Limit the recap to a calendar year (0 = all time)")
	recapCmd.Flags().StringVar(&recapTZ, "tz", "Local",
		"Timezone for day/hour grouping (IANA name, e.g. Australia/Sydney)")
	_ = recapCmd.MarkFlagRequired("db")

	rootCmd.AddCommand(recapCmd)
}

var recapCmd = &cobra.Command{
	Use:   "recap",
	Short: "Print a summary of recorded TeamSpeak activity",
	Long:  "Reads the local status database and prints a human-readable recap of server activity.",
	RunE:  runRecap,
}

func runRecap(cmd *cobra.Command, args []string) error {
	loc, err := loadLocation(recapTZ)
	if err != nil {
		return err
	}

	db, err := sql.Open("sqlite", "file:"+recapDBPath+"?mode=ro")
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	// Scope every query to a calendar-year unix range computed in the target
	// timezone. A range filter is index-friendly and keeps grouping correct
	// regardless of where the recap is run (the container itself runs in UTC).
	where, args2 := yearRange("ts", recapYear, loc)
	pWhere, pArgs := yearRange("p.ts", recapYear, loc)

	var (
		first, last sql.NullInt64
		samples     int64
	)

	if err := db.QueryRow(`SELECT MIN(ts), MAX(ts), COUNT(*) FROM samples `+where, args2...).
		Scan(&first, &last, &samples); err != nil {
		return fmt.Errorf("failed to read sample range: %w", err)
	}

	if samples == 0 {
		fmt.Println("No data recorded yet for the selected period.")

		return nil
	}

	var populated, userMinutes int64

	if err := db.QueryRow(`SELECT COUNT(*) FROM samples `+andCond(where, "total_users > 0"), args2...).
		Scan(&populated); err != nil {
		return fmt.Errorf("failed to count populated minutes: %w", err)
	}

	if err := db.QueryRow(`SELECT COUNT(*) FROM presence p `+pWhere, pArgs...).Scan(&userMinutes); err != nil {
		return fmt.Errorf("failed to count user minutes: %w", err)
	}

	var (
		peakTS    int64
		peakUsers int
	)

	if err := db.QueryRow(`SELECT ts, total_users FROM samples `+where+
		` ORDER BY total_users DESC, ts LIMIT 1`, args2...).Scan(&peakTS, &peakUsers); err != nil {
		return fmt.Errorf("failed to find peak: %w", err)
	}

	title := "all time"
	if recapYear > 0 {
		title = fmt.Sprintf("%d", recapYear)
	}

	fmt.Printf("\n=== TeamSpeak Recap (%s) ===\n\n", title)
	fmt.Printf("  Period:           %s  ->  %s\n", fmtDate(first.Int64, loc), fmtDate(last.Int64, loc))
	fmt.Printf("  Snapshots:        %d\n", samples)
	fmt.Printf("  Server populated: %s (had at least one person online)\n", humanMinutes(populated))
	fmt.Printf("  Total hangout:    %s (combined time across everyone)\n", humanMinutes(userMinutes))
	fmt.Printf("  Peak online:      %d people at %s\n\n", peakUsers, fmtTime(peakTS, loc))

	if err := printTopUsers(db, pWhere, pArgs, loc); err != nil {
		return err
	}

	if err := printChannels(db, pWhere, pArgs); err != nil {
		return err
	}

	if err := printBusiest(db, where, args2, loc); err != nil {
		return err
	}

	fmt.Println()

	return nil
}

func printTopUsers(db *sql.DB, pWhere string, pArgs []any, loc *time.Location) error {
	rows, err := db.Query(`SELECT u.nickname, COUNT(*) AS mins,
		SUM(p.flags & 1) AS muted, SUM((p.flags >> 2) & 1) AS away,
		MIN(p.ts), MAX(p.ts)
		FROM presence p JOIN users u ON u.id = p.user_id `+pWhere+`
		GROUP BY p.user_id ORDER BY mins DESC LIMIT 15`, pArgs...)
	if err != nil {
		return fmt.Errorf("failed to rank users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	fmt.Println("  Most active:")

	rank := 0

	for rows.Next() {
		var (
			nick        string
			mins        int64
			muted, away int64
			min, max    int64
		)

		if err := rows.Scan(&nick, &mins, &muted, &away, &min, &max); err != nil {
			return fmt.Errorf("failed to scan user: %w", err)
		}

		rank++

		fmt.Printf("    %2d. %-18s %-10s  muted %3d%%  afk %3d%%  (%s - %s)\n",
			rank, nick, humanMinutes(mins), pct(muted, mins), pct(away, mins),
			fmtDate(min, loc), fmtDate(max, loc))
	}

	return rows.Err()
}

// printChannels ranks channels by total time spent in them.
func printChannels(db *sql.DB, pWhere string, pArgs []any) error {
	rows, err := db.Query(`SELECT c.name, COUNT(*) AS mins
		FROM presence p JOIN channels c ON c.id = p.channel_id `+pWhere+`
		GROUP BY p.channel_id ORDER BY mins DESC LIMIT 10`, pArgs...)
	if err != nil {
		return fmt.Errorf("failed to rank channels: %w", err)
	}
	defer func() { _ = rows.Close() }()

	fmt.Println("\n  Most popular channels:")

	for rows.Next() {
		var (
			name string
			mins int64
		)

		if err := rows.Scan(&name, &mins); err != nil {
			return fmt.Errorf("failed to scan channel: %w", err)
		}

		fmt.Printf("    %-24s %s\n", name, humanMinutes(mins))
	}

	return rows.Err()
}

func pct(part, total int64) int64 {
	if total == 0 {
		return 0
	}

	return part * 100 / total
}

// printBusiest buckets samples by day and hour in the target timezone.
func printBusiest(db *sql.DB, where string, args []any, loc *time.Location) error {
	rows, err := db.Query(`SELECT ts, total_users FROM samples `+where, args...)
	if err != nil {
		return fmt.Errorf("failed to read samples: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byDay := make(map[string]int64, 366)
	byHour := make(map[int]int64, 24)

	for rows.Next() {
		var (
			ts    int64
			users int64
		)

		if err := rows.Scan(&ts, &users); err != nil {
			return fmt.Errorf("failed to scan sample: %w", err)
		}

		t := time.Unix(ts, 0).In(loc)
		byDay[t.Format("2006-01-02")] += users
		byHour[t.Hour()] += users
	}

	if err := rows.Err(); err != nil {
		return err
	}

	bestDay, _ := topKey(byDay)
	bestHour, _ := topHour(byHour)

	fmt.Printf("\n  Busiest day:      %s\n", bestDay)
	fmt.Printf("  Busiest hour:     %02d:00-%02d:59 (%s)\n", bestHour, bestHour, loc)

	return nil
}

func loadLocation(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return time.Local, nil
	}

	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", name, err)
	}

	return loc, nil
}

// yearRange returns a WHERE clause (and args) limiting col to the given calendar
// year in loc. It returns an empty clause when year is zero.
func yearRange(col string, year int, loc *time.Location) (string, []any) {
	if year <= 0 {
		return "", nil
	}

	start := time.Date(year, 1, 1, 0, 0, 0, 0, loc).Unix()
	end := time.Date(year+1, 1, 1, 0, 0, 0, 0, loc).Unix()

	return fmt.Sprintf("WHERE %s >= ? AND %s < ?", col, col), []any{start, end}
}

func andCond(where, cond string) string {
	if where == "" {
		return "WHERE " + cond
	}

	return where + " AND " + cond
}

func topKey(m map[string]int64) (string, int64) {
	var (
		key string
		max int64 = -1
	)

	for k, v := range m {
		if v > max {
			key, max = k, v
		}
	}

	return key, max
}

func topHour(m map[int]int64) (int, int64) {
	var (
		hour int
		max  int64 = -1
	)

	for h, v := range m {
		if v > max {
			hour, max = h, v
		}
	}

	return hour, max
}

func humanMinutes(m int64) string {
	d := m / (60 * 24)
	h := (m / 60) % 24
	min := m % 60

	if d > 0 {
		return fmt.Sprintf("%dd %dh %dm", d, h, min)
	}

	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, min)
	}

	return fmt.Sprintf("%dm", min)
}

func fmtDate(unix int64, loc *time.Location) string {
	return time.Unix(unix, 0).In(loc).Format("2006-01-02")
}

func fmtTime(unix int64, loc *time.Location) string {
	return time.Unix(unix, 0).In(loc).Format("2006-01-02 15:04")
}
