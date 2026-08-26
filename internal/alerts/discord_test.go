package alerts

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// resetGrace is how long a test server waits, after writing its response, for a
// connection reset to show up as a cancelled request context.
const resetGrace = 2 * time.Second

// TestSendDiscordDrainsResponseBody checks that a webhook send leaves the
// connection intact. Discord answers a good webhook with an empty 204, so the
// bug would never show against Discord itself -- it shows against anything that
// replies at length, such as a proxy error page, and it is the same undrained
// close either way.
func TestSendDiscordDrainsResponseBody(t *testing.T) {
	const bodySize = 4 << 20 // 4 MiB, far past a single segment

	payload := bytes.Repeat([]byte("discord-drain-test "), bodySize/19)
	serverErr := make(chan error, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)

		_, err := w.Write(payload)
		if err == nil {
			select {
			case <-r.Context().Done():
				err = r.Context().Err()
			case <-time.After(resetGrace):
			}
		}
		serverErr <- err
	}))
	defer srv.Close()

	sendDiscord(srv.URL, "Test Alert", "body", ColorRed)

	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("webhook endpoint saw the connection fail (%v); sendDiscord "+
				"closed the response body without draining it", err)
		}
	case <-time.After(resetGrace + 30*time.Second):
		t.Fatal("webhook endpoint never finished writing its response")
	}
}

// TestSendDiscordReusesConnection covers the other half of the undrained close:
// abandoning the connection takes it out of the pool, so a burst of alerts pays
// a TCP and TLS handshake per message to the same host.
//
// Note this does not pin the shared discordClient. It passes either way, because
// a client with a nil Transport uses the shared http.DefaultTransport, so even a
// per-call client pools its connections.
func TestSendDiscordReusesConnection(t *testing.T) {
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

	sendDiscord(srv.URL, "First Alert", "body", ColorRed)
	sendDiscord(srv.URL, "Second Alert", "body", ColorAmber)

	mu.Lock()
	defer mu.Unlock()
	if len(conns) != 2 {
		t.Fatalf("webhook endpoint saw %d sends, want 2", len(conns))
	}
	if conns[0] != conns[1] {
		t.Errorf("second send opened a new connection (%s then %s); the client is not shared, or the first body was not drained", conns[0], conns[1])
	}
}

// TestSendDiscordIgnoresEmptyWebhookURL is the documented no-transport case:
// with DISCORD_WEBHOOK_URL unset there is nowhere to send, and sendDiscord must
// return rather than attempt a request against an empty URL.
func TestSendDiscordIgnoresEmptyWebhookURL(t *testing.T) {
	sendDiscord("", "Test Alert", "body", ColorRed)
}
