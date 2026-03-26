package handlers

import (
	"log/slog"
	"net/http"
	"strconv"
)

// AnomaliesPanel renders the recent anomalies partial.
func (h *Handler) AnomaliesPanel(w http.ResponseWriter, r *http.Request) {
	anomalies, _ := h.q.RecentAnomalies(r.Context(), 20)
	tmpl, ok := h.pages["dashboard"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{"Anomalies": anomalies}}
	if err := tmpl.ExecuteTemplate(w, "_anomalies", data); err != nil {
		slog.Error("render anomalies", "err", err)
	}
}

// AcknowledgeAnomaly marks an anomaly as acknowledged.
func (h *Handler) AcknowledgeAnomaly(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	h.q.AcknowledgeAnomaly(r.Context(), id)

	anomalies, _ := h.q.RecentAnomalies(r.Context(), 20)
	tmpl, ok := h.pages["dashboard"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{"Anomalies": anomalies}}
	if err := tmpl.ExecuteTemplate(w, "_anomalies", data); err != nil {
		slog.Error("render anomalies", "err", err)
	}
}
