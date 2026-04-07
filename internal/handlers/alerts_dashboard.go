package handlers

import (
	"net/http"
)

// AlertsDashboard renders the alerts overview page.
func (h *Handler) AlertsDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	alertRules, _ := h.q.ListAlertRules(ctx)
	alertLogs, _ := h.q.RecentAlertLogs(ctx, 20)

	h.render(w, r, "alerts_dashboard", "", PageData{
		Title: "Alerts",
		Extra: map[string]any{
			"AlertRules": alertRules,
			"AlertLogs":  alertLogs,
		},
	})
}
