package handlers

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	db "github.com/exploded/monitor/db/sqlc"
)

// ListAlertRules renders the alert rules panel.
func (h *Handler) ListAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, _ := h.q.ListAlertRules(r.Context())
	h.renderAlertRules(w, rules)
}

// CreateAlertRule adds a new alert rule.
func (h *Handler) CreateAlertRule(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	typ := strings.TrimSpace(r.FormValue("type"))
	threshold, _ := strconv.ParseInt(r.FormValue("threshold"), 10, 64)
	windowMin, _ := strconv.ParseInt(r.FormValue("window_minutes"), 10, 64)
	cooldownMin, _ := strconv.ParseInt(r.FormValue("cooldown_minutes"), 10, 64)

	if name == "" || typ == "" {
		http.Error(w, "name and type required", http.StatusBadRequest)
		return
	}
	if windowMin <= 0 {
		windowMin = 5
	}
	if cooldownMin <= 0 {
		cooldownMin = 15
	}

	if err := h.q.CreateAlertRule(r.Context(), db.CreateAlertRuleParams{
		Name:            name,
		Type:            typ,
		Threshold:       threshold,
		WindowMinutes:   windowMin,
		CooldownMinutes: cooldownMin,
	}); err != nil {
		slog.Error("create alert rule", "err", err)
		http.Error(w, "failed to create rule", http.StatusInternalServerError)
		return
	}

	rules, _ := h.q.ListAlertRules(r.Context())
	h.renderAlertRules(w, rules)
}

// ToggleAlertRule toggles the enabled flag for an alert rule.
func (h *Handler) ToggleAlertRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := h.q.ToggleAlertRule(r.Context(), id); err != nil {
		slog.Error("toggle alert rule", "err", err)
	}

	rules, _ := h.q.ListAlertRules(r.Context())
	h.renderAlertRules(w, rules)
}

// DeleteAlertRule removes an alert rule.
func (h *Handler) DeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := h.q.DeleteAlertRule(r.Context(), id); err != nil {
		slog.Error("delete alert rule", "err", err)
	}

	rules, _ := h.q.ListAlertRules(r.Context())
	h.renderAlertRules(w, rules)
}

// AlertLogPanel renders the recent alert log partial.
func (h *Handler) AlertLogPanel(w http.ResponseWriter, r *http.Request) {
	logs, _ := h.q.RecentAlertLogs(r.Context(), 20)
	tmpl, ok := h.pages["dashboard"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{"AlertLogs": logs}}
	if err := tmpl.ExecuteTemplate(w, "_alert_log", data); err != nil {
		slog.Error("render alert log", "err", err)
	}
}

func (h *Handler) renderAlertRules(w http.ResponseWriter, rules []db.AlertRule) {
	tmpl, ok := h.pages["dashboard"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{"AlertRules": rules}}
	if err := tmpl.ExecuteTemplate(w, "_alert_rules", data); err != nil {
		slog.Error("render alert rules", "err", err)
	}
}
