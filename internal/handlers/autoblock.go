package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	db "github.com/exploded/monitor/db/sqlc"
)

// ListAutoblockRules renders the autoblock rules panel.
func (h *Handler) ListAutoblockRules(w http.ResponseWriter, r *http.Request) {
	rules, _ := h.q.ListAutoblockRules(r.Context())
	h.renderAutoblockList(w, rules)
}

// CreateAutoblockRule adds a new autoblock rule.
func (h *Handler) CreateAutoblockRule(w http.ResponseWriter, r *http.Request) {
	pattern := strings.TrimSpace(r.FormValue("pattern"))
	description := strings.TrimSpace(r.FormValue("description"))
	if pattern == "" {
		http.Error(w, "pattern required", http.StatusBadRequest)
		return
	}

	if err := h.q.CreateAutoblockRule(r.Context(), db.CreateAutoblockRuleParams{
		Pattern:     pattern,
		Description: description,
	}); err != nil {
		slog.Error("create autoblock rule", "err", err)
		http.Error(w, "failed to create rule", http.StatusInternalServerError)
		return
	}

	h.refreshAutoBlocker(r.Context())

	rules, _ := h.q.ListAutoblockRules(r.Context())
	h.renderAutoblockList(w, rules)
}

// ToggleAutoblockRule toggles the enabled flag for an autoblock rule.
func (h *Handler) ToggleAutoblockRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := h.q.ToggleAutoblockRule(r.Context(), id); err != nil {
		slog.Error("toggle autoblock rule", "err", err)
		http.Error(w, "failed to toggle", http.StatusInternalServerError)
		return
	}

	h.refreshAutoBlocker(r.Context())

	rules, _ := h.q.ListAutoblockRules(r.Context())
	h.renderAutoblockList(w, rules)
}

// DeleteAutoblockRule removes an autoblock rule.
func (h *Handler) DeleteAutoblockRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := h.q.DeleteAutoblockRule(r.Context(), id); err != nil {
		slog.Error("delete autoblock rule", "err", err)
		http.Error(w, "failed to delete", http.StatusInternalServerError)
		return
	}

	h.refreshAutoBlocker(r.Context())

	rules, _ := h.q.ListAutoblockRules(r.Context())
	h.renderAutoblockList(w, rules)
}

func (h *Handler) refreshAutoBlocker(ctx context.Context) {
	if h.autoBlocker == nil {
		return
	}
	rules, err := h.q.ListEnabledAutoblockRules(ctx)
	if err != nil {
		slog.Error("refresh autoblocker", "err", err)
		return
	}
	h.autoBlocker.Load(rules)
}

func (h *Handler) renderAutoblockList(w http.ResponseWriter, rules []db.AutoblockRule) {
	tmpl, ok := h.pages["dashboard"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{"AutoblockRules": rules}}
	if err := tmpl.ExecuteTemplate(w, "_autoblock_list", data); err != nil {
		slog.Error("render autoblock list", "err", err)
	}
}
