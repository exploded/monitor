package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
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

	return d, nil
}

// Prune deletes requests older than the given number of days.
func Prune(ctx context.Context, q *db.Queries, retentionDays int) error {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	return q.DeleteRequestsBefore(ctx, cutoff)
}
