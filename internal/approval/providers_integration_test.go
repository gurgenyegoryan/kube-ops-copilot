package approval

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestManualProviderRequestAndStatus(t *testing.T) {
	m := Manual{}
	id, err := m.Request(context.Background(), Request{ApprovalID: "approval-1"})
	if err != nil {
		t.Fatalf("Request returned error: %v", err)
	}
	if id != "approval-1" {
		t.Fatalf("unexpected approval id: %s", id)
	}
	st, err := m.Status(context.Background(), "approval-1")
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if st.Decision != DecisionApproved {
		t.Fatalf("expected approved decision, got %+v", st)
	}
}

func TestN8NProviderRequestAndStatus(t *testing.T) {
	var seenActions []string
	n := N8N{
		WebhookURL:  "https://approval.example.test/n8n",
		BearerToken: "secret",
		HTTP: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if got := req.Header.Get("Authorization"); got != "Bearer secret" {
					t.Fatalf("unexpected auth header: %q", got)
				}
				body, _ := io.ReadAll(req.Body)
				_ = req.Body.Close()
				payload := n8nPayload{}
				_ = json.Unmarshal(body, &payload)
				seenActions = append(seenActions, payload.Action)
				switch payload.Action {
				case "request":
					return jsonResponse(http.StatusOK, map[string]any{"approvalId": payload.ApprovalID, "decision": "pending"}), nil
				case "status":
					return jsonResponse(http.StatusOK, map[string]any{"approvalId": payload.ApprovalID, "decision": "approved", "approver": "n8n-bot", "reason": "approved in workflow"}), nil
				default:
					t.Fatalf("unexpected action: %s", payload.Action)
					return nil, nil
				}
			}),
		},
	}

	id, err := n.Request(context.Background(), Request{ApprovalID: "approval-2", Summary: "scale deployment"})
	if err != nil {
		t.Fatalf("Request returned error: %v", err)
	}
	if id != "approval-2" {
		t.Fatalf("unexpected approval id: %s", id)
	}
	st, err := n.Status(context.Background(), "approval-2")
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if st.Decision != DecisionApproved || st.Approver != "n8n-bot" {
		t.Fatalf("unexpected n8n status: %+v", st)
	}
	if strings.Join(seenActions, ",") != "request,status" {
		t.Fatalf("unexpected n8n action sequence: %v", seenActions)
	}
}

func TestTelegramProviderRequestAndStatus(t *testing.T) {
	var sentHTML string
	tg := Telegram{
		Token:  "tg-token",
		ChatID: "12345",
		HTTP: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(req.URL.Path, "/sendMessage"):
					body, _ := io.ReadAll(req.Body)
					_ = req.Body.Close()
					payload := map[string]any{}
					_ = json.Unmarshal(body, &payload)
					sentHTML, _ = payload["text"].(string)
					return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
				case strings.HasSuffix(req.URL.Path, "/getUpdates"):
					return jsonResponse(http.StatusOK, map[string]any{
						"ok": true,
						"result": []any{
							map[string]any{
								"update_id": 1,
								"message": map[string]any{
									"text": "approve approval-3",
									"from": map[string]any{"username": "operator"},
									"chat": map[string]any{"id": 12345},
								},
							},
						},
					}), nil
				default:
					t.Fatalf("unexpected telegram path: %s", req.URL.Path)
					return nil, nil
				}
			}),
		},
	}

	id, err := tg.Request(context.Background(), Request{ApprovalID: "approval-3", Operation: "scale_deployment", Target: "payments/payments-api", Summary: "remove single replica"})
	if err != nil {
		t.Fatalf("Request returned error: %v", err)
	}
	if id != "approval-3" {
		t.Fatalf("unexpected approval id: %s", id)
	}
	if !strings.Contains(sentHTML, "approve approval-3") || !strings.Contains(sentHTML, "deny approval-3") {
		t.Fatalf("unexpected telegram approval text: %q", sentHTML)
	}
	st, err := tg.Status(context.Background(), "approval-3")
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if st.Decision != DecisionApproved || st.Approver != "operator" {
		t.Fatalf("unexpected telegram status: %+v", st)
	}
}

func TestSlackProviderRequestServeHTTPAndStatus(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "approvals.json")
	var webhookPayload map[string]any
	sl := Slack{
		SigningSecret: "signing-secret",
		WebhookURL:    "https://hooks.slack.example.test/services/test",
		StorePath:     storePath,
		PublicBaseURL: "https://copilot.example.test",
		HTTP: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(req.Body)
				_ = req.Body.Close()
				_ = json.Unmarshal(body, &webhookPayload)
				return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
			}),
		},
	}

	id, err := sl.Request(context.Background(), Request{
		ApprovalID: "approval-4",
		Summary:    "scale deployment",
		Details:    "The workload is a single replica behind external traffic.",
		Operation:  "scale_deployment",
		Target:     "payments/payments-api",
	})
	if err != nil {
		t.Fatalf("Request returned error: %v", err)
	}
	if id != "approval-4" {
		t.Fatalf("unexpected approval id: %s", id)
	}
	text, _ := webhookPayload["text"].(string)
	if !strings.Contains(text, "Approval requested") {
		t.Fatalf("unexpected slack webhook payload: %+v", webhookPayload)
	}

	payload := map[string]any{
		"user": map[string]any{"username": "operator"},
		"actions": []any{
			map[string]any{"action_id": "koc_approve", "value": "approval-4"},
		},
	}
	body := "payload=" + url.QueryEscape(string(mustJSON(t, payload)))
	req := httptest.NewRequest(http.MethodPost, "/slack/actions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)
	req.Header.Set("X-Slack-Signature", signSlackRequest("signing-secret", timestamp, []byte(body)))
	rr := httptest.NewRecorder()
	sl.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected Slack callback status: %d body=%s", rr.Code, rr.Body.String())
	}

	st, err := sl.Status(context.Background(), "approval-4")
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if st.Decision != DecisionApproved || st.Approver != "operator" {
		t.Fatalf("unexpected slack status: %+v", st)
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

func mustJSON(t *testing.T, payload any) []byte {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

func signSlackRequest(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + timestamp + ":"))
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}
