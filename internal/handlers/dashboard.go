package handlers

import (
	"log/slog"
	"net/http"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
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
	botPatterns, _ := h.q.ListBotPatterns(ctx)
	blockedIPs, _ := h.q.ListBlockedIPs(ctx)
	appErrors, _ := h.q.RecentAppErrors(ctx, 20)
	appErrorCount, _ := h.q.CountAppErrorsSince(ctx, since)
	appLogCount, _ := h.q.CountAppLogsSince(ctx, since)

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
			"BotPatterns":   botPatterns,
			"BlockedIPs":    blockedIPs,
			"AppErrors":     appErrors,
			"AppErrorCount": appErrorCount,
			"AppLogCount":   appLogCount,
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

	data := PageData{
		Extra: map[string]any{
			"Total":       total,
			"Bots":        bots,
			"UniqueIPs":   uniqueIPs,
			"TopIPs":      topIPs,
			"TopUAs":      topUAs,
			"ByHost":      byHost,
			"StatusCodes": statusCodes,
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
