package handlers

import (
	"log/slog"
	"net/http"

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
// It reads the cached snapshot; a stale one is rebuilt behind the response.
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
		"TopCountries": s.TopCountries,
	}
}
