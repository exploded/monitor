package traffic

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
	"github.com/exploded/monitor/internal/database"
)

func TestSummarise(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 30, 30, 0, time.UTC)
	row := func(ago time.Duration, host, ip string, status int64, ua string, bot bool, country, ref string) db.TrafficRowsSinceRow {
		r := db.TrafficRowsSinceRow{Ts: now.Add(-ago), Host: host, ClientIp: ip, Status: status, UserAgent: ua, Country: country, Referer: ref}
		if bot {
			r.IsBot = 1
		}
		return r
	}
	var rows []db.TrafficRowsSinceRow
	// A scanner: 12 requests in two minutes, all 404, scores high enough to badge.
	for i := range 12 {
		rows = append(rows, row(time.Duration(i)*10*time.Second, "a.example", "1.1.1.1", 404, "zgrab", false, "US", ""))
	}
	// A crawler: bot traffic spread over hours, scores 25 and gets no badge.
	for i := range 5 {
		rows = append(rows, row(time.Duration(i+2)*time.Hour, "b.example", "2.2.2.2", 200, "Googlebot", true, "US", "https://www.google.com/"))
	}
	// Two visitors below the scoring threshold, tied on count.
	rows = append(rows,
		row(3*time.Hour, "b.example", "4.4.4.4", 200, "Firefox", false, "AU", ""),
		row(3*time.Hour, "b.example", "3.3.3.3", 500, "Firefox", false, "", ""),
	)

	s := Summarise(rows, now)

	if s.Total != 19 || s.Bots != 5 || s.UniqueIPs != 4 {
		t.Errorf("total/bots/unique = %d/%d/%d, want 19/5/4", s.Total, s.Bots, s.UniqueIPs)
	}
	wantIPs := []IPCount{{"1.1.1.1", 12}, {"2.2.2.2", 5}, {"3.3.3.3", 1}, {"4.4.4.4", 1}}
	if !slices.Equal(s.TopIPs, wantIPs) {
		t.Errorf("TopIPs = %v, want %v (ties by key)", s.TopIPs, wantIPs)
	}
	if got := s.IPScores["1.1.1.1"]; got < 51 {
		t.Errorf("scanner score %d, want >= 51", got)
	}
	if _, ok := s.IPScores["2.2.2.2"]; ok {
		t.Errorf("crawler scored %d, want no badge", s.IPScores["2.2.2.2"])
	}
	wantUAs := []UACount{{"zgrab", 12, false}, {"Googlebot", 5, true}, {"Firefox", 2, false}}
	if !slices.Equal(s.TopUAs, wantUAs) {
		t.Errorf("TopUAs = %v, want %v", s.TopUAs, wantUAs)
	}
	if want := []HostCount{{"a.example", 12}, {"b.example", 7}}; !slices.Equal(s.ByHost, want) {
		t.Errorf("ByHost = %v, want %v", s.ByHost, want)
	}
	if want := []StatusCount{{200, 6}, {404, 12}, {500, 1}}; !slices.Equal(s.StatusCodes, want) {
		t.Errorf("StatusCodes = %v, want %v (by status)", s.StatusCodes, want)
	}
	if want := []RefererCount{{"https://www.google.com/", 5}}; !slices.Equal(s.TopReferrers, want) {
		t.Errorf("TopReferrers = %v, want %v", s.TopReferrers, want)
	}
	if want := []CountryCount{{"US", 17}, {"AU", 1}}; !slices.Equal(s.TopCountries, want) {
		t.Errorf("TopCountries = %v, want %v", s.TopCountries, want)
	}

	// Only a.example had traffic in the last hour: 4 requests in the current
	// minute (10:30), 6 in 10:29 and 2 in 10:28.
	if _, ok := s.Sparklines["b.example"]; ok {
		t.Error("b.example has a sparkline but no traffic in the last hour")
	}
	pts := strings.Fields(s.Sparklines["a.example"])
	if len(pts) != sparkMinutes {
		t.Fatalf("sparkline has %d points, want %d", len(pts), sparkMinutes)
	}
	if pts[0] != "0,20" {
		t.Errorf("first point %q, want 0,20 (no traffic an hour ago)", pts[0])
	}
	// 10:30 is partial and left out, so 10:29 is the last point and the peak.
	if pts[58] != "58,14" || pts[59] != "59,2" {
		t.Errorf("last points %q %q, want 58,14 59,2", pts[58], pts[59])
	}
}

func TestSummariseEmpty(t *testing.T) {
	s := Summarise(nil, time.Now())
	if s.Total != 0 || len(s.TopIPs) != 0 || len(s.Sparklines) != 0 {
		t.Errorf("empty input gave %+v", s)
	}
}

// TestCacheRebuildsOnDemand walks a snapshot through its three ages: fresh is
// served as is, stale is served while a rebuild runs, and very stale waits for
// the rebuild.
func TestCacheRebuildsOnDemand(t *testing.T) {
	d, err := database.Open(filepath.Join(t.TempDir(), "test.db"), filepath.Join("..", "..", "db", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	q := db.New(d)
	ctx := context.Background()
	insert := func() {
		t.Helper()
		if err := q.InsertRequest(ctx, db.InsertRequestParams{Ts: time.Now().UTC(), Host: "a.example", ClientIp: "1.1.1.1", Method: "GET", Uri: "/", Status: 200}); err != nil {
			t.Fatal(err)
		}
	}
	c := NewCache(ctx, q)
	age := func(d time.Duration) {
		s := *c.snap.Load()
		s.AsOf = time.Now().Add(-d)
		c.snap.Store(&s)
	}

	insert()
	s, err := c.Get(ctx)
	if err != nil || s.Total != 1 {
		t.Fatalf("first Get: total %d, err %v; want 1 built inline", s.Total, err)
	}

	insert()
	if s, _ := c.Get(ctx); s.Total != 1 {
		t.Errorf("fresh: total %d, want the cached 1", s.Total)
	}

	age(2 * time.Minute)
	if s, _ := c.Get(ctx); s.Total != 1 {
		t.Errorf("stale: total %d, want the cached 1 served without waiting", s.Total)
	}
	deadline := time.Now().Add(5 * time.Second)
	for c.snap.Load().Total != 2 {
		if time.Now().After(deadline) {
			t.Fatal("stale snapshot was not rebuilt in the background")
		}
		time.Sleep(10 * time.Millisecond)
	}

	insert()
	age(10 * time.Minute)
	if s, _ := c.Get(ctx); s.Total != 3 {
		t.Errorf("very stale: total %d, want 3 rebuilt before replying", s.Total)
	}
}
