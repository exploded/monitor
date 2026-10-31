package anomaly

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
	"github.com/exploded/monitor/internal/alerts"
	"github.com/exploded/monitor/internal/database"
)

// Detector periodically checks the 5xx rate and flags anomalies.
//
// It used to look for rate spikes and new scanners too, per IP. Those existed
// to pick IPs to block, and blocking is Cloudflare's job; their three GROUP BY
// client_ip queries also held the single DB connection every 2 minutes.
type Detector struct {
	q              *db.Queries
	alertEngine    *alerts.Engine
	latencyExclude string
}

// New creates an anomaly Detector. latencyExclude is config.LatencyExcludeJSON,
// passed through to the hourly series.
func New(q *db.Queries, alertEngine *alerts.Engine, latencyExclude string) *Detector {
	return &Detector{
		q:              q,
		alertEngine:    alertEngine,
		latencyExclude: latencyExclude,
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
			d.check5xxAnomaly(ctx)
		}
	}
}

// check5xxAnomaly detects unusual 5xx error rates. It reads the hourly series,
// so the only raw rows it touches are the current hour's.
func (d *Detector) check5xxAnomaly(ctx context.Context) {
	since24h := time.Now().UTC().Add(-24 * time.Hour)

	hourly, err := database.HourlySeries(ctx, d.q, since24h, d.latencyExclude)
	if err != nil {
		slog.Error("anomaly 5xx query", "err", err)
		return
	}
	if len(hourly) < 2 {
		return
	}

	// Compute mean + stddev of 5xx rates across all hours
	var sum, sumSq float64
	for _, h := range hourly {
		rate := 0.0
		if h.Total > 0 {
			rate = float64(h.Errors5xx) / float64(h.Total)
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
	latestRate := float64(latest.Errors5xx) / float64(latest.Total)
	latest5xx := latest.Errors5xx

	if latestRate > mean+2*stddev && latest5xx >= 3 {
		score := math.Min(100, (latestRate-mean)/(stddev+0.001)*30)
		d.record(ctx, "5xx_anomaly", "", "",
			fmt.Sprintf("5xx rate %.1f%% at %s (baseline: %.1f%%, %d errors)",
				latestRate*100, latest.Hour, mean*100, latest5xx),
			score)
	}
}

// dedupWindow suppresses repeat anomalies for the same (type, ip, host). The
// detector runs every 2 minutes, so without this a single persistent offender
// wrote a row every tick for as long as it stayed over threshold — 187 rows a
// day across the table, almost all of them duplicates.
const dedupWindow = time.Hour

func (d *Detector) record(ctx context.Context, anomalyType, clientIP, host, description string, score float64) {
	seen, err := d.q.RecentAnomalyExists(ctx, db.RecentAnomalyExistsParams{
		Type:     anomalyType,
		ClientIp: clientIP,
		Host:     host,
		Since:    time.Now().UTC().Add(-dedupWindow),
	})
	if err != nil {
		slog.Error("anomaly dedup check", "err", err)
	} else if seen {
		return
	}

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
