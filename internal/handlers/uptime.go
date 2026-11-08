package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
)

// Uptime renders the full uptime monitoring page.
func (h *Handler) Uptime(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// The page always opens on 24h: the range select only swaps the chart, and
	// the Targets table is headed "Uptime (24h)".
	targets, charts := h.uptimeCharts(ctx, time.Now().UTC().Add(-24*time.Hour))

	type TargetStatus struct {
		Target    db.UptimeTarget
		UptimePct float64
		AvgMs     float64
		LastCheck *db.UptimeCheck
		IsUp      bool
	}

	statuses := make([]TargetStatus, len(targets))
	for i, t := range targets {
		c := charts[i]
		statuses[i] = TargetStatus{Target: t, UptimePct: c.UptimePct, AvgMs: c.AvgMs, LastCheck: c.LastCheck, IsUp: c.IsUp}
		if c.LastCheck == nil {
			// Disabled for over a day, or new: its last check, if any, is older
			// than the window.
			if recent, _ := h.q.RecentUptimeChecks(ctx, db.RecentUptimeChecksParams{TargetID: t.ID, Limit: 1}); len(recent) > 0 {
				statuses[i].LastCheck = &recent[0]
			}
		}
	}

	h.render(w, r, "uptime", "", PageData{
		Title: "Uptime",
		Extra: map[string]any{
			"Statuses":     statuses,
			"UptimeCharts": charts,
			"RangeLabel":   rangeLabels["24h"],
			"Range":        "24h",
		},
	})
}

// CreateUptimeTarget adds a new uptime target.
func (h *Handler) CreateUptimeTarget(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	url := strings.TrimSpace(r.FormValue("url"))
	interval, _ := strconv.ParseInt(r.FormValue("interval"), 10, 64)
	expected, _ := strconv.ParseInt(r.FormValue("expected_status"), 10, 64)

	if name == "" || url == "" {
		http.Error(w, "name and url required", http.StatusBadRequest)
		return
	}
	if interval <= 0 {
		interval = 60
	}
	if expected <= 0 {
		expected = 200
	}

	if err := h.q.CreateUptimeTarget(r.Context(), db.CreateUptimeTargetParams{
		Name:            name,
		Url:             url,
		IntervalSeconds: interval,
		ExpectedStatus:  expected,
	}); err != nil {
		slog.Error("create uptime target", "err", err)
		http.Error(w, "failed to create target", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/uptime", http.StatusSeeOther)
}

// UpdateUptimeTarget edits an existing target in place. It exists so a target
// can be repointed -- e.g. from a rendered page to /health -- without deleting
// and recreating it, which would throw away its check history and daily rollups.
//
// The uptime monitor re-reads its target list from the database every 10s, so an
// edit takes effect on the next tick with no restart.
func (h *Handler) UpdateUptimeTarget(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	url := strings.TrimSpace(r.FormValue("url"))
	interval, _ := strconv.ParseInt(r.FormValue("interval"), 10, 64)
	expected, _ := strconv.ParseInt(r.FormValue("expected_status"), 10, 64)

	if name == "" || url == "" {
		http.Error(w, "name and url required", http.StatusBadRequest)
		return
	}
	if interval <= 0 {
		interval = 60
	}
	if expected <= 0 {
		expected = 200
	}

	if err := h.q.UpdateUptimeTarget(r.Context(), db.UpdateUptimeTargetParams{
		ID:              id,
		Name:            name,
		Url:             url,
		IntervalSeconds: interval,
		ExpectedStatus:  expected,
	}); err != nil {
		// uptime_targets.url is UNIQUE, so colliding with another target is a
		// user error, not a server fault -- say which it is.
		if strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") {
			http.Error(w, "another target already watches that URL", http.StatusConflict)
			return
		}
		slog.Error("update uptime target", "err", err, "id", id)
		http.Error(w, "failed to update target", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/uptime", http.StatusSeeOther)
}

// ToggleUptimeTarget toggles the enabled flag.
func (h *Handler) ToggleUptimeTarget(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := h.q.ToggleUptimeTarget(r.Context(), id); err != nil {
		slog.Error("toggle uptime target", "id", id, "err", err)
		http.Error(w, "failed to toggle target", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/uptime", http.StatusSeeOther)
}

// DeleteUptimeTarget removes an uptime target and the history hanging off it.
//
// uptime_checks.target_id and uptime_daily.target_id are foreign keys without
// ON DELETE CASCADE, and foreign_keys is on, so deleting the target alone fails
// the moment a single check exists — which is always. The error used to be
// discarded and the handler redirected anyway, so the UI reported success while
// the target stayed put and kept being probed.
func (h *Handler) DeleteUptimeTarget(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	tx, err := h.rawDB.Begin()
	if err != nil {
		slog.Error("delete uptime target begin tx", "id", id, "err", err)
		http.Error(w, "failed to delete target", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	qtx := h.q.WithTx(tx)
	// Children first, then the target: the reverse order trips the constraint.
	if err := qtx.DeleteUptimeChecksForTarget(ctx, id); err != nil {
		slog.Error("delete uptime checks", "id", id, "err", err)
		http.Error(w, "failed to delete target", http.StatusInternalServerError)
		return
	}
	if err := qtx.DeleteUptimeDailyForTarget(ctx, id); err != nil {
		slog.Error("delete uptime daily", "id", id, "err", err)
		http.Error(w, "failed to delete target", http.StatusInternalServerError)
		return
	}
	if err := qtx.DeleteUptimeTarget(ctx, id); err != nil {
		slog.Error("delete uptime target", "id", id, "err", err)
		http.Error(w, "failed to delete target", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		slog.Error("delete uptime target commit", "id", id, "err", err)
		http.Error(w, "failed to delete target", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/uptime", http.StatusSeeOther)
}

// UptimeDetail renders the detail page for a single uptime target.
func (h *Handler) UptimeDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	target, err := h.q.GetUptimeTarget(ctx, id)
	if err != nil {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}

	// 7-day timeline + response time chart
	since := time.Now().UTC().Add(-7 * 24 * time.Hour)
	checks, _ := h.q.UptimeChecksSince(ctx, db.UptimeChecksSinceParams{
		TargetID: id,
		Since:    since,
	})

	segments, rtPoints, maxMs, uptimePct := computeUptimeChart(checks, target.ExpectedStatus, since)

	isUp := false
	if len(checks) > 0 {
		last := checks[len(checks)-1]
		isUp = last.Status == target.ExpectedStatus && last.Error == ""
	}

	chartW := 20
	if len(rtPoints) > 1 {
		chartW = (len(rtPoints)-1)*40 + 20
	}

	// Recent checks for the table (last 50)
	recentChecks, _ := h.q.RecentUptimeChecks(ctx, db.RecentUptimeChecksParams{
		TargetID: id,
		Limit:    50,
	})

	// Calculate current avg response time
	avgRow, _ := h.q.AvgResponseTime(ctx, db.AvgResponseTimeParams{
		TargetID: id,
		Ts:       time.Now().UTC().Add(-24 * time.Hour),
	})
	avgMs := 0.0
	if f, ok := avgRow.(float64); ok {
		avgMs = f
	}

	h.render(w, r, "uptime_detail", "", PageData{
		Title: fmt.Sprintf("Uptime: %s", target.Name),
		Extra: map[string]any{
			"Target":       target,
			"IsUp":         isUp,
			"UptimePct":    uptimePct,
			"AvgMs":        avgMs,
			"Segments":     segments,
			"RtPoints":     rtPoints,
			"MaxMs":        maxMs,
			"ChartW":       chartW,
			"RecentChecks": recentChecks,
		},
	})
}
