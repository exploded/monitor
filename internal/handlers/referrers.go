package handlers

import (
	"log/slog"
	"net/http"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
)

// ReferrersPage renders the referrers breakdown page.
func (h *Handler) ReferrersPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since, rng := parseRange(r)
	host := r.URL.Query().Get("host")

	// Get all hosts for the dropdown
	hostsSince := time.Now().UTC().Add(-30 * 24 * time.Hour)
	hosts, _ := h.q.DistinctHosts(ctx, hostsSince)

	// Top referrers (filtered by host if set)
	referrers, _ := h.q.ReferrersByHost(ctx, db.ReferrersByHostParams{
		Since:      since,
		HostFilter: host,
		Lim:        50,
	})

	// Recent referred requests
	recentReferred, _ := h.q.ReferrerRequestsByHost(ctx, db.ReferrerRequestsByHostParams{
		Since:      since,
		HostFilter: host,
		Lim:        100,
	})

	h.render(w, r, "referrers", "", PageData{
		Title: "Referrers",
		Extra: map[string]any{
			"Referrers":      referrers,
			"RecentReferred": recentReferred,
			"Hosts":          hosts,
			"SelectedHost":   host,
			"Range":          rng,
			"RangeLabel":     rangeLabels[rng],
		},
	})
}

// ReferrersPartial renders the referrers data partial (for HTMX filter updates).
func (h *Handler) ReferrersPartial(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since, rng := parseRange(r)
	host := r.URL.Query().Get("host")

	referrers, _ := h.q.ReferrersByHost(ctx, db.ReferrersByHostParams{
		Since:      since,
		HostFilter: host,
		Lim:        50,
	})

	recentReferred, _ := h.q.ReferrerRequestsByHost(ctx, db.ReferrerRequestsByHostParams{
		Since:      since,
		HostFilter: host,
		Lim:        100,
	})

	tmpl, ok := h.pages["referrers"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{
		"Referrers":      referrers,
		"RecentReferred": recentReferred,
		"SelectedHost":   host,
		"RangeLabel":     rangeLabels[rng],
	}}
	if err := tmpl.ExecuteTemplate(w, "_referrers_data", data); err != nil {
		slog.Error("render referrers partial", "err", err)
	}
}
