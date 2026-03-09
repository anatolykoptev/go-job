package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// vaelorToolRequest is the request body for vaelor's message tool API.
type vaelorToolRequest struct {
	Content string `json:"content"`
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

// SendTelegramNotification sends a message via vaelor's message tool.
func SendTelegramNotification(ctx context.Context, message string) error {
	baseURL := engine.Cfg.VaelorNotifyURL
	if baseURL == "" {
		return fmt.Errorf("VAELOR_NOTIFY_URL not configured")
	}
	chatID := engine.Cfg.BountyNotifyChatID
	if chatID == "" {
		chatID = "428660"
	}

	payload, _ := json.Marshal(vaelorToolRequest{
		Content: message,
		Channel: "telegram",
		ChatID:  chatID,
	})

	url := baseURL + "/api/tools/message"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("vaelor notify failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("vaelor notify status %d: %s", resp.StatusCode, body)
	}
	return nil
}
