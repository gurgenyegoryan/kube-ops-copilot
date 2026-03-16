package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type TelegramBot struct {
	Token  string
	ChatID string
	HTTP   *http.Client
}

func (t TelegramBot) Send(ctx context.Context, msg Message) error {
	if strings.TrimSpace(t.Token) == "" || strings.TrimSpace(t.ChatID) == "" {
		return fmt.Errorf("telegram token/chat id is empty")
	}
	hc := t.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}

	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", url.PathEscape(t.Token))
	payload := map[string]any{
		"chat_id":                  t.ChatID,
		"text":                     fmt.Sprintf("%s\n\n%s", msg.Title, msg.Body),
		"disable_web_page_preview": true,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
