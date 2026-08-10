package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// sampleLimit caps how much of an app's error message rides along in the alert.
const sampleLimit = 200

// Event is an immediate alert event (uptime downtime, anomaly detection).
type Event struct {
	Type string // "downtime", "rate_spike", "new_scanner", "5xx_anomaly"

	// Key scopes the cooldown to one subject — a single uptime target, a single
	// IP. Without it the cooldown is per rule, so ten services failing inside one
	// window produce one alert and nine silent drops.
	Key string

	Message string
	Details string

	// Recovered marks the end of a condition rather than its start. Recoveries
	// are never suppressed (the whole point is to close out an alert you already
	// received) and they clear the Key's cooldown so the next failure alerts
	// immediately instead of waiting out a window that no longer applies.
	Recovered bool
}

// Engine checks alert conditions periodically and fires webhooks.
type Engine struct {
	q          *db.Queries
	webhookURL string

	mu sync.Mutex
	// Cooldown state is in-memory, so a restart can re-alert on a condition that
	// is still ongoing. That is the right trade: the alternative is a restart
	// swallowing the first alert of a genuine incident.
	keyLastFired   map[string]time.Time
	appLastAlerted map[string]time.Time
	// appErrorFloor is the low-water mark for app error scanning. Errors are
	// alerted on once, when first seen, rather than re-counted every tick.
	appErrorFloor time.Time
}

// New creates an AlertEngine.
func New(q *db.Queries, webhookURL string) *Engine {
	return &Engine{
		q:              q,
		webhookURL:     webhookURL,
		keyLastFired:   make(map[string]time.Time),
		appLastAlerted: make(map[string]time.Time),
		appErrorFloor:  time.Now().UTC(),
	}
}

// Run starts the alert check loop. It blocks until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.checkRules(ctx)
		}
	}
}

// Notify handles an immediate event (called from the uptime and anomaly checks).
func (e *Engine) Notify(ev Event) {
	ctx := context.Background()

	rules, err := e.q.ListEnabledAlertRules(ctx)
	if err != nil {
		slog.Error("alert notify list rules", "err", err)
		return
	}

	for _, rule := range rules {
		if rule.Type != ev.Type {
			continue
		}

		if ev.Recovered {
			e.clearKey(ev.Key)
			e.fire(ctx, rule.ID, ev.Type, formatRecoveryTitle(ev.Type), ev.Message, ev.Details, ColorGreen)
			return
		}

		if ev.Key != "" {
			if !e.keyCooldownOK(ev.Key, rule.CooldownMinutes) {
				slog.Info("alert suppressed by cooldown", "type", ev.Type, "key", ev.Key, "cooldown_min", rule.CooldownMinutes)
				return
			}
		} else if !e.cooldownOK(rule) {
			slog.Info("alert suppressed by cooldown", "type", ev.Type, "cooldown_min", rule.CooldownMinutes)
			return
		}

		e.fire(ctx, rule.ID, ev.Type, formatAlertTitle(ev.Type), ev.Message, ev.Details, colorFor(ev.Type))
		return
	}

	slog.Warn("alert event has no matching rule", "type", ev.Type, "key", ev.Key)
}

func (e *Engine) checkRules(ctx context.Context) {
	rules, err := e.q.ListEnabledAlertRules(ctx)
	if err != nil {
		slog.Error("alert check list rules", "err", err)
		return
	}

	for _, rule := range rules {
		// app_error runs per app with its own cooldowns, so the rule-wide gate
		// would only let one app's errors through per window.
		if rule.Type == "app_error" {
			e.checkAppErrors(ctx, rule)
			continue
		}

		if !e.cooldownOK(rule) {
			slog.Debug("alert suppressed by cooldown", "type", rule.Type, "cooldown_min", rule.CooldownMinutes)
			continue
		}

		window := time.Now().UTC().Add(-time.Duration(rule.WindowMinutes) * time.Minute)
		var count int64

		switch rule.Type {
		case "5xx_spike":
			count, err = e.q.Count5xxSince(ctx, window)
		case "traffic_surge":
			count, err = e.q.CountRequestsInWindow(ctx, window)
		default:
			continue // downtime and the anomaly types arrive via Notify()
		}

		if err != nil {
			slog.Error("alert check query", "type", rule.Type, "err", err)
			continue
		}

		if count >= int64(rule.Threshold) {
			label := "requests"
			if rule.Type == "5xx_spike" {
				label = "5xx errors"
			}
			details := formatDetails("count", count, "window_min", rule.WindowMinutes, "threshold", rule.Threshold)
			msg := formatAlertTitle(rule.Type) + ": " + formatCount(count, rule.WindowMinutes, label)
			e.fire(ctx, rule.ID, rule.Type, formatAlertTitle(rule.Type), msg, details, colorFor(rule.Type))
		}
	}
}

// checkAppErrors alerts once per app per cooldown, on the first error rather
// than on a burst. A single crash in a quiet app is exactly the thing worth
// knowing about, and the previous global counter both missed it and let one
// noisy app hold the shared cooldown so every other app stayed silent.
func (e *Engine) checkAppErrors(ctx context.Context, rule db.ListEnabledAlertRulesRow) {
	e.mu.Lock()
	floor := e.appErrorFloor
	e.mu.Unlock()

	summaries, err := e.q.AppErrorSummarySince(ctx, floor)
	if err != nil {
		slog.Error("alert check query", "type", rule.Type, "err", err)
		return
	}

	// Advance the floor even when nothing matched, so the scan window stays
	// small; errors older than the floor have already been considered.
	scanned := time.Now().UTC()

	for _, s := range summaries {
		if int64(s.Cnt) < int64(rule.Threshold) {
			continue
		}
		if !e.appCooldownOK(s.App, rule.CooldownMinutes) {
			slog.Info("alert suppressed by cooldown", "type", rule.Type, "app", s.App, "cooldown_min", rule.CooldownMinutes)
			continue
		}

		msg := fmt.Sprintf("%s: %d error(s), latest: %s", s.App, s.Cnt, truncate(s.Sample, sampleLimit))
		details := formatDetails("app", s.App, "count", s.Cnt, "threshold", rule.Threshold)
		e.fire(ctx, rule.ID, rule.Type, formatAlertTitle(rule.Type)+" — "+s.App, msg, details, colorFor(rule.Type))
	}

	e.mu.Lock()
	e.appErrorFloor = scanned
	e.mu.Unlock()
}

func (e *Engine) cooldownOK(rule db.ListEnabledAlertRulesRow) bool {
	if !rule.LastFiredAt.Valid {
		return true
	}
	cooldown := time.Duration(rule.CooldownMinutes) * time.Minute
	return time.Since(rule.LastFiredAt.Time) >= cooldown
}

// keyCooldownOK reports whether this subject may alert, recording the attempt
// when it may. Check-and-set is one critical section so two concurrent target
// checks can't both pass.
func (e *Engine) keyCooldownOK(key string, cooldownMinutes int64) bool {
	return e.stampIfElapsed(e.keyLastFired, key, cooldownMinutes)
}

func (e *Engine) appCooldownOK(app string, cooldownMinutes int64) bool {
	return e.stampIfElapsed(e.appLastAlerted, app, cooldownMinutes)
}

func (e *Engine) stampIfElapsed(m map[string]time.Time, key string, cooldownMinutes int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	last, seen := m[key]
	if seen && time.Since(last) < time.Duration(cooldownMinutes)*time.Minute {
		return false
	}
	m[key] = time.Now()
	return true
}

func (e *Engine) clearKey(key string) {
	if key == "" {
		return
	}
	e.mu.Lock()
	delete(e.keyLastFired, key)
	e.mu.Unlock()
}

func (e *Engine) fire(ctx context.Context, ruleID int64, alertType, title, message, details string, color int) {
	if err := e.q.UpdateAlertRuleFired(ctx, ruleID); err != nil {
		slog.Error("alert update fired", "err", err)
	}

	if err := e.q.InsertAlertLog(ctx, db.InsertAlertLogParams{
		RuleID:  ruleID,
		Type:    alertType,
		Message: message,
		Details: details,
	}); err != nil {
		slog.Error("alert insert log", "err", err)
	}

	if e.webhookURL == "" {
		// Worth saying out loud: the alert log and "last fired" column populate
		// either way, so a missing webhook otherwise looks like a healthy system.
		slog.Warn("alert fired but DISCORD_WEBHOOK_URL is unset, nothing delivered", "type", alertType, "message", message)
		return
	}

	slog.Info("alert fired", "type", alertType, "message", message)
	sendDiscord(e.webhookURL, title, message, color)
}

func colorFor(alertType string) int {
	if alertType == "5xx_spike" || alertType == "downtime" || alertType == "app_error" {
		return ColorRed
	}
	return ColorAmber
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func formatCount(count int64, windowMin int64, label string) string {
	return fmt.Sprintf("%d %s in %d min", count, label, windowMin)
}

func formatDetails(pairs ...any) string {
	if len(pairs) == 0 {
		return "{}"
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs)-1; i += 2 {
		if k, ok := pairs[i].(string); ok {
			m[k] = pairs[i+1]
		}
	}
	b, _ := jsonMarshal(m)
	return string(b)
}
