package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	db "github.com/exploded/monitor/db/sqlc"
)

// Open opens (or creates) the SQLite database at path, applies the schema,
// and enables WAL mode for concurrent reads.
//
// PRAGMAs must use modernc.org/sqlite's `_pragma=name(value)` form. The
// mattn/go-sqlite3 style (`_journal_mode=WAL`, `_busy_timeout=5000`, ...) is
// silently discarded by this driver — applyQueryParams only recognises
// _pragma, _time_format, _time_integer_format, _txlock, _inttotime,
// _texttotime and vfs, and returns nil for anything else. Using the wrong
// dialect here left production on journal_mode=delete, synchronous=FULL,
// busy_timeout=0 and a 2 MB cache.
func Open(path, schemaPath string) (*sql.DB, error) {
	dsn := path + "?_time_format=sqlite" +
		"&_pragma=busy_timeout(5000)" + // wait rather than failing on a locked db
		"&_pragma=journal_mode(WAL)" + // readers don't block the writer
		"&_pragma=synchronous(NORMAL)" + // safe in WAL mode; eliminates most fsyncs
		"&_pragma=cache_size(-20000)" + // 20 MB page cache (negative = kibibytes)
		"&_pragma=mmap_size(268435456)" + // 256 MB memory-mapped I/O
		"&_pragma=foreign_keys(on)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	d.SetMaxOpenConns(1)

	if err := d.Ping(); err != nil {
		d.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	// Confirm the PRAGMAs landed. A wrong DSN dialect fails silently, so assert
	// rather than assume — this is exactly how WAL stayed off in production.
	var journalMode string
	if err := d.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		slog.Warn("could not read journal_mode", "err", err)
	} else if !strings.EqualFold(journalMode, "wal") {
		slog.Warn("sqlite journal_mode is not WAL — check the DSN pragma syntax",
			"mode", journalMode)
	} else {
		slog.Info("sqlite ready", "journal_mode", journalMode)
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

	// Additive column migrations: cheap, idempotent, safe to attempt every boot.
	for _, m := range []string{
		"ALTER TABLE requests ADD COLUMN country TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE requests ADD COLUMN referer TEXT NOT NULL DEFAULT ''",
	} {
		if _, err := d.Exec(m); err != nil && !isDuplicateColumn(err) {
			d.Close()
			return nil, fmt.Errorf("migration: %w", err)
		}
	}

	// Add UNIQUE constraint on alert_rules.name if missing
	d.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_alert_rules_name ON alert_rules(name)`)

	if err := runOnce(d); err != nil {
		d.Close()
		return nil, err
	}

	return d, nil
}

// onceMigration is a migration that must run exactly once. These scan or
// rewrite whole tables, so re-running them on every boot is not free: the
// timestamp normalisation below full-scanned `requests` at every start and
// rewrote every row inserted since the last one, because the driver writes
// nanosecond precision that the normalisation strips back to milliseconds.
type onceMigration struct {
	name string
	stmt string
}

var onceMigrations = []onceMigration{
	{"dedupe-alert-rules", `DELETE FROM alert_rules WHERE id NOT IN (
		SELECT MIN(id) FROM alert_rules GROUP BY name
	)`},
	{"dedupe-alert-log", `DELETE FROM alert_log WHERE id NOT IN (
		SELECT MIN(id) FROM alert_log GROUP BY type, message, created_at
	)`},
	// Canonicalise legacy timestamps so string comparison against
	// driver-formatted parameters works. Mixed fractional precision is fine —
	// '+' sorts below every digit, so ordering and ts >= ? stay correct.
	{"normalize-app-logs-ts", `UPDATE app_logs SET ts = strftime('%Y-%m-%d %H:%M:%f+00:00', ts)
		WHERE ts NOT LIKE '____-__-__ __:__:__.%+00:00'`},
	{"normalize-requests-ts", `UPDATE requests SET ts = strftime('%Y-%m-%d %H:%M:%f+00:00', ts)
		WHERE ts NOT LIKE '____-__-__ __:__:__.%+00:00'`},
	// Indexes that cost ~26 MB and served no query. See db/schema.sql.
	{"drop-idx-requests-host", "DROP INDEX IF EXISTS idx_requests_host"},
	{"drop-idx-requests-status", "DROP INDEX IF EXISTS idx_requests_status"},
	{"drop-idx-requests-country", "DROP INDEX IF EXISTS idx_requests_country"},
	// Write-only columns. Space is not reclaimed until a VACUUM.
	{"drop-requests-city", "ALTER TABLE requests DROP COLUMN city"},
	{"drop-uptime-checks-created-at", "ALTER TABLE uptime_checks DROP COLUMN created_at"},
}

// runOnce applies any onceMigration not yet recorded in schema_migrations.
// Each is recorded in the same transaction as its statement, so a crash midway
// leaves it un-recorded and it retries next boot.
func runOnce(d *sql.DB) error {
	for _, m := range onceMigrations {
		var done int
		if err := d.QueryRow(
			"SELECT COUNT(*) FROM schema_migrations WHERE name = ?", m.name,
		).Scan(&done); err != nil {
			return fmt.Errorf("check migration %s: %w", m.name, err)
		}
		if done > 0 {
			continue
		}

		tx, err := d.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(m.stmt); err != nil {
			tx.Rollback()
			// A column or index that is already gone is a success, not a
			// failure — the target state is what matters.
			if isAlreadyApplied(err) {
				if _, e := d.Exec("INSERT OR IGNORE INTO schema_migrations (name) VALUES (?)", m.name); e != nil {
					return fmt.Errorf("record migration %s: %w", m.name, e)
				}
				continue
			}
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec("INSERT OR IGNORE INTO schema_migrations (name) VALUES (?)", m.name); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", m.name, err)
		}
		slog.Info("applied migration", "name", m.name)
	}
	return nil
}

func isDuplicateColumn(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "duplicate column") || strings.Contains(s, "already exists")
}

// isAlreadyApplied reports whether the error means the migration's target state
// already holds (e.g. dropping a column that isn't there on a fresh database).
func isAlreadyApplied(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no such column") ||
		strings.Contains(s, "no such index") ||
		strings.Contains(s, "cannot drop column")
}

// Tunables, as vars so tests can exercise the multi-batch path cheaply.
var (
	// pruneBatchSize is rows per DELETE. Small enough that the single pooled
	// connection is handed back frequently, large enough to make progress.
	pruneBatchSize int64 = 5000
	// pruneBatchPause lets dashboard reads and watcher inserts interleave
	// between batches.
	pruneBatchPause = 50 * time.Millisecond
	// pruneMaxBatches bounds one table's work per run — a backstop against a
	// bad cutoff turning into an unbounded delete loop. At 5k rows a batch this
	// is 1M rows, far above any real backlog.
	pruneMaxBatches = 200
)

// Retention holds the per-table horizons, in days.
type Retention struct {
	Requests     int
	UptimeChecks int
	AppLogError  int
	AppLogNoise  int
	Anomalies    int
	AlertLog     int
}

// Prune deletes data past its retention horizon, in batches.
//
// Deleting in one statement would hold a write transaction for the whole
// operation, and with MaxOpenConns(1) that blocks every reader and the watcher
// too. Batching plus a short pause between batches keeps the database
// responsive. Note this frees pages for reuse but does not shrink the file —
// see the VACUUM INTO step in the retention runbook.
func Prune(ctx context.Context, q *db.Queries, r Retention) error {
	now := time.Now()
	appErrCutoff := now.AddDate(0, 0, -r.AppLogError)
	appNoiseCutoff := now.AddDate(0, 0, -r.AppLogNoise)

	tables := []struct {
		name string
		del  func(context.Context) (int64, error)
	}{
		{"requests", func(c context.Context) (int64, error) {
			return rows(q.DeleteRequestsBeforeBatch(c, db.DeleteRequestsBeforeBatchParams{
				Cutoff: now.AddDate(0, 0, -r.Requests), BatchSize: pruneBatchSize,
			}))
		}},
		{"uptime_checks", func(c context.Context) (int64, error) {
			return rows(q.DeleteUptimeChecksBeforeBatch(c, db.DeleteUptimeChecksBeforeBatchParams{
				Cutoff: now.AddDate(0, 0, -r.UptimeChecks), BatchSize: pruneBatchSize,
			}))
		}},
		{"app_logs", func(c context.Context) (int64, error) {
			return rows(q.DeleteAppLogsBeforeBatch(c, db.DeleteAppLogsBeforeBatchParams{
				ErrorCutoff: appErrCutoff, OtherCutoff: appNoiseCutoff, BatchSize: pruneBatchSize,
			}))
		}},
		{"anomalies", func(c context.Context) (int64, error) {
			return rows(q.DeleteAnomaliesBeforeBatch(c, db.DeleteAnomaliesBeforeBatchParams{
				Cutoff: now.AddDate(0, 0, -r.Anomalies), BatchSize: pruneBatchSize,
			}))
		}},
		{"alert_log", func(c context.Context) (int64, error) {
			return rows(q.DeleteAlertLogsBeforeBatch(c, db.DeleteAlertLogsBeforeBatchParams{
				Cutoff: now.AddDate(0, 0, -r.AlertLog), BatchSize: pruneBatchSize,
			}))
		}},
	}

	var firstErr error
	for _, t := range tables {
		deleted, err := pruneTable(ctx, t.name, t.del)
		if deleted > 0 {
			slog.Info("pruned", "table", t.name, "rows", deleted)
		}
		if err != nil {
			// Keep going: one failing table shouldn't stop the others, but the
			// error still surfaces rather than being silently dropped.
			slog.Error("prune", "table", t.name, "err", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("prune %s: %w", t.name, err)
			}
		}
	}
	return firstErr
}

func pruneTable(ctx context.Context, name string, del func(context.Context) (int64, error)) (int64, error) {
	var total int64
	for i := 0; i < pruneMaxBatches; i++ {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := del(ctx)
		if err != nil {
			return total, err
		}
		total += n
		if n < pruneBatchSize {
			return total, nil
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-time.After(pruneBatchPause):
		}
	}
	slog.Warn("prune hit batch cap, will continue next run", "table", name, "rows", total)
	return total, nil
}

func rows(res sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}
