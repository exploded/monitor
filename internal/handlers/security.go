package handlers

import (
	"net/http"
)

// Security renders the security management page.
func (h *Handler) Security(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	botPatterns, _ := h.q.ListBotPatterns(ctx)
	blockedIPs, _ := h.q.ListBlockedIPs(ctx)
	autoblockRules, _ := h.q.ListAutoblockRules(ctx)
	honeypots, _ := h.q.ListHoneypots(ctx)
	anomalies, _ := h.q.RecentAnomalies(ctx, 20)

	h.render(w, r, "security", "", PageData{
		Title: "Security",
		Extra: map[string]any{
			"BotPatterns":    botPatterns,
			"BlockedIPs":     blockedIPs,
			"AutoblockRules": autoblockRules,
			"Honeypots":      honeypots,
			"Anomalies":      anomalies,
		},
	})
}
