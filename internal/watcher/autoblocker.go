package watcher

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	db "github.com/exploded/monitor/db/sqlc"
	"github.com/exploded/monitor/internal/caddy"
)

type autoBlockRule struct {
	id          int64
	pattern     string // lowercased
	description string
}

// AutoBlocker checks request URIs against path-based rules and
// automatically blocks matching IPs via the database and Caddy.
type AutoBlocker struct {
	mu      sync.RWMutex
	rules   []autoBlockRule
	blocked map[string]bool // IPs already blocked this session (dedup)
	q       *db.Queries
	caddy   *caddy.Client
}

func NewAutoBlocker(q *db.Queries, caddyClient *caddy.Client) *AutoBlocker {
	return &AutoBlocker{
		blocked: make(map[string]bool),
		q:       q,
		caddy:   caddyClient,
	}
}

// Load rebuilds the rule cache from the database.
func (ab *AutoBlocker) Load(rules []db.ListEnabledAutoblockRulesRow) {
	entries := make([]autoBlockRule, len(rules))
	for i, r := range rules {
		entries[i] = autoBlockRule{
			id:          r.ID,
			pattern:     strings.ToLower(r.Pattern),
			description: r.Description,
		}
	}
	ab.mu.Lock()
	ab.rules = entries
	ab.mu.Unlock()
}

// ResetDedup clears the in-memory dedup map so pruned IPs can be re-blocked.
func (ab *AutoBlocker) ResetDedup() {
	ab.mu.Lock()
	ab.blocked = make(map[string]bool)
	ab.mu.Unlock()
}

// Check tests the URI against enabled autoblock rules. If matched,
// the client IP is added to blocked_ips and synced to Caddy.
func (ab *AutoBlocker) Check(uri, clientIP string) {
	ab.mu.RLock()
	if ab.blocked[clientIP] {
		ab.mu.RUnlock()
		return
	}

	lower := strings.ToLower(uri)
	var matched *autoBlockRule
	for i := range ab.rules {
		if strings.Contains(lower, ab.rules[i].pattern) {
			matched = &ab.rules[i]
			break
		}
	}
	ab.mu.RUnlock()

	if matched == nil {
		return
	}

	// Mark as blocked immediately to avoid duplicate work
	ab.mu.Lock()
	if ab.blocked[clientIP] {
		ab.mu.Unlock()
		return
	}
	ab.blocked[clientIP] = true
	ab.mu.Unlock()

	// Perform the block asynchronously so we don't slow down log processing
	ruleID := matched.id
	reason := "auto-blocked: " + matched.description
	go ab.blockIP(clientIP, reason, ruleID)
}

func (ab *AutoBlocker) blockIP(ip, reason string, ruleID int64) {
	ctx := context.Background()

	if err := ab.q.CreateBlockedIP(ctx, db.CreateBlockedIPParams{
		Ip:     ip,
		Reason: reason,
	}); err != nil {
		slog.Error("autoblocker insert IP", "ip", ip, "err", err)
		return
	}

	// Increment hit counter on the rule
	if err := ab.q.IncrementAutoblockHit(ctx, ruleID); err != nil {
		slog.Error("autoblocker increment hit", "rule_id", ruleID, "err", err)
	}

	slog.Info("autoblocker blocked IP", "ip", ip, "reason", reason)

	// Sync full blocklist to Caddy
	if ab.caddy == nil {
		return
	}
	ips, err := ab.q.ListBlockedIPs(ctx)
	if err != nil {
		slog.Error("autoblocker list IPs", "err", err)
		return
	}
	list := make([]string, len(ips))
	for i, blocked := range ips {
		list[i] = blocked.Ip
	}
	if err := ab.caddy.SyncBlockedIPs(list); err != nil {
		slog.Error("autoblocker sync to caddy", "err", err)
	}
}
