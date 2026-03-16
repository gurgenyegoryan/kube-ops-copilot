package approval

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

type Telegram struct {
	Token string
	ChatID string
	HTTP  *http.Client
}

func (t Telegram) Provider() Provider { return ProviderTelegram }

func (t Telegram) Request(ctx context.Context, req Request) (string, error) {
	if strings.TrimSpace(req.ApprovalID) == "" {
		return "", fmt.Errorf("approval id is required")
	}
	if strings.TrimSpace(t.Token) == "" || strings.TrimSpace(t.ChatID) == "" {
		return "", fmt.Errorf("telegram token/chat id required (set KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN and KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID)")
	}
	msg := fmt.Sprintf("Approval requested: %s\n\nOperation: %s\nTarget: %s\n\nTo approve: reply with `approve %s`\nTo deny: reply with `deny %s`", req.Summary, req.Operation, req.Target, req.ApprovalID, req.ApprovalID)
	if err := telegramSend(ctx, t.http(), t.Token, t.ChatID, msg); err != nil {
		return "", err
	}
	return req.ApprovalID, nil
}

func (t Telegram) Status(ctx context.Context, approvalID string) (Status, error) {
	if strings.TrimSpace(approvalID) == "" {
		return Status{}, fmt.Errorf("approval id is required")
	}
	if strings.TrimSpace(t.Token) == "" || strings.TrimSpace(t.ChatID) == "" {
		return Status{}, fmt.Errorf("telegram token/chat id required (set KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN and KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID)")
	}
	updates, err := telegramGetUpdates(ctx, t.http(), t.Token)
	if err != nil {
		return Status{}, err
	}

	needleApprove := "approve " + approvalID
	needleDeny := "deny " + approvalID

	// Scan newest first
	for i := len(updates) - 1; i >= 0; i-- {
		u := updates[i]
		if u.Message == nil {
			continue
		}
		if fmt.Sprintf("%v", u.Message.Chat.ID) != t.ChatID {
			continue
		}
		text := strings.ToLower(strings.TrimSpace(u.Message.Text))
		switch {
		case strings.Contains(text, needleApprove):
			return Status{Decision: DecisionApproved, Approver: u.Message.From.Username, Reason: "telegram reply approval", ObservedAt: time.Now().UTC(), Raw: u.Message.Text}, nil
		case strings.Contains(text, needleDeny):
			return Status{Decision: DecisionDenied, Approver: u.Message.From.Username, Reason: "telegram reply denial", ObservedAt: time.Now().UTC(), Raw: u.Message.Text}, nil
		}
	}
	return Status{Decision: DecisionPending, ObservedAt: time.Now().UTC(), Reason: "no approval message found yet"}, nil
}

func (t Telegram) http() *http.Client {
	if t.HTTP != nil {
		return t.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

type telegramUpdates struct {
	OK     bool `json:"ok"`
	Result []struct {
		UpdateID int `json:"update_id"`
		Message  *struct {
			MessageID int `json:"message_id"`
			From      struct {
				Username string `json:"username"`
			} `json:"from"`
			Chat struct {
				ID any `json:"id"`
			} `json:"chat"`
			Text string `json:"text"`
		} `json:"message"`
	} `json:"result"`
}

type telegramUpdate struct {
	Message *telegramMessage
}

type telegramMessage struct {
	From struct {
		Username string
	}
	Chat struct {
		ID any
	}
	Text string
}

func telegramGetUpdates(ctx context.Context, hc *http.Client, token string) ([]telegramUpdate, error) {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", url.PathEscape(token))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("telegram http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed telegramUpdates
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	if !parsed.OK {
		return nil, fmt.Errorf("telegram getUpdates returned ok=false")
	}
	out := make([]telegramUpdate, 0, len(parsed.Result))
	for _, r := range parsed.Result {
		if r.Message == nil {
			continue
		}
		m := telegramMessage{Text: r.Message.Text}
		m.From.Username = r.Message.From.Username
		m.Chat.ID = r.Message.Chat.ID
		out = append(out, telegramUpdate{Message: &m})
	}
	return out, nil
}

func telegramSend(ctx context.Context, hc *http.Client, token, chatID, text string) error {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", url.PathEscape(token))
	payload := map[string]any{"chat_id": chatID, "text": text, "disable_web_page_preview": true}
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
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
