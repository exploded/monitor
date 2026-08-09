package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
)

// schemaPath locates db/schema.sql from this package's directory.
func schemaPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "db", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("schema not found at %s: %v", p, err)
	}
	return p
}

func openTestDB(t *testing.T) (*sql.DB, *db.Queries) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path, schemaPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d, db.New(d)
}

// TestOpenAppliesPragmas guards the bug this work started from: the DSN used
// mattn/go-sqlite3 parameter syntax, which modernc silently ignores, so WAL was
// never actually on in production.
func TestOpenAppliesPragmas(t *testing.T) {
	d, _ := openTestDB(t)

	for _, tc := range []struct{ pragma, want string }{
		{"journal_mode", "wal"},
		{"synchronous", "1"}, // NORMAL
		{"foreign_keys", "1"},
	} {
		var got string
		if err := d.QueryRow("PRAGMA " + tc.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", tc.pragma, err)
		}
		if got != tc.want {
			t.Errorf("PRAGMA %s = %q, want %q", tc.pragma, got, tc.want)
		}
	}
}

// TestOpenIsIdempotent covers the once-migrations: a second Open must not
// re-run them, and must not error on already-dropped columns and indexes.
func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	d1, err := Open(path, schemaPath(t))
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	var first int
	if err := d1.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&first); err != nil {
		t.Fatal(err)
	}
	d1.Close()

	if first != len(onceMigrations) {
		t.Errorf("recorded %d migrations, want %d", first, len(onceMigrations))
	}

	d2, err := Open(path, schemaPath(t))
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer d2.Close()

	var second int
	if err := d2.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&second); err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("migration count changed on reopen: %d then %d", first, second)
	}
}

// TestDroppedColumnsAreGone asserts the write-only columns really are removed,
// including on a database that already had them.
func TestDroppedColumnsAreGone(t *testing.T) {
	d, _ := openTestDB(t)

	for _, tc := range []struct{ table, column string }{
		{"requests", "city"},
		{"uptime_checks", "created_at"},
	} {
		var n int
		err := d.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, tc.table, tc.column,
		).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s.%s still present", tc.table, tc.column)
		}
	}

	for _, idx := range []string{"idx_requests_host", "idx_requests_status", "idx_requests_country"} {
		var n int
		if err := d.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx,
		).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("index %s still present", idx)
		}
	}
}

func insertRequest(t *testing.T, q *db.Queries, ts time.Time, status int64, ip string) {
	t.Helper()
	err := q.InsertRequest(context.Background(), db.InsertRequestParams{
		Ts: ts, Host: "example.com", ClientIp: ip, Method: "GET", Uri: "/",
		Status: status, Size: 100, UserAgent: "test", DurationMs: 5, IsBot: 0,
		Country: "AU", Referer: "",
	})
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
}

// TestPruneBatched checks the batched delete removes everything past the
// horizon and nothing inside it, over enough rows to span several batches.
func TestPruneBatched(t *testing.T) {
	_, q := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Force the multi-batch path: 120 old rows at 25 a batch is 5 iterations.
	origSize, origPause := pruneBatchSize, pruneBatchPause
	pruneBatchSize, pruneBatchPause = 25, time.Millisecond
	t.Cleanup(func() { pruneBatchSize, pruneBatchPause = origSize, origPause })

	const old, recent = 120, 30
	for i := 0; i < old; i++ {
		insertRequest(t, q, now.AddDate(0, 0, -60), 200, "1.1.1.1")
	}
	for i := 0; i < recent; i++ {
		insertRequest(t, q, now.AddDate(0, 0, -1), 200, "2.2.2.2")
	}

	r := Retention{Requests: 30, UptimeChecks: 14, AppLogError: 90, AppLogNoise: 14, Anomalies: 90, AlertLog: 180}
	if err := Prune(ctx, q, r); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	got, err := q.CountRequestsSince(ctx, now.AddDate(0, 0, -365))
	if err != nil {
		t.Fatal(err)
	}
	if got != recent {
		t.Errorf("after prune: %d rows, want %d", got, recent)
	}
}

// TestPruneRespectsCancellation makes sure a prune in flight stops on shutdown
// instead of deleting through it.
func TestPruneRespectsCancellation(t *testing.T) {
	_, q := openTestDB(t)
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		insertRequest(t, q, now.AddDate(0, 0, -60), 200, "1.1.1.1")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := Retention{Requests: 30, UptimeChecks: 14, AppLogError: 90, AppLogNoise: 14, Anomalies: 90, AlertLog: 180}
	if err := Prune(ctx, q, r); err == nil {
		t.Error("expected an error from a cancelled context, got nil")
	}

	remaining, err := q.CountRequestsSince(context.Background(), now.AddDate(0, 0, -365))
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 10 {
		t.Errorf("cancelled prune deleted %d rows, want 0", 10-remaining)
	}
}

// TestAppLogRetentionIsLevelAware checks ERROR survives the noise horizon while
// WARN past it does not.
func TestAppLogRetentionIsLevelAware(t *testing.T) {
	_, q := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	add := func(level string, age int) {
		err := q.InsertAppLog(ctx, db.InsertAppLogParams{
			Ts: now.AddDate(0, 0, -age), App: "a", Level: level,
			Message: "m", Attrs: "{}", Source: "",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	add("ERROR", 60)  // inside the 90d error horizon, past the 14d noise one
	add("WARN", 60)   // past the noise horizon
	add("WARN", 3)    // inside it
	add("ERROR", 120) // past the error horizon

	r := Retention{Requests: 30, UptimeChecks: 14, AppLogError: 90, AppLogNoise: 14, Anomalies: 90, AlertLog: 180}
	if err := Prune(ctx, q, r); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	rows, err := q.RecentAppErrors(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	levels := map[string]int{}
	for _, row := range rows {
		levels[row.Level]++
	}
	if levels["ERROR"] != 1 {
		t.Errorf("ERROR rows = %d, want 1 (60d kept, 120d pruned)", levels["ERROR"])
	}
	if levels["WARN"] != 1 {
		t.Errorf("WARN rows = %d, want 1 (3d kept, 60d pruned)", levels["WARN"])
	}
}

// TestRollupMatchesRaw is the correctness guarantee behind deleting raw rows:
// the rollup must agree with what a direct aggregate over raw would say.
func TestRollupMatchesRaw(t *testing.T) {
	_, q := openTestDB(t)
	ctx := context.Background()
	today := time.Now().UTC()

	insertRequest(t, q, today, 200, "1.1.1.1")
	insertRequest(t, q, today, 404, "1.1.1.1")
	insertRequest(t, q, today, 500, "2.2.2.2")

	if err := Rollup(ctx, q); err != nil {
		t.Fatalf("Rollup: %v", err)
	}

	rows, err := q.DailySummary(ctx, today.AddDate(0, 0, -1).Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("DailySummary returned nothing after rollup")
	}

	var found bool
	for _, row := range rows {
		if row.Day != today.Format("2006-01-02") {
			continue
		}
		found = true
		if row.Total != 3 {
			t.Errorf("total = %d, want 3", row.Total)
		}
		if row.UniqueIps != 2 {
			t.Errorf("unique_ips = %d, want 2", row.UniqueIps)
		}
		if row.Errors != 2 {
			t.Errorf("errors = %d, want 2 (404 and 500)", row.Errors)
		}
	}
	if !found {
		t.Errorf("no rollup row for today (%s)", today.Format("2006-01-02"))
	}
}

// TestRollupIsIdempotent guards the UPSERT: the hourly job recomputes today
// every tick and must not double-count.
func TestRollupIsIdempotent(t *testing.T) {
	_, q := openTestDB(t)
	ctx := context.Background()
	today := time.Now().UTC()

	insertRequest(t, q, today, 200, "1.1.1.1")

	for i := 0; i < 3; i++ {
		if err := Rollup(ctx, q); err != nil {
			t.Fatalf("Rollup %d: %v", i, err)
		}
	}

	rows, err := q.DailySummary(ctx, today.AddDate(0, 0, -1).Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Day == today.Format("2006-01-02") && row.Total != 1 {
			t.Errorf("total = %d after 3 rollups, want 1", row.Total)
		}
	}
}

// TestRollupSurvivesPrune is the whole point of the rollup: aggregate history
// must remain after the raw rows behind it are deleted.
func TestRollupSurvivesPrune(t *testing.T) {
	_, q := openTestDB(t)
	ctx := context.Background()
	old := time.Now().UTC().AddDate(0, 0, -60)

	insertRequest(t, q, old, 200, "1.1.1.1")
	insertRequest(t, q, old, 200, "2.2.2.2")

	if err := Rollup(ctx, q); err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	r := Retention{Requests: 30, UptimeChecks: 14, AppLogError: 90, AppLogNoise: 14, Anomalies: 90, AlertLog: 180}
	if err := Prune(ctx, q, r); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	// Raw is gone.
	n, err := q.CountRequestsSince(ctx, time.Now().UTC().AddDate(0, 0, -365))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("raw rows remaining = %d, want 0", n)
	}

	// The rollup for that day is not.
	rows, err := q.DailySummary(ctx, old.AddDate(0, 0, -1).Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, row := range rows {
		if row.Day == old.Format("2006-01-02") {
			found = true
			if row.Total != 2 {
				t.Errorf("rollup total = %d, want 2", row.Total)
			}
		}
	}
	if !found {
		t.Error("rollup row lost when raw rows were pruned")
	}
}
