package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	db "github.com/exploded/monitor/db/sqlc"
)

// ListBots renders the bot patterns panel.
func (h *Handler) ListBots(w http.ResponseWriter, r *http.Request) {
	patterns, _ := h.q.ListBotPatterns(r.Context())
	h.renderBotList(w, patterns)
}

// CreateBot adds a new bot pattern.
func (h *Handler) CreateBot(w http.ResponseWriter, r *http.Request) {
	pattern := strings.TrimSpace(r.FormValue("pattern"))
	label := strings.TrimSpace(r.FormValue("label"))
	if pattern == "" || label == "" {
		http.Error(w, "pattern and label required", http.StatusBadRequest)
		return
	}

	block := int64(0)
	if r.FormValue("block") == "1" {
		block = 1
	}

	if err := h.q.CreateBotPattern(r.Context(), db.CreateBotPatternParams{
		Pattern: pattern,
		Label:   label,
		Block:   block,
	}); err != nil {
		slog.Error("create bot pattern", "err", err)
		http.Error(w, "failed to create pattern", http.StatusInternalServerError)
		return
	}

	h.refreshBotMatcher(r.Context())

	patterns, _ := h.q.ListBotPatterns(r.Context())
	h.renderBotList(w, patterns)
}

// ToggleBotBlock toggles the block flag for a bot pattern.
func (h *Handler) ToggleBotBlock(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := h.q.ToggleBotBlock(r.Context(), id); err != nil {
		slog.Error("toggle bot block", "err", err)
		http.Error(w, "failed to toggle", http.StatusInternalServerError)
		return
	}

	h.refreshBotMatcher(r.Context())

	patterns, _ := h.q.ListBotPatterns(r.Context())
	h.renderBotList(w, patterns)
}

// DeleteBot removes a bot pattern.
func (h *Handler) DeleteBot(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := h.q.DeleteBotPattern(r.Context(), id); err != nil {
		slog.Error("delete bot pattern", "err", err)
		http.Error(w, "failed to delete", http.StatusInternalServerError)
		return
	}

	h.refreshBotMatcher(r.Context())

	patterns, _ := h.q.ListBotPatterns(r.Context())
	h.renderBotList(w, patterns)
}

func (h *Handler) refreshBotMatcher(ctx context.Context) {
	patterns, err := h.q.ListBotPatterns(ctx)
	if err != nil {
		slog.Error("refresh bot matcher", "err", err)
		return
	}
	h.matcher.Load(patterns)
}

func (h *Handler) renderBotList(w http.ResponseWriter, patterns []db.BotPattern) {
	tmpl, ok := h.pages["dashboard"]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := PageData{Extra: map[string]any{"BotPatterns": patterns}}
	if err := tmpl.ExecuteTemplate(w, "_bot_list", data); err != nil {
		slog.Error("render bot list", "err", err)
	}
}
