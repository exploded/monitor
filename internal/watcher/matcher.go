package watcher

import (
	"strings"
	"sync"

	db "github.com/exploded/monitor/db/sqlc"
)

type botEntry struct {
	pattern string // lowercased for case-insensitive match
	label   string
	block   bool
}

// BotMatcher checks user agent strings against a cached list of bot patterns.
type BotMatcher struct {
	mu      sync.RWMutex
	entries []botEntry
}

func NewBotMatcher() *BotMatcher {
	return &BotMatcher{}
}

// Load rebuilds the matcher cache from the database bot patterns.
func (m *BotMatcher) Load(patterns []db.BotPattern) {
	entries := make([]botEntry, len(patterns))
	for i, p := range patterns {
		entries[i] = botEntry{
			pattern: strings.ToLower(p.Pattern),
			label:   p.Label,
			block:   p.Block == 1,
		}
	}
	m.mu.Lock()
	m.entries = entries
	m.mu.Unlock()
}

// Match returns whether the user agent matches a known bot pattern.
func (m *BotMatcher) Match(userAgent string) (isBot bool, label string) {
	lower := strings.ToLower(userAgent)
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.entries {
		if strings.Contains(lower, e.pattern) {
			return true, e.label
		}
	}
	return false, ""
}
