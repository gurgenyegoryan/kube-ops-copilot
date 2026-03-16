package llm

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

type AnthropicClient struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

func (c *AnthropicClient) Complete(ctx context.Context, req Request) (Response, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return Response{}, fmt.Errorf("anthropic api key is required")
	}
	base := c.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	model := req.Model
	if model == "" {
		model = "claude-3-5-sonnet-latest"
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}

	payload := map[string]any{
		"model":      model,
		"max_tokens": 800,
		"system":     req.System,
		"messages": []map[string]string{
			{"role": "user", "content": req.User},
		},
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return Response{}, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/messages", bytes.NewReader(b))
	if err != nil {
		return Response{}, err
	}
	hreq.Header.Set("x-api-key", c.APIKey)
	hreq.Header.Set("anthropic-version", "2023-06-01")
	hreq.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(hreq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("anthropic http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Response{}, err
	}
	for _, c := range parsed.Content {
		if c.Type == "text" {
			return Response{Text: c.Text}, nil
		}
	}
	return Response{}, fmt.Errorf("anthropic: no text content")
}
