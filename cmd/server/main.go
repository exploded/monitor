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

	retention := database.Retention{
		Requests:     cfg.RetentionDays,
		UptimeChecks: cfg.UptimeRetentionDays,
		AppLogError:  cfg.AppLogRetentionDays,
		AppLogNoise:  cfg.AppLogNoiseRetentionDays,
		Anomalies:    cfg.AnomalyRetentionDays,
		AlertLog:     cfg.AlertLogRetentionDays,
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

	// GeoIP resolver (graceful degradation if .mmdb not found)
	geoResolver, _ := geoip.New(cfg.GeoIPDBPath)
	if geoResolver != nil {
		defer geoResolver.Close()
	}

	// Alert engine
	alertEngine := alerts.New(q, cfg.DiscordWebhookURL)

	h := handlers.New(sqlDB, q, pages, matcher, alertEngine, &cfg)

	// Graceful shutdown context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start log watcher (if log path configured)
	if cfg.LogPath != "" {
		w := watcher.New(cfg.LogPath, sqlDB, q, matcher, geoResolver, cfg.IgnoreHosts, cfg.IgnoreUserAgents)
		go func() {
			if err := w.Run(ctx); err != nil && err != context.Canceled {
				slog.Error("watcher stopped", "err", err)
			}
		}()
		slog.Info("watcher started", "path", cfg.LogPath)
	} else {
		slog.Warn("LOG_PATH not set, watcher disabled")
	}

	// Maintenance: once at startup, then hourly.
	//
	// Deliberately not run before ListenAndServe. On a large database the first
	// pass is a full rollup backfill plus a big backlog of batched deletes, and
	// doing that synchronously kept the process alive but not listening — the
	// deploy's is-active check passed while Caddy served 502s for a minute. The
	// batching exists precisely so this can run against a live server.
	// The rollup's catch-up scan is a full table scan, and it is only needed for
	// days the hourly pass missed — which requires the process to have been down.
	// Startup is therefore the only time it can find anything, so it runs once
	// here and the hourly passes skip it.
	firstPass := true
	maintain := func() {
		// Roll up before pruning, always — otherwise a day can be deleted from
		// raw before it has been aggregated.
		if err := database.Rollup(ctx, q, firstPass); err != nil {
			slog.Error("rollup", "err", err)
		}
		firstPass = false
		if err := database.Prune(ctx, q, retention); err != nil {
			slog.Error("prune", "err", err)
		}
	}

	go func() {
		maintain()

		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// ctx, not Background: work in flight stops on SIGTERM rather
				// than deleting through shutdown.
				maintain()
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

	// Requests
	mux.HandleFunc("GET /requests", h.RecentRequestsPage)
	mux.HandleFunc("GET /partials/requests", h.RequestsPartial)

	// Referrers
	mux.HandleFunc("GET /referrers", h.ReferrersPage)
	mux.HandleFunc("GET /partials/referrers", h.ReferrersPartial)

	// Security
	mux.HandleFunc("GET /security", h.Security)

	// Alerts dashboard
	mux.HandleFunc("GET /alerts/dashboard", h.AlertsDashboard)

	// Bot management
	mux.HandleFunc("GET /bots", h.ListBots)
	mux.HandleFunc("POST /bots", h.CreateBot)
	mux.HandleFunc("POST /bots/{id}/delete", h.DeleteBot)

	// Alerts
	mux.HandleFunc("GET /alerts", h.ListAlertRules)
	mux.HandleFunc("POST /alerts", h.CreateAlertRule)
	mux.HandleFunc("POST /alerts/{id}/toggle", h.ToggleAlertRule)
	mux.HandleFunc("POST /alerts/{id}/delete", h.DeleteAlertRule)
	mux.HandleFunc("GET /partials/alert-log", h.AlertLogPanel)

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
	mux.HandleFunc("POST /uptime/{id}", h.UpdateUptimeTarget)
	mux.HandleFunc("POST /uptime/{id}/delete", h.DeleteUptimeTarget)

	// Anomalies
	mux.HandleFunc("GET /partials/anomalies", h.AnomaliesPanel)
	mux.HandleFunc("POST /anomalies/{id}/acknowledge", h.AcknowledgeAnomaly)

	// App logs page
	mux.HandleFunc("GET /app-logs", h.AppLogsPage)

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
