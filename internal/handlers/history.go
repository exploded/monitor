package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
)

// History renders the historical views page.
func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since := time.Now().UTC().Add(-24 * time.Hour)
	daySince := time.Now().UTC().Add(-30 * 24 * time.Hour)

	hourly, _ := h.q.HourlyRequestCounts(ctx, since)
	daily, _ := h.q.DailySummary(ctx, daySince)

	// Pre-calculate chart data
	type ChartBar struct {
		Hour   string
		Count  int64
		Height int
	}
	var maxCount int64
	for _, row := range hourly {
		if row.Cnt > maxCount {
			maxCount = row.Cnt
		}
	}
	bars := make([]ChartBar, len(hourly))
	for i, row := range hourly {
		height := 0
		if maxCount > 0 {
			height = int(float64(row.Cnt) / float64(maxCount) * 180)
		}
		if height < 2 && row.Cnt > 0 {
			height = 2
		}
		label := ""
		if s, ok := row.Hour.(string); ok {
			label = s
		}
		bars[i] = ChartBar{Hour: label, Count: row.Cnt, Height: height}
	}

	h.render(w, r, "history", "", PageData{
		Title: "History",
		Extra: map[string]any{
			"Bars":  bars,
			"Daily": daily,
		},
	})
}

// HourlyChart renders the hourly SVG bar chart partial.
func (h *Handler) HourlyChart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	host := r.URL.Query().Get("host")
	since := time.Now().UTC().Add(-24 * time.Hour)

	var hourly []db.HourlyRequestCountsRow
	if host != "" {
		rows, _ := h.q.HourlyRequestCountsByHost(ctx, db.HourlyRequestCountsByHostParams{
			Since:      since,
			HostFilter: host,
		})
		for _, row := range rows {
			hourly = append(hourly, db.HourlyRequestCountsRow{Hour: row.Hour, Cnt: row.Cnt})
		}
	} else {
		hourly, _ = h.q.HourlyRequestCounts(ctx, since)
	}

	type ChartBar struct {
		Hour   string
		Count  int64
		Height int
	}
	var maxCount int64
	for _, row := range hourly {
		if row.Cnt > maxCount {
			maxCount = row.Cnt
		}
	}
	bars := make([]ChartBar, len(hourly))
	for i, row := range hourly {
		height := 0
		if maxCount > 0 {
			height = int(float64(row.Cnt) / float64(maxCount) * 180)
		}
		if height < 2 && row.Cnt > 0 {
			height = 2
		}
		label := ""
		if s, ok := row.Hour.(string); ok {
			label = s
		}
		bars[i] = ChartBar{Hour: label, Count: row.Cnt, Height: height}
	}

	tmpl, ok := h.pages["history"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{"Bars": bars}}
	if err := tmpl.ExecuteTemplate(w, "_hourly_chart", data); err != nil {
		slog.Error("render hourly chart", "err", err)
	}
}

// DailySummary renders the daily summary table partial.
func (h *Handler) DailySummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since := time.Now().UTC().Add(-30 * 24 * time.Hour)
	daily, _ := h.q.DailySummary(ctx, since)

	tmpl, ok := h.pages["history"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{"Daily": daily}}
	if err := tmpl.ExecuteTemplate(w, "_daily_summary", data); err != nil {
		slog.Error("render daily summary", "err", err)
	}
}

// Search handles filtered request search.
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	host := q.Get("host")
	ip := q.Get("ip")
	ua := q.Get("ua")
	statusStr := q.Get("status")
	fromStr := q.Get("from")
	toStr := q.Get("to")
	pageStr := q.Get("page")

	var statusFilter int64
	if statusStr != "" {
		statusFilter, _ = strconv.ParseInt(statusStr, 10, 64)
	}

	from := time.Now().UTC().Add(-24 * time.Hour)
	if fromStr != "" {
		if t, err := time.Parse("2006-01-02", fromStr); err == nil {
			from = t
		}
	}
	to := time.Now().UTC()
	if toStr != "" {
		if t, err := time.Parse("2006-01-02", toStr); err == nil {
			to = t.Add(24*time.Hour - time.Second)
		}
	}

	page, _ := strconv.ParseInt(pageStr, 10, 64)
	if page < 0 {
		page = 0
	}
	limit := int64(50)

	results, _ := h.q.SearchRequests(ctx, db.SearchRequestsParams{
		HostFilter:   host,
		IpFilter:     ip,
		StatusFilter: statusFilter,
		UaFilter:     ua,
		FromTs:       from,
		ToTs:         to,
		Lim:          limit,
		Off:          page * limit,
	})

	tmpl, ok := h.pages["history"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{
		"Results":    results,
		"Query":    fmt.Sprintf("host=%s&ip=%s&ua=%s&status=%s&from=%s&to=%s", host, ip, ua, statusStr, fromStr, toStr),
		"NextPage": page + 1,
		"HasMore":  int64(len(results)) == limit,
	}}
	if err := tmpl.ExecuteTemplate(w, "_search_results", data); err != nil {
		slog.Error("render search results", "err", err)
	}
}
