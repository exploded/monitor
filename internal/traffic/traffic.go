// Package traffic builds the dashboard's 24h traffic summary.
//
// The panel used to run ten aggregate queries on every HTMX poll, about 4s in
// all on production, holding the single SQLite connection the whole time. It
// now reads the window's rows once, aggregates them in Go, and does that on a
// background ticker so a page load or poll only reads the latest result.
package traffic

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
	"github.com/exploded/monitor/internal/reputation"
)

const (
	window       = 24 * time.Hour
	sparkMinutes = 60
	// Below this the reputation score is noise; the 26 cutoff matches
	// reputation.BadgeClass.
	minScoredRequests = 3
	minShownScore     = 26
)

type IPCount struct {
	ClientIP string
	Cnt      int64
}

type UACount struct {
	UserAgent string
	Cnt       int64
	IsBot     bool
}

type HostCount struct {
	Host string
	Cnt  int64
}

type StatusCount struct {
	Status int64
	Cnt    int64
}

type RefererCount struct {
	Referer string
	Cnt     int64
}

type CountryCount struct {
	Country string
	Cnt     int64
}

// Snapshot is the traffic summary for the 24h before AsOf.
type Snapshot struct {
	AsOf         time.Time
	Total        int64
	Bots         int64
	UniqueIPs    int64
	TopIPs       []IPCount
	TopUAs       []UACount
	ByHost       []HostCount
	StatusCodes  []StatusCount
	TopReferrers []RefererCount
	TopCountries []CountryCount
	// IPScores holds threat scores worth a badge, for the IPs in TopIPs only.
	IPScores map[string]int
	// Sparklines maps host to SVG polyline points for its last 60 minutes.
	Sparklines map[string]string
}

type ipStats struct {
	total, count4xx, bots int64
	first, last           time.Time
}

// Summarise aggregates rows (every request since now-24h) in one pass.
func Summarise(rows []db.TrafficRowsSinceRow, now time.Time) *Snapshot {
	s := &Snapshot{AsOf: now, Total: int64(len(rows))}

	ips := make(map[string]*ipStats)
	uas := make(map[string]int64)
	uaBot := make(map[string]bool)
	hosts := make(map[string]int64)
	statuses := make(map[int64]int64)
	referers := make(map[string]int64)
	countries := make(map[string]int64)

	// Sparkline buckets are whole minutes; the current, partial minute is left
	// out so the line doesn't dip at its right-hand end.
	sparkSince := now.Add(-sparkMinutes * time.Minute)
	sparkStart := sparkSince.Truncate(time.Minute)
	minutes := make(map[string]*[sparkMinutes]int64)

	for _, r := range rows {
		bot := r.IsBot == 1
		if bot {
			s.Bots++
		}

		ip := ips[r.ClientIp]
		if ip == nil {
			ip = &ipStats{first: r.Ts, last: r.Ts}
			ips[r.ClientIp] = ip
		}
		ip.total++
		if r.Status >= 400 && r.Status < 500 {
			ip.count4xx++
		}
		if bot {
			ip.bots++
		}
		if r.Ts.Before(ip.first) {
			ip.first = r.Ts
		}
		if r.Ts.After(ip.last) {
			ip.last = r.Ts
		}

		uas[r.UserAgent]++
		if bot {
			uaBot[r.UserAgent] = true
		}
		hosts[r.Host]++
		statuses[r.Status]++
		if r.Referer != "" {
			referers[r.Referer]++
		}
		if r.Country != "" {
			countries[r.Country]++
		}

		if !r.Ts.Before(sparkSince) {
			m := minutes[r.Host]
			if m == nil {
				m = new([sparkMinutes]int64)
				minutes[r.Host] = m
			}
			if i := int(r.Ts.Truncate(time.Minute).Sub(sparkStart) / time.Minute); i >= 0 && i < sparkMinutes {
				m[i]++
			}
		}
	}

	s.UniqueIPs = int64(len(ips))

	ipTotals := make(map[string]int64, len(ips))
	for k, v := range ips {
		ipTotals[k] = v.total
	}
	s.IPScores = make(map[string]int)
	for _, k := range topN(ipTotals, 10) {
		st := ips[k]
		s.TopIPs = append(s.TopIPs, IPCount{ClientIP: k, Cnt: st.total})
		if st.total < minScoredRequests {
			continue
		}
		score := reputation.Compute(reputation.IPData{
			ClientIP:  k,
			Total:     st.total,
			Count4xx:  st.count4xx,
			BotCount:  st.bots,
			FirstSeen: st.first,
			LastSeen:  st.last,
		})
		if score.Value >= minShownScore {
			s.IPScores[k] = score.Value
		}
	}

	for _, k := range topN(uas, 10) {
		s.TopUAs = append(s.TopUAs, UACount{UserAgent: k, Cnt: uas[k], IsBot: uaBot[k]})
	}
	for _, k := range topN(hosts, 0) {
		s.ByHost = append(s.ByHost, HostCount{Host: k, Cnt: hosts[k]})
	}
	for _, k := range topN(referers, 10) {
		s.TopReferrers = append(s.TopReferrers, RefererCount{Referer: k, Cnt: referers[k]})
	}
	for _, k := range topN(countries, 15) {
		s.TopCountries = append(s.TopCountries, CountryCount{Country: k, Cnt: countries[k]})
	}
	for _, k := range slices.Sorted(maps.Keys(statuses)) {
		s.StatusCodes = append(s.StatusCodes, StatusCount{Status: k, Cnt: statuses[k]})
	}

	s.Sparklines = make(map[string]string, len(minutes))
	for host, counts := range minutes {
		s.Sparklines[host] = sparkline(counts)
	}
	return s
}

// sparkline scales counts into a 59x20 viewBox, leaving a 2px margin at the top.
func sparkline(counts *[sparkMinutes]int64) string {
	maxC := int64(1)
	for _, c := range counts {
		maxC = max(maxC, c)
	}
	pts := make([]string, sparkMinutes)
	for i, c := range counts {
		y := 20 - int(float64(c)/float64(maxC)*18)
		pts[i] = fmt.Sprintf("%d,%d", i, y)
	}
	return strings.Join(pts, " ")
}

// topN returns m's keys by descending count, ties broken by key so the order is
// stable between refreshes. n <= 0 returns every key.
func topN[K cmp.Ordered](m map[K]int64, n int) []K {
	keys := slices.SortedFunc(maps.Keys(m), func(a, b K) int {
		if c := cmp.Compare(m[b], m[a]); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	if n > 0 && len(keys) > n {
		keys = keys[:n]
	}
	return keys
}

// Cache holds the latest Snapshot and refreshes it in the background.
type Cache struct {
	q    *db.Queries
	mu   sync.Mutex // serialises refreshes
	snap atomic.Pointer[Snapshot]
}

func NewCache(q *db.Queries) *Cache {
	return &Cache{q: q}
}

// Run refreshes the snapshot now and then every interval until ctx ends.
func (c *Cache) Run(ctx context.Context, interval time.Duration) {
	c.refresh(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.refresh(ctx)
		}
	}
}

// Get returns the latest snapshot. Until the first background pass lands it
// computes one inline, so a request at startup is slow once rather than empty.
func (c *Cache) Get(ctx context.Context) (*Snapshot, error) {
	if s := c.snap.Load(); s != nil {
		return s, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if s := c.snap.Load(); s != nil {
		return s, nil
	}
	return c.compute(ctx)
}

func (c *Cache) refresh(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.compute(ctx); err != nil && ctx.Err() == nil {
		slog.Error("traffic snapshot", "err", err)
	}
}

func (c *Cache) compute(ctx context.Context) (*Snapshot, error) {
	start := time.Now()
	now := start.UTC()
	rows, err := c.q.TrafficRowsSince(ctx, now.Add(-window))
	if err != nil {
		return nil, err
	}
	s := Summarise(rows, now)
	c.snap.Store(s)
	if d := time.Since(start); d > 2*time.Second {
		slog.Warn("traffic snapshot slow", "rows", len(rows), "dur", d)
	}
	return s, nil
}
