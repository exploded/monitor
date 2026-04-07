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

// rangeLabels maps range keys to human-readable labels.
var rangeLabels = map[string]string{
	"6h":  "last 6h",
	"12h": "last 12h",
	"24h": "last 24h",
	"48h": "last 48h",
	"7d":  "last 7 days",
}

// parseRange returns the since time and range key from a request's "range" query param.
func parseRange(r *http.Request) (time.Time, string) {
	rng := r.URL.Query().Get("range")
	switch rng {
	case "6h":
		return time.Now().UTC().Add(-6 * time.Hour), "6h"
	case "12h":
		return time.Now().UTC().Add(-12 * time.Hour), "12h"
	case "48h":
		return time.Now().UTC().Add(-48 * time.Hour), "48h"
	case "7d":
		return time.Now().UTC().Add(-7 * 24 * time.Hour), "7d"
	default:
		return time.Now().UTC().Add(-24 * time.Hour), "24h"
	}
}

// labelStep returns how often to show a label given n data points.
// Aims for roughly 12-24 visible labels regardless of range.
func labelStep(n int) int {
	switch {
	case n <= 24:
		return 1
	case n <= 48:
		return 4
	default:
		return 12
	}
}

// History renders the historical views page.
func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since, rng := parseRange(r)
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

	step := labelStep(len(bars))

	h.render(w, r, "history", "", PageData{
		Title: "History",
		Extra: map[string]any{
			"Bars":          bars,
			"Daily":         daily,
			"LatencyPoints": latencyPoints,
			"MaxLatency":    maxLatency,
			"ChartWidth":    chartWidth + 20,
			"BWBars":        bwBars,
			"Range":         rng,
			"RangeLabel":    rangeLabels[rng],
			"LabelStep":     step,
		},
	})
}

// HourlyChart renders the hourly SVG bar chart partial.
func (h *Handler) HourlyChart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	host := r.URL.Query().Get("host")
	since, rng := parseRange(r)

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
	data := PageData{Extra: map[string]any{"Bars": bars, "RangeLabel": rangeLabels[rng], "LabelStep": labelStep(len(bars))}}
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
	since, rng := parseRange(r)

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
		"RangeLabel":    rangeLabels[rng],
		"LabelStep":     labelStep(len(points)),
	}}
	if err := tmpl.ExecuteTemplate(w, "_latency_chart", data); err != nil {
		slog.Error("render latency chart", "err", err)
	}
}

// BandwidthChart renders the hourly bandwidth bar chart partial.
func (h *Handler) BandwidthChart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	host := r.URL.Query().Get("host")
	since, rng := parseRange(r)

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
	data := PageData{Extra: map[string]any{"BWBars": bars, "RangeLabel": rangeLabels[rng], "LabelStep": labelStep(len(bars))}}
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

// UptimeHourSegment holds data for one hour segment on the uptime timeline.
type UptimeHourSegment struct {
	Label  string  // e.g. "2024-03-27 14:00"
	State  string  // "up", "degraded", "down", "none"
	Detail string  // tooltip text
	AvgMs  float64 // average response time (0 if no data)
}

// UptimeTargetChart holds the full chart data for one uptime target.
type UptimeTargetChart struct {
	ID        int64
	Name      string
	IsUp      bool
	UptimePct float64
	Hours     []UptimeHourSegment
	// SVG response time points
	RtPoints []UptimeRtPoint
	MaxMs    float64
	ChartW   int
}

// UptimeRtPoint holds SVG coordinates for a response time chart point.
type UptimeRtPoint struct {
	X  int
	Y  int
	Ms float64
	Hr string
}

// computeUptimeChart builds timeline segments and response time points for a target.
func computeUptimeChart(checks []db.UptimeCheck, expectedStatus int64, since time.Time) ([]UptimeHourSegment, []UptimeRtPoint, float64, float64) {
	// Build 24 hourly buckets from since to now
	type bucket struct {
		total int
		up    int
		sumMs float64
		okCnt int
	}
	buckets := make(map[string]*bucket)

	for _, c := range checks {
		key := c.Ts.UTC().Format("2006-01-02 15:00")
		b, ok := buckets[key]
		if !ok {
			b = &bucket{}
			buckets[key] = b
		}
		b.total++
		if c.Status == expectedStatus && c.Error == "" {
			b.up++
		}
		if c.Error == "" {
			b.sumMs += c.ResponseTimeMs
			b.okCnt++
		}
	}

	// Generate ordered hour keys for the last 24 hours
	now := time.Now().UTC()
	start := since.UTC().Truncate(time.Hour)
	var segments []UptimeHourSegment
	var rtPoints []UptimeRtPoint
	var maxMs float64
	totalChecks := 0
	totalUp := 0

	i := 0
	for t := start; !t.After(now); t = t.Add(time.Hour) {
		key := t.Format("2006-01-02 15:00")
		localLabel := utcHourToLocal(key)
		b := buckets[key]

		seg := UptimeHourSegment{Label: localLabel}
		avgMs := 0.0

		if b == nil || b.total == 0 {
			seg.State = "none"
			seg.Detail = "no data"
		} else {
			totalChecks += b.total
			totalUp += b.up
			if b.up == b.total {
				seg.State = "up"
			} else if b.up == 0 {
				seg.State = "down"
			} else {
				seg.State = "degraded"
			}
			if b.okCnt > 0 {
				avgMs = b.sumMs / float64(b.okCnt)
			}
			seg.Detail = fmt.Sprintf("%d/%d OK, avg %.0fms", b.up, b.total, avgMs)
			seg.AvgMs = avgMs
		}
		segments = append(segments, seg)

		if avgMs > maxMs {
			maxMs = avgMs
		}
		rtPoints = append(rtPoints, UptimeRtPoint{X: i * 40, Ms: avgMs, Hr: localLabel})
		i++
	}

	if maxMs == 0 {
		maxMs = 1
	}
	for j := range rtPoints {
		rtPoints[j].Y = 120 - int(rtPoints[j].Ms/maxMs*118)
	}

	uptimePct := 0.0
	if totalChecks > 0 {
		uptimePct = float64(totalUp) / float64(totalChecks) * 100
	}

	return segments, rtPoints, maxMs, uptimePct
}

// UptimeChart renders the uptime chart partial for the uptime page.
func (h *Handler) UptimeChart(w http.ResponseWriter, r *http.Request) {
	data := h.buildUptimeChartData(r)

	tmpl, ok := h.pages["uptime"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "_uptime_chart", data); err != nil {
		slog.Error("render uptime chart", "err", err)
	}
}

// buildUptimeChartData fetches uptime checks and builds chart data for all targets.
func (h *Handler) buildUptimeChartData(r *http.Request) PageData {
	ctx := r.Context()
	since, rng := parseRange(r)
	targets, _ := h.q.ListUptimeTargets(ctx)

	var charts []UptimeTargetChart
	for _, t := range targets {
		checks, _ := h.q.UptimeChecksSince(ctx, db.UptimeChecksSinceParams{
			TargetID: t.ID,
			Since:    since,
		})

		segments, rtPoints, maxMs, uptimePct := computeUptimeChart(checks, t.ExpectedStatus, since)

		// Determine current status from most recent check
		isUp := false
		if len(checks) > 0 {
			last := checks[len(checks)-1]
			isUp = last.Status == t.ExpectedStatus && last.Error == ""
		}

		chartW := 20
		if len(rtPoints) > 1 {
			chartW = (len(rtPoints)-1)*40 + 20
		}

		charts = append(charts, UptimeTargetChart{
			ID:        t.ID,
			Name:      t.Name,
			IsUp:      isUp,
			UptimePct: uptimePct,
			Hours:     segments,
			RtPoints:  rtPoints,
			MaxMs:     maxMs,
			ChartW:    chartW,
		})
	}

	return PageData{Extra: map[string]any{
		"UptimeCharts": charts,
		"RangeLabel":   rangeLabels[rng],
	}}
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
