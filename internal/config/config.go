package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// MonitorUserAgent identifies monitor's own outbound requests (currently the
// uptime probes). The watcher always skips requests carrying it, regardless of
// IGNORE_USER_AGENTS, so it must stay distinctive: the previous approach of
// filtering Go's default Go-http-client/x.y silently excluded every Go-written
// scanner from the request log and from bot matching.
// Sibling apps that quieten these probes in their own access logs match on the
// "mchugh-monitor/" prefix.
const MonitorUserAgent = "mchugh-monitor/1.0 (+https://monitor.mchugh.au)"

type Config struct {
	Port                     string
	Prod                     bool
	DBPath                   string
	LogPath                  string
	AuthUser                 string
	AuthPass                 string
	RetentionDays            int // raw `requests` rows
	UptimeRetentionDays      int // raw `uptime_checks` rows
	AppLogRetentionDays      int // app_logs at ERROR
	AppLogNoiseRetentionDays int // app_logs below ERROR
	AnomalyRetentionDays     int
	AlertLogRetentionDays    int
	LogAPIKey                string
	DiscordWebhookURL        string
	GeoIPDBPath              string
	IgnoreHosts              []string
	IgnoreUserAgents         []string
}

func Load() Config {
	loadDotEnv(".env")

	// Raw-event horizons. Nothing in the UI reads `requests` past 30 days (every
	// range selector caps at 7d; DailySummary and DistinctHosts reach 30d), and
	// nothing reads `uptime_checks` past 7 days — history beyond those lives in
	// the daily_stats and uptime_daily rollups instead.
	prod := strings.EqualFold(os.Getenv("PROD"), "true")

	return Config{
		Port:                     envOr("PORT", "8989"),
		Prod:                     prod,
		DBPath:                   envOr("DB_PATH", "monitor.db"),
		LogPath:                  os.Getenv("LOG_PATH"),
		AuthUser:                 envOr("AUTH_USER", "admin"),
		AuthPass:                 os.Getenv("AUTH_PASS"),
		RetentionDays:            envDays("RETENTION_DAYS", 30),
		UptimeRetentionDays:      envDays("UPTIME_RETENTION_DAYS", 14),
		AppLogRetentionDays:      envDays("APP_LOG_RETENTION_DAYS", 90),
		AppLogNoiseRetentionDays: envDays("APP_LOG_NOISE_RETENTION_DAYS", 14),
		AnomalyRetentionDays:     envDays("ANOMALY_RETENTION_DAYS", 90),
		AlertLogRetentionDays:    envDays("ALERT_LOG_RETENTION_DAYS", 180),
		LogAPIKey:                os.Getenv("LOG_API_KEY"),
		DiscordWebhookURL:        os.Getenv("DISCORD_WEBHOOK_URL"),
		// Absolute by default: a CWD-relative path resolved into /var/www/monitor,
		// which the deploy script chowns and rewrites. The .mmdb is data, not code.
		GeoIPDBPath:      envOr("GEOIP_DB_PATH", "/var/lib/GeoIP/GeoLite2-City.mmdb"),
		IgnoreHosts:      parseList(os.Getenv("IGNORE_HOSTS")),
		IgnoreUserAgents: parseList(os.Getenv("IGNORE_USER_AGENTS")),
	}
}

// envDays reads a positive day count, falling back on empty, unparseable or
// non-positive values. A zero or negative horizon would delete everything.
func envDays(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
}
