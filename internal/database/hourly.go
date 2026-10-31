package database

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
)

// hourlyStatsDays is how far back hourly_stats is backfilled and kept. The
// History range selector stops at 7 days; the extra day covers the first,
// partial hour of that range.
const hourlyStatsDays = 8

// HourFormat is the hourly_stats key: the UTC hour, as 'YYYY-MM-DD HH:00'.
const HourFormat = "2006-01-02 15:00"

// SummariseHour aggregates one hour's raw rows. Percentiles use the rule the
// History chart always has: sort, then take index floor(n*q), capped at n-1.
// They cover only rows not excluded by LATENCY_EXCLUDE; every row counts
// towards total, 5xx and bytes.
func SummariseHour(hour string, rows []db.HourRowsRow) db.HourlyStat {
	s := db.HourlyStat{Hour: hour, Total: int64(len(rows))}
	durations := make([]float64, 0, len(rows))
	for _, r := range rows {
		s.Bytes += r.Size
		if r.Status >= 500 {
			s.Errors5xx++
		}
		if r.Excluded == 0 {
			durations = append(durations, r.DurationMs)
		}
	}
	s.LatCount = int64(len(durations))
	if n := len(durations); n > 0 {
		slices.Sort(durations)
		pct := func(q float64) float64 { return durations[min(n-1, int(float64(n)*q))] }
		s.P50, s.P95, s.P99 = pct(0.50), pct(0.95), pct(0.99)
	}
	return s
}

// computeHour summarises the hour starting at start from raw rows.
func computeHour(ctx context.Context, q *db.Queries, start time.Time, latencyExclude string) (db.HourlyStat, error) {
	rows, err := q.HourRows(ctx, db.HourRowsParams{
		LatencyExclude: latencyExclude,
		FromTs:         start,
		ToTs:           start.Add(time.Hour),
	})
	if err != nil {
		return db.HourlyStat{}, err
	}
	return SummariseHour(start.Format(HourFormat), rows), nil
}

// storeHour recomputes one completed hour and upserts it. Empty hours are
// stored too, as zeros, so that a missing row always means "not computed yet".
func storeHour(ctx context.Context, q *db.Queries, start time.Time, latencyExclude string) (db.HourlyStat, error) {
	s, err := computeHour(ctx, q, start, latencyExclude)
	if err != nil {
		return s, err
	}
	return s, q.UpsertHourlyStats(ctx, db.UpsertHourlyStatsParams(s))
}

// RollupHours refreshes hourly_stats.
//
// Every pass recomputes the two most recent completed hours: the watcher
// batches inserts for up to 2s, so an hour computed just after it ended can
// miss its last rows. backfill (startup only, like Rollup's) also fills every
// hour of the last hourlyStatsDays that has no row. Each hour is its own
// bounded range read on idx_requests_ts, so even a full backfill never scans
// the table and hands the connection back between hours.
func RollupHours(ctx context.Context, q *db.Queries, backfill bool, latencyExclude string) error {
	cur := time.Now().UTC().Truncate(time.Hour)
	from := cur.Add(-hourlyStatsDays * 24 * time.Hour)

	hours := []time.Time{cur.Add(-2 * time.Hour), cur.Add(-time.Hour)}
	if backfill {
		have, err := q.HourlyStatsSince(ctx, from.Format(HourFormat))
		if err != nil {
			return fmt.Errorf("read hourly_stats: %w", err)
		}
		present := make(map[string]bool, len(have))
		for _, h := range have {
			present[h.Hour] = true
		}
		for t := from; t.Before(cur.Add(-2 * time.Hour)); t = t.Add(time.Hour) {
			if !present[t.Format(HourFormat)] {
				hours = append(hours, t)
			}
		}
	}

	var firstErr error
	for _, t := range hours {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := storeHour(ctx, q, t, latencyExclude); err != nil {
			slog.Error("rollup hourly_stats", "hour", t.Format(HourFormat), "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if n := len(hours) - 2; n > 0 {
		slog.Info("backfilled hourly_stats", "hours", n)
	}

	if err := q.DeleteHourlyStatsBefore(ctx, from.Format(HourFormat)); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// HourlySeries returns per-hour stats from the hour containing since up to and
// including the current hour, oldest first, leaving out hours with no traffic.
//
// Completed hours come from hourly_stats. Any the maintenance pass hasn't
// reached yet (it runs hourly, so usually just the last hour) are computed and
// stored here. The current hour is still filling, so it is always computed
// live from raw and never stored — at most an hour of rows.
func HourlySeries(ctx context.Context, q *db.Queries, since time.Time, latencyExclude string) ([]db.HourlyStat, error) {
	cur := time.Now().UTC().Truncate(time.Hour)
	start := since.UTC().Truncate(time.Hour)

	stored, err := q.HourlyStatsSince(ctx, start.Format(HourFormat))
	if err != nil {
		return nil, err
	}
	byHour := make(map[string]db.HourlyStat, len(stored))
	for _, s := range stored {
		byHour[s.Hour] = s
	}

	var out []db.HourlyStat
	for t := start; !t.After(cur); t = t.Add(time.Hour) {
		key := t.Format(HourFormat)
		s, ok := byHour[key]
		switch {
		case t.Equal(cur):
			if s, err = computeHour(ctx, q, t, latencyExclude); err != nil {
				return nil, err
			}
		case !ok:
			if s, err = storeHour(ctx, q, t, latencyExclude); err != nil {
				return nil, err
			}
		}
		if s.Total > 0 {
			out = append(out, s)
		}
	}
	return out, nil
}
