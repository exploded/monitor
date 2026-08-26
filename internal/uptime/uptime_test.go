package uptime

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
)

func testTarget(id int64, name string) db.ListEnabledUptimeTargetsRow {
	return db.ListEnabledUptimeTargetsRow{
		ID: id, Name: name, Url: "https://" + name + ".example",
		IntervalSeconds: 60, ExpectedStatus: 200,
	}
}

// newTestMonitor builds a Monitor with no queries and no alert engine, so the
// outcome state machine can be exercised without a database or a webhook. A nil
// alertEngine is the same nil-check the real code already performs.
func newTestMonitor() *Monitor {
	return &Monitor{
		client:    newClient(),
		lastCheck: make(map[int64]time.Time),
		state:     make(map[int64]*targetState),
	}
}

func TestSingleFailureDoesNotAlert(t *testing.T) {
	m := newTestMonitor()
	tgt := testTarget(1, "wtw")

	m.recordOutcome(tgt, true, 0, "connection refused")

	st := m.state[tgt.ID]
	if st.alerted {
		t.Error("alerted after a single failed probe; one blip must not page")
	}
	if st.consecFails != 1 {
		t.Errorf("consecFails = %d, want 1", st.consecFails)
	}
}

func TestSecondConsecutiveFailureAlertsOnce(t *testing.T) {
	m := newTestMonitor()
	tgt := testTarget(1, "wtw")

	m.recordOutcome(tgt, true, 0, "connection refused")
	m.recordOutcome(tgt, true, 0, "connection refused")

	if !m.state[tgt.ID].alerted {
		t.Fatal("no alert after two consecutive failures")
	}

	// Staying down must not re-alert; that is the cooldown's job, not a repeat.
	m.recordOutcome(tgt, true, 0, "connection refused")
	if m.state[tgt.ID].consecFails != 3 {
		t.Errorf("consecFails = %d, want 3", m.state[tgt.ID].consecFails)
	}
	if !m.state[tgt.ID].alerted {
		t.Error("alerted flag cleared while still down")
	}
}

func TestRecoveryClearsAlertedState(t *testing.T) {
	m := newTestMonitor()
	tgt := testTarget(1, "wtw")

	m.recordOutcome(tgt, true, 0, "refused")
	m.recordOutcome(tgt, true, 0, "refused")
	m.recordOutcome(tgt, false, 200, "")

	st := m.state[tgt.ID]
	if st.alerted {
		t.Error("still marked alerted after recovery")
	}
	if st.consecFails != 0 {
		t.Errorf("consecFails = %d after recovery, want 0", st.consecFails)
	}
}

func TestInterruptedFailureStreakResets(t *testing.T) {
	m := newTestMonitor()
	tgt := testTarget(1, "wtw")

	// Fail, recover, fail: two failures that are not consecutive must not alert.
	m.recordOutcome(tgt, true, 0, "refused")
	m.recordOutcome(tgt, false, 200, "")
	m.recordOutcome(tgt, true, 0, "refused")

	if m.state[tgt.ID].alerted {
		t.Error("alerted on two non-consecutive failures")
	}
}

func TestPruneStateForgetsRemovedTargets(t *testing.T) {
	m := newTestMonitor()
	live := testTarget(1, "wtw")
	gone := testTarget(2, "retired")

	m.recordOutcome(live, true, 0, "refused")
	m.recordOutcome(gone, true, 0, "refused")
	m.recordOutcome(gone, true, 0, "refused")

	m.pruneState(map[int64]bool{live.ID: true})

	if _, ok := m.state[gone.ID]; ok {
		t.Error("state kept for a target that is no longer enabled")
	}
	if _, ok := m.state[live.ID]; !ok {
		t.Error("state dropped for a still-enabled target")
	}
}

// resetGrace is how long the test server waits, after writing its response, for
// a connection reset to show up as a cancelled request context.
const resetGrace = 2 * time.Second

// TestProbeDrainsLargeResponseBody is the regression test for the broken-pipe
// errors monitor was leaving in its targets' logs. It asserts on what the
// server saw, because that is the only place the bug is visible: probe returns
// a clean 200 either way, while the target at the other end takes a reset.
//
// The body has to be far bigger than one segment or the test proves nothing. A
// short response is fully buffered before probe closes the body, so there is no
// unread data to reset the connection and an undrained close looks healthy.
//
// Where the reset lands depends on the platform's socket buffers, so the
// handler watches for both forms. On Linux the server is still blocked in Write
// when the reset arrives and the write itself fails; on Windows, loopback
// buffers swallow several MiB, so the write succeeds and the reset turns up
// moments later as a cancelled request context.
func TestProbeDrainsLargeResponseBody(t *testing.T) {
	const bodySize = 4 << 20 // 4 MiB, far past a single segment

	payload := bytes.Repeat([]byte("monitor-drain-test "), bodySize/19)
	serverErr := make(chan error, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)

		_, err := w.Write(payload)
		if err == nil {
			// A client that drained the body leaves the connection open and
			// pooled, so this waits out the full grace period and reports nil.
			select {
			case <-r.Context().Done():
				err = r.Context().Err()
			case <-time.After(resetGrace):
			}
		}
		serverErr <- err
	}))
	defer srv.Close()

	m := newTestMonitor()

	status, err := m.probe(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("probe returned an error: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want %d", status, http.StatusOK)
	}

	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("target's handler saw the connection fail (%v); the probe "+
				"closed the body without draining it, which resets the connection", err)
		}
	case <-time.After(resetGrace + 30*time.Second):
		t.Fatal("target's handler never finished writing its response")
	}
}

// TestProbeReusesConnection covers the other half of the same bug: an undrained
// close takes the connection out of the pool, so every check pays a fresh
// handshake. Drained, a second probe of the same target should land on the
// connection the first one opened.
func TestProbeReusesConnection(t *testing.T) {
	var conns []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conns = append(conns, r.RemoteAddr)
		w.Write(bytes.Repeat([]byte("x"), 64<<10))
	}))
	defer srv.Close()

	m := newTestMonitor()
	for i := range 2 {
		if _, err := m.probe(context.Background(), srv.URL); err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
	}

	if len(conns) != 2 {
		t.Fatalf("handler saw %d requests, want 2", len(conns))
	}
	if conns[0] != conns[1] {
		t.Errorf("second probe opened a new connection (%s then %s); the first body was not drained back into the pool", conns[0], conns[1])
	}
}
