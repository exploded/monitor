package anomaly

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
	"github.com/exploded/monitor/internal/alerts"
)

// Detector periodically analyzes traffic patterns and flags anomalies.
type Detector struct {
	q           *db.Queries
	alertEngine *alerts.Engine
}

// New creates an anomaly Detector.
func New(q *db.Queries, alertEngine *alerts.Engine) *Detector {
	return &Detector{
		q:           q,
		alertEngine: alertEngine,
	}
}

// Run starts the anomaly detection loop.
func (d *Detector) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.analyze(ctx)
		}
	}
}

func (d *Detector) analyze(ctx context.Context) {
	d.checkRateSpikes(ctx)
	d.checkNewScanners(ctx)
	d.check5xxAnomaly(ctx)
}

// checkRateSpikes detects IPs with unusually high request rates in last 5 min.
func (d *Detector) checkRateSpikes(ctx context.Context) {
	since5m := time.Now().UTC().Add(-5 * time.Minute)
	since24h := time.Now().UTC().Add(-24 * time.Hour)

	// Get IPs with high recent activity (>=20 requests in 5 min)
	recentIPs, err := d.q.IPRequestRateRecent(ctx, db.IPRequestRateRecentParams{
		Since:    since5m,
		MinCount: int64(20),
	})
	if err != nil {
		slog.Error("anomaly rate spike query", "err", err)
		return
	}

	// Get 24h baseline for comparison
	baselineIPs, err := d.q.IPRequestRateRecent(ctx, db.IPRequestRateRecentParams{
		Since:    since24h,
		MinCount: int64(1),
	})
	if err != nil {
		return
	}

	// Compute mean + stddev of 24h per-IP request counts
	if len(baselineIPs) == 0 {
		return
	}
	var sum, sumSq float64
	for _, ip := range baselineIPs {
		// Normalize to 5-min window (24h = 288 5-min windows)
		rate := float64(ip.Cnt) / 288.0
		sum += rate
		sumSq += rate * rate
	}
	n := float64(len(baselineIPs))
	mean := sum / n
	variance := sumSq/n - mean*mean
	stddev := math.Sqrt(math.Max(0, variance))
	threshold := mean + 2*stddev

	if threshold < 20 {
		threshold = 20 // minimum baseline
	}

	for _, ip := range recentIPs {
		if float64(ip.Cnt) > threshold {
			score := math.Min(100, float64(ip.Cnt)/threshold*50)
			d.record(ctx, "rate_spike", ip.ClientIp, "",
				fmt.Sprintf("IP %s: %d requests in 5 min (threshold: %.0f)", ip.ClientIp, ip.Cnt, threshold),
				score)
		}
	}
}

// checkNewScanners detects IPs with high 4xx rates that appeared recently.
func (d *Detector) checkNewScanners(ctx context.Context) {
	since10m := time.Now().UTC().Add(-10 * time.Minute)

	ipErrors, err := d.q.IPErrorRate(ctx, since10m)
	if err != nil {
		slog.Error("anomaly scanner query", "err", err)
		return
	}

	for _, ip := range ipErrors {
		ratio4xx := ip.Count4xx.Float64 / float64(ip.Total)
		if ratio4xx > 0.8 && ip.Total >= 10 {
			score := ratio4xx * 80
			cnt4xx := int64(ip.Count4xx.Float64)
			d.record(ctx, "new_scanner", ip.ClientIp, "",
				fmt.Sprintf("IP %s: %d/%d requests are 4xx (%.0f%%) in 10 min",
					ip.ClientIp, cnt4xx, ip.Total, ratio4xx*100),
				score)
		}
	}
}

// check5xxAnomaly detects unusual 5xx error rates.
func (d *Detector) check5xxAnomaly(ctx context.Context) {
	since24h := time.Now().UTC().Add(-24 * time.Hour)

	hourly, err := d.q.HourlyErrorRates(ctx, since24h)
	if err != nil || len(hourly) < 2 {
		return
	}

	// Compute mean + stddev of 5xx rates across all hours
	var sum, sumSq float64
	for _, h := range hourly {
		rate := 0.0
		if h.Total > 0 {
			rate = h.Count5xx.Float64 / float64(h.Total)
		}
		sum += rate
		sumSq += rate * rate
	}
	n := float64(len(hourly))
	mean := sum / n
	variance := sumSq/n - mean*mean
	stddev := math.Sqrt(math.Max(0, variance))

	// Check the most recent hour
	latest := hourly[len(hourly)-1]
	if latest.Total == 0 {
		return
	}
	latestRate := latest.Count5xx.Float64 / float64(latest.Total)
	latest5xx := int64(latest.Count5xx.Float64)

	if latestRate > mean+2*stddev && latest5xx >= 3 {
		score := math.Min(100, (latestRate-mean)/(stddev+0.001)*30)
		hour := ""
		if s, ok := latest.Hour.(string); ok {
			hour = s
		}
		d.record(ctx, "5xx_anomaly", "", "",
			fmt.Sprintf("5xx rate %.1f%% at %s (baseline: %.1f%%, %d errors)",
				latestRate*100, hour, mean*100, latest5xx),
			score)
	}
}

func (d *Detector) record(ctx context.Context, anomalyType, clientIP, host, description string, score float64) {
	if err := d.q.InsertAnomaly(ctx, db.InsertAnomalyParams{
		Ts:          time.Now().UTC(),
		Type:        anomalyType,
		ClientIp:    clientIP,
		Host:        host,
		Description: description,
		Score:       score,
	}); err != nil {
		slog.Error("anomaly insert", "err", err)
		return
	}

	slog.Info("anomaly detected", "type", anomalyType, "description", description)

	if d.alertEngine != nil {
		d.alertEngine.Notify(alerts.Event{
			Type:    anomalyType,
			Message: description,
		})
	}
}
