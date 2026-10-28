package handlers

import (
	"testing"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
)

func TestFormatMs(t *testing.T) {
	cases := map[float64]string{
		1: "1 ms", 109: "109 ms", 999: "999 ms",
		1000: "1 s", 3693: "3.7 s", 25005: "25 s",
	}
	for in, want := range cases {
		if got := formatMs(in); got != want {
			t.Errorf("formatMs(%v) = %q, want %q", in, got, want)
		}
	}
}

// The axis must reach at least 1 s, extend a decade at a time to cover the
// worst p99, and put larger values higher (smaller y).
func TestLatencyAxis(t *testing.T) {
	_, ticks := latencyAxis(50)
	if got := len(ticks); got != 4 || ticks[3].Label != "1 s" {
		t.Fatalf("max 50 ms: ticks %+v, want 1 ms to 1 s", ticks)
	}
	y, ticks := latencyAxis(25005)
	if got := ticks[len(ticks)-1].Label; got != "100 s" {
		t.Errorf("max 25 s: top tick %q, want 100 s", got)
	}
	if ticks[0].Y != latencyBottom || ticks[len(ticks)-1].Y != latencyTop {
		t.Errorf("ticks span y %d..%d, want %d..%d", ticks[0].Y, ticks[len(ticks)-1].Y, latencyBottom, latencyTop)
	}
	if !(y(2) > y(109) && y(109) > y(3693)) {
		t.Errorf("y not decreasing: y(2)=%d y(109)=%d y(3693)=%d", y(2), y(109), y(3693))
	}
	if y(0) != latencyBottom || y(0.4) != latencyBottom {
		t.Errorf("sub-millisecond values should sit on the floor, got %d and %d", y(0), y(0.4))
	}
}

func TestComputeLatencyPoints(t *testing.T) {
	var rows []db.HourlyDurationsRow
	for i := 0; i < 50; i++ { // with 51 values, p99 is the largest
		rows = append(rows, db.HourlyDurationsRow{Hour: "2026-10-25 04:00", DurationMs: 2})
	}
	rows = append(rows, db.HourlyDurationsRow{Hour: "2026-10-25 04:00", DurationMs: 3693})
	points, ticks, _ := computeLatencyPoints(rows)
	if len(points) != 1 {
		t.Fatalf("got %d points, want 1", len(points))
	}
	p := points[0]
	if p.Hour != "2026-10-25 15:00" {
		t.Errorf("hour %q, want Melbourne time 2026-10-25 15:00", p.Hour)
	}
	if p.P50 != 2 || p.P99 != 3693 {
		t.Errorf("p50 %v p99 %v, want 2 and 3693", p.P50, p.P99)
	}
	if len(ticks) != 5 { // 1 ms, 10 ms, 100 ms, 1 s, 10 s
		t.Errorf("got %d ticks, want 5", len(ticks))
	}
}

func TestParseSearchTime(t *testing.T) {
	// 25 October 2026 is in AEDT (UTC+11).
	got, ok := parseSearchTime("2026-10-25T15:00", false)
	if want := time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC); !ok || !got.Equal(want) {
		t.Errorf("datetime-local: got %v, want %v", got, want)
	}
	got, ok = parseSearchTime("2026-10-25", false)
	if want := time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC); !ok || !got.Equal(want) {
		t.Errorf("date as start: got %v, want %v (Melbourne midnight)", got, want)
	}
	got, ok = parseSearchTime("2026-10-25", true)
	if want := time.Date(2026, 10, 25, 12, 59, 59, 0, time.UTC); !ok || !got.Equal(want) {
		t.Errorf("date as end: got %v, want %v", got, want)
	}
	if _, ok := parseSearchTime("", false); ok {
		t.Error("empty string parsed")
	}
}
