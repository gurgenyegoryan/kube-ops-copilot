package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestN8NWebhookSend(t *testing.T) {
	var gotMethod, gotAuth string
	var gotPayload n8nNotifyPayload
	n := N8NWebhook{
		URL:         "https://notify.example.test/webhook",
		BearerToken: "secret-token",
		HTTP: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				gotMethod = req.Method
				gotAuth = req.Header.Get("Authorization")
				body, _ := io.ReadAll(req.Body)
				_ = req.Body.Close()
				_ = json.Unmarshal(body, &gotPayload)
				return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
			}),
		},
	}

	if err := n.Send(context.Background(), Message{Title: "Kube Ops Copilot", Body: "approval requested"}); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("unexpected method: %s", gotMethod)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("unexpected authorization header: %q", gotAuth)
	}
	if gotPayload.Action != "notify" || gotPayload.Title != "Kube Ops Copilot" || gotPayload.Body != "approval requested" {
		t.Fatalf("unexpected payload: %+v", gotPayload)
	}
}

func TestTelegramBotSend(t *testing.T) {
	var gotPath string
	var gotPayload map[string]any
	bot := TelegramBot{
		Token:  "test-token",
		ChatID: "12345",
		HTTP: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				gotPath = req.URL.Path
				body, _ := io.ReadAll(req.Body)
				_ = req.Body.Close()
				_ = json.Unmarshal(body, &gotPayload)
				return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
			}),
		},
	}

	if err := bot.Send(context.Background(), Message{Title: "execute", Body: "verified=true"}); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if gotPath != "/bottest-token/sendMessage" {
		t.Fatalf("unexpected telegram path: %s", gotPath)
	}
	if gotPayload["chat_id"] != "12345" {
		t.Fatalf("unexpected chat_id: %+v", gotPayload)
	}
	text, _ := gotPayload["text"].(string)
	if !strings.Contains(text, "execute") || !strings.Contains(text, "verified=true") {
		t.Fatalf("unexpected telegram text: %q", text)
	}
}

func TestSlackWebhookSend(t *testing.T) {
	var gotPayload map[string]any
	webhook := SlackWebhook{
		URL: "https://hooks.slack.example.test/services/test",
		HTTP: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(req.Body)
				_ = req.Body.Close()
				_ = json.Unmarshal(body, &gotPayload)
				return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
			}),
		},
	}

	if err := webhook.Send(context.Background(), Message{Title: "diagnose", Body: "high risk hotspot"}); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	text, _ := gotPayload["text"].(string)
	if !strings.Contains(text, "*diagnose*") || !strings.Contains(text, "high risk hotspot") {
		t.Fatalf("unexpected slack payload text: %q", text)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func jsonResponse(status int, payload any) *http.Response {
	body, _ := json.Marshal(payload)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
}
