package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // embed timezone data for static binary

	db "github.com/exploded/monitor/db/sqlc"
	"github.com/exploded/monitor/internal/alerts"
	"github.com/exploded/monitor/internal/anomaly"
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

	// Prune auto-blocked IPs older than 48 hours on startup
	if res, err := q.PruneAutoBlockedIPs(context.Background(), time.Now().Add(-48*time.Hour)); err != nil {
		slog.Error("prune auto-blocked IPs", "err", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("pruned stale auto-blocked IPs", "count", n)
	}

	// Load templates
	pages, err := handlers.LoadTemplates("web/templates")
	if err != nil {
		slog.Error("load templates", "err", err)
		os.Exit(1)
	}


	// Bot matcher
	matcher := watcher.NewBotMatcher()
	botPatterns, err := q.ListBotPatterns(context.Background())
	if err != nil {
		slog.Error("load bot patterns", "err", err)
		os.Exit(1)
	}
	matcher.Load(botPatterns)

	// Auto-blocker (blocks IPs by request path patterns)
	autoBlocker := watcher.NewAutoBlocker(q)
	abRules, err := q.ListEnabledAutoblockRules(context.Background())
	if err != nil {
		slog.Error("load autoblock rules", "err", err)
		os.Exit(1)
	}
	autoBlocker.Load(abRules)
	slog.Info("autoblocker loaded", "rules", len(abRules))

	// Honeypot checker (blocks IPs by honeypot path patterns)
	honeypotChecker := watcher.NewHoneypotChecker(q)
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

	h := handlers.New(sqlDB, q, pages, matcher, autoBlocker, honeypotChecker, alertEngine, &cfg)

	// Graceful shutdown context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start log watcher (if log path configured)
	if cfg.LogPath != "" {
		w := watcher.New(cfg.LogPath, sqlDB, q, matcher, autoBlocker, honeypotChecker, geoResolver, cfg.IgnoreHosts, cfg.IgnoreUserAgents)
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
				// Prune auto-blocked IPs older than 48 hours
				res, err := q.PruneAutoBlockedIPs(context.Background(), time.Now().Add(-48*time.Hour))
				if err != nil {
					slog.Error("prune auto-blocked IPs", "err", err)
					continue
				}
				if n, _ := res.RowsAffected(); n > 0 {
					slog.Info("pruned stale auto-blocked IPs", "count", n)
					// Reset dedup maps so returning IPs can be re-blocked
					autoBlocker.ResetDedup()
					honeypotChecker.ResetDedup()
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
	mux.HandleFunc("GET /partials/uptime", h.UptimeChart)
	mux.HandleFunc("GET /search", h.Search)
	mux.HandleFunc("GET /export/search", h.ExportSearch)

	// Uptime
	mux.HandleFunc("GET /uptime", h.Uptime)
	mux.HandleFunc("POST /uptime", h.CreateUptimeTarget)
	mux.HandleFunc("POST /uptime/{id}/toggle", h.ToggleUptimeTarget)
	mux.HandleFunc("GET /uptime/{id}", h.UptimeDetail)
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
	handler = handlers.SecurityHeaders(handler, cfg.Prod)
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
