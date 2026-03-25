package handlers

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	db "github.com/exploded/monitor/db/sqlc"
)

// ListBlockedIPs renders the blocked IPs panel.
func (h *Handler) ListBlockedIPs(w http.ResponseWriter, r *http.Request) {
	ips, _ := h.q.ListBlockedIPs(r.Context())
	h.renderIPList(w, ips)
}

// BlockIP adds an IP to the blocklist.
func (h *Handler) BlockIP(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.FormValue("ip"))
	if ip == "" {
		http.Error(w, "ip required", http.StatusBadRequest)
		return
	}

	reason := strings.TrimSpace(r.FormValue("reason"))
	if reason == "" {
		reason = "manually blocked"
	}

	if err := h.q.CreateBlockedIP(r.Context(), db.CreateBlockedIPParams{
		Ip:     ip,
		Reason: reason,
	}); err != nil {
		slog.Error("block ip", "err", err)
		http.Error(w, "failed to block IP", http.StatusInternalServerError)
		return
	}

	ips, _ := h.q.ListBlockedIPs(r.Context())
	h.syncBlockedIPs(ips)
	h.renderIPList(w, ips)
}

// UnblockIP removes an IP from the blocklist.
func (h *Handler) UnblockIP(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := h.q.DeleteBlockedIP(r.Context(), id); err != nil {
		slog.Error("unblock ip", "err", err)
		http.Error(w, "failed to unblock", http.StatusInternalServerError)
		return
	}

	ips, _ := h.q.ListBlockedIPs(r.Context())
	h.syncBlockedIPs(ips)
	h.renderIPList(w, ips)
}

func (h *Handler) syncBlockedIPs(ips []db.BlockedIp) {
	if h.caddy == nil {
		return
	}
	list := make([]string, len(ips))
	for i, ip := range ips {
		list[i] = ip.Ip
	}
	if err := h.caddy.SyncBlockedIPs(list); err != nil {
		slog.Error("sync blocked IPs to caddy", "err", err)
	}
}

func (h *Handler) renderIPList(w http.ResponseWriter, ips []db.BlockedIp) {
	tmpl, ok := h.pages["dashboard"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{"BlockedIPs": ips}}
	if err := tmpl.ExecuteTemplate(w, "_ip_list", data); err != nil {
		slog.Error("render ip list", "err", err)
	}
}
