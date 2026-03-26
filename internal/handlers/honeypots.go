package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	db "github.com/exploded/monitor/db/sqlc"
)

// ListHoneypots renders the honeypot management panel.
func (h *Handler) ListHoneypots(w http.ResponseWriter, r *http.Request) {
	honeypots, _ := h.q.ListHoneypots(r.Context())
	h.renderHoneypotList(w, honeypots)
}

// CreateHoneypot adds a new honeypot path.
func (h *Handler) CreateHoneypot(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.FormValue("path"))
	description := strings.TrimSpace(r.FormValue("description"))
	if path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	if err := h.q.CreateHoneypot(r.Context(), db.CreateHoneypotParams{
		Path:        path,
		Description: description,
	}); err != nil {
		slog.Error("create honeypot", "err", err)
		http.Error(w, "failed to create honeypot", http.StatusInternalServerError)
		return
	}

	h.refreshHoneypotChecker(r.Context())

	honeypots, _ := h.q.ListHoneypots(r.Context())
	h.renderHoneypotList(w, honeypots)
}

// ToggleHoneypot toggles the enabled flag for a honeypot.
func (h *Handler) ToggleHoneypot(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := h.q.ToggleHoneypot(r.Context(), id); err != nil {
		slog.Error("toggle honeypot", "err", err)
		http.Error(w, "failed to toggle", http.StatusInternalServerError)
		return
	}

	h.refreshHoneypotChecker(r.Context())

	honeypots, _ := h.q.ListHoneypots(r.Context())
	h.renderHoneypotList(w, honeypots)
}

// DeleteHoneypot removes a honeypot.
func (h *Handler) DeleteHoneypot(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := h.q.DeleteHoneypot(r.Context(), id); err != nil {
		slog.Error("delete honeypot", "err", err)
		http.Error(w, "failed to delete", http.StatusInternalServerError)
		return
	}

	h.refreshHoneypotChecker(r.Context())

	honeypots, _ := h.q.ListHoneypots(r.Context())
	h.renderHoneypotList(w, honeypots)
}

func (h *Handler) refreshHoneypotChecker(ctx context.Context) {
	if h.honeypotChecker == nil {
		return
	}
	rules, err := h.q.ListEnabledHoneypots(ctx)
	if err != nil {
		slog.Error("refresh honeypot checker", "err", err)
		return
	}
	h.honeypotChecker.Load(rules)
}

func (h *Handler) renderHoneypotList(w http.ResponseWriter, honeypots []db.Honeypot) {
	tmpl, ok := h.pages["dashboard"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{"Honeypots": honeypots}}
	if err := tmpl.ExecuteTemplate(w, "_honeypot_list", data); err != nil {
		slog.Error("render honeypot list", "err", err)
	}
}
