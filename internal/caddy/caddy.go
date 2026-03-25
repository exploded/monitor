package caddy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Client communicates with the Caddy admin API to manage IP/UA blocking.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a Caddy admin API client.
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// Healthy checks if the Caddy admin API is reachable.
func (c *Client) Healthy() bool {
	resp, err := c.httpClient.Get(c.baseURL + "/config/")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// SyncBlockedIPs pushes the full list of blocked IPs to Caddy by updating
// the named route's remote_ip matcher. If the list is empty, it removes
// the matcher so no IPs are blocked.
//
// This expects a Caddy JSON config with a route at index 0 of the first
// server that has a "remote_ip" matcher. The config shape:
//
//	apps.http.servers.srv0.routes[0].match[0].remote_ip.ranges = [...]
//
// If the path doesn't exist yet, we create the blocking route.
func (c *Client) SyncBlockedIPs(ips []string) error {
	if len(ips) == 0 {
		// Remove the block route if no IPs to block
		return c.deleteBlockRoute()
	}

	route := map[string]any{
		"match": []map[string]any{
			{
				"remote_ip": map[string]any{
					"ranges": ips,
				},
			},
		},
		"handle": []map[string]any{
			{
				"handler":     "static_response",
				"status_code": "403",
				"body":        "Forbidden",
			},
		},
	}

	body, err := json.Marshal(route)
	if err != nil {
		return fmt.Errorf("marshal block route: %w", err)
	}

	// Try to PATCH the first route; if it fails, POST it
	url := c.baseURL + "/config/apps/http/servers/srv0/routes/0"
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		slog.Warn("caddy sync blocked IPs failed", "err", err)
		return fmt.Errorf("patch block route: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		slog.Warn("caddy sync blocked IPs", "status", resp.StatusCode, "body", string(respBody))
		return fmt.Errorf("caddy API returned %d", resp.StatusCode)
	}

	slog.Info("caddy synced blocked IPs", "count", len(ips))
	return nil
}

// SyncBlockedUAs pushes the full list of blocked user agent patterns to Caddy.
// Uses a header matcher on the User-Agent header with a regex pattern.
func (c *Client) SyncBlockedUAs(patterns []string) error {
	if len(patterns) == 0 {
		return nil
	}

	// Build a regex that matches any of the patterns
	// e.g. "(?i)(AhrefsBot|SemrushBot|MJ12bot)"
	regex := "(?i)("
	for i, p := range patterns {
		if i > 0 {
			regex += "|"
		}
		regex += p
	}
	regex += ")"

	route := map[string]any{
		"match": []map[string]any{
			{
				"header_regexp": map[string]any{
					"User-Agent": map[string]any{
						"pattern": regex,
					},
				},
			},
		},
		"handle": []map[string]any{
			{
				"handler":     "static_response",
				"status_code": "403",
				"body":        "Forbidden",
			},
		},
	}

	body, err := json.Marshal(route)
	if err != nil {
		return fmt.Errorf("marshal UA block route: %w", err)
	}

	url := c.baseURL + "/config/apps/http/servers/srv0/routes/1"
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		slog.Warn("caddy sync blocked UAs failed", "err", err)
		return fmt.Errorf("patch UA block route: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		slog.Warn("caddy sync blocked UAs", "status", resp.StatusCode, "body", string(respBody))
		return fmt.Errorf("caddy API returned %d", resp.StatusCode)
	}

	slog.Info("caddy synced blocked UAs", "count", len(patterns))
	return nil
}

func (c *Client) deleteBlockRoute() error {
	url := c.baseURL + "/config/apps/http/servers/srv0/routes/0"
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil // Caddy may not be running — not fatal
	}
	resp.Body.Close()
	return nil
}
