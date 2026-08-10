package reputation

import (
	"math"
	"time"
)

// Score holds the computed threat score for an IP.
type Score struct {
	IP      string
	Value   int // 0-100
	Total   int64
	Count4xx int64
	BotCount int64
}

// IPData is the input for scoring an IP.
type IPData struct {
	ClientIP  string
	Total     int64
	Count4xx  int64
	BotCount  int64
	FirstSeen time.Time
	LastSeen  time.Time
}

// Compute calculates a threat score (0-100) for an IP.
// Formula: (4xx_ratio * 40) + (bot_pct * 25) + (velocity * 35)
//
// The weights were rebalanced when the blocklist was retired (blocking moved to
// Cloudflare). The old formula spent 25 points on "is this IP on our blocklist",
// which no longer exists; redistributing them across the three surviving signals
// keeps the 26-point "worth surfacing" cutoff meaningful. A well-behaved crawler
// (bot_pct alone) still scores 25 and stays off the list.
func Compute(d IPData) Score {
	if d.Total == 0 {
		return Score{IP: d.ClientIP}
	}

	ratio4xx := float64(d.Count4xx) / float64(d.Total)
	botPct := float64(d.BotCount) / float64(d.Total)

	// Velocity: requests per minute, normalized to 0-1 (cap at 10 req/min)
	elapsed := d.LastSeen.Sub(d.FirstSeen).Minutes()
	if elapsed < 1 {
		elapsed = 1
	}
	velocity := math.Min(1.0, (float64(d.Total)/elapsed)/10.0)

	raw := ratio4xx*40 + botPct*25 + velocity*35
	value := int(math.Min(100, math.Max(0, raw)))

	return Score{
		IP:       d.ClientIP,
		Value:    value,
		Total:    d.Total,
		Count4xx: d.Count4xx,
		BotCount: d.BotCount,
	}
}

// BadgeClass returns the CSS class for a threat score badge.
func BadgeClass(score int) string {
	switch {
	case score >= 76:
		return "threat-crit"
	case score >= 51:
		return "threat-high"
	case score >= 26:
		return "threat-med"
	default:
		return ""
	}
}
