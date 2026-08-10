package uptime

import (
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
