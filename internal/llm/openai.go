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

type OpenAIClient struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

func (c *OpenAIClient) Complete(ctx context.Context, req Request) (Response, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return Response{}, fmt.Errorf("openai api key is required")
	}
	base := c.BaseURL
	if base == "" {
		base = "https://api.openai.com"
	}
	model := req.Model
	if model == "" {
		model = "gpt-4.1-mini"
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}

	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": req.System},
			{"role": "user", "content": req.User},
		},
	}
	if req.Temperature != 0 {
		payload["temperature"] = req.Temperature
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return Response{}, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return Response{}, err
	}
	hreq.Header.Set("Authorization", "Bearer "+c.APIKey)
	hreq.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(hreq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("openai http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Response{}, err
	}
	if len(parsed.Choices) == 0 {
		return Response{}, fmt.Errorf("openai: no choices")
	}
	return Response{Text: parsed.Choices[0].Message.Content}, nil
}
