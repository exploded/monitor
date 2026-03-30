package watcher

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	db "github.com/exploded/monitor/db/sqlc"
	"github.com/exploded/monitor/internal/caddy"
)

type honeypotRule struct {
	id          int64
	path        string // lowercased
	description string
}

// HoneypotChecker checks request URIs against honeypot paths and
// automatically blocks matching IPs via the database and Caddy.
type HoneypotChecker struct {
	mu      sync.RWMutex
	rules   []honeypotRule
	blocked map[string]bool
	q       *db.Queries
	caddy   *caddy.Client
}

func NewHoneypotChecker(q *db.Queries, caddyClient *caddy.Client) *HoneypotChecker {
	return &HoneypotChecker{
		blocked: make(map[string]bool),
		q:       q,
		caddy:   caddyClient,
	}
}

// Load rebuilds the rule cache from the database.
func (hc *HoneypotChecker) Load(rules []db.ListEnabledHoneypotsRow) {
	entries := make([]honeypotRule, len(rules))
	for i, r := range rules {
		entries[i] = honeypotRule{
			id:          r.ID,
			path:        strings.ToLower(r.Path),
			description: r.Description,
		}
	}
	hc.mu.Lock()
	hc.rules = entries
	hc.mu.Unlock()
}

// ResetDedup clears the in-memory dedup map so pruned IPs can be re-blocked.
func (hc *HoneypotChecker) ResetDedup() {
	hc.mu.Lock()
	hc.blocked = make(map[string]bool)
	hc.mu.Unlock()
}

// Check tests the URI against enabled honeypot paths. If matched,
// the client IP is added to blocked_ips and synced to Caddy.
func (hc *HoneypotChecker) Check(uri, clientIP string) {
	hc.mu.RLock()
	if hc.blocked[clientIP] {
		hc.mu.RUnlock()
		return
	}

	lower := strings.ToLower(uri)
	var matched *honeypotRule
	for i := range hc.rules {
		if strings.Contains(lower, hc.rules[i].path) {
			matched = &hc.rules[i]
			break
		}
	}
	hc.mu.RUnlock()

	if matched == nil {
		return
	}

	hc.mu.Lock()
	if hc.blocked[clientIP] {
		hc.mu.Unlock()
		return
	}
	hc.blocked[clientIP] = true
	hc.mu.Unlock()

	ruleID := matched.id
	reason := "honeypot: " + matched.description
	go hc.blockIP(clientIP, reason, ruleID)
}

func (hc *HoneypotChecker) blockIP(ip, reason string, ruleID int64) {
	ctx := context.Background()

	if err := hc.q.CreateBlockedIP(ctx, db.CreateBlockedIPParams{
		Ip:     ip,
		Reason: reason,
	}); err != nil {
		slog.Error("honeypot insert IP", "ip", ip, "err", err)
		return
	}

	if err := hc.q.IncrementHoneypotHit(ctx, ruleID); err != nil {
		slog.Error("honeypot increment hit", "rule_id", ruleID, "err", err)
	}

	slog.Info("honeypot blocked IP", "ip", ip, "reason", reason)

	if hc.caddy == nil {
		return
	}
	ips, err := hc.q.ListBlockedIPs(ctx)
	if err != nil {
		slog.Error("honeypot list IPs", "err", err)
		return
	}
	list := make([]string, len(ips))
	for i, blocked := range ips {
		list[i] = blocked.Ip
	}
	if err := hc.caddy.SyncBlockedIPs(list); err != nil {
		slog.Error("honeypot sync to caddy", "err", err)
	}
}
