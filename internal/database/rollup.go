package database

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
)

// rollupRetentionDays is how long aggregated history is kept. Rollups are tiny
// (one row per day for requests, one per target per day for uptime), so this is
// generous compared with the raw horizons.
const rollupRetentionDays = 400

// Rollup refreshes the daily aggregate tables.
//
// Today and yesterday are always recomputed — today because it is still
// accumulating, yesterday so late-arriving rows are folded in before the day is
// considered final. Any other day present in raw data but missing from the
// rollup is also computed, which both backfills on first run and recovers days
// missed while the process was down.
//
// Must run before Prune, or a day can be deleted from raw before it has been
// aggregated.
//
// backfill controls the catch-up scan for days present in raw but absent from
// the rollup. That scan cannot be bounded by a date range — its whole job is to
// find days the hourly pass never saw, including ones older than the retention
// horizon that Prune is about to delete — so it is a full table scan, measured
// at seconds on a month of traffic. It is only needed after the process has
// missed a day, which means a restart, so it runs once at startup and the
// hourly passes recompute today and yesterday alone.
func Rollup(ctx context.Context, q *db.Queries, backfill bool) error {
	today := time.Now().UTC().Format("2006-01-02")
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")

	days := map[string]bool{today: true, yesterday: true}

	var missing []interface{}
	if backfill {
		var err error
		missing, err = q.DaysMissingFromDailyStats(ctx)
		if err != nil {
			return fmt.Errorf("find missing daily_stats days: %w", err)
		}
		for _, d := range missing {
			days[dayString(d)] = true
		}
	}

	var firstErr error
	for day := range days {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := q.RollupDailyStats(ctx, day); err != nil {
			slog.Error("rollup daily_stats", "day", day, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if n := len(missing); n > 0 {
		slog.Info("backfilled daily_stats", "days", n)
	}

	uptimeDays := map[string]bool{today: true, yesterday: true}
	var missingUptime []interface{}
	if backfill {
		var err error
		missingUptime, err = q.DaysMissingFromUptimeDaily(ctx)
		if err != nil {
			return fmt.Errorf("find missing uptime_daily days: %w", err)
		}
		for _, d := range missingUptime {
			uptimeDays[dayString(d)] = true
		}
	}
	for day := range uptimeDays {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := q.RollupUptimeDaily(ctx, day); err != nil {
			slog.Error("rollup uptime_daily", "day", day, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if n := len(missingUptime); n > 0 {
		slog.Info("backfilled uptime_daily", "days", n)
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -rollupRetentionDays).Format("2006-01-02")
	if err := q.DeleteDailyStatsBefore(ctx, cutoff); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := q.DeleteUptimeDailyBefore(ctx, cutoff); err != nil && firstErr == nil {
		firstErr = err
	}

	return firstErr
}

// dayString normalises what SQLite's date() returns. sqlc types it as interface{}
// because the expression has no declared column type.
func dayString(v interface{}) string {
	switch d := v.(type) {
	case string:
		return d
	case time.Time:
		return d.UTC().Format("2006-01-02")
	case []byte:
		return string(d)
	default:
		return fmt.Sprint(d)
	}
}
