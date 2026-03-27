package main

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata" // embed timezone data for static binary

	db "github.com/exploded/monitor/db/sqlc"
	"github.com/exploded/monitor/internal/alerts"
	"github.com/exploded/monitor/internal/anomaly"
	"github.com/exploded/monitor/internal/caddy"
	"github.com/exploded/monitor/internal/config"
	"github.com/exploded/monitor/internal/database"
	"github.com/exploded/monitor/internal/geoip"
	"github.com/exploded/monitor/internal/handlers"
	"github.com/exploded/monitor/internal/uptime"
	"github.com/exploded/monitor/internal/watcher"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := config.Load()

	// Open database
	sqlDB, err := database.Open(cfg.DBPath, "db/schema.sql")
	if err != nil {
		slog.Error("open database", "err", err)
		os.Exit(1)
	}
	defer sqlDB.Close()

	q := db.New(sqlDB)

	// Prune old requests on startup
	if err := database.Prune(context.Background(), q, cfg.RetentionDays); err != nil {
		slog.Error("prune", "err", err)
	}

	// Load templates
	pages, err := handlers.LoadTemplates("web/templates")
	if err != nil {
		slog.Error("load templates", "err", err)
		os.Exit(1)
	}

	// Load the live log row template for the watcher to render SSE events.
	// This needs the same funcMap as the main templates.
	rowTmpl, err := loadRowTemplate()
	if err != nil {
		slog.Error("load row template", "err", err)
		os.Exit(1)
	}

	hub := handlers.NewHub()

	// Bot matcher
	matcher := watcher.NewBotMatcher()
	botPatterns, err := q.ListBotPatterns(context.Background())
	if err != nil {
		slog.Error("load bot patterns", "err", err)
		os.Exit(1)
	}
	matcher.Load(botPatterns)

	// Caddy admin API client
	caddyClient := caddy.New(cfg.CaddyAdminURL)

	// Auto-blocker (blocks IPs by request path patterns)
	autoBlocker := watcher.NewAutoBlocker(q, caddyClient)
	abRules, err := q.ListEnabledAutoblockRules(context.Background())
	if err != nil {
		slog.Error("load autoblock rules", "err", err)
		os.Exit(1)
	}
	autoBlocker.Load(abRules)
	slog.Info("autoblocker loaded", "rules", len(abRules))

	// Honeypot checker (blocks IPs by honeypot path patterns)
	honeypotChecker := watcher.NewHoneypotChecker(q, caddyClient)
	hpRules, err := q.ListEnabledHoneypots(context.Background())
	if err != nil {
		slog.Error("load honeypot rules", "err", err)
		os.Exit(1)
	}
	honeypotChecker.Load(hpRules)
	slog.Info("honeypot checker loaded", "rules", len(hpRules))

	// GeoIP resolver (graceful degradation if .mmdb not found)
	geoResolver, _ := geoip.New(cfg.GeoIPDBPath)
	if geoResolver != nil {
		defer geoResolver.Close()
	}

	// Alert engine
	alertEngine := alerts.New(q, cfg.DiscordWebhookURL)

	h := handlers.New(sqlDB, q, pages, hub, matcher, autoBlocker, honeypotChecker, alertEngine, caddyClient, &cfg)

	// Sync blocked IPs and UAs to Caddy on startup
	if caddyClient != nil {
		blockedIPs, _ := q.ListBlockedIPs(context.Background())
		if len(blockedIPs) > 0 {
			ips := make([]string, len(blockedIPs))
			for i, ip := range blockedIPs {
				ips[i] = ip.Ip
			}
			if err := caddyClient.SyncBlockedIPs(ips); err != nil {
				slog.Warn("startup sync blocked IPs to caddy", "err", err)
			} else {
				slog.Info("synced blocked IPs to caddy", "count", len(ips))
			}
		}

		var blockedUAs []string
		for _, p := range botPatterns {
			if p.Block == 1 {
				blockedUAs = append(blockedUAs, p.Pattern)
			}
		}
		if len(blockedUAs) > 0 {
			if err := caddyClient.SyncBlockedUAs(blockedUAs); err != nil {
				slog.Warn("startup sync blocked UAs to caddy", "err", err)
			} else {
				slog.Info("synced blocked UAs to caddy", "count", len(blockedUAs))
			}
		}
	}

	// Graceful shutdown context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start log watcher (if log path configured)
	if cfg.LogPath != "" {
		w := watcher.New(cfg.LogPath, sqlDB, q, hub, matcher, autoBlocker, honeypotChecker, geoResolver, rowTmpl)
		go func() {
			if err := w.Run(ctx); err != nil && err != context.Canceled {
				slog.Error("watcher stopped", "err", err)
			}
		}()
		slog.Info("watcher started", "path", cfg.LogPath)
	} else {
		slog.Warn("LOG_PATH not set, watcher disabled")
	}

	// Prune ticker — runs hourly
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := database.Prune(context.Background(), q, cfg.RetentionDays); err != nil {
					slog.Error("prune", "err", err)
				}
			}
		}
	}()

	// Start alert engine
	go alertEngine.Run(ctx)
	slog.Info("alert engine started")

	// Start uptime monitor
	uptimeMonitor := uptime.New(q, alertEngine)
	go uptimeMonitor.Run(ctx)
	slog.Info("uptime monitor started")

	// Start anomaly detector
	anomalyDetector := anomaly.New(q, alertEngine)
	go anomalyDetector.Run(ctx)
	slog.Info("anomaly detector started")

	// Routes
	mux := http.NewServeMux()

	// Static files
	mux.Handle("GET /static/", http.StripPrefix("/static/",
		http.FileServer(http.Dir("web/static"))))

	// Favicon redirect (browsers request /favicon.ico at root)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/favicon.ico", http.StatusMovedPermanently)
	})

	// Health check (no auth)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// Dashboard
	mux.HandleFunc("GET /", h.Dashboard)
	mux.HandleFunc("GET /stream", h.LiveLogStream)
	mux.HandleFunc("GET /partials/traffic", h.TrafficOverview)
	mux.HandleFunc("GET /partials/recent", h.RecentRequests)

	// Bot management
	mux.HandleFunc("GET /bots", h.ListBots)
	mux.HandleFunc("POST /bots", h.CreateBot)
	mux.HandleFunc("POST /bots/{id}/toggle", h.ToggleBotBlock)
	mux.HandleFunc("POST /bots/{id}/delete", h.DeleteBot)

	// IP blocklist
	mux.HandleFunc("GET /ips", h.ListBlockedIPs)
	mux.HandleFunc("POST /ips", h.BlockIP)
	mux.HandleFunc("POST /ips/{id}/delete", h.UnblockIP)

	// Auto-block rules
	mux.HandleFunc("GET /autoblock", h.ListAutoblockRules)
	mux.HandleFunc("POST /autoblock", h.CreateAutoblockRule)
	mux.HandleFunc("POST /autoblock/{id}/toggle", h.ToggleAutoblockRule)
	mux.HandleFunc("POST /autoblock/{id}/delete", h.DeleteAutoblockRule)

	// Alerts
	mux.HandleFunc("GET /alerts", h.ListAlertRules)
	mux.HandleFunc("POST /alerts", h.CreateAlertRule)
	mux.HandleFunc("POST /alerts/{id}/toggle", h.ToggleAlertRule)
	mux.HandleFunc("POST /alerts/{id}/delete", h.DeleteAlertRule)
	mux.HandleFunc("GET /partials/alert-log", h.AlertLogPanel)

	// Honeypots
	mux.HandleFunc("GET /honeypots", h.ListHoneypots)
	mux.HandleFunc("POST /honeypots", h.CreateHoneypot)
	mux.HandleFunc("POST /honeypots/{id}/toggle", h.ToggleHoneypot)
	mux.HandleFunc("POST /honeypots/{id}/delete", h.DeleteHoneypot)

	// History
	mux.HandleFunc("GET /history", h.History)
	mux.HandleFunc("GET /partials/hourly", h.HourlyChart)
	mux.HandleFunc("GET /partials/daily", h.DailySummary)
	mux.HandleFunc("GET /partials/latency", h.LatencyChart)
	mux.HandleFunc("GET /partials/bandwidth", h.BandwidthChart)
	mux.HandleFunc("GET /search", h.Search)
	mux.HandleFunc("GET /export/search", h.ExportSearch)

	// Uptime
	mux.HandleFunc("GET /uptime", h.Uptime)
	mux.HandleFunc("POST /uptime", h.CreateUptimeTarget)
	mux.HandleFunc("POST /uptime/{id}/toggle", h.ToggleUptimeTarget)
	mux.HandleFunc("POST /uptime/{id}/delete", h.DeleteUptimeTarget)

	// Anomalies
	mux.HandleFunc("GET /partials/anomalies", h.AnomaliesPanel)
	mux.HandleFunc("POST /anomalies/{id}/acknowledge", h.AcknowledgeAnomaly)

	// App logs (API — uses API key, not basic auth)
	mux.HandleFunc("POST /api/logs", h.IngestAppLogs)
	mux.HandleFunc("GET /partials/app-errors", h.AppErrorsPanel)
	mux.HandleFunc("GET /partials/app-log/{id}", h.AppLogDetail)

	// Middleware stack
	var handler http.Handler = mux
	if cfg.AuthUser != "" && cfg.AuthPass != "" {
		handler = handlers.BasicAuth(handler, cfg.AuthUser, cfg.AuthPass)
	}
	handler = handlers.SecurityHeaders(handler)
	handler = handlers.RequestLogger(handler)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		slog.Info("server starting", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
	slog.Info("server stopped")
}

// loadRowTemplate loads the _live_log_row template with the same funcMap
// used by the main template engine, so the watcher can render SSE HTML.
func loadRowTemplate() (*template.Template, error) {
	mel, err := time.LoadLocation("Australia/Melbourne")
	if err != nil {
		return nil, fmt.Errorf("load timezone: %w", err)
	}
	funcMap := template.FuncMap{
		"statusClass": func(status int64) string {
			switch {
			case status >= 500:
				return "status-5xx"
			case status >= 400:
				return "status-4xx"
			case status >= 300:
				return "status-3xx"
			default:
				return "status-2xx"
			}
		},
		"formatTime": func(t time.Time) string {
			return t.In(mel).Format("15:04:05")
		},
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},
		"statusText": func(status int64) string {
			return http.StatusText(int(status))
		},
	}

	path := filepath.Join("web", "templates", "pages", "_live_log_row.html")
	return template.New("").Funcs(funcMap).ParseFiles(path)
}
