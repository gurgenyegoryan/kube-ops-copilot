package approval

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

type N8N struct {
	WebhookURL  string
	BearerToken string
	HTTP        *http.Client
}

func (n N8N) Provider() Provider { return ProviderN8N }

type n8nPayload struct {
	Action     string `json:"action"`
	ApprovalID string `json:"approvalId"`
	Summary    string `json:"summary,omitempty"`
	Operation  string `json:"operation,omitempty"`
	Target     string `json:"target,omitempty"`
	PlanPath   string `json:"planPath,omitempty"`
}

type n8nResponse struct {
	ApprovalID string `json:"approvalId"`
	Decision   string `json:"decision"` // approved|denied|pending
	Approver   string `json:"approver,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Raw        any    `json:"raw,omitempty"`
}

func (n N8N) Request(ctx context.Context, req Request) (string, error) {
	if strings.TrimSpace(n.WebhookURL) == "" {
		return "", fmt.Errorf("n8n webhook url is required (set KUBE_OPS_COPILOT_N8N_WEBHOOK_URL)")
	}
	payload := n8nPayload{Action: "request", ApprovalID: req.ApprovalID, Summary: req.Summary, Operation: req.Operation, Target: req.Target, PlanPath: req.PlanPath}
	resp, err := n.call(ctx, payload)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.ApprovalID) == "" {
		return req.ApprovalID, nil
	}
	return resp.ApprovalID, nil
}

func (n N8N) Status(ctx context.Context, approvalID string) (Status, error) {
	if strings.TrimSpace(approvalID) == "" {
		return Status{}, fmt.Errorf("approval id is required")
	}
	if strings.TrimSpace(n.WebhookURL) == "" {
		return Status{}, fmt.Errorf("n8n webhook url is required (set KUBE_OPS_COPILOT_N8N_WEBHOOK_URL)")
	}
	resp, err := n.call(ctx, n8nPayload{Action: "status", ApprovalID: approvalID})
	if err != nil {
		return Status{}, err
	}
	decision := parseDecision(resp.Decision)
	raw := ""
	if resp.Raw != nil {
		b, _ := json.Marshal(resp.Raw)
		raw = string(b)
	}
	return Status{Decision: decision, Approver: resp.Approver, Reason: resp.Reason, ObservedAt: time.Now().UTC(), Raw: raw}, nil
}

func (n N8N) call(ctx context.Context, payload n8nPayload) (n8nResponse, error) {
	hc := n.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return n8nResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.WebhookURL, bytes.NewReader(b))
	if err != nil {
		return n8nResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(n.BearerToken) != "" {
		req.Header.Set("Authorization", "Bearer "+n.BearerToken)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return n8nResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return n8nResponse{}, fmt.Errorf("n8n http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed n8nResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return n8nResponse{}, err
	}
	return parsed, nil
}

func parseDecision(s string) Decision {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "approved", "approve", "yes", "true":
		return DecisionApproved
	case "denied", "deny", "no", "false":
		return DecisionDenied
	case "pending", "wait":
		return DecisionPending
	default:
		return DecisionPending
	}
}
