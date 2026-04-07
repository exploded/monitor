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
	targets, _ := h.q.ListUptimeTargets(ctx)
	since := time.Now().UTC().Add(-24 * time.Hour)

	type TargetStatus struct {
		Target    db.UptimeTarget
		UptimePct float64
		AvgMs     float64
		LastCheck *db.UptimeCheck
		IsUp      bool
	}

	var statuses []TargetStatus
	for _, t := range targets {
		upRow, _ := h.q.UptimePercentage(ctx, db.UptimePercentageParams{
			Status:   t.ExpectedStatus,
			TargetID: t.ID,
			Ts:       since,
		})
		avgRow, _ := h.q.AvgResponseTime(ctx, db.AvgResponseTimeParams{
			TargetID: t.ID,
			Ts:       since,
		})
		recent, _ := h.q.RecentUptimeChecks(ctx, db.RecentUptimeChecksParams{
			TargetID: t.ID,
			Limit:    1,
		})

		pct := 0.0
		if upRow.Total > 0 {
			pct = upRow.UpCount.Float64 / float64(upRow.Total) * 100
		}

		avgMs := 0.0
		if f, ok := avgRow.(float64); ok {
			avgMs = f
		}

		var lastCheck *db.UptimeCheck
		isUp := false
		if len(recent) > 0 {
			lastCheck = &recent[0]
			isUp = recent[0].Status == t.ExpectedStatus && recent[0].Error == ""
		}

		statuses = append(statuses, TargetStatus{
			Target:    t,
			UptimePct: pct,
			AvgMs:     avgMs,
			LastCheck: lastCheck,
			IsUp:      isUp,
		})
	}

	// Uptime chart data
	uptimeData := h.buildUptimeChartData(r)
	uptimeCharts, _ := uptimeData.Extra["UptimeCharts"]
	rangeLabel, _ := uptimeData.Extra["RangeLabel"]

	h.render(w, r, "uptime", "", PageData{
		Title: "Uptime",
		Extra: map[string]any{
			"Statuses":     statuses,
			"UptimeCharts": uptimeCharts,
			"RangeLabel":   rangeLabel,
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

// ToggleUptimeTarget toggles the enabled flag.
func (h *Handler) ToggleUptimeTarget(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	h.q.ToggleUptimeTarget(r.Context(), id)
	http.Redirect(w, r, "/uptime", http.StatusSeeOther)
}

// DeleteUptimeTarget removes an uptime target.
func (h *Handler) DeleteUptimeTarget(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	h.q.DeleteUptimeTarget(r.Context(), id)
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
