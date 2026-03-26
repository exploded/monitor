package alerts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

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

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Error("discord send", "err", err)
		return
	}
	defer resp.Body.Close()

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

func formatAlertTitle(alertType string) string {
	switch alertType {
	case "5xx_spike":
		return "5xx Error Spike"
	case "auto_block":
		return "IP Auto-Blocked"
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
