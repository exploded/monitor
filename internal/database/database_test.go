package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

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

// TestQueryFilesAreASCII guards a trap that costs more time than it should.
// sqlc rewrites sqlc.arg() into positional parameters using byte offsets, so a
// multi-byte rune anywhere in a query file — an em-dash in a comment is the easy
// mistake — shifts every later edit and silently corrupts a different query. The
// failure surfaces as a parse error on innocent SQL several queries away.
func TestQueryFilesAreASCII(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("..", "..", "db", "queries"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(content), "\n") {
			for _, r := range line {
				if r > unicode.MaxASCII {
					t.Errorf("%s:%d contains non-ASCII %q; keep query files ASCII-only", e.Name(), i+1, r)
					break
				}
			}
		}
	}
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
		{"bot_patterns", "block"},
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

// TestBlockingArtifactsAreMigratedAway covers the upgrade path that production
// actually takes. On a fresh database the blocking tables are simply never
// created, so only a database that already holds them — with rows, and with an
// alert_log row pointing at the auto_block rule via a NOT NULL foreign key —
// proves the migrations both fire and fire in a workable order.
func TestBlockingArtifactsAreMigratedAway(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE blocked_ips (
		    id INTEGER PRIMARY KEY, ip TEXT NOT NULL UNIQUE,
		    reason TEXT NOT NULL DEFAULT '', created_at DATETIME);
		CREATE TABLE autoblock_rules (
		    id INTEGER PRIMARY KEY, pattern TEXT NOT NULL UNIQUE,
		    description TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1,
		    hit_count INTEGER NOT NULL DEFAULT 0, last_hit_at DATETIME, created_at DATETIME);
		CREATE TABLE honeypots (
		    id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE,
		    description TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1,
		    hit_count INTEGER NOT NULL DEFAULT 0, last_hit_at DATETIME, created_at DATETIME);
		CREATE TABLE bot_patterns (
		    id INTEGER PRIMARY KEY, pattern TEXT NOT NULL UNIQUE, label TEXT NOT NULL,
		    block INTEGER NOT NULL DEFAULT 0, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
		CREATE TABLE alert_rules (
		    id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, type TEXT NOT NULL,
		    enabled INTEGER NOT NULL DEFAULT 1, threshold INTEGER NOT NULL DEFAULT 0,
		    window_minutes INTEGER NOT NULL DEFAULT 5, cooldown_minutes INTEGER NOT NULL DEFAULT 15,
		    last_fired_at DATETIME, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
		CREATE TABLE alert_log (
		    id INTEGER PRIMARY KEY, rule_id INTEGER NOT NULL REFERENCES alert_rules(id),
		    type TEXT NOT NULL, message TEXT NOT NULL, details TEXT NOT NULL DEFAULT '{}',
		    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);

		INSERT INTO blocked_ips (ip) VALUES ('203.0.113.9');
		INSERT INTO autoblock_rules (pattern) VALUES ('/wp-login');
		INSERT INTO honeypots (path) VALUES ('/admin/login');
		INSERT INTO bot_patterns (pattern, label, block) VALUES ('AhrefsBot', 'Ahrefs', 1);
		INSERT INTO alert_rules (id, name, type, threshold, window_minutes, cooldown_minutes)
		    VALUES (1, 'Auto-Block', 'auto_block', 1, 1, 5), (2, 'App Error', 'app_error', 3, 5, 15);
		INSERT INTO alert_log (rule_id, type, message) VALUES (1, 'auto_block', 'blocked 203.0.113.9');
	`); err != nil {
		t.Fatalf("build legacy database: %v", err)
	}
	legacy.Close()

	d, err := Open(path, schemaPath(t))
	if err != nil {
		t.Fatalf("Open on legacy database: %v", err)
	}
	defer d.Close()

	for _, table := range []string{"blocked_ips", "autoblock_rules", "honeypots"} {
		var n int
		if err := d.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("table %s survived the migration", table)
		}
	}

	var blockCols int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('bot_patterns') WHERE name = 'block'`,
	).Scan(&blockCols); err != nil {
		t.Fatal(err)
	}
	if blockCols != 0 {
		t.Error("bot_patterns.block survived the migration")
	}

	// The seeded pattern must survive: detection is the reason the table stays.
	var patterns int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM bot_patterns WHERE pattern = 'AhrefsBot'`,
	).Scan(&patterns); err != nil {
		t.Fatal(err)
	}
	if patterns != 1 {
		t.Errorf("bot pattern lost during column drop: got %d rows, want 1", patterns)
	}

	var autoBlockRules, autoBlockLogs int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM alert_rules WHERE type = 'auto_block'`,
	).Scan(&autoBlockRules); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM alert_log WHERE type = 'auto_block'`,
	).Scan(&autoBlockLogs); err != nil {
		t.Fatal(err)
	}
	if autoBlockRules != 0 || autoBlockLogs != 0 {
		t.Errorf("auto_block artifacts remain: %d rules, %d log rows", autoBlockRules, autoBlockLogs)
	}

	// An existing App Error rule keeps its identity but adopts the per-app
	// thresholds; the schema seed alone would not touch an already-present row.
	var threshold, cooldown int
	if err := d.QueryRow(
		`SELECT threshold, cooldown_minutes FROM alert_rules WHERE type = 'app_error'`,
	).Scan(&threshold, &cooldown); err != nil {
		t.Fatal(err)
	}
	if threshold != 1 || cooldown != 30 {
		t.Errorf("app_error rule not migrated: threshold=%d cooldown=%d, want 1/30", threshold, cooldown)
	}

	// The downtime rule ships in the schema seed rather than a migration, so a
	// pre-existing database has to pick it up on boot or uptime alerts stay dead.
	var downtime int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM alert_rules WHERE type = 'downtime'`,
	).Scan(&downtime); err != nil {
		t.Fatal(err)
	}
	if downtime != 1 {
		t.Errorf("downtime rule missing after upgrade: got %d rows, want 1", downtime)
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

	if err := Rollup(ctx, q, true); err != nil {
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
		if err := Rollup(ctx, q, true); err != nil {
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

	if err := Rollup(ctx, q, true); err != nil {
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
