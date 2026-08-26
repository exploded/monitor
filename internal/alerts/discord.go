package alerts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// discordClient is shared by every webhook send, so the timeout policy lives in
// one place rather than being restated at the call site.
//
// This is tidiness, not a fix: connection pooling lives in the Transport, and a
// client with a nil Transport uses the shared http.DefaultTransport, so building
// a Client per call pooled connections perfectly well. What did leak connections
// was closing the response body undrained -- see the drain in sendDiscord.
var discordClient = &http.Client{Timeout: 10 * time.Second}

// sendDiscord posts an embed message to a Discord webhook URL.
func sendDiscord(webhookURL, title, description string, color int) {
	if webhookURL == "" {
		return
	}

	payload := map[string]any{
		"embeds": []map[string]any{{
			"title":       title,
			"description": description,
			"color":       color,
			"timestamp":   time.Now().UTC().Format(time.RFC3339),
		}},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("discord marshal", "err", err)
		return
	}

	resp, err := discordClient.Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Error("discord send", "err", err)
		return
	}
	// Drained as well as closed, so the connection goes back to the pool instead
	// of being reset. Discord answers a good webhook with an empty 204, but a 429
	// carries a JSON body explaining the rate limit -- exactly the case where the
	// next alert is close behind and wants the connection.
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode >= 400 {
		slog.Warn("discord webhook error", "status", resp.StatusCode)
	}
}

// Discord embed colors
const (
	ColorRed    = 0xD94A4A // 14240330
	ColorAmber  = 0xD9A84A // 14264394
	ColorGreen  = 0x3AB06A // 3846250
	ColorBlue   = 0x4A90D9 // 4886745
)

// formatRecoveryTitle names the end of a condition. Prefixing the alert title
// would read "Recovered: Service Down", which takes a beat to parse in a phone
// notification — the moment you least want to be parsing anything.
func formatRecoveryTitle(alertType string) string {
	switch alertType {
	case "downtime":
		return "Service Recovered"
	default:
		return "Recovered: " + formatAlertTitle(alertType)
	}
}

func formatAlertTitle(alertType string) string {
	switch alertType {
	case "5xx_spike":
		return "5xx Error Spike"
	case "traffic_surge":
		return "Traffic Surge"
	case "app_error":
		return "Application Errors"
	case "downtime":
		return "Service Down"
	default:
		return fmt.Sprintf("Alert: %s", alertType)
	}
}
