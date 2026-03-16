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

type N8NWebhook struct {
	URL         string
	BearerToken string
	HTTP        *http.Client
}

type n8nNotifyPayload struct {
	Action string `json:"action"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
}

func (n N8NWebhook) Send(ctx context.Context, msg Message) error {
	if strings.TrimSpace(n.URL) == "" {
		return fmt.Errorf("n8n webhook url is empty")
	}
	hc := n.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}

	payload := n8nNotifyPayload{Action: "notify", Title: strings.TrimSpace(msg.Title), Body: strings.TrimSpace(msg.Body)}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(n.BearerToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(n.BearerToken))
	}

	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("n8n http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
