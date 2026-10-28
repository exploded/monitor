package handlers

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
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

// searchFloor is the earliest instant search and export will look back to.
//
// It reads the configured raw `requests` horizon rather than a constant: raw
// rows older than that no longer exist, so accepting an earlier ?from= would
// silently return a truncated result set while scanning the whole table to do
// it. Hard-coding 30 here meant RETENTION_DAYS could be changed in .env and
// search would keep offering — or refusing — the wrong range. Longer history
// lives in daily_stats.
func (h *Handler) searchFloor() time.Time {
	return time.Now().UTC().AddDate(0, 0, -h.cfg.RetentionDays)
}

// DailySummaryDays is how far back the daily table reaches. It reads
// daily_stats rather than raw rows, so it is not tied to the raw horizon and
// costs one row per day — otherwise the rollup would retain history nothing
// ever displays.
const DailySummaryDays = 90

// dailySummaryFrom is the first day the rollup-backed summary covers.
func dailySummaryFrom() string {
	return time.Now().UTC().AddDate(0, 0, -DailySummaryDays).Format("2006-01-02")
}

// searchMinDate is the earliest time the search picker offers, in the
// datetime-local format. Search reads raw rows, so it is bounded by the raw
// horizon, not the rollup's.
func (h *Handler) searchMinDate() string {
	return h.searchFloor().In(melbourne).Format("2006-01-02T15:04")
}

// parseSearchTime reads a search ?from= or ?to= value as Melbourne time: a
// datetime-local value (2006-01-02T15:04), or a bare date for older links. A
// bare date means the start of that day, or its last second when end is set.
func parseSearchTime(s string, end bool) (time.Time, bool) {
	if t, err := time.ParseInLocation("2006-01-02T15:04", s, melbourne); err == nil {
		return t.UTC(), true
	}
	if t, err := time.ParseInLocation("2006-01-02", s, melbourne); err == nil {
		if end {
			t = t.AddDate(0, 0, 1).Add(-time.Second)
		}
		return t.UTC(), true
	}
	return time.Time{}, false
}

// searchFilter is the parsed form of the search and export query parameters.
type searchFilter struct {
	host, ip, ua string
	status       int64
	from, to     time.Time
	slowest      bool // sort=slowest: longest duration first
}

// parseSearch reads the search form. from defaults to 24h back and is floored
// to the raw retention horizon; to defaults to now.
func (h *Handler) parseSearch(r *http.Request) searchFilter {
	q := r.URL.Query()
	f := searchFilter{
		host:    q.Get("host"),
		ip:      q.Get("ip"),
		ua:      q.Get("ua"),
		from:    time.Now().UTC().Add(-24 * time.Hour),
		to:      time.Now().UTC(),
		slowest: q.Get("sort") == "slowest",
	}
	f.status, _ = strconv.ParseInt(q.Get("status"), 10, 64)
	if t, ok := parseSearchTime(q.Get("from"), false); ok {
		f.from = t
	}
	if t, ok := parseSearchTime(q.Get("to"), true); ok {
		f.to = t
	}
	if floor := h.searchFloor(); f.from.Before(floor) {
		f.from = floor
	}
	return f
}

// formatMs renders a duration in milliseconds for tables and axis labels:
// "4 ms", "109 ms", "1 s", "3.7 s", "25 s".
func formatMs(ms float64) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%.0f ms", ms)
	case ms < 10000:
		return strings.TrimSuffix(strconv.FormatFloat(ms/1000, 'f', 1, 64), ".0") + " s"
	default:
		return fmt.Sprintf("%.0f s", ms/1000)
	}
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

	hourly, _ := h.q.HourlyRequestCounts(ctx, since)
	daily, _ := h.q.DailySummary(ctx, dailySummaryFrom())

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
		Since:          since,
		HostFilter:     "",
		LatencyExclude: h.cfg.LatencyExcludeJSON(),
	})

	latencyPoints, latencyTicks, chartWidth := computeLatencyPoints(durRows)

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
			"LatencyTicks":  latencyTicks,
			"ChartWidth":    chartWidth,
			"BWBars":        bwBars,
			"Range":         rng,
			"RangeLabel":    rangeLabels[rng],
			"LabelStep":     step,
			"SearchMinDate": h.searchMinDate(),
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
	daily, _ := h.q.DailySummary(ctx, dailySummaryFrom())

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
		Since:          since,
		HostFilter:     host,
		LatencyExclude: h.cfg.LatencyExcludeJSON(),
	})

	points, ticks, chartWidth := computeLatencyPoints(rows)

	tmpl, ok := h.pages["history"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{
		"LatencyPoints": points,
		"LatencyTicks":  ticks,
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
	Hour          string
	P50           float64
	P95           float64
	P99           float64
	X             int
	Y50, Y95, Y99 int
}

// LatencyTick is one gridline on the latency chart's log axis.
type LatencyTick struct {
	Y      int     // SVG y, for the gridline
	TopPct float64 // the same height as a percentage, for the HTML label
	Label  string
}

// The latency chart's plot area within its 160-unit-high viewBox.
const (
	latencyTop    = 4
	latencyBottom = 152
	latencyHeight = 160
)

// latencyAxis builds a log10 axis from 1 ms to the decade above maxMs, never
// lower than 1 s. Linear was useless here: a 3.7 s p99 squashed a 2 ms p50 and
// a 100 ms p95 into the same flat line along the bottom. Log gives each decade
// the same height and fixed gridlines, so lines stay comparable across ranges.
func latencyAxis(maxMs float64) (y func(ms float64) int, ticks []LatencyTick) {
	hi := 3.0 // 1 s
	if maxMs > 0 {
		hi = math.Max(hi, math.Ceil(math.Log10(maxMs)))
	}
	y = func(ms float64) int {
		l := math.Log10(math.Max(ms, 1)) // under 1 ms sits on the floor
		l = math.Min(l, hi)
		return latencyBottom - int(math.Round(l/hi*(latencyBottom-latencyTop)))
	}
	for e := 0.0; e <= hi; e++ {
		ty := y(math.Pow(10, e))
		ticks = append(ticks, LatencyTick{
			Y:      ty,
			TopPct: float64(ty) / latencyHeight * 100,
			Label:  formatMs(math.Pow(10, e)),
		})
	}
	return y, ticks
}

// computeLatencyPoints groups hourly duration rows into percentile points with
// SVG coordinates on a log axis, and returns the axis gridlines.
func computeLatencyPoints(rows []db.HourlyDurationsRow) ([]LatencyPoint, []LatencyTick, int) {
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
			P50:  durations[int(math.Floor(float64(n)*0.50))],
			P95:  durations[int(math.Min(float64(n-1), math.Floor(float64(n)*0.95)))],
			P99:  durations[int(math.Min(float64(n-1), math.Floor(float64(n)*0.99)))],
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
	y, ticks := latencyAxis(maxVal)

	chartWidth := 20
	if len(points) > 1 {
		chartWidth = (len(points)-1)*40 + 20
	}

	for i := range points {
		points[i].X = i * 40
		points[i].Y50 = y(points[i].P50)
		points[i].Y95 = y(points[i].P95)
		points[i].Y99 = y(points[i].P99)
	}

	return points, ticks, chartWidth
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
	f := h.parseSearch(r)

	page, _ := strconv.ParseInt(r.URL.Query().Get("page"), 10, 64)
	if page < 0 {
		page = 0
	}
	limit := int64(50)

	params := db.SearchRequestsParams{
		HostFilter:   f.host,
		IpFilter:     f.ip,
		StatusFilter: f.status,
		UaFilter:     f.ua,
		FromTs:       f.from,
		ToTs:         f.to,
		Lim:          limit,
		Off:          page * limit,
	}
	var results []db.SearchRequestsRow
	if f.slowest {
		rows, _ := h.q.SearchRequestsSlowest(ctx, db.SearchRequestsSlowestParams{
			HostFilter: f.host, IpFilter: f.ip, StatusFilter: f.status, UaFilter: f.ua,
			FromTs: f.from, ToTs: f.to, LatencyExclude: h.cfg.LatencyExcludeJSON(),
			Lim: limit, Off: page * limit,
		})
		for _, row := range rows {
			results = append(results, db.SearchRequestsRow(row))
		}
	} else {
		results, _ = h.q.SearchRequests(ctx, params)
	}

	// The form's own values, escaped, for the Load More link. Building this by
	// hand broke on a user agent containing & or a space.
	query := r.URL.Query()
	query.Del("page")

	tmpl, ok := h.pages["history"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{
		"Results":  results,
		"Query":    query.Encode(),
		"NextPage": page + 1,
		"HasMore":  int64(len(results)) == limit,
	}}
	if err := tmpl.ExecuteTemplate(w, "_search_results", data); err != nil {
		slog.Error("render search results", "err", err)
	}
}
