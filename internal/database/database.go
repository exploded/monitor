package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	db "github.com/exploded/monitor/db/sqlc"
)

// Open opens (or creates) the SQLite database at path, applies the schema,
// and enables WAL mode for concurrent reads.
func Open(path, schemaPath string) (*sql.DB, error) {
	d, err := sql.Open("sqlite", path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	d.SetMaxOpenConns(1)

	if err := d.Ping(); err != nil {
		d.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("reading schema: %w", err)
	}

	if _, err := d.Exec(string(schema)); err != nil {
		d.Close()
		return nil, fmt.Errorf("applying schema: %w", err)
	}

	// Migrations: add columns that may not exist yet (safe to re-run)
	migrations := []string{
		"ALTER TABLE requests ADD COLUMN country TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE requests ADD COLUMN city TEXT NOT NULL DEFAULT ''",
	}
	for _, m := range migrations {
		if _, err := d.Exec(m); err != nil {
			// Ignore "duplicate column name" errors
			if !isDuplicateColumn(err) {
				d.Close()
				return nil, fmt.Errorf("migration: %w", err)
			}
		}
	}

	// Deduplicate alert_rules: keep the row with the lowest id for each name
	d.Exec(`DELETE FROM alert_rules WHERE id NOT IN (
		SELECT MIN(id) FROM alert_rules GROUP BY name
	)`)

	// Add UNIQUE constraint on alert_rules.name if missing (recreate via rename)
	d.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_alert_rules_name ON alert_rules(name)`)

	// Create indexes for new columns (IF NOT EXISTS is safe)
	d.Exec("CREATE INDEX IF NOT EXISTS idx_requests_country ON requests(country)")

	return d, nil
}

func isDuplicateColumn(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "duplicate column") || strings.Contains(s, "already exists")
}

// Prune deletes requests and related data older than the given number of days.
func Prune(ctx context.Context, q *db.Queries, retentionDays int) error {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	if err := q.DeleteRequestsBefore(ctx, cutoff); err != nil {
		return err
	}
	if err := q.DeleteAppLogsBefore(ctx, cutoff); err != nil {
		return err
	}
	// Best-effort prune for optional tables (may not exist yet)
	q.DeleteAlertLogsBefore(ctx, cutoff)
	q.DeleteUptimeChecksBefore(ctx, cutoff)
	q.DeleteAnomaliesBefore(ctx, cutoff)
	return nil
}
