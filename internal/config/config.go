package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port          string
	DBPath        string
	LogPath       string
	CaddyAdminURL string
	AuthUser      string
	AuthPass      string
	RetentionDays int
}

func Load() Config {
	loadDotEnv(".env")

	retention, _ := strconv.Atoi(envOr("RETENTION_DAYS", "90"))
	if retention <= 0 {
		retention = 90
	}

	return Config{
		Port:          envOr("PORT", "8989"),
		DBPath:        envOr("DB_PATH", "monitor.db"),
		LogPath:       os.Getenv("LOG_PATH"),
		CaddyAdminURL: envOr("CADDY_ADMIN_URL", "http://localhost:2019"),
		AuthUser:      envOr("AUTH_USER", "admin"),
		AuthPass:      os.Getenv("AUTH_PASS"),
		RetentionDays: retention,
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
