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

// Event is an immediate alert event (e.g., auto-block happened).
type Event struct {
	Type    string // "auto_block", "honeypot", etc.
	Message string
	Details string
}

// Engine checks alert conditions periodically and fires webhooks.
type Engine struct {
	q          *db.Queries
	webhookURL string
	mu         sync.RWMutex
}

// New creates an AlertEngine.
func New(q *db.Queries, webhookURL string) *Engine {
	return &Engine{
		q:          q,
		webhookURL: webhookURL,
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

// Notify handles an immediate event (called from autoBlocker/honeypot).
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
		if !e.cooldownOK(rule) {
			return
		}

		e.fire(ctx, rule.ID, ev.Type, ev.Message, ev.Details)
		return
	}
}

func (e *Engine) checkRules(ctx context.Context) {
	rules, err := e.q.ListEnabledAlertRules(ctx)
	if err != nil {
		slog.Error("alert check list rules", "err", err)
		return
	}

	for _, rule := range rules {
		if !e.cooldownOK(rule) {
			continue
		}

		window := time.Now().UTC().Add(-time.Duration(rule.WindowMinutes) * time.Minute)
		var count int64

		switch rule.Type {
		case "5xx_spike":
			count, err = e.q.Count5xxSince(ctx, window)
		case "traffic_surge":
			count, err = e.q.CountRequestsInWindow(ctx, window)
		case "app_error":
			count, err = e.q.CountAppErrorsSinceForAlert(ctx, window)
		default:
			continue // auto_block and downtime are handled by Notify()
		}

		if err != nil {
			slog.Error("alert check query", "type", rule.Type, "err", err)
			continue
		}

		if count >= int64(rule.Threshold) {
			msg := formatAlertTitle(rule.Type)
			details := ""
			switch rule.Type {
			case "5xx_spike":
				details = formatDetails("count", count, "window_min", rule.WindowMinutes, "threshold", rule.Threshold)
				msg += ": " + formatCount(count, rule.WindowMinutes, "5xx errors")
			case "traffic_surge":
				details = formatDetails("count", count, "window_min", rule.WindowMinutes, "threshold", rule.Threshold)
				msg += ": " + formatCount(count, rule.WindowMinutes, "requests")
			case "app_error":
				appBreakdown := ""
				if apps, err2 := e.q.TopAppErrorAppsSince(ctx, window); err2 == nil && len(apps) > 0 {
					for _, a := range apps {
						if appBreakdown != "" {
							appBreakdown += ", "
						}
						appBreakdown += fmt.Sprintf("%s: %d", a.App, a.Cnt)
					}
				}
				details = formatDetails("count", count, "window_min", rule.WindowMinutes, "threshold", rule.Threshold, "apps", appBreakdown)
				msg += ": " + formatCount(count, rule.WindowMinutes, "errors")
				if appBreakdown != "" {
					msg += " (" + appBreakdown + ")"
				}
			}
			e.fire(ctx, rule.ID, rule.Type, msg, details)
		}
	}
}

func (e *Engine) cooldownOK(rule db.ListEnabledAlertRulesRow) bool {
	if !rule.LastFiredAt.Valid {
		return true
	}
	cooldown := time.Duration(rule.CooldownMinutes) * time.Minute
	return time.Since(rule.LastFiredAt.Time) >= cooldown
}

func (e *Engine) fire(ctx context.Context, ruleID int64, alertType, message, details string) {
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

	slog.Info("alert fired", "type", alertType, "message", message)

	title := formatAlertTitle(alertType)
	color := ColorAmber
	if alertType == "5xx_spike" || alertType == "downtime" {
		color = ColorRed
	}
	sendDiscord(e.webhookURL, title, message, color)
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
