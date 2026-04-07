package handlers

import (
	"net/http"
	"time"
)

// AlertsDashboard renders the alerts overview page.
func (h *Handler) AlertsDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since := time.Now().UTC().Add(-24 * time.Hour)

	alertRules, _ := h.q.ListAlertRules(ctx)
	alertLogs, _ := h.q.RecentAlertLogs(ctx, 20)
	appErrors, _ := h.q.RecentAppErrors(ctx, 20)
	appErrorCount, _ := h.q.CountAppErrorsSince(ctx, since)
	appLogCount, _ := h.q.CountAppLogsSince(ctx, since)

	h.render(w, r, "alerts_dashboard", "", PageData{
		Title: "Alerts",
		Extra: map[string]any{
			"AlertRules":    alertRules,
			"AlertLogs":     alertLogs,
			"AppErrors":     appErrors,
			"AppErrorCount": appErrorCount,
			"AppLogCount":   appLogCount,
		},
	})
}
