package watcher

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"html/template"
	"io"
	"log/slog"
	"math"
	"os"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
	"github.com/exploded/monitor/internal/geoip"
)

// Broadcaster sends data to connected SSE clients.
type Broadcaster interface {
	Broadcast(data string)
}

// CaddyLogEntry matches Caddy's JSON structured access log format.
type CaddyLogEntry struct {
	TS      float64 `json:"ts"`
	Request struct {
		ClientIP string              `json:"client_ip"`
		Method   string              `json:"method"`
		Host     string              `json:"host"`
		URI      string              `json:"uri"`
		Headers  map[string][]string `json:"headers"`
	} `json:"request"`
	Status   int     `json:"status"`
	Size     int     `json:"size"`
	Duration float64 `json:"duration"` // seconds
}

// Watcher tails a Caddy access log file, parses JSON entries,
// and writes them to SQLite while broadcasting to SSE clients.
type Watcher struct {
	logPath          string
	rawDB            *sql.DB
	q                *db.Queries
	hub              Broadcaster
	matcher          *BotMatcher
	autoBlocker      *AutoBlocker
	honeypotChecker  *HoneypotChecker
	geo              *geoip.Resolver
	rowTmpl          *template.Template
	ingestCh         chan db.InsertRequestParams
}

// New creates a Watcher. The rowTmpl is used to render live log HTML for SSE.
func New(logPath string, rawDB *sql.DB, q *db.Queries, hub Broadcaster, matcher *BotMatcher, autoBlocker *AutoBlocker, honeypotChecker *HoneypotChecker, geo *geoip.Resolver, rowTmpl *template.Template) *Watcher {
	return &Watcher{
		logPath:         logPath,
		rawDB:           rawDB,
		q:               q,
		hub:             hub,
		matcher:         matcher,
		autoBlocker:     autoBlocker,
		honeypotChecker: honeypotChecker,
		geo:             geo,
		rowTmpl:         rowTmpl,
		ingestCh:        make(chan db.InsertRequestParams, 256),
	}
}

// Run starts the log watcher. It blocks until the context is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	f, err := os.Open(w.logPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Seek to end — only process new lines
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}

	go w.batchWriter(ctx)

	reader := bufio.NewReader(f)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				slog.Error("watcher read error", "err", err)
				return err
			}
			// No new data — check for rotation then sleep
			if w.fileRotated(f) {
				f.Close()
				f, err = os.Open(w.logPath)
				if err != nil {
					slog.Error("watcher reopen failed", "err", err)
					time.Sleep(time.Second)
					continue
				}
				reader = bufio.NewReader(f)
				slog.Info("watcher reopened log file (rotation detected)")
				continue
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}

		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		w.processLine(line)
	}
}

func (w *Watcher) processLine(line []byte) {
	var entry CaddyLogEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		slog.Debug("watcher skip malformed line", "err", err)
		return
	}

	// Skip non-request log entries
	if entry.Request.Host == "" {
		return
	}

	// Extract user agent
	ua := ""
	if agents, ok := entry.Request.Headers["User-Agent"]; ok && len(agents) > 0 {
		ua = agents[0]
	}

	// Convert timestamp
	sec, frac := math.Modf(entry.TS)
	ts := time.Unix(int64(sec), int64(frac*1e9)).UTC()

	// Check bot
	isBot, _ := w.matcher.Match(ua)
	var isBotInt int64
	if isBot {
		isBotInt = 1
	}

	// Check auto-block rules against the URI
	if w.autoBlocker != nil {
		w.autoBlocker.Check(entry.Request.URI, entry.Request.ClientIP)
	}

	// Check honeypot paths against the URI
	if w.honeypotChecker != nil {
		w.honeypotChecker.Check(entry.Request.URI, entry.Request.ClientIP)
	}

	// GeoIP lookup
	country, city := w.geo.Lookup(entry.Request.ClientIP)

	params := db.InsertRequestParams{
		Ts:         ts,
		Host:       entry.Request.Host,
		ClientIp:   entry.Request.ClientIP,
		Method:     entry.Request.Method,
		Uri:        entry.Request.URI,
		Status:     int64(entry.Status),
		Size:       int64(entry.Size),
		UserAgent:  ua,
		DurationMs: entry.Duration * 1000,
		IsBot:      isBotInt,
		Country:    country,
		City:       city,
	}

	// Send to batch writer
	select {
	case w.ingestCh <- params:
	default:
		slog.Warn("watcher ingest channel full, dropping entry")
	}

	// Broadcast to SSE clients
	w.broadcastRow(params)
}

func (w *Watcher) broadcastRow(p db.InsertRequestParams) {
	if w.rowTmpl == nil || w.hub == nil {
		return
	}

	var buf bytes.Buffer
	if err := w.rowTmpl.ExecuteTemplate(&buf, "_live_log_row", p); err != nil {
		slog.Error("watcher render row", "err", err)
		return
	}
	w.hub.Broadcast(buf.String())
}

func (w *Watcher) batchWriter(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	batch := make([]db.InsertRequestParams, 0, 100)

	for {
		select {
		case <-ctx.Done():
			w.flushBatch(batch)
			return
		case entry := <-w.ingestCh:
			batch = append(batch, entry)
			if len(batch) >= 100 {
				w.flushBatch(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				w.flushBatch(batch)
				batch = batch[:0]
			}
		}
	}
}

func (w *Watcher) flushBatch(batch []db.InsertRequestParams) {
	if len(batch) == 0 {
		return
	}

	tx, err := w.rawDB.Begin()
	if err != nil {
		slog.Error("watcher begin tx", "err", err)
		return
	}

	q := w.q.WithTx(tx)
	ctx := context.Background()

	for _, p := range batch {
		if err := q.InsertRequest(ctx, p); err != nil {
			slog.Error("watcher insert", "err", err)
			tx.Rollback()
			return
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Error("watcher commit", "err", err)
	}
}

func (w *Watcher) fileRotated(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return true
	}

	pathFi, err := os.Stat(w.logPath)
	if err != nil {
		return false
	}

	// Different file (inode changed after rotation)
	if !os.SameFile(fi, pathFi) {
		return true
	}

	// File was truncated
	pos, _ := f.Seek(0, io.SeekCurrent)
	if pathFi.Size() < pos {
		return true
	}

	return false
}
