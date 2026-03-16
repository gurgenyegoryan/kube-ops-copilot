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

type OllamaClient struct {
	BaseURL string
	HTTP    *http.Client
}

func (c *OllamaClient) Complete(ctx context.Context, req Request) (Response, error) {
	base := c.BaseURL
	if base == "" {
		base = "http://localhost:11434"
	}
	model := req.Model
	if model == "" {
		model = "llama3.1"
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}

	prompt := req.System + "\n\n" + req.User
	payload := map[string]any{
		"model":  model,
		"prompt": prompt,
		"stream": false,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return Response{}, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/api/generate", bytes.NewReader(b))
	if err != nil {
		return Response{}, err
	}
	hreq.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(hreq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("ollama http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Response{}, err
	}
	return Response{Text: parsed.Response}, nil
}
