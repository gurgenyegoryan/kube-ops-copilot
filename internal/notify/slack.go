package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type SlackWebhook struct {
	URL  string
	HTTP *http.Client
}

func (s SlackWebhook) Send(ctx context.Context, msg Message) error {
	if strings.TrimSpace(s.URL) == "" {
		return fmt.Errorf("slack webhook url is empty")
	}
	hc := s.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	payload := map[string]any{
		"text": fmt.Sprintf("*%s*\n%s", msg.Title, msg.Body),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
