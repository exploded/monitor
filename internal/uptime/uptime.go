package uptime

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
	"github.com/exploded/monitor/internal/alerts"
	"github.com/exploded/monitor/internal/config"
)

// failuresBeforeAlert is how many consecutive failed probes constitute an
// outage. One failed probe is a blip — a dropped packet, a restart mid-deploy —
// and alerting on it trains you to ignore the alerts.
const failuresBeforeAlert = 2

// targetState tracks a target across probes so the monitor can tell a blip from
// an outage, and an ongoing outage from a new one.
type targetState struct {
	consecFails int
	alerted     bool
}

// Monitor periodically checks HTTP endpoints and records results.
type Monitor struct {
	q           *db.Queries
	alertEngine *alerts.Engine
	client      *http.Client
	mu          sync.Mutex
	lastCheck   map[int64]time.Time
	state       map[int64]*targetState
}

// newClient builds the one HTTP client every probe shares, so that the tests
// exercise the same configuration production runs on.
//
// The Timeout is the part that matters: http.Client's zero value has none, and a
// target that accepts the connection then never answers would otherwise hang a
// probe goroutine forever. It covers reading the response body too, which is
// what bounds the drain in probe. Sharing the client is not what pools
// connections -- a nil Transport means every client shares
// http.DefaultTransport anyway; draining the body is what keeps a pooled
// connection usable.
func newClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second}
}

// New creates an uptime Monitor.
func New(q *db.Queries, alertEngine *alerts.Engine) *Monitor {
	return &Monitor{
		q:           q,
		alertEngine: alertEngine,
		client:      newClient(),
		lastCheck:   make(map[int64]time.Time),
		state:       make(map[int64]*targetState),
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

	live := make(map[int64]bool, len(targets))
	for _, t := range targets {
		live[t.ID] = true
	}
	m.pruneState(live)

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

// probe performs one HTTP check and reports the status code the target returned.
//
// The body is drained before it is closed, on every path out of the function.
// Closing a response body that still has unread bytes makes Go's transport
// abandon the connection instead of returning it to the pool, and abandoning a
// socket with data outstanding sends a TCP RST. The target's server is usually
// still writing at that moment, so it sees a broken pipe and logs an error for
// a request that succeeded from this side. Draining also lets the connection be
// reused, which is the difference between a handshake per check and none.
//
// The symptom only shows on responses too big for one segment: a /health
// endpoint replying with a couple of hundred bytes is fully buffered before
// Close is ever called, so there is nothing left unread to trigger it.
func (m *Monitor) probe(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	// Identify ourselves rather than sending Go's default Go-http-client/x.y.
	// The watcher filters this exact UA out of the request log; matching on
	// the generic Go default instead would hide every Go-written scanner.
	req.Header.Set("User-Agent", config.MonitorUserAgent)

	resp, err := m.client.Do(req)
	if err != nil {
		return 0, err
	}
	// Deferred so that any return added below this line still drains. The drain
	// is deliberately uncapped: capping it at, say, 1 MiB would reintroduce the
	// exact bug for any target that serves more than that, since whatever is left
	// unread is what triggers the reset. Nothing here is unbounded as a result --
	// io.Discard keeps memory flat, and m.client's Timeout covers reading the
	// body as well as the handshake, so a slow or endless target is cut off. A
	// read error is not worth surfacing: the status code is already in hand, and
	// a failed read cannot un-answer the request.
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	return resp.StatusCode, nil
}

func (m *Monitor) checkTarget(ctx context.Context, t db.ListEnabledUptimeTargetsRow) {
	start := time.Now()

	status, err := m.probe(ctx, t.Url)

	// Measured after the body has been drained, so this is time to a complete
	// response rather than time to first byte.
	responseTime := float64(time.Since(start).Milliseconds())

	errStr := ""
	if err != nil {
		status = 0
		errStr = err.Error()
		slog.Warn("uptime check failed", "target", t.Name, "url", t.Url, "err", err)
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

	failed := err != nil || int64(status) != t.ExpectedStatus
	m.recordOutcome(t, failed, status, errStr)
}

// recordOutcome advances a target's failure streak and emits the two events
// worth a notification: an outage starting, and an outage ending.
func (m *Monitor) recordOutcome(t db.ListEnabledUptimeTargetsRow, failed bool, status int, errStr string) {
	m.mu.Lock()
	st, ok := m.state[t.ID]
	if !ok {
		st = &targetState{}
		m.state[t.ID] = st
	}

	var notify *alerts.Event
	switch {
	case failed:
		st.consecFails++
		if st.consecFails >= failuresBeforeAlert && !st.alerted {
			st.alerted = true
			msg := t.Name + " is DOWN"
			if errStr != "" {
				msg += ": " + errStr
			} else {
				msg += fmt.Sprintf(": got %d %s, want %d", status, http.StatusText(status), t.ExpectedStatus)
			}
			notify = &alerts.Event{
				Type:    "downtime",
				Key:     fmt.Sprintf("downtime:%d", t.ID),
				Message: msg,
				Details: fmt.Sprintf(`{"target":%q,"url":%q,"consecutive_failures":%d}`, t.Name, t.Url, st.consecFails),
			}
		}
	case st.alerted:
		notify = &alerts.Event{
			Type:      "downtime",
			Key:       fmt.Sprintf("downtime:%d", t.ID),
			Message:   t.Name + " is UP again",
			Details:   fmt.Sprintf(`{"target":%q,"url":%q}`, t.Name, t.Url),
			Recovered: true,
		}
		st.consecFails = 0
		st.alerted = false
	default:
		st.consecFails = 0
	}
	m.mu.Unlock()

	if notify != nil && m.alertEngine != nil {
		m.alertEngine.Notify(*notify)
	}
}

// pruneState forgets targets that are no longer enabled. Without it the maps
// grow for the life of the process, and a target that is disabled while down
// would resume with a stale outage flag — suppressing the alert for the next
// real outage because the engine still thinks it already told you.
func (m *Monitor) pruneState(live map[int64]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.state {
		if !live[id] {
			delete(m.state, id)
		}
	}
	for id := range m.lastCheck {
		if !live[id] {
			delete(m.lastCheck, id)
		}
	}
}
