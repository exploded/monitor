package handlers

import (
	"log/slog"
	"net/http"

	db "github.com/exploded/monitor/db/sqlc"
	"github.com/exploded/monitor/internal/traffic"
)

// Dashboard renders the main dashboard page.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	snap, err := h.traffic.Get(r.Context())
	if err != nil {
		slog.Error("traffic snapshot", "err", err)
		snap = &traffic.Snapshot{}
	}
	h.render(w, r, "dashboard", "", PageData{
		Title: "Dashboard",
		Extra: trafficExtra(snap),
	})
}

// TrafficOverview renders the traffic stats panel (polled by HTMX every 60s).
// It reads the snapshot the background refresher keeps, so it does no queries.
func (h *Handler) TrafficOverview(w http.ResponseWriter, r *http.Request) {
	snap, err := h.traffic.Get(r.Context())
	if err != nil {
		// An error status leaves the last good panel in place.
		slog.Error("traffic snapshot", "err", err)
		http.Error(w, "traffic unavailable", http.StatusServiceUnavailable)
		return
	}

	tmpl, ok := h.pages["dashboard"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "_traffic_overview", PageData{Extra: trafficExtra(snap)}); err != nil {
		slog.Error("render traffic overview", "err", err)
	}
}

func trafficExtra(s *traffic.Snapshot) map[string]any {
	return map[string]any{
		"AsOf":         s.AsOf,
		"Total":        s.Total,
		"Bots":         s.Bots,
		"UniqueIPs":    s.UniqueIPs,
		"TopIPs":       s.TopIPs,
		"TopUAs":       s.TopUAs,
		"ByHost":       s.ByHost,
		"StatusCodes":  s.StatusCodes,
		"Sparklines":   s.Sparklines,
		"IPScores":     s.IPScores,
		"TopCountries": s.TopCountries,
		"TopReferrers": s.TopReferrers,
	}
}

// RecentRequestsPage renders the full recent requests page.
func (h *Handler) RecentRequestsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since, rng := parseRange(r)

	total, _ := h.q.CountRequestsSince(ctx, since)
	bots, _ := h.q.CountBotRequestsSince(ctx, since)
	uniqueIPs, _ := h.q.CountUniqueIPsSince(ctx, since)
	recent, _ := h.q.RecentRequestsSince(ctx, db.RecentRequestsSinceParams{Ts: since, Limit: 100})

	h.render(w, r, "requests", "", PageData{
		Title: "Requests",
		Extra: map[string]any{
			"Total":      total,
			"Bots":       bots,
			"UniqueIPs":  uniqueIPs,
			"Recent":     recent,
			"Range":      rng,
			"RangeLabel": rangeLabels[rng],
		},
	})
}

// RequestsPartial renders the requests data partial (for HTMX range updates).
func (h *Handler) RequestsPartial(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since, rng := parseRange(r)

	total, _ := h.q.CountRequestsSince(ctx, since)
	bots, _ := h.q.CountBotRequestsSince(ctx, since)
	uniqueIPs, _ := h.q.CountUniqueIPsSince(ctx, since)
	recent, _ := h.q.RecentRequestsSince(ctx, db.RecentRequestsSinceParams{Ts: since, Limit: 100})

	tmpl, ok := h.pages["requests"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{
		"Total":      total,
		"Bots":       bots,
		"UniqueIPs":  uniqueIPs,
		"Recent":     recent,
		"RangeLabel": rangeLabels[rng],
	}}
	if err := tmpl.ExecuteTemplate(w, "_requests_data", data); err != nil {
		slog.Error("render requests partial", "err", err)
	}
}
