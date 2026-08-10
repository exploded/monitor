package alerts

import (
	"testing"
	"time"
)

func newTestEngine() *Engine {
	return &Engine{
		keyLastFired:   make(map[string]time.Time),
		appLastAlerted: make(map[string]time.Time),
		appErrorFloor:  time.Now().UTC(),
	}
}

func TestKeyCooldownIsPerSubject(t *testing.T) {
	e := newTestEngine()

	if !e.keyCooldownOK("downtime:1", 15) {
		t.Fatal("first alert for a subject was suppressed")
	}
	if e.keyCooldownOK("downtime:1", 15) {
		t.Error("same subject alerted twice inside its cooldown")
	}
	// The bug this replaced: a per-rule cooldown meant the second service to go
	// down in a window was dropped silently.
	if !e.keyCooldownOK("downtime:2", 15) {
		t.Error("a different subject was suppressed by another subject's cooldown")
	}
}

func TestAppCooldownIsPerApp(t *testing.T) {
	e := newTestEngine()

	if !e.appCooldownOK("wtw", 30) {
		t.Fatal("first error for an app was suppressed")
	}
	if e.appCooldownOK("wtw", 30) {
		t.Error("same app alerted twice inside its cooldown")
	}
	// A chatty app must not mask a quiet one — the whole point of going per-app.
	if !e.appCooldownOK("advantage", 30) {
		t.Error("a quiet app was suppressed by a noisy app's cooldown")
	}
}

func TestExpiredCooldownAlertsAgain(t *testing.T) {
	e := newTestEngine()

	e.keyLastFired["downtime:1"] = time.Now().Add(-20 * time.Minute)
	if !e.keyCooldownOK("downtime:1", 15) {
		t.Error("subject stayed suppressed after its cooldown expired")
	}
}

func TestRecoveryClearsCooldownSoNextFailureAlerts(t *testing.T) {
	e := newTestEngine()

	if !e.keyCooldownOK("downtime:1", 60) {
		t.Fatal("first alert suppressed")
	}
	e.clearKey("downtime:1")

	// Without the clear, a service that failed, recovered and failed again
	// inside one long cooldown window would report the recovery but never the
	// second outage.
	if !e.keyCooldownOK("downtime:1", 60) {
		t.Error("post-recovery failure was suppressed by the pre-recovery cooldown")
	}
}

func TestTruncateKeepsShortSamples(t *testing.T) {
	if got := truncate("short", 200); got != "short" {
		t.Errorf("truncate altered a short string: %q", got)
	}
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	got := truncate(string(long), 200)
	if len([]rune(got)) != 201 { // 200 chars plus the ellipsis
		t.Errorf("truncate produced %d runes, want 201", len([]rune(got)))
	}
}
