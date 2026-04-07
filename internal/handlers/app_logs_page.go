package handlers

import (
	"net/http"
	"time"
)

// AppLogsPage renders the app logs page.
func (h *Handler) AppLogsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since := time.Now().UTC().Add(-24 * time.Hour)

	appErrors, _ := h.q.RecentAppErrors(ctx, 20)
	appErrorCount, _ := h.q.CountAppErrorsSince(ctx, since)
	appLogCount, _ := h.q.CountAppLogsSince(ctx, since)

	h.render(w, r, "app_logs", "", PageData{
		Title: "App Logs",
		Extra: map[string]any{
			"AppErrors":     appErrors,
			"AppErrorCount": appErrorCount,
			"AppLogCount":   appLogCount,
		},
	})
}
