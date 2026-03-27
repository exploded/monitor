package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
)

type appLogEntry struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"msg"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Source  string         `json:"source,omitempty"`
}

type appLogBatch struct {
	App  string        `json:"app"`
	Logs []appLogEntry `json:"logs"`
}

// IngestAppLogs handles POST /api/logs. Auth via X-API-Key header.
func (h *Handler) IngestAppLogs(w http.ResponseWriter, r *http.Request) {
	if h.cfg.LogAPIKey == "" {
		http.Error(w, "log ingestion not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("X-API-Key") != h.cfg.LogAPIKey {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var batch appLogBatch
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if batch.App == "" || len(batch.Logs) == 0 {
		http.Error(w, "app and logs required", http.StatusBadRequest)
		return
	}

	tx, err := h.rawDB.Begin()
	if err != nil {
		slog.Error("ingest app logs begin tx", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	qtx := h.q.WithTx(tx)
	ctx := r.Context()

	for _, entry := range batch.Logs {
		attrsJSON, _ := json.Marshal(entry.Attrs)
		if err := qtx.InsertAppLog(ctx, db.InsertAppLogParams{
			Ts:      entry.Time.UTC(),
			App:     batch.App,
			Level:   entry.Level,
			Message: entry.Message,
			Attrs:   string(attrsJSON),
			Source:  entry.Source,
		}); err != nil {
			slog.Error("ingest app log", "err", err)
			tx.Rollback()
			http.Error(w, "insert failed", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Error("ingest app logs commit", "err", err)
		http.Error(w, "commit failed", http.StatusInternalServerError)
		return
	}

	slog.Info("ingested app logs", "app", batch.App, "count", len(batch.Logs))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"accepted": len(batch.Logs)})
}

// AppLogDetail renders the detail view for a single app log entry.
func (h *Handler) AppLogDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	logEntry, err := h.q.GetAppLog(r.Context(), id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Parse attrs JSON into map for display.
	var attrs map[string]any
	if logEntry.Attrs != "" && logEntry.Attrs != "{}" {
		if err := json.Unmarshal([]byte(logEntry.Attrs), &attrs); err != nil {
			slog.Warn("parse app log attrs", "id", id, "err", err)
		}
	}

	// Stringify any non-string attr values for template display.
	displayAttrs := make(map[string]string, len(attrs))
	for k, v := range attrs {
		switch val := v.(type) {
		case string:
			displayAttrs[k] = val
		default:
			displayAttrs[k] = fmt.Sprintf("%v", val)
		}
	}

	tmpl, ok := h.pages["dashboard"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{
		"Log":   logEntry,
		"Attrs": displayAttrs,
	}}
	if err := tmpl.ExecuteTemplate(w, "_app_log_detail", data); err != nil {
		slog.Error("render app log detail", "id", id, "err", err)
	}
}

// AppErrorsPanel renders the recent app errors partial (polled by HTMX).
func (h *Handler) AppErrorsPanel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since := time.Now().UTC().Add(-24 * time.Hour)

	errors, _ := h.q.RecentAppErrors(ctx, 20)
	errorCount, _ := h.q.CountAppErrorsSince(ctx, since)
	totalCount, _ := h.q.CountAppLogsSince(ctx, since)

	tmpl, ok := h.pages["dashboard"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{
		"AppErrors":     errors,
		"AppErrorCount": errorCount,
		"AppLogCount":   totalCount,
	}}
	if err := tmpl.ExecuteTemplate(w, "_app_errors", data); err != nil {
		slog.Error("render app errors", "err", err)
	}
}
