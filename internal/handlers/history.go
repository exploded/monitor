package handlers

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
)

// utcHourToLocal converts a UTC hour string like "2024-03-27 14:00" to Melbourne local time.
func utcHourToLocal(s string) string {
	t, err := time.Parse("2006-01-02 15:00", s)
	if err != nil {
		return s
	}
	return t.In(melbourne).Format("2006-01-02 15:00")
}

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
			label = utcHourToLocal(s)
		}
		bars[i] = ChartBar{Hour: label, Count: row.Cnt, Height: height}
	}

	// Latency percentiles for initial page load
	durRows, _ := h.q.HourlyDurations(ctx, db.HourlyDurationsParams{
		Since:      since,
		HostFilter: "",
	})

	latencyPoints, maxLatency, chartWidth := computeLatencyPoints(durRows)

	// Bandwidth for initial page load
	bwRows, _ := h.q.HourlyBandwidth(ctx, db.HourlyBandwidthParams{
		Since:      since,
		HostFilter: "",
	})
	bwBars := computeBWBars(bwRows)

	h.render(w, r, "history", "", PageData{
		Title: "History",
		Extra: map[string]any{
			"Bars":          bars,
			"Daily":         daily,
			"LatencyPoints": latencyPoints,
			"MaxLatency":    maxLatency,
			"ChartWidth":    chartWidth + 20,
			"BWBars":        bwBars,
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
			label = utcHourToLocal(s)
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

// LatencyChart renders the p50/p95/p99 latency chart partial.
func (h *Handler) LatencyChart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	host := r.URL.Query().Get("host")
	since := time.Now().UTC().Add(-24 * time.Hour)

	rows, _ := h.q.HourlyDurations(ctx, db.HourlyDurationsParams{
		Since:      since,
		HostFilter: host,
	})

	points, maxVal, chartWidth := computeLatencyPoints(rows)

	tmpl, ok := h.pages["history"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{
		"LatencyPoints": points,
		"MaxLatency":    maxVal,
		"ChartWidth":    chartWidth,
	}}
	if err := tmpl.ExecuteTemplate(w, "_latency_chart", data); err != nil {
		slog.Error("render latency chart", "err", err)
	}
}

// BandwidthChart renders the hourly bandwidth bar chart partial.
func (h *Handler) BandwidthChart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	host := r.URL.Query().Get("host")
	since := time.Now().UTC().Add(-24 * time.Hour)

	rows, _ := h.q.HourlyBandwidth(ctx, db.HourlyBandwidthParams{
		Since:      since,
		HostFilter: host,
	})

	bars := computeBWBars(rows)

	tmpl, ok := h.pages["history"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{"BWBars": bars}}
	if err := tmpl.ExecuteTemplate(w, "_bandwidth_chart", data); err != nil {
		slog.Error("render bandwidth chart", "err", err)
	}
}

// LatencyPoint holds pre-computed SVG coordinates for a latency chart point.
type LatencyPoint struct {
	Hour       string
	P50        float64
	P95        float64
	P99        float64
	X          int
	Y50, Y95, Y99 int
}

// computeLatencyPoints groups hourly duration rows into percentile points with SVG coordinates.
func computeLatencyPoints(rows []db.HourlyDurationsRow) ([]LatencyPoint, float64, int) {
	var points []LatencyPoint
	var curHour string
	var durations []float64

	flush := func() {
		if len(durations) == 0 {
			return
		}
		sort.Float64s(durations)
		n := len(durations)
		points = append(points, LatencyPoint{
			P50: durations[int(math.Floor(float64(n)*0.50))],
			P95: durations[int(math.Min(float64(n-1), math.Floor(float64(n)*0.95)))],
			P99: durations[int(math.Min(float64(n-1), math.Floor(float64(n)*0.99)))],
			Hour: curHour,
		})
	}

	for _, row := range rows {
		hour := ""
		if s, ok := row.Hour.(string); ok {
			hour = utcHourToLocal(s)
		}
		if hour != curHour {
			flush()
			curHour = hour
			durations = durations[:0]
		}
		durations = append(durations, row.DurationMs)
	}
	flush()

	var maxVal float64
	for _, p := range points {
		if p.P99 > maxVal {
			maxVal = p.P99
		}
	}
	if maxVal == 0 {
		maxVal = 1
	}

	chartWidth := 20
	if len(points) > 1 {
		chartWidth = (len(points)-1)*40 + 20
	}

	for i := range points {
		points[i].X = i * 40
		points[i].Y50 = 150 - int(points[i].P50/maxVal*148)
		points[i].Y95 = 150 - int(points[i].P95/maxVal*148)
		points[i].Y99 = 150 - int(points[i].P99/maxVal*148)
	}

	return points, maxVal, chartWidth
}

// BWBar holds pre-computed bar chart data for bandwidth.
type BWBar struct {
	Hour   string
	Bytes  int64
	Height int
}

// computeBWBars converts hourly bandwidth rows into bar chart data.
func computeBWBars(rows []db.HourlyBandwidthRow) []BWBar {
	var maxBytes int64
	for _, row := range rows {
		b := int64(0)
		if row.TotalBytes.Valid {
			b = int64(row.TotalBytes.Float64)
		}
		if b > maxBytes {
			maxBytes = b
		}
	}
	bars := make([]BWBar, len(rows))
	for i, row := range rows {
		b := int64(0)
		if row.TotalBytes.Valid {
			b = int64(row.TotalBytes.Float64)
		}
		height := 0
		if maxBytes > 0 {
			height = int(float64(b) / float64(maxBytes) * 180)
		}
		if height < 2 && b > 0 {
			height = 2
		}
		label := ""
		if s, ok := row.Hour.(string); ok {
			label = utcHourToLocal(s)
		}
		bars[i] = BWBar{Hour: label, Bytes: b, Height: height}
	}
	return bars
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
