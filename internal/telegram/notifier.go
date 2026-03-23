package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Notifier sends messages to a Telegram chat via the Bot API.
// A nil Notifier is valid and all methods are no-ops.
type Notifier struct {
	token  string
	chatID string
	client *http.Client
}

// NewNotifier creates a Notifier. Returns nil if either token or chatID is empty,
// which disables all notifications silently.
func NewNotifier(token, chatID string) *Notifier {
	if token == "" || chatID == "" {
		return nil
	}
	return &Notifier{
		token:  token,
		chatID: chatID,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

type sendMessageRequest struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

// Send posts a text message to the configured Telegram chat.
// If the Notifier is nil, Send is a no-op.
// Errors are returned but are non-fatal; callers should log them if desired.
func (n *Notifier) Send(ctx context.Context, message string) error {
	if n == nil {
		return nil
	}

	body, err := json.Marshal(sendMessageRequest{ChatID: n.chatID, Text: message})
	if err != nil {
		return fmt.Errorf("telegram: marshalling request: %w", err)
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", n.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: sending message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: API responded with %s", resp.Status)
	}
	return nil
}
