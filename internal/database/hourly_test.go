package database

import (
	"context"
	"testing"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
)

// With 51 durations, p99 is the largest; the percentile rule is the one the
// History chart used when it read raw rows.
func TestSummariseHour(t *testing.T) {
	var rows []db.HourRowsRow
	for range 50 {
		rows = append(rows, db.HourRowsRow{Status: 200, Size: 10, DurationMs: 2})
	}
	rows = append(rows,
		db.HourRowsRow{Status: 502, Size: 10, DurationMs: 3693},
		db.HourRowsRow{Status: 200, Size: 10, DurationMs: 25005, Excluded: 1},
	)
	s := SummariseHour("2026-10-25 04:00", rows)
	if s.Total != 52 || s.Errors5xx != 1 || s.Bytes != 520 || s.LatCount != 51 {
		t.Errorf("total/5xx/bytes/lat_count = %d/%d/%d/%d, want 52/1/520/51", s.Total, s.Errors5xx, s.Bytes, s.LatCount)
	}
	if s.P50 != 2 || s.P95 != 2 || s.P99 != 3693 {
		t.Errorf("p50/p95/p99 = %v/%v/%v, want 2/2/3693 (excluded long poll left out)", s.P50, s.P95, s.P99)
	}

	if empty := SummariseHour("2026-10-25 05:00", nil); empty.Total != 0 || empty.LatCount != 0 || empty.P99 != 0 {
		t.Errorf("empty hour = %+v, want zeros", empty)
	}
}

// TestRollupHours covers the startup backfill, the lazy fill of an hour the
// maintenance pass hasn't reached, and the live current hour.
func TestRollupHours(t *testing.T) {
	_, q := openTestDB(t)
	ctx := context.Background()
	cur := time.Now().UTC().Truncate(time.Hour)

	insertRequest(t, q, cur.Add(-5*time.Hour+time.Minute), 200, "1.1.1.1")
	insertRequest(t, q, cur.Add(-3*time.Hour+time.Minute), 200, "1.1.1.1")
	insertRequest(t, q, cur.Add(-3*time.Hour+2*time.Minute), 503, "1.1.1.1")
	insertRequest(t, q, cur.Add(time.Second), 200, "1.1.1.1")

	if err := RollupHours(ctx, q, true, "[]"); err != nil {
		t.Fatalf("RollupHours: %v", err)
	}
	stored, err := q.HourlyStatsSince(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	// Every completed hour of the window is stored, empty ones as zeros; the
	// current hour never is.
	if want := hourlyStatsDays * 24; len(stored) != want {
		t.Errorf("stored %d hours, want %d", len(stored), want)
	}
	byHour := map[string]db.HourlyStat{}
	for _, s := range stored {
		byHour[s.Hour] = s
	}
	if _, ok := byHour[cur.Format(HourFormat)]; ok {
		t.Error("current hour was stored")
	}
	if s := byHour[cur.Add(-3*time.Hour).Format(HourFormat)]; s.Total != 2 || s.Errors5xx != 1 || s.Bytes != 200 {
		t.Errorf("cur-3h = %+v, want total 2, 5xx 1, bytes 200", s)
	}

	// An hour the maintenance pass hasn't reached is filled on read.
	if err := q.DeleteHourlyStatsBefore(ctx, "9999"); err != nil {
		t.Fatal(err)
	}
	series, err := HourlySeries(ctx, q, cur.Add(-6*time.Hour), "[]")
	if err != nil {
		t.Fatalf("HourlySeries: %v", err)
	}
	var got []string
	for _, s := range series {
		got = append(got, s.Hour)
	}
	want := []string{
		cur.Add(-5 * time.Hour).Format(HourFormat),
		cur.Add(-3 * time.Hour).Format(HourFormat),
		cur.Format(HourFormat),
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("series hours = %v, want %v (empty hours left out, current hour live)", got, want)
	}
	refilled, err := q.HourlyStatsSince(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(refilled) != 6 { // cur-6h .. cur-1h, not the current hour
		t.Errorf("HourlySeries stored %d hours, want 6", len(refilled))
	}
}
