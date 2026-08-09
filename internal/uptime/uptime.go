package uptime

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
	"github.com/exploded/monitor/internal/alerts"
	"github.com/exploded/monitor/internal/config"
)

// Monitor periodically checks HTTP endpoints and records results.
type Monitor struct {
	q           *db.Queries
	alertEngine *alerts.Engine
	client      *http.Client
	mu          sync.Mutex
	lastCheck   map[int64]time.Time
}

// New creates an uptime Monitor.
func New(q *db.Queries, alertEngine *alerts.Engine) *Monitor {
	return &Monitor{
		q:           q,
		alertEngine: alertEngine,
		client:      &http.Client{Timeout: 15 * time.Second},
		lastCheck:   make(map[int64]time.Time),
	}
}

// Run starts the uptime check loop.
func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkAll(ctx)
		}
	}
}

func (m *Monitor) checkAll(ctx context.Context) {
	targets, err := m.q.ListEnabledUptimeTargets(ctx)
	if err != nil {
		slog.Error("uptime list targets", "err", err)
		return
	}

	now := time.Now()
	for _, t := range targets {
		m.mu.Lock()
		last := m.lastCheck[t.ID]
		interval := time.Duration(t.IntervalSeconds) * time.Second
		if now.Sub(last) < interval {
			m.mu.Unlock()
			continue
		}
		m.lastCheck[t.ID] = now
		m.mu.Unlock()

		go m.checkTarget(ctx, t)
	}
}

func (m *Monitor) checkTarget(ctx context.Context, t db.ListEnabledUptimeTargetsRow) {
	start := time.Now()

	var resp *http.Response
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.Url, nil)
	if err == nil {
		// Identify ourselves rather than sending Go's default Go-http-client/x.y.
		// The watcher filters this exact UA out of the request log; matching on
		// the generic Go default instead would hide every Go-written scanner.
		req.Header.Set("User-Agent", config.MonitorUserAgent)
		resp, err = m.client.Do(req)
	}

	status := 0
	errStr := ""
	responseTime := float64(time.Since(start).Milliseconds())

	if err != nil {
		errStr = err.Error()
		slog.Warn("uptime check failed", "target", t.Name, "url", t.Url, "err", err)
	} else {
		status = resp.StatusCode
		resp.Body.Close()
	}

	if insertErr := m.q.InsertUptimeCheck(ctx, db.InsertUptimeCheckParams{
		TargetID:       t.ID,
		Ts:             time.Now().UTC(),
		Status:         int64(status),
		ResponseTimeMs: responseTime,
		Error:          errStr,
	}); insertErr != nil {
		slog.Error("uptime insert check", "err", insertErr)
	}

	// Alert on downtime
	if (err != nil || int64(status) != t.ExpectedStatus) && m.alertEngine != nil {
		msg := t.Name + " is DOWN"
		if err != nil {
			msg += ": " + errStr
		} else {
			msg += ": got " + http.StatusText(status)
		}
		m.alertEngine.Notify(alerts.Event{
			Type:    "downtime",
			Message: msg,
		})
	}
}
