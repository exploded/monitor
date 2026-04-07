package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
	"github.com/exploded/monitor/internal/reputation"
)

// Dashboard renders the main dashboard page.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	ctx := r.Context()
	since := time.Now().UTC().Add(-24 * time.Hour)

	total, _ := h.q.CountRequestsSince(ctx, since)
	bots, _ := h.q.CountBotRequestsSince(ctx, since)
	uniqueIPs, _ := h.q.CountUniqueIPsSince(ctx, since)
	topIPs, _ := h.q.TopIPsSince(ctx, db.TopIPsSinceParams{Ts: since, Limit: 10})
	topUAs, _ := h.q.TopUserAgentsSince(ctx, db.TopUserAgentsSinceParams{Ts: since, Limit: 10})
	byHost, _ := h.q.RequestsByHostSince(ctx, since)
	statusCodes, _ := h.q.StatusCodesSince(ctx, since)
	recent, _ := h.q.RecentRequests(ctx, 50)
	topCountries, _ := h.q.TopCountriesSince(ctx, db.TopCountriesSinceParams{Ts: since, Limit: 15})
	topReferrers, _ := h.q.TopReferrersSince(ctx, db.TopReferrersSinceParams{Ts: since, Limit: 10})

	// IP reputation scores
	repData, _ := h.q.IPReputationData(ctx, db.IPReputationDataParams{Ts: since, Limit: 50})
	blockedIPs, _ := h.q.ListBlockedIPs(ctx)
	blockedSet := make(map[string]bool, len(blockedIPs))
	for _, ip := range blockedIPs {
		blockedSet[ip.Ip] = true
	}
	ipScores := make(map[string]int)
	for _, row := range repData {
		var firstSeen, lastSeen time.Time
		if t, ok := row.FirstSeen.(time.Time); ok {
			firstSeen = t
		}
		if t, ok := row.LastSeen.(time.Time); ok {
			lastSeen = t
		}
		score := reputation.Compute(reputation.IPData{
			ClientIP:  row.ClientIp,
			Total:     row.Total,
			Count4xx:  int64(row.Count4xx.Float64),
			BotCount:  int64(row.BotCount.Float64),
			FirstSeen: firstSeen,
			LastSeen:  lastSeen,
			IsBlocked: blockedSet[row.ClientIp],
		})
		if score.Value >= 26 {
			ipScores[row.ClientIp] = score.Value
		}
	}

	// Sparklines for initial dashboard load
	sparkSince := time.Now().UTC().Add(-60 * time.Minute)
	minuteRows, _ := h.q.MinutelyRequestCountsByHost(ctx, sparkSince)
	hostMinutes := make(map[string]map[string]int64)
	for _, row := range minuteRows {
		min := ""
		if s, ok := row.Minute.(string); ok {
			min = s
		}
		if _, exists := hostMinutes[row.Host]; !exists {
			hostMinutes[row.Host] = make(map[string]int64)
		}
		hostMinutes[row.Host][min] = row.Cnt
	}
	sparklines := make(map[string]string)
	for host, minutes := range hostMinutes {
		counts := make([]int64, 60)
		var maxC int64
		for i := 0; i < 60; i++ {
			t := sparkSince.Add(time.Duration(i) * time.Minute)
			key := t.Format("2006-01-02 15:04")
			counts[i] = minutes[key]
			if counts[i] > maxC {
				maxC = counts[i]
			}
		}
		if maxC == 0 {
			maxC = 1
		}
		pts := make([]string, 60)
		for i, c := range counts {
			y := 20 - int(float64(c)/float64(maxC)*18)
			pts[i] = fmt.Sprintf("%d,%d", i, y)
		}
		sparklines[host] = strings.Join(pts, " ")
	}

	h.render(w, r, "dashboard", "", PageData{
		Title: "Dashboard",
		Extra: map[string]any{
			"Total":         total,
			"Bots":          bots,
			"UniqueIPs":     uniqueIPs,
			"TopIPs":        topIPs,
			"TopUAs":        topUAs,
			"ByHost":        byHost,
			"StatusCodes":   statusCodes,
			"Recent":        recent,
			"Sparklines":    sparklines,
			"IPScores":      ipScores,
			"TopCountries":  topCountries,
			"TopReferrers":  topReferrers,
		},
	})
}

// TrafficOverview renders the traffic stats panel (polled by HTMX every 30s).
func (h *Handler) TrafficOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since := time.Now().UTC().Add(-24 * time.Hour)

	total, _ := h.q.CountRequestsSince(ctx, since)
	bots, _ := h.q.CountBotRequestsSince(ctx, since)
	uniqueIPs, _ := h.q.CountUniqueIPsSince(ctx, since)
	topIPs, _ := h.q.TopIPsSince(ctx, db.TopIPsSinceParams{Ts: since, Limit: 10})
	topUAs, _ := h.q.TopUserAgentsSince(ctx, db.TopUserAgentsSinceParams{Ts: since, Limit: 10})
	byHost, _ := h.q.RequestsByHostSince(ctx, since)
	statusCodes, _ := h.q.StatusCodesSince(ctx, since)
	topReferrers, _ := h.q.TopReferrersSince(ctx, db.TopReferrersSinceParams{Ts: since, Limit: 10})

	// IP reputation scores for top IPs
	repData, _ := h.q.IPReputationData(ctx, db.IPReputationDataParams{Ts: since, Limit: 50})
	blockedIPs, _ := h.q.ListBlockedIPs(ctx)
	blockedSet := make(map[string]bool, len(blockedIPs))
	for _, ip := range blockedIPs {
		blockedSet[ip.Ip] = true
	}
	ipScores := make(map[string]int)
	for _, row := range repData {
		var firstSeen, lastSeen time.Time
		if t, ok := row.FirstSeen.(time.Time); ok {
			firstSeen = t
		}
		if t, ok := row.LastSeen.(time.Time); ok {
			lastSeen = t
		}
		score := reputation.Compute(reputation.IPData{
			ClientIP:  row.ClientIp,
			Total:     row.Total,
			Count4xx:  int64(row.Count4xx.Float64),
			BotCount:  int64(row.BotCount.Float64),
			FirstSeen: firstSeen,
			LastSeen:  lastSeen,
			IsBlocked: blockedSet[row.ClientIp],
		})
		if score.Value >= 26 {
			ipScores[row.ClientIp] = score.Value
		}
	}

	// Build sparkline SVG points per host (last 60 minutes)
	sparkSince := time.Now().UTC().Add(-60 * time.Minute)
	minuteRows, _ := h.q.MinutelyRequestCountsByHost(ctx, sparkSince)

	// Group by host
	hostMinutes := make(map[string]map[string]int64)
	for _, row := range minuteRows {
		min := ""
		if s, ok := row.Minute.(string); ok {
			min = s
		}
		if _, exists := hostMinutes[row.Host]; !exists {
			hostMinutes[row.Host] = make(map[string]int64)
		}
		hostMinutes[row.Host][min] = row.Cnt
	}

	// Generate SVG points string per host
	sparklines := make(map[string]string)
	for host, minutes := range hostMinutes {
		counts := make([]int64, 60)
		var maxC int64
		for i := 0; i < 60; i++ {
			t := sparkSince.Add(time.Duration(i) * time.Minute)
			key := t.Format("2006-01-02 15:04")
			counts[i] = minutes[key]
			if counts[i] > maxC {
				maxC = counts[i]
			}
		}
		if maxC == 0 {
			maxC = 1
		}
		pts := make([]string, 60)
		for i, c := range counts {
			y := 20 - int(float64(c)/float64(maxC)*18)
			pts[i] = fmt.Sprintf("%d,%d", i, y)
		}
		sparklines[host] = strings.Join(pts, " ")
	}

	data := PageData{
		Extra: map[string]any{
			"Total":       total,
			"Bots":        bots,
			"UniqueIPs":   uniqueIPs,
			"TopIPs":      topIPs,
			"TopUAs":      topUAs,
			"ByHost":      byHost,
			"StatusCodes": statusCodes,
			"Sparklines":    sparklines,
			"IPScores":      ipScores,
			"TopReferrers":  topReferrers,
		},
	}

	tmpl, ok := h.pages["dashboard"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "_traffic_overview", data); err != nil {
		slog.Error("render traffic overview", "err", err)
	}
}

// RecentRequests renders the recent requests table (polled by HTMX).
func (h *Handler) RecentRequests(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	recent, _ := h.q.RecentRequests(ctx, 50)

	data := PageData{
		Extra: map[string]any{
			"Recent": recent,
		},
	}

	tmpl, ok := h.pages["dashboard"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "_recent_requests", data); err != nil {
		slog.Error("render recent requests", "err", err)
	}
}
