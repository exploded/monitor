package watcher

import (
	"encoding/json"
	"testing"

	db "github.com/exploded/monitor/db/sqlc"
	"github.com/exploded/monitor/internal/config"
)

// newTestWatcher builds a Watcher with just enough wired up to exercise
// processLine. autoBlocker and honeypotChecker are nil-checked at the call
// site, and geo.Lookup is nil-safe.
func newTestWatcher(ignoreUAs []string) *Watcher {
	return New("", nil, nil, NewBotMatcher(), nil, nil, nil, nil, ignoreUAs)
}

func logLine(t *testing.T, ua string) []byte {
	t.Helper()
	entry := map[string]any{
		"ts": 1.7e9,
		"request": map[string]any{
			"client_ip": "203.0.113.1",
			"method":    "GET",
			"host":      "example.com",
			"uri":       "/wp-login.php",
			"headers":   map[string][]string{"User-Agent": {ua}},
		},
		"status":   404,
		"size":     100,
		"duration": 0.01,
	}
	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ingested reports whether processLine produced a row.
func ingested(w *Watcher) bool {
	select {
	case <-w.ingestCh:
		return true
	default:
		return false
	}
}

// TestMonitorProbesAreAlwaysSkipped: monitor's own uptime checks must never
// land in the request log, even with IGNORE_USER_AGENTS empty.
func TestMonitorProbesAreAlwaysSkipped(t *testing.T) {
	w := newTestWatcher(nil)
	w.processLine(logLine(t, config.MonitorUserAgent))
	if ingested(w) {
		t.Error("monitor's own probe was logged; it must always be skipped")
	}
}

// TestGoDefaultUserAgentIsNotSkipped is the regression guard for the blind spot
// this replaced. IGNORE_USER_AGENTS used to contain "Go-http-client", and the
// skip happens before bot matching, auto-blocking and the honeypot check — so
// any scanner sending Go's default UA was invisible and unblockable.
func TestGoDefaultUserAgentIsNotSkipped(t *testing.T) {
	for _, ua := range []string{"Go-http-client/1.1", "Go-http-client/2.0"} {
		w := newTestWatcher(nil)
		w.processLine(logLine(t, ua))
		if !ingested(w) {
			t.Errorf("%s was skipped; Go-written scanners must still be logged", ua)
		}
	}
}

// TestConfiguredIgnoresStillApply checks IGNORE_USER_AGENTS entries are honoured
// alongside the built-in skip, case-insensitively and as substrings.
func TestConfiguredIgnoresStillApply(t *testing.T) {
	w := newTestWatcher([]string{"SomeProbe"})
	w.processLine(logLine(t, "prefix-someprobe-suffix"))
	if ingested(w) {
		t.Error("configured ignore entry was not applied")
	}

	w2 := newTestWatcher([]string{"SomeProbe"})
	w2.processLine(logLine(t, "Mozilla/5.0"))
	if !ingested(w2) {
		t.Error("ordinary user agent was skipped")
	}
}

// TestCityIsNoLongerCollected guards the dropped column: the insert params must
// not carry a city field (a compile-time guarantee) and country still flows.
func TestCityIsNoLongerCollected(t *testing.T) {
	w := newTestWatcher(nil)
	w.processLine(logLine(t, "Mozilla/5.0"))

	var got db.InsertRequestParams
	select {
	case got = <-w.ingestCh:
	default:
		t.Fatal("nothing ingested")
	}
	if got.Uri != "/wp-login.php" {
		t.Errorf("uri = %q", got.Uri)
	}
	// With no GeoIP resolver configured, country is empty rather than absent.
	if got.Country != "" {
		t.Errorf("country = %q, want empty without a resolver", got.Country)
	}
}
