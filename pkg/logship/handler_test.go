package logship

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// resetGrace is how long a test server waits, after writing its response, for a
// connection reset to show up as a cancelled request context.
const resetGrace = 2 * time.Second

// testHandler builds a Handler that flushes synchronously on every record, so a
// test can drive a flush by logging one line. The long FlushInterval keeps the
// background goroutine from firing a second, racing flush mid-test.
func testHandler(endpoint string) *Handler {
	return New(Options{
		Endpoint:      endpoint,
		APIKey:        "test-key",
		App:           "logship-test",
		BatchSize:     1,
		FlushInterval: time.Hour,
		Level:         slog.LevelInfo,
	})
}

// TestFlushDrainsResponseBody guards against a log shipper manufacturing errors
// in the server it ships to. flush used to close the response body without
// reading it, which resets the connection and leaves a broken-pipe entry in
// monitor's own log for a batch that was accepted perfectly happily.
//
// The response has to be far bigger than one segment or the test proves nothing,
// and where the reset surfaces depends on the platform's socket buffers: the
// write itself fails where the server is still blocked in Write, and where the
// buffers swallow the lot it turns up moments later as a cancelled context.
func TestFlushDrainsResponseBody(t *testing.T) {
	const bodySize = 4 << 20 // 4 MiB, far past a single segment

	payload := bytes.Repeat([]byte("logship-drain-test "), bodySize/19)
	serverErr := make(chan error, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // consume the posted batch, as the real endpoint does
		w.WriteHeader(http.StatusOK)

		_, err := w.Write(payload)
		if err == nil {
			// A client that drained leaves the connection open and pooled, so
			// this waits out the full grace period and reports nil.
			select {
			case <-r.Context().Done():
				err = r.Context().Err()
			case <-time.After(resetGrace):
			}
		}
		serverErr <- err
	}))
	defer srv.Close()

	h := testHandler(srv.URL)
	defer h.Shutdown()

	slog.New(h).Info("a log line that triggers a flush")

	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("log endpoint saw the connection fail (%v); flush closed the "+
				"response body without draining it, which resets the connection", err)
		}
	case <-time.After(resetGrace + 30*time.Second):
		t.Fatal("log endpoint never finished writing its response")
	}
}

// TestFlushReusesConnection covers the other half of the same bug. Closing an
// undrained body takes the connection out of the pool, so every batch pays a
// fresh TCP and TLS handshake -- once per flush interval, for the life of every
// process that ships logs here.
func TestFlushReusesConnection(t *testing.T) {
	var mu sync.Mutex
	var conns []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		conns = append(conns, r.RemoteAddr)
		mu.Unlock()
		w.Write(bytes.Repeat([]byte("x"), 64<<10))
	}))
	defer srv.Close()

	h := testHandler(srv.URL)
	defer h.Shutdown()

	log := slog.New(h)
	log.Info("first batch")
	log.Info("second batch")

	mu.Lock()
	defer mu.Unlock()
	if len(conns) != 2 {
		t.Fatalf("endpoint saw %d batches, want 2", len(conns))
	}
	if conns[0] != conns[1] {
		t.Errorf("second batch opened a new connection (%s then %s); the first response body was not drained back into the pool", conns[0], conns[1])
	}
}
