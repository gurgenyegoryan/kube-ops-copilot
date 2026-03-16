package approval

import (
	"context"
	"time"
)

type Provider string

const (
	ProviderManual   Provider = "manual"
	ProviderN8N      Provider = "n8n"
	ProviderTelegram Provider = "telegram"
	ProviderSlack    Provider = "slack"
)

type Decision string

const (
	DecisionPending  Decision = "pending"
	DecisionApproved Decision = "approved"
	DecisionDenied   Decision = "denied"
)

type Request struct {
	ApprovalID string
	Summary    string
	PlanPath   string
	Operation  string
	Target     string
}

type Status struct {
	Decision   Decision
	Approver   string
	Reason     string
	ObservedAt time.Time
	Raw        string
}

type Approver interface {
	Provider() Provider
	Status(ctx context.Context, approvalID string) (Status, error)
	Request(ctx context.Context, req Request) (string, error)
}
