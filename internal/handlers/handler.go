package handlers

import (
	"database/sql"
	"log/slog"
	"net/http"

	db "github.com/exploded/monitor/db/sqlc"
	"github.com/exploded/monitor/internal/caddy"
	"github.com/exploded/monitor/internal/config"
	"github.com/exploded/monitor/internal/watcher"
)

// Handler holds shared dependencies for all HTTP handlers.
type Handler struct {
	q       *db.Queries
	rawDB   *sql.DB
	pages   PageTemplates
	hub     *Hub
	matcher *watcher.BotMatcher
	caddy   *caddy.Client
	cfg     *config.Config
}

// New creates a Handler with all dependencies.
func New(rawDB *sql.DB, q *db.Queries, pages PageTemplates, hub *Hub, matcher *watcher.BotMatcher, caddyClient *caddy.Client, cfg *config.Config) *Handler {
	return &Handler{
		q:       q,
		rawDB:   rawDB,
		pages:   pages,
		hub:     hub,
		matcher: matcher,
		caddy:   caddyClient,
		cfg:     cfg,
	}
}

// PageData is the standard data envelope for all templates.
type PageData struct {
	Title string
	Extra map[string]any
}

// isHTMX returns true when the request originated from HTMX.
func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// render renders a page template for full requests, or a named fragment for HTMX.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, pageName, fragment string, data PageData) {
	tmpl, ok := h.pages[pageName]
	if !ok {
		slog.Error("template not found", "page", pageName)
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}

	name := "base"
	if isHTMX(r) && fragment != "" {
		name = fragment
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("render template", "page", pageName, "name", name, "err", err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}
