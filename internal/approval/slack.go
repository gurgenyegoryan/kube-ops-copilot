package approval

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Slack struct {
	SigningSecret string
	WebhookURL    string
	BotToken      string
	ChannelID     string
	BaseURL       string

	StorePath     string
	PublicBaseURL string
	HTTP          *http.Client
}

func (s Slack) Provider() Provider { return ProviderSlack }

func (s Slack) Request(ctx context.Context, req Request) (string, error) {
	if strings.TrimSpace(req.ApprovalID) == "" {
		return "", fmt.Errorf("approval id is required")
	}
	if strings.TrimSpace(s.StorePath) == "" {
		s.StorePath = defaultStorePath()
	}

	r := Record{
		ApprovalID: req.ApprovalID,
		Provider:   ProviderSlack,
		CreatedAt:  time.Now().UTC(),
		Summary:    req.Summary,
		Operation:  req.Operation,
		Target:     req.Target,
		Decision:   DecisionPending,
	}
	if err := upsertRecord(s.StorePath, r); err != nil {
		return "", err
	}

	// If a webhook is configured, post an interactive message with approve/deny buttons.
	if strings.TrimSpace(s.WebhookURL) != "" {
		details := strings.TrimSpace(req.Details)
		if len(details) > 2500 {
			details = details[:2500] + "\n…(truncated)"
		}

		actionURL := strings.TrimRight(s.PublicBaseURL, "/") + "/slack/actions"
		if strings.TrimSpace(s.PublicBaseURL) == "" {
			actionURL = "(configure KUBE_OPS_COPILOT_PUBLIC_BASE_URL)/slack/actions"
		}

		blocks := []any{
			map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": fmt.Sprintf("*Approval requested* `%s`\n*Operation:* %s\n*Target:* %s", req.ApprovalID, req.Operation, req.Target)}},
		}
		if strings.TrimSpace(req.Summary) != "" {
			blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": fmt.Sprintf("*Summary:* %s", req.Summary)}})
		}
		if details != "" {
			blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": fmt.Sprintf("*Why this is recommended:*\n%s", details)}})
		}
		blocks = append(blocks,
			map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": fmt.Sprintf("Configure Slack Interactivity Request URL to: %s", actionURL)}},
			map[string]any{"type": "actions", "elements": []any{
				map[string]any{"type": "button", "text": map[string]any{"type": "plain_text", "text": "Approve"}, "style": "primary", "action_id": "koc_approve", "value": req.ApprovalID},
				map[string]any{"type": "button", "text": map[string]any{"type": "plain_text", "text": "Deny"}, "style": "danger", "action_id": "koc_deny", "value": req.ApprovalID},
			}},
		)

		payload := map[string]any{
			"text":   fmt.Sprintf("Approval requested: %s", req.Summary),
			"blocks": blocks,
		}
		if err := slackPostWebhook(ctx, s.http(), s.WebhookURL, payload); err != nil {
			return "", err
		}
	}

	return req.ApprovalID, nil
}

func (s Slack) Status(ctx context.Context, approvalID string) (Status, error) {
	_ = ctx
	if strings.TrimSpace(approvalID) == "" {
		return Status{}, fmt.Errorf("approval id is required")
	}
	if strings.TrimSpace(s.StorePath) == "" {
		s.StorePath = defaultStorePath()
	}
	r, ok, err := getRecord(s.StorePath, approvalID)
	if err != nil {
		return Status{}, err
	}
	if !ok {
		return Status{Decision: DecisionPending, ObservedAt: time.Now().UTC(), Reason: "unknown approval id (not requested on this host)"}, nil
	}
	return Status{Decision: r.Decision, Approver: r.DecidedBy, Reason: r.Reason, ObservedAt: time.Now().UTC(), Raw: r.Raw}, nil
}

func (s Slack) http() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func slackPostWebhook(ctx context.Context, hc *http.Client, webhookURL string, payload map[string]any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(b))
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
		return fmt.Errorf("slack webhook http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// ServeHTTP handles Slack interactivity callbacks.
// Configure Slack Interactivity Request URL to: http(s)://<public>/slack/actions
func (s Slack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if strings.TrimSpace(s.SigningSecret) == "" {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("missing signing secret"))
		return
	}
	if strings.TrimSpace(s.StorePath) == "" {
		s.StorePath = defaultStorePath()
	}

	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()

	if !verifySlackSignature(s.SigningSecret, r.Header.Get("X-Slack-Request-Timestamp"), r.Header.Get("X-Slack-Signature"), body) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	vals, err := url.ParseQuery(string(body))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	payload := vals.Get("payload")
	if payload == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var parsed struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
		Actions []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if len(parsed.Actions) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	a := parsed.Actions[0]
	approvalID := strings.TrimSpace(a.Value)
	who := strings.TrimSpace(parsed.User.Username)

	switch a.ActionID {
	case "koc_approve":
		_ = updateDecision(s.StorePath, approvalID, DecisionApproved, who, "slack interactive approval", payload)
	case "koc_deny":
		_ = updateDecision(s.StorePath, approvalID, DecisionDenied, who, "slack interactive denial", payload)
	default:
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"response_type":"ephemeral","text":"Recorded. You can now re-run execute."}`))
}

func verifySlackSignature(signingSecret, timestamp, signature string, body []byte) bool {
	if timestamp == "" || signature == "" {
		return false
	}
	// Reject very old timestamps to reduce replay risk.
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if time.Since(time.Unix(ts, 0)) > 5*time.Minute {
		return false
	}

	base := []byte("v0:" + timestamp + ":" + string(body))
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write(base)
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
