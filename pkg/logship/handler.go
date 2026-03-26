// Package logship provides a slog.Handler that ships log records to the
// monitor portal's POST /api/logs endpoint. It buffers records and sends
// them in batches.
//
// Usage:
//
//	ship := logship.New(logship.Options{
//	    Endpoint: "https://monitor.example.com/api/logs",
//	    APIKey:   os.Getenv("MONITOR_API_KEY"),
//	    App:      "moon",
//	    Level:    slog.LevelWarn,
//	})
//	defer ship.Shutdown()
//
//	logger := slog.New(logship.Multi(
//	    slog.NewTextHandler(os.Stderr, nil),
//	    ship,
//	))
//	slog.SetDefault(logger)
package logship

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

type entry struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"msg"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Source  string         `json:"source,omitempty"`
}

type batch struct {
	App  string  `json:"app"`
	Logs []entry `json:"logs"`
}

// Options configures the logship handler.
type Options struct {
	Endpoint      string        // Full URL, e.g. "https://monitor.example.com/api/logs"
	APIKey        string        // Sent as X-API-Key header
	App           string        // Application name
	BatchSize     int           // Max entries before flush (default: 50)
	FlushInterval time.Duration // Flush interval (default: 5s)
	Level         slog.Leveler  // Minimum level to ship (default: slog.LevelInfo)
}

// shared holds the mutable state shared across all clones of a Handler.
type shared struct {
	mu      sync.Mutex
	buf     []entry
	stopCh  chan struct{}
	stopped bool
}

// Handler is a slog.Handler that buffers records and batch-sends them
// to the monitor portal.
type Handler struct {
	opts   Options
	client *http.Client
	state  *shared
	attrs  []slog.Attr
	groups []string
}

// New creates a logship handler. Call Shutdown() on graceful exit to flush.
func New(opts Options) *Handler {
	if opts.BatchSize <= 0 {
		opts.BatchSize = 50
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = 5 * time.Second
	}
	if opts.Level == nil {
		opts.Level = slog.LevelInfo
	}

	h := &Handler{
		opts:   opts,
		client: &http.Client{Timeout: 10 * time.Second},
		state: &shared{
			buf:    make([]entry, 0, opts.BatchSize),
			stopCh: make(chan struct{}),
		},
	}
	go h.flushLoop()
	return h
}

// Enabled implements slog.Handler.
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.opts.Level.Level()
}

// Handle implements slog.Handler.
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any)

	// Pre-resolved attrs from WithAttrs
	for _, a := range h.attrs {
		key := h.prefixKey(a.Key)
		attrs[key] = a.Value.Any()
	}

	// Record attrs
	r.Attrs(func(a slog.Attr) bool {
		key := h.prefixKey(a.Key)
		attrs[key] = a.Value.Any()
		return true
	})

	source := ""
	if s, ok := attrs["source"]; ok {
		source = fmt.Sprint(s)
		delete(attrs, "source")
	}

	e := entry{
		Time:    r.Time,
		Level:   r.Level.String(),
		Message: r.Message,
		Attrs:   attrs,
		Source:  source,
	}

	h.state.mu.Lock()
	h.state.buf = append(h.state.buf, e)
	shouldFlush := len(h.state.buf) >= h.opts.BatchSize
	h.state.mu.Unlock()

	if shouldFlush {
		h.flush()
	}
	return nil
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h2 := h.clone()
	h2.attrs = append(h2.attrs, attrs...)
	return h2
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h2 := h.clone()
	h2.groups = append(h2.groups, name)
	return h2
}

// Shutdown flushes remaining logs and stops the background goroutine.
func (h *Handler) Shutdown() {
	h.state.mu.Lock()
	if h.state.stopped {
		h.state.mu.Unlock()
		return
	}
	h.state.stopped = true
	h.state.mu.Unlock()
	close(h.state.stopCh)
}

func (h *Handler) prefixKey(key string) string {
	for i := len(h.groups) - 1; i >= 0; i-- {
		key = h.groups[i] + "." + key
	}
	return key
}

func (h *Handler) clone() *Handler {
	return &Handler{
		opts:   h.opts,
		client: h.client,
		state:  h.state,
		attrs:  append([]slog.Attr{}, h.attrs...),
		groups: append([]string{}, h.groups...),
	}
}

func (h *Handler) flushLoop() {
	ticker := time.NewTicker(h.opts.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.state.stopCh:
			h.flush()
			return
		case <-ticker.C:
			h.flush()
		}
	}
}

func (h *Handler) flush() {
	h.state.mu.Lock()
	if len(h.state.buf) == 0 {
		h.state.mu.Unlock()
		return
	}
	entries := h.state.buf
	h.state.buf = make([]entry, 0, h.opts.BatchSize)
	h.state.mu.Unlock()

	payload := batch{App: h.opts.App, Logs: entries}
	body, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logship: marshal error: %v\n", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, h.opts.Endpoint, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "logship: request error: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", h.opts.APIKey)

	resp, err := h.client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logship: send error: %v\n", err)
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		fmt.Fprintf(os.Stderr, "logship: server returned %d\n", resp.StatusCode)
	}
}
